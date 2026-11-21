package lint

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestSplitBufFormatDiff(t *testing.T) {
	out := `diff -u proto/a/v1/a.proto.orig proto/a/v1/a.proto
--- proto/a/v1/a.proto.orig	2026-11-17 22:23:40.644332794 +0000
+++ proto/a/v1/a.proto	2026-11-17 22:23:40.644332794 +0000
@@ -1,2 +1,2 @@
-import "b.proto";
+import "a.proto";
diff -u proto/b/v1/b.proto.orig proto/b/v1/b.proto
--- proto/b/v1/b.proto.orig	2026-11-17 22:23:40.644332794 +0000
+++ proto/b/v1/b.proto	2026-11-17 22:23:40.644332794 +0000
@@ -3 +3 @@
-message   B {}
+message B {}
`
	got := splitBufFormatDiff(out)
	if len(got) != 2 {
		t.Fatalf("want 2 files, got %d: %+v", len(got), got)
	}
	if got[0].File != "proto/a/v1/a.proto" || got[1].File != "proto/b/v1/b.proto" {
		t.Errorf("files = %q, %q", got[0].File, got[1].File)
	}
	if !strings.Contains(got[0].Diff, `+import "a.proto";`) || strings.Contains(got[0].Diff, "message B") {
		t.Errorf("a.proto's diff is not its own:\n%s", got[0].Diff)
	}
	if !strings.Contains(got[1].Diff, "+message B {}") {
		t.Errorf("b.proto's diff is missing its hunk:\n%s", got[1].Diff)
	}
	if splitBufFormatDiff("") != nil {
		t.Error("no diff must be no pending files")
	}
}

// The diff under a finding is graded with it: a pin still in the rule's grace
// period sees a warning and a passing lane, lint.rules can turn the whole
// report off, and past GatesFrom it gates.
func TestProtoFormatDiffIsGradedWithItsFinding(t *testing.T) {
	// Graded through a synthetic row: the rule's own grace window closed.
	ro := ruleRollout{IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1"}
	withRollouts(t, map[string]ruleRollout{ruleProtoFormat: ro})
	findings := func() []lintJSONFinding {
		return []lintJSONFinding{
			{File: "proto/a.proto", Severity: lintSevError, Rule: ruleProtoFormat, Message: "would reformat"},
			{Severity: lintSevError, Rule: "external", Message: "-a\n+b", rollout: ruleProtoFormat},
		}
	}
	if fs, gated := gradeProjectLane(pinnedCfg(ro.IntroducedIn, nil), findings(), true); gated || anyErrorFinding(fs) {
		t.Errorf("pin %s (before GatesFrom %s): the lane still gates: %+v", ro.IntroducedIn, ro.GatesFrom, fs)
	}
	if fs, gated := gradeProjectLane(pinnedCfg(ro.IntroducedIn, map[string]string{ruleProtoFormat: "off"}), findings(), true); gated || len(fs) != 0 {
		t.Errorf("lint.rules off: findings survive: %+v", fs)
	}
	if _, gated := gradeProjectLane(pinnedCfg(ro.GatesFrom, nil), findings(), true); !gated {
		t.Errorf("pin %s: the lane must gate", ro.GatesFrom)
	}
}

// `--fix --scope proto/a` rewrites the scoped proto and nothing else, on both
// roads to the lane: the targeted `forge lint --proto-format` (whose driver
// narrows the report only AFTER the lane ran) and the lane inside a bare
// `forge lint`'s pipeline. A shared checkout holds other agents' protos; a
// scoped fix that formatted them would rewrite work it was told to leave.
func TestProtoFormatFixStaysInsideTheScope(t *testing.T) {
	testkit.Slow(t, "execs buf")
	if _, err := exec.LookPath("buf"); err != nil {
		t.Skip("buf not on PATH — the proto-format lane needs buf (`forge tools install buf`)")
	}
	const dirty = "syntax = \"proto3\";\npackage %s.v1;\nmessage   M {string id = 1;}\n"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "buf.yaml"), []byte("version: v2\nmodules:\n  - path: proto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inside, outside := "proto/a/v1/a.proto", "proto/b/v1/b.proto"
	reset := func() {
		for rel, pkg := range map[string]string{inside: "a", outside: "b"} {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(strings.Replace(dirty, "%s", pkg, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	reset() // the scope must name a path that exists
	scope, err := parseLintScope(root, []string{"proto/a"})
	if err != nil {
		t.Fatal(err)
	}
	check := func(road string) {
		t.Helper()
		if got := read(inside); strings.Contains(got, "message   M") {
			t.Errorf("%s: the scoped proto was not formatted:\n%s", road, got)
		}
		if got, want := read(outside), strings.Replace(dirty, "%s", "b", 1); got != want {
			t.Errorf("%s: a proto OUTSIDE --scope was rewritten:\n%s", road, got)
		}
	}

	reset()
	o, err := collectLaneOutcome(context.Background(), lintFlags{protoFormat: true, fix: true}, "proto-format",
		[]string{"./..."}, root, nil, nil, scope)
	if err != nil || !o.ran {
		t.Fatalf("targeted lane: ran=%v err=%v", o.ran, err)
	}
	check("forge lint --proto-format --fix --scope proto/a")

	reset()
	step := findStep(t, "proto-format lint")
	if step.scope != scopeByFile {
		t.Fatalf("proto-format lint must be scopeByFile, is %v", step.scope)
	}
	outcomes := collectLintOutcomesFrom(&lintRunCtx{ctx: context.Background(), fix: true, cwd: root, scope: scope,
		paths: []string{"./..."}}, []linterStep{step})
	if len(outcomes) != 1 || !outcomes[0].ran {
		t.Fatalf("pipeline lane did not run: %+v", outcomes)
	}
	check("forge lint --fix --scope proto/a")
}

// The lane runs the pre-commit hook's check: a proto buf would rewrite fails
// it with buf's diff, --fix writes buf's output, and a project with no
// buf.yaml has no lane.
func TestProtoFormatLane(t *testing.T) {
	testkit.Slow(t, "execs buf")
	if _, err := exec.LookPath("buf"); err != nil {
		t.Skip("buf not on PATH — the proto-format lane needs buf (`forge tools install buf`)")
	}
	ctx := context.Background()
	root := t.TempDir()
	if fs, gated, err := collectProtoFormatJSON(ctx, root, nil, false); err != nil || gated || len(fs) != 0 {
		t.Fatalf("no buf.yaml: want no findings, got %v gated=%v err=%v", fs, gated, err)
	}
	if err := os.WriteFile(filepath.Join(root, "buf.yaml"), []byte("version: v2\nmodules:\n  - path: proto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "proto", "a", "v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	clean := "syntax = \"proto3\";\n\npackage a.v1;\n\nmessage A {\n  string id = 1;\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "clean.proto"), []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dirty.proto"), []byte("syntax = \"proto3\";\npackage a.v1;\nmessage   B {string id = 1;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fs, gated, err := collectProtoFormatJSON(ctx, root, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !gated || len(fs) != 2 || fs[0].File != "proto/a/v1/dirty.proto" || fs[0].Rule != ruleProtoFormat {
		t.Fatalf("want one forge-proto-format finding on dirty.proto plus its diff, got gated=%v %+v", gated, fs)
	}
	if !strings.Contains(fs[1].Message, "+message B {") {
		t.Errorf("the finding's diff is not buf's:\n%s", fs[1].Message)
	}

	if fs, _, err := collectProtoFormatJSON(ctx, root, nil, true); err != nil || len(fs) != 0 {
		t.Fatalf("--fix: want no findings, got %+v (%v)", fs, err)
	}
	if fs, gated, _ := collectProtoFormatJSON(ctx, root, nil, false); gated || len(fs) != 0 {
		t.Errorf("after --fix the lane still fails: %+v", fs)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "clean.proto")); string(b) != clean {
		t.Errorf("--fix rewrote a clean file:\n%s", b)
	}
}
