// File: internal/cli/lint/lint_pending_stubs.go
//
// Pending-stub mode — the one deterministic exception to the two
// unwritten-column rules (forgeconv-read-only-field-unwritten and
// forgeconv-computed-field-unwritten) failing the build.
//
// Both rules fail `forge lint` when a `forge:read-only` or `forge:computed`
// field has no write path. That verdict is right for code a person wrote and
// wrong for the state forge itself leaves behind: right after
// `forge scaffold`, the custom rpc that will set the column (a
// ChangeJobStatus, a RecalculateEstimate) is still forge's own placeholder —
// a method stamped `// forge:gen unwired-stub` that returns Unimplemented.
// Failing the gate on forge's fresh output makes the scaffold phase's
// expected state illegal, so every agent learns to read the read-only lint as
// noise at exactly the moment it is about to matter.
//
// So while the service that declares the entity still has forge-scaffolded
// stubs in its handler package, a finding is a WARNING naming them —
// "pending: implement ChangeJobStatus, ScheduleJob". Once none remain, it is
// an error again.
//
// ── Why the whole package, and not "the rpc that writes this column" ──────
//
// Which rpc writes which column is a dataflow question this lint cannot
// answer from text, and guessing it (by rpc name, by request shape) is the
// name-derived inference forge does not do. The stub marker is a fact forge
// stamped itself, so "does this service still carry forge's placeholders?"
// has a definite answer, and the finding names the stubs it is waiting on so
// the reader can see the reason rather than trust it. The cost of the coarse
// grain is one service with an unrelated stub keeping its findings at
// warning — visible on every run, and cleared by the same act (implementing
// the stub) that clears the scaffold-not-customized warning beside it.

package lint

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
)

// pendingStubs is the forge-scaffolded unwired stubs still present in one
// service's handler package. The zero value means "none" — the finding is
// an error.
type pendingStubs struct {
	// Dir is the handler package, project-relative ("internal/handlers/jobs").
	Dir string
	// Methods are the stubbed rpc names, sorted.
	Methods []string
}

// pending reports whether the finding is held at warning by forge's own
// placeholders.
func (p pendingStubs) pending() bool { return len(p.Methods) > 0 }

// severity is the finding's JSON severity: error, unless forge's own
// placeholders are still in the service.
func (p pendingStubs) severity() string {
	if p.pending() {
		return lintSevWarning
	}
	return lintSevError
}

// label is the short form the brief and the text report lead with:
// "pending: implement ChangeJobStatus, ScheduleJob".
func (p pendingStubs) label() string {
	return "pending: implement " + strings.Join(p.Methods, ", ")
}

// clause is the full explanation appended to a pending finding's message.
// It says why the finding is a warning and when it stops being one, so a
// reader never mistakes a held verdict for a soft rule.
func (p pendingStubs) clause() string {
	return fmt.Sprintf("%s — forge-scaffolded unwired stubs still in %s, so this is a warning for now; "+
		"it fails the build once they are implemented", p.label(), p.Dir)
}

// withPending appends the pending clause to msg when p holds the finding at
// warning, and returns msg unchanged otherwise.
func withPending(msg string, p pendingStubs) string {
	if !p.pending() {
		return msg
	}
	return msg + " (" + p.clause() + ")"
}

// pendingStubsForScan resolves the unwired stubs in the handler package of
// the service one proto directory declares.
//
// The mapping is the generate pipeline's own: scan.ServiceName is the
// service block in that directory, and codegen.ResolveServiceComponent is
// the disk-first resolver `forge generate` uses to find the handler
// directory it scaffolds stubs into. A directory that declares no service,
// or a service with no handler directory on disk, has no forge placeholders
// to wait on — the zero value, so its findings stay errors.
func pendingStubsForScan(projectDir string, scan *codegen.RawProtoScan) pendingStubs {
	if scan == nil || scan.ServiceName == "" {
		return pendingStubs{}
	}
	comp, err := codegen.ResolveServiceComponent(projectDir, scan.ServiceName)
	if err != nil || !comp.FromDisk {
		return pendingStubs{}
	}
	stubs := codegen.ScanUnwiredStubMethods(comp.Dir)
	if len(stubs) == 0 {
		return pendingStubs{}
	}
	methods := make([]string, 0, len(stubs))
	for m := range stubs {
		methods = append(methods, m)
	}
	sort.Strings(methods)
	return pendingStubs{
		Dir:     filepath.ToSlash(relToProject(projectDir, comp.Dir)),
		Methods: methods,
	}
}

// anyErrorFinding reports whether fs holds an error-severity finding — the
// gating verdict for a lane whose findings carry their own severity.
func anyErrorFinding(fs []lintJSONFinding) bool {
	for _, f := range fs {
		if f.Severity == lintSevError {
			return true
		}
	}
	return false
}
