// File: internal/cli/lint/lint_hosted_image_base.go
//
// hosted-image-base — reports a host-bearing image on a forge.OnHosted item.
//
// A hosted workload's registry is NOT the author's to choose (ADR-0003 F1):
// the platform admits images from exactly one subtree,
// `<registry_host>/<org>/<project>`, and refuses every other. So
// forge resolves a bare `image = "api"` against the base composed from the org
// the credential acts for,
// and an author who writes a host is transcribing a value forge already
// composes — which is fine when they get it right and a publish-time refusal
// when they do not.
//
// This is where that is noticed early. `forge env render <env>` performs the
// authoritative version of the same check against the real render; this one
// is the cheap, offline approximation over the deploy KCL text, so it covers
// every env in the project rather than the one someone is deploying.
//
// AN OFF-BASE IMAGE IS A WARNING, and the asymmetry with render is
// deliberate. A render is what a deploy applies, so manifests the platform
// will reject make that render a document nobody can act on — it fails. A
// lint judges a project, including envs nobody is deploying, where the same
// fact is something to fix rather than something to stop for. And the text
// scan under-reports by construction (internal/hostedimage/scan.go says how),
// so it is not a gate anything should depend on being complete.
//

package lint

import (
	"path/filepath"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/hostedimage"
)

// hostedImageBaseFindings scans the project's deploy KCL for every host-bearing
// image on a hosted item.
//
// THERE IS NO BASE TO JUDGE AGAINST HERE, and that is the point of the design
// rather than a gap in it: the org half of the push base is the credential's,
// and lint runs offline with no credential. So every finding is UNVERIFIED —
// "this host-bearing image is on an OnHosted workload; the control plane admits
// only its own registry". `forge env render <env>` does the verified version,
// against the base composed from the org the credential acts for.
func hostedImageBaseFindings(projectDir string, cfg *config.ProjectConfig) []hostedimage.Finding {
	items := hostedimage.ScanTree(filepath.Join(projectDir, deployKCLDirFor(cfg)))
	if len(items) == 0 {
		return nil
	}
	return hostedimage.OffBase(items, "")
}

// deployKCLDirFor is the project's deploy KCL tree.
func deployKCLDirFor(cfg *config.ProjectConfig) string {
	if cfg != nil && cfg.K8s.KCLDir != "" {
		return cfg.K8s.KCLDir
	}
	return deployKCLDirDefault
}

// collectHostedImageBaseJSON is the JSON arm. Nothing here gates.
func collectHostedImageBaseJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	findings := hostedImageBaseFindings(rc.cwd, rc.cfg)
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, lintJSONFinding{
			File:     deployKCLDirFor(rc.cfg),
			Severity: lintSevWarning,
			Rule:     "hosted-image-base/host-bearing-image",
			Message:  f.Message(),
			FixHint:  f.FixHint(),
		})
	}
	return out, false, nil
}
