package seedplan

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// twoDiamondSchema carries the orders/prescriptions/patients diamond plus a
// SECOND one on a different table, whose two routes cannot disagree because
// the parent has exactly one reachable row per child (the indirect route and
// the direct column resolve through the same single-row table).
//
// The point is a schema where one diamond refuses and another does not, which
// is the everyday shape on a real domain: several diamonds, of which only the
// ones whose random values happen to collide produce a stanza.
func twoDiamondSchema() []schemadef.Table {
	base := diamondSchema("")

	// clinics: a second parent, reached by refills directly and through
	// prescriptions.
	clinics := schemadef.Table{
		Name:   "clinics",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("name", schemadef.TypeString, true, false),
		},
	}
	// prescriptions gains a clinic_id so refills can reach clinics two ways.
	for i := range base {
		if base[i].Name != "prescriptions" {
			continue
		}
		base[i].Columns = append(base[i].Columns, col("clinic_id", schemadef.TypeString, true, false))
		base[i].ForeignKeys = append(base[i].ForeignKeys, schemadef.ForeignKey{
			Column: "clinic_id", RefTable: "clinics", RefColumn: "id",
			Name: "prescriptions_clinic_id_fkey",
		})
	}
	refills := schemadef.Table{
		Name:   "refills",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("clinic_id", schemadef.TypeString, true, false),
			col("prescription_id", schemadef.TypeString, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{
			{Column: "clinic_id", RefTable: "clinics", RefColumn: "id", Name: "refills_clinic_id_fkey"},
			{Column: "prescription_id", RefTable: "prescriptions", RefColumn: "id", Name: "refills_prescription_id_fkey"},
		},
	}
	return append(base, clinics, refills)
}

// TestDiamond_RefusalNamesTheQuietOnesToo pins the batching fix.
//
// A refusal reports the pairs that ACTUALLY disagree in the rows it planned,
// which is correct — a pair that cannot disagree is not a decision worth
// nagging about. But it meant a schema with several diamonds surfaced them a
// few at a time, over successive runs, as different random values collided:
// declare the reported one, re-run, discover the next. On a domain with a
// real foreign-key web that is several boot cycles, each one a full
// down/migrate/generate/up.
//
// One refusal should therefore name the whole decision set: the diamonds it
// is refusing on, and the ones that are undeclared but happened to agree.
func TestDiamond_RefusalNamesTheQuietOnesToo(t *testing.T) {
	// rows=3 / salt=6 is chosen because it produces exactly the mixed case
	// this test is about: one diamond whose routes disagree (a stanza) and
	// one whose routes happen to agree at these values (previously dropped
	// on the floor). At larger row counts both disagree and the quiet path
	// is never exercised — which is precisely how the gap survived.
	p := buildOrFail(t, twoDiamondSchema(), Config{Rows: 3, Salt: 6})

	err := p.Validate()
	if err == nil {
		t.Fatal("a schema with an undeclared, disagreeing diamond must refuse")
	}
	var refusal *UndeclaredDiamondError
	if !errors.As(err, &refusal) {
		t.Fatalf("want *UndeclaredDiamondError, got %T", err)
	}
	msg := err.Error()

	if len(refusal.Stanzas) == 0 {
		t.Fatalf("fixture is not exercising the mixed case: no diamond disagreed:\n%s", msg)
	}
	if len(refusal.Quiet) == 0 {
		t.Fatalf("fixture is not exercising the mixed case: every diamond disagreed, "+
			"so the quiet path never ran:\n%s", msg)
	}

	// The quiet one is named rather than dropped, so the reader can declare
	// the whole set in one migration instead of discovering it next boot.
	for _, q := range refusal.Quiet {
		if !strings.Contains(msg, q) {
			t.Errorf("quiet reference %q missing from the rendered message:\n%s", q, msg)
		}
	}
	if !strings.Contains(msg, "a later run WILL refuse") {
		t.Errorf("the quiet list must say why it is worth acting on now:\n%s", msg)
	}
	// The disagreeing one keeps its full paste-ready stanza.
	if !strings.Contains(msg, "Declare which is authoritative (pick ONE):") {
		t.Errorf("the refusing diamond must keep its runbook stanza:\n%s", msg)
	}
}

// TestDiamond_NoQuietNoiseWhenEverythingDisagrees keeps the addition from
// becoming noise: with a single diamond there is nothing extra to say, and
// the message must not grow a dangling empty section.
func TestDiamond_NoQuietNoiseWhenEverythingDisagrees(t *testing.T) {
	p := buildOrFail(t, diamondSchema(""), Config{Rows: 20, Salt: 1})

	err := p.Validate()
	if err == nil {
		t.Fatal("undeclared diamond must refuse")
	}
	var refusal *UndeclaredDiamondError
	if !errors.As(err, &refusal) {
		t.Fatalf("want *UndeclaredDiamondError, got %T", err)
	}
	if len(refusal.Quiet) != 0 {
		t.Errorf("no quiet references expected in a single-diamond schema; got %v", refusal.Quiet)
	}
	if strings.Contains(err.Error(), "Also undeclared") {
		t.Errorf("the quiet section must be omitted entirely when empty:\n%s", err)
	}
}
