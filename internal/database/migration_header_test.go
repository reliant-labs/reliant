package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A new migration's header points at the schema instead of copying it. It
// used to copy the reconstructed schema, the whole previous migration and
// every filename: 4,658 lines on reliant, whose previous migration was the
// baseline it had just squashed, above three lines of SQL.
func TestCreateMigration_HeaderIsShortWhateverTheHistory(t *testing.T) {
	dir := t.TempDir()
	var baseline strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&baseline, "CREATE TABLE t%d (id BIGINT PRIMARY KEY, name TEXT NOT NULL, created_at TIMESTAMPTZ);\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "00001_baseline.up.sql"), []byte(baseline.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 60; i++ {
		name := fmt.Sprintf("%05d_step_%d.up.sql", i, i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := CreateMigration(context.Background(), "add_x", dir, nil); err != nil {
		t.Fatal(err)
	}
	body := readNewest(t, dir, "add_x")

	if n := strings.Count(body, "\n"); n > 15 {
		t.Errorf("the default header is %d lines:\n%s", n, body)
	}
	for _, want := range []string{
		"-- Migration: add_x",
		"60 migration(s) in this directory",
		"00060_step_60.up.sql",
		"forge project shapes --kind table",
		"forge db introspect",
		"--context",
		"Write your migration SQL below",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("header is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "TABLE t0") || strings.Contains(body, "CREATE TABLE") {
		t.Errorf("the header copied the schema:\n%s", body)
	}
	if strings.Contains(body, dir) {
		t.Errorf("the committed header names this machine's path %s:\n%s", dir, body)
	}

	// --context still writes the full block.
	if err := CreateMigration(context.Background(), "add_y", dir, &MigrationOptions{Context: true}); err != nil {
		t.Fatal(err)
	}
	if full := readNewest(t, dir, "add_y"); !strings.Contains(full, "CURRENT SCHEMA") || !strings.Contains(full, "TABLE t399") {
		t.Errorf("--context did not write the schema")
	}
}

func readNewest(t *testing.T, dir, name string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "_"+name+".up.sql") {
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
	}
	t.Fatalf("no %s migration in %s", name, dir)
	return ""
}

// The compatibility declaration (--backward-compatible / --breaks-rollback,
// or the required-declaration choices) sits above the short header, and the
// file stays short.
func TestCreateMigration_CompatDeclarationWithTheShortHeader(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte("CREATE TABLE plans (id INT);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		opts  MigrationOptions
		first string
	}{
		"compatible": {MigrationOptions{BackwardCompatible: "additive"}, "-- forge:backward-compatible — additive"},
		"breaks":     {MigrationOptions{BreaksRollback: "drops plans.y"}, "-- forge:breaks-rollback drops plans.y"},
		"required":   {MigrationOptions{RequireCompatDeclaration: true}, "-- This project requires every migration"},
	} {
		t.Run(name, func(t *testing.T) {
			opts := tc.opts
			if err := CreateMigration(context.Background(), "m_"+name, dir, &opts); err != nil {
				t.Fatal(err)
			}
			body := readNewest(t, dir, "m_"+name)
			if !strings.HasPrefix(body, tc.first) {
				t.Errorf("the declaration is not the first line:\n%s", body)
			}
			if !strings.Contains(body, "forge project shapes --kind table") || strings.Contains(body, "CURRENT SCHEMA") {
				t.Errorf("want the short header under the declaration:\n%s", body)
			}
			if n := strings.Count(body, "\n"); n > 20 {
				t.Errorf("%d lines:\n%s", n, body)
			}
		})
	}
}
