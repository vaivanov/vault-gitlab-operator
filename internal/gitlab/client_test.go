package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
)

func newTestClient(t *testing.T, addr, token string, rateLimit float64) *Client {
	t.Helper()
	t.Setenv("VGO_TEST_GITLAB_TOKEN", token)
	c, err := New(config.GitLabConfig{
		URL:       addr,
		TokenEnv:  "VGO_TEST_GITLAB_TOKEN",
		RateLimit: rateLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestResolveTarget(t *testing.T) {
	fake := newFakeGitLab("tok")
	fake.addProject("platform/backend", 42)
	fake.addGroup("platform", 7)
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "tok", 0)

	t.Run("project by path", func(t *testing.T) {
		ref := config.TargetRef{Kind: config.KindProject, Ref: "platform/backend"}
		if err := c.ResolveTarget(t.Context(), &ref); err != nil {
			t.Fatal(err)
		}
		if ref.ID != 42 {
			t.Errorf("ID = %d, want 42", ref.ID)
		}
	})

	t.Run("project by numeric id", func(t *testing.T) {
		ref := config.TargetRef{Kind: config.KindProject, Ref: "42"}
		if err := c.ResolveTarget(t.Context(), &ref); err != nil {
			t.Fatal(err)
		}
		if ref.ID != 42 {
			t.Errorf("ID = %d, want 42", ref.ID)
		}
	})

	t.Run("group by path", func(t *testing.T) {
		ref := config.TargetRef{Kind: config.KindGroup, Ref: "platform"}
		if err := c.ResolveTarget(t.Context(), &ref); err != nil {
			t.Fatal(err)
		}
		if ref.ID != 7 {
			t.Errorf("ID = %d, want 7", ref.ID)
		}
	})

	t.Run("instance needs no resolution", func(t *testing.T) {
		before := fake.resolveCalls
		ref := config.TargetRef{Kind: config.KindInstance}
		if err := c.ResolveTarget(t.Context(), &ref); err != nil {
			t.Fatal(err)
		}
		if fake.resolveCalls != before {
			t.Error("instance resolution hit the API")
		}
	})

	t.Run("unknown project", func(t *testing.T) {
		ref := config.TargetRef{Kind: config.KindProject, Ref: "no/such"}
		if err := c.ResolveTarget(t.Context(), &ref); err == nil {
			t.Fatal("expected error for unknown project")
		}
	})

	t.Run("memoization", func(t *testing.T) {
		before := fake.resolveCalls
		for i := 0; i < 3; i++ {
			ref := config.TargetRef{Kind: config.KindProject, Ref: "platform/backend"}
			if err := c.ResolveTarget(t.Context(), &ref); err != nil {
				t.Fatal(err)
			}
		}
		if got := fake.resolveCalls - before; got != 0 {
			t.Errorf("memoized resolve hit the API %d times", got)
		}
	})
}

func TestListPagination(t *testing.T) {
	fake := newFakeGitLab("tok")
	fake.addProject("a/b", 1)
	for i := 0; i < 250; i++ {
		fake.seed("project:1", fakeVar{
			Key:              fmt.Sprintf("VAR_%03d", i),
			Value:            fmt.Sprintf("value-%d", i),
			VariableType:     "env_var",
			EnvironmentScope: "*",
		})
	}
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "tok", 0)
	ref := config.TargetRef{Kind: config.KindProject, Ref: "a/b", ID: 1}

	vars, err := c.List(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 250 {
		t.Fatalf("listed %d variables, want 250", len(vars))
	}
	if vars[0].Key != "VAR_000" || vars[249].Key != "VAR_249" {
		t.Errorf("order broken: first=%s last=%s", vars[0].Key, vars[249].Key)
	}
	if fake.listCalls != 3 {
		t.Errorf("list requests = %d, want 3 (250 at per_page=100)", fake.listCalls)
	}
}

func TestCreateAtAllLevels(t *testing.T) {
	fake := newFakeGitLab("tok")
	fake.addProject("a/b", 1)
	fake.addGroup("g", 2)
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "tok", 0)

	v := Variable{
		Key:              "DB_PASSWORD",
		Value:            "hunter22hunter22",
		Type:             "file",
		Protected:        true,
		Masked:           true,
		Raw:              false,
		EnvironmentScope: "production",
		Description:      "prod db",
	}

	t.Run("project", func(t *testing.T) {
		ref := config.TargetRef{Kind: config.KindProject, Ref: "a/b", ID: 1}
		if err := c.Create(t.Context(), ref, v); err != nil {
			t.Fatal(err)
		}
		got := fake.get("project:1", "DB_PASSWORD", "production")
		if got == nil {
			t.Fatal("variable not stored")
		}
		if got.VariableType != "file" || !got.Protected || !got.Masked || got.Raw || got.Description != "prod db" {
			t.Errorf("attributes lost: %+v", got)
		}
	})

	t.Run("group", func(t *testing.T) {
		ref := config.TargetRef{Kind: config.KindGroup, Ref: "g", ID: 2}
		if err := c.Create(t.Context(), ref, v); err != nil {
			t.Fatal(err)
		}
		if fake.get("group:2", "DB_PASSWORD", "production") == nil {
			t.Fatal("group variable not stored")
		}
	})

	t.Run("instance drops scope", func(t *testing.T) {
		ref := config.TargetRef{Kind: config.KindInstance}
		iv := v
		iv.EnvironmentScope = ""
		if err := c.Create(t.Context(), ref, iv); err != nil {
			t.Fatal(err)
		}
		if fake.get("instance", "DB_PASSWORD", "") == nil {
			t.Fatal("instance variable not stored")
		}
	})
}

func TestUpdateWithDuplicateKeysAcrossScopes(t *testing.T) {
	fake := newFakeGitLab("tok")
	fake.addProject("a/b", 1)
	fake.seed("project:1", fakeVar{Key: "K", Value: "staging-old", VariableType: "env_var", EnvironmentScope: "staging"})
	fake.seed("project:1", fakeVar{Key: "K", Value: "prod-old", VariableType: "env_var", EnvironmentScope: "production"})
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "tok", 0)
	ref := config.TargetRef{Kind: config.KindProject, Ref: "a/b", ID: 1}

	err := c.Update(t.Context(), ref, Variable{
		Key:              "K",
		Value:            "prod-new",
		Type:             "env_var",
		Raw:              true,
		EnvironmentScope: "production",
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := fake.get("project:1", "K", "production"); got.Value != "prod-new" {
		t.Errorf("production value = %q, want prod-new", got.Value)
	}
	if got := fake.get("project:1", "K", "staging"); got.Value != "staging-old" {
		t.Errorf("staging value = %q — filter[environment_scope] not applied, wrong variable updated", got.Value)
	}
}

func TestListAndUpdateAtAllLevels(t *testing.T) {
	fake := newFakeGitLab("tok")
	fake.addProject("a/b", 1)
	fake.addGroup("g", 2)
	fake.seed("project:1", fakeVar{Key: "P", Value: "old", VariableType: "env_var", EnvironmentScope: "*"})
	fake.seed("group:2", fakeVar{Key: "G", Value: "old", VariableType: "env_var", EnvironmentScope: "*"})
	fake.seed("instance", fakeVar{Key: "I", Value: "old", VariableType: "env_var"})
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "tok", 0)

	targets := map[string]config.TargetRef{
		"project:1": {Kind: config.KindProject, Ref: "a/b", ID: 1},
		"group:2":   {Kind: config.KindGroup, Ref: "g", ID: 2},
		"instance":  {Kind: config.KindInstance},
	}
	keys := map[string]string{"project:1": "P", "group:2": "G", "instance": "I"}

	for scope, ref := range targets {
		t.Run(scope, func(t *testing.T) {
			vars, err := c.List(t.Context(), ref)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(vars) != 1 || vars[0].Key != keys[scope] || vars[0].Value != "old" {
				t.Fatalf("List = %+v", vars)
			}

			envScope := "*"
			if ref.Kind == config.KindInstance {
				envScope = ""
			}
			err = c.Update(t.Context(), ref, Variable{
				Key: keys[scope], Value: "new", Type: "file",
				Protected: true, Raw: true, EnvironmentScope: envScope,
				Description: "updated",
			})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			got := fake.get(scope, keys[scope], envScope)
			if got == nil || got.Value != "new" || got.VariableType != "file" || !got.Protected || !got.Raw || got.Description != "updated" {
				t.Errorf("updated variable wrong: %+v", got)
			}
		})
	}
}

func TestVariableIdentity(t *testing.T) {
	v := Variable{Key: "K", EnvironmentScope: "prod"}
	id := v.Identity()
	if id.Key != "K" || id.EnvironmentScope != "prod" {
		t.Errorf("Identity() = %+v", id)
	}
}

func TestNewRequiresResolvableToken(t *testing.T) {
	_, err := New(config.GitLabConfig{URL: "https://g", TokenEnv: "VGO_UNSET_TOKEN_VAR"})
	if err == nil {
		t.Fatal("expected error for unresolvable token")
	}
}

func TestCreateMaskedViolationSurfaces400(t *testing.T) {
	fake := newFakeGitLab("tok")
	fake.addProject("a/b", 1)
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "tok", 0)
	ref := config.TargetRef{Kind: config.KindProject, Ref: "a/b", ID: 1}

	err := c.Create(t.Context(), ref, Variable{
		Key:    "BAD",
		Value:  "short", // violates the >=8 chars masking rule
		Type:   "env_var",
		Masked: true,
	})
	if err == nil {
		t.Fatal("expected 400 for masked violation")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error does not carry the API message: %v", err)
	}
	if !strings.Contains(err.Error(), "create BAD on project:a/b") {
		t.Errorf("error lacks target context: %v", err)
	}
}

func TestCreateDuplicateSurfacesError(t *testing.T) {
	fake := newFakeGitLab("tok")
	fake.addProject("a/b", 1)
	fake.seed("project:1", fakeVar{Key: "K", Value: "v", VariableType: "env_var", EnvironmentScope: "*"})
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "tok", 0)
	ref := config.TargetRef{Kind: config.KindProject, Ref: "a/b", ID: 1}

	err := c.Create(t.Context(), ref, Variable{Key: "K", Value: "v2", Type: "env_var", EnvironmentScope: "*"})
	if err == nil || !strings.Contains(err.Error(), "already been taken") {
		t.Fatalf("expected already-taken error, got %v", err)
	}
}

func TestInstanceVariableLimit(t *testing.T) {
	fake := newFakeGitLab("tok")
	for i := 0; i < 25; i++ {
		fake.seed("instance", fakeVar{Key: fmt.Sprintf("V%d", i), Value: "v", VariableType: "env_var"})
	}
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "tok", 0)
	err := c.Create(t.Context(), config.TargetRef{Kind: config.KindInstance}, Variable{Key: "ONE_MORE", Value: "v", Type: "env_var"})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected instance limit error, got %v", err)
	}
}

func TestUpdateMissingVariableIs404(t *testing.T) {
	fake := newFakeGitLab("tok")
	fake.addProject("a/b", 1)
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "tok", 0)
	ref := config.TargetRef{Kind: config.KindProject, Ref: "a/b", ID: 1}
	err := c.Update(t.Context(), ref, Variable{Key: "NOPE", Value: "v", Type: "env_var", EnvironmentScope: "*"})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404, got %v", err)
	}
}

func TestWrongTokenIs401(t *testing.T) {
	fake := newFakeGitLab("right")
	fake.addProject("a/b", 1)
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "wrong", 0)
	ref := config.TargetRef{Kind: config.KindProject, Ref: "a/b"}
	if err := c.ResolveTarget(t.Context(), &ref); err == nil {
		t.Fatal("expected 401 error")
	}
}

func TestRateLimitedClientStillWorks(t *testing.T) {
	fake := newFakeGitLab("tok")
	fake.addProject("a/b", 1)
	fake.seed("project:1", fakeVar{Key: "K", Value: "v", VariableType: "env_var", EnvironmentScope: "*"})
	srv := fake.start()
	defer srv.Close()

	c := newTestClient(t, srv.URL, "tok", 100)
	vars, err := c.List(t.Context(), config.TargetRef{Kind: config.KindProject, Ref: "a/b", ID: 1})
	if err != nil || len(vars) != 1 {
		t.Fatalf("List with limiter: %v, %d vars", err, len(vars))
	}
}

func TestRequestTimeoutBoundsAHangingServer(t *testing.T) {
	// client-go ships no HTTP timeout; without ours this call would hang
	// until the process is killed rather than returning an error.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // accept the request, never answer it
	}))
	defer srv.Close()

	prev := requestTimeout
	requestTimeout = 100 * time.Millisecond
	t.Cleanup(func() { requestTimeout = prev })

	c := newTestClient(t, srv.URL, "tok", 0)

	done := make(chan error, 1)
	go func() {
		// context.Background so only the client's own timeout can end it.
		_, err := c.List(context.Background(), config.TargetRef{Kind: config.KindInstance})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("List against a silent server returned no error")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("List against a silent server never returned: no request timeout in effect")
	}
}
