package gitlab

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// fakeGitLab is an httptest-backed GitLab API covering exactly what the
// client uses: target resolution and variable list/create/update at the
// three levels, with pagination headers, filter[environment_scope],
// masked-value validation and the instance variable count limit.
type fakeGitLab struct {
	token string

	mu            sync.Mutex
	projects      map[string]int64 // path -> id
	groups        map[string]int64
	vars          map[string][]*fakeVar // "project:1" | "group:2" | "instance"
	instanceLimit int
	resolveCalls  int
	listCalls     int
}

type fakeVar struct {
	Key              string `json:"key"`
	Value            string `json:"value"`
	VariableType     string `json:"variable_type"`
	Protected        bool   `json:"protected"`
	Masked           bool   `json:"masked"`
	Raw              bool   `json:"raw"`
	EnvironmentScope string `json:"environment_scope"`
	Description      string `json:"description"`
}

var maskableRe = regexp.MustCompile(`^[a-zA-Z0-9+/=@:.~_-]{8,}$`)

func newFakeGitLab(token string) *fakeGitLab {
	return &fakeGitLab{
		token:         token,
		projects:      map[string]int64{},
		groups:        map[string]int64{},
		vars:          map[string][]*fakeVar{},
		instanceLimit: 25,
	}
}

func (f *fakeGitLab) addProject(path string, id int64) { f.projects[path] = id }
func (f *fakeGitLab) addGroup(path string, id int64)   { f.groups[path] = id }

func (f *fakeGitLab) seed(scope string, v fakeVar) {
	f.mu.Lock()
	defer f.mu.Unlock()
	vv := v
	f.vars[scope] = append(f.vars[scope], &vv)
}

func (f *fakeGitLab) get(scope, key, envScope string) *fakeVar {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.vars[scope] {
		if v.Key == key && v.EnvironmentScope == envScope {
			out := *v
			return &out
		}
	}
	return nil
}

func (f *fakeGitLab) count(scope string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.vars[scope])
}

func (f *fakeGitLab) start() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(f.handle))
}

func (f *fakeGitLab) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("PRIVATE-TOKEN") != f.token {
		httpError(w, http.StatusUnauthorized, "401 Unauthorized")
		return
	}

	// Use the escaped path so URL-encoded project paths (a%2Fb) survive.
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/"), "/")

	switch {
	case len(parts) >= 3 && parts[0] == "admin" && parts[1] == "ci" && parts[2] == "variables":
		key := ""
		if len(parts) == 4 {
			key = parts[3]
		}
		f.handleVariables(w, r, "instance", key, true)

	case parts[0] == "projects" || parts[0] == "groups":
		kind := strings.TrimSuffix(parts[0], "s") // project | group
		ref, err := url.PathUnescape(parts[1])
		if err != nil {
			httpError(w, http.StatusBadRequest, "bad ref")
			return
		}
		table := f.projects
		if kind == "group" {
			table = f.groups
		}
		id, ok := f.lookup(table, ref)
		if !ok {
			httpError(w, http.StatusNotFound, fmt.Sprintf("404 %s Not Found", strings.Title(kind)))
			return
		}

		if len(parts) == 2 { // resolve
			f.mu.Lock()
			f.resolveCalls++
			f.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "path_with_namespace": ref})
			return
		}
		if parts[2] != "variables" {
			httpError(w, http.StatusNotFound, "unsupported path")
			return
		}
		key := ""
		if len(parts) == 4 {
			key = parts[3]
		}
		f.handleVariables(w, r, fmt.Sprintf("%s:%d", kind, id), key, false)

	default:
		httpError(w, http.StatusNotFound, "unsupported path")
	}
}

// lookup resolves a ref that is either a known path or a numeric ID.
func (f *fakeGitLab) lookup(table map[string]int64, ref string) (int64, bool) {
	if id, ok := table[ref]; ok {
		return id, true
	}
	n, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return 0, false
	}
	for _, id := range table {
		if id == n {
			return id, true
		}
	}
	return 0, false
}

func (f *fakeGitLab) handleVariables(w http.ResponseWriter, r *http.Request, scope, key string, instance bool) {
	switch {
	case r.Method == http.MethodGet && key == "":
		f.handleList(w, r, scope)
	case r.Method == http.MethodPost && key == "":
		f.handleCreate(w, r, scope, instance)
	case r.Method == http.MethodPut && key != "":
		f.handleUpdate(w, r, scope, key, instance)
	default:
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (f *fakeGitLab) handleList(w http.ResponseWriter, r *http.Request, scope string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if per < 1 {
		per = 20
	}

	all := f.vars[scope]
	start := (page - 1) * per
	end := min(start+per, len(all))
	if start > len(all) {
		start, end = len(all), len(all)
	}

	w.Header().Set("X-Total", strconv.Itoa(len(all)))
	w.Header().Set("X-Page", strconv.Itoa(page))
	w.Header().Set("X-Per-Page", strconv.Itoa(per))
	if end < len(all) {
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
	} else {
		w.Header().Set("X-Next-Page", "")
	}
	writeJSON(w, http.StatusOK, all[start:end])
}

func (f *fakeGitLab) handleCreate(w http.ResponseWriter, r *http.Request, scope string, instance bool) {
	var v fakeVar
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	if instance {
		v.EnvironmentScope = ""
	} else if v.EnvironmentScope == "" {
		v.EnvironmentScope = "*"
	}
	if v.VariableType == "" {
		v.VariableType = "env_var"
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if instance && len(f.vars[scope]) >= f.instanceLimit {
		httpError(w, http.StatusBadRequest, fmt.Sprintf("instance variable limit of %d reached", f.instanceLimit))
		return
	}
	if v.Masked && !maskableRe.MatchString(v.Value) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"message": map[string]any{"value": []string{"is invalid"}},
		})
		return
	}
	for _, existing := range f.vars[scope] {
		if existing.Key == v.Key && existing.EnvironmentScope == v.EnvironmentScope {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"message": []string{fmt.Sprintf("(%s) has already been taken", v.Key)},
			})
			return
		}
	}
	f.vars[scope] = append(f.vars[scope], &v)
	writeJSON(w, http.StatusCreated, v)
}

func (f *fakeGitLab) handleUpdate(w http.ResponseWriter, r *http.Request, scope, key string, instance bool) {
	var payload struct {
		fakeVar
		Filter *struct {
			EnvironmentScope string `json:"environment_scope"`
		} `json:"filter"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		httpError(w, http.StatusBadRequest, "bad json")
		return
	}
	in := payload.fakeVar

	f.mu.Lock()
	defer f.mu.Unlock()

	var matches []*fakeVar
	// GitLab accepts the filter both as a query param and in the JSON body.
	filter := r.URL.Query().Get("filter[environment_scope]")
	if filter == "" && payload.Filter != nil {
		filter = payload.Filter.EnvironmentScope
	}
	for _, v := range f.vars[scope] {
		if v.Key != key {
			continue
		}
		if !instance && filter != "" && v.EnvironmentScope != filter {
			continue
		}
		matches = append(matches, v)
	}
	if len(matches) == 0 {
		httpError(w, http.StatusNotFound, "404 Variable Not Found")
		return
	}
	if len(matches) > 1 {
		// GitLab requires the filter to disambiguate duplicate keys.
		writeJSON(w, http.StatusConflict, map[string]any{
			"message": "There are multiple variables with provided parameters. Please use 'filter[environment_scope]'",
		})
		return
	}

	if in.Masked && !maskableRe.MatchString(in.Value) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"message": map[string]any{"value": []string{"is invalid"}},
		})
		return
	}

	target := matches[0]
	target.Value = in.Value
	target.VariableType = in.VariableType
	target.Protected = in.Protected
	target.Masked = in.Masked
	target.Raw = in.Raw
	target.Description = in.Description
	if !instance && in.EnvironmentScope != "" {
		target.EnvironmentScope = in.EnvironmentScope
	}
	writeJSON(w, http.StatusOK, *target)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"message": msg})
}
