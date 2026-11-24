// File: profiles_pgo.go — what profile-guided builds need from an exported
// profile that the translator and the ClickHouse exporter do not keep.
//
// Go's PGO keys a call-site edge on (caller, line - caller's StartLine, callee).
// The contrib clickhouse exporter flattens each sample's stack into parallel
// FunctionNames / FileNames / LineNumbers arrays and drops Function.start_line,
// so the start lines travel as a sample attribute instead (Contract A of the
// PGO design, read by profiles-schema.sql's profile_pgo_edges_mv):
//
//	forge.profile.start_lines = "<start>,<start>,..."
//
// one decimal per frame, in the exporter's flattening order: the stack's
// locations leaf first, each location's lines in order, a location with no
// lines as one 0. An unknown start line is 0. Beside it:
//
//	forge.profile.leaf_frames = "<n>"
//
// how many of those frames belong to the stack's first two locations. Go's
// PGO graph (GOROOT/src/internal/profile/graph.go, NewGraph) counts call edges
// only among the frames of a sample's two leaf-most locations, and the
// exporter's flat arrays lose the location boundaries. Only CPU samples carry
// either attribute.

package observe

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/pprof/profile"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pprofile"
)

// The two per-sample attributes (see the file header).
const (
	startLinesAttribute = "forge.profile.start_lines"
	leafFramesAttribute = "forge.profile.leaf_frames"
)

// inlineOrder records, for every pprof location with inlined frames, its lines
// in the order pprof gives them: the innermost inlinee first, the caller it
// was inlined into last. Capture it BEFORE conversion: the translator sorts a
// location's lines by line number (in place, on the input profile too), which
// puts an inlinee and its caller in an arbitrary order in the OTLP location and
// so in every flattened stack. OTLP's Location.lines has the same semantics as
// pprof's, so restoring the order is a correction, not a reinterpretation.
func inlineOrder(p *profile.Profile) map[*profile.Location][]profile.Line {
	orig := map[*profile.Location][]profile.Line{}
	for _, l := range p.Location {
		if len(l.Line) > 1 {
			orig[l] = append([]profile.Line(nil), l.Line...)
		}
	}
	return orig
}

// restoreInlineOrder puts each OTLP location's lines back in the pprof order
// orig recorded. It pairs pprof and OTLP locations through the samples (the
// translator keeps sample order and each stack's location order), and matches
// lines by function identity, line and column rather than by position, so it
// stays correct whether or not a translator release sorts in place.
func restoreInlineOrder(p *profile.Profile, orig map[*profile.Location][]profile.Line, pd pprofile.Profiles) error {
	if len(orig) == 0 || pd.ResourceProfiles().Len() == 0 {
		return nil
	}
	sps := pd.ResourceProfiles().At(0).ScopeProfiles()
	if sps.Len() == 0 || sps.At(0).Profiles().Len() == 0 {
		return nil
	}
	// Every sample-type profile holds the same samples over one dictionary, so
	// the first one pairs every location.
	samples := sps.At(0).Profiles().At(0).Samples()
	if samples.Len() != len(p.Sample) {
		return fmt.Errorf("pprof translator changed sample order/length")
	}
	dic := pd.Dictionary()
	stacks, locs := dic.StackTable(), dic.LocationTable()
	done := map[int32]bool{}
	for i, s := range p.Sample {
		si := samples.At(i).StackIndex()
		if si < 0 || int(si) >= stacks.Len() {
			continue
		}
		idx := stacks.At(int(si)).LocationIndices()
		if idx.Len() != len(s.Location) {
			return fmt.Errorf("pprof translator changed a stack's length")
		}
		for j, pl := range s.Location {
			want, ok := orig[pl]
			li := idx.At(j)
			if !ok || done[li] || int(li) >= locs.Len() {
				continue
			}
			done[li] = true
			reorderLines(dic, locs.At(int(li)).Lines(), want)
		}
	}
	return nil
}

// reorderLines rewrites lines into the order of want. It leaves lines alone
// when they cannot all be matched, which only a translator that rewrote line
// content could cause.
func reorderLines(dic pprofile.ProfilesDictionary, lines pprofile.LineSlice, want []profile.Line) {
	if lines.Len() != len(want) {
		return
	}
	perm := make([]int, len(want))
	used := make([]bool, lines.Len())
	for k, w := range want {
		perm[k] = -1
		for j := 0; j < lines.Len(); j++ {
			if !used[j] && lineMatches(dic, lines.At(j), w) {
				perm[k], used[j] = j, true
				break
			}
		}
		if perm[k] < 0 {
			return
		}
	}
	ordered := pprofile.NewLineSlice()
	ordered.EnsureCapacity(len(perm))
	for _, j := range perm {
		lines.At(j).CopyTo(ordered.AppendEmpty())
	}
	ordered.CopyTo(lines)
}

func lineMatches(dic pprofile.ProfilesDictionary, l pprofile.Line, w profile.Line) bool {
	if l.Line() != w.Line || l.Column() != w.Column {
		return false
	}
	fns, strs := dic.FunctionTable(), dic.StringTable()
	fi := int(l.FunctionIndex())
	if w.Function == nil {
		return fi <= 0
	}
	if fi < 0 || fi >= fns.Len() {
		return false
	}
	f := fns.At(fi)
	return stringAt(strs, f.NameStrindex()) == w.Function.Name &&
		stringAt(strs, f.SystemNameStrindex()) == w.Function.SystemName &&
		stringAt(strs, f.FilenameStrindex()) == w.Function.Filename &&
		f.StartLine() == w.Function.StartLine
}

func stringAt(table pcommon.StringSlice, i int32) string {
	if i < 0 || int(i) >= table.Len() {
		return ""
	}
	return table.At(int(i))
}

// isCPUProfile reports whether prof is Go's CPU profile (cpu/nanoseconds).
func isCPUProfile(dic pprofile.ProfilesDictionary, prof pprofile.Profile) bool {
	strs := dic.StringTable()
	return stringAt(strs, prof.SampleType().TypeStrindex()) == "cpu" &&
		stringAt(strs, prof.SampleType().UnitStrindex()) == "nanoseconds"
}

// attachStartLines adds the forge.profile.start_lines and
// forge.profile.leaf_frames attributes to every sample of prof. Samples
// sharing a stack share their attribute-table entries.
func attachStartLines(dic pprofile.ProfilesDictionary, prof pprofile.Profile) error {
	if prof.Samples().Len() == 0 {
		return nil
	}
	startsKey, err := pprofile.SetString(dic.StringTable(), startLinesAttribute)
	if err != nil {
		return err
	}
	leafKey, err := pprofile.SetString(dic.StringTable(), leafFramesAttribute)
	if err != nil {
		return err
	}
	attrs := dic.AttributeTable()
	type entry struct {
		key   int32
		value string
	}
	byValue := map[entry]int32{}
	attr := func(key int32, value string) int32 {
		e := entry{key, value}
		if idx, ok := byValue[e]; ok {
			return idx
		}
		kv := attrs.AppendEmpty()
		kv.SetKeyStrindex(key)
		kv.Value().SetStr(value)
		byValue[e] = int32(attrs.Len() - 1)
		return byValue[e]
	}
	byStack := map[int32][2]int32{}
	for s := 0; s < prof.Samples().Len(); s++ {
		sample := prof.Samples().At(s)
		si := sample.StackIndex()
		idx, seen := byStack[si]
		if !seen {
			starts, leaf := startLines(dic, si)
			idx = [2]int32{attr(startsKey, starts), attr(leafKey, strconv.Itoa(leaf))}
			byStack[si] = idx
		}
		sample.AttributeIndices().Append(idx[0], idx[1])
	}
	return nil
}

// startLines renders a stack's start lines in the order the contrib
// clickhouse exporter (v0.162.0, exporter_profiles.go resolveStack) flattens
// it into FunctionNames: a location index out of range is skipped, a location
// with no lines is one frame, and a function index out of range is a frame
// with no name, here start line 0. leaf is how many of those frames belong to
// the stack's first two locations.
func startLines(dic pprofile.ProfilesDictionary, stackIdx int32) (starts string, leaf int) {
	stacks, locs, fns := dic.StackTable(), dic.LocationTable(), dic.FunctionTable()
	if stackIdx < 0 || int(stackIdx) >= stacks.Len() {
		return "", 0
	}
	var b strings.Builder
	frames := 0
	frame := func(start int64) {
		if frames > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(start, 10))
		frames++
	}
	idx := stacks.At(int(stackIdx)).LocationIndices()
	for i := 0; i < idx.Len(); i++ {
		if i == 2 {
			leaf = frames
		}
		li := idx.At(i)
		if int(li) >= locs.Len() {
			continue
		}
		lines := locs.At(int(li)).Lines()
		if lines.Len() == 0 {
			frame(0)
			continue
		}
		for j := 0; j < lines.Len(); j++ {
			var start int64
			if fi := int(lines.At(j).FunctionIndex()); fi >= 0 && fi < fns.Len() {
				start = fns.At(fi).StartLine()
			}
			frame(start)
		}
	}
	if idx.Len() < 3 {
		leaf = frames
	}
	return b.String(), leaf
}
