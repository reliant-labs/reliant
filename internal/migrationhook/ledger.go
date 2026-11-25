package migrationhook

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/reliant-labs/forge/internal/config"
)

// Ledger is what a built-in tool's history table says about one database:
// the migrations it records as applied (and from which file content, where
// the tool records that), and the one it records as failed part-way. It is
// how forge tells "fully migrated" from "behind" without trusting the
// schema's shape, which a pending data-only migration, a changed repeatable
// or a migration that failed after its DDL leaves looking current.
type Ledger struct {
	// Table is the history table read.
	Table LedgerTable
	// Applied are the migrations recorded as applied: versions, and a
	// flyway repeatable as RepeatablePrefix + its script.
	Applied []string
	// Sums is the checksum the tool recorded for an applied migration,
	// keyed like Applied, where it records one (flyway's checksum, atlas's
	// hash). A repeatable's is the content it last ran successfully.
	Sums map[string]string
	// Through is a version every version at or below which counts as
	// applied: golang-migrate's current version (it records only that),
	// or a flyway/atlas baseline. Empty when there is none. It never
	// covers a repeatable.
	Through string
	// Dirty is the migration recorded as failed part-way, "" when none:
	// golang-migrate's dirty flag, a flyway migration (versioned or
	// repeatable) whose last run did not succeed, an atlas revision with an
	// error or fewer statements applied than it has.
	Dirty string
}

// RepeatablePrefix marks a flyway repeatable migration (R__*.sql) in
// Ledger.Applied: it has no version, only a script, and it is pending
// whenever its content changes.
const RepeatablePrefix = "repeatable "

// ledgerQuerier is the one database method ReadLedger needs.
type ledgerQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// HasLedger reports whether forge knows how to read h's tool's history
// (ReadLedger): every built-in tool, not custom.
func (h Hook) HasLedger() bool {
	return len(h.LedgerTables()) > 0
}

// ledgerTable is the history table h's tool writes: the one its argv names,
// else the tool's default (the last of LedgerTables).
func (h Hook) ledgerTable() (LedgerTable, bool) {
	tables := h.LedgerTables()
	if len(tables) == 0 {
		return LedgerTable{}, false
	}
	return tables[len(tables)-1], true
}

// ReadLedger reads h's tool's history in db. A database whose history table
// does not exist has applied nothing: the zero Ledger (with Table set) and
// exists false. A tool forge cannot read (custom) is an error.
func (h Hook) ReadLedger(ctx context.Context, db ledgerQuerier) (l Ledger, exists bool, err error) {
	t, ok := h.ledgerTable()
	if !ok {
		return Ledger{}, false, fmt.Errorf("forge cannot read the history of migration tool %s", h.Tool)
	}
	l.Table = t
	ident := quoteIdent(t.Name)
	if t.Schema != "" {
		ident = quoteIdent(t.Schema) + "." + ident
	}
	rows, err := db.QueryContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, ident)
	if err != nil {
		return l, false, fmt.Errorf("look for %s: %w", ident, err)
	}
	exists, err = scanOne[bool](rows)
	if err != nil || !exists {
		return l, false, err
	}

	// Every query yields (migration, ok, kind, sum, repeatable) in the
	// order the tool applied them.
	var query string
	switch h.Tool {
	case config.MigrationToolGolangMigrate:
		// One row: the current version, and ok is its dirty flag.
		query = `SELECT version::text, dirty, '', '', false FROM ` + ident + ` ORDER BY version DESC LIMIT 1`
	case config.MigrationToolGoose:
		// Replayed in order: a later is_applied=false row is a down.
		query = `SELECT version_id::text, is_applied, '', '', false FROM ` + ident + ` WHERE version_id <> 0 ORDER BY id`
	case config.MigrationToolDbmate:
		query = `SELECT version::text, true, '', '', false FROM ` + ident + ` ORDER BY version`
	case config.MigrationToolFlyway:
		// A repeatable has no version: it is its script, re-run (a new
		// row) whenever its checksum changes.
		query = `SELECT COALESCE(version, script), success, type, COALESCE(checksum::text, ''), version IS NULL FROM ` + ident + ` ORDER BY installed_rank`
	case config.MigrationToolAtlas:
		query = `SELECT version, applied >= total AND COALESCE(error, '') = '', type::text, COALESCE(hash, ''), false FROM ` + ident + ` WHERE version NOT LIKE '.%' ORDER BY version`
	default:
		return l, false, fmt.Errorf("forge cannot read the history of migration tool %s", h.Tool)
	}
	rows, err = db.QueryContext(ctx, query)
	if err != nil {
		return l, true, fmt.Errorf("read %s: %w", ident, err)
	}
	defer func() { _ = rows.Close() }()
	type run struct {
		ok, undone bool
		sum        string
	}
	last := map[string]run{}
	var order []string
	for rows.Next() {
		var key, kind, sum string
		var ok, repeatable bool
		if err := rows.Scan(&key, &ok, &kind, &sum, &repeatable); err != nil {
			return l, true, fmt.Errorf("read %s: %w", ident, err)
		}
		switch h.Tool {
		case config.MigrationToolGolangMigrate:
			// golang-migrate records the version it is applying before
			// it applies it: a dirty version failed part-way, a clean one
			// is applied along with every version below it.
			if ok {
				l.Dirty = key
			} else {
				l.Through = key
			}
			continue
		case config.MigrationToolFlyway:
			switch {
			case kind == "SCHEMA" || kind == "DELETE":
				continue
			case kind == "BASELINE":
				l.Through = maxVersion(l.Through, key)
				continue
			case strings.HasPrefix(kind, "UNDO_") && ok:
				// An undo takes its version back out.
				last[key] = run{undone: true}
				continue
			}
		case config.MigrationToolAtlas:
			// atlas's RevisionType is a bit set; 1 is a baseline.
			if n, err := strconv.Atoi(kind); err == nil && n&1 != 0 {
				l.Through = maxVersion(l.Through, key)
				continue
			}
		}
		if repeatable {
			key = RepeatablePrefix + key
		}
		if _, seen := last[key]; !seen {
			order = append(order, key)
		}
		last[key] = run{ok: ok, sum: sum}
	}
	if err := rows.Err(); err != nil {
		return l, true, fmt.Errorf("read %s: %w", ident, err)
	}
	// goose's false is a down (the version is not applied); flyway's and
	// atlas's is a migration whose last run failed part-way.
	failedMeansDirty := h.Tool == config.MigrationToolFlyway || h.Tool == config.MigrationToolAtlas
	for _, k := range order {
		r := last[k]
		switch {
		case r.undone:
		case r.ok:
			l.Applied = append(l.Applied, k)
			if r.sum != "" {
				if l.Sums == nil {
					l.Sums = map[string]string{}
				}
				l.Sums[k] = r.sum
			}
		case failedMeansDirty && l.Dirty == "":
			l.Dirty = k
		}
	}
	return l, true, nil
}

// Behind returns the first migration l records as applied that have does
// not, or "" when have has applied everything l has. l is the ledger of a
// database the tool brought fully up to date (the shadow), have the target's.
// A version is applied when have records it or it is at or below have's
// Through; a repeatable only when have records it with the same checksum,
// since flyway re-runs one whose content changed.
func (l Ledger) Behind(have Ledger) string {
	applied := map[string]bool{}
	for _, v := range have.Applied {
		applied[v] = true
	}
	covered := func(v string) bool {
		if strings.HasPrefix(v, RepeatablePrefix) {
			return applied[v] && l.Sums[v] == have.Sums[v]
		}
		return applied[v] || (have.Through != "" && CompareVersions(v, have.Through) <= 0)
	}
	for _, v := range l.Applied {
		if !covered(v) {
			return v
		}
	}
	if l.Through != "" && !covered(l.Through) {
		return l.Through
	}
	return ""
}

// Diverged returns the first versioned migration both ledgers record as
// applied with different checksums, or "": have applied a file whose content
// differs from the one on disk now, so its schema is not the shadow's.
func (l Ledger) Diverged(have Ledger) string {
	for _, v := range l.Applied {
		if strings.HasPrefix(v, RepeatablePrefix) {
			continue
		}
		want, theirs := l.Sums[v], have.Sums[v]
		if want != "" && theirs != "" && want != theirs {
			return v
		}
	}
	return ""
}

// Empty reports whether the ledger records nothing applied.
func (l Ledger) Empty() bool {
	return len(l.Applied) == 0 && l.Through == ""
}

// CompareVersions orders two migration versions the way the tools do:
// numerically, segment by segment, where a segment is a run of digits
// (`20240101120000`, `1.2.10`, `3_1`). It returns -1, 0 or 1. A version with
// a non-digit run falls back to comparing that run as text.
func CompareVersions(a, b string) int {
	as, bs := versionSegments(a), versionSegments(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		if i >= len(as) {
			return -1
		}
		if i >= len(bs) {
			return 1
		}
		if c := compareSegment(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return 0
}

func versionSegments(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return !unicode.IsDigit(r) && !unicode.IsLetter(r) })
}

func compareSegment(a, b string) int {
	if isDigits(a) && isDigits(b) {
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
	}
	return strings.Compare(a, b)
}

func isDigits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

func maxVersion(a, b string) string {
	if a == "" || CompareVersions(b, a) > 0 {
		return b
	}
	return a
}

// quoteIdent quotes a postgres identifier.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func scanOne[T any](rows *sql.Rows) (T, error) {
	var v T
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return v, err
		}
		return v, sql.ErrNoRows
	}
	if err := rows.Scan(&v); err != nil {
		return v, err
	}
	return v, rows.Err()
}
