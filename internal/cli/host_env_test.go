package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/reliant-labs/forge/internal/hostlaunch"
)

// TestWorkloadHostEnv covers the projection from a workload's spec.env to
// the flat NAME→VALUE map a host launch receives. Inline values pass
// through; secret / config-map references (resolved elsewhere, or with no
// host equivalent) drop; a workload with no env returns empty, not nil.
func TestWorkloadHostEnv(t *testing.T) {
	t.Run("no env returns empty map", func(t *testing.T) {
		got := hostWL("api").HostEnv()
		if got == nil {
			t.Fatal("got nil, want empty map")
		}
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("inline values only", func(t *testing.T) {
		got := hostWL("api",
			withEnv("LOG_LEVEL", "debug", "DATABASE_URL", "postgres://x"),
			withSecretRef("TOKEN", "creds", "token"),
		).HostEnv()
		if got["LOG_LEVEL"] != "debug" || got["DATABASE_URL"] != "postgres://x" {
			t.Errorf("got %v", got)
		}
		if _, leaked := got["TOKEN"]; leaked {
			t.Errorf("a secretRef has no host literal; got %v", got)
		}
	})

	t.Run("empty-name and empty-value entries dropped", func(t *testing.T) {
		got := hostWL("api", withEnv("OK", "yes", "", "no-name", "EMPTY_VALUE", "")).HostEnv()
		if len(got) != 1 {
			t.Errorf("want 1 entry, got %v", got)
		}
		if got["OK"] != "yes" {
			t.Errorf("OK: got %q", got["OK"])
		}
	})

	t.Run("launch env layers over the declaration", func(t *testing.T) {
		w := hostWL("api", withEnv("PORT", "8080", "LOG_LEVEL", "info"))
		w.Runtime.Host.LaunchEnv = map[string]string{"PORT": "41234"}
		got := w.HostEnv()
		if got["PORT"] != "41234" || got["LOG_LEVEL"] != "info" {
			t.Errorf("got %v, want PORT=41234 (launch) and LOG_LEVEL=info (declared)", got)
		}
	})
}

// TestHostEnvComposition_EnvVarsOnly: secrets_file empty, env_vars
// populated → final env carries env_vars only (plus os.Environ baseline).
func TestHostEnvComposition_EnvVarsOnly(t *testing.T) {
	secrets, err := hostlaunch.LoadSecretsFile("")
	if err != nil {
		t.Fatalf("LoadSecretsFile(\"\"): %v", err)
	}
	if secrets != nil {
		t.Errorf("empty path: want nil map, got %v", secrets)
	}
	envVars := hostWL("api", withEnv("FROM_KCL", "kcl-val")).HostEnv()
	final := hostlaunch.LayerHostEnv([]string{"PATH=/usr/bin"}, nil, secrets, envVars)
	if !envSliceContains(final, "FROM_KCL=kcl-val") {
		t.Errorf("FROM_KCL missing from final env: %v", final)
	}
}

// TestHostEnvComposition_SecretsFileOnly: secrets file present, no
// env_vars → final env carries secrets-file values.
func TestHostEnvComposition_SecretsFileOnly(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, ".env.dev.secrets")
	const content = `STRIPE_SECRET_KEY=sk_test_xxx
SUPABASE_ANON_KEY=anon_xxx
`
	if err := os.WriteFile(secretsPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	secrets, err := hostlaunch.LoadSecretsFile(secretsPath)
	if err != nil {
		t.Fatalf("LoadSecretsFile: %v", err)
	}
	if secrets["STRIPE_SECRET_KEY"] != "sk_test_xxx" {
		t.Errorf("STRIPE: got %q", secrets["STRIPE_SECRET_KEY"])
	}
	final := hostlaunch.LayerHostEnv([]string{"PATH=/usr/bin"}, nil, secrets, nil)
	if !envSliceContains(final, "STRIPE_SECRET_KEY=sk_test_xxx") {
		t.Errorf("STRIPE missing from final env: %v", final)
	}
	if !envSliceContains(final, "SUPABASE_ANON_KEY=anon_xxx") {
		t.Errorf("SUPABASE missing from final env: %v", final)
	}
}

// TestHostEnvComposition_BothEnvVarsWinOnConflict: when both layers
// declare the same key, env_vars (KCL) overrides secrets_file. This is
// the load-bearing reproducibility invariant — config can't drift
// between developer machines because the gitignored secrets file
// accidentally shadows a KCL value.
func TestHostEnvComposition_BothEnvVarsWinOnConflict(t *testing.T) {
	dir := t.TempDir()
	secretsPath := filepath.Join(dir, ".env.dev.secrets")
	const content = `LOG_LEVEL=trace
STRIPE_SECRET_KEY=sk_test_xxx
`
	if err := os.WriteFile(secretsPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
	secrets, _ := hostlaunch.LoadSecretsFile(secretsPath)
	envVars := hostWL("api", withEnv("LOG_LEVEL", "debug")).HostEnv() // collides with secrets_file
	final := hostlaunch.LayerHostEnv([]string{"PATH=/usr/bin"}, nil, secrets, envVars)

	// KCL env_vars wins on LOG_LEVEL.
	if !envSliceContains(final, "LOG_LEVEL=debug") {
		t.Errorf("LOG_LEVEL: KCL should win; final=%v", final)
	}
	if envSliceContains(final, "LOG_LEVEL=trace") {
		t.Errorf("LOG_LEVEL: secrets value leaked through; final=%v", final)
	}
	// Non-conflicting secret still appears.
	if !envSliceContains(final, "STRIPE_SECRET_KEY=sk_test_xxx") {
		t.Errorf("STRIPE: should pass through; final=%v", final)
	}
}

// TestHostEnvComposition_SecretsMissingIsWarnNotError: a missing
// secrets file is non-fatal — the runner warns and continues.
// `forge env up` should still launch the subprocess.
func TestHostEnvComposition_SecretsMissingIsWarnNotError(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.secrets")

	_, err := hostlaunch.LoadSecretsFile(missing)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("want os.ErrNotExist, got %v", err)
	}
}

// TestHostEnvComposition_SecretsUnreadable: an existing-but-unreadable
// secrets file (chmod 000) propagates the error. The CLI surfaces this
// as a fatal startup error because it signals an environment problem
// the developer needs to fix rather than the "haven't created secrets
// yet" non-fatal case.
func TestHostEnvComposition_SecretsUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits don't gate read on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode bits")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, ".env.dev.secrets")
	if err := os.WriteFile(path, []byte("STRIPE=x\n"), 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) }) // tempdir cleanup

	_, err := hostlaunch.LoadSecretsFile(path)
	if err == nil {
		t.Fatal("want error reading mode-000 file, got nil")
	}
	if os.IsNotExist(err) {
		t.Errorf("got NotExist; want permission error: %v", err)
	}
}

// TestLoadProjectConfigEnv_UnknownEnvReturnsEmpty: missing env name
// yields an empty map (not an error) so the caller can pass the result
// straight to LayerHostEnv without guarding.
func TestLoadProjectConfigEnv_UnknownEnvReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `name: testproj
module_path: github.com/example/testproj
version: "0.1.0"
binary: shared
`
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	markServiceProject(t, dir)
	t.Chdir(dir)

	cfg, err := loadProjectConfig()
	if err != nil {
		t.Fatalf("loadProjectConfig: %v", err)
	}

	got := loadProjectConfigEnv(cfg, "nonexistent")
	if got == nil {
		t.Error("got nil, want empty map (callers shouldn't need to nil-check)")
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty map", got)
	}
}

// TestHostEnvComposition_ProjectConfigUnderSecretsFile pins the
// precedence chain `forge env up` host-mode applies:
//
//	os.Environ() ⊕ forge.yaml config ⊕ .env.<env> ⊕ KCL env_vars
//
// (base wins; among extras, later overrides earlier on key conflict.)
// This is the cp-forge friction-log regression: forge.yaml config must
// be visible to host-mode services, and `.env.<env>` developer-local
// overrides must win over committed forge.yaml values.
func TestHostEnvComposition_ProjectConfigUnderSecretsFile(t *testing.T) {
	// forge.yaml config (lowest extra layer).
	projectConfig := map[string]string{
		"ENVIRONMENT": "development", // pass-through (no conflict)
		"LOG_LEVEL":   "info",        // overridden by .env.<env>
		"LOG_FORMAT":  "text",        // overridden by KCL env_vars
	}
	// .env.<env> secrets (middle extra layer).
	secrets := map[string]string{
		"LOG_LEVEL":  "debug",      // wins over forge.yaml; overridden by KCL
		"STRIPE_KEY": "sk_test_xx", // pass-through (no conflict)
	}
	// KCL env_vars (top extra layer).
	envVars := map[string]string{
		"LOG_FORMAT": "json", // wins over forge.yaml
	}
	base := []string{"PATH=/usr/bin"}

	final := hostlaunch.LayerHostEnv(base, projectConfig, secrets, envVars)

	wants := map[string]string{
		"PATH":        "/usr/bin",    // base wins
		"ENVIRONMENT": "development", // forge.yaml passes through
		"LOG_LEVEL":   "debug",       // .env.<env> wins over forge.yaml
		"LOG_FORMAT":  "json",        // KCL wins over forge.yaml
		"STRIPE_KEY":  "sk_test_xx",  // .env.<env> passes through
	}
	for k, v := range wants {
		if !envSliceContains(final, k+"="+v) {
			t.Errorf("want %s=%s; final=%v", k, v, final)
		}
	}
	// Verify no overridden value leaked through.
	leaks := []string{"LOG_LEVEL=info", "LOG_FORMAT=text"}
	for _, leak := range leaks {
		if envSliceContains(final, leak) {
			t.Errorf("value leaked: %q in final=%v", leak, final)
		}
	}
}

// TestHostEnvComposition_DotEnvWinsOverForgeYAML is the smallest possible
// statement of the load-bearing precedence rule: when forge.yaml config
// and `.env.<env>` declare the same key, `.env.<env>` wins. forge.yaml
// is committed and non-secret; `.env.<env>` is gitignored and carries
// developer-local overrides — the dev-local layer has to win or the
// override is invisible.
func TestHostEnvComposition_DotEnvWinsOverForgeYAML(t *testing.T) {
	projectConfig := map[string]string{"LOG_LEVEL": "info"} // committed
	secrets := map[string]string{"LOG_LEVEL": "trace"}      // dev-local override
	final := hostlaunch.LayerHostEnv([]string{"PATH=/usr/bin"}, projectConfig, secrets, nil)

	if !envSliceContains(final, "LOG_LEVEL=trace") {
		t.Errorf("LOG_LEVEL=trace missing from final env: %v", final)
	}
	if envSliceContains(final, "LOG_LEVEL=info") {
		t.Errorf("forge.yaml value leaked through .env override: %v", final)
	}
}

// envSliceContains is a tiny test helper.
func envSliceContains(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}
