// File: pkg/seedplan/biconditional_test.go
//
// A two-way rule is a valid constraint, and forge seeds it:
//
//	CHECK ((status = 'APPROVED') = (approved_at IS NOT NULL))   -- a biconditional
//	CHECK ((kind = 'workspace') = (daemon_id IS NOT NULL))      -- reliant's connector_grants
//
// biconditional.go reads `(P) = (Q)` as the union of its two truth values,
// `(P AND Q) OR (NOT P AND NOT Q)` (and `(P) <> (Q)` as the xor), so the
// rows forge writes satisfy it by construction and cover BOTH truth values.
// Before, forge refused it and told the author to weaken it to a one-way
// implication, which no seeder should ever ask of a schema.
//
// These tests read the literals the plan actually renders, row by row, and
// evaluate the CHECK against them.

package seedplan

import (
	"os"
	"path/filepath"
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

// bicondTable is estimatesTable plus a second vocabulary column, `kind`, for
// the two-value-atom cases. withStatusVocab false drops status's IN CHECK.
func bicondTable(withStatusVocab bool, checks ...schemadef.CheckConstraint) schemadef.Table {
	t := estimatesTable(checks...)
	t.Columns = append(t.Columns, textCol("kind"))
	t.Checks = append(t.Checks, check("estimates_kind_check",
		"CHECK ((kind = ANY (ARRAY['quote'::text, 'final'::text])))", "kind"))
	if !withStatusVocab {
		t.Checks = t.Checks[1:]
	}
	return t
}

// rowLits renders every column of row i the way the INSERT spells it.
func rowLits(t *testing.T, p *Plan, table string, i int) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tp := range p.tables {
		if tp.table.Name != table {
			continue
		}
		for _, cp := range tp.cols {
			out[cp.col.Name] = p.cellLiteral(tp, cp, i, 0)
		}
	}
	return out
}

// unquoted strips a rendered string literal's quotes and cast.
func unquoted(lit string) string {
	if i := strings.Index(lit, "::"); i > 0 {
		lit = lit[:i]
	}
	return strings.Trim(lit, "'")
}

func TestBiconditional_PlacedOnBothTruthValues(t *testing.T) {
	isNull := func(r map[string]string, c string) bool { return r[c] == "NULL" }
	in := func(r map[string]string, c string, vs ...string) bool {
		for _, v := range vs {
			if unquoted(r[c]) == v {
				return true
			}
		}
		return false
	}
	cases := []struct {
		name  string
		table schemadef.Table
		// p and q evaluate the two sides on a rendered row.
		p, q func(map[string]string) bool
		xor  bool
		// oneSided: only the P-true side can be written, and a warning
		// says so.
		oneSided bool
	}{{
		name:  "lifecycle shape, as postgres renders it",
		table: estimatesTable(approvedIff()),
		p:     func(r map[string]string) bool { return in(r, "status", "APPROVED") },
		q:     func(r map[string]string) bool { return !isNull(r, "approved_at") },
	}, {
		name: "arms swapped",
		table: estimatesTable(check("iff", "CHECK (((approved_at IS NOT NULL) = (status = 'APPROVED'::text)))",
			"status", "approved_at")),
		p: func(r map[string]string) bool { return in(r, "status", "APPROVED") },
		q: func(r map[string]string) bool { return !isNull(r, "approved_at") },
	}, {
		name: "IS NULL polarity",
		table: estimatesTable(check("iff", "CHECK (((status = 'DRAFT'::text) = (approved_at IS NULL)))",
			"status", "approved_at")),
		p: func(r map[string]string) bool { return in(r, "status", "DRAFT") },
		q: func(r map[string]string) bool { return isNull(r, "approved_at") },
	}, {
		name: "IN list on the value side",
		table: estimatesTable(check("iff",
			"CHECK (((status = ANY (ARRAY['SENT'::text, 'APPROVED'::text])) = (sent_at IS NOT NULL)))",
			"status", "sent_at")),
		p: func(r map[string]string) bool { return in(r, "status", "SENT", "APPROVED") },
		q: func(r map[string]string) bool { return !isNull(r, "sent_at") },
	}, {
		name: "negated value side",
		table: estimatesTable(check("iff", "CHECK (((status <> 'DRAFT'::text) = (sent_at IS NOT NULL)))",
			"status", "sent_at")),
		p: func(r map[string]string) bool { return !in(r, "status", "DRAFT") },
		q: func(r map[string]string) bool { return !isNull(r, "sent_at") },
	}, {
		name: "NOT IN, as postgres renders it",
		table: estimatesTable(check("iff",
			"CHECK (((status <> ALL (ARRAY['DRAFT'::text, 'SENT'::text])) = (approved_at IS NOT NULL)))",
			"status", "approved_at")),
		p: func(r map[string]string) bool { return !in(r, "status", "DRAFT", "SENT") },
		q: func(r map[string]string) bool { return !isNull(r, "approved_at") },
	}, {
		name: "xor",
		table: estimatesTable(check("xor", "CHECK (((status = 'DRAFT'::text) <> (approved_at IS NOT NULL)))",
			"status", "approved_at")),
		p:   func(r map[string]string) bool { return in(r, "status", "DRAFT") },
		q:   func(r map[string]string) bool { return !isNull(r, "approved_at") },
		xor: true,
	}, {
		name: "two presence sides",
		table: estimatesTable(check("iff", "CHECK (((approved_at IS NULL) = (sent_at IS NULL)))",
			"approved_at", "sent_at")),
		p: func(r map[string]string) bool { return isNull(r, "approved_at") },
		q: func(r map[string]string) bool { return isNull(r, "sent_at") },
	}, {
		name: "two value sides, both with a vocabulary",
		table: bicondTable(true, check("iff", "CHECK (((status = 'APPROVED'::text) = (kind = 'final'::text)))",
			"status", "kind")),
		p: func(r map[string]string) bool { return in(r, "status", "APPROVED") },
		q: func(r map[string]string) bool { return in(r, "kind", "final") },
	}, {
		name:  "beside a one-way guard over the same column",
		table: estimatesTable(approvedIff(), sentImplication()),
		p:     func(r map[string]string) bool { return in(r, "status", "APPROVED") },
		q:     func(r map[string]string) bool { return !isNull(r, "approved_at") },
	}, {
		name:     "a value side with no vocabulary seeds only the side it can write",
		table:    bicondTable(false, approvedIff()),
		p:        func(r map[string]string) bool { return in(r, "status", "APPROVED") },
		q:        func(r map[string]string) bool { return !isNull(r, "approved_at") },
		oneSided: true,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const rows = 12
			p := planFor(t, tc.table, rows)
			joined := strings.Join(p.Warnings(), "\n")
			switch {
			case tc.oneSided && !strings.Contains(joined, "seeded on only one side"):
				t.Errorf("a one-sided placement must say so; warnings:\n%s", joined)
			case !tc.oneSided && joined != "":
				t.Errorf("a placeable biconditional must plan without warnings; got:\n%s", joined)
			}
			if strings.Contains(joined, "one-way implication") {
				t.Errorf("forge must not tell the author to weaken a constraint it places; got:\n%s", joined)
			}
			seen := map[bool]bool{}
			for i := 0; i < rows; i++ {
				r := rowLits(t, p, "estimates", i)
				pv, qv := tc.p(r), tc.q(r)
				if (pv != qv) != tc.xor {
					t.Errorf("row %d violates the constraint (P=%v Q=%v xor=%v): %v", i, pv, qv, tc.xor, r)
				}
				seen[pv] = true
			}
			if tc.oneSided {
				if seen[false] {
					t.Errorf("a one-sided placement wrote a row on the side it cannot prove")
				}
				return
			}
			if !seen[true] || !seen[false] {
				t.Errorf("rows cover only one truth value of the constraint: %v", seen)
			}
		})
	}
}

// The reliant shape: the presence side is a NULLABLE FOREIGN KEY, so the
// false side is a declined edge, and the discriminator also carries two
// one-way guards whose consequents are non-blank requirements
// (`length(TRIM(BOTH FROM col)) > 0`). All three merge into one union over
// `kind`.
func TestBiconditional_ConnectorGrantsShape(t *testing.T) {
	daemons := schemadef.Table{Name: "daemons", PKCols: []string{"id"}, Columns: []schemadef.Column{idCol()}}
	grants := schemadef.Table{
		Name:   "connector_grants",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(),
			{Name: "daemon_id", DeclType: "TEXT", Type: schemadef.TypeString},
			{Name: "kind", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true, Default: "'workspace'::text"},
			{Name: "org_id", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true, Default: "''::text"},
			{Name: "path_root", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true},
		},
		ForeignKeys: []schemadef.ForeignKey{{Column: "daemon_id", RefTable: "daemons", RefColumn: "id"}},
		Checks: []schemadef.CheckConstraint{
			check("connector_grants_kind_valid",
				"CHECK ((kind = ANY (ARRAY['workspace'::text, 'forge'::text]))) NOT VALID", "kind"),
			check("connector_grants_forge_org",
				"CHECK (((kind <> 'forge'::text) OR (length(TRIM(BOTH FROM org_id)) > 0))) NOT VALID", "kind", "org_id"),
			check("connector_grants_daemon_by_kind",
				"CHECK (((kind = 'workspace'::text) = (daemon_id IS NOT NULL))) NOT VALID", "kind", "daemon_id"),
			check("connector_grants_path_root_not_empty",
				"CHECK (((kind <> 'workspace'::text) OR (length(TRIM(BOTH FROM path_root)) > 0))) NOT VALID", "kind", "path_root"),
		},
	}
	tables := []schemadef.Table{daemons, grants}
	for _, minimal := range []bool{false, true} {
		p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 10, Salt: 3, Minimal: minimal})
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}
		if w := strings.Join(p.Warnings(), "\n"); strings.Contains(w, "connector_grants") {
			t.Errorf("minimal=%v: connector_grants constraints are warned about:\n%s", minimal, w)
		}
		kinds := map[string]bool{}
		for i := 0; i < p.rowsOf["connector_grants"]; i++ {
			r := rowLits(t, p, "connector_grants", i)
			kind := unquoted(r["kind"])
			if minimal && !p.Writes("connector_grants", "kind") {
				kind = "workspace" // the DEFAULT
			}
			kinds[kind] = true
			hasDaemon := p.Writes("connector_grants", "daemon_id") && r["daemon_id"] != "NULL"
			if (kind == "workspace") != hasDaemon {
				t.Errorf("minimal=%v row %d: kind=%s daemon_id=%s violates connector_grants_daemon_by_kind", minimal, i, kind, r["daemon_id"])
			}
			if kind == "forge" && (!p.Writes("connector_grants", "org_id") || strings.TrimSpace(unquoted(r["org_id"])) == "") {
				t.Errorf("minimal=%v row %d: a forge grant with a blank org_id (%s)", minimal, i, r["org_id"])
			}
		}
		if !minimal && (!kinds["workspace"] || !kinds["forge"]) {
			t.Errorf("rows cover only %v; both kinds must appear", kinds)
		}
	}
}

// reliant's model_endpoints, found by the e2e seed: the project had pinned
// `daemon_id: [null]` and `route: [direct]` in vocab.yaml to dodge the
// refusal. Once forge places the biconditional, a via_daemon row must still
// get a daemon_id — an overlay NULL on a cell the branch requires present is
// replaced by a usable value, never written.
func TestBiconditional_OverlayNullOnARequiredCell(t *testing.T) {
	endpoints := schemadef.Table{
		Name:   "model_endpoints",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(),
			textCol("route"),
			{Name: "daemon_id", DeclType: "TEXT", Type: schemadef.TypeString},
		},
		Checks: []schemadef.CheckConstraint{
			check("model_endpoints_route_check",
				"CHECK ((route = ANY (ARRAY['direct'::text, 'via_daemon'::text])))", "route"),
			check("model_endpoints_check",
				"CHECK (((route = 'via_daemon'::text) = (daemon_id IS NOT NULL)))", "daemon_id", "route"),
		},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "vocab.yaml")
	if err := os.WriteFile(path, []byte("columns:\n  model_endpoints.route: [direct]\n  model_endpoints.daemon_id: [null]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := LoadVocab(path)
	if err != nil {
		t.Fatalf("LoadVocab: %v", err)
	}
	tables := []schemadef.Table{endpoints}
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 6, Salt: 1})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.ApplyVocab(v)
	routes := map[string]bool{}
	for i := 0; i < 6; i++ {
		r := rowLits(t, p, "model_endpoints", i)
		route := unquoted(r["route"])
		routes[route] = true
		if (route == "via_daemon") != (r["daemon_id"] != "NULL") {
			t.Errorf("row %d: route=%s daemon_id=%s violates model_endpoints_check", i, route, r["daemon_id"])
		}
	}
	if !routes["direct"] || !routes["via_daemon"] {
		t.Errorf("rows cover only %v", routes)
	}
}

func TestUnionNonBlankUsesOnlyValidVocabularyMembers(t *testing.T) {
	tab := schemadef.Table{
		Name: "grants", PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), textCol("kind"), {Name: "label", DeclType: "TEXT", Type: schemadef.TypeString},
		},
		Checks: []schemadef.CheckConstraint{
			check("kind_pool", `CHECK (kind = ANY (ARRAY['workspace'::text, ''::text]))`, "kind"),
			check("label_shape", `CHECK (((kind = 'workspace') AND (length(kind) > 0) AND (length(label) > 0)) OR ((kind = '') AND (label IS NULL)))`, "kind", "label"),
		},
	}
	for _, tc := range []struct {
		name, pool string
		place      bool
	}{
		{"blank included with usable member", `''::text, 'ready'::text`, true},
		{"all blank", `''::text, ' '::text`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tb := tab
			tb.Checks = append(append([]schemadef.CheckConstraint{}, tab.Checks...), check("label_pool", "CHECK (label = ANY (ARRAY["+tc.pool+"]))", "label"))
			p := planFor(t, tb, 8)
			p.ApplyVocab(&Vocab{Columns: map[string][]string{"grants.label": {vocabNull}}})
			if !tc.place {
				if len(p.unions[tb.Name]) != 0 || !strings.Contains(strings.Join(p.Warnings(), "\n"), "declared values are all blank") {
					t.Fatalf("all-blank pool was not refused with a warning: unions=%v, warnings=%v", p.unions[tb.Name], p.Warnings())
				}
				if lit, ok := p.presentLiteral(tb.Name, tb.Columns[2], 0, true); ok || lit != "" {
					t.Fatalf("presentLiteral returned a usable draw from an all-blank pool: (%q, %t)", lit, ok)
				}
				return
			}
			seen := map[string]bool{}
			for row := range 8 {
				lits := rowLits(t, p, tb.Name, row)
				kind, label := lits["kind"], lits["label"]
				seen[kind] = true
				if kind == "'workspace'" && label != "'ready'" || kind == "''" && label != "NULL" {
					t.Fatalf("row %d: invalid kind/label combination %s %s", row, kind, label)
				}
			}
			if !seen["'workspace'"] || !seen["''"] {
				t.Fatalf("did not exercise both branches: %v, warnings=%v", seen, p.Warnings())
			}
		})
	}
}

// What stays refused: a side outside the grammar keeps the ordering pass's
// refusal, unchanged.
func TestBiconditional_OutsideTheGrammarKeepsTheRefusal(t *testing.T) {
	table := estimatesTable(check("estimates_cmp_iff",
		"CHECK (((char_length(status) > 3) = (approved_at IS NULL)))", "status", "approved_at"))
	joined := strings.Join(planFor(t, table, 6).Warnings(), "\n")
	if !strings.Contains(joined, "estimates_cmp_iff") || !strings.Contains(joined, "not a two-column ordering comparison") {
		t.Errorf("an unreadable biconditional must keep the existing refusal; got:\n%s", joined)
	}
}

func TestParseBiconditional(t *testing.T) {
	cases := []struct {
		def    string
		wantOK bool
		xor    bool
	}{
		{"CHECK (((kind = 'workspace'::text) = (daemon_id IS NOT NULL))) NOT VALID", true, false},
		{"CHECK (((kind = 'workspace'::text) <> (daemon_id IS NULL)))", true, true},
		{"CHECK (((kind = ANY (ARRAY['a'::text, 'b'::text])) = (x IS NULL)))", true, false},
		{"CHECK (((a IS NULL) = (b IS NULL)))", true, false},
		{"CHECK (((a = 1) = (b = 'x'::text)))", true, false},
		{"CHECK (((a IS NULL) = (a IS NOT NULL)))", false, false}, // one column
		{"CHECK ((amount_paid_cents = amount_cents))", false, false},
		{"CHECK ((starts_at <= ends_at))", false, false},
		{"CHECK (((status <> 'SENT'::text) OR (sent_at IS NOT NULL)))", false, false},
		{"CHECK (((a > 0) = (b IS NULL)))", false, false},
	}
	for _, tc := range cases {
		body, _ := checkBody(tc.def)
		bc, ok := parseBiconditional(body)
		if ok != tc.wantOK {
			t.Errorf("parseBiconditional(%q) ok = %v, want %v", tc.def, ok, tc.wantOK)
			continue
		}
		if ok && bc.xor != tc.xor {
			t.Errorf("parseBiconditional(%q) xor = %v, want %v", tc.def, bc.xor, tc.xor)
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
