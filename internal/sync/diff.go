package sync

import (
	"github.com/scentbird/vault-gitlab-operator/internal/config"
	"github.com/scentbird/vault-gitlab-operator/internal/gitlab"
)

const redacted = "[REDACTED]"

// diffTarget compares desired against observed variables of one target
// and plans creates/updates. Observed variables absent from the desired
// set are ignored entirely — the operator never deletes.
func diffTarget(ref config.TargetRef, desired, observed []gitlab.Variable) []Action {
	index := make(map[config.Identity]gitlab.Variable, len(observed))
	for _, v := range observed {
		index[v.Identity()] = v
	}

	actions := make([]Action, 0, len(desired))
	for _, want := range desired {
		have, exists := index[want.Identity()]
		if !exists {
			actions = append(actions, Action{Op: OpCreate, Target: ref, Variable: want})
			continue
		}
		changes := fieldChanges(have, want)
		if len(changes) == 0 {
			actions = append(actions, Action{Op: OpNoop, Target: ref, Variable: want})
			continue
		}
		actions = append(actions, Action{Op: OpUpdate, Target: ref, Variable: want, Diff: changes})
	}
	return actions
}

// fieldChanges lists attribute-level differences. The variable value is
// compared but rendered redacted; identity fields (key, scope) are never
// "changed" — a different identity is a different variable.
func fieldChanges(have, want gitlab.Variable) []FieldChange {
	var changes []FieldChange
	if have.Value != want.Value {
		changes = append(changes, FieldChange{Field: "value", Old: redacted, New: redacted})
	}
	if have.Type != want.Type {
		changes = append(changes, FieldChange{Field: "variable_type", Old: have.Type, New: want.Type})
	}
	if have.Protected != want.Protected {
		changes = append(changes, FieldChange{Field: "protected", Old: boolStr(have.Protected), New: boolStr(want.Protected)})
	}
	if have.Masked != want.Masked {
		changes = append(changes, FieldChange{Field: "masked", Old: boolStr(have.Masked), New: boolStr(want.Masked)})
	}
	if have.Raw != want.Raw {
		changes = append(changes, FieldChange{Field: "raw", Old: boolStr(have.Raw), New: boolStr(want.Raw)})
	}
	if have.Description != want.Description {
		changes = append(changes, FieldChange{Field: "description", Old: have.Description, New: want.Description})
	}
	return changes
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
