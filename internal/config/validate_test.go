package config

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// validBaseYAML is a minimal forge.yaml that satisfies all required-field
// rules. Tests start from this and inject the fault under test so that
// other validation phases stay green and failures are unambiguous.
const validBaseYAML = `name: demo
module_path: github.com/example/demo
database:
  migration_safety:
    enabled: true
ci:
  provider: github
docker:
  build_contexts:
    shared: ../shared
k8s:
  kcl_dir: deploy/kcl
lint:
  frontend:
    css_health: true
contracts:
  exclude: []
`

func TestLoadProject_ValidConfig(t *testing.T) {
	cfg, err := LoadProject([]byte(validBaseYAML), "forge.yaml")
	if err != nil {
		t.Fatalf("expected clean load, got: %v", err)
	}
	if cfg.Name != "demo" || cfg.ModulePath != "github.com/example/demo" {
		t.Errorf("unexpected parse result: %+v", cfg)
	}
}

func TestLoadProject_UnknownKey_WithCloseMatch(t *testing.T) {
	in := strings.Replace(validBaseYAML, "contracts:", "contarcts:", 1)
	_, err := LoadProject([]byte(in), "forge.yaml")
	ve := requireValidationError(t, err)
	if !containsAll(ve.Error(), "unknown key", "contarcts", "did you mean", "contracts") {
		t.Errorf("expected typo suggestion in error, got:\n%s", ve.Error())
	}
}

func TestLoadProject_UnknownKey_NoCloseMatch(t *testing.T) {
	in := validBaseYAML + "completely_unrelated_key: 42\n"
	_, err := LoadProject([]byte(in), "forge.yaml")
	ve := requireValidationError(t, err)
	if !containsAll(ve.Error(), "unknown key", "completely_unrelated_key") {
		t.Errorf("expected unknown-key error, got:\n%s", ve.Error())
	}
	if strings.Contains(ve.Error(), "did you mean") {
		t.Errorf("expected no suggestion for distant key, got:\n%s", ve.Error())
	}
}

func TestLoadProject_MultipleUnknownKeys(t *testing.T) {
	// Two typo'd top-level keys, each near a still-present forge.yaml key:
	// `contarcts`→`contracts` and `databse`→`database`.
	in := validBaseYAML + "contarcts: x\ndatabse: y\n" //nolint:misspell // intentional typo for suggestion test
	// Drop the real contracts:/database: blocks first so we don't get a duplicate
	// issue from the still-valid originals while testing the typos.
	in = strings.Replace(in, "contracts:\n  exclude: []\n", "", 1)
	in = strings.Replace(in, "database:\n  migration_safety:\n    enabled: true\n", "", 1)
	_, err := LoadProject([]byte(in), "forge.yaml")
	ve := requireValidationError(t, err)
	if !containsAll(ve.Error(), "contarcts", "contracts", "databse", "database") { //nolint:misspell // checks suggestion output
		t.Errorf("expected both typos with suggestions, got:\n%s", ve.Error())
	}
}

func TestLoadProject_ConfigGuard_InvalidEnforceValue(t *testing.T) {
	in := validBaseYAML + "config:\n  enforce_typed_access: nonsense\n"
	_, err := LoadProject([]byte(in), "forge.yaml")
	ve := requireValidationError(t, err)
	if !containsAll(ve.Error(), "config.enforce_typed_access", "nonsense", "off", "warn", "error") {
		t.Errorf("expected enum-rejection error listing valid values, got:\n%s", ve.Error())
	}
}

func TestLoadProject_ConfigGuard_ValidValues(t *testing.T) {
	for _, v := range []string{"off", "warn", "error", "warning", "Error"} {
		in := validBaseYAML + "config:\n  enforce_typed_access: " + v + "\n"
		if _, err := LoadProject([]byte(in), "forge.yaml"); err != nil {
			t.Errorf("value %q should load clean, got: %v", v, err)
		}
	}
}

// TestLoadProject_DockerBaseImages_RejectedAsUnknownKey asserts the base-image
// surface is fully GONE: forge is base-image-agnostic, so a forge.yaml that
// still carries the old `docker.base_images` block fails the strict loader as
// an unknown key rather than being silently accepted. (The Dockerfile's FROM
// is the complete source of truth for bases/mirrors/pins now.)
func TestLoadProject_DockerBaseImages_RejectedAsUnknownKey(t *testing.T) {
	in := strings.Replace(validBaseYAML,
		"docker:\n  build_contexts:\n    shared: ../shared\n",
		"docker:\n  build_contexts:\n    shared: ../shared\n  base_images:\n    mirror_prefix: us-docker.pkg.dev/p/dockerhub\n    tags:\n      - alpine:3.21\n",
		1)
	_, err := LoadProject([]byte(in), "forge.yaml")
	ve := requireValidationError(t, err)
	if !containsAll(ve.Error(), "unknown key", "base_images") {
		t.Errorf("expected base_images unknown-key rejection, got:\n%s", ve.Error())
	}
}

func TestLoadProject_ConfigGuard_AbsentDefaultsToWarn(t *testing.T) {
	cfg, err := LoadProject([]byte(validBaseYAML), "forge.yaml")
	if err != nil {
		t.Fatalf("clean load: %v", err)
	}
	if got := cfg.Config.EffectiveEnforceTypedAccess(); got != EnforceTypedAccessWarn {
		t.Errorf("absent config: block → %q, want warn", got)
	}
	if got := cfg.Config.EffectiveLoaderPackage(); got != DefaultLoaderPackage {
		t.Errorf("absent loader_package → %q, want %q", got, DefaultLoaderPackage)
	}
}

func TestLoadProject_MissingRequired_ModulePath(t *testing.T) {
	in := strings.Replace(validBaseYAML, "module_path: github.com/example/demo\n", "", 1)
	_, err := LoadProject([]byte(in), "forge.yaml")
	ve := requireValidationError(t, err)
	if !containsAll(ve.Error(), "module_path", "required") {
		t.Errorf("expected module_path required error, got:\n%s", ve.Error())
	}
}

func TestLoadProject_MissingRequired_Multiple(t *testing.T) {
	in := strings.Replace(validBaseYAML, "name: demo\n", "", 1)
	in = strings.Replace(in, "module_path: github.com/example/demo\n", "", 1)
	_, err := LoadProject([]byte(in), "forge.yaml")
	ve := requireValidationError(t, err)
	got := ve.Error()
	if !strings.Contains(got, "'name' is required") {
		t.Errorf("expected 'name' required, got:\n%s", got)
	}
	if !strings.Contains(got, "'module_path' is required") {
		t.Errorf("expected 'module_path' required, got:\n%s", got)
	}
}

func TestLoadProject_TypeMismatch(t *testing.T) {
	// lint.frontend.css_health is a bool; pass a string to surface a yaml type error.
	in := strings.Replace(validBaseYAML, "css_health: true", "css_health: \"not-a-bool\"", 1)
	_, err := LoadProject([]byte(in), "forge.yaml")
	ve := requireValidationError(t, err)
	if !strings.Contains(ve.Error(), "cannot unmarshal") {
		t.Errorf("expected type-mismatch error mentioning unmarshal, got:\n%s", ve.Error())
	}
}

func TestLoadProject_NestedUnknownKey(t *testing.T) {
	// migration_safety has a bogus subkey "unsafe_add_colum" — should be detected at
	// the nested level with a path-prefixed message and a suggestion.
	// (Components moved out of forge.yaml, so the nested-walk path is now
	// exercised against a still-YAML-parsed block.)
	in := strings.Replace(validBaseYAML, "    enabled: true\n",
		"    enabled: true\n    unsafe_add_colum: warn\n", 1)
	_, err := LoadProject([]byte(in), "forge.yaml")
	ve := requireValidationError(t, err)
	if !containsAll(ve.Error(), "database.migration_safety.unsafe_add_colum", "did you mean", "unsafe_add_column") {
		t.Errorf("expected nested-path unknown-key + suggestion, got:\n%s", ve.Error())
	}
}

func TestLoadProject_InvalidModulePath(t *testing.T) {
	in := strings.Replace(validBaseYAML, "module_path: github.com/example/demo", "module_path: notamodule", 1)
	_, err := LoadProject([]byte(in), "forge.yaml")
	ve := requireValidationError(t, err)
	if !strings.Contains(ve.Error(), "does not look like a Go module path") {
		t.Errorf("expected module-path shape warning, got:\n%s", ve.Error())
	}
}

func TestLoadProject_FourIssuesAtOnce(t *testing.T) {
	// Smoke test mirroring the CLI smoke: 3 typos + 1 missing required
	// field should all surface in a single error.
	in := strings.Replace(validBaseYAML, "contracts:", "contarcts:", 1)
	// `components` is no longer a forge.yaml key, so the third typo targets
	// another still-present top-level key: `docker`→`dockr`.
	in = strings.Replace(in, "docker:", "dockr:", 1)
	in = strings.Replace(in, "database:", "databse:", 1)
	in = strings.Replace(in, "module_path: github.com/example/demo\n", "", 1)

	_, err := LoadProject([]byte(in), "forge.yaml")
	ve := requireValidationError(t, err)
	got := ve.Error()
	for _, want := range []string{"contarcts", "dockr", "databse", "module_path"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in error, got:\n%s", want, got)
		}
	}
	for _, suggestion := range []string{"contracts", "docker", "database"} {
		if !strings.Contains(got, suggestion) {
			t.Errorf("expected suggestion %q, got:\n%s", suggestion, got)
		}
	}
}

// TestLoadProject_NestedUnknownKey_LineAndPath pins down both the
// dot-notation path and the YAML line number for an unknown nested
// key. Earlier reports observed the wrong line / wrong path in
// nested cases, so this test asserts both invariants explicitly: the
// reported line must be the literal line of the offending key in the
// input, and the dot-path must match where the key actually lives in
// the YAML tree.
func TestLoadProject_NestedUnknownKey_LineAndPath(t *testing.T) {
	// Inject a genuinely unknown subkey "bogus_knob: 1" inside k8s: at a
	// known position so we can compute the expected line precisely. We use
	// a NEVER-a-real-key name (not a removed key like k8s.provider, which
	// is now a non-fatal warning) so this stays a fatal error and keeps
	// exercising the line/path reporting it was written to pin.
	in := strings.Replace(validBaseYAML,
		"k8s:\n  kcl_dir: deploy/kcl\n",
		"k8s:\n  bogus_knob: 1\n  kcl_dir: deploy/kcl\n",
		1)
	wantLine := lineOf(t, in, "  bogus_knob: 1")
	_, err := LoadProject([]byte(in), "forge.yaml")
	ve := requireValidationError(t, err)
	got := ve.Error()
	if !strings.Contains(got, `"k8s.bogus_knob"`) {
		t.Errorf("expected key path 'k8s.bogus_knob' in error, got:\n%s", got)
	}
	// Standard compiler/editor format: `path:line:col:`. Lets an LLM
	// (or human in vim/emacs/VS Code) jump straight to the offending
	// token without grep round-trips.
	wantLineMarker := "forge.yaml:" + strconv.Itoa(wantLine) + ":"
	if !strings.Contains(got, wantLineMarker) {
		t.Errorf("expected %q in error (the literal line of `bogus_knob: 1`), got:\n%s", wantLineMarker, got)
	}
}

// TestLoadProject_UnknownKeyClassification is the table-driven matrix for
// the unknown-key outcomes, all fatal:
//
//  1. retired key  → migration hint, NO Levenshtein suggestion
//  2. typo'd key   → "did you mean" suggestion
//  3. distant key  → plain unknown-key, no suggestion/hint
//
// Retired keys must win over suggestions: an agent that sees
// "did you mean 'kcl_dir'?" for k8s.provider would rename instead of
// migrating.
func TestLoadProject_UnknownKeyClassification(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(string) string // injects the fault into validBaseYAML
		wantSubstr []string            // all must appear in the surfaced message
		notSubstr  []string            // none may appear in the surfaced message
	}{
		{
			name: "retired key k8s.provider fails with migration hint",
			mutate: func(in string) string {
				return strings.Replace(in, "k8s:\n  kcl_dir: deploy/kcl\n",
					"k8s:\n  kcl_dir: deploy/kcl\n  provider: k3d\n", 1)
			},
			wantSubstr: []string{
				`"k8s.provider" is no longer a forge.yaml key`,
				"forge.K8sCluster",
			},
			notSubstr: []string{"did you mean"},
		},
		{
			name: "retired top-level key services fails with real-source migration hint",
			mutate: func(in string) string {
				return in + "services:\n  - name: api\n    type: go_service\n    path: handlers/api\n"
			},
			wantSubstr: []string{
				`"services" is no longer a forge.yaml key`,
				"forge scaffold service",
			},
			notSubstr: []string{"did you mean"},
		},
		{
			name: "retired top-level key docs fails with its hint",
			mutate: func(in string) string {
				return in + "docs:\n  output_dir: docs/generated\n"
			},
			wantSubstr: []string{
				`"docs" is no longer a forge.yaml key`,
				"forge no longer generates documentation",
			},
			notSubstr: []string{"did you mean"},
		},
		{
			name: "typo'd key gets a fatal did-you-mean suggestion",
			mutate: func(in string) string {
				return strings.Replace(in, "contracts:", "contarcts:", 1)
			},
			wantSubstr: []string{"unknown key", "contarcts", "did you mean", "contracts"},
			notSubstr:  []string{"is no longer a forge.yaml key"},
		},
		{
			name: "distant key gets a fatal plain unknown-key error",
			mutate: func(in string) string {
				return in + "completely_unrelated_key: 42\n"
			},
			wantSubstr: []string{"unknown key", "completely_unrelated_key"},
			notSubstr:  []string{"did you mean", "is no longer a forge.yaml key"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadProject([]byte(tc.mutate(validBaseYAML)), "forge.yaml")
			got := requireValidationError(t, err).Error()
			for _, want := range tc.wantSubstr {
				if !strings.Contains(got, want) {
					t.Errorf("expected %q in surfaced message, got:\n%s", want, got)
				}
			}
			for _, not := range tc.notSubstr {
				if strings.Contains(got, not) {
					t.Errorf("did not expect %q in surfaced message, got:\n%s", not, got)
				}
			}
		})
	}
}

// TestLoadProject_EveryRemovedKeyIsDiagnosed drives the WHOLE
// removedSchemaKeys registry instead of the keys
// TestLoadProject_UnknownKeyClassification names by hand. Retiring a key
// therefore comes with its coverage automatically: add the registry entry and
// this test exercises it, forget the entry and nothing here changes — which is
// the case worth thinking about below.
//
// The nested (`features.*`, `auth.*`) entries are the reason this is a sweep
// rather than another hand-picked case. A field deleted from a nested struct
// leaves its key with nothing to decode into, so the typed unmarshal drops it
// without a word and the unknown-key walk is the ONLY thing that ever sees it.
// The walk does see it — `features` is a struct, so it descends — and it
// resolves the qualified path against the registry BEFORE reaching for a
// Levenshtein suggestion, which is what turns a retired key into migration
// guidance rather than a "did you mean …?" that would send an agent renaming
// instead of migrating. A retired key with no registry entry is not silent
// either: it falls through to the fatal unknown-key error.
//
// Each entry is pinned on three things:
//
//  1. the load FAILS — forge does not tolerate a retired key;
//  2. the error names the key and quotes the registry's own fix hint, so the
//     message can't rot into a bare notice;
//  3. no typo suggestion rides along.
func TestLoadProject_EveryRemovedKeyIsDiagnosed(t *testing.T) {
	// A bare required-fields-only fixture, not validBaseYAML: several retired
	// keys hang off a section validBaseYAML already declares (`ci:`, `k8s:`),
	// and appending a second block for one is a duplicate-mapping-key parse
	// error rather than the unknown-key case under test.
	const requiredOnly = "name: demo\nmodule_path: github.com/example/demo\n"

	for key, hint := range removedSchemaKeys {
		// Slice-element paths ("services[].dev_target") need a populated list
		// element to reach; their parent list key is itself retired, so the
		// walk reports the parent. Same for any key nested under another
		// retired key — the ancestor is what surfaces.
		if strings.Contains(key, "[]") || hasRetiredAncestor(key) {
			continue
		}
		t.Run(key, func(t *testing.T) {
			_, err := LoadProject([]byte(requiredOnly+nestedYAML(key)), "forge.yaml")
			got := requireValidationError(t, err).Error()
			if !containsAll(got, `"`+key+`" is no longer a forge.yaml key`, hint) {
				t.Errorf("the error must name the key and carry the registry's migration hint, got:\n%s", got)
			}
			if strings.Contains(got, "did you mean") {
				t.Errorf("a retired key must not emit a typo suggestion, got:\n%s", got)
			}
		})
	}
}

// hasRetiredAncestor reports whether a dotted key path sits under another
// entry in removedSchemaKeys. The unknown-key walk stops at the outermost
// unknown key, so only the ancestor's message is ever surfaced.
func hasRetiredAncestor(key string) bool {
	parts := strings.Split(key, ".")
	for i := 1; i < len(parts); i++ {
		if _, ok := removedSchemaKeys[strings.Join(parts[:i], ".")]; ok {
			return true
		}
	}
	return false
}

// nestedYAML renders a dotted key path as the YAML block that plants it —
// "features.experimental.deploy" becomes three indented lines. The value is
// always a scalar: the unknown-key walk matches on the key path alone, and an
// unknown key never reaches the typed decode where its shape would matter.
func nestedYAML(key string) string {
	var b strings.Builder
	for depth, part := range strings.Split(key, ".") {
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteString(part)
		b.WriteString(":\n")
	}
	// Re-open the last line to hang the scalar off it.
	return strings.TrimSuffix(b.String(), ":\n") + ": true\n"
}

// TestLoadProject_NoDeprecatedKeyNoWarning guards against false-positive
// warnings: a clean config must produce no warning output.
func TestLoadProject_NoDeprecatedKeyNoWarning(t *testing.T) {
	var sink strings.Builder
	prev := SetConfigWarningSink(&sink)
	defer SetConfigWarningSink(prev)

	if _, err := LoadProject([]byte(validBaseYAML), "forge.yaml"); err != nil {
		t.Fatalf("expected clean config to load, got: %v", err)
	}
	if out := sink.String(); out != "" {
		t.Errorf("expected no warnings on a clean config, got: %q", out)
	}
}

func TestNormalizeKeyPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"services[0].dev_target", "services[].dev_target"},
		{"services[12].dev_target", "services[].dev_target"},
		{"k8s.provider", "k8s.provider"},
		{"binaries[3].kind", "binaries[].kind"},
	}
	for _, c := range cases {
		if got := normalizeKeyPath(c.in); got != c.want {
			t.Errorf("normalizeKeyPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		// transposition counts as 2 substitutions in classic Levenshtein
		{"auth", "auht", 2},
		{"environments", "environments", 0},
		// typo: 'enviornments' vs 'environments' is a single transposition,
		// i.e. distance 2 in classic Levenshtein (no transposition op).
		{"enviornments", "environments", 2}, //nolint:misspell // intentional typo for distance test
		{"hello", "world", 4},
	}
	for _, c := range cases {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestClosestMatch_Threshold(t *testing.T) {
	cands := []string{"auth", "environments", "services"}
	if got := closestMatch("auht", cands); got != "auth" {
		t.Errorf("closestMatch auht: got %q want auth", got)
	}
	if got := closestMatch("totally_different_key", cands); got != "" {
		t.Errorf("expected no match for distant key, got %q", got)
	}
	// 'enviornments' (12 chars) vs 'environments' (12 chars) is distance 2 —
	// well within the 3-char tolerance for length >= 8.
	if got := closestMatch("environments", cands); got != "environments" {
		t.Errorf("closestMatch environments: got %q want environments", got)
	}
}

func requireValidationError(t *testing.T, err error) *ValidationError {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	return ve
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// lineOf returns the 1-based line number of the first line in input
// whose trimmed content equals (or starts with) marker. Test helper
// used to pin "the validator reports the exact line of <X>" without
// hard-coding fragile line numbers that drift as validBaseYAML
// evolves.
func lineOf(t *testing.T, input, marker string) int {
	t.Helper()
	for i, line := range strings.Split(input, "\n") {
		if line == marker || strings.HasPrefix(strings.TrimRight(line, "\r"), marker) {
			return i + 1
		}
	}
	t.Fatalf("marker %q not found in input", marker)
	return 0
}

// TestLoadProject_RegistryKeysAreRefused: an image registry is declared in the
// env's KCL and nowhere else, so forge.yaml's `docker.registry` and
// `deploy.registry` are gone — and a forge.yaml still carrying one FAILS to
// load with the runbook, rather than warning. A warning would let the key
// linger while the build silently stopped honouring it, which is exactly the
// quiet change of push destination the refusal exists to prevent.
func TestLoadProject_RegistryKeysAreRefused(t *testing.T) {
	for _, tc := range []struct{ name, in, key string }{
		{"docker.registry", strings.Replace(validBaseYAML, "docker:\n  build_contexts:", "docker:\n  registry: ghcr.io\n  build_contexts:", 1), "docker.registry"},
		{"deploy.registry", validBaseYAML + "deploy:\n  registry: gar\n", "deploy.registry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.in, strings.SplitN(tc.key, ".", 2)[0]+":\n  registry:") {
				t.Fatalf("fixture precondition: input does not carry %s:\n%s", tc.key, tc.in)
			}
			_, err := LoadProject([]byte(tc.in), "forge.yaml")
			ve := requireValidationError(t, err)
			if !containsAll(ve.Error(), `"`+tc.key+`"`, "deploy/kcl/<env>/main.k", "forge.ClusterTarget") {
				t.Errorf("%s: want a refusal naming the key and where the registry is declared, got:\n%s", tc.key, ve.Error())
			}
			if strings.Contains(ve.Error(), "did you mean") {
				t.Errorf("%s: a retired key must get its runbook, not a typo suggestion:\n%s", tc.key, ve.Error())
			}
		})
	}
}

// docker.build_contexts is NOT a registry and stays: a docker block that
// carries only it loads cleanly.
func TestLoadProject_DockerBuildContextsStay(t *testing.T) {
	cfg, err := LoadProject([]byte(validBaseYAML), "forge.yaml")
	if err != nil {
		t.Fatalf("docker.build_contexts alone must load: %v", err)
	}
	if cfg.Docker.BuildContexts["shared"].Path != "../shared" {
		t.Errorf("BuildContexts = %v, want the declared context", cfg.Docker.BuildContexts)
	}
}

// TestLoadProject_RemovedBlocksAreRefusedWithTheirHint pins every key the
// forge.yaml cleanup removed. Each must FAIL the load (not warn) — a stale value
// that merely stopped being read would silently change what forge does — and the
// failure must carry the specific migration, never a typo suggestion.
func TestLoadProject_RemovedBlocksAreRefusedWithTheirHint(t *testing.T) {
	const base = "name: demo\nmodule_path: github.com/example/demo\n"
	cases := []struct {
		name  string
		yaml  string
		key   string
		hints []string
	}{
		{"stack", "stack:\n  frontend:\n    framework: nextjs\n", "stack",
			[]string{"frontends/<name>", "forge.Frontend"}},
		{"frontends", "frontends:\n  - name: web\n    type: nextjs\n", "frontends",
			[]string{"forge.Frontend", "routes", "frontends/"}},
		{"frontend", "frontend:\n  workspaces: true\n", "frontend",
			[]string{"pnpm-workspace.yaml"}},
		{"smoke", "smoke:\n  flow_checks:\n    - name: x\n      url: http://localhost:1/x\n", "smoke",
			[]string{"flow_checks", "forge.FlowCheck", "workload"}},
		{"database.driver", "database:\n  driver: postgres\n", "database.driver",
			[]string{"derived", "db/migrations"}},
		{"database.seed", "database:\n  seed:\n    rows: 5\n", "database.seed",
			[]string{"20 rows", "--no-seed"}},
		{"database.migration_safety.allowed_destructive",
			"database:\n  migration_safety:\n    allowed_destructive:\n      - db/migrations/1_x.up.sql\n",
			"database.migration_safety.allowed_destructive",
			[]string{"-- forge:allow-destructive"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadProject([]byte(base+tc.yaml), "forge.yaml")
			ve := requireValidationError(t, err)
			got := ve.Error()
			if !strings.Contains(got, `"`+tc.key+`" is no longer a forge.yaml key`) {
				t.Errorf("want a refusal naming %q, got:\n%s", tc.key, got)
			}
			if !containsAll(got, tc.hints...) {
				t.Errorf("refusal for %q must carry its migration hint %v, got:\n%s", tc.key, tc.hints, got)
			}
			if strings.Contains(got, "did you mean") {
				t.Errorf("a retired key must get its runbook, not a typo suggestion:\n%s", got)
			}
		})
	}
}

// TestRemovedBlocksAreRefusedNotWarned guards the registry choice itself: these
// keys live in refusedSchemaKeys (fatal), never removedSchemaKeys (warning).
func TestRemovedBlocksAreRefusedNotWarned(t *testing.T) {
	for _, key := range []string{"stack", "frontends", "frontend", "smoke",
		"database.driver", "database.seed",
		"database.migration_safety.allowed_destructive"} {
		if _, ok := refusedSchemaKeys[key]; !ok {
			t.Errorf("%q must be in refusedSchemaKeys so a stale value cannot be silently ignored", key)
		}
		if _, ok := removedSchemaKeys[key]; ok {
			t.Errorf("%q must not also be in removedSchemaKeys (it would shadow nothing and mislead)", key)
		}
	}
}
