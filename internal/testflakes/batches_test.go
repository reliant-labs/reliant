package testflakes

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// wideChange is a selection like reliant#3723's: many packages whose names
// together pass one -run argument, one of them past it on its own.
func wideChange() ChangedTests {
	var c ChangedTests
	for p := range 17 {
		var names []string
		n := 12
		if p == 3 {
			n = 400 // ~12 KiB of names alone: split
		}
		for i := range n {
			names = append(names, fmt.Sprintf("TestPackage%02d_SomethingFairlyDescriptive_%03d", p, i))
		}
		c.Packages = append(c.Packages, ChangedPackage{Rel: fmt.Sprintf("internal/p%02d", p), Names: names})
	}
	return c
}

// Every selected (package, test) pair runs in an invocation of that package
// whose -run selects it; no -run passes the bound, and none selects every test.
func TestBatchesRunEverySelectedTestAndNeverEveryTest(t *testing.T) {
	c := wideChange()
	if c.RunRegex() != "^(Test|Fuzz)" {
		t.Fatal("the fixture must not fit one -run argument")
	}
	batches := c.Batches(0)
	if len(batches) < 3 {
		t.Fatalf("batches = %d, want the selection split", len(batches))
	}
	for _, b := range batches {
		if len(b.Run) > MaxRunBytes {
			t.Errorf("a -run of %d bytes", len(b.Run))
		}
		if b.Run == "^(Test|Fuzz)" || !strings.HasSuffix(b.Run, ")$") {
			t.Errorf("run = %.40s…, want the selected names only", b.Run)
		}
		if len(b.Packages) != len(slices.Compact(slices.Clone(b.Packages))) {
			t.Errorf("a package twice in one invocation: %v", b.Packages)
		}
	}
	for _, p := range c.Packages {
		for _, name := range p.Names {
			found := false
			for _, b := range batches {
				if slices.Contains(b.Packages, "./"+p.Rel) && regexp.MustCompile(b.Run).MatchString(name) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s of %s runs in no invocation of its package", name, p.Rel)
			}
		}
	}
	// The package too large for one argument is split, alone in each part.
	parts := 0
	for _, b := range batches {
		if slices.Contains(b.Packages, "./internal/p03") {
			parts++
		}
	}
	if parts < 2 {
		t.Errorf("internal/p03 ran in %d invocation(s), want it split", parts)
	}
}

// A selection that fits is one invocation, as it always was.
func TestBatchesSmallSelectionIsOneInvocation(t *testing.T) {
	c := ChangedTests{Packages: []ChangedPackage{{Rel: ".", Names: []string{"TestRoot"}}, {Rel: "internal/a", Names: []string{"TestNew", "FuzzParse"}}}}
	b := c.Batches(0)
	if len(b) != 1 || b[0].Run != c.RunRegex() || strings.Join(b[0].Packages, " ") != "./. ./internal/a" {
		t.Fatalf("batches = %+v", b)
	}
}

// The gate runs one invocation per batch; one failing fails the gate, and the
// reproduce line names only the failing invocation.
func TestGateSplitsALargeSelectionAcrossInvocations(t *testing.T) {
	files := map[string]string{}
	git := map[string]string{}
	var changed []string
	for _, pkg := range []string{"a", "b"} {
		n := 5
		if pkg == "a" {
			n = 300
		}
		var src, diff strings.Builder
		src.WriteString("package " + pkg + "\n\nimport \"testing\"\n")
		for i := range n {
			name := fmt.Sprintf("Test%s_ALongDescriptiveNameForTheSplit_%03d", strings.ToUpper(pkg), i)
			fmt.Fprintf(&src, "\nfunc %s(t *testing.T) {}\n", name)
			fmt.Fprintf(&diff, "+func %s(t *testing.T) {}\n", name)
		}
		rel := "internal/" + pkg + "/" + pkg + "_test.go"
		files[rel], git[rel] = src.String(), diff.String()
		changed = append(changed, rel)
	}
	root := newModule(t, files)
	r := &fakeRunner{script: func(argv []string) (string, int) {
		if slices.Contains(argv, "./internal/b") {
			return ev("fail", testModule+"/internal/b", "", ""), 1
		}
		return ev("pass", testModule+"/internal/a", "", ""), 0
	}}
	o, out := gateOpts(t, root, r, fakeGit(root, strings.Join(changed, "\n"), git), listOf(root, "internal/a", "internal/b"))
	res := Gate(context.Background(), o)
	if len(r.calls) < 2 || len(res.Argvs) != len(r.calls) || !slices.Equal(res.Argv, r.calls[0]) {
		t.Fatalf("invocations = %d, argvs = %d\n%s", len(r.calls), len(res.Argvs), out)
	}
	for _, argv := range r.calls {
		if run := argAfter(argv, "-run"); len(run) > MaxRunBytes || run == "^(Test|Fuzz)" {
			t.Errorf("-run = %.40s… (%d bytes)", run, len(run))
		}
		if argAfter(argv, "-timeout") != "10m" {
			t.Errorf("each invocation keeps its own budget: %v", argv[:8])
		}
	}
	if res.ExitCode != GateFailed {
		t.Fatalf("exit = %d (%s)", res.ExitCode, res.Message)
	}
	// internal/b shares the last invocation with the tail of a's names; the
	// passing first invocation (a's first names) is not in the reproduce line.
	if !strings.Contains(res.Message, "./internal/b") || strings.Contains(res.Message, "TestA_ALongDescriptiveNameForTheSplit_000") {
		t.Errorf("reproduce names only the failing invocation: %.300s", res.Message)
	}
	if !strings.Contains(out.String(), "go test invocations") {
		t.Errorf("the split is said: %s", out)
	}
}
