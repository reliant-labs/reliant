package seedplan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gofakeit "github.com/brianvoe/gofakeit/v7"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// TestVocabTypes_EveryDeclaredTypeGenerates walks the SUPPORTED SET ITSELF —
// not a copy of it — and requires each type to produce usable values. A type
// added to the table is covered the moment it is added; a type whose upstream
// generator disappears fails here rather than at someone's seed run.
func TestVocabTypes_EveryDeclaredTypeGenerates(t *testing.T) {
	names := VocabTypeNames()
	if len(names) == 0 {
		t.Fatal("no vocab types are supported — the derived set is empty")
	}
	for _, name := range names {
		vals, err := VocabTypeValues(name, 0, "widgets", "col", 8)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(vals) == 0 {
			t.Errorf("%s: produced no values", name)
			continue
		}
		for _, v := range vals {
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s: produced a blank value", name)
			}
		}
	}
	t.Logf("verified %d vocab type(s): %s", len(names), strings.Join(names, ", "))
}

// TestVocabTypes_AreDeterministic pins forge's standing seed guarantee: the
// same (salt, table, column) renders the same values. The expectation is the
// generator's OWN first output, so this cannot pass by agreeing with a stale
// hardcoded list.
func TestVocabTypes_AreDeterministic(t *testing.T) {
	for _, name := range VocabTypeNames() {
		a, err := VocabTypeValues(name, 0, "orders", "customer_email", 5)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, err := VocabTypeValues(name, 0, "orders", "customer_email", 5)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Join(a, "|") != strings.Join(b, "|") {
			t.Errorf("%s: two identical calls disagreed:\n  %v\n  %v", name, a, b)
		}
		// A different column must draw independently, or adding vocab to one
		// column would reshuffle another.
		c, err := VocabTypeValues(name, 0, "orders", "other_column", 5)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Join(a, "|") == strings.Join(c, "|") && len(a) > 1 {
			t.Errorf("%s: two different columns drew identical values — draws are not column-local", name)
		}
	}
}

// TestVocabTypes_ValuesMatchTheirGenerator asserts each type's values really
// come from the gofakeit lookup the table names, by regenerating from that
// lookup directly with the same faker seed and requiring the same output.
// This is what makes the name→lookup mapping testable at all: it compares
// forge's output against the PRODUCER, not against literals.
func TestVocabTypes_ValuesMatchTheirGenerator(t *testing.T) {
	checked := 0
	for _, g := range vocabTypeGens {
		info := gofakeit.GetFuncLookup(g.lookup)
		if info == nil {
			t.Errorf("%s: lookup %q missing", g.name, g.lookup)
			continue
		}
		got, err := VocabTypeValues(g.name, 0, "t", "c", 1)
		if err != nil || len(got) == 0 {
			t.Errorf("%s: %v", g.name, err)
			continue
		}
		// Same seed derivation as VocabTypeValues.
		faker := gofakeit.New(cellHash(0, "t", "c#type", 0))
		want, err := info.Generate(faker, &gofakeit.MapParams{}, info)
		if err != nil {
			t.Errorf("%s: regenerate: %v", g.name, err)
			continue
		}
		if got[0] != want {
			t.Errorf("%s: first value %q does not match lookup %q output %v",
				g.name, got[0], g.lookup, want)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no types compared against their generators")
	}
}

// TestVocabTypes_UnknownTypeIsRejected: an unsupported type must fail loudly
// and the message must list what IS supported (derived, so it cannot drift).
func TestVocabTypes_UnknownTypeIsRejected(t *testing.T) {
	_, err := VocabTypeValues("not_a_real_type", 0, "t", "c", 3)
	if err == nil {
		t.Fatal("unknown type was accepted")
	}
	for _, name := range VocabTypeNames() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error message omits supported type %q: %v", name, err)
		}
	}
}

// TestLoadVocab_TypeEntryExpandsToValues covers the YAML surface end to end:
// `{type: email}` must load as a real value pool.
func TestLoadVocab_TypeEntryExpandsToValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vocab.yaml")
	body := "columns:\n  orders.customer_email: {type: email}\n  orders.shipping_country: {type: country_code}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := LoadVocab(path)
	if err != nil {
		t.Fatalf("LoadVocab: %v", err)
	}
	if v == nil {
		t.Fatal("typed entries loaded as an empty overlay")
	}
	for _, key := range []string{"orders.customer_email", "orders.shipping_country"} {
		if len(v.Columns[key]) == 0 {
			t.Errorf("%s expanded to no values", key)
		}
	}
	// A country code is 2 characters by definition — a property of the TYPE,
	// checked without naming any specific code.
	for _, cc := range v.Columns["orders.shipping_country"] {
		if len([]rune(cc)) != 2 {
			t.Errorf("country_code produced %q, which is not a 2-character code", cc)
		}
	}
	// An email contains exactly one @ — likewise a property, not a literal.
	for _, em := range v.Columns["orders.customer_email"] {
		if strings.Count(em, "@") != 1 {
			t.Errorf("email produced %q", em)
		}
	}
}

// TestLoadVocab_UnknownTypeIsALoadError: a typo in vocab.yaml must fail at
// load with a line number, not silently fall back to generic synthesis.
func TestLoadVocab_UnknownTypeIsALoadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vocab.yaml")
	if err := os.WriteFile(path, []byte("columns:\n  orders.x: {type: emial}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadVocab(path)
	if err == nil {
		t.Fatal("a misspelled type loaded successfully")
	}
	if !strings.Contains(err.Error(), "emial") {
		t.Errorf("error does not name the offending type: %v", err)
	}
}

// TestInferVocabType_OnlyWhenForced is the inference contract, and most of it
// is about REFUSING. A column whose reading is a guess must return false, so
// the author is asked rather than given a plausible wrong value.
func TestInferVocabType_OnlyWhenForced(t *testing.T) {
	str := func(name string) schemadef.Column {
		return schemadef.Column{Name: name, Type: schemadef.TypeString, NotNull: true}
	}
	check := func(name, def string, cols ...string) schemadef.CheckConstraint {
		return schemadef.CheckConstraint{Name: name, Def: def, Columns: cols}
	}

	cases := []struct {
		desc  string
		table schemadef.Table
		col   string
		want  string // "" means "must refuse"
	}{
		{
			desc: "email name + @-mentioning regex is forced",
			table: schemadef.Table{Name: "orders", Columns: []schemadef.Column{str("customer_email")},
				Checks: []schemadef.CheckConstraint{
					check("c", `CHECK ((customer_email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$'::text))`, "customer_email")}},
			col: "customer_email", want: "email",
		},
		{
			desc:  "email name with NO constraint is a guess — refuse",
			table: schemadef.Table{Name: "orders", Columns: []schemadef.Column{str("customer_email")}},
			col:   "customer_email", want: "",
		},
		{
			desc: "country pinned to exactly 2 chars is forced",
			table: schemadef.Table{Name: "orders", Columns: []schemadef.Column{str("shipping_country")},
				Checks: []schemadef.CheckConstraint{
					check("c", `CHECK ((char_length(shipping_country) = 2))`, "shipping_country")}},
			col: "shipping_country", want: "country_code",
		},
		{
			desc: "country with a WIDE range could be a full name — refuse",
			table: schemadef.Table{Name: "orders", Columns: []schemadef.Column{str("shipping_country")},
				Checks: []schemadef.CheckConstraint{
					check("c", `CHECK (((char_length(shipping_country) >= 1) AND (char_length(shipping_country) <= 60)))`, "shipping_country")}},
			col: "shipping_country", want: "",
		},
		{
			desc: "a 2-char column that is NOT a country — refuse",
			table: schemadef.Table{Name: "orders", Columns: []schemadef.Column{str("state_abbr")},
				Checks: []schemadef.CheckConstraint{
					check("c", `CHECK ((char_length(state_abbr) = 2))`, "state_abbr")}},
			col: "state_abbr", want: "",
		},
		{
			// "countryside" CONTAINS "country" but is not a country column.
			// Segment matching is what separates the two; a substring test
			// would infer country_code here and ship ISO codes into a column
			// about land.
			desc: "substring match must not fire: countryside_ref is not a country",
			table: schemadef.Table{Name: "plots", Columns: []schemadef.Column{str("countryside_ref")},
				Checks: []schemadef.CheckConstraint{
					check("c", `CHECK ((char_length(countryside_ref) = 2))`, "countryside_ref")}},
			col: "countryside_ref", want: "",
		},
		{
			// Likewise "email" inside a longer segment.
			desc: "emailer_id is not an email column",
			table: schemadef.Table{Name: "jobs", Columns: []schemadef.Column{str("emailer_id")},
				Checks: []schemadef.CheckConstraint{
					check("c", `CHECK ((emailer_id ~ '^[^@]+@.+$'::text))`, "emailer_id")}},
			col: "emailer_id", want: "",
		},
		{
			desc: "currency pinned to 3 chars is forced",
			table: schemadef.Table{Name: "orders", Columns: []schemadef.Column{str("currency")},
				Checks: []schemadef.CheckConstraint{
					check("c", `CHECK ((char_length(currency) = 3))`, "currency")}},
			col: "currency", want: "currency_code",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			var col schemadef.Column
			for _, c := range tc.table.Columns {
				if c.Name == tc.col {
					col = c
				}
			}
			got, ok := InferVocabType(tc.table, col)
			if tc.want == "" {
				if ok {
					t.Errorf("inferred %q where the reading is a guess", got)
				}
				return
			}
			if !ok {
				t.Fatalf("refused to infer %q where the constraint forces it", tc.want)
			}
			if got != tc.want {
				t.Errorf("inferred %q, want %q", got, tc.want)
			}
			// Whatever it inferred must actually be a supported type.
			if !IsVocabType(got) {
				t.Errorf("inferred unsupported type %q", got)
			}
		})
	}
}
