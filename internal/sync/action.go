// Package sync implements the reconcile loop: build desired state from
// config + Vault, observe GitLab, diff, and apply creates/updates. It
// never deletes anything by design.
package sync

import (
	"fmt"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
	"github.com/vaivanov/vault-gitlab-operator/internal/gitlab"
)

// Op is the planned operation for one variable.
type Op string

// The reconciler plans creates and updates only; OpNoop marks converged
// variables and OpSkip ones that could not be materialized. There is no
// delete operation by design.
const (
	OpCreate Op = "create"
	OpUpdate Op = "update"
	OpNoop   Op = "noop"
	OpSkip   Op = "skip"
)

// FieldChange describes one attribute difference. Values of the "value"
// field are redacted at construction time and never carried in clear.
type FieldChange struct {
	Field string
	Old   string
	New   string
}

func (f FieldChange) String() string {
	return fmt.Sprintf("%s: %s -> %s", f.Field, f.Old, f.New)
}

// Action is one planned (or skipped) operation on a GitLab variable.
type Action struct {
	Op       Op
	Target   config.TargetRef
	Variable gitlab.Variable // desired state; for OpSkip may carry only Key
	Diff     []FieldChange   // populated for OpUpdate
	Reason   string          // populated for OpSkip
	Err      error           // populated when applying the action failed
}

func (a Action) String() string {
	id := a.Variable.Key
	if a.Variable.EnvironmentScope != "" && a.Variable.EnvironmentScope != "*" {
		id += " (scope " + a.Variable.EnvironmentScope + ")"
	}
	switch a.Op {
	case OpSkip:
		return fmt.Sprintf("skip   %s @ %s: %s", id, a.Target, a.Reason)
	case OpUpdate:
		return fmt.Sprintf("update %s @ %s: %s", id, a.Target, joinChanges(a.Diff))
	case OpCreate:
		return fmt.Sprintf("create %s @ %s", id, a.Target)
	default:
		return fmt.Sprintf("%-6s %s @ %s", a.Op, id, a.Target)
	}
}

func joinChanges(changes []FieldChange) string {
	out := ""
	for i, c := range changes {
		if i > 0 {
			out += ", "
		}
		out += c.String()
	}
	return out
}
