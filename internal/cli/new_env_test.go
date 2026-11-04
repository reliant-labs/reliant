package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProjectFixture lays down a minimal forge project (forge.yaml +
// deploy/kcl/<env>/main.k) so projectRoot() resolves and ListEnvs() sees the
// template env. The template main.k mimics the dangerous-knob SHAPES a real
// forge env carries (option-default tag/registry/namespace, a ClusterTarget
// with cluster/platform, supabase identity) but stays plain-KCL-compilable so
// the --check render path can run hermetically (no forge KCL module needed).
func writeProjectFixture(t *testing.T, template string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("project: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	envDir := filepath.Join(dir, "deploy", "kcl", template)
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Note: the cluster-target var name follows the `_<env>_k8s` convention so
	// the rename transform has something to rewrite.
	main := `_image_tag = option("image_tag") or "` + template + `"
_namespace = option("namespace") or "app-` + template + `"

_` + template + `_k8s = {
    cluster = "gke_acme_` + template + `"
    namespace = _namespace
    platform = "amd64"
}

_supabase_url = "https://proj.supabase.co"
_supabase_jwt_issuer = "https://proj.supabase.co/auth/v1"

# A line that references the cluster-target var, to prove the rename.
cluster_name = _` + template + `_k8s.cluster
`
	if err := os.WriteFile(filepath.Join(envDir, "main.k"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// withCwd (chdir-with-restore) is shared from upgrade_migrations_test.go.

func TestNewEnv_ScaffoldsPlaceholdersFromTemplate(t *testing.T) {
	dir := writeProjectFixture(t, "staging")

	withCwd(t, dir, func() {
		if err := runNewEnv(context.Background(), "preview", "staging", false, false); err != nil {
			t.Fatalf("new-env: %v", err)
		}
	})

	got, err := os.ReadFile(filepath.Join(dir, "deploy", "kcl", "preview", "main.k"))
	if err != nil {
		t.Fatalf("read generated main.k: %v", err)
	}
	body := string(got)

	// The dangerous knobs must be neutralized to placeholders — NOT silently
	// inherited from staging. This is the whole point of the command.
	for _, want := range []string{
		// No REPLACE_ME_REGISTRY: an env declares no registry, so a derived env
		// has none to get wrong and nothing to placeholder.
		"REPLACE_ME_NAMESPACE",
		"REPLACE_ME_CLUSTER_CONTEXT",
		"REPLACE_ME_PLATFORM",
		"REPLACE_ME_SUPABASE_URL",
		"REPLACE_ME_SUPABASE_JWT_ISSUER",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("generated main.k missing placeholder %q\n---\n%s", want, body)
		}
	}

	// The inherited literal values must be GONE (the footgun: copy-paste keeps
	// them).
	for _, gone := range []string{"ghcr.io/acme", "app-staging", "gke_acme_staging", `platform = "amd64"`} {
		if strings.Contains(body, gone) {
			t.Errorf("generated main.k still carries inherited literal %q (should be a placeholder)", gone)
		}
	}

	// Every dangerous-knob placeholder must carry inline 'check:' guidance.
	if c := strings.Count(body, "#   check:"); c < 5 {
		t.Errorf("expected a 'check:' guidance comment per dangerous knob; got %d", c)
	}

	// image_tag is a SAFE knob (just a tag string): it should default to the
	// new env name, not become a placeholder.
	if !strings.Contains(body, `option("image_tag") or "preview"`) {
		t.Errorf("image_tag should default to the new env name 'preview'\n---\n%s", body)
	}

	// The cluster-target local var must be renamed to the new env so the file
	// is self-consistent (no dangling _staging_k8s reference).
	if strings.Contains(body, "_staging_k8s") {
		t.Errorf("generated main.k still references the template's _staging_k8s var")
	}
	if !strings.Contains(body, "_preview_k8s") {
		t.Errorf("generated main.k did not rename the cluster-target var to _preview_k8s\n---\n%s", body)
	}
}

func TestNewEnv_CheckFailsWhilePlaceholdersRemain(t *testing.T) {
	dir := writeProjectFixture(t, "staging")

	withCwd(t, dir, func() {
		if err := runNewEnv(context.Background(), "preview", "staging", false, false); err != nil {
			t.Fatalf("new-env: %v", err)
		}
		// --check must FAIL: the fresh scaffold is all placeholders. This is
		// the build-time gate that converts the silent footgun into an error.
		err := runNewEnv(context.Background(), "preview", "", true, false)
		if err == nil {
			t.Fatal("new-env --check passed on an un-filled env; the gate must fail while REPLACE_ME_* remains")
		}
		if !strings.Contains(err.Error(), "REPLACE_ME") {
			t.Errorf("--check error should name the remaining placeholders; got: %v", err)
		}
	})
}

func TestNewEnv_CheckPassesOnceFilledAndCompiles(t *testing.T) {
	dir := writeProjectFixture(t, "staging")

	withCwd(t, dir, func() {
		if err := runNewEnv(context.Background(), "preview", "staging", false, false); err != nil {
			t.Fatalf("new-env: %v", err)
		}
	})

	// Fill every placeholder with a real value (the author's job).
	mainPath := filepath.Join(dir, "deploy", "kcl", "preview", "main.k")
	got, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	filled := string(got)
	repl := map[string]string{
		"REPLACE_ME_REGISTRY":            "ghcr.io/acme",
		"REPLACE_ME_NAMESPACE":           "app-preview",
		"REPLACE_ME_CLUSTER_CONTEXT":     "gke_acme_preview",
		"REPLACE_ME_PLATFORM":            "amd64",
		"REPLACE_ME_SUPABASE_URL":        "https://preview.supabase.co",
		"REPLACE_ME_SUPABASE_JWT_ISSUER": "https://preview.supabase.co/auth/v1",
	}
	// Replace the longest tokens first so REPLACE_ME_SUPABASE_URL isn't eaten
	// by a REPLACE_ME prefix match.
	for _, k := range []string{
		"REPLACE_ME_SUPABASE_JWT_ISSUER", "REPLACE_ME_SUPABASE_URL",
		"REPLACE_ME_CLUSTER_CONTEXT", "REPLACE_ME_NAMESPACE",
		"REPLACE_ME_REGISTRY", "REPLACE_ME_PLATFORM",
	} {
		filled = strings.ReplaceAll(filled, k, repl[k])
	}
	if strings.Contains(stripComments(filled), "REPLACE_ME") {
		t.Fatalf("test bug: a live placeholder survived the fill\n%s", filled)
	}
	if err := os.WriteFile(mainPath, []byte(filled), 0o644); err != nil {
		t.Fatal(err)
	}

	withCwd(t, dir, func() {
		// --check must now PASS: no placeholders AND the env KCL-compiles.
		if err := runNewEnv(context.Background(), "preview", "", true, false); err != nil {
			t.Fatalf("new-env --check failed after filling placeholders: %v", err)
		}
	})
}

// stripComments removes `#...` tails so the test's own placeholder-survival
// assertion ignores the guidance comments (which legitimately mention the
// bare word REPLACE_ME).
func stripComments(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if h := strings.Index(line, "#"); h >= 0 {
			line = line[:h]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestNewEnv_AutoPicksCloudShapedTemplate(t *testing.T) {
	dir := writeProjectFixture(t, "staging")
	// Add a host-mode 'dev' env with NO platform knob — auto-pick must prefer
	// the cloud-shaped 'staging', not the alphabetically-first 'dev'.
	devDir := filepath.Join(dir, "deploy", "kcl", "dev")
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devDir, "main.k"), []byte("_x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	envs, err := ListEnvs(dir)
	if err != nil {
		t.Fatal(err)
	}
	pick, err := resolveTemplateEnv(dir, envs, "", "preview")
	if err != nil {
		t.Fatal(err)
	}
	if pick != "staging" {
		t.Errorf("auto-pick chose %q; expected the cloud-shaped 'staging' over host-mode 'dev'", pick)
	}
}

func TestNewEnv_RejectsExistingWithoutForce(t *testing.T) {
	dir := writeProjectFixture(t, "staging")
	// Pre-create the target env dir.
	if err := os.MkdirAll(filepath.Join(dir, "deploy", "kcl", "preview"), 0o755); err != nil {
		t.Fatal(err)
	}
	withCwd(t, dir, func() {
		err := runNewEnv(context.Background(), "preview", "staging", false, false)
		if err == nil {
			t.Fatal("new-env overwrote an existing env without --force")
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("expected an 'already exists' error; got: %v", err)
		}
	})
}

func TestNewEnv_InvalidName(t *testing.T) {
	dir := writeProjectFixture(t, "staging")
	withCwd(t, dir, func() {
		for _, bad := range []string{"", "Preview", "1env", "env_underscore", "env/slash"} {
			if err := runNewEnv(context.Background(), bad, "staging", false, false); err == nil {
				t.Errorf("env name %q accepted; expected rejection", bad)
			}
		}
	})
}

// TestNewEnv_HasNoRegistryKnob: `forge env new` neither placeholders nor
// rewrites a registry, because an env does not declare one — a WORKLOAD does,
// as part of its image, and that declaration is shared by every env.
//
// This replaces the old REPLACE_ME_REGISTRY test. Emitting a placeholder now
// would be worse than emitting nothing: `registry` is not a field on either
// env schema, so the derived env would fail to load with an error about a
// placeholder forge itself had just written.
func TestNewEnv_HasNoRegistryKnob(t *testing.T) {
	// A leftover registry line from an OLD template is passed through
	// untouched. It is not this command's job to migrate it — `forge generate`
	// does that (internal/kclmigrate), with the ambiguity judgment that needs
	// the whole tree, which a per-line transform cannot make.
	for _, line := range []string{
		`    registry = "ghcr.io/acme"`,
		`_registry = "ghcr.io/acme"`,
	} {
		got := strings.Join(transformLine(line, "staging", "preview", "_staging_k8s", "_preview_k8s"), "\n")
		if got != line {
			t.Errorf("transformLine(%q) = %q, want it passed through unchanged", line, got)
		}
		if strings.Contains(got, "REPLACE_ME_REGISTRY") {
			t.Errorf("transformLine(%q) wrote a registry placeholder; an env has no registry field to place one in:\n%s", line, got)
		}
	}
}

func TestLineHasLivePlaceholder(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{`    registry = "REPLACE_ME_REGISTRY"`, true},
		{`    # REPLACE_ME — the image registry.`, false}, // guidance comment, not an offender
		{`    #   check:  run forge build`, false},
		{`    registry = "ghcr.io/acme"`, false},
		{`    cluster = "gke" # was REPLACE_ME_CLUSTER_CONTEXT`, false}, // token only in trailing comment
	}
	for _, c := range cases {
		if got := lineHasLivePlaceholder(c.line); got != c.want {
			t.Errorf("lineHasLivePlaceholder(%q) = %v; want %v", c.line, got, c.want)
		}
	}
}

// --bind rebinds one workload's line in the derived env; a malformed or
// unknown target names the accepted forms, and a workload the env does not
// bind is refused rather than silently ignored.
func TestNewEnv_BindRebindsOneWorkload(t *testing.T) {
	dir := writeProjectFixture(t, "staging")
	stagingMain := filepath.Join(dir, "deploy", "kcl", "staging", "main.k")
	b, _ := os.ReadFile(stagingMain)
	bindings := "\n_workloads = [\n    _on_cluster(wl.api)\n    _on_cluster(wl.migrate)\n]\n\n_bundle = {\n    project = \"fixture\"\n}\n"
	if err := os.WriteFile(stagingMain, append(b, bindings...), 0o644); err != nil {
		t.Fatal(err)
	}
	withCwd(t, dir, func() {
		run := func(args ...string) error {
			cmd := newEnvNewCmd()
			cmd.SetArgs(args)
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			return cmd.Execute()
		}
		if err := run("preview", "--from", "staging", "--bind", "api=hosted"); err != nil {
			t.Fatalf("--bind api=hosted: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "deploy", "kcl", "preview", "main.k"))
		for _, want := range []string{"_hosted(wl.api)", "_on_cluster(wl.migrate)", "control_plane = forge.ControlPlane {"} {
			if !strings.Contains(string(got), want) {
				t.Errorf("derived env lacks %q:\n%s", want, got)
			}
		}
		for _, bad := range []string{"api", "api=serverless"} {
			if err := run("p2", "--from", "staging", "--bind", bad); err == nil || !strings.Contains(err.Error(), "hosted") {
				t.Errorf("--bind %q: err = %v, want the accepted forms", bad, err)
			}
		}
		if err := run("p3", "--from", "staging", "--bind", "billing=hosted"); err == nil || !strings.Contains(err.Error(), "wl.billing") {
			t.Errorf("--bind of an unbound workload: err = %v, want a refusal naming it", err)
		}
	})
}

// The scaffolded envs write the cluster target's namespace as a LITERAL
// (`namespace = "acme-prod"`), not the option() form the transform first knew.
// Copied verbatim, a derived env deploys into the template env's namespace —
// the exact hazard `env new` exists to prevent.
//
// There is no registry half to this any more: the target declares none.
func TestNewEnv_NeutralizesLiteralNamespace(t *testing.T) {
	body := "_cluster = forge.ClusterTarget {\n    cluster = \"acme-prod\"\n    namespace = \"acme-prod\"\n    platform = \"amd64\"\n}\n"
	out := transformEnvFile(body, "prod", "cloud")
	if strings.Contains(out, `"acme-prod"`) {
		t.Errorf("derived env still carries the template's namespace:\n%s", out)
	}
	if !strings.Contains(out, `namespace = "REPLACE_ME_NAMESPACE"`) {
		t.Errorf("derived env lacks the namespace placeholder:\n%s", out)
	}
}
