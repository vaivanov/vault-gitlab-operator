package sync

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
	"github.com/vaivanov/vault-gitlab-operator/internal/gitlab"
)

// instanceValueLimit is the GitLab cap on instance-level variable values.
const instanceValueLimit = 10000

var keyRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)

// buildDesired turns one expanded config target into concrete GitLab
// variables by reading the referenced Vault secrets. Specs that cannot be
// materialized become OpSkip actions instead of failing the whole target;
// error-class skips (Vault failures, missing fields, key collisions)
// carry Err and count as failures, while masked-rule violations are
// failures or plain skips depending on sync.on_masked_violation.
func (r *Reconciler) buildDesired(ctx context.Context, t config.Target) ([]gitlab.Variable, []Action) {
	var desired []gitlab.Variable
	var skips []Action

	skipWarn := func(key, reason string) {
		skips = append(skips, Action{
			Op:       OpSkip,
			Target:   t.Ref,
			Variable: gitlab.Variable{Key: key},
			Reason:   reason,
		})
	}
	skipErr := func(key, reason string) {
		skips = append(skips, Action{
			Op:       OpSkip,
			Target:   t.Ref,
			Variable: gitlab.Variable{Key: key},
			Reason:   reason,
			Err:      errors.New(reason),
		})
	}

	// Explicit keys reserve their identity up front so from_secret
	// expansion cannot silently shadow them regardless of spec order.
	taken := map[config.Identity]bool{}
	for _, spec := range t.Variables {
		if spec.Vault != nil {
			taken[r.identityFor(t.Ref, spec.Key, spec.EnvironmentScope)] = true
		}
	}

	add := func(spec config.ResolvedVariable, key, value string) {
		v := gitlab.Variable{
			Key:              key,
			Value:            value,
			Type:             spec.Type,
			Protected:        spec.Protected,
			Masked:           spec.Masked,
			Raw:              spec.Raw,
			EnvironmentScope: spec.EnvironmentScope,
			Description:      spec.Description,
		}
		if t.Ref.Kind == config.KindInstance {
			// Instance variables have no environment scope.
			v.EnvironmentScope = ""
			if len(v.Value) > instanceValueLimit {
				skipErr(key, fmt.Sprintf("value exceeds the %d-character instance variable limit", instanceValueLimit))
				return
			}
		}
		if v.Masked {
			if reason := maskableViolation(v.Value); reason != "" {
				if r.Config.Sync.OnMaskedViolation == config.MaskedViolationSkipWarn {
					skipWarn(key, reason)
				} else {
					skipErr(key, reason)
				}
				return
			}
		}
		desired = append(desired, v)
	}

	for _, spec := range t.Variables {
		switch {
		case spec.Vault != nil:
			data, _, err := r.Secrets.Read(ctx, *spec.Vault)
			if err != nil {
				skipErr(spec.Key, fmt.Sprintf("vault: %v", err))
				continue
			}
			value, ok := data[spec.Vault.Field]
			if !ok {
				skipErr(spec.Key, fmt.Sprintf("vault: secret %s has no field %q", spec.Vault.SecretKey(), spec.Vault.Field))
				continue
			}
			add(spec, spec.Key, value)

		case spec.FromSecret != nil:
			data, _, err := r.Secrets.Read(ctx, *spec.FromSecret)
			if err != nil {
				skipErr("from_secret:"+spec.FromSecret.SecretKey(), fmt.Sprintf("vault: %v", err))
				continue
			}
			// Deterministic order regardless of map iteration.
			fields := make([]string, 0, len(data))
			for field := range data {
				fields = append(fields, field)
			}
			sort.Strings(fields)

			for _, field := range fields {
				key := spec.Prefix + sanitizeKey(field)
				if !keyRe.MatchString(key) {
					skipErr(key, fmt.Sprintf("derived key from field %q of %s is not a valid variable key", field, spec.FromSecret.SecretKey()))
					continue
				}
				id := r.identityFor(t.Ref, key, spec.EnvironmentScope)
				if taken[id] {
					skipErr(key, fmt.Sprintf("derived key from field %q of %s collides with another variable", field, spec.FromSecret.SecretKey()))
					continue
				}
				taken[id] = true
				add(spec, key, data[field])
			}
		}
	}

	return desired, skips
}

// identityFor normalizes the identity scope: instance variables have none.
func (r *Reconciler) identityFor(ref config.TargetRef, key, scope string) config.Identity {
	if ref.Kind == config.KindInstance {
		scope = ""
	}
	return config.Identity{Key: key, EnvironmentScope: scope}
}

// sanitizeKey converts a Vault field name into a GitLab variable key:
// upper-cased, with every invalid character replaced by underscore.
func sanitizeKey(field string) string {
	upper := strings.ToUpper(field)
	return strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, upper)
}
