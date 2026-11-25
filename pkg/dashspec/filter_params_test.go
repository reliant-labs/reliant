package dashspec

import (
	"reflect"
	"strings"
	"testing"
)

func TestFilterParamIsAStringSQLParameter(t *testing.T) {
	valid := `{"id":"traffic","title":"Traffic","filters":[` +
		`{"id":"env","label":"Env","field":"deployment.environment.name"}],` +
		`"filterParams":[{"id":"f_route","label":"Page","description":"Visitors who viewed it."},{"id":"f_country","label":"Country"}],` +
		`"panels":[{"id":"pages","title":"Pages","kind":"sql",` +
		`"sql":"SELECT Props['route'] AS \"Page\", count() AS \"Views\" FROM product_events WHERE ({f_route:String} = '' OR Props['route'] = {f_route:String}) GROUP BY \"Page\"",` +
		`"trendSql":"SELECT toStartOfHour(Timestamp) AS t, count() FROM product_events WHERE {f_country:String} = '' OR Country = {f_country:String} GROUP BY t",` +
		`"select":{"param":"f_route","column":"Page"}}]}`
	d, err := ParseDashboard("traffic", []byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.FilterParams) != 2 || d.FilterParams[0] != (FilterParam{ID: "f_route", Label: "Page", Description: "Visitors who viewed it."}) {
		t.Errorf("filter params parsed as %+v", d.FilterParams)
	}
	if s := d.Panels[0].Select; s == nil || *s != (RowSelect{Param: "f_route", Column: "Page"}) {
		t.Errorf("select parsed as %+v", s)
	}
	// Every declared param is bound: the viewer's value when set, else the
	// empty string; names the dashboard does not declare are dropped.
	got := d.FilterParamValues(map[string]string{"f_route": "/pricing", "f_other": "x"})
	if want := map[string]string{"f_route": "/pricing", "f_country": ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("FilterParamValues = %v, want %v", got, want)
	}
	if got := (Dashboard{}).FilterParamValues(map[string]string{"f_route": "x"}); got != nil {
		t.Errorf("a dashboard with no params binds %v", got)
	}

	for _, tc := range []struct{ name, from, to, want string }{
		{"bad-id", `{"id":"f_country","label":"Country"}`, `{"id":"country","label":"Country"}`, "must match"},
		{"upper-id", `{"id":"f_country","label":"Country"}`, `{"id":"f_Country","label":"Country"}`, "must match"},
		{"duplicate", `{"id":"f_country","label":"Country"}`, `{"id":"f_route","label":"Country"}`, "duplicate filter param"},
		{"is-a-filter", `{"id":"env","label":"Env"`, `{"id":"f_country","label":"Env"`, "also a filter's id"},
		{"no-label", `{"id":"f_country","label":"Country"}`, `{"id":"f_country"}`, `missing required field "label"`},
		{"unknown-field", `{"id":"f_country","label":"Country"}`, `{"id":"f_country","label":"Country","default":"US"}`, `unknown field "default"`},
		{"undeclared-read", `{f_route:String} = ''`, `{f_page:String} = ''`, "declares in no filterParams"},
		{"undeclared-trend", `{f_country:String} = ''`, `{f_city:String} = ''`, "declares in no filterParams"},
		{"not-a-string", `({f_route:String} = ''`, `({f_route:Array(String)} = ''`, "a filter param is a String"},
		{"select-undeclared", `"param":"f_route"`, `"param":"f_page"`, "not one of the dashboard's filterParams"},
		{"select-no-column", `,"column":"Page"`, ``, `missing required field "column"`},
		{"select-unknown-field", `"column":"Page"}`, `"column":"Page","value":"x"}`, `unknown field "value"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDashboard(tc.name, []byte(strings.Replace(valid, tc.from, tc.to, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}

	// A toggle that predates filter params and has an f_ id stays a Bool.
	toggle := `{"id":"t","title":"T","filters":[{"id":"f_internal","kind":"toggle","label":"Internal"}],` +
		`"panels":[{"id":"p","title":"P","kind":"sql","sql":"SELECT count() FROM product_events WHERE {f_internal:Bool}"}]}`
	if _, err := ParseDashboard("toggle", []byte(toggle)); err != nil {
		t.Errorf("a toggle named f_internal: %v", err)
	}

	// Only a sql panel takes a select.
	line := `{"id":"traffic","title":"T","filterParams":[{"id":"f_route","label":"Page"}],"panels":[{"id":"n","title":"N","kind":"number","signal":"traces",` +
		`"series":[{"label":"n","agg":"count"}],"select":{"param":"f_route","column":"n"}}]}`
	if _, err := ParseDashboard("line", []byte(line)); err == nil || !strings.Contains(err.Error(), `only a sql panel takes "select"`) {
		t.Errorf("a number panel's select: err = %v", err)
	}
	// At most MaxFilterParams.
	var many []string
	for i := 0; i <= MaxFilterParams; i++ {
		many = append(many, `{"id":"f_`+string(rune('a'+i))+`","label":"X"}`)
	}
	tooMany := `{"id":"t","title":"T","filterParams":[` + strings.Join(many, ",") + `],"panels":[{"id":"p","title":"P","kind":"sql","sql":"SELECT 1"}]}`
	if _, err := ParseDashboard("many", []byte(tooMany)); err == nil || !strings.Contains(err.Error(), "filter params, over the") {
		t.Errorf("%d filter params: err = %v", MaxFilterParams+1, err)
	}
}

func TestCheckFilterParamValue(t *testing.T) {
	for _, ok := range []string{"", "/pricing", "/blog/:slug", "Mac OS", "x' OR '1'='1", `a\b`, "Zürich", strings.Repeat("a", MaxFilterParamValueBytes)} {
		if err := CheckFilterParamValue(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for bad, want := range map[string]string{
		strings.Repeat("a", MaxFilterParamValueBytes+1): "at most",
		"\xff":    "UTF-8",
		"a\x00b":  "control",
		"a\nb":    "control",
		"a\tb":    "control",
		"a\u0085": "control",
	} {
		if err := CheckFilterParamValue(bad); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", bad, err, want)
		}
	}
}

func TestUsedSQLParams(t *testing.T) {
	sql := `SELECT {a:String}, { b : Array(String) }, {a:String}
-- {c:String} in a comment
/* {d:String} in a block */
FROM t WHERE x = '{e:String}' AND "{f:String}" = 1 AND ` + "`{g:String}`" + ` = {h:UInt32} # {i:String}
  AND y = 'it''s {j:String}' AND z = {k:Bool}`
	got := UsedSQLParams(sql)
	want := []SQLParamUse{{"a", "String"}, {"b", "Array(String)"}, {"h", "UInt32"}, {"k", "Bool"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UsedSQLParams = %+v, want %+v", got, want)
	}
	if got := UsedSQLParams("SELECT 1 /* unterminated"); got != nil {
		t.Errorf("an unterminated comment: %+v", got)
	}
}
