package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const changedTestsSrc = `package leaf

import "testing"

func helper() int { return 1 }

func TestUntouched(t *testing.T) {
	_ = helper()
}

func TestEdited(t *testing.T) {
	if helper() != 1 {
		t.Fatal("no")
	}
}

func TestLostALine(t *testing.T) {
	_ = 1
}

func BenchmarkEdited(b *testing.B) {}

func TestNotATest(x int) {}
`

func TestParseZeroContextDiff(t *testing.T) {
	diff := `diff --git a/internal/leaf/leaf_test.go b/internal/leaf/leaf_test.go
--- a/internal/leaf/leaf_test.go
+++ b/internal/leaf/leaf_test.go
@@ -12 +12,2 @@ func TestEdited(t *testing.T) {
@@ -20,2 +21,0 @@ func TestLostALine(t *testing.T) {
diff --git a/internal/gone_test.go b/internal/gone_test.go
--- a/internal/gone_test.go
+++ /dev/null
@@ -1,3 +0,0 @@
`
	got := parseZeroContextDiff(diff)
	want := map[string][]lineRange{
		"internal/leaf/leaf_test.go": {{12, 13}, {21, 22}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSelectChangedTestsPicksTouchedTestFuncsOnly(t *testing.T) {
	files := map[string][]byte{"internal/leaf/leaf_test.go": []byte(changedTestsSrc)}
	read := func(p string) ([]byte, bool) { b, ok := files[p]; return b, ok }
	builds := func(string) bool { return true }

	touched := map[string][]lineRange{
		// Line 12 is inside TestEdited; 19-20 straddle TestLostALine's closing
		// brace; 5 is the helper; 21 is the benchmark; 23 is a Test-named
		// func that is not a test.
		"internal/leaf/leaf_test.go": {{12, 12}, {19, 20}, {5, 5}, {21, 21}, {23, 23}},
		"internal/leaf/leaf.go":      {{1, 3}},
		"internal/gone/gone_test.go": {{1, 1}},
	}
	got, err := selectChangedTests(touched, read, builds)
	if err != nil {
		t.Fatal(err)
	}
	want := []changedPkg{{Dir: "./internal/leaf", Tests: []string{"TestEdited", "TestLostALine"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if p := got[0].runPattern(); p != "^(TestEdited|TestLostALine)$" {
		t.Errorf("runPattern = %q", p)
	}
}

func TestSelectChangedTestsSkipsFilesBehindBuildTags(t *testing.T) {
	files := map[string][]byte{"e2e/stories/s_test.go": []byte(changedTestsSrc)}
	read := func(p string) ([]byte, bool) { b, ok := files[p]; return b, ok }
	got, err := selectChangedTests(map[string][]lineRange{"e2e/stories/s_test.go": {{1, 1 << 30}}}, read, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a tagged file does not build in the unit lane; got %+v", got)
	}
}

// A package in a nested Go module (sdk/go) is not in the root module, so
// `go test ./sdk/go/reliant` from the root fails before running anything. It
// is reported relative to its own module, with the module's directory.
func TestChangedTestsInANestedModuleAreRelativeToIt(t *testing.T) {
	moduleOf := func(dir string) string {
		if dir == "sdk/go" || strings.HasPrefix(dir, "sdk/go/") {
			return "sdk/go"
		}
		return ""
	}
	got := inNestedModules([]changedPkg{
		{Dir: "./internal/leaf", Tests: []string{"TestA"}},
		{Dir: "./sdk/go", Tests: []string{"TestB"}},
		{Dir: "./sdk/go/reliant/follow", Tests: []string{"TestC"}},
		{Dir: "./sdk/gox", Tests: []string{"TestD"}},
	}, moduleOf)
	want := []changedPkg{
		{Dir: "./internal/leaf", Tests: []string{"TestA"}},
		{Dir: ".", Tests: []string{"TestB"}, Module: "sdk/go"},
		{Dir: "./reliant/follow", Tests: []string{"TestC"}, Module: "sdk/go"},
		{Dir: "./sdk/gox", Tests: []string{"TestD"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestNestedModuleOfFindsTheNearestGoModBelowTheRoot(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"go.mod", "sdk/go/go.mod", "sdk/go/reliant/x.go", "internal/leaf/x.go"} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	of := nestedModuleOf(root)
	for dir, want := range map[string]string{
		"sdk/go/reliant": "sdk/go", "sdk/go": "sdk/go", "internal/leaf": "", ".": "", "sdk": "",
	} {
		if got := of(dir); got != want {
			t.Errorf("nestedModuleOf(%q) = %q, want %q", dir, got, want)
		}
	}
}
