package testreport_test

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testreport"
)

func TestMergeErrors_NameWhatDisagrees(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want []string
	}{
		{"block statement counts", &testreport.SourceMismatchError{File: "a.go", Block: "1.1,2.2", Statements: [2]int{2, 3}},
			[]string{"a.go:1.1,2.2", "different source", "2 statements in one, 3 in another"}},
		{"block sets", &testreport.SourceMismatchError{File: "a.go", Detail: "3 blocks in one, 4 in another"},
			[]string{"a.go", "different source", "3 blocks in one, 4 in another"}},
		{"builds", &testreport.BuildMismatchError{Package: "x.com/p", Fingerprints: [2]string{"f1", "f2"}},
			[]string{"x.com/p", "f1", "f2", "two builds"}},
		{"trees", &testreport.TreeMismatchError{Trees: [2]string{"t1", "t2"}},
			[]string{"different trees", "t1", "t2"}},
		{"an unknown tree is spelled out", &testreport.TreeMismatchError{Trees: [2]string{"", "t2"}},
			[]string{"(unknown)", "t2"}},
		{"granularity", &testreport.GranularityMismatchError{File: "src/a.ts"},
			[]string{"src/a.ts", "statement and line coverage cannot merge"}},
		{"overlap", &testreport.OverlapError{Digests: []string{"sha256:a", "sha256:b"}},
			[]string{"sha256:a, sha256:b", "already merged"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			if !strings.HasPrefix(msg, "testreport: ") {
				t.Errorf("message %q lacks the package prefix", msg)
			}
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("message %q does not mention %q", msg, w)
				}
			}
		})
	}
}

func TestMerge_BlockSetsAndStatementCountsMustAgree(t *testing.T) {
	opts := testreport.CoverageOptions{Lane: "a", Tree: "t"}
	base, err := parseCovString(t, "mode: count\nx.com/p/a.go:1.1,2.2 1 1\nx.com/p/a.go:3.1,4.2 2 0\n", opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Lane = "b"
	for _, tc := range []struct {
		name, profile string
		check         func(error) bool
	}{
		{"a block only one input has", "mode: count\nx.com/p/a.go:1.1,2.2 1 1\nx.com/p/a.go:5.1,6.2 2 1\n", func(err error) bool {
			var sm *testreport.SourceMismatchError
			return errors.As(err, &sm) && sm.File == "x.com/p/a.go" && strings.Contains(sm.Detail, "5.1,6.2")
		}},
		{"a different number of blocks", "mode: count\nx.com/p/a.go:1.1,2.2 1 1\n", func(err error) bool {
			var sm *testreport.SourceMismatchError
			return errors.As(err, &sm) && strings.Contains(sm.Detail, "2 blocks in one, 1 in another")
		}},
		{"the same block with another statement count", "mode: count\nx.com/p/a.go:1.1,2.2 1 1\nx.com/p/a.go:3.1,4.2 5 1\n", func(err error) bool {
			var sm *testreport.SourceMismatchError
			return errors.As(err, &sm) && sm.Statements == [2]int{2, 5} && sm.Block == "3.1,4.2"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other, err := parseCovString(t, tc.profile, opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := testreport.Merge([]*testreport.Coverage{base, other}, testreport.MergeOptions{}); !tc.check(err) {
				t.Fatalf("Merge error = %v", err)
			}
		})
	}
}

func TestMerge_UnionsChangedRangesAndSortsBlocks(t *testing.T) {
	a := parseCov(t, "unit.profile", testreport.LaneUnit)
	b := parseCov(t, "server.profile", testreport.LaneE2E)
	a.Changed = []testreport.ChangedFile{{Path: "a/a.go", Ranges: []testreport.LineRange{{Start: 1, End: 2}}}}
	b.Changed = []testreport.ChangedFile{
		{Path: "a/a.go", Ranges: []testreport.LineRange{{Start: 8, End: 9}}},
		{Path: "b/b.go", Ranges: []testreport.LineRange{{Start: 3, End: 3}}},
	}
	m := merge(t, testreport.MergeOptions{}, a, b)
	want := []testreport.ChangedFile{
		{Path: "a/a.go", Ranges: []testreport.LineRange{{Start: 1, End: 2}, {Start: 8, End: 9}}},
		{Path: "b/b.go", Ranges: []testreport.LineRange{{Start: 3, End: 3}}},
	}
	if !reflect.DeepEqual(m.Changed, want) {
		t.Errorf("Changed = %+v, want %+v", m.Changed, want)
	}
	// Merging must not alias the second document's ranges into the result.
	m.Changed[1].Ranges[0].Start = 99
	if b.Changed[1].Ranges[0].Start != 3 {
		t.Error("the merged document shares its range slices with an input")
	}
	for _, f := range m.Files {
		for i := 1; i < len(f.Blocks); i++ {
			p, q := f.Blocks[i-1], f.Blocks[i]
			if p.StartLine > q.StartLine || (p.StartLine == q.StartLine && p.StartCol > q.StartCol) {
				t.Errorf("%s: blocks out of order: %+v before %+v", f.DisplayPath(), p, q)
			}
		}
	}
}

func TestMerge_SortsBlocksByEveryPositionField(t *testing.T) {
	// Blocks sharing a start line differ by column, then end line, then end column.
	in := "mode: count\n" +
		"x.com/p/a.go:5.9,6.1 1 1\n" +
		"x.com/p/a.go:5.2,7.1 1 1\n" +
		"x.com/p/a.go:5.2,6.8 1 1\n" +
		"x.com/p/a.go:5.2,6.3 1 1\n" +
		"x.com/p/a.go:1.1,1.5 1 1\n"
	doc, err := parseCovString(t, in, testreport.CoverageOptions{Lane: "u", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	m := merge(t, testreport.MergeOptions{}, doc)
	var got []string
	for _, b := range m.Files[0].Blocks {
		got = append(got, strings.Join([]string{strconv.Itoa(b.StartLine), strconv.Itoa(b.StartCol), strconv.Itoa(b.EndLine), strconv.Itoa(b.EndCol)}, "."))
	}
	want := []string{"1.1.1.5", "5.2.6.3", "5.2.6.8", "5.2.7.1", "5.9.6.1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("block order = %v, want %v", got, want)
	}
}

func TestMerge_HitCountsSaturateInsteadOfOverflowing(t *testing.T) {
	const max = int64(^uint64(0) >> 1)
	in := func(lane string, hits string) *testreport.Coverage {
		t.Helper()
		d, err := parseCovString(t, "mode: count\nx.com/p/a.go:1.1,2.2 1 "+hits+"\n", testreport.CoverageOptions{Lane: testreport.Lane(lane), Tree: "t"})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	// Two inputs of one lane sum into one count.
	a, b := in("unit", "9223372036854775800"), in("unit", "100")
	b.Inputs[0].Digest = "sha256:other"
	m := merge(t, testreport.MergeOptions{}, a, b)
	if got := m.Files[0].Blocks[0].Hits[0]; got != max {
		t.Errorf("hits = %d, want saturation at %d", got, max)
	}
}

func TestMerge_ModulePathDerivesRepoPaths(t *testing.T) {
	doc, err := parseCovString(t, "mode: count\nx.com/mod/sub/a.go:1.1,2.2 1 1\nother.com/z/b.go:1.1,2.2 1 1\n", testreport.CoverageOptions{Lane: "u", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	m := merge(t, testreport.MergeOptions{ModulePath: "x.com/mod", ModuleDir: "svc"}, doc)
	paths := map[string]string{}
	for _, f := range m.Files {
		paths[f.ImportPath] = f.Path
	}
	if paths["x.com/mod/sub/a.go"] != "svc/sub/a.go" {
		t.Errorf("a file under the module path gets ModuleDir plus the rest, got %q", paths["x.com/mod/sub/a.go"])
	}
	if p, ok := paths["other.com/z/b.go"]; !ok || p != "" {
		t.Errorf("a file outside the module path keeps no repo path, got %q (present=%v)", p, ok)
	}
}

func TestMerge_SkipsNilDocumentsAndIncludeGenerated(t *testing.T) {
	doc, err := parseCovString(t, "mode: count\nx.com/p/a_gen.go:1.1,2.2 4 1\nx.com/p/b.go:1.1,2.2 1 0\n", testreport.CoverageOptions{Lane: "u", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	m := merge(t, testreport.MergeOptions{}, nil, doc, nil)
	if s := m.Summary(); s.Generated.Statements != 4 || s.Combined.Statements != 1 {
		t.Errorf("generated files are reported apart: %+v", s)
	}
	m = merge(t, testreport.MergeOptions{IncludeGenerated: true}, doc)
	if s := m.Summary(); s.Generated.Statements != 0 || s.Combined.Statements != 5 {
		t.Errorf("IncludeGenerated counts them: %+v", s)
	}
	m = merge(t, testreport.MergeOptions{Include: []string{"x.com/p/b.go"}, Generated: []string{"x.com/p/b.go"}}, doc)
	if s := m.Summary(); s.Generated.Statements != 5 {
		t.Errorf("a caller-declared generated glob excludes the file: %+v", s)
	}
}

func TestMerge_LaneLimitsAreRefused(t *testing.T) {
	a, err := parseCovString(t, "mode: count\nx.com/p/a.go:1.1,2.2 1 1\n", testreport.CoverageOptions{Lane: "u", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseCovString(t, "mode: count\nx.com/p/b.go:1.1,2.2 1 1\n", testreport.CoverageOptions{Lane: "v", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	var le *testreport.LimitError
	if _, err := testreport.Merge([]*testreport.Coverage{a, b}, testreport.MergeOptions{Limits: testreport.Limits{MaxCases: 1}}); !errors.As(err, &le) || le.Limit != "MaxCases" {
		t.Errorf("MaxCases: %v", err)
	}
}

func TestMerge_SetLanesStaySetAcrossMixedModes(t *testing.T) {
	set, err := parseCovString(t, "mode: set\nx.com/p/a.go:1.1,2.2 1 1\n", testreport.CoverageOptions{Lane: "u", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	count, err := parseCovString(t, "mode: count\nx.com/p/a.go:1.1,2.2 1 7\n", testreport.CoverageOptions{Lane: "u", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	m := merge(t, testreport.MergeOptions{}, count, set)
	if len(m.Lanes) != 1 || m.Lanes[0].Mode != testreport.ModeSet {
		t.Fatalf("a lane any set input joined is a set lane: %+v", m.Lanes)
	}
	if h := m.Files[0].Blocks[0].Hits[0]; h != 1 {
		t.Errorf("set-lane hits are clamped to 1, got %d", h)
	}
}

func TestParseCoverage_LCOVMalformed(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"DA before any SF", "DA:1,1\n", "DA before SF"},
		{"DA with one field", "SF:a.ts\nDA:1\n", "malformed DA"},
		{"DA with a non-numeric line", "SF:a.ts\nDA:x,1\n", "malformed DA"},
		{"DA with a non-numeric count", "SF:a.ts\nDA:1,y\n", "malformed DA"},
		{"DA with a negative line", "SF:a.ts\nDA:-1,1\n", "malformed DA"},
		{"no SF record at all", "TN:\nend_of_record\n", "no input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCovString(t, tc.in, testreport.CoverageOptions{Lane: "fe", Format: testreport.FormatLCOV})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
	// A DA after end_of_record belongs to no file.
	_, err := parseCovString(t, "SF:a.ts\nDA:1,1\nend_of_record\nDA:2,1\n", testreport.CoverageOptions{Lane: "fe", Format: testreport.FormatLCOV})
	if err == nil || !strings.Contains(err.Error(), "DA before SF") {
		t.Errorf("DA after end_of_record: %v", err)
	}
}

func TestParseCoverage_LCOVWithoutTrailingNewlineAndLongLines(t *testing.T) {
	c, err := parseCovString(t, "SF:a.ts\nDA:1,3\nDA:2,0", testreport.CoverageOptions{Lane: "fe", Format: testreport.FormatLCOV})
	if err != nil {
		t.Fatal(err)
	}
	if s := c.Summary().Combined; s.Statements != 2 || s.Covered != 1 {
		t.Errorf("a last line with no newline still counts: %+v", s)
	}
	long := "SF:" + strings.Repeat("a", 200) + "\n"
	_, err = parseCovString(t, long, testreport.CoverageOptions{Lane: "fe", Format: testreport.FormatLCOV, Limits: testreport.Limits{MaxLineBytes: 100}})
	if err == nil || !strings.Contains(err.Error(), "longer than 100 bytes") {
		t.Errorf("an over-long LCOV line: %v", err)
	}
	_, err = parseCovString(t, "mode: count\n"+strings.Repeat("x", 200)+"\n", testreport.CoverageOptions{Lane: "u", Format: testreport.FormatGoCover, Limits: testreport.Limits{MaxLineBytes: 100}})
	if err == nil || !strings.Contains(err.Error(), "longer than 100 bytes") {
		t.Errorf("an over-long Go profile line: %v", err)
	}
}

func TestParseCoverage_GoProfileMalformedLines(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"no colon", "x.com/p/a.go 1 1"},
		{"bad position", "x.com/p/a.go:a.b,c.d 1 1"},
		{"missing comma", "x.com/p/a.go:1.1 1 1"},
		{"bad start column", "x.com/p/a.go:1.x,2.2 1 1"},
		{"bad statement count", "x.com/p/a.go:1.1,2.2 x 1"},
		{"bad hit count", "x.com/p/a.go:1.1,2.2 1 x"},
		{"negative hit count", "x.com/p/a.go:1.1,2.2 1 -1"},
		{"negative column", "x.com/p/a.go:1.-1,2.2 1 1"},
		{"no dot in a position", "x.com/p/a.go:1,2 1 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCovString(t, "mode: count\n"+tc.line+"\n", testreport.CoverageOptions{Lane: "u", Format: testreport.FormatGoCover})
			if err == nil || !strings.Contains(err.Error(), "go profile line 2") {
				t.Fatalf("err = %v, want a go profile error naming line 2", err)
			}
		})
	}
}

func TestParseCoverage_CoberturaAndIstanbulMalformed(t *testing.T) {
	cob := func(body string) string {
		return `<coverage><sources><source>/r</source></sources><packages><package><classes>` + body + `</classes></package></packages></coverage>`
	}
	for _, tc := range []struct {
		name   string
		format testreport.Format
		in     string
		want   string
	}{
		{"cobertura line without a number", testreport.FormatCobertura, cob(`<class filename="a.py"><lines><line hits="1"/></lines></class>`), "malformed <line"},
		{"cobertura line with a bad hit count", testreport.FormatCobertura, cob(`<class filename="a.py"><lines><line number="1" hits="many"/></lines></class>`), "malformed <line"},
		{"cobertura that is not XML", testreport.FormatCobertura, `<coverage><unclosed></coverage>`, "cobertura"},
		{"cobertura with no coverage root", testreport.FormatCobertura, `<other/>`, "no input"},
		{"istanbul that is not JSON", testreport.FormatIstanbul, `{"a.ts": `, "istanbul"},
		{"istanbul with an empty document", testreport.FormatIstanbul, ``, "no input"},
		{"istanbul with a malformed hit count", testreport.FormatIstanbul, `{"a.ts":{"statementMap":{"0":{"start":{"line":1},"end":{"line":1}}},"s":{"0":-3}}}`, "malformed count"},
		{"istanbul with a fractional hit count", testreport.FormatIstanbul, `{"a.ts":{"statementMap":{"0":{"start":{"line":1},"end":{"line":1}}},"s":{"0":1.5}}}`, "malformed count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCovString(t, tc.in, testreport.CoverageOptions{Lane: "fe", Format: tc.format})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestParseCoverage_CoberturaRelativeSourceAndIstanbulPaths(t *testing.T) {
	// A relative filename is joined to the first <source>; an absolute one is kept; a method's lines repeat the class's and do not count.
	c, err := parseCovString(t, `<coverage><sources><source>/r/app</source><source>/ignored</source></sources><packages><package><classes>
<class filename="m.py"><methods><method><lines><line number="1" hits="1"/></lines></method></methods><lines><line number="1" hits="0"/></lines></class>
<class filename="/r/abs.py"><lines><line number="4" hits="2"/></lines></class>
</classes></package></packages></coverage>`, testreport.CoverageOptions{Lane: "py", Format: testreport.FormatCobertura, Root: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]testreport.Tally{}
	for _, f := range c.Files {
		var tl testreport.Tally
		for _, b := range f.Blocks {
			tl.Statements += b.Statements
			if b.Covered() {
				tl.Covered += b.Statements
			}
		}
		got[f.Path] = tl
	}
	if got["app/m.py"] != (testreport.Tally{Statements: 1, Covered: 0}) || got["abs.py"] != (testreport.Tally{Statements: 1, Covered: 1}) || len(got) != 2 {
		t.Errorf("files = %+v", got)
	}

	// istanbul: a "path" field wins over the map key, and a missing hit count is zero.
	c, err = parseCovString(t, `{"k1":{"path":"/r/src/a.ts","statementMap":{"0":{"start":{"line":1,"column":2},"end":{"line":1,"column":-4}},"1":{"start":{"line":3},"end":{"line":3}}},"s":{"0":2}}}`,
		testreport.CoverageOptions{Lane: "fe", Format: testreport.FormatIstanbul, Root: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	f := c.Files[0]
	if f.Path != "src/a.ts" || len(f.Blocks) != 2 || f.Blocks[0].StartCol != 2 || f.Blocks[0].EndCol != 0 || !f.Blocks[0].Covered() || f.Blocks[1].Covered() {
		t.Errorf("istanbul file = %+v", f)
	}
}

func TestParseCoverage_LimitsApplyToEveryFormat(t *testing.T) {
	var le *testreport.LimitError
	_, err := parseCovString(t, "SF:a.ts\nend_of_record\nSF:b.ts\nend_of_record\n", testreport.CoverageOptions{Lane: "fe", Format: testreport.FormatLCOV, Limits: testreport.Limits{MaxCases: 1}})
	if !errors.As(err, &le) || le.Limit != "MaxCases" {
		t.Errorf("LCOV MaxCases: %v", err)
	}
	_, err = parseCovString(t, `{"a.ts":{"statementMap":{},"s":{}},"b.ts":{"statementMap":{},"s":{}}}`, testreport.CoverageOptions{Lane: "fe", Format: testreport.FormatIstanbul, Limits: testreport.Limits{MaxCases: 1}})
	if !errors.As(err, &le) || le.Limit != "MaxCases" {
		t.Errorf("istanbul MaxCases: %v", err)
	}
	_, err = parseCovString(t, `<coverage><classes><class filename="a.py"/><class filename="b.py"/></classes></coverage>`, testreport.CoverageOptions{Lane: "py", Format: testreport.FormatCobertura, Limits: testreport.Limits{MaxCases: 1}})
	if !errors.As(err, &le) || le.Limit != "MaxCases" {
		t.Errorf("cobertura MaxCases: %v", err)
	}
	_, err = parseCovString(t, `{"a.ts":{"statementMap":{"0":{"start":{"line":1},"end":{"line":1}},"1":{"start":{"line":2},"end":{"line":2}}},"s":{}}}`, testreport.CoverageOptions{Lane: "fe", Format: testreport.FormatIstanbul, Limits: testreport.Limits{MaxBlocks: 1}})
	if !errors.As(err, &le) || le.Limit != "MaxBlocks" {
		t.Errorf("istanbul MaxBlocks: %v", err)
	}
	_, err = parseCovString(t, strings.Repeat("a", 100), testreport.CoverageOptions{Lane: "fe", Format: testreport.FormatLCOV, Limits: testreport.Limits{MaxInputBytes: 10}})
	if !errors.As(err, &le) || le.Limit != "MaxInputBytes" {
		t.Errorf("MaxInputBytes: %v", err)
	}
	if _, err := parseCovString(t, "x", testreport.CoverageOptions{Lane: "fe", Format: testreport.FormatJUnit}); err == nil || !strings.Contains(err.Error(), "not a coverage format") {
		t.Errorf("a results format given as coverage: %v", err)
	}
	if _, err := parseCovString(t, "<testsuite/>", testreport.CoverageOptions{Lane: "fe"}); err == nil || !strings.Contains(err.Error(), "not a coverage format") {
		t.Errorf("detected results format given as coverage: %v", err)
	}
}

func TestDelta(t *testing.T) {
	head, err := parseCovString(t, "mode: count\nx.com/p/a.go:1.1,2.2 3 1\nx.com/p/a.go:3.1,4.2 1 0\n", testreport.CoverageOptions{Lane: "u", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	base, err := parseCovString(t, "mode: count\nx.com/p/a.go:1.1,2.2 1 1\nx.com/p/a.go:3.1,4.2 3 0\n", testreport.CoverageOptions{Lane: "u", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if d := testreport.Delta(head, nil); d.Available || d.Reason != "no report for base" {
		t.Errorf("no base: %+v", d)
	}
	d := testreport.Delta(head, base)
	if !d.Available || d.Value != 50 || d.Head.Covered != 3 || d.Base.Covered != 1 {
		t.Errorf("75%% against 25%%: %+v", d)
	}
	empty := &testreport.Coverage{}
	if d := testreport.Delta(head, empty); !d.Available || d.Value != 75 {
		t.Errorf("an empty base counts as 0%%: %+v", d)
	}
	if d := testreport.Delta(empty, base); d.Value != -25 {
		t.Errorf("an empty head counts as 0%%: %+v", d)
	}
}

func TestRollup_ByFileAndByPackage(t *testing.T) {
	doc, err := parseCovString(t, "mode: count\nx.com/m/a/one.go:1.1,2.2 2 1\nx.com/m/a/two.go:1.1,2.2 2 0\nx.com/m/b/three.go:1.1,2.2 1 1\n", testreport.CoverageOptions{Lane: "u", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	// Without a repo path a Go package rolls up under its import path; with one, under its directory.
	byPkg := doc.Rollup(testreport.ByPackage)
	if len(byPkg) != 2 || byPkg[0].Path != "x.com/m/a" || byPkg[0].Combined.Pct != 50 || byPkg[1].Path != "x.com/m/b" || byPkg[1].Combined.Pct != 100 {
		t.Errorf("by package (import paths) = %+v", byPkg)
	}
	m := merge(t, testreport.MergeOptions{ModulePath: "x.com/m", ModuleDir: "svc"}, doc)
	byPkg = m.Rollup(testreport.ByPackage)
	if len(byPkg) != 2 || byPkg[0].Path != "svc/a" || byPkg[1].Path != "svc/b" {
		t.Errorf("by package (repo paths) = %+v", byPkg)
	}
	byFile := m.Rollup(testreport.ByFile)
	if len(byFile) != 3 || byFile[0].Path != "svc/a/one.go" || byFile[2].Path != "svc/b/three.go" {
		t.Errorf("by file = %+v", byFile)
	}
	if o := byFile[0].Lanes["u"]; o.Statements != 2 || o.Covered != 2 {
		t.Errorf("a file's per-lane tally = %+v", o)
	}
}

func TestRollup_SkipsExcludedAndSortsByLanguage(t *testing.T) {
	ts, err := parseCovString(t, "SF:src/z.ts\nDA:1,1\nend_of_record\n", testreport.CoverageOptions{Lane: "fe", Format: testreport.FormatLCOV, Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	gen, err := parseCovString(t, "mode: count\nx.com/m/a/a.go:1.1,2.2 1 1\nx.com/m/a/b_gen.go:1.1,2.2 5 1\n", testreport.CoverageOptions{Lane: "u", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	m := merge(t, testreport.MergeOptions{}, ts, gen)
	rs := m.Rollup(testreport.ByFile)
	if len(rs) != 2 || rs[0].Lang != testreport.LangGo || rs[1].Lang != testreport.LangTS {
		t.Errorf("generated files are skipped and languages sorted: %+v", rs)
	}
	s := m.Summary()
	if len(s.Languages) != 2 || s.CombinedLabel != "weighted by statements across go, ts" {
		t.Errorf("summary languages = %+v label %q", s.Languages, s.CombinedLabel)
	}
	if got := (&testreport.Coverage{}).Summary(); got.Languages == nil || len(got.Languages) != 0 || got.CombinedLabel != "" {
		t.Errorf("an empty document has an empty (non-nil) language list: %+v", got)
	}
}

func TestDiff(t *testing.T) {
	doc, err := parseCovString(t, "mode: count\n"+
		"x.com/m/a.go:1.1,3.2 1 1\n"+ // lines 1-3 covered
		"x.com/m/a.go:5.1,6.2 1 0\n"+ // lines 5-6 not
		"x.com/m/a.go:6.1,7.2 1 1\n"+ // line 6 spanned by a covered block too: partly covered counts as not covered
		"x.com/m/b.go:1.1,1.9 1 1\n", testreport.CoverageOptions{Lane: "u", Tree: "t"})
	if err != nil {
		t.Fatal(err)
	}
	m := merge(t, testreport.MergeOptions{ModulePath: "x.com/m"}, doc)
	d := m.Diff([]testreport.ChangedFile{
		{Path: "./a.go", Ranges: []testreport.LineRange{{Start: 2, End: 6}, {Start: 4, End: 8}}},
		{Path: "b.go", Ranges: []testreport.LineRange{{Start: 50, End: 60}}}, // no executable line changed
		{Path: "ghost.go", Ranges: []testreport.LineRange{{Start: 1, End: 1}}},
	})
	if d.Lines != 5 || d.Covered != 3 || d.Pct != 60 || len(d.Files) != 1 || d.Truncated {
		t.Fatalf("diff = %+v", d)
	}
	f := d.Files[0]
	if f.Path != "a.go" || !reflect.DeepEqual(f.Uncovered, []testreport.LineRange{{Start: 5, End: 6}}) {
		t.Errorf("file = %+v", f)
	}
	if d := m.Diff(nil); d.Lines != 0 || d.Pct != 0 || d.Files != nil {
		t.Errorf("nothing changed: %+v", d)
	}
}

func TestIsGeneratedAndMatchGlob(t *testing.T) {
	for p, want := range map[string]bool{
		"internal/x/mock_gen.go":    true,
		"gen/x/v1/x.pb.go":          true,
		"a/gen/b.go":                true,
		`a\gen\b.go`:                true,
		"web/src/api-hooks.ts":      true,
		"x.pb.go":                   true,
		"internal/generator/gen.go": false,
		"internal/gender/x.go":      false,
		"web/src/api.ts":            false,
	} {
		if got := testreport.IsGenerated(p); got != want {
			t.Errorf("IsGenerated(%q) = %v, want %v", p, got, want)
		}
	}
	for _, tc := range []struct {
		glob, path string
		want       bool
	}{
		{"*_test.go", "a/b/x_test.go", true},
		{"**/*.go", "a.go", true},
		{"a/**", "a/b/c.go", true},
		{"a/**/z.go", "a/z.go", true},
		{"a/**/z.go", "a/b/c/z.go", true},
		{"a/**/z.go", "a/b/c/y.go", false},
		{"./a/*.go", "./a/x.go", true},
		{"a/*.go", "a/b/x.go", false},
		{"a/b.go", "a", false},
		{"[", "a", false},
	} {
		if got := testreport.MatchGlob(tc.glob, tc.path); got != tc.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", tc.glob, tc.path, got, tc.want)
		}
	}
}

func TestDefaultRulesAreCopies(t *testing.T) {
	rules := testreport.DefaultHostRules()
	if len(rules) == 0 {
		t.Fatal("want built-in host rules")
	}
	rules[0] = testreport.HostRule{}
	if again := testreport.DefaultHostRules(); again[0].Regexp == nil {
		t.Error("mutating the returned slice changed the defaults")
	}
	if name, ok := testreport.MatchHost("dial tcp: i/o timeout\ntoomanyrequests: rate limit", nil); !ok || name == "" {
		t.Errorf("the defaults must recognise a registry rate limit, got %q %v", name, ok)
	}

	hs := testreport.DefaultHarnesses()
	names := map[string]bool{}
	for _, h := range hs {
		names[h.Name] = h.Regexp.MatchString("FAIL: " + strings.ToUpper(h.Name) + " unavailable")
	}
	for _, n := range []string{"testsrv", "clickhouse", "pgtest"} {
		if !names[n] {
			t.Errorf("harness %q missing or does not match its own name case-insensitively: %v", n, names)
		}
	}
	hs[0] = testreport.Harness{}
	if testreport.DefaultHarnesses()[0].Regexp == nil {
		t.Error("mutating the returned slice changed the defaults")
	}
}

func TestParseJUnit_LanguageAndDurationEdgeCases(t *testing.T) {
	const doc = `<testsuites>
<testsuite name="suite-a">
  <testcase classname="a.b" name="sloppy" time="1,234.5" file="svc/x.py"/>
  <testcase classname="" name="nosuite" time="-3"/>
  <testcase classname="src/x.test.ts" name="class is a ts path" time="NaN"/>
  <testcase classname="pkg.Class" name="bad time" time="soon"/>
  <testcase classname="pkg.Class" name="huge" time="9e99"/>
</testsuite></testsuites>`
	r, err := testreport.ParseResults(strings.NewReader(doc), testreport.ResultsOptions{Format: testreport.FormatJUnit, Lane: "py"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]testreport.Case{}
	for _, c := range r.Cases {
		byName[c.ID.Name] = c
	}
	if c := byName["sloppy"]; c.DurationMS != 1234500 || c.ID.Lang != testreport.LangFromPath("svc/x.py") {
		t.Errorf("a thousands separator is read, and the language comes from the file: %+v", c)
	}
	if c := byName["nosuite"]; c.ID.Package != "suite-a" || c.DurationMS != 0 {
		t.Errorf("a missing classname falls back to the suite; a negative time is zero: %+v", c)
	}
	if c := byName["class is a ts path"]; c.ID.Lang != testreport.LangTS || c.DurationMS != 0 {
		t.Errorf("a ts-looking classname makes a ts case: %+v", c)
	}
	for _, n := range []string{"bad time", "huge"} {
		if c := byName[n]; c.DurationMS != 0 {
			t.Errorf("%s: an unreadable or absurd time is zero, got %d", n, c.DurationMS)
		}
	}

	forced, err := testreport.ParseResults(strings.NewReader(doc), testreport.ResultsOptions{Format: testreport.FormatJUnit, Lane: "py", Lang: testreport.LangGo})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range forced.Cases {
		if c.ID.Lang != testreport.LangGo {
			t.Errorf("opts.Lang overrides the per-case language: %+v", c.ID)
		}
	}
}

func TestParseJUnit_MalformedAndEmpty(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"not XML", `<testsuite><testcase name="x"`, "junit"},
		{"no suite", `<html/>`, "no input"},
	} {
		_, err := testreport.ParseResults(strings.NewReader(tc.in), testreport.ResultsOptions{Format: testreport.FormatJUnit})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one mentioning %q", tc.name, err, tc.want)
		}
	}
	var le *testreport.LimitError
	_, err := testreport.ParseResults(strings.NewReader(`<testsuite><testcase classname="a" name="1"/><testcase classname="b" name="2"/></testsuite>`),
		testreport.ResultsOptions{Format: testreport.FormatJUnit, Limits: testreport.Limits{MaxPackages: 1}})
	if !errors.As(err, &le) || le.Limit != "MaxPackages" {
		t.Errorf("MaxPackages: %v", err)
	}
}
