// Package compat reads the contracts a migration declares about itself: that
// it is BACKWARD-COMPATIBLE, or deliberately BREAKS ROLLBACK (this file), and
// which foreign migration history a baseline ADOPTS (adopt.go).
//
// THE CONTRACT. A migration's .up.sql may carry the line
//
//	-- forge:backward-compatible
//
// which is its author's statement that the PREVIOUS release's code keeps
// working against the schema this migration leaves behind — the "expand"
// half of expand/contract. An ADD COLUMN that old code never reads is
// backward-compatible; a DROP COLUMN old code still SELECTs is not.
//
// WHY A DECLARATION AND NOT AN INFERENCE. Whether N-1 code survives a schema
// is a fact about that code, not about the SQL, and nothing short of running
// it can prove it. An inference from the SQL would be wrong in both
// directions: `DROP TABLE` of a table nothing reads is safe, and `ADD COLUMN
// ... NOT NULL` without a default breaks every old INSERT. So the author,
// who knows, states it once, in the file, in review — and forge enforces
// what follows from it: the migrator records the declared versions in the
// database as it applies them, so an OLDER binary that meets a schema ahead
// of it — the previous release's pods, still serving or rescheduled, during
// every rolling deploy — can prove the extra versions are safe rather than
// hoping (see migratekit.SchemaAheadError).
//
// WHY THIS IS ITS OWN PACKAGE. It is a comment parser every project's
// migrator links; keeping it free of golang-migrate lets any tool read the
// declaration without that dependency, so it imports nothing but the
// standard library.
package compat

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Directive is the exact line a migration carries to declare itself
// backward-compatible. Exported so remediation text, the linter and the
// scaffolded comment all quote the same spelling the parser matches.
const Directive = "-- forge:backward-compatible"

// directiveRe matches Directive as a whole SQL line comment, case-insensitive,
// tolerating indentation and the `--   forge:` spacing people actually type.
//
// The directive must END at whitespace, a colon or end of line — not at a
// `\b`. A word boundary sits between `e` and `-`, so `\b` would read
// `forge:backward-compatible-ish` as the declaration; a rationale may follow
// after a space or a colon (`-- forge:backward-compatible — additive column`).
//
// Only a LINE COMMENT counts. A mention inside a block comment or a string is
// prose about the directive, not the directive, and treating it as a
// declaration would let a migration that merely discusses compatibility claim
// it.
var directiveRe = regexp.MustCompile(`(?i)^\s*--\s*forge:backward-compatible(?:\s|:|$)`)

// BreaksRollbackDirective is the line a migration carries to declare the
// opposite: the PREVIOUS release does NOT keep working on the schema this
// migration leaves — a dropped or renamed column old code reads, a NOT NULL
// column old code does not write. It takes a reason on the same line:
//
//	-- forge:breaks-rollback the 2026-09 release stopped reading plans.legacy_tier
//
// The migrator treats it exactly as it treats a migration that declares
// nothing (an older binary refuses to serve over it). What it adds is that the
// author SAID so, in review, with why: a project that requires every migration
// to declare itself (`database.migration_safety.require_compat_declaration`)
// can tell a deliberate break from a forgotten declaration.
const BreaksRollbackDirective = "-- forge:breaks-rollback"

// breaksRollbackRe matches BreaksRollbackDirective by the same rules as
// directiveRe.
var breaksRollbackRe = regexp.MustCompile(`(?i)^\s*--\s*forge:breaks-rollback(?:\s|:|$)`)

// directiveTailRe captures what follows either directive on its line.
var directiveTailRe = regexp.MustCompile(`(?i)^\s*--\s*forge:(?:backward-compatible|breaks-rollback)(.*)$`)

// Declaration is one compatibility directive line in a migration.
type Declaration struct {
	// Line is the 1-based line it is on.
	Line int
	// Reason is the text after the directive, with the separator a reader
	// types (`—`, `-`, `:`) trimmed; "" when there is none. A reason that
	// starts with `<` is a template placeholder (`<why …>`), not a reason,
	// and reads as "".
	Reason string
}

// Declarations returns every backward-compatible and every breaks-rollback
// line in one migration's up SQL. A file is meant to carry exactly one of
// the two; the linter reports anything else.
func Declarations(upSQL string) (compatible, breaksRollback []Declaration) {
	_ = directiveLines(upSQL, func(line int, text string) error {
		var into *[]Declaration
		switch {
		case directiveRe.MatchString(text):
			into = &compatible
		case breaksRollbackRe.MatchString(text):
			into = &breaksRollback
		default:
			return nil
		}
		reason := ""
		if m := directiveTailRe.FindStringSubmatch(text); m != nil {
			reason = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(m[1]), "—–-:"))
		}
		if strings.HasPrefix(reason, "<") {
			reason = ""
		}
		*into = append(*into, Declaration{Line: line, Reason: reason})
		return nil
	})
	return compatible, breaksRollback
}

// directiveLines calls fn with every line of upSQL that could hold a forge
// directive, with its 1-based number: a SQL line comment (`--` after leading
// whitespace) that mentions `forge:` in any case. Every directive regex in
// this package requires both, so a line skipped here is one none of them
// could match. Lines are split as bufio.ScanLines splits them (a trailing \r
// dropped), with no length limit. It stops at the first error fn returns.
//
// WHY. The migrator scans every embedded migration on every start, and a
// migration is overwhelmingly DDL, not directives. Running the regexes on
// every line cost control-plane's 170 migrations (12k lines) 240ms per scan,
// and 5s under -race — paid by every integration test that migrates a fresh
// database, which pushed whole packages past their 30m budget. With the
// prefilter the regexes see the directive lines alone.
func directiveLines(upSQL string, fn func(line int, text string) error) error {
	for line := 1; upSQL != ""; line++ {
		text, rest, _ := strings.Cut(upSQL, "\n")
		upSQL = rest
		text = strings.TrimSuffix(text, "\r")
		// `\s` in the directive regexes is [\t\n\f\r ].
		t := strings.TrimLeft(text, " \t\f\r")
		if !strings.HasPrefix(t, "--") || !containsFold(t, "forge:") {
			continue
		}
		if err := fn(line, text); err != nil {
			return err
		}
	}
	return nil
}

// containsFold reports whether s contains the ASCII lower-case substr in any
// case, without allocating.
func containsFold(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

// upFileRe is golang-migrate's filename grammar, restricted to the up half:
// `<digits>_<name>.up.<ext>`. Stated here rather than imported so this
// package stays dependency-free; it is the same regex golang-migrate's
// source.Parse uses.
var upFileRe = regexp.MustCompile(`^([0-9]+)_(.*)\.up\.(.*)$`)

// Declared reports whether one migration's up SQL declares itself
// backward-compatible. A file that ALSO declares breaks-rollback contradicts
// itself, and is read the safe way: not compatible, so an older binary
// refuses to serve over it rather than trusting half of the claim.
func Declared(upSQL string) bool {
	compatible, breaks := Declarations(upSQL)
	return len(compatible) > 0 && len(breaks) == 0
}

// Migration is one up migration's version and what it declares.
type Migration struct {
	// Version is the numeric prefix — the value golang-migrate records in
	// schema_migrations.
	Version uint
	// Name is the filename, for messages.
	Name string
	// Descriptor is the filename between the version prefix and `.up.` —
	// `retire_plan_compute_free` in `00091_retire_plan_compute_free.up.sql`.
	// It is what identifies WHICH migration a version is: two branches that
	// both claim 91 differ here, while a change of zero-padding does not.
	Descriptor string
	// BackwardCompatible is whether the file carries Directive (and not
	// BreaksRollbackDirective as well; see Declared).
	BackwardCompatible bool
	// BreaksRollback is whether the file carries BreaksRollbackDirective.
	BreaksRollback bool
	// Adopts is the foreign history this migration replaces, from its
	// AdoptsDirective lines; nil when it declares none. See adopt.go.
	Adopts *Adoption
}

// Scan reads every up migration in dir of fsys, in version order.
//
// A directory holding no up migrations returns an empty slice, not an error:
// a project that has not written one yet is a normal state. An unreadable
// directory IS an error, because "no migrations" and "could not look" call
// for different fixes.
func Scan(fsys fs.FS, dir string) ([]Migration, error) {
	if dir == "" {
		dir = "."
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations (%s): %w", dir, err)
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := upFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, perr := strconv.ParseUint(m[1], 10, 64)
		if perr != nil {
			return nil, fmt.Errorf("migration %s: version %q is not a number: %w", e.Name(), m[1], perr)
		}
		raw, rerr := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if rerr != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), rerr)
		}
		upSQL := string(raw)
		adopts, aerr := ParseAdoption(upSQL)
		if aerr != nil {
			return nil, fmt.Errorf("migration %s: %w", e.Name(), aerr)
		}
		compatible, breaks := Declarations(upSQL)
		out = append(out, Migration{Version: uint(v), Name: e.Name(), Descriptor: m[2],
			// Declared's rule: a file that also declares breaks-rollback is
			// read the safe way, as not compatible.
			BackwardCompatible: len(compatible) > 0 && len(breaks) == 0, BreaksRollback: len(breaks) > 0, Adopts: adopts})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}
