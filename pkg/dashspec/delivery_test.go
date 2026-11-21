package dashspec

import (
	"strings"
	"testing"
)

func TestDeliveryKindsValidate(t *testing.T) {
	for _, src := range []string{
		onePanel(`"kind":"release_health"`),
		onePanel(`"kind":"release_health","description":"Each env's running release.","section":"s","width":24,"height":10`),
		onePanel(`"kind":"promotions"`),
		onePanel(`"kind":"promotions","limit":25,"width":24,"height":9`),
		onePanel(`"kind":"delivery","width":24,"height":5`),
	} {
		src = strings.Replace(src, `"panels"`, `"sections":[{"id":"s","title":"S"}],"panels"`, 1)
		d, err := ParseDashboard("f.json", []byte(src))
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if k := d.Panels[0].Kind; !IsDeliveryKind(k) || !IsConsoleKind(k) {
			t.Errorf("%s: delivery kind = %v, console kind = %v", k, IsDeliveryKind(k), IsConsoleKind(k))
		}
	}
}

func TestDeliveryKindsCarryNoQuery(t *testing.T) {
	bad := []struct{ name, src, path, want string }{
		{"health with a signal", onePanel(`"kind":"release_health","signal":"traces"`), "signal", `a release_health panel takes no "signal"`},
		{"health with series", onePanel(`"kind":"release_health",` + oneCount), "series", `a release_health panel takes no "series"`},
		{"health with a limit", onePanel(`"kind":"release_health","limit":5`), "limit", `a release_health panel takes no "limit"`},
		{"health with a where", onePanel(`"kind":"release_health","where":[{"field":"service.name","op":"eq","value":"api"}]`), "where", `takes no "where"`},
		{"promotions with sql", onePanel(`"kind":"promotions","sql":"SELECT 1"`), "sql", `a promotions panel takes no "sql"`},
		{"promotions with a display", onePanel(`"kind":"promotions","display":{"style":"line"}`), "display", `takes no "display"`},
		{"promotions limit zero", onePanel(`"kind":"promotions","limit":0`), "limit", "from 1 to 200"},
		{"promotions limit over the cap", onePanel(`"kind":"promotions","limit":201`), "limit", "from 1 to 200"},
		{"promotions fractional limit", onePanel(`"kind":"promotions","limit":2.5`), "limit", "whole number"},
		{"delivery with a unit", onePanel(`"kind":"delivery","unit":"percent"`), "unit", `a delivery panel takes no "unit"`},
		{"delivery with a groupBy", onePanel(`"kind":"delivery","groupBy":["service.name"]`), "groupBy", `takes no "groupBy"`},
		{"delivery with markdown", onePanel(`"kind":"delivery","markdown":"hi"`), "markdown", `only a text panel takes "markdown"`},
		{"delivery too wide", onePanel(`"kind":"delivery","width":25`), "width", "grid columns from 1 to 24"},
		{"a link on promotions", onePanel(`"kind":"promotions","link":{"database":"app","table":"public.t","row":true}`), "link", "a promotions panel draws none"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDashboard("f.json", []byte(tc.src))
			if err == nil {
				t.Fatalf("validated; want %q at %q", tc.want, tc.path)
			}
			for _, p := range ProblemsOf(err) {
				if strings.HasSuffix(p.Path, tc.path) && strings.Contains(p.Message, tc.want) {
					return
				}
			}
			t.Fatalf("problems = %v; want %q at %q", ProblemsOf(err), tc.want, tc.path)
		})
	}
}
