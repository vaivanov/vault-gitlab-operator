package sync

import (
	"fmt"
	"io"
	"time"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
)

// TargetResult holds the outcome for one sync target.
type TargetResult struct {
	Target  config.TargetRef
	Actions []Action
	// Err is a target-level failure (resolution or listing) that
	// prevented any action planning.
	Err error
}

// Report is the outcome of one reconcile run.
type Report struct {
	Targets  []TargetResult
	Duration time.Duration
	DryRun   bool
}

// Counts aggregates action outcomes across all targets.
type Counts struct {
	Created, Updated, Unchanged, Skipped, Failed, TargetErrors int
}

// Counts aggregates action outcomes across all targets of the run.
func (r *Report) Counts() Counts {
	var c Counts
	for _, t := range r.Targets {
		if t.Err != nil {
			c.TargetErrors++
		}
		for _, a := range t.Actions {
			switch {
			case a.Err != nil:
				c.Failed++
			case a.Op == OpCreate:
				c.Created++
			case a.Op == OpUpdate:
				c.Updated++
			case a.Op == OpNoop:
				c.Unchanged++
			case a.Op == OpSkip:
				c.Skipped++
			}
		}
	}
	return c
}

// HasErrors reports whether anything failed (apply errors, target errors,
// or skips when on_masked_violation/error-class skips count as failures —
// the caller decides that policy; skips are not errors here).
func (r *Report) HasErrors() bool {
	c := r.Counts()
	return c.Failed > 0 || c.TargetErrors > 0
}

// HasChanges reports whether any create/update is pending or was applied.
func (r *Report) HasChanges() bool {
	c := r.Counts()
	return c.Created > 0 || c.Updated > 0
}

// Render writes a human-readable summary. With verbose, noop actions are
// listed too; otherwise only creates, updates, skips and errors.
func (r *Report) Render(w io.Writer, verbose bool) {
	for _, t := range r.Targets {
		if t.Err != nil {
			fmt.Fprintf(w, "error  %s: %v\n", t.Target, t.Err)
			continue
		}
		for _, a := range t.Actions {
			if a.Err != nil {
				fmt.Fprintf(w, "failed %s @ %s: %v\n", a.Variable.Key, a.Target, a.Err)
				continue
			}
			if a.Op == OpNoop && !verbose {
				continue
			}
			fmt.Fprintln(w, a.String())
		}
	}

	c := r.Counts()
	mode := "applied"
	if r.DryRun {
		mode = "planned"
	}
	fmt.Fprintf(w, "%s: %d created, %d updated, %d unchanged, %d skipped, %d failed, %d target error(s) in %s\n",
		mode, c.Created, c.Updated, c.Unchanged, c.Skipped, c.Failed, c.TargetErrors, r.Duration.Round(time.Millisecond))
}
