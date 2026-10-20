package main

import (
	"reflect"
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
