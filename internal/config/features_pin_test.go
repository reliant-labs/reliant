package config

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// loadTreeWith loads root with the given forge.yaml body appended to the
// identity keys.
func loadTreeWith(t *testing.T, root, extra string) (*ProjectConfig, error) {
	t.Helper()
	write(t, root, "forge.yaml", "name: demo\nmodule_path: example.com/demo\n"+extra)
	return LoadProjectDir(root)
}

// cliTree is reliant's shape before P3b: a cmd/ binary and protos, nothing a
// server scaffold writes. Adopting one service creates internal/handlers/.
func cliTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "cmd/app/main.go", "package main\n\nfunc main() {}\n")
	write(t, root, "proto/app/v1/app.proto", "syntax = \"proto3\";\n")
	return root
}

// TestFeatures_StructuralChangeTurnsDerivedObservabilityOn pins the F15
// mechanism the pin exists for: internal/handlers/ appearing flips the
// derived kind to service and turns observability and hot_reload on.
func TestFeatures_StructuralChangeTurnsDerivedObservabilityOn(t *testing.T) {
	root := cliTree(t)
	before, err := loadTreeWith(t, root, "")
	if err != nil {
		t.Fatal(err)
	}
	if before.EffectiveKind() != ProjectKindCLI || before.Features.ObservabilityEnabled() {
		t.Fatalf("precondition: a cmd/ binary with protos is cli-kind with observability off (kind=%s)", before.EffectiveKind())
	}
	write(t, root, "internal/handlers/question/service.go", "package question\n")
	after, err := loadTreeWith(t, root, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.EffectiveKind() != ProjectKindService || !after.Features.ObservabilityEnabled() || !after.Features.HotReloadEnabled() {
		t.Fatalf("internal/handlers/ should derive a service with observability and hot_reload on")
	}
}

// TestFeatures_PinHoldsAcrossTheStructuralChange: forge.yaml's features:
// block is the switch. A pinned feature stays where it was pinned whatever
// the tree grows, and `forge project features` can say it was pinned.
func TestFeatures_PinHoldsAcrossTheStructuralChange(t *testing.T) {
	root := cliTree(t)
	write(t, root, "internal/handlers/question/service.go", "package question\n")
	cfg, err := loadTreeWith(t, root, "features:\n  observability: false\n  hot_reload: false\n")
	if err != nil {
		t.Fatalf("a features: block must load: %v", err)
	}
	if cfg.Features.ObservabilityEnabled() || cfg.Features.HotReloadEnabled() {
		t.Error("a pinned-off feature must stay off when the tree derives it on")
	}
	if !cfg.Features.Pinned(FeatureObservability) || cfg.Features.Pinned(FeatureDeploy) {
		t.Error("Pinned must report exactly the keys the block sets")
	}
	if !cfg.Features.CodegenEnabled() {
		t.Error("an unpinned feature still derives from the tree")
	}

	// And on: a pin turns a feature on that the tree does not imply.
	on, err := loadTreeWith(t, cliTree(t), "features:\n  observability: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !on.Features.ObservabilityEnabled() {
		t.Error("a pinned-on feature must be on")
	}
}

// A derived feature follows its requirement off when a pin turns that off,
// instead of loading as a feature-graph error against values nobody wrote.
// A PINNED dependent whose requirement is off is a contradiction the user
// wrote, and fails the load naming both.
func TestFeatures_DerivedDependentsFollowAPinnedRequirement(t *testing.T) {
	root := serviceTree(t)
	write(t, root, "deploy/kcl/dev/main.k", "gw = forge.Gateway {\n  name = \"edge\"\n}\n")
	derived, err := loadTreeWith(t, root, "")
	if err != nil {
		t.Fatal(err)
	}
	if !derived.Features.IngressEnabled() || !derived.Features.ORMEnabled() {
		t.Fatal("precondition: the tree derives ingress and orm on")
	}
	cfg, err := loadTreeWith(t, root, "features:\n  deploy: false\n  codegen: false\n")
	if err != nil {
		t.Fatalf("pinning a requirement off must not fail the load on its derived dependents: %v", err)
	}
	for _, name := range []FeatureName{FeatureDeploy, FeatureIngress, FeatureCodegen, FeatureORM, FeatureMigrations} {
		if cfg.Features.resolve(name) {
			t.Errorf("%s should be off: pinned, or derived and requiring a pinned-off feature", name)
		}
	}

	_, err = loadTreeWith(t, serviceTree(t), "features:\n  deploy: false\n  ingress: true\n")
	if err == nil || !strings.Contains(err.Error(), `feature "ingress" requires "deploy"`) {
		t.Fatalf("pinning ingress on with deploy pinned off must fail naming both, got %v", err)
	}
}

func TestFeatures_UnknownFeatureKeyIsATypo(t *testing.T) {
	_, err := loadTreeWith(t, cliTree(t), "features:\n  observabilty: false\n")
	if err == nil || !strings.Contains(err.Error(), `did you mean "observability"`) {
		t.Fatalf("a misspelled feature must fail with a suggestion, got %v", err)
	}
}

// A pin is never normalized away on write, even when it equals what the
// tree derives today: matching today is not the same as meaning it once
// internal/handlers/ appears.
func TestFeatures_PinSurvivesNormalizeForWrite(t *testing.T) {
	cfg, err := loadTreeWith(t, cliTree(t), "features:\n  observability: false\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Features.ObservabilityEnabled() {
		t.Fatal("precondition: off")
	}
	data, err := yaml.Marshal(NormalizeForWrite(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "features:\n    observability: false\n") {
		t.Errorf("the pin must survive a write round-trip, got:\n%s", data)
	}
	if strings.Contains(string(data), "codegen") {
		t.Errorf("derived values must not be written as pins:\n%s", data)
	}
}

func TestFeatures_PinRefusesOrm(t *testing.T) {
	var f FeaturesConfig
	if _, ok := f.Pin(FeatureORM, false); ok {
		t.Error("orm has no key — its switch is //forge:no-orm")
	}
	if g, ok := f.Pin(FeatureObservability, false); !ok || !g.Pinned(FeatureObservability) || g.ObservabilityEnabled() {
		t.Error("Pin must pin")
	}
}
