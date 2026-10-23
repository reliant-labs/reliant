package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

func newTestGen() *generator.ProjectGenerator {
	return generator.NewProjectGenerator("test", "/tmp/test", "github.com/example/test")
}

// allFeaturesEnabled returns true when every STABLE feature reports
// enabled (nil = default = enabled). Experimental features are
// intentionally excluded — they're default-OFF and not part of the
// "default on" promise.
func allFeaturesEnabled(gen *generator.ProjectGenerator) bool {
	f := gen.Features
	return f.ORMEnabled() &&
		f.CodegenEnabled() &&
		f.MigrationsEnabled() &&
		f.CIEnabled() &&
		f.ContractsEnabled() &&
		f.FrontendEnabled() &&
		f.ObservabilityEnabled() &&
		f.HotReloadEnabled()
}

func TestApplyDisableFlags_EmptyList(t *testing.T) {
	gen := newTestGen()
	if err := applyDisableFlags(gen, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allFeaturesEnabled(gen) {
		t.Error("expected all features enabled with nil disable list")
	}

	gen = newTestGen()
	if err := applyDisableFlags(gen, []string{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allFeaturesEnabled(gen) {
		t.Error("expected all features enabled with empty disable list")
	}
}

func TestApplyDisableFlags_SingleFeature(t *testing.T) {
	gen := newTestGen()
	if err := applyDisableFlags(gen, []string{"frontend"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gen.Features.FrontendEnabled() {
		t.Error("expected frontend to be disabled")
	}
	// Other features remain enabled (nil).
	if !gen.Features.ORMEnabled() {
		t.Error("expected orm to remain enabled")
	}
	if !gen.Features.CIEnabled() {
		t.Error("expected ci to remain enabled")
	}
	if !gen.Features.ObservabilityEnabled() {
		t.Error("expected observability to remain enabled")
	}
}

func TestApplyDisableFlags_MultipleFeatures(t *testing.T) {
	gen := newTestGen()
	if err := applyDisableFlags(gen, []string{"frontend", "ci", "observability"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gen.Features.FrontendEnabled() {
		t.Error("expected frontend to be disabled")
	}
	if gen.Features.CIEnabled() {
		t.Error("expected ci to be disabled")
	}
	if gen.Features.ObservabilityEnabled() {
		t.Error("expected observability to be disabled")
	}
	// Others still enabled.
	if !gen.Features.ORMEnabled() {
		t.Error("expected orm to remain enabled")
	}
	if !gen.Features.HotReloadEnabled() {
		t.Error("expected hot_reload to remain enabled")
	}
}

func TestApplyDisableFlags_AllFeatures(t *testing.T) {
	gen := newTestGen()
	// All stable features. Experimental features are explicitly
	// rejected by applyDisableFlags (they're default-OFF already) —
	// covered by TestApplyDisableFlags_ExperimentalRejected below.
	all := []string{"orm", "codegen", "migrations", "ci", "deploy", "contracts", "frontend", "observability", "hot_reload"}
	if err := applyDisableFlags(gen, all); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f := gen.Features
	if f.ORMEnabled() {
		t.Error("orm should be disabled")
	}
	if f.DeployEnabled() {
		t.Error("deploy should be disabled")
	}
	if f.CodegenEnabled() {
		t.Error("codegen should be disabled")
	}
	if f.MigrationsEnabled() {
		t.Error("migrations should be disabled")
	}
	if f.CIEnabled() {
		t.Error("ci should be disabled")
	}
	if f.ContractsEnabled() {
		t.Error("contracts should be disabled")
	}
	if f.FrontendEnabled() {
		t.Error("frontend should be disabled")
	}
	if f.ObservabilityEnabled() {
		t.Error("observability should be disabled")
	}
	if f.HotReloadEnabled() {
		t.Error("hot_reload should be disabled")
	}
}

// TestApplyDisableFlags_DerivedFeaturesRejected: ingress and operators cannot be
// --disable'd. They are on only when the repo declares a gateway or an operator,
// and a new project has neither, so there is nothing to turn off — and the
// message says what turns them on instead of leaving a dead flag.
func TestApplyDisableFlags_DerivedFeaturesRejected(t *testing.T) {
	for _, name := range []string{"ingress", "operators"} {
		t.Run(name, func(t *testing.T) {
			gen := newTestGen()
			err := applyDisableFlags(gen, []string{name})
			if err == nil {
				t.Fatalf("--disable %s should error: it derives from what exists in the repo", name)
			}
			if !strings.Contains(err.Error(), "derives from what exists") {
				t.Errorf("error should explain the feature is derived; got: %v", err)
			}
		})
	}
}

// The retired experimental spellings are unknown features now, not silently
// accepted.
func TestApplyDisableFlags_RetiredExperimentalNamesAreUnknown(t *testing.T) {
	for _, name := range []string{"external_builds", "strict_wiring", "reconcile"} {
		if err := applyDisableFlags(newTestGen(), []string{name}); err == nil || !strings.Contains(err.Error(), "unknown feature") {
			t.Errorf("--disable %s: want an unknown-feature error, got %v", name, err)
		}
	}
}

func TestApplyDisableFlags_HotReloadVariants(t *testing.T) {
	variants := []string{"hot_reload", "hot-reload", "hotreload"}
	for _, v := range variants {
		t.Run(v, func(t *testing.T) {
			gen := newTestGen()
			if err := applyDisableFlags(gen, []string{v}); err != nil {
				t.Fatalf("unexpected error for %q: %v", v, err)
			}
			if gen.Features.HotReloadEnabled() {
				t.Errorf("expected hot_reload disabled via %q", v)
			}
		})
	}
}

func TestApplyDisableFlags_UnknownFeature(t *testing.T) {
	gen := newTestGen()
	err := applyDisableFlags(gen, []string{"bogus"})
	if err == nil {
		t.Fatal("expected error for unknown feature")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should mention the unknown feature name, got: %v", err)
	}
	if !strings.Contains(err.Error(), "valid features") {
		t.Errorf("error should list valid features, got: %v", err)
	}
}

func TestApplyDisableFlags_MixedValidAndInvalid(t *testing.T) {
	gen := newTestGen()
	err := applyDisableFlags(gen, []string{"frontend", "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown feature")
	}
	// "frontend" was processed before the error on "bogus".
	if gen.Features.FrontendEnabled() {
		t.Error("expected frontend to be disabled even though a later feature errored")
	}
}

// TestApplyDisableFlags_NewFeatures covers the build
// names added by the features-block work — they must be valid
// --disable values and resolve to disabling the matching flag on
// the generator's FeaturesConfig.
func TestApplyDisableFlags_NewFeatures(t *testing.T) {
	gen := newTestGen()
	if err := applyDisableFlags(gen, []string{"build", "deploy"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gen.Features.BuildEnabled() {
		t.Error("expected build to be disabled")
	}
	if gen.Features.DeployEnabled() {
		t.Error("expected deploy to be disabled")
	}
	// Unrelated stable features still enabled (nil default).
	if !gen.Features.FrontendEnabled() {
		t.Error("expected frontend to remain enabled (only build/deploy disabled)")
	}
}

func TestApplyDisableFlags_CaseInsensitive(t *testing.T) {
	gen := newTestGen()
	if err := applyDisableFlags(gen, []string{"FRONTEND", "CI"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gen.Features.FrontendEnabled() {
		t.Error("expected FRONTEND (uppercase) to disable frontend")
	}
	if gen.Features.CIEnabled() {
		t.Error("expected CI (uppercase) to disable ci")
	}
}

// TestRunNewKindValidation exercises the `--kind` flag's validation surface
// via the pure validateNewArgs helper. Doing it via runNew would invoke the
// full scaffold (go mod tidy + buf generate + …), which is slow and can
// hang in CI environments without network access.
func TestRunNewKindValidation(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		wantKind string
		wantErr  string
	}{
		{"unknown kind rejected", "framework", "", `invalid --kind "framework"`},
		{"empty becomes service", "", config.ProjectKindService, ""}, // empty == service (back-compat)
		{"service explicit", "service", config.ProjectKindService, ""},
		{"cli explicit", "cli", config.ProjectKindCLI, ""},
		{"library explicit", "library", config.ProjectKindLibrary, ""},
		{"upper-case normalized", "CLI", config.ProjectKindCLI, ""},
		{"trims whitespace", "  service  ", config.ProjectKindService, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotKind, _, _, err := validateNewArgs(tc.kind, "local", "", nil, nil)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotKind != tc.wantKind {
				t.Fatalf("kind = %q, want %q", gotKind, tc.wantKind)
			}
		})
	}
}

// TestRunNewKindCLIRejectsServiceFlag — `--service` is service-only.
func TestRunNewKindCLIRejectsServiceFlag(t *testing.T) {
	_, _, _, err := validateNewArgs("cli", "local", "", []string{"api"}, nil)
	if err == nil || !strings.Contains(err.Error(), "--service is only meaningful with --kind service") {
		t.Fatalf("expected --service rejection in CLI mode, got: %v", err)
	}
}

// TestValidateNewArgs_BufPlugins covers the --buf-plugins normalization.
func TestValidateNewArgs_BufPlugins(t *testing.T) {
	cases := []struct {
		input       string
		wantPlugins string
		wantErr     bool
	}{
		{"", "local", false}, // default
		{"local", "local", false},
		{"remote", "remote", false},
		{"REMOTE", "remote", false},
		{"  local  ", "local", false},
		{"bogus", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			_, gotPlugins, _, err := validateNewArgs("service", tc.input, "", nil, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotPlugins != tc.wantPlugins {
				t.Fatalf("plugins = %q, want %q", gotPlugins, tc.wantPlugins)
			}
		})
	}
}

func TestApplyDisableFlags_WhitespaceHandling(t *testing.T) {
	gen := newTestGen()
	if err := applyDisableFlags(gen, []string{" frontend "}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gen.Features.FrontendEnabled() {
		t.Error("expected ' frontend ' (with whitespace) to disable frontend")
	}
}

// A component asked for by name whose feature --disable leaves out is refused
// before anything is written. The scaffold used to drop it without a word.
func TestRefuseDisabledComponents(t *testing.T) {
	for _, tc := range []struct {
		name      string
		disable   []string
		services  []string
		frontends []string
		wantErr   string
	}{
		{name: "service without codegen", disable: []string{"codegen"}, services: []string{"orders"}, wantErr: "--disable codegen"},
		{name: "case-insensitive", disable: []string{" Codegen "}, services: []string{"orders"}, wantErr: "--disable codegen"},
		{name: "frontend without frontend", disable: []string{"frontend"}, frontends: []string{"web"}, wantErr: "--disable frontend"},
		{name: "frontend-only shape", disable: []string{"orm", "migrations", "codegen"}, frontends: []string{"web"}},
		{name: "service, frontend codegen off elsewhere", disable: []string{"ci"}, services: []string{"orders"}, frontends: []string{"web"}},
		{name: "nothing asked for", disable: []string{"codegen", "frontend"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseDisabledComponents(tc.disable, tc.services, tc.frontends)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("refused a coherent shape: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one naming %q", err, tc.wantErr)
			}
		})
	}
}
