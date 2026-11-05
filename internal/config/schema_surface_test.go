package config

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestSchemaSurface_TopLevelKeys pins the COMPLETE set of keys forge.yaml
// accepts at the top level, and on the blocks whose surface has been trimmed
// down to what a reader actually resolves.
//
// The rule this guards: a forge.yaml key earns its place by being READ
// somewhere that changes behavior AND by not being derivable from the code,
// the protos, or KCL. Three keys shipped without meeting it — a top-level
// `hot_reload:` that resolved through an accessor no caller invoked (the live
// switch is `features.hot_reload`), a `version:` nothing read, and
// `ci.extra_jobs:` that the workflow generator never fed to its template, so
// declared jobs silently vanished. Each parsed clean and configured nothing,
// which is the failure mode a schema test catches and a unit test does not:
// there is no behavior to assert on a key with no reader.
//
// Adding a key here must therefore be a deliberate edit with a read site to
// point at. Deleting one is free.
func TestSchemaSurface_TopLevelKeys(t *testing.T) {
	cases := []struct {
		name string
		typ  reflect.Type
		want []string
	}{
		{
			name: "ProjectConfig",
			typ:  reflect.TypeFor[ProjectConfig](),
			// features: read by FeaturesConfig.resolve through every
			// XxxEnabled accessor, and by followPinnedRequirements.
			want: []string{
				"api", "binary", "ci", "config", "contracts", "database",
				"deploy", "dev_stack", "docker", "features",
				"forge_version", "generate", "harness", "k8s",
				"lint", "module_path", "name", "observability", "upgrade",
			},
		},
		{
			// Every feature but orm, whose switch is //forge:no-orm.
			name: "FeaturesConfig",
			typ:  reflect.TypeFor[FeaturesConfig](),
			want: []string{
				"build", "ci", "codegen", "contracts", "deploy", "frontend",
				"hot_reload", "ingress", "migrations", "observability", "operators",
			},
		},
		{
			// No extra_jobs: the ci: block only feeds the ONE-TIME workflow
			// render. A job you want in .github/workflows/ci.yml is one edit
			// in the file you already own.
			name: "CIConfig",
			typ:  reflect.TypeFor[CIConfig](),
			//
			// test_skips: read by `forge ci verify-test-run`
			// (internal/cli/ci_test_run.go -> testreport.Policy) to
			// tune the silent-mass-skip gate and to hold the declared
			// per-package exemptions.
			//
			// gate: read by `forge ci gate` (internal/cli/ci_gate.go) for
			// the project's extra_checks.
			want: []string{
				"e2e", "gate", "lint", "permissions", "provider",
				"test", "test_skips", "vuln_scan",
			},
		},
		{
			// env / path: every check's environment, read by
			// cigate.GateEnv / GatePath. affected_ignore: read by
			// cigate.ComputeAffected.
			name: "CIGateConfig",
			typ:  reflect.TypeFor[CIGateConfig](),
			want: []string{"affected_ignore", "env", "extra_checks", "path"},
		},
		{
			// Every key is read by cigate's planner (internal/cigate/
			// plan.go extras) into a Check the scheduler honors.
			name: "CIGateCheck",
			typ:  reflect.TypeFor[CIGateCheck](),
			want: []string{
				"after", "cache", "command", "dir", "env", "group", "heavy",
				"lane", "mutates_tree", "name", "probe", "replaces", "requires",
				"skip_exit_code",
			},
		},
		{
			name: "CIGateProbe",
			typ:  reflect.TypeFor[CIGateProbe](),
			want: []string{"command", "reason"},
		},
		{
			// Only migration_safety, orm and postgres_version are authored.
			// The driver and migrations directory derive from db/migrations
			// existing, and the dev seed has no configuration at all.
			// postgres_version: read by forge/pkg/pgtest (declaredInForgeYAML).
			name: "DatabaseConfig",
			typ:  reflect.TypeFor[DatabaseConfig](),
			want: []string{"migration_safety", "orm", "postgres_version"},
		},
		{
			// orm.package: where the generated data layer lands, read by
			// the ORM emitter and every consumer through config.ORMPackage.
			name: "ORMConfig",
			typ:  reflect.TypeFor[ORMConfig](),
			want: []string{"package"},
		},
		{
			// The per-migration exemption is the `-- forge:allow-destructive`
			// directive in the SQL file, not a glob list here.
			// require_compat_declaration: read by migrationlint
			// (compat_declaration.go) through ConfigFromProject.
			name: "MigrationSafetyConfig",
			typ:  reflect.TypeFor[MigrationSafetyConfig](),
			want: []string{"destructive_change", "enabled", "require_compat_declaration", "unsafe_add_column", "volatile_default"},
		},
		{
			// Only the two escape hatches. The contract rules are
			// unconditional; the lint is always on.
			name: "ContractsConfig",
			typ:  reflect.TypeFor[ContractsConfig](),
			want: []string{"exclude", "interface_types"},
		},
		{
			name: "LintConfig",
			typ:  reflect.TypeFor[LintConfig](),
			// allow_skip: read by the lint command (internal/cli/lint/lint_skip.go).
			want: []string{"allow_skip", "frontend", "handler_file_max_loc", "rules", "tracing"},
		},
		{
			name: "LintSkipAllowance",
			typ:  reflect.TypeFor[LintSkipAllowance](),
			want: []string{"lane", "reason"},
		},
		{
			// tracing: read by the tracing lane (internal/cli/lint/lint_tracing.go).
			name: "TracingLintConfig",
			typ:  reflect.TypeFor[TracingLintConfig](),
			want: []string{"log_wrappers"},
		},
		{
			name: "LogWrapperConfig",
			typ:  reflect.TypeFor[LogWrapperConfig](),
			want: []string{"call", "context_call"},
		},
		{
			// proto_package: read by generator.ResolveServiceProtoPackage,
			// the package a scaffolded service declares.
			name: "APIConfig",
			typ:  reflect.TypeFor[APIConfig](),
			want: []string{"openapi", "proto_package", "rest"},
		},
		{
			// hooks: read by the "generate hooks (forge.yaml)" pipeline
			// step (internal/cli/generate_hooks.go).
			name: "GenerateConfig",
			typ:  reflect.TypeFor[GenerateConfig](),
			want: []string{"hooks"},
		},
		{
			// vendored_contracts: read by `forge project upgrade`
			// (internal/cli/upgrade_converge.go).
			name: "UpgradeConfig",
			typ:  reflect.TypeFor[UpgradeConfig](),
			want: []string{"vendored_contracts"},
		},
		{
			name: "VendoredContract",
			typ:  reflect.TypeFor[VendoredContract](),
			want: []string{"commands", "env_path", "name", "report", "schema"},
		},
		{
			name: "GenerateHook",
			typ:  reflect.TypeFor[GenerateHook](),
			want: []string{"command", "dir", "name", "writes"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := knownNames(yamlKeysOf(tc.typ))
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("%s yaml keys:\n got %v\nwant %v", tc.name, got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("%s yaml keys:\n got %v\nwant %v", tc.name, got, want)
				}
			}
		})
	}
}

// TestSchemaSurface_KindIsNotAuthored pins that `kind` is derived, never
// parsed: the field carries a yaml:"-" tag so a stale `kind:` in a file is
// reported as a retired key rather than silently overriding what the
// project's real sources say.
func TestSchemaSurface_KindIsNotAuthored(t *testing.T) {
	if _, ok := yamlKeysOf(reflect.TypeFor[ProjectConfig]())["kind"]; ok {
		t.Error("`kind` must not be a parsed forge.yaml key — it derives from the project's sources")
	}
	if _, retired := removedSchemaKeys["kind"]; !retired {
		t.Error("`kind` must stay in removedSchemaKeys so a stale key gets a migration hint")
	}
}

// TestConfigWarnings_DedupAcrossLabelSpellings pins that the same retired key
// in the same file warns ONCE per process even when callers spell the path
// differently. A single command loads forge.yaml through several call sites,
// some passing an absolute path and some the bare filename; keying the dedup
// set on the raw label made those look like two files and printed every
// retired-key notice twice.
func TestConfigWarnings_DedupAcrossLabelSpellings(t *testing.T) {
	var sink strings.Builder
	prev := SetConfigWarningSink(&sink)
	defer SetConfigWarningSink(prev)

	const in = "name: demo\nmodule_path: github.com/example/demo\nversion: 1.0.0\n"
	for _, label := range []string{"forge.yaml", "/abs/path/to/forge.yaml", "./forge.yaml"} {
		if _, err := LoadProject([]byte(in), label); err != nil {
			t.Fatalf("retired key must not gate the load (%s): %v", label, err)
		}
	}

	if got := strings.Count(sink.String(), `"version" is no longer`); got != 1 {
		t.Errorf("warning emitted %d times across label spellings, want 1:\n%s", got, sink.String())
	}
}
