package database

import (
	"os"
	"path/filepath"
	"testing"
)

// forge report ee1f039a: after a batch rebase, prose inside the batch's files
// still named the old versions ("-- 20261118112732 validates it"). Comments
// mentioning a renamed version now name its new one; SQL is left alone, and
// a version inside a longer run of digits never matches.
func TestRewriteBatchReferencesRewritesCommentsOnly(t *testing.T) {
	dir := t.TempDir()
	add := filepath.Join(dir, "20261118151824_add_check.up.sql")
	val := filepath.Join(dir, "20261118151825_validate_check.up.sql")
	if err := os.WriteFile(add, []byte("-- forge:backward-compatible — 20261118112732 validates it\n"+
		"ALTER TABLE t ADD CONSTRAINT c CHECK (n IS NOT NULL) NOT VALID; -- 20261118103929 is this file\n"+
		"INSERT INTO notes VALUES ('20261118103929'); -- not 120261118103929 nor 202611181039290\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(val, []byte("-- 20261118103929_add_check added the CHECK this validates\nALTER TABLE t VALIDATE CONSTRAINT c;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	results := []RebaseResult{
		{OldPath: "x", NewPath: add, OldVersion: 20261118103929, NewVersion: 20261118151824},
		{OldPath: "y", NewPath: val, OldVersion: 20261118112732, NewVersion: 20261118151825},
	}
	if err := rewriteBatchReferences(results); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(add)
	want := "-- forge:backward-compatible — 20261118151825 validates it\n" +
		"ALTER TABLE t ADD CONSTRAINT c CHECK (n IS NOT NULL) NOT VALID; -- 20261118151824 is this file\n" +
		"INSERT INTO notes VALUES ('20261118103929'); -- not 120261118103929 nor 202611181039290\n"
	if string(got) != want {
		t.Errorf("add_check:\n%s\nwant:\n%s", got, want)
	}
	got, _ = os.ReadFile(val)
	if want := "-- 20261118151824_add_check added the CHECK this validates\nALTER TABLE t VALIDATE CONSTRAINT c;\n"; string(got) != want {
		t.Errorf("validate_check:\n%s\nwant:\n%s", got, want)
	}
}
