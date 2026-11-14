package forgeconv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// TestLintFrontendEnvFiles_FlagsForgeOwnedVars exercises the forge-owned
// dotenv rule: a committed .env* that assigns a forge-owned frontend
// variable (mock / api_url / otel / environment across the three framework
// prefixes) is flagged; commented lines, non-forge vars, and example files
// are left alone.
func TestLintFrontendEnvFiles_FlagsForgeOwnedVars(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	feDir := filepath.Join(root, "frontends", "web")
	if err := os.MkdirAll(feDir, 0o755); err != nil {
		t.Fatal(err)
	}

	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(feDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Active dotenv files with a mix of forge-owned and benign lines.
	write(".env.local", "NEXT_PUBLIC_MOCK_API=true\n# NEXT_PUBLIC_API_URL=commented\nNEXT_PUBLIC_SUPABASE_URL=https://x\n")
	write(".env.production", "VITE_API_URL=https://api.example.com\nexport EXPO_PUBLIC_OTEL_ENDPOINT=https://otel\n")
	// Example file — documentation, must be skipped even though it sets a
	// forge-owned var.
	write(".env.local.example", "NEXT_PUBLIC_MOCK_API=true\n")

	res := LintFrontendEnvFiles(root, []string{feDir}, finding.SeverityWarning)

	// Expect exactly three findings: MOCK_API (.env.local), API_URL and
	// OTEL_ENDPOINT (.env.production). The commented api_url, the Supabase
	// var, and the .example file contribute nothing.
	if len(res.Findings) != 3 {
		t.Fatalf("expected 3 findings, got %d: %+v", len(res.Findings), res.Findings)
	}
	for _, f := range res.Findings {
		if f.Rule != "forgeconv-frontend-env-forge-owned" {
			t.Errorf("unexpected rule %q", f.Rule)
		}
		if f.Severity != finding.SeverityWarning {
			t.Errorf("expected warning severity, got %v", f.Severity)
		}
		if filepath.IsAbs(f.File) {
			t.Errorf("File should be root-relative, got %q", f.File)
		}
	}
}

// TestLintFrontendEnvFiles_SeverityIsCallerControlled confirms the SAME
// analyzer emits at whatever severity the caller passes — the dev/build
// split (WARN in `forge lint`, ERROR on the deploy path) rides on this.
func TestLintFrontendEnvFiles_SeverityIsCallerControlled(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	feDir := filepath.Join(root, "frontends", "web")
	if err := os.MkdirAll(feDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(feDir, ".env"), []byte("NEXT_PUBLIC_MOCK_API=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := LintFrontendEnvFiles(root, []string{feDir}, finding.SeverityError)
	if len(res.Findings) != 1 || res.Findings[0].Severity != finding.SeverityError {
		t.Fatalf("expected 1 error finding, got %+v", res.Findings)
	}
	if !res.HasErrors() {
		t.Error("HasErrors should be true at error severity")
	}
}

// TestLintFrontendEnvFiles_CleanFrontend confirms a frontend with no
// forge-owned dotenv assignments (only benign vars / no .env files)
// produces no findings.
func TestLintFrontendEnvFiles_CleanFrontend(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	feDir := filepath.Join(root, "frontends", "web")
	if err := os.MkdirAll(feDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(feDir, ".env.local"), []byte("NEXT_PUBLIC_SUPABASE_ANON_KEY=abc\nSOME_OTHER=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := LintFrontendEnvFiles(root, []string{feDir}, finding.SeverityWarning)
	if len(res.Findings) != 0 {
		t.Fatalf("expected no findings for a clean frontend, got %+v", res.Findings)
	}
}
