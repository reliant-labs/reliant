package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `forge db migration new --closed-set` writes the fix the lint prints, as a
// fresh migration with its compatibility line; a CHECK already in step has
// nothing to write.
func TestNewClosedSetMigration(t *testing.T) {
	if testing.Short() {
		t.Skip("resolves the closed set by loading Go packages (go list); runs in task test")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	mig := filepath.Join(dir, "db", "migrations")
	if err := os.MkdirAll(mig, 0o755); err != nil {
		t.Fatal(err)
	}
	base := `CREATE TABLE grants (
    kind text,
    CONSTRAINT grants_kind CHECK (kind IN ('daemon', 'port'))
);
COMMENT ON CONSTRAINT grants_kind ON grants IS 'forge:closed-set accesstoken.AllResourceKinds';
`
	if err := os.WriteFile(filepath.Join(mig, "20260101000000_base.up.sql"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := newClosedSetMigration(context.Background(), "grants_kind", "grants_kind_closed_set", mig, "", false); err != nil {
		t.Fatal(err)
	}
	written, _ := filepath.Glob(filepath.Join(mig, "*_grants_kind_closed_set.up.sql"))
	if len(written) != 1 {
		t.Fatalf("want one new migration, got %v", written)
	}
	body, _ := os.ReadFile(written[0])
	for _, want := range []string{
		"-- forge:backward-compatible — widens grants_kind",
		"ADD CONSTRAINT grants_kind CHECK (kind IN ('daemon', 'port', 'connector', 'environment'));",
		"COMMENT ON CONSTRAINT grants_kind ON grants IS\n    'forge:closed-set accesstoken.AllResourceKinds';",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("migration lacks %q:\n%s", want, body)
		}
	}

	err := newClosedSetMigration(context.Background(), "grants_kind", "again", mig, "", false)
	if err == nil || !strings.Contains(err.Error(), "has no closed-set finding") {
		t.Fatalf("an in-step CHECK has nothing to write: %v", err)
	}
}
