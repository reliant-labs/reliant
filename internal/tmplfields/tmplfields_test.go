package tmplfields

import (
	"strings"
	"testing"
)

// fixture scans one in-memory module, example.com/m, whose templates package
// is example.com/m/tpl with a single ProjectTemplates category.
func fixture(t *testing.T, tmpls map[string]string, files map[string]string) Report {
	t.Helper()
	src := map[string][]byte{
		"tpl/tpl.go": []byte(`package tpl

type Category struct{}

func ProjectTemplates() Category { return Category{} }

func (Category) Render(name string, data any) ([]byte, error) { return nil, nil }
`),
	}
	for k, v := range files {
		src[k] = []byte(v)
	}
	rep, err := Scan(src, Config{
		ModulePath:   "example.com/m",
		TemplatesPkg: "example.com/m/tpl",
		Accessors: map[string]Accessor{
			"ProjectTemplates": {Get: func(_, name string) ([]byte, bool) {
				s, ok := tmpls[name]
				return []byte(s), ok
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// fields lists each finding as "<site> <field> <template position>".
func fields(rep Report) []string {
	var out []string
	for _, f := range rep.Findings {
		out = append(out, f.Site+" "+f.Field+" "+f.At)
	}
	return out
}

func wantFindings(t *testing.T, rep Report, want ...string) {
	t.Helper()
	got := fields(rep)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("findings:\n  got  %q\n  want %q", got, want)
	}
}

const callerHeader = `package app

import "example.com/m/tpl"

`

func TestMissingRootFieldOfAnonymousStruct(t *testing.T) {
	rep := fixture(t,
		map[string]string{"roots.tmpl": "{{.Roots}}{{if .Process}}{{.Process.EnvMap}}{{end}}"},
		map[string]string{"app/app_test.go": callerHeader + `
func stub() {
	tpl.ProjectTemplates().Render("roots.tmpl", struct{ Roots []string }{})
}
`})
	wantFindings(t, rep, "app/app_test.go:7 Process roots.tmpl:1:15")
	if rep.Checked != 1 || rep.Open != 0 {
		t.Errorf("checked %d open %d, want 1 and 0", rep.Checked, rep.Open)
	}
	if f := rep.Findings[0]; !f.HandBuilt || !strings.Contains(f.String(), "production data builder") {
		t.Errorf("a hand-built struct's finding must name the production builder: %s", f)
	}
}

func TestMissingFieldInsideRangeOfNamedType(t *testing.T) {
	rep := fixture(t,
		map[string]string{"list.tmpl": "{{range .Items}}{{.Name}}{{.Kind}}{{end}}{{range $i, $e := .Items}}{{$e.Size}}{{end}}"},
		map[string]string{
			"app/types.go": `package app

type data struct{ Items []item }

type item struct{ Name string }
`,
			"app/app.go": callerHeader + `
func render() {
	d := data{}
	tpl.ProjectTemplates().Render("list.tmpl", &d)
}
`,
		})
	wantFindings(t, rep,
		"app/app.go:8 Kind list.tmpl:1:27",
		"app/app.go:8 Size list.tmpl:1:71",
	)
	if f := rep.Findings[0]; f.HandBuilt || !strings.Contains(f.String(), "add the field to app.item") {
		t.Errorf("a named type's finding must name the type to grow: %s", f)
	}
}

func TestDollarRootInsideWithAndDefine(t *testing.T) {
	rep := fixture(t,
		map[string]string{"d.tmpl": `{{define "x"}}{{.A}}{{.Missing}}{{end}}{{with .Inner}}{{$.Top}}{{$.Gone}}{{.A}}{{end}}{{template "x" .Inner}}`},
		map[string]string{"app/app.go": callerHeader + `
type inner struct{ A string }

func render() {
	tpl.ProjectTemplates().Render("d.tmpl", struct {
		Top   string
		Inner inner
	}{})
}
`})
	wantFindings(t, rep,
		"app/app.go:9 Gone d.tmpl:1:66",
		"app/app.go:9 Missing d.tmpl:1:22",
	)
}

func TestOpenTypesAreNotReported(t *testing.T) {
	rep := fixture(t,
		map[string]string{"o.tmpl": "{{.Anything}}{{range .X}}{{.Y}}{{end}}"},
		map[string]string{"app/app.go": callerHeader + `
import "os"

type withExternal struct{ os.File }

func data() any { return nil }

func render(m map[string]any) {
	tpl.ProjectTemplates().Render("o.tmpl", data())
	tpl.ProjectTemplates().Render("o.tmpl", m)
	tpl.ProjectTemplates().Render("o.tmpl", nil)
	tpl.ProjectTemplates().Render("o.tmpl", withExternal{})
	tpl.ProjectTemplates().Render("o.tmpl", struct{ X []any; Anything int }{})
}
`})
	wantFindings(t, rep)
	if rep.Open != 4 || rep.Checked != 1 {
		t.Errorf("open %d checked %d, want 4 and 1", rep.Open, rep.Checked)
	}
}

func TestMethodSatisfiesAField(t *testing.T) {
	rep := fixture(t,
		map[string]string{"m.tmpl": "{{.Label}}{{.Ptr}}{{.Embedded}}{{.Deep}}{{.Label.Nope}}"},
		map[string]string{"app/app.go": callerHeader + `
type base struct{ Deep bool }

type label string

func (label) Upper() string { return "" }

type data struct {
	base
	Embedded int
}

func (d data) Label() label { return "" }

func (d *data) Ptr() string { return "" }

func render() {
	tpl.ProjectTemplates().Render("m.tmpl", data{})
}
`})
	wantFindings(t, rep, "app/app.go:22 Nope m.tmpl:1:48")
}

func TestCrossPackageTypeAndLocalDefinitions(t *testing.T) {
	rep := fixture(t,
		map[string]string{"x.tmpl": "{{.Name}}{{.Port}}"},
		map[string]string{
			"cfg/cfg.go": `package cfg

type Data struct{ Name string }
`,
			"app/app_test.go": callerHeader + `
import "example.com/m/cfg"

const tmplName = "x.tmpl"

func render() {
	cases := []struct{ d cfg.Data }{{}}
	for _, c := range cases {
		tpl.ProjectTemplates().Render(tmplName, c.d)
	}
	name := "x.tmpl"
	name = "other.tmpl"
	tpl.ProjectTemplates().Render(name, cfg.Data{})
}
`,
		})
	wantFindings(t, rep, "app/app_test.go:13 Port x.tmpl:1:11")
	if rep.Skipped["non-literal template name"] != 1 {
		t.Errorf("a reassigned name must be skipped, not trusted: %v", rep.Skipped)
	}
}

func TestReassignedTemplateVariableIsOpen(t *testing.T) {
	rep := fixture(t,
		map[string]string{"v.tmpl": "{{$x := .A}}{{if .B}}{{$x = .B}}{{end}}{{$x.OnlyOnB}}{{$y := .A}}{{$y.NotOnA}}"},
		map[string]string{"app/app.go": callerHeader + `
type a struct{ Z int }

type b struct{ OnlyOnB int }

func render() {
	tpl.ProjectTemplates().Render("v.tmpl", struct{ A a; B *b }{})
}
`})
	wantFindings(t, rep, "app/app.go:11 NotOnA v.tmpl:1:69")
}

func TestCallResultsAndRangedNames(t *testing.T) {
	rep := fixture(t,
		map[string]string{
			"a.tmpl": "{{.Name}}{{.Port}}",
			"b.tmpl": "{{.Name}}",
			"c.tmpl": "{{.Name}}{{.Host}}",
		},
		map[string]string{
			"cfg/cfg.go": `package cfg

type Data struct{ Name string }

func New() (Data, error) { return Data{}, nil }

type Builder struct{}

func (Builder) Build() *Data { return nil }
`,
			"app/app.go": callerHeader + `
import "example.com/m/cfg"

func render() {
	d, _ := cfg.New()
	for _, name := range []string{"a.tmpl", "b.tmpl"} {
		tpl.ProjectTemplates().Render(name, d)
	}
	for _, tc := range []struct{ tmpl string }{{"b.tmpl"}, {tmpl: "c.tmpl"}} {
		tpl.ProjectTemplates().Render(tc.tmpl, cfg.Builder{}.Build())
	}
}
`,
		})
	wantFindings(t, rep,
		"app/app.go:11 Port a.tmpl:1:11",
		"app/app.go:14 Host c.tmpl:1:11",
	)
	if rep.Checked != 4 {
		t.Errorf("checked %d, want 4: each ranged name is its own render", rep.Checked)
	}
}
