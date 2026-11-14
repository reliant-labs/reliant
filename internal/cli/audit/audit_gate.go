// File: internal/cli/audit/audit_gate.go
//
// The exit code of `forge project audit`.
//
// Every category in this group already computes a severity, and
// rollupStatus already collapses them into one verdict. What was missing
// was that nothing consumed the verdict: runAudit printed the report and
// returned nil, so a category could render
//
//	✗ unscoped_auth — 23 authenticated RPC(s) over forge:owner-declared
//	  data never resolve the caller
//
// and the command still exited 0. Nothing in CI, and no agent reading an
// exit status, ever saw it. A dogfood run believed it had escaped this
// with `forge project audit --strict`; no such flag exists, so what it
// measured was cobra rejecting an unknown flag. There was no way at all
// to make an audit finding fail a build.
//
// # Severity IS the predicate — there is no second switch
//
// The obvious alternative was a --strict flag that escalates warnings.
// It is the wrong shape here, for the reason unscoped_auth was designed
// the way it was: this group already distinguishes "unscoped" from
// "unsafe", and it does so with a declaration the DEVELOPER wrote. A
// table saying `forge:owner` is what turns that category's warning into
// an error. Adding a flag that also escalates warnings would give one
// question two answers — the project's declarations and the caller's
// flag — and CI would then have to pick. Picking --strict re-breaks the
// fresh scaffold, which is entirely unscoped by construction and would
// go red on day one. Picking no flag restores exactly the silence this
// fixes.
//
// So the gate is unconditional and reads only the severity the
// categories already assign:
//
//	error → exit non-zero
//	warn  → exit ZERO, finding still printed
//
// Arming stays where it belongs, in the project's own migrations, and a
// greenfield project keeps a green audit until it declares that some row
// belongs to someone.
//
// # Why not put this in forge lint instead
//
// Surfacing unscoped_auth as a `forge lint` step was the other candidate,
// since lint is the command CI runs. It was rejected as the PRIMARY fix:
// it moves one category and leaves every other audit error — a missing
// forge.yaml, an unsatisfiable secret mount — still exiting 0, which is
// the same defect with a smaller blast radius. Fixing the verdict at the
// report level covers the whole surface at once. `forge project audit` is
// a documented command with a documented report; making it honest about
// its own findings is the smaller change and the more complete one.

package audit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
)

// gateError is the verdict `forge project audit` returns when at least
// one category reported StatusError. It carries the failing category
// names so the message is falsifiable: the reader is told WHICH check
// failed rather than being handed a bare non-zero exit and left to
// re-read the report.
type gateError struct {
	// categories are the StatusError category keys, sorted.
	categories []string
	msg        string
}

func (e *gateError) Error() string { return e.msg }

// gateOnReport converts a finished report into the command's error
// return: non-nil exactly when some category is StatusError.
//
// It reads the per-category statuses rather than r.OverallStatus alone,
// because the message has to name the failures — but the two cannot
// disagree, since OverallStatus is rollupStatus over the same map.
func gateOnReport(r *Report) error {
	var failing []string
	for name, cat := range r.Categories {
		if cat.Status == audittype.StatusError {
			failing = append(failing, name)
		}
	}
	if len(failing) == 0 {
		return nil
	}
	sort.Strings(failing)

	return &gateError{
		categories: failing,
		msg: cliutil.UserErr(
			cmdutil.Name()+" project audit",
			fmt.Sprintf("%d categor%s reported errors: %s",
				len(failing), plural(len(failing)), strings.Join(failing, ", ")),
			"",
			"address the ✗ categories above, or record the deliberate exception the category "+
				"names in its hint; warnings (⚠) do not fail this command",
		).Error(),
	}
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
