package tmplfields

import "testing"

func TestLiteralParametersStayInTheirBody(t *testing.T) {
	rep := fixture(t, map[string]string{
		"outer.tmpl": "{{.Outer}}{{.Missing}}",
		"inner.tmpl": "{{.Inner}}{{.Missing}}",
	}, map[string]string{"app/app.go": callerHeader + `
func render() {
	data := struct{ Outer string }{}
	tpl.ProjectTemplates().Render("outer.tmpl", data)
	_ = func(data struct{ Inner string }) {
		tpl.ProjectTemplates().Render("inner.tmpl", data)
		_ = func(data any) {
			tpl.ProjectTemplates().Render("inner.tmpl", data)
		}
		tpl.ProjectTemplates().Render("inner.tmpl", data)
	}
	tpl.ProjectTemplates().Render("outer.tmpl", data)
}
`})
	wantFindings(t, rep,
		"app/app.go:8 Missing outer.tmpl:1:12",
		"app/app.go:10 Missing inner.tmpl:1:12",
		"app/app.go:14 Missing inner.tmpl:1:12",
		"app/app.go:16 Missing outer.tmpl:1:12",
	)
	if rep.Checked != 4 || rep.Open != 1 {
		t.Fatalf("checked %d open %d, want 4 and 1", rep.Checked, rep.Open)
	}
}

func TestMultiResultDefinitions(t *testing.T) {
	for _, declaration := range []string{"a, b, c := pair()", "var a, b, c = pair()", "a, b, c := other.Pair()", "a, b, c := receiver{}.Pair()"} {
		t.Run(declaration, func(t *testing.T) {
			rep := fixture(t, map[string]string{
				"a.tmpl": "{{.A}}{{.MissingA}}",
				"b.tmpl": "{{.B}}{{.MissingB}}",
			}, map[string]string{
				"other/data.go": `package other
type A struct{ A string }
type B struct{ B string }
func Pair() (a A, b, c B) { return }
`,
				"app/app.go": callerHeader + `
import "example.com/m/other"
type A struct{ A string }
type B struct{ B string }
type receiver struct{}
func pair() (a A, b, c B) { return }
func (receiver) Pair() (A, B, B) { return A{}, B{}, B{} }
func render() {
	` + declaration + `
	tpl.ProjectTemplates().Render("a.tmpl", a)
	tpl.ProjectTemplates().Render("b.tmpl", b)
	tpl.ProjectTemplates().Render("b.tmpl", c)
}
`,
			})
			wantFindings(t, rep,
				"app/app.go:14 MissingA a.tmpl:1:8",
				"app/app.go:15 MissingB b.tmpl:1:8",
				"app/app.go:16 MissingB b.tmpl:1:8",
			)
			if rep.Checked != 3 || rep.Open != 0 {
				t.Fatalf("checked %d open %d, want 3 and 0", rep.Checked, rep.Open)
			}
		})
	}
}
