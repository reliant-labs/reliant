package deadcodeguard

import (
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// scanWith visits one target OS at a time and drops each load before the next
// (holding all three whole-program loads at once is what OOM-killed the CI
// Test job). That is only sound if the facts merge exactly however the loads
// interleave. These tests pin the three properties it rests on, against the
// scan's own bookkeeping rather than a whole-repo load.

func newTestScan() *scan {
	return &scan{
		root:      "/repo",
		judge:     "example.com/m/internal/",
		fields:    map[fieldKey]*fieldFacts{},
		counted:   map[string]bool{},
		generated: map[string]bool{},
	}
}

// A use recorded BEFORE any load declared its field judgeable must still
// count once a later load does. With a host-first visit order this is the
// common case for a field whose judgeable declaration arrives later.
func TestScanMerge_UseBeforeDeclarationIsKept(t *testing.T) {
	s := newTestScan()
	k := fieldKey{"example.com/m/internal/p", "T", "F"}

	s.record(k, token.Position{Filename: "/repo/internal/p/read.go", Offset: 40, Line: 7}, false, false)

	f := s.facts(k)
	f.judged, f.decl = true, token.Position{Filename: "/repo/internal/p/t.go", Line: 3}

	got := s.phantomFields()
	if len(got) != 1 || got[0].Key != "example.com/m/internal/p.T.F" {
		t.Fatalf("want one phantom-field finding for example.com/m/internal/p.T.F, got %v", got)
	}
	if !strings.Contains(got[0].Detail, "read by production code 1×") {
		t.Errorf("the pre-declaration read must be counted: %s", got[0].Detail)
	}
}

// The same site seen by several loads (every file all OSes compile) is one
// use, not one per load; and a production write seen only in a LATER load
// (a _windows.go writer) clears a field an earlier load saw only read.
func TestScanMerge_CrossLoadDedupAndLateWriter(t *testing.T) {
	s := newTestScan()
	k := fieldKey{"example.com/m/internal/p", "T", "F"}
	f := s.facts(k)
	f.judged = true

	read := token.Position{Filename: "/repo/internal/p/read.go", Offset: 40}
	for range 3 { // host, then two cross-OS loads, all compiling read.go
		s.record(k, read, false, false)
	}
	if f.prodRead != 1 {
		t.Fatalf("a site every load sees must count once, got prodRead=%d", f.prodRead)
	}
	if got := s.phantomFields(); len(got) != 1 {
		t.Fatalf("read-only field must be phantom before its writer is seen, got %v", got)
	}

	s.record(k, token.Position{Filename: "/repo/internal/p/write_windows.go", Offset: 12}, false, true)
	if got := s.phantomFields(); len(got) != 0 {
		t.Errorf("a production writer from a later load must clear the finding, got %v", got)
	}
}

// Recording uses before declarations must not widen the rule: a field no load
// declared judgeable (a tagged struct, a generated file, a seam) is never
// reported, and a field outside the judged prefix is not even recorded.
func TestScanMerge_OnlyJudgedFieldsAreReported(t *testing.T) {
	s := newTestScan()
	unjudged := fieldKey{"example.com/m/internal/p", "Tagged", "Label"}
	outside := fieldKey{"example.com/other/q", "T", "F"}

	s.record(unjudged, token.Position{Filename: "/repo/internal/p/a.go", Offset: 1}, false, false)
	s.record(outside, token.Position{Filename: "/repo/other/q/a.go", Offset: 1}, false, false)

	if got := s.phantomFields(); len(got) != 0 {
		t.Errorf("an unjudged field must never be reported, got %v", got)
	}
	if _, ok := s.fields[outside]; ok {
		t.Errorf("a use of a field outside the judged prefix must not be recorded")
	}
}

// scanEnv must give each target its own slice: the per-OS loads would
// otherwise share a backing array and the last GOOS would win for all of them.
func TestScanEnv(t *testing.T) {
	extra := make([]string, 1, 8) // spare capacity is what makes aliasing possible
	extra[0] = "GOWORK=off"

	host := scanEnv(extra, "")
	linux := scanEnv(extra, "linux")
	windows := scanEnv(extra, "windows")

	if want := []string{"GOWORK=off"}; !reflect.DeepEqual(host, want) {
		t.Errorf("host env = %v, want %v (no GOOS override for the host)", host, want)
	}
	if want := []string{"GOWORK=off", "GOOS=linux", "CGO_ENABLED=0"}; !reflect.DeepEqual(linux, want) {
		t.Errorf("linux env = %v, want %v", linux, want)
	}
	if want := []string{"GOWORK=off", "GOOS=windows", "CGO_ENABLED=0"}; !reflect.DeepEqual(windows, want) {
		t.Errorf("windows env = %v, want %v", windows, want)
	}
}
