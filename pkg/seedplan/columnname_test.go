package seedplan

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// ─────────────────────────────────────────────────────────────────────────────
// The seeder reads a column's DECLARATION, never its NAME.
//
// tablename_test.go pins the same invariant one level up. This file is the
// column-granular half: what NOUN belongs in a column is a decision the schema
// does not carry, so forge cannot derive it and must not guess it. The
// declaration surface is db/seeds/vocab.yaml.
//
// Each assertion below derives what it wants from something the emitter
// STAMPS — SyntheticStringPrefix, the declared CHECK pattern, the
// schemadef.DetectConventions classification — and fails loudly when the set
// it derived is empty, so none of them can pass vacuously.
// ─────────────────────────────────────────────────────────────────────────────

// namedColumn is one column whose NAME used to decide its value. The list is
// the whole of the deleted vocabulary: money, currency codes, colors, design
// tokens, addresses, phone numbers, URLs, card last-four, birthdates, dates,
// roles, and the person/company name pools.
type namedColumn struct {
	name string
	typ  schemadef.CanonicalType
}

var deletedNameHeuristics = []namedColumn{
	{"name", schemadef.TypeString},
	{"first_name", schemadef.TypeString},
	{"last_name", schemadef.TypeString},
	{"full_name", schemadef.TypeString},
	{"email", schemadef.TypeString},
	{"support_email", schemadef.TypeString},
	{"phone", schemadef.TypeString},
	{"phone_number", schemadef.TypeString},
	{"title", schemadef.TypeString},
	{"description", schemadef.TypeString},
	{"url", schemadef.TypeString},
	{"website", schemadef.TypeString},
	{"avatar_url", schemadef.TypeString},
	{"webhook_url", schemadef.TypeString},
	{"redirect_uri", schemadef.TypeString},
	{"docs_link", schemadef.TypeString},
	{"theme_primary", schemadef.TypeString},
	{"accent_color", schemadef.TypeString},
	{"background", schemadef.TypeString},
	{"brand_hex", schemadef.TypeString},
	{"address", schemadef.TypeString},
	{"city", schemadef.TypeString},
	{"state", schemadef.TypeString},
	{"country", schemadef.TypeString},
	{"zip_code", schemadef.TypeString},
	{"status", schemadef.TypeString},
	{"role", schemadef.TypeString},
	{"category", schemadef.TypeString},
	{"slug", schemadef.TypeString},
	{"username", schemadef.TypeString},
	{"password_hash", schemadef.TypeString},
	{"api_key", schemadef.TypeString},
	{"code", schemadef.TypeString},
	{"date_of_birth", schemadef.TypeString},
	{"start_date", schemadef.TypeString},
	{"due_on", schemadef.TypeString},
	{"last4", schemadef.TypeString},
	{"card_last4", schemadef.TypeString},
	{"currency", schemadef.TypeString},
	{"settle_currency", schemadef.TypeString},
	{"notes", schemadef.TypeString},
	{"locale", schemadef.TypeString},
	{"timezone", schemadef.TypeString},
	{"age", schemadef.TypeInt},
	{"quantity", schemadef.TypeInt},
	{"price", schemadef.TypeInt},
	{"amount", schemadef.TypeInt},
	{"price_cents", schemadef.TypeInt},
	{"sort_order", schemadef.TypeInt},
	{"expires_at", schemadef.TypeTime},
}

// neutralName is the same column under a name no English word list contains.
// It is stable per index so a failure names the pair it came from.
func neutralName(i int) string { return "c" + strconv.Itoa(i) + "z" }

// twoNamings returns the SAME table twice: once with every column carrying a
// name a heuristic used to key on, once with those names replaced by neutral
// ones. Declared types, order, and constraints are identical, so any
// difference in what the seeder writes came from the identifier alone.
func twoNamings() (declared, neutral schemadef.Table) {
	mk := func(table string, rename bool) schemadef.Table {
		tb := schemadef.Table{Name: table, PKCols: []string{"id"},
			Columns: []schemadef.Column{col("id", schemadef.TypeString, true, true)}}
		for i, nc := range deletedNameHeuristics {
			name := nc.name
			if rename {
				name = neutralName(i)
			}
			tb.Columns = append(tb.Columns, col(name, nc.typ, true, false))
		}
		return tb
	}
	return mk("declared", false), mk("neutral", true)
}

// A column's NAME may label the value forge invents; it may never CHOOSE it.
//
// The two tables below are identical declarations under two vocabularies. Every
// cell of one must equal the corresponding cell of the other with the column
// name substituted — that substitution is the only role a name is allowed to
// play, and any other difference is forge deciding what a `price_cents` or a
// `last4` column means.
func TestColumnNameLabelsAValueButNeverChoosesIt(t *testing.T) {
	if len(deletedNameHeuristics) == 0 {
		t.Fatal("the heuristic sweep is empty — every assertion below would hold vacuously")
	}
	declared, neutral := twoNamings()
	p := buildOrFail(t, []schemadef.Table{declared, neutral}, Config{Rows: 6, Salt: 5})

	checked := 0
	for i, nc := range deletedNameHeuristics {
		alias := neutralName(i)
		got := cellsFor(p, "neutral", alias)
		want := cellsFor(p, "declared", nc.name)
		if len(got) == 0 || len(want) == 0 {
			t.Fatalf("column %q/%q seeded no cells — the pair proves nothing", nc.name, alias)
		}
		for row := range want {
			expect := strings.ReplaceAll(want[row], nc.name, alias)
			if got[row] != expect {
				t.Errorf("%s (%s) row %d: declared-name column = %s, neutral-name column = %s\n"+
					"    the same declaration under two names produced two different values — "+
					"the seeder read domain meaning off the identifier %q",
					nc.name, nc.typ, row, want[row], got[row], nc.name)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no cells were compared — the sweep derived an empty set, so this test proves nothing")
	}
}

// Every string forge INVENTS carries the emitter's stamp, so a seeded database
// says which values are placeholders and which came from someone who knows the
// domain. Realistic-looking fake data cannot: `["enterprise","free"]` sat in a
// clinic's order line items and passed its CHECK precisely because it looked
// like real data.
func TestSynthesizedStringsCarryTheEmittersStamp(t *testing.T) {
	if SyntheticStringPrefix == "" {
		t.Fatal("the emitter stamps nothing — every assertion below would hold vacuously")
	}
	declared, _ := twoNamings()
	p := buildOrFail(t, []schemadef.Table{declared}, Config{Rows: 4, Salt: 9})

	checked := 0
	for _, nc := range deletedNameHeuristics {
		if nc.typ != schemadef.TypeString {
			continue
		}
		for row, lit := range cellsFor(p, "declared", nc.name) {
			raw, ok := decodeScalarLiteral(lit)
			if !ok {
				t.Fatalf("declared.%s row %d = %s is not a scalar literal", nc.name, row, lit)
			}
			if !strings.HasPrefix(raw, SyntheticStringPrefix) {
				t.Errorf("declared.%s row %d = %q does not carry %q — an invented value that "+
					"reads as real data is indistinguishable from data the app produced",
					nc.name, row, raw, SyntheticStringPrefix)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no synthesized strings were checked — this test proves nothing")
	}
}

// A CHECK that states a PATTERN is a declaration, and forge satisfies it from
// the pattern itself. That is the general answer the name heuristics only
// approximated: they satisfied an email-format CHECK on a column called
// `email` and violated the identical CHECK on a column called `contact` —
// which aborts the whole seed, because it runs in one transaction.
func TestDeclaredPatternIsSatisfiedWhateverTheColumnIsCalled(t *testing.T) {
	const emailRE = `^[^@\s]+@[^@\s]+\.[^@\s]+$`
	patterns := map[string]string{
		"contact":   emailRE,
		"email":     emailRE,
		"reference": `^SKU-[0-9]{4}$`,
		"handle":    `^[a-z][a-z0-9_]{2,15}$`,
		"zone":      `^(north|south)-[0-9]+$`,
	}
	tb := schemadef.Table{Name: "records", PKCols: []string{"id"},
		Columns: []schemadef.Column{col("id", schemadef.TypeString, true, true)}}
	names := make([]string, 0, len(patterns))
	for name := range patterns {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		tb.Columns = append(tb.Columns, col(name, schemadef.TypeString, true, false))
		tb.Checks = append(tb.Checks, schemadef.CheckConstraint{
			Name:    "records_" + name + "_check",
			Def:     "CHECK ((" + name + " ~ '" + patterns[name] + "'::text))",
			Columns: []string{name},
		})
	}
	// Twelve rows, deliberately past nine: the row number is what varies a
	// pattern-derived value, and a two-digit row is where a generator that
	// keeps only one digit starts colliding.
	p := buildOrFail(t, []schemadef.Table{tb}, Config{Rows: 12, Salt: 2})

	checked := 0
	for _, name := range names {
		re := regexp.MustCompile(patterns[name])
		distinct := map[string]bool{}
		cells := cellsFor(p, "records", name)
		for row, lit := range cells {
			raw, ok := decodeScalarLiteral(lit)
			if !ok {
				t.Fatalf("records.%s row %d = %s is not a scalar literal", name, row, lit)
			}
			if !re.MatchString(raw) {
				t.Errorf("records.%s row %d = %q violates the CHECK the schema declares (%s) — "+
					"postgres rejects it and the whole transactional seed rolls back",
					name, row, raw, patterns[name])
			}
			distinct[raw] = true
			checked++
		}
		// Every pattern here has a digit run, so the row number can vary the
		// value without leaving the shape. A pattern-derived value that were
		// constant would make the column unable to carry a UNIQUE index.
		if len(distinct) != len(cells) {
			t.Errorf("records.%s produced %d distinct values across %d rows (%v) — a fixed-width "+
				"digit field must carry the row, or a UNIQUE index caps the table at one row",
				name, len(distinct), len(cells), distinct)
		}
	}
	if checked == 0 {
		t.Fatal("no pattern-constrained cells were checked — this test proves nothing")
	}
}

// A column can declare a pattern AND a length, and both are the author's. The
// value has to satisfy them together: fitting a pattern-derived value to a
// length AFTERWARDS pads it with characters the pattern may not admit, which
// trades one constraint violation for another and aborts the same transaction.
func TestPatternAndLengthAreSatisfiedTogether(t *testing.T) {
	const pattern = `^[a-z0-9-]+$`
	tb := schemadef.Table{
		Name: "assets", PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("handle", schemadef.TypeString, true, false),
		},
		Checks: []schemadef.CheckConstraint{
			{Name: "assets_handle_shape", Def: "CHECK ((handle ~ '" + pattern + "'::text))", Columns: []string{"handle"}},
			{Name: "assets_handle_len", Def: "CHECK ((char_length(handle) >= 12))", Columns: []string{"handle"}},
		},
	}
	p := buildOrFail(t, []schemadef.Table{tb}, Config{Rows: 5, Salt: 8})
	re := regexp.MustCompile(pattern)
	cells := cellsFor(p, "assets", "handle")
	if len(cells) == 0 {
		t.Fatal("assets.handle seeded no cells — this test proves nothing")
	}
	for row, lit := range cells {
		raw, ok := decodeScalarLiteral(lit)
		if !ok {
			t.Fatalf("assets.handle row %d = %s is not a scalar literal", row, lit)
		}
		if !re.MatchString(raw) {
			t.Errorf("assets.handle row %d = %q violates the pattern CHECK", row, raw)
		}
		if len([]rune(raw)) < 12 {
			t.Errorf("assets.handle row %d = %q is %d chars, below the char_length CHECK of 12",
				row, raw, len([]rune(raw)))
		}
	}
}

// Managed timestamps are the one classification the seeder is allowed to act
// on, and it does not own it: schemadef.DetectConventions decides, from the
// same columns pkg/crud and the ORM read. The classification is TYPE-GATED, so
// a `deleted_at` the generator could never stamp is not a soft-delete marker —
// and a seeder that matched the NAME would write NULL into a NOT NULL column
// and take the dataset down with it.
func TestManagedTimestampsFollowDetectConventions(t *testing.T) {
	managed := schemadef.Table{Name: "managed", PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("created_at", schemadef.TypeTime, true, false),
			col("updated_at", schemadef.TypeTime, true, false),
			col("deleted_at", schemadef.TypeTime, false, false),
		}}
	// Same three names, types DetectConventions refuses to manage: an epoch
	// integer nobody can stamp as a time.Time.
	epoch := schemadef.Table{Name: "epoch", PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("created_at", schemadef.TypeInt, true, false),
			col("updated_at", schemadef.TypeInt, true, false),
			col("deleted_at", schemadef.TypeInt, true, false),
		}}
	if c := schemadef.DetectConventions(managed); !c.SoftDelete || !c.Timestamps {
		t.Fatalf("the premise is wrong: DetectConventions(managed) = %+v", c)
	}
	if c := schemadef.DetectConventions(epoch); c.SoftDelete || c.Timestamps {
		t.Fatalf("the premise is wrong: DetectConventions(epoch) = %+v", c)
	}
	p := buildOrFail(t, []schemadef.Table{managed, epoch}, Config{Rows: 4, Salt: 3})

	for row, lit := range cellsFor(p, "managed", "deleted_at") {
		if lit != "NULL" {
			t.Errorf("managed.deleted_at row %d = %s, want NULL — a soft-deleting table seeds live rows", row, lit)
		}
	}
	created := cellsFor(p, "managed", "created_at")
	updated := cellsFor(p, "managed", "updated_at")
	if len(created) == 0 || len(updated) == 0 {
		t.Fatal("the managed timestamp pair seeded no cells — this test proves nothing")
	}
	for row := range created {
		if !(created[row] < updated[row]) {
			t.Errorf("managed row %d: created_at %s is not before updated_at %s — the ORM stamps "+
				"created once and updated on every write", row, created[row], updated[row])
		}
	}
	for _, name := range []string{"created_at", "updated_at", "deleted_at"} {
		cells := cellsFor(p, "epoch", name)
		if len(cells) == 0 {
			t.Fatalf("epoch.%s seeded no cells — this test proves nothing", name)
		}
		for row, lit := range cells {
			if _, err := strconv.ParseInt(lit, 10, 64); err != nil {
				t.Errorf("epoch.%s row %d = %s, want the integer its DECLARED type takes — "+
					"DetectConventions does not manage this column, so its name means nothing",
					name, row, lit)
			}
		}
	}
}

// sortStrings keeps the pattern table's column order deterministic without
// pulling sort into the file's import list for one call.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
