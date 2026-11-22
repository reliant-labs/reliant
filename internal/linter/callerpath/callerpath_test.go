package callerpath

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// file wraps body in a file importing what the cases use.
func file(body string) string {
	return `package p

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var _ = errors.New
var _ = fmt.Sprint
var _ = os.ReadFile
var _ = filepath.Dir
var _ = testing.Short

` + body
}

// got runs CheckFile and renders "rule@line" for each finding, the line
// relative to the first line of body.
func got(t *testing.T, name, src string, offset int) []string {
	t.Helper()
	var out []string
	for _, f := range CheckFile(name, []byte(src)) {
		out = append(out, fmt.Sprintf("%s@%d", f.Rule, f.Line-offset))
	}
	return out
}

func TestCheckFile(t *testing.T) {
	offset := strings.Count(file(""), "\n")
	cases := []struct {
		name string
		body string
		want []int
	}{
		// ── flagged ──────────────────────────────────────────────────
		{"testdata from the source dir", `func TestX(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok { t.Fatal("no caller") }
	os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", "x.json"))
}`, []int{2}},
		{"repo root helper returns it", `func repoRoot() string {
	_, self, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(self))
}`, []int{2}},
		{"plain assignment", `func TestX(t *testing.T) {
	var self string
	var ok bool
	if _, self, _, ok = runtime.Caller(1); !ok { t.Fatal() }
	_ = os.Chdir(filepath.Dir(self))
}`, []int{4}},
		{"package-level var", `var _, here, _, _ = runtime.Caller(0)

var testdataDir = filepath.Join(filepath.Dir(here), "testdata")`, []int{1}},
		{"handed to a helper", `func TestX(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	load(t, file)
}`, []int{2}},
		{"copied into another var", `func TestX(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	var dir = filepath.Dir(file)
	_ = dir
}`, []int{2}},
		{"display and path uses mixed", `func TestX(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	t.Logf("running from %s", fmt.Sprint(file))
	os.ReadFile(filepath.Join(filepath.Dir(file), "x"))
}`, []int{2}},
		{"stored in a field", `type s struct{ src string }
func (x *s) init() {
	_, x.src, _, _ = runtime.Caller(0)
}`, []int{3}},
		{"in a closure", `func TestX(t *testing.T) {
	dir := func() string {
		_, f, _, _ := runtime.Caller(0)
		return filepath.Dir(f)
	}()
	_ = dir
}`, []int{3}},
		{"non-test file", `func templatesDir() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(f), "templates")
}`, []int{2}},

		{"shadow display does not excuse unused caller", `func f() {
	_, file, _, _ := runtime.Caller(0)
	{ file := filepath.Base("x"); fmt.Sprint(file) }
}`, []int{2}},
		{"shadow guard does not excuse caller", `func f() {
	_, file, _, _ := runtime.Caller(0)
	{ file := "x"; if filepath.IsAbs(file) { fmt.Sprint(file) } }
	os.ReadFile(file)
}`, []int{2}},
		{"partial guard", `func f() {
	_, file, _, _ := runtime.Caller(0)
	if filepath.IsAbs(file) { fmt.Sprint(file) }
	os.ReadFile(file)
}`, []int{2}},
		{"ignored guard result", `func f() {
	_, file, _, _ := runtime.Caller(0)
	filepath.IsAbs(file)
	os.ReadFile(file)
}`, []int{2}},
		{"guard after unsafe use", `func f() {
	_, file, _, _ := runtime.Caller(0)
	os.ReadFile(file)
	if !filepath.IsAbs(file) { return }
}`, []int{2}},

		// ── not flagged ──────────────────────────────────────────────
		{"shadow path does not taint display", `func f() {
	_, file, _, _ := runtime.Caller(0)
	fmt.Sprint(file)
	{ file := "x"; os.ReadFile(file) }
}`, nil},
		{"closure parameter shadows display", `func f() {
	_, file, _, _ := runtime.Caller(0)
	fmt.Sprint(file)
	func(file string) { os.ReadFile(file) }("x")
}`, nil},
		{"closure capture is unsafe", `func f() {
	_, file, _, _ := runtime.Caller(0)
	func() { os.ReadFile(file) }()
}`, []int{2}},
		{"file discarded", `func line() int {
	_, _, l, _ := runtime.Caller(1)
	return l
}`, nil},
		{"pc only", `func pc() uintptr {
	pc, _, _, _ := runtime.Caller(0)
	return pc
}`, nil},
		{"guarded by filepath.IsAbs", `func devRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok || file == "" || !filepath.IsAbs(file) {
		return ""
	}
	return filepath.Dir(file)
}`, []int{2}},
		{"base name for display", `func where() string {
	_, file, line, _ := runtime.Caller(1)
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}`, nil},
		{"formatted into an error", `func caller() error {
	_, file, line, _ := runtime.Caller(1)
	return errors.New(fmt.Sprint(file, line))
}`, nil},
		{"a field named like the binding", `type loc struct{ file string }
func where() loc {
	_, file, _, _ := runtime.Caller(1)
	return loc{file: fmt.Sprint(file)}
}`, nil},
		{"some other Caller", `type r struct{}
func (r) Caller(int) (uintptr, string, int, bool) { return 0, "", 0, false }
func f() string {
	_, file, _, _ := r{}.Caller(0)
	return filepath.Dir(file)
}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var want []string
			for _, l := range tc.want {
				want = append(want, fmt.Sprintf("%s@%d", RuleID, l))
			}
			if g := got(t, "x_test.go", file(tc.body), offset); !slices.Equal(g, want) {
				t.Errorf("findings = %v, want %v", g, want)
			}
		})
	}
}

func TestCheckFileAliasedImports(t *testing.T) {
	src := `package p

import (
	rt "runtime"
	fp "path/filepath"
)

func dir() string {
	_, f, _, _ := rt.Caller(0)
	return fp.Dir(f)
}

func guarded() string {
	_, f, _, _ := rt.Caller(0)
	if !fp.IsAbs(f) {
		return ""
	}
	return fp.Dir(f)
}
`
	if g, want := got(t, "x.go", src, 0), []string{RuleID + "@9", RuleID + "@14"}; !slices.Equal(g, want) {
		t.Errorf("findings = %v, want %v", g, want)
	}
}

func TestCheckFileSkips(t *testing.T) {
	flagged := file(`func dir() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Dir(f)
}`)
	cases := []struct {
		name string
		src  string
	}{
		{"generated", "// Code generated by x. DO NOT EDIT.\n\n" + flagged},
		{"does not parse", flagged + "\nfunc {"},
		{"no runtime import", "package p\n\nfunc f(r interface{ Caller(int) (uintptr, string, int, bool) }) {\n\t_, file, _, _ := r.Caller(0)\n\t_ = file\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if g := CheckFile("x.go", []byte(tc.src)); len(g) != 0 {
				t.Errorf("findings = %v, want none", g)
			}
		})
	}
}

func TestFindingNamesTheFix(t *testing.T) {
	fs := CheckFile("x_test.go", []byte(file(`func dir() string {
	_, self, _, _ := runtime.Caller(0)
	return filepath.Dir(self)
}`)))
	if len(fs) != 1 {
		t.Fatalf("findings = %v, want one", fs)
	}
	f := fs[0]
	for _, want := range []string{"`self`", "-trimpath", "forge's default build mode", "not a file on disk"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message %q does not name %q", f.Message, want)
		}
	}
	for _, want := range []string{"filepath.Abs(\"testdata/x\")", "go.mod", "filepath.IsAbs", "forge:lint-disable-next-line " + RuleID} {
		if !strings.Contains(f.Remediation, want) {
			t.Errorf("remediation %q does not name %q", f.Remediation, want)
		}
	}
	if f.File != "x_test.go" || f.Severity != "error" {
		t.Errorf("finding = %+v, want File x_test.go, severity error", f)
	}
}
