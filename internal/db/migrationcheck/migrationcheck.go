// Copyright (c) 2025 Reliant Labs

// Package migrationcheck is the one definition of the rules every Postgres
// migration file must satisfy: a goose-generated YYYYMMDDHHMMSS_<name>.sql
// filename, a real timestamp for every version after the hand-numbered era, no
// two files on one version, and no `-- +goose Down` section.
//
// Two callers enforce it. The internal/db tests run it over the embedded
// migrations — the ones that ship — and cmd/check-migrations runs it over the
// files on disk; that is what scripts/check-migrations.sh and the
// check-migrations CI workflow invoke. Writing the rules once, in Go, is what
// keeps those two from drifting, and lets the check run anywhere Go does. The
// shell script it replaces needed bash 4 (associative arrays), so it could not
// run on macOS's bash 3.2 at all.
//
// The incidents behind each rule are recorded beside the tests that enforce
// them in internal/db: TestNewMigrationsCarryARealTimestamp,
// TestMigrationVersionsAreUnique and TestMigrationsHaveNoGooseDownSection.
package migrationcheck

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Rule names the rule a Problem breaks.
type Rule string

const (
	// RuleNoMigrations: the directory holds no *.sql files, so nothing was
	// checked. Almost always a wrong path rather than a real empty set.
	RuleNoMigrations Rule = "no-migrations"
	// RuleFilename: the name is not YYYYMMDDHHMMSS_<name>.sql.
	RuleFilename Rule = "filename"
	// RuleTimestamp: the version is not a real timestamp, or carries a
	// hand-picked time of day (HHMM or MMSS of 0000).
	RuleTimestamp Rule = "timestamp"
	// RuleUniqueVersion: two or more files claim one version.
	RuleUniqueVersion Rule = "unique-version"
	// RuleNoGooseDown: the file has a `-- +goose Down` section.
	RuleNoGooseDown Rule = "no-goose-down"
)

// LastHandNumberedVersion is the newest version that predates the real-timestamp
// rule. Versions up to it carry hand-picked times of day and are left alone:
// renaming an applied migration makes goose run it again.
const LastHandNumberedVersion int64 = 20260926000001

// createHint is how a migration should be created, quoted in every message
// that asks for a new name.
const createHint = "`goose -dir internal/db/migrations/postgres create <name> sql`"

var (
	// migrationFilename is the shape `goose create` produces.
	migrationFilename = regexp.MustCompile(`^(\d{14})_.+\.sql$`)
	// gooseDownMarker matches the line goose rolls a migration back from. Any
	// Down marker fails, empty or not, so a template or copy-paste cannot
	// reintroduce one.
	gooseDownMarker = regexp.MustCompile(`(?m)^\s*--\s*\+goose\s+Down\b`)
)

// Problem is one rule violation.
type Problem struct {
	// File is the migration's base name; empty for a problem that is not
	// about one file (no migrations at all, or several sharing a version).
	File string
	Rule Rule
	Msg  string
}

// String renders the problem as "[rule] file: message".
func (p Problem) String() string {
	if p.File == "" {
		return fmt.Sprintf("[%s] %s", p.Rule, p.Msg)
	}
	return fmt.Sprintf("[%s] %s: %s", p.Rule, p.File, p.Msg)
}

// Report is the result of checking one migration directory.
type Report struct {
	// Files is how many *.sql migrations were checked.
	Files int
	// Problems is every violation, ordered by file name.
	Problems []Problem
}

// OK reports whether the migrations passed every rule.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Check validates every *.sql migration directly under dir in fsys. The error
// is reserved for failing to read the migrations at all; rule violations are
// returned in the Report.
func Check(fsys fs.FS, dir string) (Report, error) {
	if info, err := fs.Stat(fsys, dir); err != nil {
		return Report{}, fmt.Errorf("migration directory %s: %w", dir, err)
	} else if !info.IsDir() {
		return Report{}, fmt.Errorf("migration directory %s: not a directory", dir)
	}
	files, err := fs.Glob(fsys, path.Join(dir, "*.sql"))
	if err != nil {
		return Report{}, fmt.Errorf("list migrations in %s: %w", dir, err)
	}
	sort.Strings(files)

	report := Report{Files: len(files)}
	if len(files) == 0 {
		report.Problems = append(report.Problems, Problem{
			Rule: RuleNoMigrations,
			Msg:  fmt.Sprintf("no *.sql migrations found in %s", dir),
		})
		return report, nil
	}

	byVersion := make(map[int64][]string, len(files))
	for _, file := range files {
		name := path.Base(file)
		body, err := fs.ReadFile(fsys, file)
		if err != nil {
			return Report{}, fmt.Errorf("read %s: %w", file, err)
		}
		if gooseDownMarker.Match(body) {
			report.Problems = append(report.Problems, Problem{
				File: name,
				Rule: RuleNoGooseDown,
				Msg: "has a `-- +goose Down` section; migrations are up-only — " +
					"roll forward with a new migration instead",
			})
		}

		m := migrationFilename.FindStringSubmatch(name)
		if m == nil {
			report.Problems = append(report.Problems, Problem{
				File: name,
				Rule: RuleFilename,
				Msg:  "want a YYYYMMDDHHMMSS_<name>.sql filename — create migrations with " + createHint,
			})
			continue
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			// Fourteen digits always fit in an int64; this is unreachable.
			return Report{}, fmt.Errorf("parse version of %s: %w", name, err)
		}
		byVersion[version] = append(byVersion[version], name)
		if msg := timestampProblem(m[1], version); msg != "" {
			report.Problems = append(report.Problems, Problem{File: name, Rule: RuleTimestamp, Msg: msg})
		}
	}

	// No grandfather clause here: a duplicate is unsafe at every version,
	// including the hand-numbered ones. Reported once per version, naming
	// every file: which of them is the newer one is not in the filenames.
	versions := make([]int64, 0, len(byVersion))
	for version := range byVersion {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	for _, version := range versions {
		names := byVersion[version]
		if len(names) < 2 {
			continue
		}
		report.Problems = append(report.Problems, Problem{
			Rule: RuleUniqueVersion,
			Msg: fmt.Sprintf("version %d is claimed by %d migrations (%s); goose records only the version, "+
				"so a database that applied one of these reports the others as already applied and "+
				"silently skips their SQL. Renumber the newer one with %s",
				version, len(names), strings.Join(names, ", "), createHint),
		})
	}

	sort.SliceStable(report.Problems, func(i, j int) bool {
		a, b := report.Problems[i], report.Problems[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Rule < b.Rule
	})
	return report, nil
}

// timestampProblem explains why a version is not a real goose timestamp, or
// returns "" when it is (or predates the rule).
func timestampProblem(digits string, version int64) string {
	if version <= LastHandNumberedVersion {
		return ""
	}
	if _, err := time.Parse("20060102150405", digits); err != nil {
		return fmt.Sprintf("version %s is not a valid YYYYMMDDHHMMSS timestamp: %v", digits, err)
	}
	hhmm, mmss := digits[8:12], digits[10:14]
	if hhmm == "0000" || mmss == "0000" {
		return fmt.Sprintf("version %s has a hand-picked time of day; two branches doing the same on the same day "+
			"claim the same version, and goose then treats one's file as the other's. Create it with %s "+
			"(or rename it to the current UTC time: `date -u +%%Y%%m%%d%%H%%M%%S`)", digits, createHint)
	}
	return ""
}
