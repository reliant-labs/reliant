package codegen

import (
	"regexp"
	"strconv"
	"strings"
)

// InsertDefault is how an entity struct field lets a NOT NULL column's
// DEFAULT apply on INSERT.
//
// A struct field always carries a value, so "the caller did not set it" and
// "the caller set it to the Go zero" are the same bytes. For most defaulted
// columns that does not matter — `DEFAULT 0`, `DEFAULT false`, a DEFAULT of
// the empty string, and `DEFAULT '{}'` (an array) store exactly the zero the
// struct carries.
// For the rest it is silent wrong data: `threads.status INTEGER NOT NULL
// DEFAULT 2` was stored as 0 by every Create that did not mention status,
// and `jsonb NOT NULL DEFAULT '{}'` failed the insert with `""` (reliant
// P5b, F12.5). The two fixes `default:`-tag magic offers both lose: Bun's
// `default:` reads a zero as "unset", so an explicit `false` into
// `active BOOLEAN NOT NULL DEFAULT true` silently stores true.
//
// So the type carries the distinction, and nothing is guessed:
//
//   - InsertDefaultPointer: a scalar column whose DEFAULT is not its Go
//     zero (`DEFAULT 2`, `DEFAULT true`, `DEFAULT 'draft'`, or any
//     expression — `gen_random_uuid()::text`, `nextval(…)`). The field is a
//     pointer: nil means "the column DEFAULT", which Bun sends as DEFAULT
//     and pkg/crud reads back via RETURNING; a non-nil zero is written as
//     the zero. A read always yields non-nil.
//   - InsertDefaultNullZero: a column whose Go zero is never a value it
//     holds — a json document (`""` is not JSON), an array/bytea whose
//     DEFAULT is not empty, or a scalar a CHECK keeps from its zero (every
//     enum column forge births: `TEXT NOT NULL DEFAULT 'X' CHECK (col IN
//     (...))`). The zero IS "unset" there, so the field keeps its type and
//     takes `,nullzero`.
//   - InsertDefaultNone: no DEFAULT, a DEFAULT equal to the zero, or a
//     column with its own lifecycle (PK, generated, version, timestamp).
type InsertDefault int

// The InsertDefault values; see the type.
const (
	InsertDefaultNone InsertDefault = iota
	InsertDefaultPointer
	InsertDefaultNullZero
)

// InsertDefaultColumn is the column shape ColumnInsertDefault decides on —
// the facts both the struct emitter (internal/generator, from a plan field)
// and the CRUD conversion (from an EntityColumn) hold.
type InsertDefaultColumn struct {
	Canonical string // the canonical schema type ("int64", "json", …)
	IsArray   bool
	NotNull   bool
	IsPK      bool
	Generated bool
	Version   bool   // forge:version — pkg/crud owns its value
	Default   string // the DEFAULT expression verbatim, "" for none
	// ZeroRejected: a CHECK keeps the column from holding its Go zero
	// (an enum vocabulary without ''), so the zero already means unset.
	ZeroRejected bool
}

// ColumnInsertDefault decides how c's DEFAULT applies on INSERT. It is the
// ONE rule: the ORM struct and the CRUD conversions over it both read it,
// so a pointer field and the code that writes it cannot disagree.
func ColumnInsertDefault(c InsertDefaultColumn) InsertDefault {
	def := strings.TrimSpace(c.Default)
	if !c.NotNull || c.IsPK || c.Generated || c.Version || def == "" {
		return InsertDefaultNone
	}
	if c.Canonical == "time" && !c.IsArray {
		// A zero time.Time is never a stored instant; the ORM tags every
		// defaulted time column `default:` already, and pkg/crud stamps
		// the managed pair.
		return InsertDefaultNone
	}
	if c.Canonical == "json" && !c.IsArray {
		return InsertDefaultNullZero
	}
	base, ok := CanonicalGoTypeOK(c.Canonical, c.IsArray)
	if !ok {
		return InsertDefaultNone
	}
	if c.IsArray || base == "[]byte" {
		// pkg/crud normalizes a nil NOT NULL slice to empty, which is what
		// an empty DEFAULT already holds; anything else must come from the
		// DEFAULT itself.
		if isEmptyArrayDefault(def) {
			return InsertDefaultNone
		}
		return InsertDefaultNullZero
	}
	if pgDefaultIsZero(def, base) {
		return InsertDefaultNone
	}
	if c.ZeroRejected {
		return InsertDefaultNullZero
	}
	return InsertDefaultPointer
}

// pgDefaultCastRE strips the trailing `::type` cast postgres renders on a
// literal default (`'DRAFT'::text`, `'{}'::jsonb`, `'{}'::text[]`).
var pgDefaultCastRE = regexp.MustCompile(`::[\w ]+(\[\])?$`)

// pgDefaultIsZero reports whether a DEFAULT expression, as postgres renders
// it, is a plain literal equal to the Go zero of the scalar type base:
// `0`, `(0)::bigint`, `0.0`, `false`, the empty string, `'0'::numeric`. Anything
// else — another literal, or an expression (`now()`, `nextval(…)`,
// `gen_random_uuid()`) — is not, and its column needs a way to say unset.
func pgDefaultIsZero(def, base string) bool {
	raw := unwrapPGDefault(def)
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		raw = strings.ReplaceAll(raw[1:len(raw)-1], "''", "'")
		if base == "string" {
			return raw == ""
		}
	}
	switch base {
	case "int64", "int32", "int16", "float64":
		f, err := strconv.ParseFloat(raw, 64)
		return err == nil && f == 0
	case "bool":
		b, err := strconv.ParseBool(raw)
		return err == nil && !b
	}
	return false
}

// unwrapPGDefault strips the casts and parentheses postgres wraps a literal
// DEFAULT in: `('x'::text)`, `(-1)`, `'{}'::text[]`.
func unwrapPGDefault(def string) string {
	raw := strings.TrimSpace(pgDefaultCastRE.ReplaceAllString(strings.TrimSpace(def), ""))
	for len(raw) >= 2 && raw[0] == '(' && raw[len(raw)-1] == ')' {
		raw = strings.TrimSpace(pgDefaultCastRE.ReplaceAllString(strings.TrimSpace(raw[1:len(raw)-1]), ""))
	}
	return raw
}

// isEmptyArrayDefault reports a DEFAULT that is the empty array or empty
// bytea: `'{}'::text[]`, `ARRAY[]::text[]`, `'\x'::bytea`, an empty bytea literal.
func isEmptyArrayDefault(def string) bool {
	raw := unwrapPGDefault(def)
	switch strings.ToUpper(strings.ReplaceAll(raw, " ", "")) {
	case "'{}'", "ARRAY[]", `'\X'`, "''":
		return true
	}
	return false
}
