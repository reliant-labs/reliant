// File: internal/cli/lint/lint_unwritten_outcome.go
//
// The consequence clause shared by the two unwritten-column rules —
// forgeconv-read-only-field-unwritten and
// forgeconv-computed-field-unwritten.
//
// Both rules report the same defect: a column that no code writes, which
// therefore ships as whatever the schema leaves behind. Their persuasive
// force comes entirely from making that outcome CONCRETE — the reason the
// read-only rule caught real defects in two separate dogfood runs is that
// "$0.00 on an invoice" is a thing a reader can picture, where "takes the
// column default" is not.
//
// ── Why the illustration had to become type-specific ──────────────────────
//
// The clause used to be one fixed money sentence spliced into every
// finding: "for a money column that ships as $0.00". As a statement about
// the bug CLASS that is true. As a sentence sharing a clause with one
// specific field it is a trap, because nothing in it signals that it has
// stopped describing the field named two clauses earlier.
//
// A dogfood run hit exactly that. A `TIMESTAMPTZ` column produced:
//
//	Document.quarantined_at is marked `forge:read-only` … every row takes
//	the column default — for a money column that ships as $0.00 …
//
// and the reader's report was "I genuinely thought forge had mis-detected
// quarantined_at as numeric, and started checking my proto for a type
// error." The rule spent that reader's attention on a bug that did not
// exist, which is the same currency a false positive spends.
//
// The fix is not to delete the vividness — that is the working part. It is
// to make the illustration TRUE of the column in hand, which both rules can
// do because both already know the type. The read-only rule parsed the DDL
// (sqlColumn.Type); the computed rule has the proto field's kind. So a
// money column keeps its $0.00, and a timestamp gets the consequence that
// is actually alarming about a timestamp: it stays NULL forever, so the
// event it records never appears to have happened.
//
// ── Why a shape and not the type name ─────────────────────────────────────
//
// The clause needs the CONSEQUENCE, not the type: "ships as BIGINT" is not
// a sentence, and enumerating every postgres type would put a type table in
// a lint message. valueShape is the small set of outcomes that read
// differently to a human, and both the SQL and the proto side project onto
// it — which is also what keeps the two rules' wording identical, as they
// must be for one defect reported under two ids.

package lint

import (
	"regexp"
	"strings"
)

// valueShape is the consequence-relevant classification of a column: what a
// human would notice if every row carried the type's empty value.
type valueShape int

const (
	// shapeUnknown is the honest answer for a type this classifier does
	// not recognize (UUID, a domain type, an enum). Its consequence names
	// no type at all, which is the property the whole file exists to
	// protect — an unrecognized column must not be handed someone else's
	// illustration.
	shapeUnknown valueShape = iota
	shapeMoney
	shapeNumeric
	shapeText
	shapeBool
	shapeTimestamp
	shapeJSON
)

// shapeConsequence renders what the reader would see if nothing ever writes
// the column. Each one is written to be pictured rather than parsed: the
// money case keeps the $0.00 that made this warning work, and the others
// aim for the equivalent in their own terms.
//
// INVARIANT: only shapeMoney may mention money or dollars. Every other arm
// describing itself as a money column is the exact defect this file was
// written to remove, and it is pinned by
// TestShapeConsequence_NonNumericNeverClaimsMoney.
func shapeConsequence(shape valueShape) string {
	switch shape {
	case shapeMoney:
		return "a money column that reads $0.00 on every screen and every invoice it appears on"
	case shapeNumeric:
		return "a count or measure that reads 0 for every row, including the rows where it should not"
	case shapeText:
		return "an empty string for every row, rendering as a blank where a label should be"
	case shapeBool:
		return "false for every row, so every branch that checks it takes the negative path forever"
	case shapeTimestamp:
		return "a timestamp that stays NULL forever, so the event it records never appears to have happened"
	case shapeJSON:
		return "an empty document for every row, so every reader of it finds nothing and carries on"
	default:
		return "the same empty value for every row, indistinguishable from one somebody chose"
	}
}

// moneyColumnNameRE matches the names forge projects overwhelmingly give
// money columns. Anchored to a SUFFIX on purpose: `total_items` is a count,
// and reading a bare "total" as money would reintroduce the wrong-type
// illustration in the opposite direction.
var moneyColumnNameRE = regexp.MustCompile(`(?i)(?:^|_)(?:cents|amount|price|cost|fee|balance|subtotal|usd)$`)

// sqlTypeHeadRE captures the type at the head of a column definition, up to
// the first constraint keyword — so `NUMERIC(12, 2) NOT NULL DEFAULT 0`
// yields `NUMERIC(12, 2)` and `TIMESTAMP WITH TIME ZONE` survives whole.
var sqlTypeHeadRE = regexp.MustCompile(
	`(?is)^(.*?)(?:\s+\b(?:NOT\s+NULL|NULL|DEFAULT|PRIMARY\s+KEY|UNIQUE|REFERENCES|CHECK|GENERATED|COLLATE)\b.*)?$`)

// sqlValueShape classifies a column from its DDL type and name.
//
// The type wins where it is decisive — NUMERIC and DECIMAL are what a
// schema uses when it means money — and the name decides only among the
// integer types, where `total_cents` and `retry_count` are the same BIGINT
// and only the name distinguishes them.
func sqlValueShape(sqlType, column string) valueShape {
	t := strings.ToUpper(strings.TrimSpace(sqlType))
	switch {
	case t == "":
		// No type parsed. The name is weaker evidence than the type, so
		// it is consulted but nothing is invented from its absence.
		if moneyColumnNameRE.MatchString(column) {
			return shapeMoney
		}
		return shapeUnknown
	case strings.HasPrefix(t, "NUMERIC"), strings.HasPrefix(t, "DECIMAL"), t == "MONEY":
		return shapeMoney
	case strings.HasPrefix(t, "TIMESTAMP"), t == "DATE", strings.HasPrefix(t, "TIME"):
		return shapeTimestamp
	case t == "BOOLEAN", t == "BOOL":
		return shapeBool
	case strings.HasPrefix(t, "JSON"):
		return shapeJSON
	case strings.HasPrefix(t, "TEXT"), strings.HasPrefix(t, "VARCHAR"),
		strings.HasPrefix(t, "CHAR"), strings.HasPrefix(t, "CITEXT"):
		return shapeText
	case t == "BIGINT", t == "INTEGER", t == "INT", t == "INT4", t == "INT8",
		t == "SMALLINT", t == "REAL", strings.HasPrefix(t, "DOUBLE"), t == "FLOAT8":
		if moneyColumnNameRE.MatchString(column) {
			return shapeMoney
		}
		return shapeNumeric
	default:
		return shapeUnknown
	}
}

// timestampFieldNameRE matches forge's own convention for a timestamp
// field. It is consulted because the scaffolded entities spell timestamps
// as proto `string`, so the kind alone would classify `approved_at` as text
// and hand it the blank-label consequence — true of a string, and wrong
// about what this column is for.
var timestampFieldNameRE = regexp.MustCompile(`(?i)(?:^|_)(?:at|time|date|on)$`)

// protoValueShape classifies a field from its proto kind, message type and
// name — the computed rule's side, which has no migrations to read.
func protoValueShape(kind, typeName, field string) valueShape {
	if typeName == "google.protobuf.Timestamp" || timestampFieldNameRE.MatchString(field) {
		return shapeTimestamp
	}
	switch kind {
	case "bool":
		return shapeBool
	case "string", "bytes":
		return shapeText
	case "int32", "int64", "uint32", "uint64", "sint32", "sint64",
		"fixed32", "fixed64", "sfixed32", "sfixed64", "float", "double":
		if moneyColumnNameRE.MatchString(field) {
			return shapeMoney
		}
		return shapeNumeric
	default:
		// message, enum, map — no empty value a reader would picture.
		return shapeUnknown
	}
}
