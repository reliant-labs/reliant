package seedplan

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// A column declared `forge:ref authoritative` can reach its parent by MORE
// than one route, and the declaration is a statement about every one of them:
// "this column is the truth; whatever else reaches the same parent agrees with
// it". Honouring it for the first route only, and leaving the second to an
// independent pick, wrote rows whose second route named a parent of another
// tenant — 5 of 7 background_processes rows in one report, 7 of 8
// agent_messages rows in another — while Validate passed.

// twoRouteSchema is the shape of the first report: `jobs.project_id` is the
// truth, and the row reaches `projects` again through `chat_id` (chats.
// project_id) AND through `worktree_id` (worktrees.project_id). Every column is
// NOT NULL, so no route is optional and nothing settles it but the declaration.
func twoRouteSchema(decl string) []schemadef.Table {
	projects := schemadef.Table{
		Name:   "projects",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("name", schemadef.TypeString, true, false),
		},
	}
	scoped := func(name string) schemadef.Table {
		return schemadef.Table{
			Name:   name,
			PKCols: []string{"id"},
			Columns: []schemadef.Column{
				col("id", schemadef.TypeString, true, true),
				col("project_id", schemadef.TypeString, true, false),
			},
			ForeignKeys: []schemadef.ForeignKey{
				{Column: "project_id", RefTable: "projects", RefColumn: "id", Name: name + "_project_id_fkey"},
			},
		}
	}
	jobs := schemadef.Table{
		Name:   "jobs",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("project_id", schemadef.TypeString, true, false),
			col("chat_id", schemadef.TypeString, true, false),
			col("worktree_id", schemadef.TypeString, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{
			{Column: "project_id", RefTable: "projects", RefColumn: "id", Name: "jobs_project_id_fkey", Comment: decl},
			{Column: "chat_id", RefTable: "chats", RefColumn: "id", Name: "jobs_chat_id_fkey"},
			{Column: "worktree_id", RefTable: "worktrees", RefColumn: "id", Name: "jobs_worktree_id_fkey"},
		},
	}
	return []schemadef.Table{jobs, scoped("chats"), scoped("worktrees"), projects}
}

// sameTableRoutesSchema is the shape of the second report: `messages.chat_id`
// is the truth, and BOTH other routes go through the same table — the sending
// and the receiving thread, each of which belongs to a chat.
func sameTableRoutesSchema(decl string) []schemadef.Table {
	chats := schemadef.Table{
		Name:   "chats",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("title", schemadef.TypeString, true, false),
		},
	}
	threads := schemadef.Table{
		Name:   "threads",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("chat_id", schemadef.TypeString, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{
			{Column: "chat_id", RefTable: "chats", RefColumn: "id", Name: "threads_chat_id_fkey"},
		},
	}
	messages := schemadef.Table{
		Name:   "messages",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("chat_id", schemadef.TypeString, true, false),
			col("from_thread_id", schemadef.TypeString, true, false),
			col("to_thread_id", schemadef.TypeString, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{
			{Column: "chat_id", RefTable: "chats", RefColumn: "id", Name: "messages_chat_id_fkey", Comment: decl},
			{Column: "from_thread_id", RefTable: "threads", RefColumn: "id", Name: "messages_from_thread_id_fkey"},
			{Column: "to_thread_id", RefTable: "threads", RefColumn: "id", Name: "messages_to_thread_id_fkey"},
		},
	}
	return []schemadef.Table{messages, threads, chats}
}

// routeDisagreements counts the child rows whose `viaCol` reaches a parent
// (through viaTable.refCol) other than the one `directCol` names, computed from
// the PUBLIC seeded values rather than the resolver's own bookkeeping.
func routeDisagreements(t *testing.T, p *Plan, child, directCol, viaCol, viaTable, refCol string) int {
	t.Helper()
	viaParent := map[string]string{}
	for j := 0; j < p.rowsOf[viaTable]; j++ {
		id, ok1 := p.SeedValue(viaTable, "id", j)
		ref, ok2 := p.SeedValue(viaTable, refCol, j)
		if ok1 && ok2 {
			viaParent[id] = ref
		}
	}
	n := 0
	for i := 0; i < p.rowsOf[child]; i++ {
		direct, ok1 := p.SeedValue(child, directCol, i)
		via, ok2 := p.SeedValue(child, viaCol, i)
		if !ok1 || !ok2 {
			continue
		}
		reached, ok := viaParent[via]
		if !ok {
			t.Fatalf("%s row %d references %s %q, which no seeded row carries", child, i, viaTable, via)
		}
		if direct != reached {
			n++
		}
	}
	return n
}

func TestDiamond_AuthoritativeNarrowsEveryRoute(t *testing.T) {
	type route struct{ viaCol, viaTable, refCol string }
	cases := []struct {
		name      string
		schema    func(string) []schemadef.Table
		child     string
		directCol string
		routes    []route
	}{
		{
			name: "routes through two tables", schema: twoRouteSchema,
			child: "jobs", directCol: "project_id",
			routes: []route{{"chat_id", "chats", "project_id"}, {"worktree_id", "worktrees", "project_id"}},
		},
		{
			name: "two routes through one table", schema: sameTableRoutesSchema,
			child: "messages", directCol: "chat_id",
			routes: []route{{"from_thread_id", "threads", "chat_id"}, {"to_thread_id", "threads", "chat_id"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const rows = 20
			// The fixture must exercise the defect: undeclared, every route
			// disagrees somewhere.
			bare := buildOrFail(t, tc.schema(""), Config{Rows: rows, Salt: 1})
			for _, r := range tc.routes {
				if routeDisagreements(t, bare, tc.child, tc.directCol, r.viaCol, r.viaTable, r.refCol) == 0 {
					t.Fatalf("fixture proves nothing: undeclared, the route through %s already agrees", r.viaCol)
				}
			}

			p := buildOrFail(t, tc.schema("forge:ref authoritative"), Config{Rows: rows, Salt: 1})
			if err := p.Validate(); err != nil {
				t.Fatalf("a declared diamond must not refuse: %v", err)
			}
			for _, r := range tc.routes {
				if n := routeDisagreements(t, p, tc.child, tc.directCol, r.viaCol, r.viaTable, r.refCol); n != 0 {
					t.Errorf("%s.%s is authoritative, but %d of %d row(s) reach another parent through %s",
						tc.child, tc.directCol, n, rows, r.viaCol)
				}
			}
			// Agreement must not come from collapsing the column onto one parent.
			distinct := map[string]bool{}
			for i := 0; i < rows; i++ {
				if v, ok := p.SeedValue(tc.child, tc.directCol, i); ok {
					distinct[v] = true
				}
			}
			if len(distinct) < 2 {
				t.Errorf("%s.%s narrowed to %d distinct value(s) — the dataset lost its variety", tc.child, tc.directCol, len(distinct))
			}
		})
	}
}

// Two declarations that each narrow the same column cannot both be honoured:
// jobs.chat_id is the edge jobs.project_id narrows to agree with it, and it is
// also declared authoritative over the worktree that names a chat. Picking one
// and silently dropping the other is the defect this file pins, so the second
// is refused, naming why.
func TestDiamond_AuthoritativeColumnNarrowedTwiceRefuses(t *testing.T) {
	tables := twoRouteSchema("forge:ref authoritative")
	for i := range tables {
		switch tables[i].Name {
		case "worktrees":
			tables[i].Columns = append(tables[i].Columns, col("chat_id", schemadef.TypeString, true, false))
			tables[i].ForeignKeys = append(tables[i].ForeignKeys,
				schemadef.ForeignKey{Column: "chat_id", RefTable: "chats", RefColumn: "id", Name: "worktrees_chat_id_fkey"})
		case "jobs":
			for k := range tables[i].ForeignKeys {
				if tables[i].ForeignKeys[k].Column == "chat_id" {
					tables[i].ForeignKeys[k].Comment = "forge:ref authoritative"
				}
			}
		}
	}
	p := buildOrFail(t, tables, Config{Rows: 20, Salt: 1})
	if routeDisagreements(t, p, "jobs", "chat_id", "worktree_id", "worktrees", "chat_id") == 0 {
		t.Fatal("fixture proves nothing: the worktree route already agrees with jobs.chat_id")
	}
	err := p.Validate()
	var ude *UndeclaredDiamondError
	if !errors.As(err, &ude) {
		t.Fatalf("a declaration forge cannot honour beside another must refuse, not seed disagreeing rows; got %v", err)
	}
	for _, want := range []string{
		"jobs.chat_id -> chats.id",
		"jobs.worktree_id -> worktrees.chat_id",
		"jobs.chat_id is already narrowed to agree with jobs.project_id",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must contain %q:\n%v", want, err)
		}
	}
	// The declaration that WAS honoured still holds.
	if n := routeDisagreements(t, p, "jobs", "project_id", "chat_id", "chats", "project_id"); n != 0 {
		t.Errorf("jobs.project_id is authoritative, but %d row(s) reach another project through chat_id", n)
	}
}

// A conflict on one route must not leave the other routes partially resolved.
// Here owner_chat_id claims worktree_id first. The project declaration must
// therefore leave even its otherwise satisfiable chat_id route unresolved.
func TestDiamond_AuthoritativeRouteConflictRejectsWholePair(t *testing.T) {
	tables := twoRouteSchema("forge:ref authoritative")
	for i := range tables {
		switch tables[i].Name {
		case "jobs":
			tables[i].Columns = append([]schemadef.Column{col("owner_chat_id", schemadef.TypeString, true, false)}, tables[i].Columns...)
			tables[i].ForeignKeys = append(tables[i].ForeignKeys, schemadef.ForeignKey{
				Column: "owner_chat_id", RefTable: "chats", RefColumn: "id", Comment: "forge:ref authoritative",
			})
		case "worktrees":
			tables[i].Columns = append(tables[i].Columns, col("chat_id", schemadef.TypeString, true, false))
			tables[i].ForeignKeys = append(tables[i].ForeignKeys, schemadef.ForeignKey{
				Column: "chat_id", RefTable: "chats", RefColumn: "id",
			})
			tables[i].ForeignKeys[0].Comment = "forge:ref independent"
		}
	}
	p := buildOrFail(t, tables, Config{Rows: 20, Salt: 1})
	for _, ap := range p.authRefs["jobs"] {
		if ap.directCol == "project_id" {
			t.Fatal("conflicting declaration was partially accepted")
		}
	}
	if n := routeDisagreements(t, p, "jobs", "owner_chat_id", "worktree_id", "worktrees", "chat_id"); n != 0 {
		t.Fatalf("earlier accepted declaration lost %d agreements", n)
	}
	if n := routeDisagreements(t, p, "jobs", "project_id", "chat_id", "chats", "project_id"); n == 0 {
		t.Fatal("fixture must leave the unclaimed project route disagreeing")
	}
	var ude *UndeclaredDiamondError
	if err := p.Validate(); !errors.As(err, &ude) {
		t.Fatalf("an unresolved authoritative pair must refuse: %v", err)
	}
}

// The resolver accepts an authoritative route and then checks it against the
// rows it produced. When the routes share no parent the narrowing has nothing
// to draw from and every cell falls back to the independent pick; that must
// refuse, not pass as declared. Every route reaches parent row 0 through the
// seeder's row-0 spine, so the empty intersection is staged on the plan's
// route groupings directly.
func TestDiamond_AuthoritativeRoutesWithNoCommonParentRefuse(t *testing.T) {
	p := buildOrFail(t, twoRouteSchema("forge:ref authoritative"), Config{Rows: 20, Salt: 1})
	p.bucketMemo = map[string]routeBucketSet{
		"chats|chats.project_id":         {buckets: map[int][]int{0: {0}}, targets: []int{0}},
		"worktrees|worktrees.project_id": {buckets: map[int][]int{1: {0}}, targets: []int{1}},
	}
	err := p.Validate()
	var ude *UndeclaredDiamondError
	if !errors.As(err, &ude) {
		t.Fatalf("routes with no parent in common cannot all agree; Validate must refuse, got %v", err)
	}
	if want := "no row of projects is reachable through every route from jobs.project_id (chat_id, worktree_id)"; !strings.Contains(err.Error(), want) {
		t.Errorf("refusal must say why the declaration cannot hold (%q):\n%v", want, err)
	}
}
