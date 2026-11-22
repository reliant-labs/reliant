package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGenerateMigrateEmbedsOnlyWithSQL pins the one thing this emitter still
// derives: `//go:embed migrations/*.sql` does not compile against an empty
// match, so db/embed.go may exist only once the project has written SQL.
func TestGenerateMigrateEmbedsOnlyWithSQL(t *testing.T) {
	for _, tc := range []struct {
		name          string
		hasMigrations bool
		wantEmbed     bool
	}{
		{"no SQL yet", false, false},
		{"has SQL", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := GenerateMigrate(dir, "example.com/proj", tc.hasMigrations, nil); err != nil {
				t.Fatalf("GenerateMigrate: %v", err)
			}
			_, err := os.Stat(filepath.Join(dir, "db", "embed_gen.go"))
			if got := err == nil; got != tc.wantEmbed {
				t.Errorf("db/embed.go present = %v, want %v", got, tc.wantEmbed)
			}
		})
	}
}
