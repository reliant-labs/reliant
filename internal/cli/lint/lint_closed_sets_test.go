package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// The lane reads the project's migrations against the registry compiled into
// this binary: a CHECK that lacks a member of accesstoken.AllScopes fails,
// naming the value and printing the fix with the command that writes it.
func TestClosedSetsLint(t *testing.T) {
	dir := t.TempDir()
	mig := filepath.Join(dir, "db", "migrations")
	if err := os.MkdirAll(mig, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(mig, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("0001_tokens.up.sql", `CREATE TABLE access_tokens (
    scopes text[] NOT NULL,
    CONSTRAINT access_tokens_scopes CHECK (scopes <@ ARRAY['deploy:read', 'deploy:write']::text[])
);
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS 'forge:closed-set accesstoken.AllScopes';
`)
	cfg := &config.ProjectConfig{}
	cfg.Database.MigrationsDir = mig

	fs, err := collectClosedSetsJSON(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 {
		t.Fatalf("want one finding, got %+v", fs)
	}
	f := fs[0]
	if f.File != filepath.Join("db", "migrations", "0001_tokens.up.sql") || f.Line != 3 || f.Rule != "forge-closed-set" || f.Severity != lintSevError {
		t.Errorf("finding = %+v", f)
	}
	for _, want := range []string{"'identity:manage'", "'cluster:manage'"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message does not name %s: %s", want, f.Message)
		}
	}
	if !strings.Contains(f.FixHint, "forge db migration new --closed-set access_tokens_scopes") ||
		!strings.Contains(f.FixHint, "ADD CONSTRAINT access_tokens_scopes CHECK (scopes <@ ARRAY['deploy:read', 'deploy:write', 'cluster:manage'") {
		t.Errorf("fix hint: %s", f.FixHint)
	}

}
