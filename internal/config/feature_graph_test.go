package config

import (
	"strings"
	"testing"
)

// withFeatures builds a config whose derived feature set is forced, to drive
// the graph validator with a contradiction no derivation rule would produce.
func withFeatures(db string, force map[FeatureName]bool) *ProjectConfig {
	c := &ProjectConfig{Name: "demo", ModulePath: "github.com/example/demo", Kind: ProjectKindService}
	c.Database.Driver = db
	for name, on := range force {
		c.Features = c.Features.With(name, on)
	}
	return c
}

func requireGraphIssue(t *testing.T, c *ProjectConfig, wants ...string) {
	t.Helper()
	issues := validateFeatureGraph(c)
	if len(issues) == 0 {
		t.Fatal("expected a feature-graph violation, got none")
	}
	var all strings.Builder
	for _, i := range issues {
		all.WriteString(i.msg + " | " + i.fix + "\n")
	}
	for _, want := range wants {
		if !strings.Contains(all.String(), want) {
			t.Errorf("violation missing %q\ngot: %s", want, all.String())
		}
	}
}

func TestFeatureGraph_FrontendRequiresCodegen(t *testing.T) {
	requireGraphIssue(t, withFeatures("postgres", map[FeatureName]bool{FeatureCodegen: false, FeatureFrontend: true}),
		"frontend", "codegen", "disabled")
}

func TestFeatureGraph_ORMRequiresDriver(t *testing.T) {
	requireGraphIssue(t, withFeatures("none", map[FeatureName]bool{FeatureORM: true, FeatureMigrations: false}),
		"orm", "database driver", "db/migrations")
}

func TestFeatureGraph_DeployRequiresBuild(t *testing.T) {
	requireGraphIssue(t, withFeatures("postgres", map[FeatureName]bool{FeatureBuild: false, FeatureDeploy: true}),
		"deploy", "build")
}

func TestFeatureGraph_IngressRequiresDeploy(t *testing.T) {
	requireGraphIssue(t, withFeatures("postgres", map[FeatureName]bool{FeatureDeploy: false, FeatureIngress: true}),
		"ingress", "deploy")
}

func TestFeatureGraph_BatchesMultipleViolations(t *testing.T) {
	c := withFeatures("postgres", map[FeatureName]bool{
		FeatureCodegen: false, FeatureFrontend: true, FeatureBuild: false, FeatureDeploy: true,
	})
	requireGraphIssue(t, c, "frontend", "deploy")
}

// TestDeriveFeatureDefaults_Consistent asserts the DERIVED set is always
// dependency-consistent across every kind: validateFeatureGraph must pass on a
// config the loader produced, whatever is on disk.
func TestDeriveFeatureDefaults_Consistent(t *testing.T) {
	for _, kind := range []string{ProjectKindService, ProjectKindCLI, ProjectKindLibrary} {
		t.Run(kind, func(t *testing.T) {
			c := &ProjectConfig{Name: "demo", ModulePath: "github.com/example/demo", Kind: kind}
			ApplyDerivedDefaults(c)
			if issues := validateFeatureGraph(c); len(issues) > 0 {
				t.Errorf("derived defaults for kind=%s are not dependency-consistent: %+v", kind, issues)
			}
		})
	}
}

// TestDeriveFeatureDefaults_FrontendGatedOnCodegen: a non-service project that
// nonetheless has a frontend must NOT derive frontend=on while codegen=off
// (that would trip the validator).
func TestDeriveFeatureDefaults_FrontendGatedOnCodegen(t *testing.T) {
	c := &ProjectConfig{
		Name: "demo", ModulePath: "github.com/example/demo", Kind: ProjectKindCLI,
		Frontends: []FrontendConfig{{Name: "web"}},
	}
	ApplyDerivedDefaults(c)
	if c.Features.FrontendEnabled() {
		t.Error("frontend should not derive on for a non-service (codegen-off) kind")
	}
	if issues := validateFeatureGraph(c); len(issues) > 0 {
		t.Errorf("derived set should stay consistent: %+v", issues)
	}
}

func TestFeatureDependencies(t *testing.T) {
	if got := FeatureDependencies(FeatureFrontend); len(got) != 1 || got[0] != FeatureCodegen {
		t.Errorf("frontend deps = %v, want [codegen]", got)
	}
	if got := FeatureDependencies(FeatureORM); len(got) != 2 {
		t.Errorf("orm deps = %v, want 2 (codegen + driver)", got)
	}
	if got := FeatureDependencies(FeatureCodegen); len(got) != 0 {
		t.Errorf("codegen deps = %v, want none", got)
	}
}

// FollowingRequirements is the loader's graph rule applied to a feature set
// that has not been loaded — what `forge project new` is about to write a
// tree for. A pin turns off every unpinned dependent, transitively; features
// that do not depend on it, and pins, are left alone; and the set it returns
// loads clean against the graph.
func TestFeaturesConfig_FollowingRequirements(t *testing.T) {
	var f FeaturesConfig
	f, _ = f.Pin(FeatureCodegen, false)
	f, _ = f.Pin(FeatureBuild, false)
	f = f.FollowingRequirements()

	for _, off := range []FeatureName{FeatureCodegen, FeatureORM, FeatureMigrations, FeatureFrontend, FeatureBuild, FeatureDeploy, FeatureIngress} {
		if f.resolve(off) {
			t.Errorf("%s is on; the graph takes it off with codegen/build", off)
		}
	}
	for _, on := range []FeatureName{FeatureCI, FeatureContracts, FeatureObservability, FeatureHotReload} {
		if !f.resolve(on) {
			t.Errorf("%s was turned off, but nothing it requires is", on)
		}
	}
	for _, derived := range []FeatureName{FeatureORM, FeatureMigrations, FeatureFrontend, FeatureDeploy} {
		if f.Pinned(derived) {
			t.Errorf("%s was pinned: the graph derives it, forge.yaml need not say so", derived)
		}
	}
	cfg := &ProjectConfig{Features: f, Database: DatabaseConfig{Driver: "postgres"}}
	if issues := validateFeatureGraph(cfg); len(issues) != 0 {
		t.Errorf("the followed set does not satisfy the graph: %v", issues)
	}

	// A pinned-on dependent is the user's contradiction, reported by the
	// validator — never silently flipped.
	var g FeaturesConfig
	g, _ = g.Pin(FeatureCodegen, false)
	g, _ = g.Pin(FeatureFrontend, true)
	if !g.FollowingRequirements().resolve(FeatureFrontend) {
		t.Error("a pinned-on frontend was turned off")
	}
}
