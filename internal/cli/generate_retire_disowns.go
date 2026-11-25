// Auto-retire obsolete disowns.
//
// `forge project disown` is a last-resort one-way escape hatch: it tells forge
// "stop regenerating this Tier-1 file, the bytes are mine now". Needing
// it routinely means a missing extension point. A disown that has become
// OBSOLETE — forge no longer Tier-1-owns that path — is dead weight: it
// protects against an overwrite that can no longer happen, and it
// misleads users into thinking disown is normal.
//
// The canonical case: a project disowned a frontend page.tsx under an
// OLD forge that regenerated page.tsx as Tier-1. The current forge makes
// page.tsx Tier-2 (scaffold-once: written if absent, NEVER overwritten).
// The disown is now meaningless — forge would never overwrite a Tier-2
// file anyway — yet it lingered across version migrations and had to be
// hand-dropped.
//
// THE FIX: during `forge generate`, after every Tier-1 emitter has run,
// detect disowns whose path is NO LONGER a current Tier-1 emit target
// and auto-retire them (remove from .forge/disowned.json) with a loud
// per-path notice. A disown is:
//
//   - STILL VALID when its path IS a current Tier-1 emit target — forge
//     WOULD regenerate it but for the disown. The target set
//     (checksums.Tier1TargetSet) records every path a Tier-1 writer
//     touched this run, BEFORE the disown-skip, so a disowned-but-live
//     Tier-1 path is still in the set. Leave it alone.
//   - OBSOLETE when its path is NOT a current Tier-1 emit target — it
//     became a Tier-2 scaffold-once file, or forge stopped emitting it.
//     Retire it.
//
// Conservatism: a disowned path can be absent from the target set for a
// reason unrelated to tiering — the emitter that WOULD own it was gated
// OFF this run (e.g. a frontend file under features.frontend=false). In
// that case absence is uninformative and we must NOT retire. The
// `targetable` predicate below gates retirement on "an emitter that
// could own this path as Tier-1 actually ran this run", so a gated-off
// subsystem never sheds its legitimate disowns.
package cli

import (
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// stepRetireObsoleteDisowns drops disowns whose path forge no longer
// Tier-1-owns. Positioned LATE in the plan (after every emitter, before
// the rehash/save) so checksums.Tier1TargetSet is fully populated.
func stepRetireObsoleteDisowns(ctx *pipelineContext) error {
	checksums.RetireObsoleteDisowns(ctx.Checksums, retirementTargetable(ctx))
	return nil
}

// retirementTargetable returns the predicate RetireObsoleteDisowns uses
// to decide whether a disowned path's ABSENCE from the Tier-1 target set
// is meaningful. It returns true only when an emitter that could own the
// path as Tier-1 actually ran this run — otherwise the path was never
// going to be in the target set regardless of its tier, and retiring on
// that basis would drop a legitimate disown.
//
// The path is bucketed by location into the broad emit subsystem that
// owns it; the subsystem's enabling gate decides whether it ran. Buckets
// mirror the pipeline's coarse feature gates (codegen, frontend, deploy).
// A path matching no bucket is treated as NON-targetable (return false):
// forge can't prove an owning emitter ran, so it keeps the disown — the
// conservative posture the prompt demands ("when in doubt, keep").
//
// Reconciled paths short-circuit ahead of the buckets, because for them the
// whole premise is wrong rather than merely unproven: forge edits them in
// place while never listing them as Tier-1 targets, so their absence from the
// set says nothing about whether the disown still does work.
// reconciledPaths are scaffold-once files forge EDITS IN PLACE rather than
// regenerates: it splices new accessors and fields into them as components are
// discovered. They are never Tier-1 emit targets, so the "absent from the
// target set ⇒ obsolete" inference does not hold — a disown on one of these is
// load-bearing, and it is the only thing standing between the reconciler and a
// composition root the user deliberately took ownership of. Retiring it would
// hand those bytes back silently.
var reconciledPaths = []string{
	"internal/app/compose.go",
	"internal/app/lifecycle.go",
}

func retirementTargetable(ctx *pipelineContext) func(string) bool {
	return func(relPath string) bool {
		for _, p := range reconciledPaths {
			if relPath == p {
				return false
			}
		}
		switch {
		case strings.HasPrefix(relPath, "frontends/"):
			// Frontend Tier-1 emitters (hooks, pages, nav) run only when
			// the frontend feature is on AND the project actually has
			// frontends/services to emit for. If neither runs, a frontend
			// file's absence from the target set says nothing about its
			// tier — keep the disown.
			return gateFrontendHasFrontends(ctx) || gateFrontendHasServices(ctx)
		case hasAnyPrefix(relPath, serviceDrivenCodegenPrefixes):
			// Service-derived Go Tier-1 emitters (handlers, ORM,
			// bootstrap, cmd subcommands, middleware, mocks). All require
			// the codegen feature AND at least one service. With no
			// services none of these emitters runs, so absence from the
			// target set is uninformative.
			return gateCodegenEnabled(ctx) && ctx.HasServices
		case hasAnyPrefix(relPath, deployDrivenPrefixes):
			// Infra / deploy Tier-1 emitters run under the deploy feature.
			return gateDeployEnabled(ctx)
		default:
			// Unknown location — can't prove an owning emitter ran.
			// Keep the disown (conservative).
			return false
		}
	}
}

func hasAnyPrefix(relPath string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(relPath, p) {
			return true
		}
	}
	return false
}

// serviceDrivenCodegenPrefixes are the trees whose Tier-1 output is
// produced by the codegen emitters that gate on HasServices.
var serviceDrivenCodegenPrefixes = []string{
	"internal/handlers/",
	"internal/db/",
	"pkg/app/",
	"pkg/middleware/",
	"pkg/config/",
	"cmd/",
	"db/",
	"gen/",
}

// deployDrivenPrefixes are the trees whose Tier-1 output is produced by
// the deploy-feature emitters (infra files, KCL, components manifest).
var deployDrivenPrefixes = []string{
	"deploy/",
}
