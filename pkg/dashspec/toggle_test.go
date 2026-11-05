package dashspec

import (
	"reflect"
	"strings"
	"testing"
)

func TestToggleFilterIsASQLParameter(t *testing.T) {
	valid := `{"id":"usage","title":"Usage","filters":[` +
		`{"id":"env","label":"Env","field":"deployment.environment.name"},` +
		`{"id":"include_internal","kind":"toggle","label":"Include internal users"},` +
		`{"id":"only_paid","kind":"toggle","label":"Paid only","default":true}],` +
		`"panels":[{"id":"n","title":"N","kind":"sql","sql":"SELECT count() FROM product_events WHERE {include_internal:Bool} OR UserId != ''"}]}`
	d, err := ParseDashboard("toggle", []byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if f := d.Filters[1]; f.Kind != FilterToggle || f.Field != "" || f.Default {
		t.Errorf("toggle parsed as %+v", f)
	}
	if !d.Filters[2].Default {
		t.Error("a toggle's default was lost")
	}
	// Every declared toggle is bound: the viewer's value when set, else its
	// default; names the dashboard does not declare are dropped.
	got := d.ToggleValues(map[string]bool{"include_internal": true, "not_declared": true})
	if want := map[string]bool{"include_internal": true, "only_paid": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("ToggleValues = %v, want %v", got, want)
	}
	if got := d.ToggleValues(nil); !reflect.DeepEqual(got, map[string]bool{"include_internal": false, "only_paid": true}) {
		t.Errorf("ToggleValues(nil) = %v, want each default", got)
	}

	for _, tc := range []struct{ name, from, to, want string }{
		{"bad-id", `"id":"include_internal"`, `"id":"Include-Internal"`, "must match"},
		{"builtin-id", `"id":"include_internal"`, `"id":"env_ids"`, "built-in sql parameter"},
		{"toggle-with-field", `"kind":"toggle","label":"Include internal users"`, `"kind":"toggle","label":"Include internal users","field":"service.name"`, "takes no field"},
		{"non-bool-default", `"default":true`, `"default":"yes"`, "must be a boolean"},
		{"default-on-dropdown", `"field":"deployment.environment.name"}`, `"field":"deployment.environment.name","default":true}`, "only a toggle filter"},
		{"unknown-kind", `"kind":"toggle","label":"Paid only"`, `"kind":"switch","label":"Paid only"`, "is not one of"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDashboard(tc.name, []byte(strings.Replace(valid, tc.from, tc.to, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}
