package obsbackend

import (
	"sort"
	"strings"
	"time"
)

// Trace-tree caps (DESIGN §8.1, Sentry's overview discipline). --full lifts
// them.
const (
	MaxTreeDepth = 8
	MaxTreeSpans = 100
)

// TreeNode is one rendered node of a trace: a span, or a collapsed run of
// identical siblings.
type TreeNode struct {
	Span Span
	// Count > 1 marks a collapsed run: Count identical sibling spans (same
	// service, name and status) shown once, with Min/P50/P95/Max over their
	// durations.
	Count    int
	Min, Max time.Duration
	P50, P95 time.Duration
	// Error is true when this span (or, for a run, any span in it) failed.
	Error bool
	// Critical marks the spans on the critical path: from the root, the child
	// that ends last at each level.
	Critical bool
	// Offset is the span's start relative to the trace's first span.
	Offset   time.Duration
	Children []*TreeNode
	// Hidden counts descendants not rendered because of a cap, so the output
	// can say "… 12 more spans" instead of dropping them silently.
	Hidden int
	// Uninstrumented is how much of the span no child span covers, when that
	// is enough to be where the time went (see [IsGap]); zero otherwise. A
	// handler that took 10s with 15ms of children spent the rest in code that
	// emitted no span, and the reader should look there, not at the children.
	// Gap is the largest such stretch, as offsets from the span's start.
	Uninstrumented time.Duration
	Gap            [2]time.Duration
	// Unread is how much of the span no READ child covers after the read
	// stopped (TreeOptions.ReadUntil), when [IsGap] says it matters. Its
	// children there were never fetched, so the time is unknown, not
	// uninstrumented; UnreadFrom is where it begins, from the span's start.
	Unread     time.Duration
	UnreadFrom time.Duration
	// WholeTrace is this run's count over the whole trace
	// (TreeOptions.Runs) when the read saw only part of it: the read run is
	// "×4996", the trace's is "×79427".
	WholeTrace *SpanGroup
}

// Thresholds for reporting a span's uninstrumented time: at least
// GapMinDuration, and at least GapMinShare of the span. Smaller gaps are the
// ordinary cost of code between calls.
const (
	GapMinDuration = 50 * time.Millisecond
	GapMinShare    = 0.2
)

// IsGap reports whether uninstrumented time gap in a span of duration dur is
// worth pointing at.
func IsGap(gap, dur time.Duration) bool {
	return gap >= GapMinDuration && float64(gap) >= GapMinShare*float64(dur)
}

// Tree is a rendered trace.
type Tree struct {
	Roots []*TreeNode
	// Spans is every span of the trace; Shown how many are rendered (a
	// collapsed run counts once).
	Spans, Shown int
	// Duration is first start to last end.
	Duration time.Duration
	// Orphans counts spans whose parent is not in the trace (a dropped or
	// not-yet-exported parent); they are rendered as extra roots.
	Orphans int
	// Capped reports that a cap hid something.
	Capped bool
}

// TreeOptions shape BuildTreeWith.
type TreeOptions struct {
	// Full lifts the caps.
	Full bool
	// ReadUntil is set when the spans are a truncated read of the trace
	// (TraceResult.SpansTruncated): the start of the last span read. Spans
	// are read in start order, so a child starting after it may exist
	// unread, and a parent's time past it is reported as Unread, never as
	// uninstrumented.
	ReadUntil time.Time
	// Runs are the trace's runs of identical siblings counted over every span
	// (TraceResult.SiblingRuns); a run the read saw only part of carries its
	// whole-trace count in WholeTrace.
	Runs []SpanGroup
}

// BuildTree arranges spans into a tree under the caps. full lifts them.
func BuildTree(spans []Span, full bool) Tree {
	return BuildTreeWith(spans, TreeOptions{Full: full})
}

// BuildTreeWith is BuildTree for a read that may be partial.
func BuildTreeWith(spans []Span, o TreeOptions) Tree {
	full := o.Full
	b := newTreeBuilder(spans, full)
	b.readUntil = o.ReadUntil
	b.runs = map[string]*SpanGroup{}
	for i, r := range o.Runs {
		b.runs[runKey(r.ParentID, r.Service, r.Name, r.StatusCode)] = &o.Runs[i]
	}
	if len(spans) == 0 {
		return b.t
	}
	b.markFailing()
	b.markCritical()
	b.budget = MaxTreeSpans
	if full {
		b.budget = len(spans) + 1
	}
	for _, r := range b.roots {
		if b.budget <= 0 && !b.keep(r) {
			b.t.Capped = true
			continue
		}
		b.t.Roots = append(b.t.Roots, b.build(r, 1))
	}
	return b.t
}

// treeBuilder holds what one BuildTree call computes about a trace.
type treeBuilder struct {
	t      Tree
	full   bool
	budget int
	first  time.Time
	roots  []*Span
	kids   map[string][]*Span
	// failing[id]: this span or a descendant failed. critical[id]: on the
	// critical path.
	failing, critical map[string]bool
	readUntil         time.Time
	// runs are TreeOptions.Runs by runKey.
	runs map[string]*SpanGroup
}

func runKey(parent, service, name, status string) string {
	return parent + "\x00" + service + "\x00" + name + "\x00" + strings.ToLower(status)
}

// wholeRun is the whole-trace run s belongs to, when the trace has more of it
// than the read members (have).
func (b *treeBuilder) wholeRun(s *Span, have int) *SpanGroup {
	r := b.runs[runKey(s.ParentID, s.Service, s.Name, s.StatusCode)]
	if r == nil || r.Count <= have {
		return nil
	}
	return r
}

func newTreeBuilder(spans []Span, full bool) *treeBuilder {
	b := &treeBuilder{
		t: Tree{Spans: len(spans)}, full: full,
		kids: map[string][]*Span{}, failing: map[string]bool{}, critical: map[string]bool{},
	}
	if len(spans) == 0 {
		return b
	}
	byID := map[string]*Span{}
	for i := range spans {
		byID[spans[i].SpanID] = &spans[i]
	}
	for i := range spans {
		s := &spans[i]
		switch _, hasParent := byID[s.ParentID]; {
		case s.ParentID == "":
			b.roots = append(b.roots, s)
		case !hasParent:
			b.roots = append(b.roots, s)
			b.t.Orphans++
		default:
			b.kids[s.ParentID] = append(b.kids[s.ParentID], s)
		}
	}
	orderSpans(b.roots)
	first, last := spans[0].Start, spans[0].Start.Add(time.Duration(spans[0].DurationNs))
	for _, s := range spans {
		if s.Start.Before(first) {
			first = s.Start
		}
		if e := s.Start.Add(time.Duration(s.DurationNs)); e.After(last) {
			last = e
		}
	}
	b.first, b.t.Duration = first, last.Sub(first)
	return b
}

func orderSpans(ss []*Span) {
	sort.SliceStable(ss, func(i, j int) bool {
		if !ss[i].Start.Equal(ss[j].Start) {
			return ss[i].Start.Before(ss[j].Start)
		}
		return ss[i].SpanID < ss[j].SpanID
	})
}

// markFailing flags every span that failed or has a failing descendant, so an
// error stays reachable from the root.
func (b *treeBuilder) markFailing() {
	var mark func(s *Span) bool
	mark = func(s *Span) bool {
		f := isErrorSpan(s)
		for _, c := range b.kids[s.SpanID] {
			if mark(c) {
				f = true
			}
		}
		b.failing[s.SpanID] = f
		return f
	}
	for _, r := range b.roots {
		mark(r)
	}
}

// markCritical follows, from each root, the child that ends last.
func (b *treeBuilder) markCritical() {
	var walk func(s *Span)
	walk = func(s *Span) {
		b.critical[s.SpanID] = true
		var best *Span
		var bestEnd time.Time
		for _, c := range b.kids[s.SpanID] {
			if e := c.Start.Add(time.Duration(c.DurationNs)); best == nil || e.After(bestEnd) {
				best, bestEnd = c, e
			}
		}
		if best != nil {
			walk(best)
		}
	}
	for _, r := range b.roots {
		walk(r)
	}
}

// keep reports whether a span must be shown despite the caps.
func (b *treeBuilder) keep(s *Span) bool {
	return !b.full && (b.failing[s.SpanID] || b.critical[s.SpanID])
}

// split reports whether a span keeps its own node instead of folding into a
// run of identical siblings: a failing one, or a critical one the path
// continues through. A critical LEAF folds in and marks the run.
func (b *treeBuilder) split(s *Span) bool {
	return !b.full && (b.failing[s.SpanID] || (b.critical[s.SpanID] && len(b.kids[s.SpanID]) > 0))
}

// size counts the spans under s, itself included.
func (b *treeBuilder) size(s *Span) int {
	n := 1
	for _, c := range b.kids[s.SpanID] {
		n += b.size(c)
	}
	return n
}

// groupKids partitions a span's children into runs of identical siblings
// (same service, name and status); a span that must keep its own node is a
// run of one.
func (b *treeBuilder) groupKids(cs []*Span) [][]*Span {
	var groups [][]*Span
	index := map[string]int{}
	for _, c := range cs {
		if b.split(c) {
			groups = append(groups, []*Span{c})
			continue
		}
		k := c.Service + "\x00" + c.Name + "\x00" + c.StatusCode
		if gi, ok := index[k]; ok {
			groups[gi] = append(groups[gi], c)
			continue
		}
		index[k] = len(groups)
		groups = append(groups, []*Span{c})
	}
	return groups
}

func (b *treeBuilder) build(s *Span, depth int) *TreeNode {
	nd := &TreeNode{Span: *s, Count: 1, Error: isErrorSpan(s), Critical: b.critical[s.SpanID], Offset: s.Start.Sub(b.first)}
	b.budget--
	b.t.Shown++
	cs := b.kids[s.SpanID]
	orderSpans(cs)
	nd.Uninstrumented, nd.Gap, nd.Unread, nd.UnreadFrom = uninstrumented(s, cs, b.readUntil)
	if !b.full && depth >= MaxTreeDepth {
		for _, c := range cs {
			nd.Hidden += b.size(c)
		}
		b.t.Capped = b.t.Capped || nd.Hidden > 0
		return nd
	}
	for _, g := range b.groupKids(cs) {
		b.addGroup(nd, g, depth)
	}
	b.t.Capped = b.t.Capped || nd.Hidden > 0
	return nd
}

// addGroup renders one run of siblings under nd: each member on its own when
// it is a single span or the caps are lifted, a ×N node otherwise; or counts
// it hidden when the span budget is spent.
func (b *treeBuilder) addGroup(nd *TreeNode, g []*Span, depth int) {
	if b.budget <= 0 && !b.keep(g[0]) {
		for _, m := range g {
			nd.Hidden += b.size(m)
		}
		b.t.Capped = true
		return
	}
	if len(g) == 1 || b.full {
		for _, m := range g {
			c := b.build(m, depth+1)
			if len(g) == 1 && !b.full && !c.Error {
				// The read holds one of a run the trace has many of.
				c.WholeTrace = b.wholeRun(m, 1)
			}
			nd.Children = append(nd.Children, c)
		}
		return
	}
	run := collapse(g, b.first, &b.budget, &b.t.Shown)
	run.WholeTrace = b.wholeRun(g[0], len(g))
	for _, m := range g {
		run.Critical = run.Critical || b.critical[m.SpanID]
		// The run is shown as ×N; what is not shown is what hangs under its members.
		nd.Hidden += b.size(m) - 1
	}
	nd.Children = append(nd.Children, run)
}

// collapse folds a run of identical siblings into one node. Their descendants
// are not rendered (Hidden on the parent counts them): a run is "×37 SELECT
// jobs", its children would be 37 copies of the same thing.
func collapse(ms []*Span, first time.Time, budget, shown *int) *TreeNode {
	ds := make([]time.Duration, len(ms))
	for i, m := range ms {
		ds[i] = time.Duration(m.DurationNs)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	pick := func(q float64) time.Duration { return ds[min(int(q*float64(len(ds))), len(ds)-1)] }
	*budget--
	*shown++
	return &TreeNode{
		Span: *ms[0], Count: len(ms), Min: ds[0], Max: ds[len(ds)-1], P50: pick(0.5), P95: pick(0.95),
		Offset: ms[0].Start.Sub(first),
	}
}

// uninstrumented is the part of s no child covers, and its largest stretch,
// when [IsGap] says it matters. A leaf counts only when its kind says the work
// was its own: a client or producer span's time is spent by the peer it
// called, which is not a gap in this service's instrumentation.
//
// readUntil, when set, is where a truncated read stopped. The uncovered time
// after it is unread (its children may exist and were not fetched), returned
// as unread from unreadFrom when [IsGap] says it matters; only the time before
// it can be uninstrumented.
func uninstrumented(s *Span, kids []*Span, readUntil time.Time) (gap time.Duration, largest [2]time.Duration, unread, unreadFrom time.Duration) {
	dur := time.Duration(s.DurationNs)
	horizon := dur
	if !readUntil.IsZero() {
		horizon = min(max(readUntil.Sub(s.Start), 0), dur)
	}
	if len(kids) == 0 {
		switch strings.ToLower(s.Kind) {
		case "client", "producer", "":
			return 0, [2]time.Duration{}, 0, 0
		}
	}
	// kids are ordered by start; walk the union of their intervals, clipped
	// to the parent, splitting what they leave uncovered at the horizon.
	var cursor time.Duration
	uncovered := func(from, to time.Duration) {
		if from < horizon {
			end := min(to, horizon)
			gap += end - from
			if end-from > largest[1]-largest[0] {
				largest = [2]time.Duration{from, end}
			}
		}
		if to > horizon {
			start := max(from, horizon)
			if unread == 0 {
				unreadFrom = start
			}
			unread += to - start
		}
	}
	for _, k := range kids {
		// Clip the child's own start and end to the parent independently: a
		// child that starts before its parent (clock skew across services)
		// covers only the part of it that overlaps.
		start := k.Start.Sub(s.Start)
		from := min(max(start, 0), dur)
		to := min(max(start+time.Duration(k.DurationNs), from), dur)
		if from > cursor {
			uncovered(cursor, from)
		}
		cursor = max(cursor, to)
	}
	if cursor < dur {
		uncovered(cursor, dur)
	}
	if !IsGap(gap, dur) {
		gap, largest = 0, [2]time.Duration{}
	}
	if !IsGap(unread, dur) {
		unread, unreadFrom = 0, 0
	}
	return gap, largest, unread, unreadFrom
}

func isErrorSpan(s *Span) bool {
	if strings.EqualFold(strings.TrimPrefix(strings.ToUpper(s.StatusCode), "STATUS_CODE_"), "error") {
		return true
	}
	for _, e := range s.Events {
		if e.Name == "exception" {
			return true
		}
	}
	return false
}

// ErrorSpans are the failing spans of a trace, earliest first.
func ErrorSpans(spans []Span) []Span {
	var out []Span
	for _, s := range spans {
		if isErrorSpan(&s) {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}
