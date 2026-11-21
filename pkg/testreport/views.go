package testreport

import (
	"math"
	"path"
	"sort"
	"strings"
)

// Tally is covered over instrumented statements.
type Tally struct {
	Statements int `json:"statements"`
	Covered    int `json:"covered"`
	// Pct is 100 * Covered / Statements, rounded to one decimal; 0 when
	// there are no statements.
	Pct float64 `json:"pct"`
}

func (t *Tally) add(stmts int, covered bool) {
	t.Statements += stmts
	if covered {
		t.Covered += stmts
	}
}

func (t Tally) plus(o Tally) Tally {
	return Tally{Statements: t.Statements + o.Statements, Covered: t.Covered + o.Covered}
}

func (t Tally) rounded() Tally {
	t.Pct = pct(t.Covered, t.Statements)
	return t
}

func (t Tally) ratio() float64 {
	if t.Statements == 0 {
		return 0
	}
	return float64(t.Covered) / float64(t.Statements)
}

func pct(covered, total int) float64 {
	if total == 0 {
		return 0
	}
	return round1(100 * float64(covered) / float64(total))
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// CoverageSummary is a document's figures: per lane, combined, only-e2e, per
// language, and what was excluded.
type CoverageSummary struct {
	// Lanes is each lane's tally over the statements that lane instrumented,
	// across every counted language.
	Lanes map[Lane]Tally `json:"lanes"`
	// Combined counts a statement covered if any lane covered it.
	Combined Tally `json:"combined"`
	// CombinedLabel is set when Combined adds more than one language:
	// "weighted by statements across go, ts".
	CombinedLabel string `json:"combined_label,omitempty"`
	// OnlyE2E counts the statements an e2e lane covered and no other lane
	// did, over every counted statement: what is tested only by prod-like
	// traffic.
	OnlyE2E Tally `json:"only_e2e"`
	// Languages holds the same figures per language. Go and TypeScript are
	// never added together anywhere but Combined.
	Languages []LangSummary `json:"languages"`
	// Generated and Filtered are the excluded files' statements, reported on
	// their own line; their Covered counts what any lane reached.
	Generated Tally `json:"generated"`
	Filtered  Tally `json:"filtered"`
}

// LangSummary is one language's figures.
type LangSummary struct {
	Lang        Lang           `json:"lang"`
	Granularity Granularity    `json:"granularity"`
	Lanes       map[Lane]Tally `json:"lanes"`
	Combined    Tally          `json:"combined"`
	OnlyE2E     Tally          `json:"only_e2e"`
}

// tallies is the accumulation behind every view.
type tallies struct {
	lanes    []Tally
	combined Tally
	onlyE2E  Tally
}

func newTallies(n int) tallies { return tallies{lanes: make([]Tally, n)} }

// addBlock folds one block in: a lane tallies it only if it instrumented it
// (Hits >= 0).
func (t *tallies) addBlock(c *Coverage, b Block) {
	covered, e2e, other := false, false, false
	for i, h := range b.Hits {
		if i >= len(t.lanes) || h < 0 {
			continue
		}
		t.lanes[i].add(b.Statements, h > 0)
		if h > 0 {
			covered = true
			if c.Lanes[i].Name.IsE2E() {
				e2e = true
			} else {
				other = true
			}
		}
	}
	t.combined.add(b.Statements, covered)
	t.onlyE2E.add(b.Statements, e2e && !other)
}

func (t tallies) laneMap(c *Coverage) map[Lane]Tally {
	m := make(map[Lane]Tally, len(t.lanes))
	for i, l := range c.Lanes {
		if t.lanes[i].Statements > 0 {
			m[l.Name] = t.lanes[i].rounded()
		}
	}
	return m
}

// Summary computes the document's figures. Excluded files count only toward
// Generated and Filtered.
func (c *Coverage) Summary() CoverageSummary {
	all := newTallies(len(c.Lanes))
	per := map[Lang]*tallies{}
	gran := map[Lang]Granularity{}
	var s CoverageSummary
	for _, f := range c.Files {
		if f.Excluded != "" {
			var t *Tally
			if f.Excluded == ExcludedGenerated {
				t = &s.Generated
			} else {
				t = &s.Filtered
			}
			for _, b := range f.Blocks {
				t.add(b.Statements, b.Covered())
			}
			continue
		}
		lt := per[f.Lang]
		if lt == nil {
			v := newTallies(len(c.Lanes))
			lt = &v
			per[f.Lang] = lt
		}
		if gran[f.Lang] != GranularityLine {
			gran[f.Lang] = f.Granularity
		}
		for _, b := range f.Blocks {
			all.addBlock(c, b)
			lt.addBlock(c, b)
		}
	}
	s.Lanes = all.laneMap(c)
	s.Combined = all.combined.rounded()
	s.OnlyE2E = all.onlyE2E.rounded()
	s.Generated = s.Generated.rounded()
	s.Filtered = s.Filtered.rounded()
	var langs []string
	for l := range per {
		langs = append(langs, string(l))
	}
	sort.Strings(langs)
	for _, l := range langs {
		t := per[Lang(l)]
		s.Languages = append(s.Languages, LangSummary{
			Lang: Lang(l), Granularity: gran[Lang(l)], Lanes: t.laneMap(c),
			Combined: t.combined.rounded(), OnlyE2E: t.onlyE2E.rounded(),
		})
	}
	if s.Languages == nil {
		s.Languages = []LangSummary{}
	}
	if len(langs) > 1 {
		s.CombinedLabel = "weighted by statements across " + strings.Join(langs, ", ")
	}
	return s
}

// RollupBy is the grouping of [Coverage.Rollup].
type RollupBy int

// The two groupings.
const (
	// ByPackage groups by Go package (the repo-relative directory when the
	// path is known, else the import path) or, for other languages, by
	// directory.
	ByPackage RollupBy = iota
	// ByFile is one row per file.
	ByFile
)

// Rollup is one package's or file's figures.
type Rollup struct {
	Lang Lang `json:"lang"`
	// Path is the repo-relative directory or file, or the Go import path
	// when the repo path is unknown.
	Path     string         `json:"path"`
	Combined Tally          `json:"combined"`
	Lanes    map[Lane]Tally `json:"lanes"`
	OnlyE2E  Tally          `json:"only_e2e"`
}

// Rollup computes per-package or per-file figures over the counted (not
// excluded) files, sorted by language then path.
func (c *Coverage) Rollup(by RollupBy) []Rollup {
	type acc struct {
		lang Lang
		path string
		t    tallies
	}
	groups := map[string]*acc{}
	for _, f := range c.Files {
		if f.Excluded != "" {
			continue
		}
		p := f.DisplayPath()
		if by == ByPackage {
			if f.Path != "" || f.Lang != LangGo {
				p = path.Dir(f.DisplayPath())
			} else {
				p = f.Package
			}
		}
		k := string(f.Lang) + ":" + p
		a := groups[k]
		if a == nil {
			a = &acc{lang: f.Lang, path: p, t: newTallies(len(c.Lanes))}
			groups[k] = a
		}
		for _, b := range f.Blocks {
			a.t.addBlock(c, b)
		}
	}
	out := make([]Rollup, 0, len(groups))
	for _, a := range groups {
		out = append(out, Rollup{Lang: a.lang, Path: a.path, Combined: a.t.combined.rounded(), Lanes: a.t.laneMap(c), OnlyE2E: a.t.onlyE2E.rounded()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Lang != out[j].Lang {
			return out[i].Lang < out[j].Lang
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// DiffSummary is coverage of the executable lines a diff changed.
type DiffSummary struct {
	// Lines is the changed lines that are executable (inside some block of
	// a counted file); Covered is those every spanning block covers. A line
	// only partly covered (one block hit, another not) counts as not
	// covered.
	Lines   int        `json:"lines"`
	Covered int        `json:"covered"`
	Pct     float64    `json:"pct"`
	Files   []DiffFile `json:"files,omitempty"`
	// Truncated says the computation stopped at its work bound (a hostile
	// document); the figures are partial.
	Truncated bool `json:"truncated,omitempty"`
}

// DiffFile is one changed file's diff coverage.
type DiffFile struct {
	Path    string `json:"path"`
	Lines   int    `json:"lines"`
	Covered int    `json:"covered"`
	// Uncovered are the changed executable lines no lane covered, merged
	// into ranges.
	Uncovered []LineRange `json:"uncovered,omitempty"`
}

// maxLine bounds the line numbers diff coverage walks, so a hostile block or
// range cannot make it iterate without bound. No source file is this long.
const maxLine = 1_000_000

// Diff computes diff coverage over changed line ranges, matched to files by
// repo-relative path. A changed line outside every block is not executable
// and does not count; nor do lines of excluded files.
func (c *Coverage) Diff(changed []ChangedFile) DiffSummary {
	byPath := map[string]*CoverageFile{}
	for i := range c.Files {
		if c.Files[i].Path != "" && c.Files[i].Excluded == "" {
			byPath[c.Files[i].Path] = &c.Files[i]
		}
	}
	var s DiffSummary
	budget := diffWorkBudget
	for _, ch := range changed {
		f := byPath[strings.TrimPrefix(ch.Path, "./")]
		if f == nil {
			continue
		}
		want := changedLines(ch.Ranges)
		// line -> covered by every spanning block so far
		lines := map[int]bool{}
		for _, b := range f.Blocks {
			bs, be := clampLine(b.StartLine), clampLine(b.EndLine)
			cov := b.Covered()
			for i := sort.SearchInts(want, bs); i < len(want) && want[i] <= be; i++ {
				if budget--; budget < 0 {
					s.Truncated = true
					break
				}
				l := want[i]
				prev, seen := lines[l]
				lines[l] = cov && (!seen || prev)
			}
		}
		if len(lines) == 0 {
			continue
		}
		df := DiffFile{Path: f.Path, Lines: len(lines)}
		var unc []int
		for l, cov := range lines {
			if cov {
				df.Covered++
			} else {
				unc = append(unc, l)
			}
		}
		sort.Ints(unc)
		df.Uncovered = toRanges(unc)
		s.Lines += df.Lines
		s.Covered += df.Covered
		s.Files = append(s.Files, df)
	}
	sort.Slice(s.Files, func(i, j int) bool { return s.Files[i].Path < s.Files[j].Path })
	s.Pct = pct(s.Covered, s.Lines)
	return s
}

// diffWorkBudget bounds the (block, changed line) pairs Diff visits. Real
// blocks are a few lines long, so real diffs never come near it; a hostile
// document of huge overlapping blocks stops here with Truncated set.
const diffWorkBudget = 50_000_000

func clampLine(l int) int { return min(max(l, 0), maxLine) }

// changedLines is the sorted, de-duplicated set of lines the ranges name,
// clamped to [1, maxLine].
func changedLines(ranges []LineRange) []int {
	rs := make([]LineRange, 0, len(ranges))
	for _, r := range ranges {
		lo, hi := max(clampLine(r.Start), 1), clampLine(r.End)
		if lo <= hi {
			rs = append(rs, LineRange{Start: lo, End: hi})
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Start < rs[j].Start })
	var out []int
	next := 1
	for _, r := range rs {
		for l := max(r.Start, next); l <= r.End; l++ {
			out = append(out, l)
		}
		next = max(next, r.End+1)
	}
	return out
}

func toRanges(sorted []int) []LineRange {
	var out []LineRange
	for _, l := range sorted {
		if n := len(out); n > 0 && out[n-1].End == l-1 {
			out[n-1].End = l
			continue
		}
		out = append(out, LineRange{Start: l, End: l})
	}
	return out
}

// DeltaSummary is the change in combined coverage from a base report.
type DeltaSummary struct {
	// Available is false when there is no base report; Reason then says
	// so. A delta is never computed against some older report instead.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Value is head's combined percent minus base's, in points, rounded to
	// one decimal.
	Value float64 `json:"value"`
	Head  Tally   `json:"head"`
	Base  Tally   `json:"base"`
}

// Delta compares head with base. base nil means the base commit has no report.
func Delta(head, base *Coverage) DeltaSummary {
	if base == nil {
		return DeltaSummary{Reason: "no report for base"}
	}
	h, b := head.Summary().Combined, base.Summary().Combined
	return DeltaSummary{Available: true, Value: round1(100 * (h.ratio() - b.ratio())), Head: h, Base: b}
}
