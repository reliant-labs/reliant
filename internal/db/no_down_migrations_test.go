// Copyright (c) 2025 Reliant Labs
package db

import (
	"io/fs"
	"regexp"
	"testing"
)

// goose runs whatever follows a `-- +goose Down` marker when asked to roll a
// migration back. We never roll back — recovery is a new migration that rolls
// forward from the state the database is actually in — so a Down section is
// SQL nobody will run on purpose and nobody has tested against real data.
// Fail on any Down marker, empty or not, so a template or copy-paste cannot
// reintroduce one.
var gooseDownMarker = regexp.MustCompile(`(?m)^\s*--\s*\+goose\s+Down\b`)

func TestMigrationsHaveNoGooseDownSection(t *testing.T) {
	files, err := fs.Glob(FS, "migrations/postgres/*.sql")
	if err != nil {
		t.Fatalf("glob embedded migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected embedded migrations, got none")
	}
	for _, name := range files {
		body, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if gooseDownMarker.Match(body) {
			t.Errorf("%s has a `-- +goose Down` section; migrations are up-only — roll forward with a new migration instead", name)
		}
	}
}
