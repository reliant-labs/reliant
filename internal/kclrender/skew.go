package kclrender

import (
	"fmt"

	"github.com/reliant-labs/forge/internal/forgecompat"
)

// Version skew between the running binary and the project's forge pin, at
// the one seam every render passes through.
//
// forge's KCL schema module is embedded in the binary (internal/kclvendor),
// so the schema a render evaluates against comes from the BINARY, not from
// the project's module graph. Render a project pinned to v0.1.43 with a
// v0.1.42 binary and KCL reports the field v0.1.43 added as an error on the
// project's line:
//
//	deploy/kcl/dev/main.k:2933: Cannot add member 'runtime_type' to schema 'Workload'
//
// True about what KCL saw, wrong about whose mistake it is, and it names
// neither version. It cost an agent real time reading line 2933 as the
// fault.
//
// Two guards, because they fail differently. skewPreflight refuses up front,
// which is right whenever the skew is comparable: a stale schema cannot
// render a project that uses anything added since, and failing before the
// render keeps KCL's misattributing message off the screen entirely.
// annotateRenderErr is the backstop for everything the preflight cannot
// prove — it adds nothing when there is no skew, so an unrelated render bug
// reads exactly as it did before.

// skewPreflight refuses the render when the binary is older than the
// project's pin.
//
// Older-than-pin is a refusal and newer-than-pin is not, because the two are
// not symmetric: a newer binary has every schema field the project's pin
// declared, while an older one is missing fields the project is entitled to
// use. The existing forge_version nudge (internal/cli.stepAnnounceProject)
// already covers the newer direction as a warning.
//
// Parameters rather than direct buildinfo/project lookups so the decision
// table is testable without a project on disk or a stamped binary.
func skewPreflight(pinnedVersion, binaryVersion string) error {
	if forgecompat.SkewOverridden() {
		return nil
	}
	diagnosis := forgecompat.SkewDiagnosis(pinnedVersion, binaryVersion)
	if diagnosis == "" {
		return nil
	}
	return fmt.Errorf("%s\n  To render anyway (this will misreport schema errors against your own\n"+
		"  KCL line numbers): %s=1",
		diagnosis, forgecompat.SkewOverrideEnv)
}

// annotateRenderErr puts the version skew in FRONT of a failed render's own
// message.
//
// Leading, not trailing. What cost time was reading KCL's line number as the
// fault; a diagnosis appended after it gets read second, if at all. The
// underlying error stays wrapped so errors.Is still matches it — callers
// branch on render failures and an annotation must not break that.
//
// Returns err unchanged when there is no comparable skew, including when err
// is nil. An annotation layer that reworded every failure would make every
// other render bug harder to read.
func annotateRenderErr(err error, pinnedVersion, binaryVersion string) error {
	if err == nil {
		return nil
	}
	diagnosis := forgecompat.SkewDiagnosis(pinnedVersion, binaryVersion)
	if diagnosis == "" {
		return err
	}
	return fmt.Errorf("KCL render failed, and the most likely cause is a forge version skew:\n  %s\n\n"+
		"  The render's own error follows; its line number is where the missing field was\n"+
		"  USED, not where the problem is:\n  %w", diagnosis, err)
}
