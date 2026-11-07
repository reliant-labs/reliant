package generator

import (
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// `forge project upgrade` has two lanes and they must not both claim a
// file. frontends/<name>/eslint.config.mjs was in both: it printed twice
// under `--check`, once from the managed Tier-2 registration and once from
// the scaffold-once advisory sweep.
//
// The duplicate line is the cosmetic part. The real problem is that the
// lanes disagree about ownership and do not offer the same thing — managed
// refreshes a pristine copy on upgrade and offers `disown`, advisory only
// ever reports — so a user following one lane's remedy is being told a
// different story about the same file than the other lane tells.
//
// Asserted as a set-disjointness property over every frontend kind rather
// than as "eslint is not in the advisory list", so the NEXT file registered
// in both lanes fails here too.
func TestUpgradeLanes_DoNotBothClaimTheSameFile(t *testing.T) {
	for _, kind := range []string{"nextjs", "vite-spa", "react-native"} {
		t.Run(kind, func(t *testing.T) {
			cfg := &config.ProjectConfig{
				Name:       "demo",
				ModulePath: "github.com/example/demo",
				Frontends: []config.FrontendConfig{
					config.FrontendConfig{Name: "web", Type: kind}.WithDir(filepath.Join("frontends", "web")),
				},
			}

			managed := map[string]bool{}
			for _, f := range frontendManagedFiles(cfg) {
				managed[filepath.ToSlash(f.destPath)] = true
			}

			advisory, err := AdvisoryFilesFor(t.TempDir(), cfg)
			if err != nil {
				t.Fatalf("AdvisoryFilesFor: %v", err)
			}

			for _, a := range advisory {
				p := filepath.ToSlash(a.Path)
				if managed[p] {
					t.Errorf("%s is claimed by BOTH upgrade lanes — `upgrade --check` reports it twice, "+
						"and the two lanes offer different remedies for it. Exclude it from the advisory "+
						"lane (perKindAdvisoryEligible) or drop its managed registration.", p)
				}
			}
		})
	}
}

// The exclusion must not go too far the other way: eslint.config.mjs still
// has to be reported by SOMEBODY, which was the original bug that put it in
// a lane at all. Silence here would be a regression to "hand-edit it and no
// forge command ever mentions it".
func TestUpgradeLanes_EslintIsStillClaimedByExactlyOneLane(t *testing.T) {
	cfg := &config.ProjectConfig{
		Name:       "demo",
		ModulePath: "github.com/example/demo",
		Frontends: []config.FrontendConfig{
			config.FrontendConfig{Name: "web", Type: "nextjs"}.WithDir(filepath.Join("frontends", "web")),
		},
	}
	want := "frontends/web/eslint.config.mjs"

	inManaged := false
	for _, f := range frontendManagedFiles(cfg) {
		if filepath.ToSlash(f.destPath) == want {
			inManaged = true
		}
	}
	advisory, err := AdvisoryFilesFor(t.TempDir(), cfg)
	if err != nil {
		t.Fatalf("AdvisoryFilesFor: %v", err)
	}
	inAdvisory := false
	for _, a := range advisory {
		if filepath.ToSlash(a.Path) == want {
			inAdvisory = true
		}
	}

	if !inManaged {
		t.Errorf("%s is no longer a managed file; upgrade would stop refreshing it and stop offering `disown`", want)
	}
	if inAdvisory {
		t.Errorf("%s is still in the advisory lane; it is managed, so this is the duplicate report", want)
	}
}
