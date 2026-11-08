package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// TestAutoSeedFirstBootAppliesCustomOverlay: `forge env up dev`'s first-boot
// seed applied the synthesized dataset and stopped, so a project's own dev
// rows in db/seeds/custom/ (growth's first tenant) were missing until a
// manual `forge db seed apply`. The first boot applies both layers, and a
// later boot, seeing rows, touches neither.
func TestAutoSeedFirstBootAppliesCustomOverlay(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; runs in task test")
	}
	const migration = `
CREATE TABLE widgets (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
CREATE TABLE notes (
    id TEXT PRIMARY KEY,
    body TEXT NOT NULL
);
`
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(migration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	root := t.TempDir()
	migDir := filepath.Join(root, "db", "migrations")
	customDir := filepath.Join(root, "db", "seeds", "custom")
	for dir, files := range map[string]map[string]string{
		migDir: {"00001_init.up.sql": migration},
		customDir: {
			"001_mine.sql": "INSERT INTO notes (id, body) VALUES ('mine', 'from the overlay') ON CONFLICT (id) DO UPDATE SET body = EXCLUDED.body;\n",
			"example.sql":  "-- fully commented: skipped\n",
		},
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	ctx := context.Background()
	cfg := seedplan.DefaultConfig()
	cfg.Tables = []string{"widgets"} // the CRUD-entity scope; notes is hand-written schema

	autoSeedFirstBoot(ctx, db, migDir, "", cfg)

	var widgets int
	if err := db.QueryRow(`SELECT count(*) FROM widgets`).Scan(&widgets); err != nil {
		t.Fatal(err)
	}
	if widgets == 0 {
		t.Fatal("first boot synthesized no widgets")
	}
	var body string
	if err := db.QueryRow(`SELECT body FROM notes WHERE id = 'mine'`).Scan(&body); err != nil {
		t.Fatalf("first boot did not apply db/seeds/custom/001_mine.sql: %v", err)
	}

	// A later boot sees rows and leaves the developer's edits alone: the
	// overlay's upsert must not run again.
	if _, err := db.Exec(`UPDATE notes SET body = 'edited' WHERE id = 'mine'`); err != nil {
		t.Fatal(err)
	}
	autoSeedFirstBoot(ctx, db, migDir, "", cfg)
	if err := db.QueryRow(`SELECT body FROM notes WHERE id = 'mine'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "edited" {
		t.Errorf("a second boot re-applied the overlay over the developer's edit: body = %q", body)
	}
}
