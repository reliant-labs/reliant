package generator

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// sourcedFrontendCfg is a project whose only frontend's code lives in
// ANOTHER repository, pinned by ref. It has no directory in this tree by
// design — that is the entire point of a `source:` pin, and it is what
// lets the frontend build in a CI checkout of this repo alone.
func sourcedFrontendCfg() *config.ProjectConfig {
	return &config.ProjectConfig{
		Name:       "demo",
		ModulePath: "github.com/example/demo",
		Frontends: []config.FrontendConfig{{
			Name:   "sibling-web",
			Type:   "nextjs",
			Source: &config.GitSource{Repo: "github.com/example/sibling", Ref: "v1.0.0"},
		}},
	}
}

// TestFrontendManagedSkipsSourcedFrontend pins a DELIBERATE behavior
// change made when FrontendConfig.Path was unexported.
//
// The retired EffectivePath returned the conventional frontends/<name>
// label for a frontend whose code is in another repository, and this
// lane used it unguarded. So forge registered a MANAGED file at
// frontends/sibling-web/eslint.config.mjs — a path no frontend occupies.
// Managed files are written and rewritten on every upgrade, and the
// stale-artifact sweep then keeps the invented file alive, so forge would
// create and maintain a lint config for a frontend that is not there.
//
// The real tree belongs to another repository, whose own forge manages
// its lint config. Skipping is the correct answer.
func TestFrontendManagedSkipsSourcedFrontend(t *testing.T) {
	t.Parallel()
	for _, f := range frontendManagedFiles(sourcedFrontendCfg()) {
		t.Errorf("registered managed file %q for a frontend whose code is in another "+
			"repository — frontends/<name> is an invented directory here", f.destPath)
	}
}

// TestFrontendAdvisorySkipsSourcedFrontend is the same change on the
// advisory lane, where the consequence is a report rather than a write:
// every template file was compared against a tree that does not exist,
// so the whole frontend was reported as drifted-or-missing on every
// `forge upgrade`. An advisory about a directory that is not there is
// noise, and it is noise about files this project does not own.
func TestFrontendAdvisorySkipsSourcedFrontend(t *testing.T) {
	t.Parallel()
	rows, err := frontendAdvisoryFiles(t.TempDir(), sourcedFrontendCfg())
	if err != nil {
		t.Fatalf("frontendAdvisoryFiles: %v", err)
	}
	for _, r := range rows {
		if strings.Contains(r.Path, "sibling-web") {
			t.Errorf("advisory row %q for a frontend whose code is in another repository", r.Path)
		}
	}
	if len(rows) != 0 {
		t.Errorf("got %d advisory rows for a project whose only frontend is cross-repo, want 0", len(rows))
	}
}
