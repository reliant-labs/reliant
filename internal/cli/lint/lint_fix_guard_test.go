package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The reliant P5b incident, reduced: a "fix" for SA9004 gives every constant
// in a mixed block the first one's type, and a use that relied on the others
// being untyped stops compiling. A fix like that must not be left applied.
const (
	sa9004Before = "package yaml\n\ntype FullName string\n\nconst (\n\tTriggerMessage FullName = \"reliant.v1.Trigger\"\n\tFieldName               = \"name\"\n)\n\nfunc Key() string { return FieldName + \".\" + string(TriggerMessage) }\n"
	sa9004After  = "package yaml\n\ntype FullName string\n\nconst (\n\tTriggerMessage FullName = \"reliant.v1.Trigger\"\n\tFieldName      FullName = \"name\"\n)\n\nfunc Key() string { return FieldName + \".\" + string(TriggerMessage) }\n"
	cleanBefore  = "package other\n\nfunc   Fine() {}\n"
	cleanAfter   = "package other\n\nfunc Fine() {}\n"
)

func newGuardModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/guard\n\ngo 1.22\n")
	writeFile(t, root, "internal/workflow/yaml/triggers.go", sa9004Before)
	writeFile(t, root, "internal/other/other.go", cleanBefore)
	return root
}

func applyGuardFixes(t *testing.T, root string, set *goFixSet) {
	t.Helper()
	for rel, data := range map[string]string{
		"internal/workflow/yaml/triggers.go": sa9004After,
		"internal/other/other.go":            cleanAfter,
	} {
		if err := set.write(filepath.Join(root, filepath.FromSlash(rel)), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readRel(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// With the real type-check: the breaking fix is taken back, its file is the
// author's again, and the harmless fix in another package stands.
func TestGoFixGuard_FixThatBreaksTheBuildIsNotApplied(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks a temp module through go list (a subprocess); runs in task test")
	}
	root := newGuardModule(t)
	set := newGoFixSet(root)
	applyGuardFixes(t, root, set)

	reverted := set.settle(typecheckGoPackages)

	if got := readRel(t, root, "internal/workflow/yaml/triggers.go"); got != sa9004Before {
		t.Fatalf("the fix that broke the package was left applied:\n%s", got)
	}
	if got := readRel(t, root, "internal/other/other.go"); got != cleanAfter {
		t.Fatalf("a fix that type-checks was taken back:\n%s", got)
	}
	if len(reverted) != 1 || reverted[0].File != "internal/workflow/yaml/triggers.go" || reverted[0].Reason == "" {
		t.Fatalf("reverted = %+v, want the one triggers.go fix with the compiler's reason", reverted)
	}
}

// The rule without a subprocess: a package that type-checked before the fix
// and not after gets its bytes back; one that already failed keeps the fix
// (withholding it would repair nothing); one that passes is untouched.
func TestGoFixGuard_RollsBackOnlyWhatTheFixBroke(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "broke/a.go", "before-a")
	writeFile(t, root, "already/b.go", "before-b")
	writeFile(t, root, "fine/c.go", "before-c")
	set := newGoFixSet(root)
	for _, rel := range []string{"broke/a.go", "already/b.go", "fine/c.go"} {
		if err := set.write(filepath.Join(root, rel), []byte("after"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A fake checker: a dir fails when it holds "after" bytes (broke/), or
	// always (already/).
	check := func(root string, dirs []string) map[string]string {
		out := map[string]string{}
		for _, d := range dirs {
			switch filepath.Base(d) {
			case "already":
				out[d] = "pre-existing error"
			case "broke":
				if b, _ := os.ReadFile(filepath.Join(d, "a.go")); string(b) == "after" {
					out[d] = "undefined: x"
				}
			}
		}
		return out
	}
	reverted := set.settle(check)
	if got := readRel(t, root, "broke/a.go"); got != "before-a" {
		t.Errorf("broke/a.go = %q, want its original bytes", got)
	}
	if got := readRel(t, root, "already/b.go"); got != "after" {
		t.Errorf("already/b.go = %q: a package failing before the fix keeps it", got)
	}
	if got := readRel(t, root, "fine/c.go"); got != "after" {
		t.Errorf("fine/c.go = %q: a fix that type-checks stands", got)
	}
	if len(reverted) != 1 || reverted[0].File != "broke/a.go" || !strings.Contains(reverted[0].Reason, "undefined") {
		t.Errorf("reverted = %+v", reverted)
	}
}

// golangci-lint is never asked to fix, whatever the mode: its fixes are the
// staticcheck/gocritic rewrites (SA9004) that broke reliant's build.
func TestGolangciGateArgs_NeverFix(t *testing.T) {
	for _, fix := range []bool{false, true} {
		for _, a := range golangciGateArgs(&lintRunCtx{fix: fix, paths: []string{"./internal/..."}}) {
			if a == "--fix" || strings.HasPrefix(a, "--fix=") {
				t.Fatalf("golangci-lint gate args carry %s with rc.fix=%v", a, fix)
			}
		}
	}
}

// The structured driver (--quiet, --scope, --json) runs the same never-fix
// invocation: `forge lint --fix --quiet` used to hand golangci-lint --fix.
func TestCollectGolangciLintJSON_NeverFix(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake golangci-lint through a shell subprocess; runs in task test")
	}
	fakeGolangciLint(t, `for a in "$@"; do
  case "$a" in --fix|--fix=*) echo "golangci-lint was asked to fix" >&2; exit 1;; esac
done
echo "0 issues."`)
	rc := testRunCtx(t, false)
	rc.fix = true
	if fs, gated := collectGolangciLintJSON(rc); gated || len(fs) != 0 {
		t.Fatalf("structured golangci-lint lane under --fix: gated=%v findings=%+v", gated, fs)
	}
}
