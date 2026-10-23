package seedplan

import (
	"errors"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// scopedDiamondSchema is the shape that carries no decision: `events` reaches
// `orgs` directly through a NOT NULL column, and again through a NULLABLE
// reference to `projects`. The nullable edge is optional by declaration — the
// schema already says a row may exist without it — so it cannot be the fact
// that settles which org the row belongs to. `decl` is the comment on the
// direct constraint.
func scopedDiamondSchema(decl string, viaNotNull bool) []schemadef.Table {
	orgs := schemadef.Table{
		Name:   "orgs",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("name", schemadef.TypeString, true, false),
		},
	}
	projects := schemadef.Table{
		Name:   "projects",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("org_id", schemadef.TypeString, true, false),
			col("title", schemadef.TypeString, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{
			{Column: "org_id", RefTable: "orgs", RefColumn: "id", Name: "projects_org_id_fkey"},
		},
	}
	events := schemadef.Table{
		Name:   "events",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("org_id", schemadef.TypeString, true, false),
			col("project_id", schemadef.TypeString, viaNotNull, false),
			col("kind", schemadef.TypeString, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{
			{Column: "org_id", RefTable: "orgs", RefColumn: "id", Name: "events_org_id_fkey", Comment: decl},
			{Column: "project_id", RefTable: "projects", RefColumn: "id", Name: "events_project_id_fkey"},
		},
	}
	return []schemadef.Table{events, projects, orgs}
}

// A NOT NULL direct edge against an OPTIONAL indirect route is not a question:
// the row must name its parent, and the route that might settle it instead is
// one the schema already permits to be absent. Auto-resolving it removes the
// bulk of the refusals a wide schema produces without forge choosing between
// two things a domain could reasonably mean.
func TestDiamond_RequiredDirectVsOptionalRoute_AutoResolves(t *testing.T) {
	p, err := BuildPlan(scopedDiamondSchema("", false), nil, Config{Rows: 5, Salt: 7})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var ude *UndeclaredDiamondError
	if errors.As(p.Validate(), &ude) {
		t.Fatalf("refused a diamond whose indirect route is optional — nothing here is a domain decision:\n%v", ude)
	}

	// Auto-resolution must mean the direct column IS the truth, so the seeded
	// rows have to agree through both routes. A resolution that still writes
	// disagreeing rows would be worse than the refusal it replaced.
	if n := countEventOrgDisagreements(t, p, 5); n != 0 {
		t.Errorf("%d seeded row(s) disagree about their org after auto-resolution — the direct column must win", n)
	}
}

// countEventOrgDisagreements is countDisagreeing for the events/projects/orgs
// schema: it walks the seeded values the public API exposes, so it can only
// agree with the resolver by the rows actually being consistent.
func countEventOrgDisagreements(t *testing.T, p *Plan, rows int) int {
	t.Helper()
	projectOrg := map[string]string{}
	for j := range rows {
		id, ok1 := p.SeedValue("projects", "id", j)
		org, ok2 := p.SeedValue("projects", "org_id", j)
		if ok1 && ok2 {
			projectOrg[id] = org
		}
	}
	n := 0
	for i := range rows {
		direct, ok1 := p.SeedValue("events", "org_id", i)
		projectID, ok2 := p.SeedValue("events", "project_id", i)
		if !ok1 || !ok2 {
			continue
		}
		via, ok := projectOrg[projectID]
		if !ok {
			t.Fatalf("events row %d references project %q, which no seeded row carries", i, projectID)
		}
		if direct != via {
			n++
		}
	}
	return n
}

// The author always outranks the heuristic. `independent` is the declaration
// that most visibly contradicts auto-resolution: it says the two facts are
// unrelated and must NOT be reconciled.
func TestDiamond_ExplicitDeclarationBeatsAutoResolution(t *testing.T) {
	p, err := BuildPlan(scopedDiamondSchema("forge:ref independent", false), nil, Config{Rows: 5, Salt: 7})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("an explicit declaration must be honoured, not refused: %v", err)
	}
	// `independent` means each edge is picked on its own, exactly as it was
	// before auto-resolution existed. If the rows agree anyway the declaration
	// was ignored and the heuristic silently overrode the author.
	n := countEventOrgDisagreements(t, p, 5)
	if n == 0 {
		t.Error("declared independent, but every row agrees — the heuristic overrode the declaration")
	}
}

// The load-bearing negative. When BOTH routes are required, the schema really
// does state the same fact twice and forge cannot know which one the domain
// means — that is the case this whole subsystem exists for, and it must still
// refuse. A change that made this test pass by seeding anyway would have
// disabled the feature rather than narrowed it.
func TestDiamond_BothRoutesRequired_StillRefuses(t *testing.T) {
	p, err := BuildPlan(scopedDiamondSchema("", true), nil, Config{Rows: 5, Salt: 7})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var ude *UndeclaredDiamondError
	if !errors.As(p.Validate(), &ude) {
		t.Fatal("two required routes to one parent is a genuine domain decision — forge must still refuse to guess it")
	}
}

// The canonical orders/prescriptions diamond has two NOT NULL routes, so it is
// the genuine-ambiguity case and must be untouched by auto-resolution. Pinned
// separately from the tests above because it is the schema the rest of this
// file's cases are written against.
func TestDiamond_CanonicalSchemaStillRefuses(t *testing.T) {
	p, err := BuildPlan(diamondSchema(""), nil, Config{Rows: 5, Salt: 7})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var ude *UndeclaredDiamondError
	if !errors.As(p.Validate(), &ude) {
		t.Fatal("the canonical two-required-route diamond must still refuse")
	}
}
