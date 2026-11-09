package migrationlint

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

// RuleNoDownMigration is the rule ID for a down migration: a `*.down.sql`
// file, or a goose `-- +goose Down` section with SQL in it.
//
// Forge's policy is roll forward, never roll back. A down migration claims
// to undo a release and cannot: by the time anyone would run it, the release
// has written rows in the new shape, other services have read them, and jobs
// have acted on them. The script was written before any of that happened, it
// is untested in the state it would run in, and the moment it is needed is
// mid-incident. So forge writes none, runs none, and flags any that appear.
const RuleNoDownMigration = "no-down-migration"

// NoDownMigrationRemediation is the fix text for a no-down-migration finding.
const NoDownMigrationRemediation = `delete the down migration — forge never runs one. Recover from a bad migration with a new forward migration that repairs the schema from the state it is actually in, and write migrations expand-then-contract (add, backfill, switch readers, drop in a later release) so the previous release keeps working against the new schema`

// gooseDownRe matches the goose Down marker line.
var gooseDownRe = regexp.MustCompile(`^\s*--\s*\+goose\s+Down\b`)

// gooseMarkerRe matches any goose directive line (StatementBegin/End, Up,
// Down, NO TRANSACTION) — directives are not SQL, so a Down section holding
// only directives is empty.
var gooseMarkerRe = regexp.MustCompile(`^\s*--\s*\+goose\b`)

// lintDownMigrations reports every down migration among files, each as an
// error. There is no grandfathering: an existing down file is deleted, never
// tolerated, because forge never runs it and deleting it is always safe.
//
// files are the non-`.up.sql` SQL files of a migrations directory. A
// `*.down.sql` is a down migration by its name, whatever it holds — an empty
// one still tells golang-migrate there is a way back. A goose one-file
// migration is one only if its Down section carries SQL: an absent or empty
// Down is exactly what the policy asks for.
func lintDownMigrations(files []string) ([]Finding, error) {
	var findings []Finding
	for _, file := range files {
		line, isDown, err := downMigrationLine(file)
		if err != nil {
			return nil, err
		}
		if !isDown {
			continue
		}
		findings = append(findings, Finding{
			File:     file,
			Line:     line,
			Rule:     RuleNoDownMigration,
			Severity: SeverityError,
			Message:  "down migration: forge rolls forward only and never runs down SQL — delete it and recover from a bad migration with a new forward migration",
		})
	}
	return findings, nil
}

// downMigrationLine reports whether file is a down migration and the line to
// point at: line 1 of a `*.down.sql`, or the goose Down marker's line.
func downMigrationLine(file string) (int, bool, error) {
	if strings.HasSuffix(file, ".down.sql") {
		return 1, true, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lineNo, downLine := 0, 0
	for scanner.Scan() {
		lineNo++
		text := scanner.Text()
		if downLine == 0 {
			if gooseDownRe.MatchString(text) {
				downLine = lineNo
			}
			continue
		}
		if gooseMarkerRe.MatchString(text) {
			continue
		}
		if strings.TrimSpace(stripSQLComments(text)) != "" {
			return downLine, true, nil
		}
	}
	return 0, false, scanner.Err()
}
