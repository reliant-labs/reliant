package sqlident

import (
	"reflect"
	"regexp"
	"testing"
)

// TestParts pins the normalization: an unquoted part folds to lower case,
// a quoted one is kept verbatim with `""` read as one quote, and the dot may
// carry whitespace. This is how postgres resolves the same text, which is
// what lets a textual reader agree with the applied schema.
func TestParts(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{`tasks`, []string{"tasks"}},
		{`Tasks`, []string{"tasks"}},
		{`public.tasks`, []string{"public", "tasks"}},
		{`"public"."tasks"`, []string{"public", "tasks"}},
		{`public."Tasks"`, []string{"public", "Tasks"}},
		{`"Public".tasks`, []string{"Public", "tasks"}},
		{`public . tasks`, []string{"public", "tasks"}},
		{`"odd""name"`, []string{`odd"name`}},
		{`"has.dot"`, []string{"has.dot"}},
		{`app.tasks.owner_id`, []string{"app", "tasks", "owner_id"}},
		{` tasks `, []string{"tasks"}},
	} {
		if got := Parts(tc.raw); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Parts(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestLast(t *testing.T) {
	for raw, want := range map[string]string{
		`tasks`:            "tasks",
		`public.tasks`:     "tasks",
		`"public"."tasks"`: "tasks",
		`public."Tasks"`:   "Tasks",
		``:                 "",
	} {
		if got := Last(raw); got != want {
			t.Errorf("Last(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestQualified_ReadsTheWholeName is the regression the package exists for:
// a CREATE TABLE pattern built on Qualified must capture the qualified name
// whole, so the table is read as `tasks` and never as `public`.
func TestQualified_ReadsTheWholeName(t *testing.T) {
	re := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(` + Qualified + `)\s*\(`)
	for stmt, want := range map[string]string{
		`CREATE TABLE tasks (id text);`:                         "tasks",
		`CREATE TABLE public.tasks (id text);`:                  "tasks",
		`create table "public"."tasks"(id text);`:               "tasks",
		`CREATE TABLE public."Tasks" (id text);`:                "Tasks",
		`CREATE TABLE IF NOT EXISTS app.tasks (id text);`:       "tasks",
		`CREATE TABLE "my schema"."my ""table""" (id text);`:    `my "table"`,
		`CREATE TABLE IF NOT EXISTS "public" . "tasks" (id t);`: "tasks",
	} {
		m := re.FindStringSubmatch(stmt)
		if m == nil {
			t.Errorf("%q: no match", stmt)
			continue
		}
		if got := Last(m[1]); got != want {
			t.Errorf("%q: table = %q, want %q (captured %q)", stmt, got, want, m[1])
		}
	}
}
