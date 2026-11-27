package scaffold

import (
	"reflect"
	"testing"
)

// normalizeRouteSlugs canonicalizes what an author types into --routes. The
// input is usually copied from a URL bar, so slashes and casing must not
// decide whether the route matches.
func TestNormalizeRouteSlugs(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in   []string
		want []string
	}{
		"lowercases":            {[]string{"Users", "USAGE-EVENTS"}, []string{"users", "usage-events"}},
		"trims slashes":         {[]string{"/users/", "/usage-events"}, []string{"users", "usage-events"}},
		"trims whitespace":      {[]string{"  users  "}, []string{"users"}},
		"drops blanks":          {[]string{"users", "", "   ", "/"}, []string{"users"}},
		"collapses duplicates":  {[]string{"users", "/Users", "users"}, []string{"users"}},
		"preserves given order": {[]string{"plans", "users", "daemons"}, []string{"plans", "users", "daemons"}},
		"empty input":           {nil, []string{}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := normalizeRouteSlugs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("normalizeRouteSlugs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
