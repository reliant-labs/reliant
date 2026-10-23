package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGrantsProject lays down a project whose only schema is migrations, so
// the lane has nothing proto-side to read.
func writeGrantsProject(t *testing.T, migrations map[string]string) string {
	t.Helper()
	root := t.TempDir()
	migDir := filepath.Join(root, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, sql := range migrations {
		if err := os.WriteFile(filepath.Join(migDir, name), []byte(sql), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const grantsBirthSQL = `CREATE TABLE git_credentials (
    id            TEXT PRIMARY KEY,
    provider      TEXT NOT NULL,
    access_token  TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    token_hash    TEXT NOT NULL,
    input_tokens  BIGINT NOT NULL DEFAULT 0
);
`

func grantsReport(t *testing.T, root string) dbGrantsReport {
	t.Helper()
	if testing.Short() {
		t.Skip("applies migrations to a shadow postgres; skipped under -short")
	}
	rep, err := collectDBGrantsFindings(root, "db/migrations")
	if err != nil {
		t.Fatalf("collectDBGrantsFindings: %v", err)
	}
	if rep.Unverified != "" {
		t.Fatalf("lane did not run: %s", rep.Unverified)
	}
	return rep
}

func names(rep dbGrantsReport) string {
	var out []string
	for _, f := range rep.Findings {
		out = append(out, f.qualified())
	}
	return strings.Join(out, ",")
}

// Every undecided credential-shaped column is named, with the file and line of
// its definition and both ways to decide. The numeric counter is not one.
func TestDBGrants_ReportsUndecidedCredentialColumns(t *testing.T) {
	root := writeGrantsProject(t, map[string]string{"0001_birth.up.sql": grantsBirthSQL})
	rep := grantsReport(t, root)
	if got, want := names(rep), "git_credentials.access_token,git_credentials.refresh_token,git_credentials.token_hash"; got != want {
		t.Fatalf("findings = %s, want %s", got, want)
	}
	f := rep.Findings[0]
	if f.File != "db/migrations/0001_birth.up.sql" || f.Line != 4 {
		t.Errorf("located at %s:%d, want db/migrations/0001_birth.up.sql:4", f.File, f.Line)
	}
	for _, want := range []string{"forge:secret", "forge:readable", "COMMENT ON COLUMN git_credentials.access_token"} {
		if !strings.Contains(f.fixHint(), want) {
			t.Errorf("fix hint lacks %q: %s", want, f.fixHint())
		}
	}

}

// A decision, in either spelling, clears the finding; an undecided neighbour
// stays reported.
func TestDBGrants_DecisionsClearTheFinding(t *testing.T) {
	root := writeGrantsProject(t, map[string]string{
		"0001_birth.up.sql": grantsBirthSQL,
		"0002_decide.up.sql": `
COMMENT ON COLUMN git_credentials.access_token IS 'forge:secret';
COMMENT ON COLUMN git_credentials.token_hash IS 'forge:readable — content hash of the clone URL';
`,
	})
	if got, want := names(grantsReport(t, root)), "git_credentials.refresh_token"; got != want {
		t.Errorf("findings = %s, want only %s", got, want)
	}
}

// The comment already on a column survives the fix: COMMENT ON replaces it, so
// the hint carries it forward.
func TestDBGrants_FixHintCarriesTheExistingComment(t *testing.T) {
	root := writeGrantsProject(t, map[string]string{
		"0001_birth.up.sql": grantsBirthSQL,
		"0002_note.up.sql":  `COMMENT ON COLUMN git_credentials.refresh_token IS 'forge:immutable';`,
	})
	rep := grantsReport(t, root)
	for _, f := range rep.Findings {
		if f.Column == "refresh_token" && !strings.Contains(f.fixHint(), "forge:immutable") {
			t.Errorf("hint drops the existing comment: %s", f.fixHint())
		}
	}
}

func TestDBGrants_CleanProjectAndNoMigrations(t *testing.T) {
	clean := writeGrantsProject(t, map[string]string{"0001_notes.up.sql": "CREATE TABLE notes (id TEXT PRIMARY KEY, body TEXT NOT NULL);"})
	if rep := grantsReport(t, clean); len(rep.Findings) != 0 {
		t.Errorf("clean schema reported: %+v", rep.Findings)
	}
	rep, err := collectDBGrantsFindings(t.TempDir(), "db/migrations")
	if err != nil || len(rep.Findings) != 0 || rep.Unverified != "" {
		t.Errorf("no migrations: (%+v, %v), want nothing", rep, err)
	}
}

// The JSON lane gates: an undecided column is an error, and a lane that could
// not run is an error too, never silence.
func TestDBGrants_JSONSeverityIsError(t *testing.T) {
	root := writeGrantsProject(t, map[string]string{"0001_birth.up.sql": grantsBirthSQL})
	grantsReport(t, root) // skips under -short
	fs, err := collectDBGrantsJSON(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 3 {
		t.Fatalf("%d JSON findings, want 3: %+v", len(fs), fs)
	}
	for _, f := range fs {
		if f.Severity != lintSevError || f.Rule != dbGrantsRule || f.File == "" || f.Line == 0 || f.FixHint == "" {
			t.Errorf("finding not actionable: %+v", f)
		}
	}
}
