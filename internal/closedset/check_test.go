package closedset

import (
	"slices"
	"strings"
	"testing"
)

// The spellings below are the ones the two consumers actually carry:
// reliant's pg_dump baseline, reliant's and control-plane's hand-written
// replacements, and what pg_get_constraintdef normalizes an IN and a varchar
// IN to.
func TestParseCheck_Forms(t *testing.T) {
	cases := []struct {
		name, expr, column, form string
		values                   []string
		cast                     string
	}{
		{
			name:   "pg_dump array containment, members cast",
			expr:   `((cardinality(scopes) > 0) AND (scopes <@ ARRAY['deploy:read'::text, 'deploy:write'::text]))`,
			column: "scopes", form: FormContainedBy, values: []string{"deploy:read", "deploy:write"}, cast: "::text",
		},
		{
			name: "hand-written containment, array cast",
			expr: `
        cardinality(scopes) > 0
        AND scopes <@ ARRAY[
            'deploy:read',
            'identity:manage'
        ]::TEXT[]
    `,
			column: "scopes", form: FormContainedBy, values: []string{"deploy:read", "identity:manage"},
		},
		{
			name:   "= ANY behind IS NULL OR",
			expr:   `((resource_kind IS NULL) OR (resource_kind = ANY (ARRAY['daemon'::text, 'port'::text, 'connector'::text])))`,
			column: "resource_kind", form: FormAny, values: []string{"daemon", "port", "connector"}, cast: "::text",
		},
		{
			name:   "IN list",
			expr:   `status IN ('open', 'closed')`,
			column: "status", form: FormIn, values: []string{"open", "closed"},
		},
		{
			name:   "pg_dump varchar IN",
			expr:   `((status)::text = ANY ((ARRAY['a'::character varying, 'b'::character varying])::text[]))`,
			column: "status", form: FormAny, values: []string{"a", "b"}, cast: "::character varying",
		},
		{
			name:   "array literal",
			expr:   `tags <@ '{red,"dark blue"}'::text[]`,
			column: "tags", form: FormContainedBy, values: []string{"red", "dark blue"},
		},
		{
			name:   "quoted values and integers",
			expr:   `"Level" IN (1, 2, -3) -- prose 'x'`,
			column: "Level", form: FormIn, values: []string{"1", "2", "-3"},
		},
		{
			name:   "escaped quote",
			expr:   `v = ANY (ARRAY['it''s'])`,
			column: "v", form: FormAny, values: []string{"it's"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := ParseCheck(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			if l.Column != tc.column || l.Form != tc.form || !slices.Equal(l.Values, tc.values) || l.Cast != tc.cast {
				t.Fatalf("got column=%q form=%q values=%q cast=%q, want %q %q %q %q",
					l.Column, l.Form, l.Values, l.Cast, tc.column, tc.form, tc.values, tc.cast)
			}
		})
	}
}

func TestParseCheck_Refuses(t *testing.T) {
	cases := map[string]struct{ expr, want string }{
		"no list":        {`amount_cents >= 0`, "no closed value list"},
		"deny list":      {`status NOT IN ('x')`, "no closed value list"},
		"two lists":      {`a IN ('x') AND b IN ('y')`, "2 value lists (on a, b)"},
		"non-literal":    {`a = ANY (ARRAY['x', lower('Y')])`, `member 2 of the CHECK's value list is "lower"`},
		"subquery":       {`a IN (SELECT x FROM t)`, "no closed value list"},
		"unclosed array": {`a <@ ARRAY['x'`, "not closed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseCheck(tc.expr)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// A rewrite replaces the members only: the guard around the list and the
// array's trailing cast survive, and the members keep their cast and layout.
func TestRenderMembers_KeepsTheExpression(t *testing.T) {
	expr := `((cardinality(scopes) > 0) AND (scopes <@ ARRAY['a'::text, 'b'::text]))`
	l, err := ParseCheck(expr)
	if err != nil {
		t.Fatal(err)
	}
	got := expr[:l.Start] + renderMembers(l, []string{"a", "b", "c"}) + expr[l.End:]
	want := `((cardinality(scopes) > 0) AND (scopes <@ ARRAY['a'::text, 'b'::text, 'c'::text]))`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}

	multi := "\n    scopes <@ ARRAY[\n        'a',\n        'b'\n    ]::TEXT[]\n"
	l, err = ParseCheck(multi)
	if err != nil {
		t.Fatal(err)
	}
	got = multi[:l.Start] + renderMembers(l, []string{"a", "c"}) + multi[l.End:]
	if want := "\n    scopes <@ ARRAY[\n        'a',\n        'c'\n    ]::TEXT[]\n"; got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}

	lit := `tags <@ '{red,blue}'::text[]`
	l, err = ParseCheck(lit)
	if err != nil {
		t.Fatal(err)
	}
	got = lit[:l.Start] + renderMembers(l, []string{"red", "dark blue"}) + lit[l.End:]
	if want := `tags <@ '{red,"dark blue"}'::text[]`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
