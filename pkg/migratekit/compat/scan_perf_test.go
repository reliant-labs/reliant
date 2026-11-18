package compat

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

// The prefilter (directiveLines) must hand the regexes every line they could
// match, with the line number bufio.ScanLines would have given it: leading
// tabs and spaces, CRLF endings, any case, a blank line before, and a file
// that does not end in a newline.
func TestDirectiveLines_KeepsEveryCandidateAndItsLineNumber(t *testing.T) {
	sql := "CREATE TABLE a (id int);\r\n" +
		"\r\n" +
		"\t  -- FORGE:Backward-Compatible — adds a table\r\n" +
		"SELECT '-- forge:breaks-rollback not a comment';\n" +
		"/* -- forge:breaks-rollback inside a block comment */\n" +
		"  --forge:breaks-rollback the old reader is gone\n" +
		"-- forge:adopts goose 1 2"
	compatible, breaks := Declarations(sql)
	if len(compatible) != 1 || compatible[0].Line != 3 || compatible[0].Reason != "adds a table" {
		t.Errorf("compatible = %+v, want line 3 reason %q", compatible, "adds a table")
	}
	if len(breaks) != 1 || breaks[0].Line != 6 || breaks[0].Reason != "the old reader is gone" {
		t.Errorf("breaks = %+v, want line 6", breaks)
	}
	a, err := ParseAdoption(sql)
	if err != nil || a == nil || a.Line != 7 || fmt.Sprint(a.Versions) != "[1 2]" {
		t.Errorf("ParseAdoption = %+v, %v; want line 7, versions [1 2]", a, err)
	}
	// A refusal still names its line.
	if _, err := ParseAdoption("SELECT 1;\n\n-- forge:adopt goose 1\n"); err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Errorf("ParseAdoption error = %v, want one naming line 3", err)
	}
}

// Scan runs on every migrator start over every embedded migration, and an
// integration test that migrates a fresh database pays it per test. It must
// stay proportional to the directive lines, not to the SQL around them.
// control-plane's 170 migrations (12k lines) took 240ms a scan, 5s under
// -race, before the prefilter.
func BenchmarkScan(b *testing.B) {
	fsys := fstest.MapFS{}
	ddl := strings.Repeat("ALTER TABLE t ADD COLUMN c_x TEXT NOT NULL DEFAULT ''; -- a comment\n", 70)
	for i := 1; i <= 170; i++ {
		fsys[fmt.Sprintf("%05d_m.up.sql", i)] = &fstest.MapFile{Data: []byte("-- forge:backward-compatible — expand\n" + ddl)}
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := Scan(fsys, "."); err != nil {
			b.Fatal(err)
		}
	}
}
