package config

import (
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestCIConfig_IsLintEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  CIConfig
		want bool
	}{
		{"zero value = all enabled", CIConfig{}, true},
		{"golangci only", CIConfig{Lint: CILintConfig{Golangci: true}}, true},
		{"buf only", CIConfig{Lint: CILintConfig{Buf: true}}, true},
		{"buf breaking only", CIConfig{Lint: CILintConfig{BufBreaking: true}}, true},
		{"frontend only", CIConfig{Lint: CILintConfig{Frontend: true}}, true},
		{"migration safety only", CIConfig{Lint: CILintConfig{MigrationSafety: true}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.IsLintEnabled()
			if got != tt.want {
				t.Errorf("IsLintEnabled(): got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCIConfig_IsTestRaceEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  CIConfig
		want bool
	}{
		{"zero value = enabled", CIConfig{}, true},
		{"race true", CIConfig{Test: CITestConfig{Race: true}}, true},
		{"coverage only (race false)", CIConfig{Test: CITestConfig{Coverage: true}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.IsTestRaceEnabled()
			if got != tt.want {
				t.Errorf("IsTestRaceEnabled(): got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCIConfig_IsVulnScanEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  CIConfig
		want bool
	}{
		{"zero value = all enabled", CIConfig{}, true},
		{"go only", CIConfig{VulnScan: CIVulnConfig{Go: true}}, true},
		// Accepting an advisory is not a scanner selection. If it were,
		// adding one exemption would silently switch a project from
		// "all scanners" to "only the ones now listed" — i.e. turn
		// scanners OFF as a side effect of accepting one CVE.
		{
			"exemptions alone do not narrow the scanner set",
			CIConfig{VulnScan: CIVulnConfig{Exemptions: []CIVulnExemption{
				{ID: "GO-2026-4887", Reason: "unreachable", Expires: "2026-12-31"},
			}}},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.IsVulnScanEnabled()
			if got != tt.want {
				t.Errorf("IsVulnScanEnabled(): got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCIConfig_EffectivePermContents(t *testing.T) {
	tests := []struct {
		contents string
		want     string
	}{
		{"", "read"},
		{"write", "write"},
	}
	for _, tt := range tests {
		cfg := &CIConfig{Permissions: CIPermConfig{Contents: tt.contents}}
		got := cfg.EffectivePermContents()
		if got != tt.want {
			t.Errorf("EffectivePermContents() with contents=%q: got %q, want %q", tt.contents, got, tt.want)
		}
	}
}

func TestMigrationSafetyConfigDefaults(t *testing.T) {
	cfg := MigrationSafetyConfig{}
	if !cfg.IsEnabled() {
		t.Fatal("zero-value migration safety config should be enabled")
	}
	if got := cfg.EffectiveUnsafeAddColumn(); got != "error" {
		t.Errorf("EffectiveUnsafeAddColumn() = %q, want error", got)
	}
	if got := cfg.EffectiveDestructiveChange(); got != "error" {
		t.Errorf("EffectiveDestructiveChange() = %q, want error", got)
	}
	if got := cfg.EffectiveVolatileDefault(); got != "warn" {
		t.Errorf("EffectiveVolatileDefault() = %q, want warn", got)
	}
}

// TestComponentConfig_EffectiveKind pins the kind discriminator's
// normalisation. There is no YAML round-trip to test: a ComponentConfig is
// never serialized — it is the in-memory shape discovery hands back.
func TestComponentConfig_EffectiveKind(t *testing.T) {
	tests := []struct {
		name     string
		comp     ComponentConfig
		wantKind string
	}{
		{"cron with schedule", ComponentConfig{Name: "cleanup", Kind: "cron", Schedule: "*/5 * * * *"}, "cron"},
		{"worker", ComponentConfig{Name: "processor", Kind: "worker"}, "worker"},
		{"empty kind defaults to server", ComponentConfig{Name: "api"}, "server"},
		{"kind is case/space insensitive", ComponentConfig{Name: "api", Kind: " Operator "}, "operator"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.comp.EffectiveKind(); got != tt.wantKind {
				t.Errorf("EffectiveKind = %q, want %q", got, tt.wantKind)
			}
		})
	}
}

func TestDeployConfig_IsConcurrencyEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  DeployConfig
		want bool
	}{
		{"zero value = enabled", DeployConfig{}, true},
		{"explicitly enabled", DeployConfig{Concurrency: DeployConcurrency{Enabled: true}}, true},
		{"cancel in progress only (enabled false)", DeployConfig{Concurrency: DeployConcurrency{CancelInProgress: true}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.IsConcurrencyEnabled()
			if got != tt.want {
				t.Errorf("IsConcurrencyEnabled(): got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProjectConfig_ForgeVersionRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		yamlStr string
		want    string
	}{
		{
			"explicit forge_version",
			"name: p\nmodule_path: example.com/p\nforge_version: \"1.5.0\"\n",
			"1.5.0",
		},
		{
			"missing forge_version (legacy)",
			"name: p\nmodule_path: example.com/p\n",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg ProjectConfig
			if err := yaml.Unmarshal([]byte(tt.yamlStr), &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if cfg.ForgeVersion != tt.want {
				t.Errorf("ForgeVersion = %q, want %q", cfg.ForgeVersion, tt.want)
			}

			// Round-trip
			out, err := yaml.Marshal(&cfg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var cfg2 ProjectConfig
			if err := yaml.Unmarshal(out, &cfg2); err != nil {
				t.Fatalf("unmarshal round-trip: %v", err)
			}
			if cfg2.ForgeVersion != tt.want {
				t.Errorf("round-trip ForgeVersion = %q, want %q", cfg2.ForgeVersion, tt.want)
			}
		})
	}
}

func TestProjectConfig_EffectiveForgeVersion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty (legacy)", "", "0.0.0"},
		{"whitespace only", "   ", "0.0.0"},
		{"explicit version", "1.5.0", "1.5.0"},
		{"dev sentinel passes through", "dev", "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ProjectConfig{ForgeVersion: tt.in}
			if got := cfg.EffectiveForgeVersion(); got != tt.want {
				t.Errorf("EffectiveForgeVersion(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestProjectConfig_HasReactNativeFrontend asserts the detector
// returns true only when at least one frontend is RN-shaped. Gates
// the @<scope>/ui-native package emit.
func TestProjectConfig_HasReactNativeFrontend(t *testing.T) {
	tests := []struct {
		name      string
		frontends []FrontendConfig
		want      bool
	}{
		{"empty", nil, false},
		{"only nextjs", []FrontendConfig{{Name: "web", Type: "nextjs"}}, false},
		{"only vite-spa", []FrontendConfig{{Name: "admin", Type: "vite-spa"}}, false},
		{"react-native present", []FrontendConfig{{Name: "mobile", Type: "react-native"}}, true},
		{"underscore form accepted", []FrontendConfig{{Name: "mobile", Type: "react_native"}}, true},
		{"mixed", []FrontendConfig{
			{Name: "web", Type: "nextjs"},
			{Name: "mobile", Type: "react-native"},
		}, true},
		{"case-insensitive type", []FrontendConfig{{Name: "mobile", Type: "React-Native"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ProjectConfig{Frontends: tt.frontends}
			if got := cfg.HasReactNativeFrontend(); got != tt.want {
				t.Errorf("HasReactNativeFrontend() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDockerConfig_BuildContextsYAMLRoundTrip pins the forge.yaml
// surface for `docker.build_contexts` — both the YAML key spelling and
// the supported value shapes (local relative path, `docker-image://`
// scheme ref, registry-style ref). YAML round-trip catches any future
// rename of the struct tag.
func TestDockerConfig_BuildContextsYAMLRoundTrip(t *testing.T) {
	yamlStr := "" +
		"name: p\n" +
		"module_path: example.com/p\n" +
		"docker:\n" +
		"  build_contexts:\n" +
		"    shared: ../shared-libs\n" +
		"    base: docker-image://my-base:latest\n" +
		"    pinned: ghcr.io/acme/base:v1\n"

	var cfg ProjectConfig
	if err := yaml.Unmarshal([]byte(yamlStr), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := map[string]string{
		"shared": "../shared-libs",
		"base":   "docker-image://my-base:latest",
		"pinned": "ghcr.io/acme/base:v1",
	}
	if got := cfg.Docker.BuildContexts; len(got) != len(want) {
		t.Fatalf("BuildContexts len = %d, want %d (got %v)", len(got), len(want), got)
	}
	for k, v := range want {
		if cfg.Docker.BuildContexts[k].Path != v {
			t.Errorf("BuildContexts[%q] = %q, want %q", k, cfg.Docker.BuildContexts[k].Path, v)
		}
	}

	// Round-trip: marshal then unmarshal and confirm the map survives.
	out, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var cfg2 ProjectConfig
	if err := yaml.Unmarshal(out, &cfg2); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	for k, v := range want {
		if cfg2.Docker.BuildContexts[k].Path != v {
			t.Errorf("round-trip BuildContexts[%q] = %q, want %q", k, cfg2.Docker.BuildContexts[k].Path, v)
		}
	}
}

// TestDockerConfig_BuildContextsOmittedWhenEmpty pins that an unset
// build_contexts map does NOT appear in the marshalled YAML — the
// omitempty tag keeps existing forge.yaml files byte-stable on
// round-trip when they don't declare any contexts.
func TestDockerConfig_BuildContextsOmittedWhenEmpty(t *testing.T) {
	cfg := ProjectConfig{
		Name:       "p",
		ModulePath: "example.com/p",
	}
	out, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// `contains` lives in api_test.go in the same package — reuse it
	// instead of dragging in strings just for one substring check.
	if got := string(out); contains(got, "build_contexts") {
		t.Errorf("empty BuildContexts should not appear in marshalled YAML, got:\n%s", got)
	}
}

func TestConfigGuardConfig_EffectiveEnforceTypedAccess(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"absent defaults to warn", "", EnforceTypedAccessWarn},
		{"whitespace defaults to warn", "   ", EnforceTypedAccessWarn},
		{"off", "off", EnforceTypedAccessOff},
		{"warn", "warn", EnforceTypedAccessWarn},
		{"error", "error", EnforceTypedAccessError},
		{"warning alias", "warning", EnforceTypedAccessWarn},
		{"case-insensitive Error", "Error", EnforceTypedAccessError},
		{"unknown defaults to warn", "nonsense", EnforceTypedAccessWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ConfigGuardConfig{EnforceTypedAccess: tt.in}
			if got := c.EffectiveEnforceTypedAccess(); got != tt.want {
				t.Errorf("EffectiveEnforceTypedAccess(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestConfigGuardConfig_EffectiveLoaderPackage(t *testing.T) {
	if got := (ConfigGuardConfig{}).EffectiveLoaderPackage(); got != DefaultLoaderPackage {
		t.Errorf("absent loader_package = %q, want %q", got, DefaultLoaderPackage)
	}
	if got := (ConfigGuardConfig{LoaderPackage: "internal/cfg"}).EffectiveLoaderPackage(); got != "internal/cfg" {
		t.Errorf("explicit loader_package = %q, want internal/cfg", got)
	}
}

func TestConfigGuardConfig_GuardGatesAndEnabled(t *testing.T) {
	tests := []struct {
		in          string
		wantEnabled bool
		wantGates   bool
	}{
		{"", true, false},     // absent → warn: enabled, advisory
		{"off", false, false}, // off → nothing
		{"warn", true, false}, // warn → enabled, advisory
		{"error", true, true}, // error → enabled, gating
	}
	for _, tt := range tests {
		c := ConfigGuardConfig{EnforceTypedAccess: tt.in}
		if got := c.TypedAccessGuardEnabled(); got != tt.wantEnabled {
			t.Errorf("TypedAccessGuardEnabled(%q) = %v, want %v", tt.in, got, tt.wantEnabled)
		}
		if got := c.TypedAccessGuardGates(); got != tt.wantGates {
			t.Errorf("TypedAccessGuardGates(%q) = %v, want %v", tt.in, got, tt.wantGates)
		}
	}
}
