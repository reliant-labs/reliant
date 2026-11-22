package testreport_test

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// The fixtures under testdata/cover are real tool output from a two-package
// module (example.com/demo: a, b, cmd/server), made with go1.27:
//
//	unit.profile           go test -covermode=atomic -coverprofile ./a ./b
//	coverpkg.profile       the same with -coverpkg=./... (blocks repeat per test binary)
//	server.profile         go tool covdata textfmt of a -cover server binary's run (cov1/)
//	covdata-merged.profile covdata textfmt of `go tool covdata merge` of the unit
//	                   tests' -test.gocoverdir output and cov1/
//	shifted.profile        the server rebuilt after a one-line shift in a/a.go (cov2/)
//	set.profile            go test -covermode=set -coverprofile ./a

func parseCov(t *testing.T, file string, lane testreport.Lane, opts ...func(*testreport.CoverageOptions)) *testreport.Coverage {
	t.Helper()
	f, err := os.Open("testdata/cover/" + file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	o := testreport.CoverageOptions{Lane: lane, Tree: "tree1"}
	for _, fn := range opts {
		fn(&o)
	}
	c, err := testreport.ParseCoverage(f, o)
	if err != nil {
		t.Fatalf("ParseCoverage(%s): %v", file, err)
	}
	return c
}

func parseCovString(t *testing.T, in string, opts testreport.CoverageOptions) (*testreport.Coverage, error) {
	t.Helper()
	return testreport.ParseCoverage(strings.NewReader(in), opts)
}

type blockState struct {
	stmts int
	hits  int64
}

// blocks flattens a document to file:position -> statements and summed hits.
func blocks(c *testreport.Coverage) map[string]blockState {
	out := map[string]blockState{}
	for _, f := range c.Files {
		for _, b := range f.Blocks {
			var h int64
			for _, x := range b.Hits {
				if x > 0 {
					h += x
				}
			}
			k := f.Key() + ":" + strings.Join([]string{strconv.Itoa(b.StartLine), strconv.Itoa(b.StartCol), strconv.Itoa(b.EndLine), strconv.Itoa(b.EndCol)}, ".")
			out[k] = blockState{stmts: b.Statements, hits: h}
		}
	}
	return out
}

func merge(t *testing.T, opts testreport.MergeOptions, docs ...*testreport.Coverage) *testreport.Coverage {
	t.Helper()
	m, err := testreport.Merge(docs, opts)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	return m
}

// Phase 0, case 1: a unit profile and an instrumented server's textfmt output
// name exactly the same blocks for the packages both cover.
func TestMerge_IdenticalBlockSets(t *testing.T) {
	unit := parseCov(t, "unit.profile", testreport.LaneUnit)
	server := parseCov(t, "server.profile", testreport.LaneE2E)
	ub, sb := blocks(unit), blocks(server)
	shared := 0
	for k, u := range ub {
		s, ok := sb[k]
		if !ok {
			t.Errorf("block %s is in the unit profile only", k)
			continue
		}
		if s.stmts != u.stmts {
			t.Errorf("block %s: %d statements vs %d", k, u.stmts, s.stmts)
		}
		shared++
	}
	if shared != 12 {
		t.Errorf("shared blocks = %d, want 12", shared)
	}
	if _, err := testreport.Merge([]*testreport.Coverage{unit, server}, testreport.MergeOptions{}); err != nil {
		t.Errorf("same-source inputs refused: %v", err)
	}
}

// Phase 0, case 2: the union is exact. Merging the unit profile with the
// server's equals Go's own `covdata merge` of the same counters, block for
// block and count for count.
func TestMerge_UnionEqualsCovdataMerge(t *testing.T) {
	unit := parseCov(t, "unit.profile", testreport.LaneUnit)
	server := parseCov(t, "server.profile", testreport.LaneE2E)
	got := blocks(merge(t, testreport.MergeOptions{}, unit, server))
	want := blocks(parseCov(t, "covdata-merged.profile", "merged"))
	if len(got) != len(want) {
		t.Fatalf("union has %d blocks, covdata merge %d", len(got), len(want))
	}
	for k, w := range want {
		if g := got[k]; g != w {
			t.Errorf("block %s: union %+v, covdata merge %+v", k, g, w)
		}
	}
}

// Phase 0, case 3: two builds of different source are refused, never averaged
// (covdata merge itself accepts them and reports 72 statements for 36).
func TestMerge_ShiftedBuildIsRefused(t *testing.T) {
	server := parseCov(t, "server.profile", testreport.LaneE2E)
	shifted := parseCov(t, "shifted.profile", testreport.LaneE2E)
	_, err := testreport.Merge([]*testreport.Coverage{server, shifted}, testreport.MergeOptions{})
	var sm *testreport.SourceMismatchError
	if !errors.As(err, &sm) || !strings.Contains(sm.File, "a/a.go") {
		t.Fatalf("shifted build: %v", err)
	}
	// The same happens against the unit lane.
	unit := parseCov(t, "unit.profile", testreport.LaneUnit)
	if _, err := testreport.Merge([]*testreport.Coverage{unit, shifted}, testreport.MergeOptions{}); !errors.As(err, &sm) {
		t.Errorf("shifted build vs unit lane: %v", err)
	}
}

// Phase 0, case 4: a -coverpkg profile repeats each block once per test
// binary; parsing deduplicates it, summing the counts.
func TestParseCoverage_CoverpkgDedupe(t *testing.T) {
	raw, err := os.ReadFile("testdata/cover/coverpkg.profile")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(raw), "\n") - 1; lines != 24 {
		t.Fatalf("fixture has %d block lines", lines)
	}
	c := parseCov(t, "coverpkg.profile", testreport.LaneUnit)
	b := blocks(c)
	if len(b) != 12 {
		t.Errorf("deduplicated to %d blocks, want 12 (a and b)", len(b))
	}
	// b's test reaches a.Abs through b.Dist: coverpkg counts it.
	if got := b["go:example.com/demo/a/a.go:5.2.5.11"].hits; got != 2 {
		t.Errorf("a.Abs's first block: %d hits, want 2 (one from each test binary)", got)
	}
	unit := parseCov(t, "unit.profile", testreport.LaneUnit)
	if _, err := testreport.Merge([]*testreport.Coverage{c, unit}, testreport.MergeOptions{}); err == nil {
		// unit and coverpkg are the same lane and the same source; fine.
	} else {
		t.Errorf("coverpkg and per-package profiles of one tree refused: %v", err)
	}
}

func TestParseCoverage_StatementMismatchInOneInput(t *testing.T) {
	in := "mode: count\nx.com/p/a.go:1.1,2.2 1 1\nx.com/p/a.go:1.1,2.2 2 1\n"
	_, err := parseCovString(t, in, testreport.CoverageOptions{Lane: "unit"})
	var sm *testreport.SourceMismatchError
	if !errors.As(err, &sm) || sm.Statements != [2]int{1, 2} {
		t.Errorf("disagreeing statements: %v", err)
	}
}

func TestParseCoverage_Malformed(t *testing.T) {
	for _, in := range []string{
		"x.com/p/a.go:1.1,2.2 1 1\n",               // no mode line
		"mode: count\nx.com/p/a.go 1 1\n",          // no position
		"mode: count\nx.com/p/a.go:1.1,2.2 -1 1\n", // negative statements
		"mode: weird\n",
	} {
		if _, err := parseCovString(t, in, testreport.CoverageOptions{Lane: "unit", Format: testreport.FormatGoCover}); err == nil {
			t.Errorf("parsed malformed profile %q", in)
		}
	}
	if _, err := parseCovString(t, "", testreport.CoverageOptions{Lane: "unit"}); !errors.Is(err, testreport.ErrNoInput) {
		t.Errorf("empty: %v", err)
	}
	if _, err := parseCovString(t, "mode: set\n", testreport.CoverageOptions{}); err == nil {
		t.Error("a coverage input with no lane parsed")
	}
	var le *testreport.LimitError
	big := "mode: count\n" + strings.Repeat("x.com/p/a.go:1.1,2.2 1 1\n", 3) + "x.com/p/a.go:3.1,4.2 1 1\n"
	if _, err := parseCovString(t, big, testreport.CoverageOptions{Lane: "u", Limits: testreport.Limits{MaxBlocks: 1}}); !errors.As(err, &le) || le.Limit != "MaxBlocks" {
		t.Errorf("MaxBlocks: %v", err)
	}
}

func TestMerge_Refusals(t *testing.T) {
	unit := parseCov(t, "unit.profile", testreport.LaneUnit)
	other := parseCov(t, "server.profile", testreport.LaneE2E, func(o *testreport.CoverageOptions) { o.Tree = "tree2" })
	var tm *testreport.TreeMismatchError
	if _, err := testreport.Merge([]*testreport.Coverage{unit, other}, testreport.MergeOptions{}); !errors.As(err, &tm) {
		t.Errorf("different trees: %v", err)
	}
	unknown := parseCov(t, "server.profile", testreport.LaneE2E, func(o *testreport.CoverageOptions) { o.Tree = "" })
	if _, err := testreport.Merge([]*testreport.Coverage{unit, unknown}, testreport.MergeOptions{}); !errors.As(err, &tm) {
		t.Errorf("a known and an unknown tree: %v", err)
	}

	fp := func(v string) func(*testreport.CoverageOptions) {
		return func(o *testreport.CoverageOptions) { o.BuildFingerprint = v }
	}
	a := parseCov(t, "unit.profile", testreport.LaneUnit, fp("pkgfp-1"))
	b := parseCov(t, "server.profile", testreport.LaneE2E, fp("pkgfp-2"))
	var bm *testreport.BuildMismatchError
	if _, err := testreport.Merge([]*testreport.Coverage{a, b}, testreport.MergeOptions{}); !errors.As(err, &bm) {
		t.Errorf("two fingerprints for one package: %v", err)
	}

	lcov, err := parseCovString(t, "SF:x.com/p/a.go\nDA:1,1\nend_of_record\n", testreport.CoverageOptions{Lane: "u", Tree: "tree1"})
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := parseCovString(t, "mode: count\n/abs/x.com/p/a.go:1.1,1.5 1 1\n", testreport.CoverageOptions{Lane: "v", Tree: "tree1", Root: "/abs"})
	if err != nil {
		t.Fatal(err)
	}
	var gm *testreport.GranularityMismatchError
	if _, err := testreport.Merge([]*testreport.Coverage{lcov, stmt}, testreport.MergeOptions{}); !errors.As(err, &gm) {
		t.Errorf("statement vs line granularity: %v", err)
	}

	m := merge(t, testreport.MergeOptions{}, unit)
	server := parseCov(t, "server.profile", testreport.LaneE2E)
	both := merge(t, testreport.MergeOptions{}, unit, server)
	var om *testreport.OverlapError
	if _, err := testreport.Merge([]*testreport.Coverage{m, both}, testreport.MergeOptions{}); !errors.As(err, &om) {
		t.Errorf("a document repeating some merged inputs: %v", err)
	}
}

// Two binaries built from one tree (an api and a worker sharing a package)
// have different covmeta hashes but the same blocks: they merge.
func TestMerge_TwoBinariesOfOneTreeMerge(t *testing.T) {
	api := parseCov(t, "server.profile", testreport.LaneE2E)
	worker := parseCov(t, "server.profile", testreport.LaneE2E)
	worker.Inputs[0].Digest = "sha256:worker"
	m := merge(t, testreport.MergeOptions{}, api, worker)
	if got := blocks(m)["go:example.com/demo/a/a.go:13.2.13.9"].hits; got != 4 {
		t.Errorf("hits sum across binaries: %d, want 4", got)
	}
}

func TestMerge_IdempotentOnDigest(t *testing.T) {
	unit := parseCov(t, "unit.profile", testreport.LaneUnit)
	again := parseCov(t, "unit.profile", testreport.LaneUnit)
	once := blocks(merge(t, testreport.MergeOptions{}, unit))
	twice := merge(t, testreport.MergeOptions{}, unit, again)
	if len(twice.Inputs) != 1 {
		t.Errorf("inputs = %d", len(twice.Inputs))
	}
	for k, v := range blocks(twice) {
		if once[k] != v {
			t.Errorf("block %s changed on a repeated input: %+v vs %+v", k, v, once[k])
		}
	}
	// Merging a merge with the inputs it already holds changes nothing.
	thrice := merge(t, testreport.MergeOptions{}, twice, again)
	if len(thrice.Inputs) != 1 || len(blocks(thrice)) != len(once) {
		t.Errorf("re-merge: %d inputs", len(thrice.Inputs))
	}
}

func TestMerge_SetModeIsBoolean(t *testing.T) {
	set := parseCov(t, "set.profile", testreport.LaneUnit)
	if set.Lanes[0].Mode != testreport.ModeSet || set.Inputs[0].Mode != "set" {
		t.Fatalf("set lane: %+v %+v", set.Lanes, set.Inputs)
	}
	atomic, err := parseCovString(t, "mode: atomic\nexample.com/demo/a/a.go:5.2,5.11 1 7\n", testreport.CoverageOptions{Lane: testreport.LaneUnit, Tree: "tree1"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := testreport.Merge([]*testreport.Coverage{set, atomic}, testreport.MergeOptions{})
	if err == nil {
		t.Fatalf("a partial block set of a/a.go merged: %+v", m.Files)
	}
	server := parseCov(t, "server.profile", testreport.LaneUnit) // same lane, count mode
	m = merge(t, testreport.MergeOptions{}, set, server)
	if m.Lanes[0].Mode != testreport.ModeSet {
		t.Errorf("a lane a set input joined is a set lane: %+v", m.Lanes)
	}
	for _, f := range m.Files {
		for _, b := range f.Blocks {
			if b.Hits[0] > 1 {
				t.Errorf("%s: set lane hit %d", f.DisplayPath(), b.Hits[0])
			}
		}
	}
}

func TestMerge_PathsAndExclusions(t *testing.T) {
	gen, err := parseCovString(t, strings.Join([]string{
		"mode: atomic",
		"example.com/demo/gen/api/v1/api.pb.go:1.1,2.1 10 0",
		"example.com/demo/internal/db/mock_gen.go:1.1,2.1 5 1",
		"example.com/demo/internal/db/store.go:1.1,2.1 4 1",
		"example.com/demo/internal/db/store_extra.go:1.1,2.1 3 0",
		"example.com/demo/cmd/tool/main.go:1.1,2.1 2 0",
		"",
	}, "\n"), testreport.CoverageOptions{Lane: "unit"})
	if err != nil {
		t.Fatal(err)
	}
	m := merge(t, testreport.MergeOptions{ModulePath: "example.com/demo", Exclude: []string{"cmd/**"}, Generated: []string{"internal/db/store_extra.go"}}, gen)
	ex := map[string]string{}
	for _, f := range m.Files {
		ex[f.Path] = f.Excluded
	}
	want := map[string]string{
		"gen/api/v1/api.pb.go":       testreport.ExcludedGenerated,
		"internal/db/mock_gen.go":    testreport.ExcludedGenerated,
		"internal/db/store.go":       "",
		"internal/db/store_extra.go": testreport.ExcludedGenerated,
		"cmd/tool/main.go":           testreport.ExcludedFiltered,
	}
	for p, w := range want {
		if got, ok := ex[p]; !ok || got != w {
			t.Errorf("%s: excluded %q (present %v), want %q", p, got, ok, w)
		}
	}
	s := m.Summary()
	if s.Combined.Statements != 4 || s.Combined.Covered != 4 || s.Generated.Statements != 18 || s.Filtered.Statements != 2 {
		t.Errorf("summary %+v", s)
	}
	inc := merge(t, testreport.MergeOptions{ModulePath: "example.com/demo", IncludeGenerated: true, Include: []string{"internal/**"}}, gen)
	if got := inc.Summary().Combined.Statements; got != 12 {
		t.Errorf("include internal/** with generated counted: %d statements, want 12", got)
	}
}

// The ordinary Go text profile names each file by its import-path-qualified
// name. Parsing keeps that name whole as ImportPath (its directory is the
// package) and leaves Path empty; Merge with the module path derives the
// repo path, which path filters, generated-file exclusion and diff coverage
// then read (TestMerge_PathsAndExclusions, TestViews_LanesCombinedOnlyE2E).
func TestParseCoverage_GoProfileNamesFilesByImportPath(t *testing.T) {
	c, err := parseCovString(t, "mode: set\nexample.com/demo/internal/db/store.go:3.2,4.10 2 1\n", testreport.CoverageOptions{Lane: testreport.LaneUnit, Root: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	f := c.Files[0]
	if f.ImportPath != "example.com/demo/internal/db/store.go" || f.Package != "example.com/demo/internal/db" || f.Path != "" || f.Lang != testreport.LangGo {
		t.Fatalf("parsed file %+v", f)
	}
	m := merge(t, testreport.MergeOptions{ModulePath: "example.com/demo"}, c)
	if got := m.Files[0]; got.Path != "internal/db/store.go" || got.ImportPath != f.ImportPath {
		t.Errorf("merged file %+v, want Path internal/db/store.go and the import path kept", got)
	}
	d := m.Diff([]testreport.ChangedFile{{Path: "internal/db/store.go", Ranges: []testreport.LineRange{{Start: 3, End: 4}}}})
	if d.Lines != 2 || d.Covered != 2 {
		t.Errorf("diff coverage over the derived path: %+v", d)
	}
}

// A Go profile names a file by import path; another input may name the same
// file by its path (an absolute path under Root, an LCOV or Cobertura
// report). Once Merge derives the path from the module path, both are one
// file, and a line-granular report of it is a granularity mismatch rather
// than a second file.
func TestMerge_ImportPathAndRepoPathAreOneFile(t *testing.T) {
	byImport := parseCov(t, "unit.profile", testreport.LaneUnit)
	byPath, err := parseCovString(t, "mode: atomic\n/repo/a/a.go:13.2,13.9 1 4\n/repo/a/a.go:5.2,5.11 1 0\n/repo/a/a.go:6.3,7.1 1 0\n/repo/a/a.go:8.2,8.10 1 0\n/repo/a/a.go:15.3,15.12 1 0\n/repo/a/a.go:17.3,17.11 1 0\n/repo/a/a.go:19.2,19.10 1 0\n/repo/a/a.go:23.24,23.41 1 0\n",
		testreport.CoverageOptions{Lane: testreport.LaneE2E, Tree: "tree1", Root: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if byPath.Files[0].Path != "a/a.go" || byPath.Files[0].ImportPath != "" {
		t.Fatalf("by-path input: %+v", byPath.Files[0])
	}
	m := merge(t, testreport.MergeOptions{ModulePath: "example.com/demo"}, byImport, byPath)
	var aFiles []testreport.CoverageFile
	for _, f := range m.Files {
		if f.Path == "a/a.go" {
			aFiles = append(aFiles, f)
		}
	}
	if len(aFiles) != 1 {
		t.Fatalf("a/a.go appears %d times: %+v", len(aFiles), m.Files)
	}
	f := aFiles[0]
	if f.ImportPath != "example.com/demo/a/a.go" || f.Granularity != testreport.GranularityStatement || len(f.Blocks) != 8 {
		t.Errorf("merged file %+v", f)
	}
	if e := m.Summary().Lanes[testreport.LaneE2E]; e.Statements != 8 || e.Covered != 1 {
		t.Errorf("e2e lane over a/a.go: %+v", e)
	}

	// The same file as LCOV lines: one file, two granularities, refused.
	lcov, err := parseCovString(t, "SF:/repo/a/a.go\nDA:5,1\nend_of_record\n", testreport.CoverageOptions{Lane: "lines", Tree: "tree1", Root: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	var gm *testreport.GranularityMismatchError
	if _, err := testreport.Merge([]*testreport.Coverage{byImport, lcov}, testreport.MergeOptions{ModulePath: "example.com/demo"}); !errors.As(err, &gm) || gm.File != "a/a.go" {
		t.Errorf("profile + LCOV of one .go file: %v", err)
	}
	// In either order.
	if _, err := testreport.Merge([]*testreport.Coverage{lcov, byImport}, testreport.MergeOptions{ModulePath: "example.com/demo"}); !errors.As(err, &gm) {
		t.Errorf("LCOV first: %v", err)
	}
}

// identityInputs are three inputs of one Go file, x.com/m/p/f.go at repo
// path p/f.go, each naming it differently and each in its own lane:
//
//	byImport  a Go profile: import path only
//	byPath    a profile of absolute paths: repo path only
//	bridge    a document an earlier Merge derived: both names
//
// Each lane hits one block, so a split or double-counted file shows in the
// per-lane counts.
func identityInputs(t *testing.T) (byImport, byPath, bridge *testreport.Coverage) {
	t.Helper()
	parse := func(in string, lane testreport.Lane, root string) *testreport.Coverage {
		c, err := parseCovString(t, in, testreport.CoverageOptions{Lane: lane, Tree: "t", Root: root})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	byImport = parse("mode: atomic\nx.com/m/p/f.go:1.1,2.1 2 5\nx.com/m/p/f.go:3.1,4.1 3 0\nx.com/m/p/f.go:5.1,6.1 1 0\n", "unit", "")
	byPath = parse("mode: atomic\n/r/p/f.go:1.1,2.1 2 0\n/r/p/f.go:3.1,4.1 3 7\n/r/p/f.go:5.1,6.1 1 0\n", "e2e", "/r")
	raw := parse("mode: atomic\nx.com/m/p/f.go:1.1,2.1 2 0\nx.com/m/p/f.go:3.1,4.1 3 0\nx.com/m/p/f.go:5.1,6.1 1 9\n", "integration", "")
	var err error
	if bridge, err = testreport.Merge([]*testreport.Coverage{raw}, testreport.MergeOptions{ModulePath: "x.com/m"}); err != nil {
		t.Fatal(err)
	}
	if f := bridge.Files[0]; f.Path != "p/f.go" || f.ImportPath != "x.com/m/p/f.go" {
		t.Fatalf("bridge names %+v", f)
	}
	return byImport, byPath, bridge
}

// assertOneIdentityFile checks a merge of identityInputs holds p/f.go once,
// with each lane's own counts.
func assertOneIdentityFile(t *testing.T, name string, m *testreport.Coverage, lanes map[testreport.Lane][2]int) {
	t.Helper()
	if len(m.Files) != 1 {
		t.Fatalf("%s: %d files, want 1: %+v", name, len(m.Files), m.Files)
	}
	f := m.Files[0]
	if f.Path != "p/f.go" || f.ImportPath != "x.com/m/p/f.go" || len(f.Blocks) != 3 || f.Granularity != testreport.GranularityStatement {
		t.Errorf("%s: file %+v", name, f)
	}
	s := m.Summary()
	if s.Combined.Statements != 6 {
		t.Errorf("%s: %d statements, want 6 (double-counted?)", name, s.Combined.Statements)
	}
	for lane, want := range lanes {
		if got := s.Lanes[lane]; got.Statements != want[0] || got.Covered != want[1] {
			t.Errorf("%s: lane %s = %d/%d, want %d/%d", name, lane, got.Covered, got.Statements, want[1], want[0])
		}
	}
	if len(s.Lanes) != len(lanes) {
		t.Errorf("%s: lanes %+v", name, s.Lanes)
	}
}

// [B1] A Go file named by import path in one input and by repo path in
// another is one file however Merge learns the two names are the same, and
// in whichever order the inputs arrive.
func TestMerge_OneFileWhicheverNameComesFirst(t *testing.T) {
	byImport, byPath, bridge := identityInputs(t)

	// Two inputs, both orders: the module path links the names.
	for _, order := range [][]*testreport.Coverage{{byImport, byPath}, {byPath, byImport}} {
		m := merge(t, testreport.MergeOptions{ModulePath: "x.com/m"}, order...)
		assertOneIdentityFile(t, "module path, "+string(order[0].Lanes[0].Name)+" first", m,
			map[testreport.Lane][2]int{"unit": {6, 2}, "e2e": {6, 3}})
	}

	// Two inputs, both orders, no module path: a document that already
	// carries both names (an earlier merge) links them.
	for _, order := range [][]*testreport.Coverage{{bridge, byImport}, {byImport, bridge}, {bridge, byPath}, {byPath, bridge}} {
		m := merge(t, testreport.MergeOptions{}, order...)
		other := order[0].Lanes[0].Name
		if other == "integration" {
			other = order[1].Lanes[0].Name
		}
		want := map[testreport.Lane][2]int{"integration": {6, 1}, "unit": {6, 2}}
		if other == "e2e" {
			want = map[testreport.Lane][2]int{"integration": {6, 1}, "e2e": {6, 3}}
		}
		assertOneIdentityFile(t, "bridge with "+string(other), m, want)
	}

	// Three inputs, every order, no module path: the import-only and the
	// path-only input are two aggregates until the bridge joins them, which
	// may arrive first, between them, or last.
	three := []*testreport.Coverage{byImport, byPath, bridge}
	for _, perm := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		in := []*testreport.Coverage{three[perm[0]], three[perm[1]], three[perm[2]]}
		m := merge(t, testreport.MergeOptions{}, in...)
		assertOneIdentityFile(t, "three inputs "+strconv.Itoa(perm[0])+strconv.Itoa(perm[1])+strconv.Itoa(perm[2]), m,
			map[testreport.Lane][2]int{"unit": {6, 2}, "e2e": {6, 3}, "integration": {6, 1}})
	}

	// A bridge joining two aggregates still refuses different source.
	shifted, err := parseCovString(t, "mode: atomic\n/r/p/f.go:2.1,3.1 2 1\n/r/p/f.go:3.1,4.1 3 0\n/r/p/f.go:5.1,6.1 1 0\n", testreport.CoverageOptions{Lane: "e2e", Tree: "t", Root: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	var sm *testreport.SourceMismatchError
	if _, err := testreport.Merge([]*testreport.Coverage{byImport, shifted, bridge}, testreport.MergeOptions{}); !errors.As(err, &sm) {
		t.Errorf("a shifted path-named input joined by the bridge: %v", err)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"cmd/**", "cmd/a/b.go", true},
		{"cmd/**", "internal/cmd/b.go", false},
		{"**/testdata/**", "a/testdata/x.go", true},
		{"**/testdata/**", "testdata/x.go", true},
		{"*_test.go", "a/b/c_test.go", true},
		{"internal/*.go", "internal/a.go", true},
		{"internal/*.go", "internal/x/a.go", false},
		{"**", "anything/at/all", true},
	}
	for _, c := range cases {
		if got := testreport.MatchGlob(c.glob, c.path); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v", c.glob, c.path, got)
		}
	}
	for p, want := range map[string]bool{
		"gen/x.go": true, "a/gen/b/c.go": true, "x_gen.go": true, "a/b.pb.go": true, "web/src/api-hooks.ts": true,
		"internal/generator/x.go": false, "gen.go": false,
	} {
		if got := testreport.IsGenerated(p); got != want {
			t.Errorf("IsGenerated(%q) = %v", p, got)
		}
	}
}

func TestViews_LanesCombinedOnlyE2E(t *testing.T) {
	unit := parseCov(t, "unit.profile", testreport.LaneUnit)
	server := parseCov(t, "server.profile", testreport.LaneE2E)
	m := merge(t, testreport.MergeOptions{ModulePath: "example.com/demo"}, unit, server)
	s := m.Summary()
	// unit instruments a and b (12 statements, 3 covered); the server also
	// instruments cmd/server (13, 6 covered). Union: 9 of 13. Only e2e: the
	// Sign blocks, Max's two blocks and main = 5... minus nothing unit hit.
	if u := s.Lanes[testreport.LaneUnit]; u.Statements != 12 || u.Covered != 3 || u.Pct != 25 {
		t.Errorf("unit lane %+v", u)
	}
	if e := s.Lanes[testreport.LaneE2E]; e.Statements != 13 || e.Covered != 6 {
		t.Errorf("e2e lane %+v", e)
	}
	if s.Combined.Statements != 13 || s.Combined.Covered != 9 || s.Combined.Pct != 69.2 {
		t.Errorf("combined %+v", s.Combined)
	}
	if s.OnlyE2E.Covered != 6 {
		t.Errorf("only e2e %+v", s.OnlyE2E)
	}
	if len(s.Languages) != 1 || s.CombinedLabel != "" {
		t.Errorf("one language, no label: %+v %q", s.Languages, s.CombinedLabel)
	}

	pkgs := m.Rollup(testreport.ByPackage)
	if len(pkgs) != 3 || pkgs[0].Path != "a" || pkgs[0].Combined.Statements != 8 || pkgs[0].Combined.Covered != 5 {
		t.Errorf("by package %+v", pkgs)
	}
	if files := m.Rollup(testreport.ByFile); len(files) != 3 || files[2].Path != "cmd/server/main.go" {
		t.Errorf("by file %+v", files)
	}

	diff := m.Diff([]testreport.ChangedFile{{Path: "a/a.go", Ranges: []testreport.LineRange{{Start: 1, End: 9}, {Start: 22, End: 23}}}, {Path: "README.md", Ranges: []testreport.LineRange{{Start: 1, End: 3}}}})
	// Executable changed lines in a/a.go: 5, 6, 7, 8 (Abs) and 23 (Unused);
	// 5-7 covered by unit, 8 and 23 not.
	if diff.Lines != 5 || diff.Covered != 3 || diff.Pct != 60 || len(diff.Files) != 1 {
		t.Fatalf("diff %+v", diff)
	}
	if u := diff.Files[0].Uncovered; len(u) != 2 || u[0] != (testreport.LineRange{Start: 8, End: 8}) || u[1] != (testreport.LineRange{Start: 23, End: 23}) {
		t.Errorf("uncovered ranges %+v", u)
	}

	d := testreport.Delta(m, nil)
	if d.Available || d.Reason != "no report for base" {
		t.Errorf("delta without base %+v", d)
	}
	base := merge(t, testreport.MergeOptions{}, parseCov(t, "server.profile", testreport.LaneE2E))
	d = testreport.Delta(m, base)
	if !d.Available || d.Value != 23.1 {
		t.Errorf("delta %+v (69.2 - 46.2)", d)
	}
}

func TestViews_LanguagesKeptApart(t *testing.T) {
	gocov, err := parseCovString(t, "mode: atomic\nx.com/p/a.go:1.1,2.1 3 1\nx.com/p/a.go:3.1,4.1 1 0\n", testreport.CoverageOptions{Lane: "unit", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	lcov, err := parseCovString(t, "TN:\nSF:/r/web/src/a.ts\nDA:1,1\nDA:2,0\nDA:3,0\nDA:4,0\nend_of_record\n", testreport.CoverageOptions{Lane: "frontend", Tree: "t", Root: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	s := merge(t, testreport.MergeOptions{}, gocov, lcov).Summary()
	if len(s.Languages) != 2 || s.Languages[0].Lang != "go" || s.Languages[0].Combined.Pct != 75 || s.Languages[1].Lang != "ts" || s.Languages[1].Combined.Pct != 25 || s.Languages[1].Granularity != testreport.GranularityLine {
		t.Errorf("languages %+v", s.Languages)
	}
	if s.Combined.Pct != 50 || s.CombinedLabel != "weighted by statements across go, ts" {
		t.Errorf("combined %+v %q", s.Combined, s.CombinedLabel)
	}
}

func TestParseCoverage_OtherFormats(t *testing.T) {
	cases := []struct {
		file  string
		gran  testreport.Granularity
		path  string
		stmts int
		cov   int
	}{
		{"lcov.info", testreport.GranularityLine, "src/lib/money.ts", 4, 2},
		{"cobertura.xml", testreport.GranularityLine, "src/app/main.py", 3, 2},
		{"istanbul.json", testreport.GranularityStatement, "src/lib/money.ts", 3, 2},
	}
	for _, c := range cases {
		cov := parseCov(t, c.file, "frontend", func(o *testreport.CoverageOptions) { o.Root = "/work/repo" })
		if len(cov.Files) != 1 {
			t.Errorf("%s: %d files", c.file, len(cov.Files))
			continue
		}
		f := cov.Files[0]
		s := cov.Summary()
		if f.Granularity != c.gran || f.Path != c.path || s.Combined.Statements != c.stmts || s.Combined.Covered != c.cov {
			t.Errorf("%s: file %+v summary %+v", c.file, f, s.Combined)
		}
	}
}

func TestGitBlobID(t *testing.T) {
	// `printf 'hello\n' | git hash-object --stdin`
	if got := testreport.GitBlobID([]byte("hello\n")); got != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Errorf("GitBlobID = %s", got)
	}
}
