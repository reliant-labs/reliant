package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/forgereach"
	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestParseConsumer(t *testing.T) {
	cases := []struct {
		spec, name, src, ref string
		wantErr              bool
	}{
		{spec: "reliant=https://github.com/reliant-labs/reliant.git", name: "reliant", src: "https://github.com/reliant-labs/reliant.git", ref: "main"},
		{spec: "r=https://github.com/o/r.git@v1.2", name: "r", src: "https://github.com/o/r.git", ref: "v1.2"},
		{spec: "cp=git@github.com:o/cp.git", name: "cp", src: "git@github.com:o/cp.git", ref: "main"},
		{spec: "cp=git@github.com:o/cp.git@abc123", name: "cp", src: "git@github.com:o/cp.git", ref: "abc123"},
		{spec: "x=/work/reliant", name: "x", src: "/work/reliant", ref: "main"},
		{spec: "x=/work/reliant@feature", name: "x", src: "/work/reliant", ref: "feature"},
		{spec: "noequals", wantErr: true},
		{spec: "=x", wantErr: true},
		{spec: "x=", wantErr: true},
	}
	for _, tc := range cases {
		c, err := ParseConsumer(tc.spec)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: want an error", tc.spec)
			}
			continue
		}
		if err != nil || c.Name != tc.name || c.Source != tc.src || c.Ref != tc.ref {
			t.Errorf("%q: got %+v, %v; want %s %s %s", tc.spec, c, err, tc.name, tc.src, tc.ref)
		}
	}
}

func TestNewFailuresIgnoresTimingsAndCacheMarkers(t *testing.T) {
	base := "ok  \texample.com/c/z\t0.20s\n# example.com/c/a\na/a.go:3:2: undefined: x\nFAIL\texample.com/c/a\t0.31s\n"
	head := "ok  \texample.com/c/y\t9.99s\n# example.com/c/a\na/a.go:3:2: undefined: x\nFAIL\texample.com/c/a\t1.02s\n"
	if f := NewFailures(head, base); len(f) != 0 {
		t.Fatalf("the same failure with different timings is not new: %v", f)
	}
	head += "b/b.go:9:1: too many arguments in call to forge.Open\n"
	f := NewFailures(head, base)
	if len(f) != 1 || !strings.Contains(f[0], "too many arguments") {
		t.Fatalf("want exactly the new compile error, got %v", f)
	}
}

func TestForgeKeyIgnoresWhatACannotChangeAConsumer(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module m\n")
	write("pkg/a/a.go", "package a\n")
	k1, err := forgeKey(root)
	if err != nil {
		t.Fatal(err)
	}
	write("pkg/a/a_test.go", "package a\n")
	write("README.md", "x\n")
	write("pkg/a/testdata/x.go", "package x\n")
	if k2, _ := forgeKey(root); k2 != k1 {
		t.Fatal("tests, docs and testdata must not change the key")
	}
	write("pkg/a/a.go", "package a\n\nfunc A() {}\n")
	if k3, _ := forgeKey(root); k3 == k1 {
		t.Fatal("a source change must change the key")
	}
}

// A local forge module and a local consumer repository, so the run needs no
// network and reads neither a real forge nor a real consumer.
type fixture struct {
	forge, pinned, consumer, cache string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{forge: filepath.Join(root, "forge"), pinned: filepath.Join(root, "pinned"), consumer: filepath.Join(root, "consumer"), cache: filepath.Join(root, "cache")}
	put := func(dir, rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(f.forge, "go.mod", "module example.com/forge\n\ngo 1.27\n")
	put(f.forge, "pkg/widget/widget.go", "package widget\n\nfunc Open(name string) int { return len(name) }\n")
	git(t, f.forge, "init", "-q")
	git(t, f.forge, "add", "-A")
	git(t, f.forge, "commit", "-q", "-m", "base")

	// The forge the consumer pins: a copy of the base, standing in for a
	// released version, so editing f.forge (the PR) never changes the baseline.
	put(f.pinned, "go.mod", "module example.com/forge\n\ngo 1.27\n")
	put(f.pinned, "pkg/widget/widget.go", "package widget\n\nfunc Open(name string) int { return len(name) }\n")

	put(f.consumer, "go.mod", "module example.com/consumer\n\ngo 1.27\n\nrequire example.com/forge v0.0.0\n\nreplace example.com/forge => "+f.pinned+"\n")
	put(f.consumer, "app/app.go", "package app\n\nimport \"example.com/forge/pkg/widget\"\n\nfunc N() int { return widget.Open(\"x\") }\n")
	put(f.consumer, "app/app_test.go", "package app\n\nimport \"testing\"\n\nfunc TestN(t *testing.T) {\n\tif N() != 1 {\n\t\tt.Fatal(N())\n\t}\n}\n")
	put(f.consumer, "other/other.go", "package other\n\nfunc Z() int { return 0 }\n")
	git(t, f.consumer, "init", "-q")
	git(t, f.consumer, "add", "-A")
	git(t, f.consumer, "commit", "-q", "-m", "main")
	git(t, f.consumer, "branch", "-M", "main")
	return f
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func (f fixture) edit(t *testing.T, rel, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(f.forge, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.forge, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) canary(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	args := append([]string{"-forge", f.forge, "-base", "HEAD", "-cache", f.cache, "-consumer", "c=" + f.consumer}, extra...)
	err := run(context.Background(), args, &out, &out)
	return out.String(), err
}

func slow(t *testing.T) {
	t.Helper()
	testkit.Slow(t, "builds and tests a scratch consumer module with the go tool",
		testkit.Inputs("internal/consumercanary/**", "internal/forgereach/**"))
}

func TestBreakingForgeChangeFailsTheConsumer(t *testing.T) {
	slow(t)
	f := newFixture(t)
	f.edit(t, "pkg/widget/widget.go", "package widget\n\nfunc Open(name string, mode int) int { return len(name) + mode }\n")
	out, err := f.canary(t)
	if err == nil {
		t.Fatalf("a signature change the consumer calls must fail:\n%s", out)
	}
	for _, want := range []string{"FAIL", "go build ./... broke", "not enough arguments"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if st, _ := os.Stat(f.consumer); st == nil {
		t.Fatal("the consumer repository must be left alone")
	}
	if out, _ := exec.Command("git", "-C", f.consumer, "status", "--porcelain").Output(); len(out) != 0 {
		t.Fatalf("the consumer working tree was modified:\n%s", out)
	}
}

func TestCompatibleForgeChangePassesAndRunsOnlyAffectedTests(t *testing.T) {
	slow(t)
	f := newFixture(t)
	f.edit(t, "pkg/widget/widget.go", "package widget\n\nfunc Open(name string) int { return len(name) }\n\nfunc Peek() int { return 0 }\n")
	out, err := f.canary(t)
	if err != nil {
		t.Fatalf("an additive change must pass: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 package(s) import a changed forge package") {
		t.Errorf("only example.com/consumer/app imports pkg/widget:\n%s", out)
	}
	out, err = f.canary(t)
	if err != nil || !strings.Contains(out, "PASS (cached)") {
		t.Fatalf("an identical second run must be a cache hit: %v\n%s", err, out)
	}
}

func TestChangeThatReachesNoConsumerIsSkipped(t *testing.T) {
	slow(t)
	f := newFixture(t)
	f.edit(t, "README.md", "docs\n")
	out, err := f.canary(t)
	if err != nil || !strings.Contains(out, "skipped") {
		t.Fatalf("a docs-only diff must skip: %v\n%s", err, out)
	}
	f.edit(t, "internal/cli/x.go", "package cli\n")
	out, err = f.canary(t)
	if err != nil || !strings.Contains(out, "skipped") {
		t.Fatalf("a package outside pkg/'s import graph must skip: %v\n%s", err, out)
	}
}

func TestFailureThatPredatesTheChangeIsNotBlamedOnIt(t *testing.T) {
	slow(t)
	f := newFixture(t)
	// The consumer's main is already red: a test that fails whatever forge is.
	if err := os.WriteFile(filepath.Join(f.consumer, "app", "app_test.go"),
		[]byte("package app\n\nimport \"testing\"\n\nfunc TestN(t *testing.T) { t.Fatal(\"red on main\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, f.consumer, "commit", "-q", "-am", "red")
	f.edit(t, "pkg/widget/widget.go", "package widget\n\nfunc Open(name string) int { return len(name) }\n\nfunc Peek() int { return 0 }\n")
	out, err := f.canary(t)
	if err != nil {
		t.Fatalf("a failure the base forge has too must not fail the PR: %v\n%s", err, out)
	}
	if !strings.Contains(out, "predate") {
		t.Errorf("a failure that predates the change must still be reported:\n%s", out)
	}
}

func TestReachFindsPackagesTheConsumerImports(t *testing.T) {
	slow(t)
	f := newFixture(t)
	f.edit(t, "pkg/widget/widget.go", "package widget\n\nfunc Open(name string) int { return 0 }\n")
	changed, err := forgereach.ChangedFiles(f.forge, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	r, err := forgereach.Compute(f.forge, "HEAD", changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Packages) != 1 || r.Packages[0] != "example.com/forge/pkg/widget" || !r.API() {
		t.Fatalf("reach = %+v", r)
	}
}

func TestPassingPackagesAreNeverFailures(t *testing.T) {
	out := "ok  \texample.com/c/a\t1.2s\n?   \texample.com/c/b\t[no test files]\n--- FAIL: TestX (0.01s)\nFAIL\texample.com/c/a\t0.5s\n"
	if f := NewFailures(out, ""); len(f) != 2 {
		t.Fatalf("want the --- FAIL and FAIL lines only, got %v", f)
	}
	if got := failureSummary("only noise\n"); got != "only noise\n" {
		t.Errorf("a failure no pattern knows must still be shown, got %q", got)
	}
}

func TestForcedRunBlamesEveryFailure(t *testing.T) {
	slow(t)
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.consumer, "app", "app_test.go"),
		[]byte("package app\n\nimport \"testing\"\n\nfunc TestN(t *testing.T) { t.Fatal(\"red on main\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, f.consumer, "commit", "-q", "-am", "red")
	out, err := f.canary(t, "-force")
	if err == nil {
		t.Fatalf("a forced run has no diff to blame: a red consumer must fail it:\n%s", out)
	}
}

func TestBreakAlreadyOnTheBaseIsNotBlamedOnAnUnrelatedChange(t *testing.T) {
	slow(t)
	f := newFixture(t)
	// forge's base already breaks the consumer (it removed Open's string arg)...
	f.edit(t, "pkg/widget/widget.go", "package widget\n\nfunc Open(name string, mode int) int { return len(name) + mode }\n")
	git(t, f.forge, "commit", "-q", "-am", "main broke the consumer")
	// ...and the PR only adds an unrelated function.
	f.edit(t, "pkg/widget/extra.go", "package widget\n\nfunc Extra() int { return 1 }\n")
	out, err := f.canary(t)
	if err != nil {
		t.Fatalf("the PR did not cause this break; it must not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "predate") {
		t.Errorf("a break the base already had must still be reported:\n%s", out)
	}
}

func TestFailedPackagesAreReadFromTheVerdictLines(t *testing.T) {
	out := "ok  \texample.com/a\t1s\n--- FAIL: TestX (0.1s)\nFAIL\nFAIL\texample.com/b\t0.5s\nFAIL\texample.com/b\t0.5s\nFAIL\texample.com/c [build failed]\n"
	got := failedPackages(out)
	if len(got) != 2 || got[0] != "example.com/b" || got[1] != "example.com/c" {
		t.Fatalf("got %v, want [example.com/b example.com/c]", got)
	}
}
