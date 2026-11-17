package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// TestIsFeatureEnabled_NilConfig locks in the permissive default
// when no forge.yaml is loaded — required so commands run outside a
// project don't error on a missing config.
func TestIsFeatureEnabled_NilConfig(t *testing.T) {
	if !isFeatureEnabled(nil, config.FeatureBuild) {
		t.Error("isFeatureEnabled(nil, build) = false, want true")
	}
}

// TestIsFeatureEnabled_DefaultsTrue covers a config the loader did not derive
// (the zero value): every feature accessor but ingress/operators reports enabled.
func TestIsFeatureEnabled_DefaultsTrue(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "t", ModulePath: "x/t"}
	for _, name := range []string{
		config.FeatureBuild, config.FeatureFrontend,
		config.FeatureCI,
		config.FeatureObservability,
	} {
		if !isFeatureEnabled(projectstore.New(cfg), name) {
			t.Errorf("isFeatureEnabled(<no-features>, %q) = false, want true", name)
		}
	}
}

// TestIsFeatureEnabled_ForcedOff covers a feature the scaffold turned off: it
// reads disabled, and the features it did not touch keep their defaults.
func TestIsFeatureEnabled_ForcedOff(t *testing.T) {
	cfg := &config.ProjectConfig{
		Features: config.FeaturesConfig{}.With(config.FeatureBuild, false).With(config.FeatureDeploy, false),
	}
	for _, name := range []string{config.FeatureBuild, config.FeatureDeploy} {
		if isFeatureEnabled(projectstore.New(cfg), name) {
			t.Errorf("isFeatureEnabled(<%s off>, %q) = true, want false", name, name)
		}
	}
	if !isFeatureEnabled(projectstore.New(cfg), config.FeatureCI) {
		t.Error("isFeatureEnabled(<build off>, ci) flipped — only build and deploy were turned off")
	}
}

// TestIsFeatureEnabled_IngressAndOperatorsDefaultOff: with nothing in the repo
// to turn them on, a zero-value config reports ingress and operators disabled.
func TestIsFeatureEnabled_IngressAndOperatorsDefaultOff(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "t", ModulePath: "x/t"}
	for _, name := range []string{config.FeatureIngress, config.FeatureOperators} {
		if isFeatureEnabled(projectstore.New(cfg), name) {
			t.Errorf("isFeatureEnabled(<zero>, %q) = true, want false", name)
		}
	}
}

// TestIsFeatureEnabled_UnknownNamePermissive asserts the
// "additive-extension" rule: an unknown feature name returns true
// (enabled) rather than erroring, so a new gate site added in a
// downstream forge doesn't crash older configs that don't yet know
// the constant.
func TestIsFeatureEnabled_UnknownNamePermissive(t *testing.T) {
	cfg := &config.ProjectConfig{}
	if !isFeatureEnabled(projectstore.New(cfg), "made-up-feature") {
		t.Error("isFeatureEnabled(projectstore.New(cfg), unknown) = false, want true (additive-extension)")
	}
}

// TestDisabledFeatureError_Wording locks in the user-visible string the CLI
// emits when a direct cobra command is invoked against a project whose feature
// is off. It must point at the repo, not at a forge.yaml key that no longer
// exists.
func TestDisabledFeatureError_Wording(t *testing.T) {
	for _, name := range []string{
		config.FeatureBuild,
		config.FeatureFrontend, config.FeatureCI,
		config.FeatureObservability,
	} {
		err := config.DisabledFeatureError(name)
		if err == nil {
			t.Errorf("DisabledFeatureError(%q) = nil", name)
			continue
		}
		got := err.Error()
		if !strings.Contains(got, "feature '"+name+"' is off for this project") {
			t.Errorf("DisabledFeatureError(%q) = %q, missing canonical prefix", name, got)
		}
		if !strings.Contains(got, "forge project features") {
			t.Errorf("DisabledFeatureError(%q) = %q, missing the pointer to `forge project features`", name, got)
		}
		if strings.Contains(got, "features."+name+": true") {
			t.Errorf("DisabledFeatureError(%q) = %q, still tells the user to set a removed forge.yaml key", name, got)
		}
	}
}

// TestRequireFeature_NoProject covers the no-forge.yaml path: the
// helper must surface ErrProjectConfigNotFound so the cobra command
// shows the existing "not in a forge project" message rather than
// a confusing "feature disabled" string.
func TestRequireFeature_NoProject(t *testing.T) {
	// chdir to a temp dir without a forge.yaml so loadProjectConfig
	// walks up and never finds one.
	t.Chdir(t.TempDir())
	_, err := requireFeature(config.FeatureBuild)
	if !errors.Is(err, ErrProjectConfigNotFound) {
		t.Errorf("requireFeature outside project: got %v, want ErrProjectConfigNotFound", err)
	}
}
