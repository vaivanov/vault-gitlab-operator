package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// keyRe matches valid GitLab CI/CD variable keys.
var keyRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)

// validate performs structural validation that does not require bundle
// expansion. All failures are aggregated into one error; non-fatal
// findings are appended to c.Warnings.
func (c *Config) validate() error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}
	warn := func(format string, args ...any) {
		c.Warnings = append(c.Warnings, fmt.Sprintf(format, args...))
	}

	// --- connections ---
	if c.Vault.Address == "" {
		fail("vault.address is required (or set VAULT_ADDR)")
	} else if err := validURL(c.Vault.Address); err != nil {
		fail("vault.address: %v", err)
	}

	switch c.Vault.Auth.Method {
	case "token":
		if c.Vault.Auth.Token.Env != "" && c.Vault.Auth.Token.File != "" {
			fail("vault.auth.token: set either env or file, not both")
		}
	case "approle":
		a := c.Vault.Auth.AppRole
		if err := exactlyOneRef("vault.auth.approle.role_id", a.RoleIDEnv, a.RoleIDFile); err != nil {
			errs = append(errs, err)
		}
		if err := exactlyOneRef("vault.auth.approle.secret_id", a.SecretIDEnv, a.SecretIDFile); err != nil {
			errs = append(errs, err)
		}
	case "kubernetes":
		if c.Vault.Auth.Kubernetes.Role == "" {
			fail("vault.auth.kubernetes.role is required")
		}
	default:
		fail("vault.auth.method must be token, approle or kubernetes, got %q", c.Vault.Auth.Method)
	}

	if c.GitLab.URL == "" {
		fail("gitlab.url is required")
	} else if err := validURL(c.GitLab.URL); err != nil {
		fail("gitlab.url: %v", err)
	}
	if err := exactlyOneRef("gitlab token (token_env/token_file)", c.GitLab.TokenEnv, c.GitLab.TokenFile); err != nil {
		errs = append(errs, err)
	}
	if c.GitLab.RateLimit < 0 {
		fail("gitlab.rate_limit must be >= 0, got %v", c.GitLab.RateLimit)
	}

	// --- sync ---
	if c.Sync.Interval.Std() <= 0 {
		fail("sync.interval must be positive")
	}
	if c.Sync.Jitter.Std() < 0 {
		fail("sync.jitter must be >= 0")
	}
	if c.Sync.Concurrency < 1 {
		fail("sync.concurrency must be >= 1, got %d", c.Sync.Concurrency)
	}
	if c.Sync.PassTimeout() < 0 {
		fail("sync.timeout must be >= 0 (0 disables the pass timeout)")
	} else if t := c.Sync.PassTimeout(); t > 0 && t < c.Sync.Interval.Std() {
		warn("sync.timeout (%s) is shorter than sync.interval (%s): passes will be cut short",
			t, c.Sync.Interval.Std())
	}
	if v := c.Sync.OnMaskedViolation; v != MaskedViolationError && v != MaskedViolationSkipWarn {
		fail("sync.on_masked_violation must be %q or %q, got %q", MaskedViolationError, MaskedViolationSkipWarn, v)
	}

	// --- defaults ---
	if t := c.Defaults.VariableType; t != "env_var" && t != "file" {
		fail("defaults.variable_type must be env_var or file, got %q", t)
	}

	// --- bundles ---
	for name, specs := range c.Bundles {
		if len(specs) == 0 {
			warn("bundle %q is empty", name)
		}
		for i, spec := range specs {
			where := fmt.Sprintf("bundles.%s[%d]", name, i)
			errs = append(errs, c.validateSpec(where, spec, KindProject, warn)...)
		}
	}

	// --- targets ---
	hasTarget := false
	if c.Targets.Instance != nil {
		hasTarget = true
		errs = append(errs, c.validateTargetSpecs("targets.instance", KindInstance,
			c.Targets.Instance.Bundles, c.Targets.Instance.Variables, warn)...)
	}
	// Each GitLab object may be declared at most once: two targets for the
	// same group/project are reconciled concurrently and would race each
	// other's create/update calls. Only exact duplicates are catchable
	// here — path-vs-numeric-ID aliases are caught after resolution.
	seenGroups := map[string]int{}
	for i, g := range c.Targets.Groups {
		hasTarget = true
		where := fmt.Sprintf("targets.groups[%d]", i)
		switch first, dup := seenGroups[g.Group]; {
		case g.Group == "":
			fail("%s: group is required (path or numeric ID)", where)
		case dup:
			fail("%s: group %q is already declared at targets.groups[%d]; merge the two entries into one", where, g.Group, first)
		default:
			seenGroups[g.Group] = i
		}
		errs = append(errs, c.validateTargetSpecs(where, KindGroup, g.Bundles, g.Variables, warn)...)
	}
	seenProjects := map[string]int{}
	for i, p := range c.Targets.Projects {
		hasTarget = true
		where := fmt.Sprintf("targets.projects[%d]", i)
		switch first, dup := seenProjects[p.Project]; {
		case p.Project == "":
			fail("%s: project is required (path or numeric ID)", where)
		case dup:
			fail("%s: project %q is already declared at targets.projects[%d]; merge the two entries into one", where, p.Project, first)
		default:
			seenProjects[p.Project] = i
		}
		errs = append(errs, c.validateTargetSpecs(where, KindProject, p.Bundles, p.Variables, warn)...)
	}
	if !hasTarget {
		fail("targets: at least one of instance, groups or projects is required")
	}

	return errors.Join(errs...)
}

func (c *Config) validateTargetSpecs(where string, kind TargetKind, bundles []string, specs []VariableSpec, warn func(string, ...any)) []error {
	var errs []error
	for _, b := range bundles {
		if _, ok := c.Bundles[b]; !ok {
			errs = append(errs, fmt.Errorf("%s: unknown bundle %q", where, b))
		}
	}
	for i, spec := range specs {
		specWhere := fmt.Sprintf("%s.variables[%d]", where, i)
		errs = append(errs, c.validateSpec(specWhere, spec, kind, warn)...)
	}
	// Instance-level variables also must not come from bundles that carry
	// environment scopes; that is caught during expansion where bundle
	// specs land on a concrete target.
	return errs
}

// validateSpec checks one variable spec. kind affects environment_scope
// rules: forbidden on instance, Premium-only warning on group. Bundle
// specs are validated with KindProject (most permissive); scope rules are
// re-checked against the real target kind during expansion.
func (c *Config) validateSpec(where string, spec VariableSpec, kind TargetKind, warn func(string, ...any)) []error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	switch {
	case spec.Vault != nil && spec.FromSecret != nil:
		fail("%s: vault and from_secret are mutually exclusive", where)
	case spec.Vault == nil && spec.FromSecret == nil:
		fail("%s: exactly one of vault or from_secret is required", where)
	case spec.Vault != nil:
		if spec.Key == "" {
			fail("%s: key is required", where)
		} else if !keyRe.MatchString(spec.Key) {
			fail("%s: key %q is invalid (must match %s)", where, spec.Key, keyRe)
		}
		if spec.Vault.Path == "" {
			fail("%s: vault.path is required", where)
		}
		if spec.Vault.Field == "" {
			fail("%s: vault.field is required (use from_secret to map all fields)", where)
		}
		if spec.Prefix != "" {
			fail("%s: prefix is only valid with from_secret", where)
		}
	case spec.FromSecret != nil:
		if spec.Key != "" {
			fail("%s: key is not allowed with from_secret (keys derive from secret fields)", where)
		}
		if spec.FromSecret.Path == "" {
			fail("%s: from_secret.path is required", where)
		}
		if spec.FromSecret.Field != "" {
			fail("%s: from_secret.field is not allowed (use vault: to map a single field)", where)
		}
		if spec.Prefix != "" && !keyRe.MatchString(spec.Prefix) {
			fail("%s: prefix %q is invalid (must match %s)", where, spec.Prefix, keyRe)
		}
	}

	if spec.VariableType != nil && *spec.VariableType != "env_var" && *spec.VariableType != "file" {
		fail("%s: variable_type must be env_var or file, got %q", where, *spec.VariableType)
	}

	if spec.EnvironmentScope != nil && *spec.EnvironmentScope != defaultEnvironmentScope {
		switch kind {
		case KindInstance:
			fail("%s: environment_scope is not supported on instance-level variables", where)
		case KindGroup:
			warn("%s: environment_scope on group variables requires GitLab Premium/Ultimate", where)
		}
	}

	return errs
}

func exactlyOneRef(what, env, file string) error {
	switch {
	case env == "" && file == "":
		return fmt.Errorf("%s: one of env or file reference is required", what)
	case env != "" && file != "":
		return fmt.Errorf("%s: set either env or file reference, not both", what)
	}
	return nil
}

func validURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL %q must use http or https", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("URL %q has no host", raw)
	}
	if strings.HasSuffix(u.Path, "/") {
		return fmt.Errorf("URL %q must not end with a slash", raw)
	}
	return nil
}
