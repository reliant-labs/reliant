package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// gocritic's appendAssign flags `a = append(b, x)`, which is usually a typo
// for `b = append(b, x)`, so the scaffolded config keeps it on. Its false
// positive is buffer reuse: ranger's balancer wrote
// `x.reps = append(best.reps[:0], reps...)` to append into another slice's
// backing array on purpose, and had to restructure the code to get past the
// gate. The scaffolded config excludes exactly that shape: a `[:0]` base and
// a single spread argument — a whole slice copied into a reused buffer.
// `a = append(b[:0], x)` is still reported.

func renderGolangciTemplate(t *testing.T) string {
	t.Helper()
	out, err := ProjectTemplates().Render("golangci.yml.tmpl", struct {
		Module, TypedAccessGuard, LoaderPackage string
	}{"example.com/fx", "", ""})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// The exclusion's source regex, applied the way golangci-lint applies it (to
// the finding's line), accepts the reuse idiom and nothing else.
func TestGolangciTemplate_AppendAssignExcludesOnlyBufferReuse(t *testing.T) {
	t.Parallel()
	cfg := renderGolangciTemplate(t)
	m := regexp.MustCompile(`(?m)text: "appendAssign"\n\s+source: '([^']+)'`).FindStringSubmatch(cfg)
	if m == nil {
		t.Fatalf("the scaffolded .golangci.yml has no appendAssign exclusion scoped by source:\n%s", cfg)
	}
	source := regexp.MustCompile(m[1])
	for line, excluded := range map[string]bool{
		"\tx.reps = append(best.reps[:0], reps...)": true,
		"\tbuf = append(scratch[:0], b...)":         true,
		"\tw.out = append( w.buf[:0] , p... )":      true,
		"\ta = append(b[:0], 1)":                    false,
		"\ta = append(b[:0], x, y)":                 false,
		"\ta = append(b[:0], x, ys...)":             false,
		"\ta = append(b, 1)":                        false,
		"\ta = append(b[1:], 1)":                    false,
		"\ta = append(f(b[:0]), 1)":                 false,
	} {
		if got := source.MatchString(line); got != excluded {
			t.Errorf("source %q on %q = %v, want %v", m[1], line, got, excluded)
		}
	}
}

// The pinned golangci-lint, under the scaffolded config, passes the reuse
// idiom and still reports the typo.
func TestGolangciTemplate_AppendAssignUnderGolangciLint(t *testing.T) {
	if testing.Short() {
		t.Skip("runs golangci-lint over a fixture module (seconds); runs in task test")
	}
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skip("golangci-lint not on PATH")
	}
	dir := t.TempDir()
	files := map[string]string{
		".golangci.yml": renderGolangciTemplate(t),
		"go.mod":        "module example.com/fx\n\ngo 1.24\n",
		"fx.go": `// Package fx holds one false positive and one real finding.
package fx

type best struct{ reps []int }

// State reuses best's backing array.
type State struct {
	reps []int
	b    best
}

// Reuse appends into b's backing array on purpose.
func (x *State) Reuse(reps []int) {
	x.reps = append(x.b.reps[:0], reps...)
}

// Typo is the defect appendAssign exists for.
func Typo(a, b []int) []int {
	a = append(b, 1)
	return a
}

// TypoOnReslice appends one value to a reset b and keeps it as a.
func TypoOnReslice(a, b []int) []int {
	a = append(b[:0], 2)
	return a
}
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("golangci-lint", "run", "--enable-only=gocritic", "./...")
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	out, _ := cmd.CombinedOutput()
	report := string(out)
	if !strings.Contains(report, "fx.go:19") || !strings.Contains(report, "appendAssign") {
		t.Fatalf("the typo on line 19 must still be reported (or the lane never ran):\n%s", report)
	}
	if !strings.Contains(report, "fx.go:25") {
		t.Errorf("a single value appended to a [:0] reslice of another slice (line 25) must still be reported:\n%s", report)
	}
	if strings.Contains(report, "fx.go:14") {
		t.Errorf("the buffer reuse on line 14 is reported:\n%s", report)
	}
}
