package cli

import (
	"context"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// A build-only workload that declares build_variants builds exactly those
// variants. Its GoBuild names the package the variants build from; it is not a
// binary of its own. Before the fix the plan (and the build) also ran a host
// `go build` to bin/<name> with forge's default stamps — an artifact nobody
// asked for, which no release records, followed by a "metrics schema skipped"
// warning from trying to run it (bug f905c063).
func TestBuildPlan_BuildOnlyVariantsBuildNoHostBinary(t *testing.T) {
	goBuild := BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/acme"}}
	entities := &KCLEntities{Workloads: []WorkloadEntity{
		{
			Name: "acme-cli", Kind: "tool", Build: goBuild,
			Runtime: RuntimeEntity{Type: RuntimeBuildOnly, BuildOnly: &BuildOnlyDeploy{BuildVariants: []BuildVariant{
				{Name: "linux-amd64", GOOS: "linux", GOARCH: "amd64"},
				{Name: "darwin-arm64", GOOS: "darwin", GOARCH: "arm64"},
			}}},
		},
		// A build-only workload WITHOUT variants is still its own binary.
		{
			Name: "acme-tool", Kind: "tool",
			Build:   BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/tool"}},
			Runtime: RuntimeEntity{Type: RuntimeBuildOnly, BuildOnly: &BuildOnlyDeploy{}},
		},
	}}
	cfg := &config.ProjectConfig{Name: "acme"}

	goTargets := resolveGoTargets(true, entities, cfg)
	if len(goTargets) != 1 || goTargets[0].outputName != "acme-tool" {
		t.Fatalf("go targets = %+v, want only acme-tool (acme-cli builds through its variants alone)", goTargets)
	}

	report := planBuild(context.Background(), planInputs{
		cfg:           cfg,
		entities:      entities,
		targets:       buildTargetSet{goTargets: goTargets},
		opts:          buildOptions{env: "release"},
		projectDir:    t.TempDir(),
		goPackageName: func(context.Context, goBuildTarget) (string, error) { return "main", nil },
	})
	var services, variants []string
	for _, s := range report.steps {
		switch s.kind {
		case "service":
			services = append(services, s.name)
		case "variant":
			variants = append(variants, s.name)
		}
	}
	if len(services) != 1 || services[0] != "acme-tool" {
		t.Errorf("service steps = %v, want [acme-tool]: a variants-only workload must not plan a host binary", services)
	}
	if len(variants) != 2 || variants[0] != "acme-cli:linux-amd64" || variants[1] != "acme-cli:darwin-arm64" {
		t.Errorf("variant steps = %v, want acme-cli's two variants", variants)
	}
}
