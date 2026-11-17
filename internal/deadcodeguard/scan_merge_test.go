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

const pkgP = "example.com/m/internal/p"

func newTestScan() *scan {
	return newScan("/repo", "example.com/m/internal/")
}

// A use recorded BEFORE any load declared its field judgeable must still
// count once a later load does. With a host-first visit order this is the
// common case for a field whose judgeable declaration arrives later.
func TestScanMerge_UseBeforeDeclarationIsKept(t *testing.T) {
	s := newTestScan()
	k := fieldKey{"example.com/m/internal/p", "T", "F"}

	s.record(k, token.Position{Filename: "/repo/internal/p/read.go", Offset: 40, Line: 7}, pkgP, false, false)

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
		s.record(k, read, pkgP, false, false)
	}
	if got := f.tally(nil).prodRead; got != 1 {
		t.Fatalf("a site every load sees must count once, got prodRead=%d", got)
	}
	if got := s.phantomFields(); len(got) != 1 {
		t.Fatalf("read-only field must be phantom before its writer is seen, got %v", got)
	}

	s.record(k, token.Position{Filename: "/repo/internal/p/write_windows.go", Offset: 12}, pkgP, false, true)
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

	s.record(unjudged, token.Position{Filename: "/repo/internal/p/a.go", Offset: 1}, pkgP, false, false)
	s.record(outside, token.Position{Filename: "/repo/other/q/a.go", Offset: 1}, "example.com/other/q", false, false)

	if got := s.phantomFields(); len(got) != 0 {
		t.Errorf("an unjudged field must never be reported, got %v", got)
	}
	if _, ok := s.fields[outside]; ok {
		t.Errorf("a use of a field outside the judged prefix must not be recorded")
	}
}

// A package only test code imports is test support, and the import edges that
// decide it merge across loads like uses do: one production import, from any
// load, makes a package (and what only it imports) production.
func TestScanMerge_TestSupport(t *testing.T) {
	const (
		fake   = "example.com/m/internal/fake"   // imported by two packages' tests
		rows   = "example.com/m/internal/rows"   // imported only by fake
		shared = "example.com/m/internal/shared" // tests import it, and so does p
		late   = "example.com/m/internal/late"   // its production importer is in a later load
		main   = "example.com/m/cmd/app"
	)
	s := newTestScan()
	for _, e := range []importEdge{
		{from: pkgP, to: fake, test: true},
		{from: "example.com/m/internal/q_test", to: fake, test: true},
		{from: fake, to: rows},
		{from: "example.com/m/internal/q", to: shared, test: true},
		{from: pkgP, to: shared},
		{from: main, to: pkgP},
		{from: pkgP + ".test", to: pkgP, test: true}, // a test binary's main
		{from: pkgP, to: late, test: true},
	} {
		s.imports[e] = true
	}
	got := s.testSupport()
	for pkg, want := range map[string]bool{fake: true, rows: true, shared: false, pkgP: false, late: true} {
		if got[pkg] != want {
			t.Errorf("testSupport[%s] = %v, want %v", pkg, got[pkg], want)
		}
	}
	s.imports[importEdge{from: "example.com/m/internal/late_windows_user", to: late}] = true
	if s.testSupport()[late] {
		t.Errorf("a production import from a later load must make %s production", late)
	}

	// A field the fake reads and only tests write is the fixture's input,
	// not a shape production cannot produce.
	k := fieldKey{fake, "Row", "Value"}
	s.facts(k).judged = true
	s.record(k, token.Position{Filename: "/repo/internal/fake/fake.go", Offset: 9}, fake, false, false)
	s.record(k, token.Position{Filename: "/repo/internal/p/p_test.go", Offset: 9}, pkgP, true, true)
	if got := s.phantomFields(); len(got) != 0 {
		t.Errorf("a test-support package's reads are test reads, got %v", got)
	}
	// The same read from a production package is still judged.
	s.record(k, token.Position{Filename: "/repo/internal/p/p.go", Offset: 9}, pkgP, false, false)
	if got := s.phantomFields(); len(got) != 1 {
		t.Errorf("a production read of a test-only-written field must be reported, got %v", got)
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
