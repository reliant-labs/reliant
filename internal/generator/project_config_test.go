package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/config"
)

// TestWriteProjectConfig_StampsForgeVersion verifies that scaffolding a
// new project records the current forge binary version under
// `forge_version` in forge.yaml. This is the foundation of the upgrade
// story — `forge project upgrade` consumes the field, `forge generate` warns on
// mismatch.
func TestWriteProjectConfig_StampsForgeVersion(t *testing.T) {
	tmp := t.TempDir()

	g := NewProjectGenerator("test-stamp", tmp, "example.com/test-stamp")
	g.ServiceName = "api"
	if err := g.writeProjectConfig(); err != nil {
		t.Fatalf("writeProjectConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "forge.yaml"))
	if err != nil {
		t.Fatalf("read forge.yaml: %v", err)
	}

	var cfg config.ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := buildinfo.Version()
	if cfg.ForgeVersion != want {
		t.Errorf("ForgeVersion = %q, want %q (buildinfo.Version())", cfg.ForgeVersion, want)
	}

	// Sanity check: the field is actually present in the marshaled yaml,
	// not just defaulted by the unmarshaler.
	if want != "" && want != "dev" {
		// Only assert when buildinfo reports a real release; in dev/test
		// runs the value is "dev" which marshals via omitempty rules.
		got := string(data)
		if !strings.Contains(got, "forge_version") {
			t.Errorf("forge.yaml missing forge_version key:\n%s", got)
		}
	}
}

// TestWriteProjectConfig_ScaffoldsTypedAccessError verifies the scaffold
// lane writes an explicit `config.enforce_typed_access: error` into a
// freshly-generated forge.yaml. Greenfield projects carry no legacy
// env-reading debt, so the guardrail scaffolds in its strict gating form —
// deliberately DIFFERENT from the schema default for an ABSENT key (which is
// "warn", so existing projects upgrade without a flag-day). The explicit
// "error" must survive write-time normalization and round-trip on load.
func TestWriteProjectConfig_ScaffoldsTypedAccessError(t *testing.T) {
	tmp := t.TempDir()
	g := NewProjectGenerator("guard-scaffold", tmp, "example.com/guard-scaffold")
	g.ServiceName = "api"
	if err := g.writeProjectConfig(); err != nil {
		t.Fatalf("writeProjectConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "forge.yaml"))
	if err != nil {
		t.Fatalf("read forge.yaml: %v", err)
	}
	if !strings.Contains(string(data), "enforce_typed_access: error") {
		t.Errorf("scaffolded forge.yaml missing `enforce_typed_access: error`:\n%s", data)
	}

	cfg, err := ReadProjectConfig(filepath.Join(tmp, "forge.yaml"))
	if err != nil {
		t.Fatalf("ReadProjectConfig: %v", err)
	}
	if got := cfg.Config.EffectiveEnforceTypedAccess(); got != config.EnforceTypedAccessError {
		t.Errorf("loaded EffectiveEnforceTypedAccess() = %q, want %q", got, config.EnforceTypedAccessError)
	}
	if !cfg.Config.TypedAccessGuardGates() {
		t.Error("scaffolded config must gate the build (TypedAccessGuardGates() = false)")
	}

	// The scaffold render lane must agree: forScaffold projects the strict
	// gating mode into the .golangci.yml payload.
	if got := g.forScaffold().TypedAccessGuard; got != config.EnforceTypedAccessError {
		t.Errorf("forScaffold().TypedAccessGuard = %q, want %q", got, config.EnforceTypedAccessError)
	}
}

// TestWriteProjectConfig_AdoptingScaffoldsTypedAccessWarn: a project adopted
// in place already reads the environment directly, so a gating guardrail
// would fail its first `forge lint` on every one of those reads. It starts
// advisory, written explicitly so the user flips it where it is recorded,
// and the .golangci.yml render agrees.
func TestWriteProjectConfig_AdoptingScaffoldsTypedAccessWarn(t *testing.T) {
	tmp := t.TempDir()
	g := NewProjectGenerator("guard-adopt", tmp, "example.com/guard-adopt")
	g.ServiceName = "api"
	g.Adopting = true
	if err := g.writeProjectConfig(); err != nil {
		t.Fatalf("writeProjectConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "forge.yaml"))
	if err != nil {
		t.Fatalf("read forge.yaml: %v", err)
	}
	if !strings.Contains(string(data), "enforce_typed_access: warn") {
		t.Errorf("adopted forge.yaml missing an explicit `enforce_typed_access: warn`:\n%s", data)
	}
	cfg, err := ReadProjectConfig(filepath.Join(tmp, "forge.yaml"))
	if err != nil {
		t.Fatalf("ReadProjectConfig: %v", err)
	}
	if cfg.Config.TypedAccessGuardGates() {
		t.Error("an adopted project's guardrail must not gate the build")
	}
	if !cfg.Config.TypedAccessGuardEnabled() {
		t.Error("an adopted project's guardrail must still report")
	}
	if got := g.forScaffold().TypedAccessGuard; got != config.EnforceTypedAccessWarn {
		t.Errorf("forScaffold().TypedAccessGuard = %q, want %q", got, config.EnforceTypedAccessWarn)
	}
}

// TestApplyKindFeatureDefaults_Service is a no-op assertion: the
// default scaffold (`forge project new --kind service` or no flag) must leave
// every feature enabled except ingress and operators, which only exist once the
// repo declares a gateway or an operator.
func TestApplyKindFeatureDefaults_Service(t *testing.T) {
	g := NewProjectGenerator("svc", "/tmp/svc", "example.com/svc")
	g.ApplyKindFeatureDefaults(config.ProjectKindService)
	effective := g.Features.EffectiveFeatures()
	for name, on := range effective {
		// ingress and operators are stable but DERIVE to off: neither
		// follows from a project's shape, so a scaffolded service does
		// not get Gateway API wiring or CRD codegen until asked.
		if name == config.FeatureIngress || name == config.FeatureOperators {
			if on {
				t.Errorf("kind=service: %q expected disabled (opt-in), got enabled", name)
			}
			continue
		}
		if !on {
			t.Errorf("kind=service: stable feature %q expected enabled, got disabled", name)
		}
	}
}

// TestApplyKindFeatureDefaults_CLI verifies the CLI per-kind matrix.
// The feature-block prompt's documented matrix is "build/ci/docs
// true; deploy/frontend/observability false." Per the
// existing forge convention (forge has disabled codegen/ORM/migrations
// for non-service kinds since the kind flag landed), we also leave
// those off — the CLI scaffold has no proto/services dir to drive
// them. Contracts stays on so the linter still nudges CLI authors
// toward interface-bounded surface area.
func TestApplyKindFeatureDefaults_CLI(t *testing.T) {
	g := NewProjectGenerator("c", "/tmp/c", "example.com/c")
	g.ApplyKindFeatureDefaults(config.ProjectKindCLI)
	effective := g.Features.EffectiveFeatures()

	want := map[string]bool{
		config.FeatureBuild:         true,
		config.FeatureCI:            true,
		config.FeatureContracts:     true,
		config.FeatureDeploy:        false,
		config.FeatureFrontend:      false,
		config.FeatureObservability: false,
		config.FeatureORM:           false,
		config.FeatureCodegen:       false, // existing forge default — no proto/services to codegen
		config.FeatureMigrations:    false,
		config.FeatureHotReload:     false,
		config.FeatureIngress:       false,
	}
	for name, expect := range want {
		if effective[name] != expect {
			t.Errorf("kind=cli: feature %q = %v, want %v", name, effective[name], expect)
		}
	}
}

// TestApplyKindFeatureDefaults_Library verifies the library matrix.
// The feature-block prompt's documented matrix is "library: ci
// true, everything else false." We honor the prompt for build/
// deploy/frontend/observability/orm/codegen/migrations/
// hot_reload but preserve the existing forge convention of CI=false
// for library — TestProjectGeneratorKindLibraryScaffold asserts no
// .github/workflows/ tree is emitted on a library scaffold, and the
// user can flip features.ci: true manually to re-enable lint+test
// workflows. Contracts stays enabled (linting is the headline value
// for a library; package authors want interface-bounded surface).
func TestApplyKindFeatureDefaults_Library(t *testing.T) {
	g := NewProjectGenerator("lib", "/tmp/lib", "example.com/lib")
	g.ApplyKindFeatureDefaults(config.ProjectKindLibrary)
	effective := g.Features.EffectiveFeatures()

	want := map[string]bool{
		config.FeatureContracts:     true,
		config.FeatureCI:            false, // existing forge convention
		config.FeatureBuild:         false,
		config.FeatureDeploy:        false,
		config.FeatureFrontend:      false,
		config.FeatureObservability: false,
		config.FeatureORM:           false,
		config.FeatureCodegen:       false,
		config.FeatureMigrations:    false,
		config.FeatureHotReload:     false,
		config.FeatureIngress:       false,
	}
	for name, expect := range want {
		if effective[name] != expect {
			t.Errorf("kind=library: feature %q = %v, want %v", name, effective[name], expect)
		}
	}
}

// TestWriteProjectConfig_CLIKindFeaturesDeriveOnLoad verifies the
// scaffolded CLI forge.yaml carries NO features: block — the per-kind
// matrix is derived from `kind: cli` at load time. The round-trip that
// matters is `forge project new` → loadProjectConfig: the loaded config must
// resolve build=on / packs=off without any explicit flags on disk.
func TestWriteProjectConfig_CLIKindFeaturesDeriveOnLoad(t *testing.T) {
	tmp := t.TempDir()
	g := NewProjectGenerator("cli-feat", tmp, "example.com/cli-feat")
	g.Kind = config.ProjectKindCLI
	g.ApplyKindFeatureDefaults(config.ProjectKindCLI)
	if err := g.writeProjectConfig(); err != nil {
		t.Fatalf("writeProjectConfig: %v", err)
	}
	// Kind derives from real sources on reload. This test only writes
	// forge.yaml (not a full scaffold), so stamp the cli's real artifact — a
	// cmd/<name>/main.go binary with no service sources — so ReadProjectConfig
	// derives kind=cli (and the cli feature matrix) rather than library.
	if err := os.MkdirAll(filepath.Join(tmp, "cmd", "cli-feat"), 0o755); err != nil {
		t.Fatalf("mkdir cmd/cli-feat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "cmd", "cli-feat", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write cmd/cli-feat/main.go: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "forge.yaml"))
	if err != nil {
		t.Fatalf("read forge.yaml: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "features:") {
			t.Errorf("kind=cli forge.yaml should not materialize a features: block (derived from kind); got:\n%s", data)
		}
	}
	cfg, err := ReadProjectConfig(filepath.Join(tmp, "forge.yaml"))
	if err != nil {
		t.Fatalf("ReadProjectConfig: %v", err)
	}
	if !cfg.Features.BuildEnabled() {
		t.Error("kind=cli loaded config: BuildEnabled() = false, want true (derived)")
	}
	if cfg.Features.CodegenEnabled() {
		t.Error("kind=cli loaded config: CodegenEnabled() = true, want false (derived)")
	}
}

// TestWriteProjectConfig_NonServiceKindsStillStampForgeVersion verifies
// that CLI- and library-kind projects also get a forge_version pin
// (the scaffold-time stamp is shape-agnostic).
func TestWriteProjectConfig_NonServiceKindsStillStampForgeVersion(t *testing.T) {
	for _, kind := range []string{"cli", "library"} {
		t.Run(kind, func(t *testing.T) {
			tmp := t.TempDir()
			g := NewProjectGenerator("kind-"+kind, tmp, "example.com/kind-"+kind)
			g.Kind = kind
			if err := g.writeProjectConfig(); err != nil {
				t.Fatalf("writeProjectConfig: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(tmp, "forge.yaml"))
			if err != nil {
				t.Fatalf("read forge.yaml: %v", err)
			}
			var cfg config.ProjectConfig
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if cfg.ForgeVersion != buildinfo.Version() {
				t.Errorf("ForgeVersion = %q, want %q", cfg.ForgeVersion, buildinfo.Version())
			}
		})
	}
}

// TestWriteProjectConfig_AdoptedForgePinWins: an in-place scaffold adopting a
// module that already requires forge records THAT version, not the binary's,
// and renders go.mod / gen/go.mod against it — forge_version is a projection
// of go.mod's forge require.
func TestWriteProjectConfig_AdoptedForgePinWins(t *testing.T) {
	const pin = "v0.1.44-0.20261007193810-e75c74afb46e"
	tmp := t.TempDir()
	g := NewProjectGenerator("adopted", tmp, "github.com/example/adopted")
	g.ForgePin = pin
	if err := g.writeProjectConfig(); err != nil {
		t.Fatalf("writeProjectConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.ForgeVersion != pin {
		t.Errorf("forge_version = %q, want the adopted pin %q", cfg.ForgeVersion, pin)
	}
	if got := g.forScaffold().ForgeVersion; got != pin {
		t.Errorf("go.mod templates would require forge %q, want the adopted pin %q", got, pin)
	}
}

// `forge project new --disable observability` pins the feature off in the
// forge.yaml it writes, so the first internal/handlers/ (which derives
// observability on) does not bring it back. Only pins serialize: the
// scaffold's own per-kind derived state stays out of the file.
func TestWriteProjectConfig_PersistsDisabledFeaturesAsPins(t *testing.T) {
	tmp := t.TempDir()
	g := NewProjectGenerator("pins", tmp, "example.com/pins")
	g.ApplyKindFeatureDefaults(config.ProjectKindCLI)
	g.Features = g.Features.With(config.FeatureObservability, false)
	g.Features, _ = g.Features.Pin(config.FeatureObservability, false)
	if err := g.writeProjectConfig(); err != nil {
		t.Fatalf("writeProjectConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "features:\n    observability: false\n") {
		t.Fatalf("forge.yaml must pin observability off:\n%s", data)
	}
	if strings.Contains(string(data), "codegen") || strings.Contains(string(data), "hot_reload") {
		t.Errorf("the CLI kind's derived defaults must not be written as pins:\n%s", data)
	}
}
