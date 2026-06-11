package sync

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/scentbird/vault-gitlab-operator/internal/config"
	"github.com/scentbird/vault-gitlab-operator/internal/gitlab"
)

// newReconciler parses a config body (only sync/defaults/targets matter
// for the reconciler) and wires the in-memory fakes.
func newReconciler(t *testing.T, yaml string, secrets *fakeSecrets, store *fakeStore) *Reconciler {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return &Reconciler{
		Config:  cfg,
		Secrets: secrets,
		Store:   store,
		Log:     slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}
}

const header = `
vault: {address: https://v:8200}
gitlab: {url: https://g, token_env: T}
`

func TestCreateWhenAbsent(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"password": "hunter22hunter22"})
	store := newFakeStore()

	r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {key: DB_PASSWORD, vault: {path: ci/app, field: password}, masked: true, protected: true}
`, secrets, store)

	report := r.Run(t.Context(), false)
	c := report.Counts()
	if c.Created != 1 || c.Failed != 0 {
		t.Fatalf("counts = %+v", c)
	}
	got := store.get("project:a/b", "DB_PASSWORD", "*")
	if got == nil {
		t.Fatal("variable not created")
	}
	if got.Value != "hunter22hunter22" || !got.Masked || !got.Protected || !got.Raw || got.Type != "env_var" {
		t.Errorf("created variable wrong: %+v", got)
	}
}

func TestUpdateOnEachFieldChange(t *testing.T) {
	base := gitlab.Variable{
		Key: "K", Value: "same-value", Type: "env_var",
		Protected: false, Masked: false, Raw: true,
		EnvironmentScope: "*", Description: "Managed by vault-gitlab-operator",
	}

	tests := []struct {
		name      string
		mutate    func(*gitlab.Variable) // mutates the OBSERVED variable
		wantField string
	}{
		{"value drift", func(v *gitlab.Variable) { v.Value = "old-value" }, "value"},
		{"type drift", func(v *gitlab.Variable) { v.Type = "file" }, "variable_type"},
		{"protected drift", func(v *gitlab.Variable) { v.Protected = true }, "protected"},
		{"masked drift", func(v *gitlab.Variable) { v.Masked = true }, "masked"},
		{"raw drift", func(v *gitlab.Variable) { v.Raw = false }, "raw"},
		{"description drift", func(v *gitlab.Variable) { v.Description = "manual edit" }, "description"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secrets := newFakeSecrets()
			secrets.put("secret/ci/app", map[string]string{"f": "same-value"})
			store := newFakeStore()
			observed := base
			tt.mutate(&observed)
			store.seed("project:a/b", observed)

			r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {key: K, vault: {path: ci/app, field: f}}
`, secrets, store)

			report := r.Run(t.Context(), false)
			c := report.Counts()
			if c.Updated != 1 {
				t.Fatalf("counts = %+v, want exactly one update", c)
			}

			var update *Action
			for _, tr := range report.Targets {
				for i := range tr.Actions {
					if tr.Actions[i].Op == OpUpdate {
						update = &tr.Actions[i]
					}
				}
			}
			if update == nil || len(update.Diff) != 1 || update.Diff[0].Field != tt.wantField {
				t.Fatalf("update diff = %+v, want single change of %s", update, tt.wantField)
			}

			got := store.get("project:a/b", "K", "*")
			if got.Value != "same-value" || got.Type != "env_var" || got.Protected || got.Masked || !got.Raw {
				t.Errorf("variable not converged: %+v", got)
			}
		})
	}
}

func TestNoopWhenConverged(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"f": "value-123"})
	store := newFakeStore()
	store.seed("project:a/b", gitlab.Variable{
		Key: "K", Value: "value-123", Type: "env_var", Raw: true,
		EnvironmentScope: "*", Description: "Managed by vault-gitlab-operator",
	})

	r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {key: K, vault: {path: ci/app, field: f}}
`, secrets, store)

	report := r.Run(t.Context(), false)
	c := report.Counts()
	if c.Unchanged != 1 || c.Created+c.Updated+c.Failed != 0 {
		t.Fatalf("counts = %+v, want pure noop", c)
	}
	if store.writes() != 0 {
		t.Errorf("converged run performed %d writes", store.writes())
	}
}

func TestNoDeleteGuarantee(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"f": "value-123"})
	store := newFakeStore()
	// A manually-created variable that the config knows nothing about.
	store.seed("project:a/b", gitlab.Variable{
		Key: "MANUAL_VAR", Value: "precious", Type: "env_var", EnvironmentScope: "*",
	})

	r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {key: K, vault: {path: ci/app, field: f}}
`, secrets, store)

	report := r.Run(t.Context(), false)

	if got := store.get("project:a/b", "MANUAL_VAR", "*"); got == nil || got.Value != "precious" {
		t.Fatalf("unmanaged variable was touched: %+v", got)
	}
	for _, tr := range report.Targets {
		for _, a := range tr.Actions {
			if a.Variable.Key == "MANUAL_VAR" {
				t.Errorf("action planned for unmanaged variable: %s", a)
			}
		}
	}
}

func TestMaskedViolationErrorMode(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"f": "short"})
	store := newFakeStore()

	r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {key: K, vault: {path: ci/app, field: f}, masked: true}
`, secrets, store)

	report := r.Run(t.Context(), false)
	c := report.Counts()
	if c.Failed != 1 || c.Created != 0 {
		t.Fatalf("counts = %+v, want 1 failed (error mode)", c)
	}
	if !report.HasErrors() {
		t.Error("report.HasErrors() = false")
	}
	if store.writes() != 0 {
		t.Error("violating variable was written anyway")
	}
}

func TestMaskedViolationSkipWarnMode(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"f": "short", "ok": "long-enough-value"})
	store := newFakeStore()

	r := newReconciler(t, header+`
sync: {on_masked_violation: skip-warn}
targets:
  projects:
    - project: a/b
      variables:
        - {key: BAD, vault: {path: ci/app, field: f}, masked: true}
        - {key: GOOD, vault: {path: ci/app, field: ok}, masked: true}
`, secrets, store)

	report := r.Run(t.Context(), false)
	c := report.Counts()
	if c.Skipped != 1 || c.Created != 1 || c.Failed != 0 {
		t.Fatalf("counts = %+v, want 1 skipped + 1 created, no failures", c)
	}
	if report.HasErrors() {
		t.Error("skip-warn mode must not flag errors")
	}
	if store.get("project:a/b", "GOOD", "*") == nil {
		t.Error("valid variable not created alongside the skipped one")
	}
}

func TestMaskedViolationTable(t *testing.T) {
	tests := []struct {
		value string
		ok    bool
	}{
		{"abcdefgh", true},
		{"abc-def_gh+/=@:.~", true},
		{"abcdefg", false},          // too short
		{"abc defgh", false},        // space
		{"abcdef\ngh", false},       // newline
		{"abcdefgh!", false},        // bad char
		{"пароль-пароль", false},    // non-ascii
		{"AAAAAAAAAAAAAAAA", true},
	}
	for _, tt := range tests {
		got := maskableViolation(tt.value)
		if (got == "") != tt.ok {
			t.Errorf("maskableViolation(%q) = %q, want ok=%v", tt.value, got, tt.ok)
		}
	}
}

func TestFromSecretExpansion(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/dotenv", map[string]string{
		"db-url":    "postgres://x",
		"api_token": "tok-123",
	})
	store := newFakeStore()

	r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {from_secret: {path: ci/dotenv}, prefix: APP_}
`, secrets, store)

	report := r.Run(t.Context(), false)
	if c := report.Counts(); c.Created != 2 || c.Failed != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if v := store.get("project:a/b", "APP_DB_URL", "*"); v == nil || v.Value != "postgres://x" {
		t.Errorf("APP_DB_URL = %+v (sanitize/upper-case broken)", v)
	}
	if v := store.get("project:a/b", "APP_API_TOKEN", "*"); v == nil || v.Value != "tok-123" {
		t.Errorf("APP_API_TOKEN = %+v", v)
	}
}

func TestFromSecretCollisionWithExplicitKey(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/dotenv", map[string]string{"db_url": "from-secret"})
	secrets.put("secret/ci/db", map[string]string{"url": "explicit"})
	store := newFakeStore()

	r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {from_secret: {path: ci/dotenv}, prefix: APP_}
        - {key: APP_DB_URL, vault: {path: ci/db, field: url}}
`, secrets, store)

	report := r.Run(t.Context(), false)
	c := report.Counts()
	if c.Created != 1 || c.Failed != 1 {
		t.Fatalf("counts = %+v, want explicit key wins + collision failure", c)
	}
	// The explicit key must win even though from_secret is listed first.
	if v := store.get("project:a/b", "APP_DB_URL", "*"); v == nil || v.Value != "explicit" {
		t.Errorf("APP_DB_URL = %+v, want the explicit spec's value", v)
	}
}

func TestVaultFailureIsolation(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/ok", map[string]string{"f": "fine-value"})
	secrets.errs["secret/ci/broken"] = errors.New("permission denied")
	store := newFakeStore()

	r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {key: BROKEN, vault: {path: ci/broken, field: f}}
        - {key: OK, vault: {path: ci/ok, field: f}}
`, secrets, store)

	report := r.Run(t.Context(), false)
	c := report.Counts()
	if c.Created != 1 || c.Failed != 1 {
		t.Fatalf("counts = %+v, want broken path isolated", c)
	}
	if store.get("project:a/b", "OK", "*") == nil {
		t.Error("healthy variable not synced when sibling path is broken")
	}
}

func TestMissingFieldIsFailure(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"other": "x"})
	store := newFakeStore()

	r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {key: K, vault: {path: ci/app, field: missing}}
`, secrets, store)

	report := r.Run(t.Context(), false)
	if c := report.Counts(); c.Failed != 1 {
		t.Fatalf("counts = %+v, want missing field reported as failure", c)
	}
}

func TestDryRunPerformsZeroWrites(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"f": "value-one", "g": "value-two"})
	store := newFakeStore()
	store.seed("project:a/b", gitlab.Variable{
		Key: "K", Value: "stale", Type: "env_var", Raw: true,
		EnvironmentScope: "*", Description: "Managed by vault-gitlab-operator",
	})

	r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {key: K, vault: {path: ci/app, field: f}}
        - {key: NEW, vault: {path: ci/app, field: g}}
`, secrets, store)

	report := r.Run(t.Context(), true)
	c := report.Counts()
	if c.Created != 1 || c.Updated != 1 {
		t.Fatalf("dry-run counts = %+v, want planned 1 create + 1 update", c)
	}
	if !report.HasChanges() {
		t.Error("HasChanges() = false with pending diff")
	}
	if store.writes() != 0 {
		t.Fatalf("dry run performed %d writes", store.writes())
	}
	if got := store.get("project:a/b", "K", "*"); got.Value != "stale" {
		t.Errorf("dry run mutated state: %+v", got)
	}
}

func TestInstanceScopeForcedEmpty(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"f": "value-123"})
	store := newFakeStore()

	r := newReconciler(t, header+`
targets:
  instance:
    variables:
      - {key: K, vault: {path: ci/app, field: f}}
`, secrets, store)

	report := r.Run(t.Context(), false)
	if c := report.Counts(); c.Created != 1 {
		t.Fatalf("counts = %+v", c)
	}
	if store.get("instance", "K", "") == nil {
		t.Error("instance variable must carry empty environment scope")
	}
}

func TestInstanceValueLimit(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"f": strings.Repeat("x", 10001)})
	store := newFakeStore()

	r := newReconciler(t, header+`
targets:
  instance:
    variables:
      - {key: K, vault: {path: ci/app, field: f}}
`, secrets, store)

	report := r.Run(t.Context(), false)
	if c := report.Counts(); c.Failed != 1 || c.Created != 0 {
		t.Fatalf("counts = %+v, want oversized instance value rejected", c)
	}
}

func TestTargetFailureIsolation(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"f": "value-123"})
	store := newFakeStore()
	store.resolveErr["project:gone/project"] = errors.New("404 not found")

	r := newReconciler(t, header+`
targets:
  projects:
    - project: gone/project
      variables:
        - {key: K, vault: {path: ci/app, field: f}}
    - project: a/b
      variables:
        - {key: K, vault: {path: ci/app, field: f}}
`, secrets, store)

	report := r.Run(t.Context(), false)
	c := report.Counts()
	if c.TargetErrors != 1 || c.Created != 1 {
		t.Fatalf("counts = %+v, want broken target isolated", c)
	}
	if !report.HasErrors() {
		t.Error("HasErrors() = false with a target error")
	}
}

func TestListFailureIsTargetError(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"f": "value-123"})
	store := newFakeStore()
	store.listErr["project:a/b"] = errors.New("503")

	r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {key: K, vault: {path: ci/app, field: f}}
`, secrets, store)

	report := r.Run(t.Context(), false)
	if c := report.Counts(); c.TargetErrors != 1 {
		t.Fatalf("counts = %+v", c)
	}
}

func TestApplyFailureRecordedAndIsolated(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"a": "value-one", "b": "value-two"})
	store := newFakeStore()
	store.createErr["FAILING"] = errors.New("400 bad request")

	r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {key: FAILING, vault: {path: ci/app, field: a}}
        - {key: WORKING, vault: {path: ci/app, field: b}}
`, secrets, store)

	report := r.Run(t.Context(), false)
	c := report.Counts()
	if c.Failed != 1 || c.Created != 1 {
		t.Fatalf("counts = %+v, want failure isolated from sibling", c)
	}
}

func TestReportRender(t *testing.T) {
	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"f": "new-secret-value"})
	store := newFakeStore()
	store.seed("project:a/b", gitlab.Variable{
		Key: "K", Value: "old-secret-value", Type: "env_var", Raw: true,
		EnvironmentScope: "*", Description: "Managed by vault-gitlab-operator",
	})

	r := newReconciler(t, header+`
targets:
  projects:
    - project: a/b
      variables:
        - {key: K, vault: {path: ci/app, field: f}}
`, secrets, store)

	report := r.Run(t.Context(), true)
	var buf bytes.Buffer
	report.Render(&buf, false)
	out := buf.String()

	if !strings.Contains(out, "update K @ project:a/b") {
		t.Errorf("render missing update line: %q", out)
	}
	if !strings.Contains(out, "value: [REDACTED] -> [REDACTED]") {
		t.Errorf("render missing redacted value change: %q", out)
	}
	if strings.Contains(out, "old-secret-value") || strings.Contains(out, "new-secret-value") {
		t.Fatalf("render leaked a secret value: %q", out)
	}
	if !strings.Contains(out, "planned: 0 created, 1 updated") {
		t.Errorf("render summary wrong: %q", out)
	}
}

func TestLogsNeverContainSecrets(t *testing.T) {
	var logBuf bytes.Buffer

	secrets := newFakeSecrets()
	secrets.put("secret/ci/app", map[string]string{"f": "super-secret-value-42"})
	store := newFakeStore()

	cfg, err := config.Parse([]byte(header + `
targets:
  projects:
    - project: a/b
      variables:
        - {key: K, vault: {path: ci/app, field: f}}
`))
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{
		Config:  cfg,
		Secrets: secrets,
		Store:   store,
		Log:     slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}

	report := r.Run(t.Context(), false)
	var rendered bytes.Buffer
	report.Render(&rendered, true)

	for name, out := range map[string]string{"log": logBuf.String(), "render": rendered.String()} {
		if strings.Contains(out, "super-secret-value-42") {
			t.Errorf("%s output leaked the secret value: %q", name, out)
		}
	}
}
