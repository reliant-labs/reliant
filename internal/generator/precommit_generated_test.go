package generator

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

// Pre-commit's MUTATING hooks never touch a forge-generated file.
//
// forge ci verify-generated regenerates the tree and demands byte-identical
// output, so a formatter that rewrites a generated file makes the two gates
// fight on every commit. houndersclub's first PR: trailing-whitespace
// rewrote config_gen.k, end-of-file-fixer rewrote frontend_config_gen.k and
// the grafana dashboards, prettier rewrote membership-service-hooks_gen.ts —
// and each rewrite was then "drift" to verify-generated.
//
// The generated paths below are every forge-owned file in a fresh
// `forge project new --service item --frontend web` (the files carrying a
// forge:hash marker, plus the dashboards, gen/ stubs and the hooks barrel),
// and the hand-written paths are files a formatter MUST still own.
func TestForgeGeneratedPathPattern(t *testing.T) {
	g := &ProjectGenerator{Name: "demo", Path: t.TempDir()}
	if err := g.generatePreCommitConfig(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(g.Path, ".pre-commit-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Repos []struct {
			Hooks []struct {
				ID      string `yaml:"id"`
				Files   string `yaml:"files"`
				Exclude string `yaml:"exclude"`
			} `yaml:"hooks"`
		} `yaml:"repos"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf(".pre-commit-config.yaml is not valid YAML: %v", err)
	}

	generated := []string{
		"gen/services/item/v1/item.pb.go",
		"frontends/web/src/gen/services/item/v1/item_pb.ts",
		"cmd/demo/cmd/services/item_mount_gen.go",
		"internal/handlers/item/helpers_gen_test.go",
		"pkg/config/config_gen.go",
		"deploy/kcl/config_gen.k",
		"deploy/kcl/frontend_config_gen.k",
		"frontends/web/public/config.js",
		"frontends/web/src/lib/config_gen.ts",
		"frontends/web/src/hooks/membership-service-hooks_gen.ts",
		"frontends/web/src/hooks/index.ts",
		"frontends/web/src/mocks/fixture-freshness_gen.test.ts",
		".forge-kcl/forge/schema.k",
	}
	handWritten := []string{
		"frontends/web/src/components/pricing.tsx",
		"frontends/web/src/hooks/use-session.ts",
		"frontends/web/src/lib/config.ts",
		"internal/handlers/item/service.go",
		"deploy/kcl/prod/main.k",
		"README.md",
		".github/workflows/ci.yml",
	}

	mutating := map[string]bool{
		"trailing-whitespace": true, "end-of-file-fixer": true,
		"go-fmt": true, "go-imports": true, "prettier": true,
	}
	seen := 0
	for _, repo := range cfg.Repos {
		for _, h := range repo.Hooks {
			if !mutating[h.ID] {
				continue
			}
			seen++
			files := regexp.MustCompile(".*")
			switch {
			case h.Files != "":
				files = regexp.MustCompile(h.Files)
			case h.ID == "go-fmt" || h.ID == "go-imports":
				// dnephin's hook manifest scopes these with `types: [go]`.
				files = regexp.MustCompile(`\.go$`)
			}
			if h.Exclude == "" {
				t.Errorf("mutating hook %s has no exclude", h.ID)
				continue
			}
			exclude, err := regexp.Compile(h.Exclude)
			if err != nil {
				t.Errorf("hook %s exclude does not compile: %v", h.ID, err)
				continue
			}
			for _, p := range generated {
				if files.MatchString(p) && !exclude.MatchString(p) {
					t.Errorf("mutating hook %s would rewrite forge-generated %s", h.ID, p)
				}
			}
			for _, p := range handWritten {
				if files.MatchString(p) && exclude.MatchString(p) {
					t.Errorf("hook %s excludes hand-written %s — the formatter must still own it", h.ID, p)
				}
			}
		}
	}
	if seen != len(mutating) {
		t.Errorf("found %d of %d mutating hooks — did one get renamed?", seen, len(mutating))
	}
}
