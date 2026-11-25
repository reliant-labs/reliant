package observe

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pprofile"

	"github.com/google/pprof/profile"
)

// exportedRow is one otel_profiles row as the contrib clickhouse exporter
// (v0.162.0, exporter_profiles.go) writes it: the stack flattened by
// resolveStack, and the sample's attributes as a string map.
type exportedRow struct {
	sampleType string
	functions  []string
	lines      []int32
	attrs      map[string]string
}

// exporterRows mirrors the exporter's flattening, so the tests check the
// attribute against the arrays the materialized view will zip it with.
func exporterRows(t *testing.T, pd pprofile.Profiles) []exportedRow {
	t.Helper()
	dic := pd.Dictionary()
	strs, stacks, locs, fns := dic.StringTable(), dic.StackTable(), dic.LocationTable(), dic.FunctionTable()
	var rows []exportedRow
	for i := 0; i < pd.ResourceProfiles().Len(); i++ {
		sps := pd.ResourceProfiles().At(i).ScopeProfiles()
		for j := 0; j < sps.Len(); j++ {
			profs := sps.At(j).Profiles()
			for k := 0; k < profs.Len(); k++ {
				prof := profs.At(k)
				typ := stringAt(strs, prof.SampleType().TypeStrindex())
				for s := 0; s < prof.Samples().Len(); s++ {
					sample := prof.Samples().At(s)
					row := exportedRow{sampleType: typ, attrs: map[string]string{}}
					idx := stacks.At(int(sample.StackIndex())).LocationIndices()
					for li := 0; li < idx.Len(); li++ {
						if int(idx.At(li)) >= locs.Len() {
							continue
						}
						lines := locs.At(int(idx.At(li))).Lines()
						if lines.Len() == 0 {
							row.functions, row.lines = append(row.functions, ""), append(row.lines, 0)
							continue
						}
						for ln := 0; ln < lines.Len(); ln++ {
							name := ""
							if fi := int(lines.At(ln).FunctionIndex()); fi < fns.Len() {
								name = stringAt(strs, fns.At(fi).NameStrindex())
							}
							row.functions = append(row.functions, name)
							row.lines = append(row.lines, int32(lines.At(ln).Line()))
						}
					}
					attrs, err := pprofile.FromAttributeIndices(dic.AttributeTable(), sample, dic)
					if err != nil {
						t.Fatal(err)
					}
					for key, v := range attrs.All() {
						row.attrs[key] = v.AsString()
					}
					rows = append(rows, row)
				}
			}
		}
	}
	return rows
}

func parseStartLines(t *testing.T, v string) []int64 {
	t.Helper()
	var out []int64
	for _, s := range strings.Split(v, ",") {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("start lines %q: %v", v, err)
		}
		out = append(out, n)
	}
	return out
}

// inlinedCPUProfile is a CPU profile whose hot stack has an inlined frame
// (main.inner inlined into main.work, at a HIGHER line than its caller's call
// site, so a by-line sort swaps them), a location the runtime could not
// symbolize (no lines), and the caller main.main.
func inlinedCPUProfile() *profile.Profile {
	m := &profile.Mapping{ID: 1, Start: 0x1000, Limit: 0x9000, File: "/app/bin/app", HasFunctions: true}
	inner := &profile.Function{ID: 1, Name: "main.inner", Filename: "main.go", StartLine: 40}
	work := &profile.Function{ID: 2, Name: "main.work", Filename: "main.go", StartLine: 8}
	mainFn := &profile.Function{ID: 3, Name: "main.main", Filename: "main.go", StartLine: 2}
	leaf := &profile.Location{ID: 1, Mapping: m, Address: 0x1010, Line: []profile.Line{
		{Function: inner, Line: 42}, {Function: work, Line: 10},
	}}
	unknown := &profile.Location{ID: 2, Mapping: m, Address: 0x2020}
	root := &profile.Location{ID: 3, Mapping: m, Address: 0x3030, Line: []profile.Line{{Function: mainFn, Line: 4}}}
	return &profile.Profile{
		SampleType:    []*profile.ValueType{{Type: "samples", Unit: "count"}, {Type: "cpu", Unit: "nanoseconds"}},
		PeriodType:    &profile.ValueType{Type: "cpu", Unit: "nanoseconds"},
		Period:        10_000_000,
		TimeNanos:     time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC).UnixNano(),
		DurationNanos: int64(5 * time.Second),
		Mapping:       []*profile.Mapping{m},
		Function:      []*profile.Function{inner, work, mainFn},
		Location:      []*profile.Location{leaf, unknown, root},
		Sample: []*profile.Sample{
			{Location: []*profile.Location{leaf, unknown, root}, Value: []int64{3, 30_000_000},
				Label: map[string][]string{"trace_id": {testTraceHex}, "span_id": {testSpanHex}}},
			{Location: []*profile.Location{root}, Value: []int64{1, 10_000_000}},
			{Location: []*profile.Location{leaf, root}, Value: []int64{0, 0}}, // dropped: zero
		},
	}
}

func TestCPUSamplesCarryStartLinesInTheExportersFrameOrder(t *testing.T) {
	pd, err := pprofToOTLP(encode(t, inlinedCPUProfile()), testResource())
	if err != nil {
		t.Fatal(err)
	}
	rows := exporterRows(t, pd)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (the zero sample is dropped)", len(rows))
	}
	want := []struct {
		functions []string
		starts    string
		leaf      string
	}{
		// Inlinee first, then the caller it was inlined into, then the
		// line-less location as one frame, then main.main. The two leaf
		// locations are the inlined pair and the line-less one: 3 frames.
		{[]string{"main.inner", "main.work", "", "main.main"}, "40,8,0,2", "3"},
		{[]string{"main.main"}, "2", "1"},
	}
	for i, row := range rows {
		if !slices.Equal(row.functions, want[i].functions) {
			t.Errorf("row %d FunctionNames = %q, want %q", i, row.functions, want[i].functions)
		}
		got, ok := row.attrs[startLinesAttribute]
		if !ok {
			t.Fatalf("row %d has no %s attribute: %v", i, startLinesAttribute, row.attrs)
		}
		if got != want[i].starts {
			t.Errorf("row %d %s = %q, want %q", i, startLinesAttribute, got, want[i].starts)
		}
		if n := len(parseStartLines(t, got)); n != len(row.functions) {
			t.Errorf("row %d: %d start lines for %d frames", i, n, len(row.functions))
		}
		if leaf := row.attrs[leafFramesAttribute]; leaf != want[i].leaf {
			t.Errorf("row %d %s = %q, want %q", i, leafFramesAttribute, leaf, want[i].leaf)
		}
	}
	// The PGO call-site offset of main.work calling main.inner is line - start.
	if off := rows[0].lines[1] - 8; off != 2 {
		t.Errorf("main.work's call-site offset = %d, want 2", off)
	}
}

// The leaf frame count stops after the second location, however deep the
// stack, and counts both frames of an inlined leaf.
func TestLeafFramesCountTheTwoLeafLocations(t *testing.T) {
	p := inlinedCPUProfile()
	leaf, unknown, root := p.Location[0], p.Location[1], p.Location[2]
	deep := &profile.Location{ID: 4, Mapping: leaf.Mapping, Address: 0x4040, Line: []profile.Line{
		{Function: &profile.Function{ID: 4, Name: "runtime.main", Filename: "proc.go", StartLine: 150}, Line: 283},
	}}
	p.Function = append(p.Function, deep.Line[0].Function)
	p.Location = append(p.Location, deep)
	p.Sample = []*profile.Sample{
		{Location: []*profile.Location{leaf, root, deep}, Value: []int64{1, 10_000_000}},
		{Location: []*profile.Location{root, leaf}, Value: []int64{1, 10_000_000}},
		{Location: []*profile.Location{unknown, root, deep}, Value: []int64{1, 10_000_000}},
	}
	pd, err := pprofToOTLP(encode(t, p), nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := exporterRows(t, pd)
	for i, want := range []string{"3", "3", "2"} {
		if got := rows[i].attrs[leafFramesAttribute]; got != want {
			t.Errorf("row %d (%q) %s = %q, want %q", i, rows[i].functions, leafFramesAttribute, got, want)
		}
	}
}

func TestStartLinesShareOneAttributeEntryPerStack(t *testing.T) {
	p := inlinedCPUProfile()
	p.Sample = append(p.Sample, &profile.Sample{Location: p.Sample[1].Location, Value: []int64{2, 20_000_000}})
	pd, err := pprofToOTLP(encode(t, p), nil)
	if err != nil {
		t.Fatal(err)
	}
	entries := 0
	dic := pd.Dictionary()
	for i := 0; i < dic.AttributeTable().Len(); i++ {
		if stringAt(dic.StringTable(), dic.AttributeTable().At(i).KeyStrindex()) == startLinesAttribute {
			entries++
		}
	}
	for s := 0; s < pd.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles().At(0).Samples().Len(); s++ {
		sample := pd.ResourceProfiles().At(0).ScopeProfiles().At(0).Profiles().At(0).Samples().At(s)
		if n := sample.AttributeIndices().Len(); n < 2 {
			t.Errorf("sample %d has %d attributes, want start lines and leaf frames", s, n)
		}
	}
	if entries != 2 {
		t.Errorf("%d %s attribute entries for 2 distinct stacks, want 2", entries, startLinesAttribute)
	}
}

func TestHeapSamplesCarryNoStartLines(t *testing.T) {
	for _, drop := range [][]string{nil, {"alloc_objects", "alloc_space"}} {
		p := heapFixture(50)
		// A heap stack with an inlined frame, as the runtime writes them.
		p.Function[0].StartLine, p.Function[1].StartLine = 7, 3
		p.Location[0].Line = append(p.Location[0].Line, profile.Line{Function: p.Function[1], Line: 1})
		pd, err := pprofToOTLP(encode(t, p), testResource(), drop...)
		if err != nil {
			t.Fatal(err)
		}
		rows := exporterRows(t, pd)
		if len(rows) == 0 {
			t.Fatal("no heap rows")
		}
		for _, row := range rows {
			for _, key := range []string{startLinesAttribute, leafFramesAttribute} {
				if _, ok := row.attrs[key]; ok {
					t.Fatalf("%s sample carries %s", row.sampleType, key)
				}
			}
		}
		strs := pd.Dictionary().StringTable()
		for i := 0; i < strs.Len(); i++ {
			if strs.At(i) == startLinesAttribute || strs.At(i) == leafFramesAttribute {
				t.Fatalf("heap profile's string table holds %s", startLinesAttribute)
			}
		}
	}
}

// pgoSpin is the known function the real-profile test looks for. It must stay
// out of line so it is its own frame.
//
//go:noinline
func pgoSpin(deadline time.Time) int {
	x := 0
	for time.Now().Before(deadline) {
		for i := 0; i < 1e5; i++ {
			x += pgoSquare(i)
		}
	}
	return x
}

// pgoSquare is small enough that the compiler inlines it into pgoSpin.
func pgoSquare(i int) int { return i * i }

// declLine is the line of a function's `func` keyword in this file, which is
// what the Go runtime records as Function.StartLine.
func declLine(t *testing.T, name string) int64 {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "profiles_pgo_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			return int64(fset.Position(fd.Pos()).Line)
		}
	}
	t.Fatalf("no func %s in profiles_pgo_test.go", name)
	return 0
}

// A real runtime/pprof CPU profile: every sample's start lines line up with
// its flattened frames, including inlined ones, and the known function's value
// is the line its declaration starts on.
func TestRealCPUProfileStartLinesMatchTheRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: burns CPU for 1s under the real profiler; runs in task test")
	}
	var buf bytes.Buffer
	if err := pprof.StartCPUProfile(&buf); err != nil {
		t.Skip(err)
	}
	_ = pgoSpin(time.Now().Add(time.Second))
	pprof.StopCPUProfile()

	pd, err := pprofToOTLP(&buf, testResource())
	if err != nil {
		t.Fatal(err)
	}
	spinStart, squareStart := declLine(t, "pgoSpin"), declLine(t, "pgoSquare")
	sawSpin, sawInlined := false, false
	for i, row := range exporterRows(t, pd) {
		v, ok := row.attrs[startLinesAttribute]
		if !ok {
			t.Fatalf("CPU row %d has no %s", i, startLinesAttribute)
		}
		starts := parseStartLines(t, v)
		if len(starts) != len(row.functions) {
			t.Fatalf("row %d: %d start lines for %d frames %q", i, len(starts), len(row.functions), row.functions)
		}
		leaf, err := strconv.Atoi(row.attrs[leafFramesAttribute])
		if err != nil || leaf < min(2, len(row.functions)) || leaf > len(row.functions) {
			t.Fatalf("row %d: %s = %q for %d frames", i, leafFramesAttribute, row.attrs[leafFramesAttribute], len(row.functions))
		}
		for f, name := range row.functions {
			switch {
			case strings.HasSuffix(name, ".pgoSpin"):
				sawSpin = true
				if starts[f] != spinStart {
					t.Errorf("pgoSpin start line = %d, want %d", starts[f], spinStart)
				}
			case strings.HasSuffix(name, ".pgoSquare"):
				if starts[f] != squareStart {
					t.Errorf("pgoSquare start line = %d, want %d", starts[f], squareStart)
				}
				// Inlined: its caller is the next frame, in the same location.
				if f+1 < len(row.functions) && strings.HasSuffix(row.functions[f+1], ".pgoSpin") {
					sawInlined = true
				}
			}
		}
	}
	if !sawSpin {
		t.Fatal("no CPU sample in pgoSpin")
	}
	if !sawInlined {
		t.Log("no sample caught the inlined pgoSquare frame (it is rarely the leaf)")
	}
}

// FORGE_PROFILES_PGO: unset or "on" attaches the start lines (the default),
// "off" leaves them off, and anything else is ignored with a warning, the
// way FORGE_PROFILES is.
func TestProfilesPGOEnvSelectsStartLines(t *testing.T) {
	for v, want := range map[string]struct {
		off  bool
		warn bool
	}{
		"": {}, "on": {}, " ON ": {}, "off": {off: true}, "Off": {off: true}, "false": {warn: true},
	} {
		clearProfilesEnv(t)
		t.Setenv(EnvProfilesPGO, v)
		s, warns := resolveProfileSettings(ProfilesConfig{})
		if s.pgoOff != want.off {
			t.Errorf("%s=%q: pgoOff = %v, want %v", EnvProfilesPGO, v, s.pgoOff, want.off)
		}
		if gotWarn := len(warns) == 1 && strings.Contains(warns[0].Error(), EnvProfilesPGO); gotWarn != want.warn || (!want.warn && len(warns) > 0) {
			t.Errorf("%s=%q: warnings = %v, want a warning: %v", EnvProfilesPGO, v, warns, want.warn)
		}
	}
}

// With PGO off the CPU samples carry neither attribute and the dictionary
// holds neither key; the stacks are unchanged.
func TestStartLinesOffLeavesCPUSamplesBare(t *testing.T) {
	on, err := pprofToOTLPWith(encode(t, inlinedCPUProfile()), testResource(), true)
	if err != nil {
		t.Fatal(err)
	}
	off, err := pprofToOTLPWith(encode(t, inlinedCPUProfile()), testResource(), false)
	if err != nil {
		t.Fatal(err)
	}
	onRows, offRows := exporterRows(t, on), exporterRows(t, off)
	if len(offRows) != len(onRows) || len(offRows) == 0 {
		t.Fatalf("rows: %d with start lines, %d without", len(onRows), len(offRows))
	}
	for i, row := range offRows {
		if !slices.Equal(row.functions, onRows[i].functions) {
			t.Errorf("row %d stack changed: %q vs %q", i, row.functions, onRows[i].functions)
		}
		for _, key := range []string{startLinesAttribute, leafFramesAttribute} {
			if _, ok := row.attrs[key]; ok {
				t.Errorf("row %d carries %s with PGO off", i, key)
			}
		}
	}
	strs := off.Dictionary().StringTable()
	for i := 0; i < strs.Len(); i++ {
		if strs.At(i) == startLinesAttribute || strs.At(i) == leafFramesAttribute {
			t.Fatalf("string table holds %s with PGO off", strs.At(i))
		}
	}
}

// The capture loop honours the setting on what it sends.
func TestProfilerSendsNoStartLinesWhenPGOIsOff(t *testing.T) {
	clearProfilesEnv(t)
	t.Setenv(EnvProfilesPGO, "off")
	var got []pprofile.Profiles
	p := testProfiler(func(context.Context) error { return nil }, func(_ context.Context, pd pprofile.Profiles) error {
		got = append(got, pd)
		return nil
	})
	p.settings, _ = resolveProfileSettings(ProfilesConfig{Types: []ProfileType{ProfileCPU}})
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(encode(t, inlinedCPUProfile())); err != nil {
		t.Fatal(err)
	}
	if err := p.deliver(context.Background(), capture{name: "cpu", raw: &raw}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("sent %d profiles, want 1", len(got))
	}
	for _, row := range exporterRows(t, got[0]) {
		if _, ok := row.attrs[startLinesAttribute]; ok {
			t.Fatalf("the profiler sent start lines with %s=off", EnvProfilesPGO)
		}
	}
}
