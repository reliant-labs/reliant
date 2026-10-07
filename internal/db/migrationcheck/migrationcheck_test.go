// Copyright (c) 2025 Reliant Labs
package migrationcheck

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

const upOnly = "-- +goose Up\nSELECT 1;\n"

// migrations builds a migration directory "m" from name -> body.
func migrations(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{"m": {Mode: fs.ModeDir | 0o755}}
	for name, body := range files {
		fsys["m/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

// problems runs Check and returns "rule file" for each problem, failing the
// test on a read error.
func problems(t *testing.T, files map[string]string) []string {
	t.Helper()
	report, err := Check(migrations(files), "m")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.OK() != (len(report.Problems) == 0) {
		t.Fatalf("OK() = %v with %d problems", report.OK(), len(report.Problems))
	}
	got := make([]string, 0, len(report.Problems))
	for _, p := range report.Problems {
		got = append(got, string(p.Rule)+" "+p.File)
	}
	return got
}

func assertProblems(t *testing.T, files map[string]string, want ...string) {
	t.Helper()
	got := problems(t, files)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("problems:\n  got  %q\n  want %q", got, want)
	}
}

func TestCleanSetPasses(t *testing.T) {
	assertProblems(t, map[string]string{
		// Hand-numbered era: zeroed times are grandfathered.
		"20260211000002_init_schema.sql":          upOnly,
		"20260926000001_last_hand_numbered.sql":   upOnly,
		"20261006204431_scope_spawn_report.sql":   upOnly,
		"20261007091530_with_statement_block.sql": "-- +goose Up\n-- +goose StatementBegin\nSELECT 1;\n-- +goose StatementEnd\n",
		// Only *.sql files are migrations.
		"README.md": "not a migration",
	})
}

func TestReportCountsFiles(t *testing.T) {
	report, err := Check(migrations(map[string]string{
		"20261006204431_a.sql": upOnly,
		"20261006204432_b.sql": upOnly,
		"notes.txt":            "",
	}), "m")
	if err != nil {
		t.Fatal(err)
	}
	if report.Files != 2 || !report.OK() {
		t.Errorf("report = %+v, want 2 clean files", report)
	}
}

func TestEmptyDirectoryIsAProblem(t *testing.T) {
	assertProblems(t, map[string]string{}, "no-migrations ")
}

func TestMissingDirectoryIsAnError(t *testing.T) {
	if _, err := Check(fstest.MapFS{}, "nope"); err == nil {
		t.Fatal("Check of a missing directory returned no error")
	}
}

func TestFilenameShape(t *testing.T) {
	for _, name := range []string{
		"add_thing.sql",                // no version
		"20261006_short_version.sql",   // 8 digits
		"202610062044310_long.sql",     // 15 digits
		"20261006204431-dash.sql",      // wrong separator
		"20261006204431_.sql",          // no description
		"v20261006204431_prefixed.sql", // prefix
	} {
		t.Run(name, func(t *testing.T) {
			assertProblems(t, map[string]string{name: upOnly}, "filename "+name)
		})
	}
}

func TestTimestampRule(t *testing.T) {
	cases := map[string]bool{
		"20261007000000_midnight.sql":      true,  // HHMM 0000
		"20261007120000_noon.sql":          true,  // MMSS 0000
		"20261007000012_early.sql":         true,  // HHMM 0000
		"20261332123456_bad_month.sql":     true,  // not a date
		"20261007256060_bad_clock.sql":     true,  // not a time
		"20261007120001_one_second.sql":    false, // real
		"20261007000100_one_minute.sql":    false, // HHMM 0001, MMSS 0100: real
		"20260926000000_grandfathered.sql": false, // <= LastHandNumberedVersion
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			var want []string
			if bad {
				want = []string{"timestamp " + name}
			}
			assertProblems(t, map[string]string{name: upOnly}, want...)
		})
	}
}

func TestDuplicateVersions(t *testing.T) {
	// One problem per shared version, naming every file on it, in version
	// order — including the hand-numbered era, which has no grandfather
	// clause for duplicates.
	report, err := Check(migrations(map[string]string{
		"20261006204431_c.sql":                         upOnly,
		"20261006204431_a.sql":                         upOnly,
		"20261006204431_b.sql":                         upOnly,
		"20260923000000_temporal_payload_blobs.sql":    upOnly,
		"20260923000000_access_tokens_retire_pats.sql": upOnly,
		"20261006204432_alone.sql":                     upOnly,
	}), "m")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"version 20260923000000 is claimed by 2 migrations " +
			"(20260923000000_access_tokens_retire_pats.sql, 20260923000000_temporal_payload_blobs.sql)",
		"version 20261006204431 is claimed by 3 migrations " +
			"(20261006204431_a.sql, 20261006204431_b.sql, 20261006204431_c.sql)",
	}
	if len(report.Problems) != len(want) {
		t.Fatalf("problems = %v, want %d", report.Problems, len(want))
	}
	for i, p := range report.Problems {
		if p.Rule != RuleUniqueVersion || p.File != "" || !strings.HasPrefix(p.Msg, want[i]) {
			t.Errorf("problem %d = %+v, want a %s problem starting %q", i, p, RuleUniqueVersion, want[i])
		}
	}
}

func TestNoGooseDown(t *testing.T) {
	for name, body := range map[string]string{
		"20261007120001_plain.sql":     "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 2;\n",
		"20261007120002_empty.sql":     "-- +goose Up\nSELECT 1;\n\n-- +goose Down\n",
		"20261007120003_tight.sql":     "-- +goose Up\nSELECT 1;\n--+goose Down\n",
		"20261007120004_indented.sql":  "-- +goose Up\nSELECT 1;\n   --   +goose   Down\n",
		"20261007120005_down_only.sql": "-- +goose Down\nDROP TABLE x;\n",
	} {
		t.Run(name, func(t *testing.T) {
			assertProblems(t, map[string]string{name: body}, "no-goose-down "+name)
		})
	}
}

func TestGooseDownMentionedInProseIsFine(t *testing.T) {
	assertProblems(t, map[string]string{
		"20261007120001_prose.sql": "-- +goose Up\n-- We never write a +goose Down section; roll forward.\nSELECT 1;\n",
	})
}

func TestEveryProblemIsReported(t *testing.T) {
	// One bad file per rule, plus a file breaking two rules at once: nothing
	// short-circuits, so a contributor sees the whole list in one run.
	assertProblems(t, map[string]string{
		"bad_name.sql":              upOnly,
		"20261007000000_zeroed.sql": "-- +goose Up\n-- +goose Down\n",
		"20261006204431_a.sql":      upOnly,
		"20261006204431_b.sql":      upOnly,
	},
		"unique-version ",
		"no-goose-down 20261007000000_zeroed.sql",
		"timestamp 20261007000000_zeroed.sql",
		"filename bad_name.sql",
	)
}
