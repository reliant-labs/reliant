package migrationlint

// WHICH RULES APPLY UNDER WHICH MIGRATION TOOL.
//
// A project may keep its own migration tool (forge.yaml
// `database.migrations.tool`: goose, atlas, flyway, dbmate, golang-migrate or
// custom). The SQL-text rules — a destructive statement, an unsafe NOT NULL, a
// volatile default — read SQL and hold for any tool's .sql files. Others
// encode facts of forge's migrator: its file naming and version order, its
// forward-only policy, its one-transaction-per-file execution, its baseline
// and adoption directives. Under another tool those either do not hold or
// mean something else, so the rule is not run, and the result says so — a
// rule that silently checked nothing would read as a pass.
//
// Every rule declares its scope here; TestEveryRuleDeclaresItsTools fails a
// rule ID with no row.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
)

// Rule IDs of the SQL-text rules (migrationlint.go spells them inline).
const (
	RuleDestructiveChange         = "destructive-change"
	RuleUnsafeAddNotNullColumn    = "unsafe-add-not-null-column"
	RuleVolatileDefault           = "volatile-default"
	RuleSetNotNullWithoutBackfill = "set-not-null-without-backfill"
)

// ruleScope is the set of tools a rule runs under. nil tools means every
// tool; otherwise why says what the rule depends on, completing
// "skipped (tool: X): ...".
type ruleScope struct {
	tools []string
	why   string
}

// forgeOnly is the scope of a rule about forge's own migrator.
func forgeOnly(why string) ruleScope {
	return ruleScope{tools: []string{config.MigrationToolForge}, why: why}
}

// oneTransactionPerFile is the scope of the transaction-block rules: the tools
// that send a migration file to postgres as one transaction. goose, dbmate and
// atlas do unless the file opts out with its own directive, which
// transactionalUnder reads per file.
var oneTransactionPerFile = ruleScope{
	tools: []string{config.MigrationToolForge, config.MigrationToolGolangMigrate, config.MigrationToolGoose,
		config.MigrationToolDbmate, config.MigrationToolAtlas},
	why: "whether a file runs as one transaction is the tool's decision (flyway detects non-transactional statements itself; a custom command's is unknown)",
}

// ruleTools is every rule's scope.
var ruleTools = map[string]ruleScope{
	RuleDestructiveChange:         {},
	RuleUnsafeAddNotNullColumn:    {},
	RuleVolatileDefault:           {},
	RuleSetNotNullWithoutBackfill: {},

	RuleNonTransactionalStatement: oneTransactionPerFile,
	RuleEnumValueUsedBeforeCommit: oneTransactionPerFile,

	RuleNoDownMigration: forgeOnly("down migrations are part of the tool's model; forge's roll-forward policy governs only forge's migrator"),

	RuleDuplicateVersion:       forgeOnly("they read forge's file naming (<timestamp>_<name>.up.sql) and version order"),
	RuleNonTimestampVersion:    forgeOnly("they read forge's file naming (<timestamp>_<name>.up.sql) and version order"),
	RuleVersionBelowMergedHead: forgeOnly("they read forge's file naming (<timestamp>_<name>.up.sql) and version order"),

	RuleCompatConflict:   forgeOnly("the -- forge:backward-compatible / -- forge:breaks-rollback declaration is ordered against forge's versions and baseline"),
	RuleCompatUndeclared: forgeOnly("the -- forge:backward-compatible / -- forge:breaks-rollback declaration is ordered against forge's versions and baseline"),
	RuleCompatReason:     forgeOnly("the -- forge:backward-compatible / -- forge:breaks-rollback declaration is ordered against forge's versions and baseline"),

	RuleInvalidAdoption: forgeOnly("-- forge:adopts is how forge's migrator takes over another tool's history"),
}

// RuleAppliesTo reports whether rule runs under tool and, when it does not,
// why. An unknown rule applies everywhere.
func RuleAppliesTo(rule, tool string) (bool, string) {
	if tool == "" {
		tool = config.MigrationToolForge
	}
	s, ok := ruleTools[rule]
	if !ok || s.tools == nil {
		return true, ""
	}
	for _, t := range s.tools {
		if t == tool {
			return true, ""
		}
	}
	return false, s.why
}

// RuleSkip is a rule that did not run, and why.
type RuleSkip struct {
	Rules  []string
	Tool   string
	Reason string
}

// String renders the skip as the report line's text.
func (s RuleSkip) String() string {
	return fmt.Sprintf("%s skipped (tool: %s): %s", strings.Join(s.Rules, ", "), s.Tool, s.Reason)
}

// ruleSkipsFor groups the rules that do not run under tool by reason, in a
// stable order.
func ruleSkipsFor(tool string) []RuleSkip {
	byReason := map[string][]string{}
	for rule := range ruleTools {
		if ok, why := RuleAppliesTo(rule, tool); !ok {
			byReason[why] = append(byReason[why], rule)
		}
	}
	out := make([]RuleSkip, 0, len(byReason))
	for why, rules := range byReason {
		sort.Strings(rules)
		out = append(out, RuleSkip{Rules: rules, Tool: tool, Reason: why})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rules[0] < out[j].Rules[0] })
	return out
}

// toolMigrationFiles are the files of dir that tool applies, the ones the
// SQL-text rules read. A non-SQL migration (a goose .go one, an atlas.sum) is
// not SQL to lint.
func toolMigrationFiles(dir, tool string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".sql") {
			return nil
		}
		switch tool {
		case config.MigrationToolGolangMigrate:
			if !strings.HasSuffix(name, ".up.sql") {
				return nil // the .down.sql twin is not applied going forward
			}
		case config.MigrationToolFlyway:
			// Versioned (V…__) and repeatable (R__…) scripts are applied; an
			// undo script (U…__) is the tool's rollback.
			if !(strings.HasPrefix(name, "V") || strings.HasPrefix(name, "R__")) {
				return nil
			}
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}

var (
	gooseUpRe    = regexp.MustCompile(`^\s*--\s*\+goose\s+Up\b`)
	dbmateUpRe   = regexp.MustCompile(`^\s*--\s*migrate:up\b`)
	dbmateDownRe = regexp.MustCompile(`^\s*--\s*migrate:down\b`)

	gooseNoTxRe  = regexp.MustCompile(`(?im)^\s*--\s*\+goose\s+NO\s+TRANSACTION\b`)
	dbmateNoTxRe = regexp.MustCompile(`(?im)^\s*--\s*migrate:up\b[^\n]*\btransaction:false\b`)
	atlasNoTxRe  = regexp.MustCompile(`(?im)^\s*--\s*atlas:txmode\s+none\b`)
)

// upSection is the part of content tool applies going forward: goose's
// `-- +goose Up` section and dbmate's `-- migrate:up` one; the whole file for
// every other tool. Lines outside it are blanked rather than cut, so a
// finding's line number is the file's.
func upSection(content, tool string) string {
	var upRe, downRe *regexp.Regexp
	switch tool {
	case config.MigrationToolGoose:
		upRe, downRe = gooseUpRe, gooseDownRe
	case config.MigrationToolDbmate:
		upRe, downRe = dbmateUpRe, dbmateDownRe
	default:
		return content
	}
	if firstMatchLine(content, upRe) == "" {
		return content // no sections declared: the tool applies the file whole
	}
	var b strings.Builder
	in := false
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case upRe.MatchString(line):
			in = true
			line = ""
		case downRe.MatchString(line):
			in = false
		}
		if !in {
			line = ""
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func firstMatchLine(content string, re *regexp.Regexp) string {
	for _, line := range strings.Split(content, "\n") {
		if re.MatchString(line) {
			return line
		}
	}
	return ""
}

// transactionalUnder reports whether tool runs this file as one transaction,
// the premise of the transaction-block rules.
func transactionalUnder(content, tool string) bool {
	switch tool {
	case config.MigrationToolGoose:
		return !gooseNoTxRe.MatchString(content)
	case config.MigrationToolDbmate:
		return !dbmateNoTxRe.MatchString(content)
	case config.MigrationToolAtlas:
		return !atlasNoTxRe.MatchString(content)
	}
	ok, _ := RuleAppliesTo(RuleNonTransactionalStatement, tool)
	return ok
}

// lintForeignToolDir is LintMigrationsDir for a directory another tool
// applies: the rules whose scope includes the tool, over the files it
// applies, and a RuleSkip for each that does not run.
func lintForeignToolDir(dir string, cfg RuleConfig) (Result, error) {
	files, err := toolMigrationFiles(dir, cfg.Tool)
	if err != nil {
		return Result{}, err
	}
	skips := ruleSkipsFor(cfg.Tool)
	if len(files) == 0 {
		return Result{Dir: dir, Tool: cfg.Tool, RuleSkips: skips,
			Skipped: fmt.Sprintf("%s holds no SQL migration %s applies (tool: %s)", dir, cfg.Tool, cfg.Tool)}, nil
	}
	var findings []Finding
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return Result{}, err
		}
		up := upSection(string(data), cfg.Tool)
		findings = append(findings, lintStatements(file, up, cfg)...)
		if transactionalUnder(string(data), cfg.Tool) {
			findings = append(findings, lintTransactionBlock(file, up)...)
		}
	}
	return Result{Findings: findings, Dir: dir, Scanned: files, Tool: cfg.Tool, RuleSkips: skips}, nil
}
