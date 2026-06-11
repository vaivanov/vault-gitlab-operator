package config

import (
	"errors"
	"fmt"
)

// expand flattens targets: bundle references are inlined (in listed
// order), inline variables are appended, defaults are applied to every
// unset attribute. An inline variable with the same (key,
// environment_scope) as a bundle variable overrides it; any other
// duplicate identity within one target is an error.
func (c *Config) expand() ([]Target, error) {
	var targets []Target
	var errs []error

	if c.Targets.Instance != nil {
		t, err := c.expandTarget(TargetRef{Kind: KindInstance},
			c.Targets.Instance.Bundles, c.Targets.Instance.Variables)
		targets, errs = append(targets, t), append(errs, err)
	}
	for _, g := range c.Targets.Groups {
		t, err := c.expandTarget(TargetRef{Kind: KindGroup, Ref: g.Group}, g.Bundles, g.Variables)
		targets, errs = append(targets, t), append(errs, err)
	}
	for _, p := range c.Targets.Projects {
		t, err := c.expandTarget(TargetRef{Kind: KindProject, Ref: p.Project}, p.Bundles, p.Variables)
		targets, errs = append(targets, t), append(errs, err)
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return targets, nil
}

func (c *Config) expandTarget(ref TargetRef, bundles []string, inline []VariableSpec) (Target, error) {
	var errs []error

	// ordered preserves declaration order; originBundle is parallel to it
	// and holds the bundle a spec came from ("" = inline). keyed indexes
	// single-field specs by GitLab identity for override/collision checks.
	var ordered []ResolvedVariable
	var originBundle []string
	keyed := map[Identity]int{}
	fromSecretSeen := map[string]bool{} // mount/path#prefix@scope

	add := func(spec VariableSpec, fromBundle string) {
		v := c.resolve(spec)

		// Re-check environment scope against the concrete target kind:
		// bundle specs are only checked against the most permissive kind
		// at validation time.
		if v.EnvironmentScope != defaultEnvironmentScope {
			switch ref.Kind {
			case KindInstance:
				errs = append(errs, fmt.Errorf(
					"%s: variable %q (bundle %q): environment_scope is not supported on instance-level variables",
					ref, v.Key, fromBundle))
				return
			case KindGroup:
				if fromBundle != "" {
					c.Warnings = append(c.Warnings, fmt.Sprintf(
						"%s: variable %q (bundle %q): environment_scope on group variables requires GitLab Premium/Ultimate",
						ref, v.Key, fromBundle))
				}
			}
		}

		if v.FromSecret != nil {
			sig := v.FromSecret.SecretKey() + "#" + v.Prefix + "@" + v.EnvironmentScope
			if fromSecretSeen[sig] {
				errs = append(errs, fmt.Errorf(
					"%s: duplicate from_secret %s with prefix %q and scope %q",
					ref, v.FromSecret.SecretKey(), v.Prefix, v.EnvironmentScope))
				return
			}
			fromSecretSeen[sig] = true
			ordered = append(ordered, v)
			originBundle = append(originBundle, fromBundle)
			return
		}

		id := v.Identity()
		if at, dup := keyed[id]; dup {
			// An inline spec may override a bundle-provided definition;
			// every other duplicate identity is a config error.
			if fromBundle == "" && originBundle[at] != "" {
				ordered[at] = v
				originBundle[at] = ""
				return
			}
			errs = append(errs, fmt.Errorf(
				"%s: duplicate variable %q (environment_scope %q)%s",
				ref, v.Key, v.EnvironmentScope, bundleSuffix(fromBundle)))
			return
		}
		keyed[id] = len(ordered)
		ordered = append(ordered, v)
		originBundle = append(originBundle, fromBundle)
	}

	for _, name := range bundles {
		for _, spec := range c.Bundles[name] {
			add(spec, name)
		}
	}
	for _, spec := range inline {
		add(spec, "")
	}

	return Target{Ref: ref, Variables: ordered}, errors.Join(errs...)
}

func bundleSuffix(bundle string) string {
	if bundle == "" {
		return ""
	}
	return fmt.Sprintf(" from bundle %q", bundle)
}

// resolve applies defaults to a raw spec, producing concrete values for
// every attribute.
func (c *Config) resolve(spec VariableSpec) ResolvedVariable {
	v := ResolvedVariable{
		Key:              spec.Key,
		Prefix:           spec.Prefix,
		Type:             c.Defaults.VariableType,
		Protected:        c.Defaults.Protected,
		Masked:           c.Defaults.Masked,
		Raw:              *c.Defaults.Raw,
		EnvironmentScope: c.Defaults.EnvironmentScope,
		Description:      c.Defaults.Description,
	}
	if spec.Vault != nil {
		r := *spec.Vault
		if r.Mount == "" {
			r.Mount = c.Defaults.VaultMount
		}
		v.Vault = &r
	}
	if spec.FromSecret != nil {
		r := *spec.FromSecret
		if r.Mount == "" {
			r.Mount = c.Defaults.VaultMount
		}
		v.FromSecret = &r
	}
	if spec.VariableType != nil {
		v.Type = *spec.VariableType
	}
	if spec.Protected != nil {
		v.Protected = *spec.Protected
	}
	if spec.Masked != nil {
		v.Masked = *spec.Masked
	}
	if spec.Raw != nil {
		v.Raw = *spec.Raw
	}
	if spec.EnvironmentScope != nil {
		v.EnvironmentScope = *spec.EnvironmentScope
	}
	if spec.Description != nil {
		v.Description = *spec.Description
	}
	return v
}
