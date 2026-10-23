package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/doctor"
	"github.com/reliant-labs/forge/internal/forgecompat"
)

// The local-forge bridge: a project compiling forge from a checkout on disk
// (a go.work `use`) instead of a published version.
//
// It is OPT-IN. `forge project new` used to write it on its own whenever the
// scaffolding binary was a dev build — including a host binary that embeds
// forge through a workspace, which bridged every project it created to
// whatever checkout that build happened to compile from. On a shared machine
// that is the main checkout everyone pulls into, so the library under a
// project moved every time someone merged, while the binary generating its
// code stayed put. Now the bridge exists only because someone asked for it:
// `forge project new --link-forge`, or `go work use <forge-checkout>` by hand.
// The go.work line IS the record of that decision; generate's npm twin
// (.forge-link/) follows it rather than deciding again.
//
// And because a bridge can drift even when it was asked for, generate, lint
// and doctor say so whenever this binary is not built from the bridged
// checkout as it is now (forgecompat.InspectBridge).

// linkForgeEnv is --link-forge for harnesses that scaffold many projects
// (forge's own e2e corpus). Any non-empty value opts in.
const linkForgeEnv = "FORGE_LINK_FORGE"

// linkForgeRequested reports whether this scaffold opts into the bridge.
func linkForgeRequested(flag bool) bool {
	return flag || strings.TrimSpace(os.Getenv(linkForgeEnv)) != ""
}

// bridgeSkewCommands are the top-level commands whose output depends on the
// binary and the bridged library agreeing: generate writes code against the
// library, lint judges code against it.
var bridgeSkewCommands = map[string]bool{"generate": true, "lint": true}

// bridgeSkewChecked reports whether cmd is one of those commands, mounted
// directly under forge's root (standalone or embedded).
func bridgeSkewChecked(cmd, root *cobra.Command) bool {
	return cmd.Parent() == root && bridgeSkewCommands[cmd.Name()]
}

// warnBridgeSkew prints the one-line skew warning for projectDir, and nothing
// when the project is unbridged or in sync.
func warnBridgeSkew(w io.Writer, projectDir string) {
	if line := forgecompat.InspectBridge(projectDir).Line(); line != "" {
		_, _ = fmt.Fprintln(w, line)
	}
}

const bridgeCheckName = "Forge Bridge"

// runBridgeDoctorCheck is doctor's view of the same check. It has no
// --signal of its own: none of deploy/metrics/traces/logs/profiles is about
// which forge generated the code.
func runBridgeDoctorCheck(projectDir, signal string) []doctor.CheckResult {
	if signal != "" {
		return nil
	}
	start := time.Now()
	r := bridgeCheckResult(forgecompat.InspectBridge(projectDir))
	r.Duration = time.Since(start)
	return []doctor.CheckResult{r}
}

// bridgeCheckResult renders a BridgeReport as a doctor check. Pure.
func bridgeCheckResult(rep forgecompat.BridgeReport) doctor.CheckResult {
	r := doctor.CheckResult{Name: bridgeCheckName}
	switch {
	case !rep.Bridged:
		r.Status = doctor.StatusSkip
		r.Message = "no local forge bridge: forge resolves from its published version"
	case !rep.CheckoutOK:
		r.Status = doctor.StatusUnknown
		r.Message = fmt.Sprintf("%s, which is not a readable git checkout — cannot tell whether this binary matches it", rep.Bridge)
	case rep.Skewed:
		r.Status = doctor.StatusWarn
		r.Message = strings.TrimPrefix(rep.Line(), "⚠️  forge skew: ")
		r.Evidence = bridgeEvidence(rep)
	default:
		r.Status = doctor.StatusPass
		r.Message = fmt.Sprintf("%s at %s; %s was built from that source", rep.Bridge, shortRev(rep.Checkout.Head), rep.Binary.Name)
		r.Evidence = bridgeEvidence(rep)
	}
	return r
}

func bridgeEvidence(rep forgecompat.BridgeReport) string {
	b, c := rep.Binary, rep.Checkout
	rev := b.Revision
	if rev == "" {
		rev = "none recorded"
	}
	if b.Modified {
		rev += " (dirty tree)"
	}
	built := "unknown"
	if !b.BuiltAt.IsZero() {
		built = b.BuiltAt.Format(time.RFC3339)
	}
	root := b.Root
	if root == "" {
		root = "unknown"
	}
	moved := "unknown"
	if !c.HeadMovedAt.IsZero() {
		moved = c.HeadMovedAt.Format(time.RFC3339)
	}
	return fmt.Sprintf("binary %s: forge revision %s, built %s, compiled from %s\ncheckout %s: HEAD %s (moved %s), dirty=%t\ndeclared in %s",
		b.Name, rev, built, root, c.Dir, c.Head, moved, c.Dirty, rep.Bridge.File)
}

func shortRev(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
