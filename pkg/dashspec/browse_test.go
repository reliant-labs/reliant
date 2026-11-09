package dashspec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// withFilters is onePanel on a dashboard declaring a dropdown filter "plan"
// and a toggle "internal", whose values a browse filter may reference.
func withFilters(fields string) string {
	return `{"id":"x","title":"X","filters":[` +
		`{"id":"plan","label":"Plan","field":"resource.attr.plan"},` +
		`{"id":"internal","kind":"toggle","label":"Internal"}],` +
		`"panels":[{"id":"p","title":"P",` + fields + `}]}`
}

// expectProblem fails unless err carries a problem at panels[0].<path> whose
// message contains want.
func expectProblem(t *testing.T, err error, path, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("validated; want %q at %q", want, path)
	}
	full := "panels[0]"
	if path != "" {
		full += "." + path
	}
	for _, p := range ProblemsOf(err) {
		if p.Path == full && strings.Contains(p.Message, want) {
			if p.Line == 0 || p.Col == 0 {
				t.Errorf("problem at %s is not located: %+v", full, p)
			}
			return
		}
	}
	t.Errorf("no problem %q at %s; got:\n%v", want, full, err)
}

func TestBrowsePanelIsAConsoleKind(t *testing.T) {
	if !IsConsoleKind(KindBrowse) {
		t.Fatal("a browse panel must be skipped by the HyperDX export")
	}
}

const usersLink = `"link":{"database":"app","table":"public.users","column":"id","fromField":"span.attr.user_id"}`

func TestValidationAcceptsLinks(t *testing.T) {
	ok := []struct{ name, src string }{
		{"bar filtering by a group", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.attr.user_id"],"series":[{"label":"n","agg":"count"}],` + usersLink)},
		{"table filtering by a group", onePanel(`"kind":"table","signal":"logs","groupBy":["service.name","log.attr.user_id"],"series":[{"label":"n","agg":"count"}],"link":{"database":"app","table":"public.users","column":"id","fromField":"log.attr.user_id"}`)},
		{"table opening a row", onePanel(`"kind":"table","signal":"traces","groupBy":["span.attr.org","span.attr.user"],"series":[{"label":"n","agg":"count"}],"link":{"database":"app","table":"public.members","row":true,"fromFields":["span.attr.org","span.attr.user"]}`)},
		{"postgres drawn auto", onePanel(`"kind":"postgres","postgres":{"database":"app","sql":"SELECT id, email FROM users"},"link":{"database":"app","table":"public.users","row":true,"fromFields":["id"]}`)},
		{"postgres drawn as a table", onePanel(`"kind":"postgres","postgres":{"database":"app","draw":"table","sql":"SELECT 1"},"link":{"database":"reliant","table":"public.chats","column":"user_id","fromField":"user_id"}`)},
		{"postgres drawn as bars", onePanel(`"kind":"postgres","postgres":{"database":"app","draw":"bar","group":["plan"],"sql":"SELECT plan, count(*) FROM users GROUP BY 1"},"link":{"database":"app","table":"public.users","column":"plan","fromField":"plan"}`)},
		{"row false is a filter link", onePanel(`"kind":"bar","signal":"traces","groupBy":["span.attr.user_id"],"series":[{"label":"n","agg":"count"}],"link":{"database":"app","table":"public.users","column":"id","fromField":"span.attr.user_id","row":false}`)},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseDashboard("f.json", []byte(tc.src)); err != nil {
				t.Errorf("rejected: %v", err)
			}
		})
	}
}

func TestValidationRefusesMisusedLinks(t *testing.T) {
	bar := func(link string) string {
		return onePanel(`"kind":"bar","signal":"traces","groupBy":["span.attr.user_id"],"series":[{"label":"n","agg":"count"}],"link":` + link)
	}
	bad := []struct{ name, src, path, want string }{
		{"on a line", onePanel(`"kind":"line","signal":"traces","series":[{"label":"n","agg":"count"}],` + usersLink), "link", "only a table, bar or postgres panel takes \"link\""},
		{"on a text panel", onePanel(`"kind":"text","markdown":"# x",` + usersLink), "link", "a text panel draws none"},
		{"on a browse panel", onePanel(`"kind":"browse","browse":{"database":"app","table":"public.users"},` + usersLink), "link", "only a table, bar or postgres panel"},
		{"on a postgres line", onePanel(`"kind":"postgres","postgres":{"database":"app","draw":"line","time":"t","sql":"SELECT 1"},` + usersLink), "link", `drawn as "line" has no rows`},
		{"not an object", bar(`"public.users"`), "link", "must be an object"},
		{"unknown field", bar(`{"database":"app","table":"public.users","column":"id","fromField":"span.attr.user_id","filter":"x"}`), "link.filter", `unknown field "filter"`},
		{"no database", bar(`{"table":"public.users","column":"id","fromField":"span.attr.user_id"}`), "link", `missing required field "database"`},
		{"bad database", bar(`{"database":"App","table":"public.users","column":"id","fromField":"span.attr.user_id"}`), "link.database", "is not a database name"},
		{"no table", bar(`{"database":"app","column":"id","fromField":"span.attr.user_id"}`), "link", `missing required field "table"`},
		{"unqualified table", bar(`{"database":"app","table":"users","column":"id","fromField":"span.attr.user_id"}`), "link.table", `not schema-qualified: name it as <schema>.<table>, like "public.users"`},
		{"empty schema", bar(`{"database":"app","table":".users","column":"id","fromField":"span.attr.user_id"}`), "link.table", "each part non-empty"},
		{"table too long", bar(`{"database":"app","table":"public.` + strings.Repeat("t", 121) + `","column":"id","fromField":"span.attr.user_id"}`), "link.table", "at most 127 bytes"},
		{"neither form", bar(`{"database":"app","table":"public.users"}`), "link", `either "column" and "fromField"`},
		{"column without fromField", bar(`{"database":"app","table":"public.users","column":"id"}`), "link", `missing required field "fromField"`},
		{"fromField without column", bar(`{"database":"app","table":"public.users","fromField":"span.attr.user_id"}`), "link", `missing required field "column"`},
		{"fromField not a group", bar(`{"database":"app","table":"public.users","column":"id","fromField":"span.name"}`), "link.fromField", `"span.name" is not one of the panel's groupBy fields (span.attr.user_id)`},
		{"table with no group", onePanel(`"kind":"table","signal":"traces","series":[{"label":"n","agg":"count"}],` + usersLink), "link.fromField", "one row of totals"},
		{"long column", bar(`{"database":"app","table":"public.users","column":"` + strings.Repeat("c", 64) + `","fromField":"span.attr.user_id"}`), "link.column", "1 to 63 bytes"},
		{"postgres fromField too long", onePanel(`"kind":"postgres","postgres":{"database":"app","sql":"SELECT 1"},"link":{"database":"app","table":"public.users","column":"id","fromField":"` + strings.Repeat("f", 64) + `"}`), "link.fromField", "1 to 63 bytes"},
		{"fromFields on a filter link", bar(`{"database":"app","table":"public.users","column":"id","fromField":"span.attr.user_id","fromFields":["span.attr.user_id"]}`), "link.fromFields", `only a row link ("row": true) takes "fromFields"`},
		{"row not a bool", bar(`{"database":"app","table":"public.users","row":"yes","fromFields":["span.attr.user_id"]}`), "link.row", "must be true or false"},
		{"row link without keys", bar(`{"database":"app","table":"public.users","row":true}`), "link", `a row link needs "fromFields"`},
		{"row link with no keys", bar(`{"database":"app","table":"public.users","row":true,"fromFields":[]}`), "link.fromFields", "1 to 8 key fields, has 0"},
		{"row link keys not a list", bar(`{"database":"app","table":"public.users","row":true,"fromFields":"id"}`), "link.fromFields", "must be an array"},
		{"row link with a column", bar(`{"database":"app","table":"public.users","row":true,"column":"id","fromFields":["span.attr.user_id"]}`), "link.column", `a row link opens one row by its key and takes no "column"`},
		{"row link with a fromField", bar(`{"database":"app","table":"public.users","row":true,"fromField":"span.attr.user_id","fromFields":["span.attr.user_id"]}`), "link.fromField", `takes no "fromField"`},
		{"row key not a group", bar(`{"database":"app","table":"public.users","row":true,"fromFields":["span.attr.org"]}`), "link.fromFields[0]", "is not one of the panel's groupBy fields"},
		{"row key twice", bar(`{"database":"app","table":"public.users","row":true,"fromFields":["span.attr.user_id","span.attr.user_id"]}`), "link.fromFields[1]", "named twice"},
		{"row key not a string", bar(`{"database":"app","table":"public.users","row":true,"fromFields":[1]}`), "link.fromFields[0]", "must be a string"},
		{"too many row keys", onePanel(`"kind":"postgres","postgres":{"database":"app","sql":"SELECT 1"},"link":{"database":"app","table":"public.t","row":true,"fromFields":["a","b","c","d","e","f","g","h","i"]}`), "link.fromFields", "1 to 8 key fields, has 9"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDashboard("f.json", []byte(tc.src))
			expectProblem(t, err, tc.path, tc.want)
		})
	}
}

func TestValidationAcceptsBrowsePanels(t *testing.T) {
	ok := []struct{ name, src string }{
		{"a table", onePanel(`"kind":"browse","browse":{"database":"app","table":"public.users"}`)},
		{"with a size and layout", onePanel(`"kind":"browse","description":"signups","width":24,"height":10,"browse":{"database":"app","table":"public.users","size":200}`)},
		{"every field", withFilters(`"kind":"browse","browse":{"database":"app","table":"controlplane.users",` +
			`"columns":["id","email","plan","created_at"],` +
			`"filters":[{"column":"created_at","op":"gte","values":["{from}"]},{"column":"created_at","op":"lt","values":["{to}"]},` +
			`{"column":"plan","op":"in","values":["pro","{plan}"]},{"column":"internal","op":"eq","values":["{internal}"]},` +
			`{"column":"deleted_at","op":"is_null"},{"column":"email","op":"ilike","values":["%@example.com"]}],` +
			`"sort":[{"column":"created_at","desc":true},{"column":"email"}]}`)},
		{"every operator", onePanel(`"kind":"browse","browse":{"database":"app","table":"public.t","filters":[` +
			`{"column":"a","op":"eq","values":["1"]},{"column":"a","op":"neq","values":["1"]},{"column":"a","op":"lt","values":["1"]},` +
			`{"column":"a","op":"lte","values":["1"]},{"column":"a","op":"gt","values":["1"]},{"column":"a","op":"gte","values":["1"]},` +
			`{"column":"a","op":"like","values":["x%"]},{"column":"a","op":"ilike","values":["x%"]},{"column":"a","op":"is_null","values":[]},` +
			`{"column":"a","op":"not_null"},{"column":"a","op":"in","values":["1","2"]}]}`)},
		{"braces inside a literal", onePanel(`"kind":"browse","browse":{"database":"app","table":"public.t","filters":[{"column":"a","op":"eq","values":["{a} or {b}"]}]}`)},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			d, err := ParseDashboard("f.json", []byte(tc.src))
			if err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if p := d.Panels[0]; p.Kind != KindBrowse || p.Browse == nil || p.Browse.Table == "" || p.Signal != "" || p.Series != nil {
				t.Errorf("decoded %+v", p)
			}
		})
	}
}

func TestValidationRefusesMisusedBrowsePanels(t *testing.T) {
	br := func(b string) string {
		return onePanel(`"kind":"browse","browse":{"database":"app","table":"public.t",` + b + `}`)
	}
	f := func(filter string) string { return br(`"filters":[` + filter + `]`) }
	bad := []struct{ name, src, path, want string }{
		{"no browse block", onePanel(`"kind":"browse"`), "", `needs a "browse" block`},
		{"not an object", onePanel(`"kind":"browse","browse":"public.t"`), "browse", "must be an object"},
		{"unknown field", br(`"limit":5`), "browse.limit", `unknown field "limit"`},
		{"no database", onePanel(`"kind":"browse","browse":{"table":"public.t"}`), "browse", `missing required field "database"`},
		{"bad database", onePanel(`"kind":"browse","browse":{"database":"a.b","table":"public.t"}`), "browse.database", "is not a database name"},
		{"no table", onePanel(`"kind":"browse","browse":{"database":"app"}`), "browse", `missing required field "table"`},
		{"unqualified table", onePanel(`"kind":"browse","browse":{"database":"app","table":"users"}`), "browse.table", "not schema-qualified"},
		{"signal", onePanel(`"kind":"browse","signal":"traces","browse":{"database":"app","table":"public.t"}`), "signal", `a browse panel takes no "signal"`},
		{"series", onePanel(`"kind":"browse","series":[{"label":"a","agg":"count"}],"browse":{"database":"app","table":"public.t"}`), "series", `takes no "series"`},
		{"unit", onePanel(`"kind":"browse","unit":"count","browse":{"database":"app","table":"public.t"}`), "unit", `takes no "unit"`},
		{"postgres beside browse", onePanel(`"kind":"browse","postgres":{"database":"app","sql":"SELECT 1"},"browse":{"database":"app","table":"public.t"}`), "postgres", `takes no "postgres"`},
		{"display", onePanel(`"kind":"browse","display":{"style":"bar"},"browse":{"database":"app","table":"public.t"}`), "display", "takes no \"display\""},
		{"browse on a line", onePanel(`"kind":"line","signal":"traces","series":[{"label":"a","agg":"count"}],"browse":{"database":"app","table":"public.t"}`), "browse", `only a browse panel takes "browse"`},
		{"browse on a postgres panel", onePanel(`"kind":"postgres","postgres":{"database":"app","sql":"SELECT 1"},"browse":{"database":"app","table":"public.t"}`), "browse", `takes no "browse"`},
		{"browse on issues", onePanel(`"kind":"issues","signal":"traces","browse":{"database":"app","table":"public.t"}`), "browse", `takes no "browse"`},
		{"browse on a text panel", onePanel(`"kind":"text","markdown":"x","browse":{"database":"app","table":"public.t"}`), "browse", `takes no "browse"`},
		{"column twice", br(`"columns":["id","id"]`), "browse.columns[1]", `column "id" is shown twice`},
		{"empty column", br(`"columns":[""]`), "browse.columns[0]", "1 to 63 bytes"},
		{"column not a string", br(`"columns":[1]`), "browse.columns[0]", "must be a string"},
		{"too many columns", br(`"columns":[` + strings.TrimSuffix(strings.Repeat(`"c",`, 101), ",") + `]`), "browse.columns", "over the 100 allowed"},
		{"filters not a list", br(`"filters":{}`), "browse.filters", "must be an array"},
		{"too many filters", br(`"filters":[` + strings.TrimSuffix(strings.Repeat(`{"column":"a","op":"not_null"},`, 33), ",") + `]`), "browse.filters", "over the 32 allowed"},
		{"filter unknown field", f(`{"column":"a","op":"eq","value":"1","values":["1"]}`), "browse.filters[0].value", `unknown field "value"`},
		{"filter without column", f(`{"op":"not_null"}`), "browse.filters[0]", `missing required field "column"`},
		{"filter without op", f(`{"column":"a"}`), "browse.filters[0]", `missing required field "op"`},
		{"telemetry op", f(`{"column":"a","op":"prefix","values":["x"]}`), "browse.filters[0].op", `"prefix" is not one of: eq, neq, lt, lte, gt, gte, like, ilike, is_null, not_null, in`},
		{"eq without a value", f(`{"column":"a","op":"eq"}`), "browse.filters[0]", `"eq" takes exactly 1 value in "values", has 0`},
		{"eq with two", f(`{"column":"a","op":"eq","values":["1","2"]}`), "browse.filters[0]", "has 2"},
		{"is_null with a value", f(`{"column":"a","op":"is_null","values":["x"]}`), "browse.filters[0].values", `"is_null" takes no values, has 1`},
		{"in with none", f(`{"column":"a","op":"in","values":[]}`), "browse.filters[0]", `"in" takes 1 to 100 values`},
		{"in with too many", f(`{"column":"a","op":"in","values":[` + strings.TrimSuffix(strings.Repeat(`"v",`, 101), ",") + `]}`), "browse.filters[0]", "has 101"},
		{"values not a list", f(`{"column":"a","op":"eq","values":"1"}`), "browse.filters[0].values", "must be an array"},
		{"value not a string", f(`{"column":"a","op":"eq","values":[1]}`), "browse.filters[0].values[0]", "must be a string"},
		{"value too long", f(`{"column":"a","op":"eq","values":["` + strings.Repeat("v", 4097) + `"]}`), "browse.filters[0].values[0]", "over the 4096"},
		{"unknown parameter", f(`{"column":"a","op":"gte","values":["{since}"]}`), "browse.filters[0].values[0]", "{since} references no parameter: a value may reference {from}, {to} or the id of a dashboard filter (this dashboard declares none)"},
		{"step is not a browse parameter", f(`{"column":"a","op":"gte","values":["{step}"]}`), "browse.filters[0].values[0]", "references no parameter"},
		{"sort twice", br(`"sort":[{"column":"a"},{"column":"a","desc":true}]`), "browse.sort[1].column", "sorted on twice"},
		{"sort without column", br(`"sort":[{"desc":true}]`), "browse.sort[0]", `missing required field "column"`},
		{"sort desc not a bool", br(`"sort":[{"column":"a","desc":"yes"}]`), "browse.sort[0].desc", "must be true or false"},
		{"sort unknown field", br(`"sort":[{"column":"a","nulls":"last"}]`), "browse.sort[0].nulls", `unknown field "nulls"`},
		{"too many sort keys", br(`"sort":[` + strings.TrimSuffix(strings.Repeat(`{"column":"a"},`, 9), ",") + `]`), "browse.sort", "over the 8 allowed"},
		{"odd size", br(`"size":30`), "browse.size", "is not a page size: one of 25, 50, 100, 200, 500 (leave it out for 50)"},
		{"fractional size", br(`"size":25.5`), "browse.size", "is not a page size"},
		{"zero size", br(`"size":0`), "browse.size", "is not a page size"},
		{"size not a number", br(`"size":"50"`), "browse.size", "must be a number"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDashboard("f.json", []byte(tc.src))
			expectProblem(t, err, tc.path, tc.want)
		})
	}
}

// A browse filter's parameter reference names a filter the dashboard declares.
func TestBrowseFilterReferencesTheDashboardsFilters(t *testing.T) {
	_, err := ParseDashboard("f.json", []byte(withFilters(`"kind":"browse","browse":{"database":"app","table":"public.t","filters":[{"column":"a","op":"eq","values":["{region}"]}]}`)))
	expectProblem(t, err, "browse.filters[0].values[0]", "{region} references no parameter: a value may reference {from}, {to} or the id of a dashboard filter (internal, plan)")
}

func TestBrowseProblemsAreLocated(t *testing.T) {
	src := `{"id":"x","title":"X","panels":[
  {"id":"p","title":"P","kind":"browse",
   "browse":{"database":"app","table":"users",
     "filters":[{"column":"a","op":"eq","values":["{nope}"]}]}},
  {"id":"q","title":"Q","kind":"bar","signal":"traces","groupBy":["span.name"],"series":[{"label":"n","agg":"count"}],
   "link":{"database":"app","table":"public.users","column":"id","fromField":"span.attr.user_id"}}
]}`
	_, err := ParseDashboard("dash.json", []byte(src))
	if err == nil {
		t.Fatal("validated")
	}
	want := map[string][2]int{
		"panels[0].browse.table":                {3, 39},
		"panels[0].browse.filters[0].values[0]": {4, 51},
		"panels[1].link.fromField":              {6, 78},
	}
	got := map[string][2]int{}
	for _, p := range ProblemsOf(err) {
		got[p.Path] = [2]int{p.Line, p.Col}
	}
	for path, pos := range want {
		if got[path] != pos {
			t.Errorf("%s at %v, want line %d col %d; all:\n%v", path, got[path], pos[0], pos[1], err)
		}
	}
	if !strings.Contains(err.Error(), "dash.json:6:78: panels[1].link.fromField: ") {
		t.Errorf("error does not print file:line:col: path:\n%v", err)
	}
}

func TestLinkAndBrowseRoundTripAndStayOutOfOtherPanels(t *testing.T) {
	src := `{"id":"x","title":"X","filters":[{"id":"plan","label":"Plan","field":"resource.attr.plan"}],"panels":[
{"id":"b","title":"B","kind":"browse","browse":{"database":"app","table":"public.users","columns":["id","email"],
 "filters":[{"column":"created_at","op":"gte","values":["{from}"]},{"column":"plan","op":"eq","values":["{plan}"]},{"column":"deleted_at","op":"is_null"}],
 "sort":[{"column":"created_at","desc":true}],"size":100}},
{"id":"t","title":"T","kind":"table","signal":"traces","groupBy":["span.attr.user_id"],"series":[{"label":"n","agg":"count"}],
 "link":{"database":"app","table":"public.users","row":true,"fromFields":["span.attr.user_id"]}}]}`
	d, err := ParseDashboard("f.json", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	wantBrowse := &BrowseView{
		Database: "app", Table: "public.users", Columns: []string{"id", "email"},
		Filters: []BrowseFilter{
			{Column: "created_at", Op: BrowseOpGte, Values: []string{"{from}"}},
			{Column: "plan", Op: BrowseOpEq, Values: []string{"{plan}"}},
			{Column: "deleted_at", Op: BrowseOpIsNull},
		},
		Sort: []BrowseSort{{Column: "created_at", Desc: true}},
		Size: 100,
	}
	if !reflect.DeepEqual(d.Panels[0].Browse, wantBrowse) {
		t.Errorf("browse decoded %+v", d.Panels[0].Browse)
	}
	wantLink := &PanelLink{Database: "app", Table: "public.users", Row: true, FromFields: []string{"span.attr.user_id"}}
	if !reflect.DeepEqual(d.Panels[1].Link, wantLink) {
		t.Errorf("link decoded %+v", d.Panels[1].Link)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseDashboard("f.json", out)
	if err != nil {
		t.Fatalf("a marshalled dashboard does not validate: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(again, d) {
		t.Errorf("round trip changed the dashboard:\n%s", out)
	}
	// A panel that sets neither marshals with neither key, so a reader that
	// predates them is never sent one.
	old := Panel{ID: "p", Title: "P", Kind: KindLine, Signal: Traces, Series: []Series{{Label: "a", Agg: AggCount}}}
	got, _ := json.Marshal(old)
	for _, key := range []string{`"browse"`, `"link"`} {
		if strings.Contains(string(got), key) {
			t.Errorf("marshal grew a %s key: %s", key, got)
		}
	}
}

func TestBrowseParam(t *testing.T) {
	for value, want := range map[string]string{
		"{from}": "from", "{to}": "to", "{plan}": "plan", "{service.name}": "service.name", "{include_internal}": "include_internal",
		"from": "", "{}": "", "{a} or {b}": "", " {from}": "", "{from} ": "", "{{from}}": "", "{-x}": "", "x{from}": "",
	} {
		name, ok := BrowseParam(value)
		if name != want || ok != (want != "") {
			t.Errorf("BrowseParam(%q) = (%q, %v), want %q", value, name, ok, want)
		}
	}
}

// The operator set and its arities are the data browser's (dbbrowse), so a
// saved view is a request the browser serves.
func TestBrowseOpsAreTheDataBrowsers(t *testing.T) {
	want := map[BrowseOp]int{
		"eq": 1, "neq": 1, "lt": 1, "lte": 1, "gt": 1, "gte": 1, "like": 1, "ilike": 1,
		"is_null": 0, "not_null": 0, "in": -1,
	}
	ops := BrowseOps()
	if len(ops) != len(want) {
		t.Fatalf("BrowseOps() = %v, want %d operators", ops, len(want))
	}
	for _, op := range ops {
		n, ok := BrowseOpArity(op)
		if w, known := want[op]; !ok || !known || n != w {
			t.Errorf("BrowseOpArity(%q) = (%d, %v), want %d", op, n, ok, w)
		}
	}
	if _, ok := BrowseOpArity("prefix"); ok {
		t.Error("prefix is a telemetry operator, not a browse one")
	}
	if got := BrowseSizes(); !reflect.DeepEqual(got, []int{25, 50, 100, 200, 500}) || DefaultBrowseSize != 50 {
		t.Errorf("BrowseSizes() = %v (default %d)", got, DefaultBrowseSize)
	}
	BrowseSizes()[0] = 1
	if BrowseSizes()[0] != 25 {
		t.Error("BrowseSizes returned the package's own slice")
	}
}
