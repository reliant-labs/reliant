// File: pkg/seedplan/biconditional_test.go
//
// A status-lifecycle invariant has two spellings, and forge places only one of
// them:
//
//	CHECK (status <> 'APPROVED' OR approved_at IS NOT NULL)     -- an implication, placeable
//	CHECK ((status = 'APPROVED') = (approved_at IS NOT NULL))   -- a biconditional, not
//
// guard.go's grammar (guard.go:60-71) admits only the negative arm, so
// tableGuardSpecs merges any number of implications over one column into a
// single union. A biconditional has no top-level OR at all, so no pass reads
// it.
//
// Two things about the failure are not obvious, and both were measured rather
// than reasoned (see lifecycle_form_test.go, and the probe run recorded in the
// task report):
//
//  1. A biconditional does not reach union.go's joint-satisfiability refusal.
//     It falls through to the ORDERING pass and is refused with "not a
//     two-column ordering comparison" — a message about ordering, for a
//     constraint that has nothing to do with ordering. That is the message an
//     author actually sees, and it points nowhere near the real problem.
//
//  2. One biconditional takes its WELL-FORMED NEIGHBOURS down with it. It
//     still spans `status`, so the merged guard union hits the rivalry
//     refusal at union.go:464 and every implication over that column goes
//     unplaced too. This is why the first dogfood run's incremental repairs
//     looked like no progress at all: fixing two of three constraints yields
//     the identical failure, so the author concluded the rewrite did not work
//     and began deleting constraints instead.
//
// These tests pin that both messages name the shape and the rewrite.

package seedplan

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// estimatesTable is the measured shape: a lifecycle column with a readable
// vocabulary, plus whatever constraints a case adds.
func estimatesTable(checks ...schemadef.CheckConstraint) schemadef.Table {
	return schemadef.Table{
		Name:   "estimates",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(),
			textCol("status"),
			{Name: "approved_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime},
			{Name: "sent_at", DeclType: "TIMESTAMPTZ", Type: schemadef.TypeTime},
		},
		Checks: append([]schemadef.CheckConstraint{
			check("estimates_status_check",
				"CHECK ((status = ANY (ARRAY['DRAFT'::text, 'SENT'::text, 'APPROVED'::text])))",
				"status"),
		}, checks...),
	}
}

// approvedIff is the biconditional, in the spelling pg_get_constraintdef
// actually returns (verified by introspecting a real postgres).
func approvedIff() schemadef.CheckConstraint {
	return check("estimates_approved_iff",
		"CHECK (((status = 'APPROVED'::text) = (approved_at IS NOT NULL)))",
		"status", "approved_at")
}

// sentImplication is a well-formed one-way guard over the same column.
func sentImplication() schemadef.CheckConstraint {
	return check("estimates_sent_has_stamp",
		"CHECK (((status <> 'SENT'::text) OR (sent_at IS NOT NULL)))",
		"status", "sent_at")
}

// The message an author with a lone biconditional actually receives. Today it
// is the ordering pass's "not a two-column ordering comparison", which
// describes forge's parser rather than the author's mistake.
func TestBiconditional_OrderingRefusalNamesTheRewrite(t *testing.T) {
	joined := strings.Join(planFor(t, estimatesTable(approvedIff()), 6).Warnings(), "\n")

	for _, want := range []string{
		"estimates_approved_iff",
		// Name the shape, so the author can recognize it in the migration.
		"biconditional",
		// Name the fix as a concept AND as pasteable SQL.
		"one-way implication",
		"status <> 'APPROVED' OR approved_at IS NOT NULL",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the biconditional refusal must contain %q; got:\n%s", want, joined)
		}
	}

	// The old text described the wrong subsystem. A constraint that never
	// mentions an ordering must not be reported as a failed ordering.
	if strings.Contains(joined, "not a two-column ordering comparison") {
		t.Errorf("a biconditional must no longer be reported as a failed ordering comparison; got:\n%s", joined)
	}
}

// The second precondition, and the difference between good advice and a worse
// dead end. Rewriting to an implication only helps if the guarded column
// carries a readable vocabulary — guard.go:67-71 refuses otherwise, because
// with no `IN (...)` CHECK there is no set of values to draw the non-excluded
// branch from. A scaffolded enum gets this free; a hand-written TEXT column
// does not. Omit it and the author follows the advice and is refused again.
func TestBiconditional_RefusalNamesTheVocabularyPrecondition(t *testing.T) {
	joined := strings.Join(planFor(t, estimatesTable(approvedIff()), 6).Warnings(), "\n")
	if !strings.Contains(joined, "CHECK (status IN") {
		t.Errorf("the rewrite advice must state the vocabulary precondition on the guarded column; got:\n%s", joined)
	}
}

// The expensive case: one biconditional blocking otherwise-placeable
// implications over the same column. The refusal must say that the neighbours
// are collateral damage, because an author who cannot see that concludes the
// rewrite does not work.
func TestBiconditional_BlockedNeighboursAreNamed(t *testing.T) {
	table := estimatesTable(approvedIff(), sentImplication())
	joined := strings.Join(planFor(t, table, 6).Warnings(), "\n")

	for _, want := range []string{
		// The blocked neighbour, by name.
		"estimates_sent_has_stamp",
		// The blocker, by name.
		"estimates_approved_iff",
		// And the causal claim, so the author fixes ONE constraint rather
		// than weakening three.
		"one-way implication",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the blocked-guard refusal must contain %q; got:\n%s", want, joined)
		}
	}
}

// A rendering bug found while probing: the guard refusal builds its label with
// an already-quoted %q, producing `"a\", \"b"` in the user's terminal. Several
// constraint names must read as a plain quoted list.
func TestGuardRefusal_MultipleConstraintNamesRenderCleanly(t *testing.T) {
	table := estimatesTable(
		approvedIff(),
		sentImplication(),
		check("estimates_draft_has_no_stamp",
			"CHECK (((status <> 'DRAFT'::text) OR (approved_at IS NULL)))",
			"status", "approved_at"),
	)
	joined := strings.Join(planFor(t, table, 6).Warnings(), "\n")

	if strings.Contains(joined, `\"`) {
		t.Errorf("constraint-name lists must not contain escaped quotes; got:\n%s", joined)
	}
}

// The guard against wrong advice. A rival that is NOT a biconditional must
// keep the original refusal and must NOT be told to rewrite as an
// implication — that sends the author to rewrite a constraint the rewrite
// does not apply to.
func TestNonBiconditional_KeepsTheOriginalRefusal(t *testing.T) {
	table := estimatesTable(check("estimates_exclusive",
		"CHECK ((num_nonnulls(status, approved_at) = 1))",
		"status", "approved_at"))

	joined := strings.Join(planFor(t, table, 6).Warnings(), "\n")

	if !strings.Contains(joined, "estimates_exclusive") ||
		!strings.Contains(joined, "not a two-column ordering comparison") {
		t.Errorf("a non-biconditional must keep the existing refusal naming it; got:\n%s", joined)
	}
	if strings.Contains(joined, "one-way implication") {
		t.Errorf("a non-biconditional must NOT be told to rewrite as an implication; got:\n%s", joined)
	}
}

// The shape detector itself: both orderings of the arms, both NULL
// polarities, and the shapes that must NOT match.
func TestBiconditionalShape_Detection(t *testing.T) {
	cases := []struct {
		name    string
		def     string
		wantOK  bool
		wantSQL string
	}{{
		name:    "discriminator first, as postgres renders it",
		def:     "CHECK (((status = 'APPROVED'::text) = (approved_at IS NOT NULL)))",
		wantOK:  true,
		wantSQL: "status <> 'APPROVED' OR approved_at IS NOT NULL",
	}, {
		name:    "arms swapped — the same rule, written the other way round",
		def:     "CHECK (((approved_at IS NOT NULL) = (status = 'APPROVED'::text)))",
		wantOK:  true,
		wantSQL: "status <> 'APPROVED' OR approved_at IS NOT NULL",
	}, {
		name:    "IS NULL polarity — the complement, and a different rewrite",
		def:     "CHECK (((status = 'DRAFT'::text) = (approved_at IS NULL)))",
		wantOK:  true,
		wantSQL: "status <> 'DRAFT' OR approved_at IS NULL",
	}, {
		name:   "a one-way implication is NOT a biconditional",
		def:    "CHECK (((status <> 'SENT'::text) OR (sent_at IS NOT NULL)))",
		wantOK: false,
	}, {
		name:   "a function call over both columns is not this shape",
		def:    "CHECK ((num_nonnulls(status, approved_at) = 1))",
		wantOK: false,
	}, {
		name:   "a plain two-column comparison is not this shape",
		def:    "CHECK ((starts_at < ends_at))",
		wantOK: false,
	}, {
		name:   "equality between two plain columns is not this shape",
		def:    "CHECK ((amount_paid_cents = amount_cents))",
		wantOK: false,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := biconditionalRewrite(tc.def)
			if ok != tc.wantOK {
				t.Fatalf("biconditionalRewrite(%q) ok = %v, want %v (got %q)", tc.def, ok, tc.wantOK, got)
			}
			if ok && got != tc.wantSQL {
				t.Errorf("biconditionalRewrite(%q) = %q, want %q", tc.def, got, tc.wantSQL)
			}
		})
	}
}
