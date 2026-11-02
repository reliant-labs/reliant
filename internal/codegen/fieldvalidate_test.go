package codegen

import (
	"strings"
	"testing"
)

func u64(n uint64) *uint64 { return &n }

func TestFieldConstraints_HasAny(t *testing.T) {
	if (&FieldConstraints{}).HasAny() {
		t.Fatal("empty constraints must report HasAny=false")
	}
	cases := []FieldConstraints{
		{Gte: "0"}, {MinLen: u64(1)}, {MaxLen: u64(5)},
		{Pattern: "^x$"}, {Email: true}, {Required: true}, {Lt: "10"},
	}
	for _, c := range cases {
		if !c.HasAny() {
			t.Errorf("%+v should report HasAny=true", c)
		}
	}
}

func TestFieldConstraints_SQLChecks_Numeric(t *testing.T) {
	c := &FieldConstraints{Gte: "0", Lte: "100"}
	got := c.SQLChecks("amount_cents", "int64")
	want := []string{"CHECK (amount_cents >= 0)", "CHECK (amount_cents <= 100)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("SQLChecks int64 = %v, want %v", got, want)
	}
	// gt/lt spellings, and a float literal preserved verbatim.
	c = &FieldConstraints{Gt: "0", Lt: "9.99"}
	got = c.SQLChecks("price", "double")
	if strings.Join(got, "|") != "CHECK (price > 0)|CHECK (price < 9.99)" {
		t.Fatalf("SQLChecks double = %v", got)
	}
	// Numeric constraints must NOT be projected onto a string kind.
	if s := (&FieldConstraints{Gte: "0"}).SQLChecks("x", "string"); len(s) != 0 {
		t.Fatalf("numeric constraint on string kind must yield no checks, got %v", s)
	}
}

func TestFieldConstraints_SQLChecks_String(t *testing.T) {
	// distinct min+max collapse to BETWEEN.
	c := &FieldConstraints{MinLen: u64(2), MaxLen: u64(64)}
	if got := c.SQLChecks("name", "string"); len(got) != 1 || got[0] != "CHECK (char_length(name) BETWEEN 2 AND 64)" {
		t.Fatalf("SQLChecks string len = %v", got)
	}
	// Fixed-length code (min == max, e.g. string.len = 3 for an ISO currency):
	// a single exact `= N` check, never `BETWEEN N AND N` — the form buf lint
	// requires on the wire and the seed-data length parser reads.
	c = &FieldConstraints{MinLen: u64(3), MaxLen: u64(3)}
	if got := c.SQLChecks("currency", "string"); len(got) != 1 || got[0] != "CHECK (char_length(currency) = 3)" {
		t.Fatalf("SQLChecks fixed-length = %v", got)
	}
	// required with no explicit min → non-empty check.
	if got := (&FieldConstraints{Required: true}).SQLChecks("name", "string"); len(got) != 1 || got[0] != "CHECK (char_length(name) >= 1)" {
		t.Fatalf("SQLChecks required = %v", got)
	}
	// pattern → regex, single quotes doubled.
	c = &FieldConstraints{Pattern: "^SKU-[0-9]+$"}
	if got := c.SQLChecks("sku", "string"); len(got) != 1 || got[0] != "CHECK (sku ~ '^SKU-[0-9]+$')" {
		t.Fatalf("SQLChecks pattern = %v", got)
	}
	// email → permissive regex check.
	if got := (&FieldConstraints{Email: true}).SQLChecks("email", "string"); len(got) != 1 || !strings.Contains(got[0], "email ~ '^[^@") {
		t.Fatalf("SQLChecks email = %v", got)
	}
}

func TestFieldConstraints_SuppressesZeroDefault(t *testing.T) {
	yes := []struct {
		c    FieldConstraints
		kind string
	}{
		{FieldConstraints{Required: true}, "string"},
		{FieldConstraints{MinLen: u64(1)}, "string"},
		{FieldConstraints{Email: true}, "string"},
		{FieldConstraints{Pattern: "^x$"}, "string"},
		{FieldConstraints{Gt: "0"}, "int64"},
		{FieldConstraints{Gte: "5"}, "int64"},
	}
	for _, tc := range yes {
		if !tc.c.SuppressesZeroDefault(tc.kind) {
			t.Errorf("%+v (%s) should suppress zero default", tc.c, tc.kind)
		}
	}
	no := []struct {
		c    FieldConstraints
		kind string
	}{
		{FieldConstraints{MaxLen: u64(64)}, "string"}, // empty still valid
		{FieldConstraints{Gte: "0"}, "int64"},         // 0 still valid
		{FieldConstraints{Lte: "100"}, "int64"},       // 0 still valid
		{FieldConstraints{}, "string"},
	}
	for _, tc := range no {
		if tc.c.SuppressesZeroDefault(tc.kind) {
			t.Errorf("%+v (%s) should NOT suppress zero default", tc.c, tc.kind)
		}
	}
}

func TestParseRawValidateOptions(t *testing.T) {
	if ParseRawValidateOptions("") != nil {
		t.Fatal("empty options must parse to nil")
	}
	if ParseRawValidateOptions("[deprecated = true]") != nil {
		t.Fatal("non-validate options must parse to nil")
	}
	c := ParseRawValidateOptions(`[(buf.validate.field).int64.gte = 0, (buf.validate.field).int64.lte = 100000]`)
	if c == nil || c.Gte != "0" || c.Lte != "100000" {
		t.Fatalf("numeric parse = %+v", c)
	}
	c = ParseRawValidateOptions(`[(buf.validate.field).string.min_len = 2, (buf.validate.field).string.max_len = 64]`)
	if c == nil || c.MinLen == nil || *c.MinLen != 2 || c.MaxLen == nil || *c.MaxLen != 64 {
		t.Fatalf("string len parse = %+v", c)
	}
	// Exact length: `string.len = N` (the fixed-length-code form buf lint wants
	// over min_len == max_len) reads as MinLen == MaxLen == N and projects to a
	// single `CHECK (char_length(col) = N)`.
	c = ParseRawValidateOptions(`[(buf.validate.field).string.len = 3]`)
	if c == nil || c.MinLen == nil || *c.MinLen != 3 || c.MaxLen == nil || *c.MaxLen != 3 {
		t.Fatalf("string.len parse = %+v", c)
	}
	if got := c.SQLChecks("currency", "string"); len(got) != 1 || got[0] != "CHECK (char_length(currency) = 3)" {
		t.Fatalf("string.len SQLChecks = %v", got)
	}
	c = ParseRawValidateOptions(`[(buf.validate.field).string.pattern = "^SKU-[0-9,]+$"]`)
	if c == nil || c.Pattern != "^SKU-[0-9,]+$" {
		t.Fatalf("pattern parse (with comma inside quotes) = %+v", c)
	}
	c = ParseRawValidateOptions(`[(buf.validate.field).string.email = true]`)
	if c == nil || !c.Email {
		t.Fatalf("email parse = %+v", c)
	}
	c = ParseRawValidateOptions(`[(buf.validate.field).required = true]`)
	if c == nil || !c.Required {
		t.Fatalf("required parse = %+v", c)
	}
}

// TestParseRawValidateOptions_Aggregate pins the braced aggregate spellings
// to the SAME projection as the dotted form, so a brand-new entity births its
// CHECK constraints no matter which syntax the author used (the birth-side
// raw scan must agree with the compiled-descriptor / drift-detector reading).
func TestParseRawValidateOptions_Aggregate(t *testing.T) {
	// Per-type partial aggregate — the reported defect: gte/lte in a `{ }`.
	c := ParseRawValidateOptions(`[(buf.validate.field).int64 = {gte: 1, lte: 12}]`)
	if c == nil || c.Gte != "1" || c.Lte != "12" {
		t.Fatalf("numeric aggregate parse = %+v", c)
	}
	// Dot and braced forms must project identically.
	dot := ParseRawValidateOptions(`[(buf.validate.field).int64.gte = 1, (buf.validate.field).int64.lte = 12]`)
	if dot == nil || *c != *dot {
		t.Fatalf("aggregate %+v must equal dotted %+v", c, dot)
	}

	// String rules in the partial aggregate, incl. a pattern that carries a
	// comma and braces INSIDE its quotes (must not confuse token/brace split).
	c = ParseRawValidateOptions(`[(buf.validate.field).string = {min_len: 2, max_len: 64, pattern: "^SKU-[0-9]{2,4}$"}]`)
	if c == nil || c.MinLen == nil || *c.MinLen != 2 || c.MaxLen == nil || *c.MaxLen != 64 {
		t.Fatalf("string aggregate len parse = %+v", c)
	}
	if c.Pattern != "^SKU-[0-9]{2,4}$" {
		t.Fatalf("string aggregate pattern parse = %q", c.Pattern)
	}

	// Full aggregate — per-type rules nested a level deeper, plus a top-level
	// required. Both must be reached.
	c = ParseRawValidateOptions(`[(buf.validate.field) = {required: true, int64: {gte: 0}}]`)
	if c == nil || !c.Required || c.Gte != "0" {
		t.Fatalf("full aggregate parse = %+v", c)
	}

	// Colon-less message form (`int64 {gte: 1}`) is valid proto text too.
	c = ParseRawValidateOptions(`[(buf.validate.field) = {int64 {gte: 5}}]`)
	if c == nil || c.Gte != "5" {
		t.Fatalf("colon-less aggregate parse = %+v", c)
	}

	// email in aggregate form.
	c = ParseRawValidateOptions(`[(buf.validate.field).string = {email: true}]`)
	if c == nil || !c.Email {
		t.Fatalf("email aggregate parse = %+v", c)
	}
}

func TestFieldConstraints_ZodChain(t *testing.T) {
	cases := []struct {
		name     string
		c        FieldConstraints
		formType string
		want     string
	}{
		{"numeric bounds", FieldConstraints{Gte: "0", Lte: "100"}, "number", ".gte(0).lte(100)"},
		{"numeric gt/lt", FieldConstraints{Gt: "1", Lt: "9.99"}, "number", ".gt(1).lt(9.99)"},
		{"string len", FieldConstraints{MinLen: u64(2), MaxLen: u64(64)}, "text", ".min(2).max(64)"},
		{"required only", FieldConstraints{Required: true}, "text", ".min(1)"},
		{"email", FieldConstraints{Email: true}, "text", ".email()"},
		{"pattern", FieldConstraints{Pattern: `^SKU-\d+$`}, "text", `.regex(new RegExp("^SKU-\\d+$"))`},
		{"numeric ignores string rules", FieldConstraints{MinLen: u64(3)}, "number", ""},
		{"empty", FieldConstraints{}, "text", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.ZodChain(tc.formType); got != tc.want {
				t.Errorf("ZodChain = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestValidateFieldOptions pins the carrier that lets a FLATTENING request
// message (Create<Entity>Request) re-declare an entity field's rules: the
// options come back verbatim, only the `(buf.validate.field)` ones, folded
// onto one line.
func TestValidateFieldOptions(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"no options at all",
			"",
			"",
		},
		{
			"options with no validate rule",
			`[deprecated = true]`,
			"",
		},
		{
			"two dotted options on one field",
			`[(buf.validate.field).string.min_len = 1, (buf.validate.field).string.max_len = 120]`,
			`[(buf.validate.field).string.min_len = 1, (buf.validate.field).string.max_len = 120]`,
		},
		{
			// A braced aggregate is ONE option: the comma inside the braces
			// is not a top-level separator.
			"braced aggregate",
			`[(buf.validate.field).int64 = {gte: 1, lte: 1000000}]`,
			`[(buf.validate.field).int64 = {gte: 1, lte: 1000000}]`,
		},
		{
			// The raw scan folds a multi-line braced value onto one line with
			// runs of spaces; they collapse, and the option stays whole.
			"multi-line braced value collapses",
			`[(buf.validate.field).int32 = {   gte: 1   lte: 12  }]`,
			`[(buf.validate.field).int32 = { gte: 1 lte: 12 }]`,
		},
		{
			// A pattern literal may carry both a bracket and a comma —
			// the two characters a naive splitter breaks on — and runs of
			// spaces that must survive inside the quotes.
			"quoted pattern with brackets, commas and spaces",
			`[(buf.validate.field).string.pattern = "^[A-Z]{2,4}-  [0-9]+$"]`,
			`[(buf.validate.field).string.pattern = "^[A-Z]{2,4}-  [0-9]+$"]`,
		},
		{
			"non-validate options are dropped, validate ones kept",
			`[deprecated = true, (buf.validate.field).required = true]`,
			`[(buf.validate.field).required = true]`,
		},
		{
			// Rules outside the SQL/zod-projected subset ride along too —
			// the wire's target is proto, so nothing needs dropping.
			"unprojected rules still travel",
			`[(buf.validate.field).string.uuid = true]`,
			`[(buf.validate.field).string.uuid = true]`,
		},
		{
			"cel rule with an embedded brace and quote",
			`[(buf.validate.field).cel = {id: "n", message: "too big", expression: "this < 10"}]`,
			`[(buf.validate.field).cel = {id: "n", message: "too big", expression: "this < 10"}]`,
		},
		{
			"brackets optional",
			`(buf.validate.field).string.email = true`,
			`[(buf.validate.field).string.email = true]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidateFieldOptions(tc.in); got != tc.want {
				t.Errorf("ValidateFieldOptions(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestValidateFieldOptions_MatchesParsedRules keeps the two readers of the
// same `[...]` text honest: whatever ParseRawValidateOptions folds into the
// projected FieldConstraints, ValidateFieldOptions must also carry — a
// field that births a DB CHECK can never be one whose wire rules vanish.
func TestValidateFieldOptions_MatchesParsedRules(t *testing.T) {
	for _, in := range []string{
		`[(buf.validate.field).string.min_len = 1, (buf.validate.field).string.max_len = 120]`,
		`[(buf.validate.field).int64 = {gte: 1, lte: 1000000}]`,
		`[(buf.validate.field) = {int64: {gte: 1}, required: true}]`,
		`[(buf.validate.field).string.pattern = "^SKU-[0-9]+$"]`,
		`[(buf.validate.field).string.len = 3]`,
		`[deprecated = true, (buf.validate.field).string.email = true]`,
	} {
		if ParseRawValidateOptions(in).HasAny() && ValidateFieldOptions(in) == "" {
			t.Errorf("%q projects to SQL/zod but carries nothing to the wire", in)
		}
	}
	// And the converse for text with no rules in it at all.
	for _, in := range []string{"", `[deprecated = true]`} {
		if ValidateFieldOptions(in) != "" {
			t.Errorf("%q must carry nothing, got %q", in, ValidateFieldOptions(in))
		}
	}
}
