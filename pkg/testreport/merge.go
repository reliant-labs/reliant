package testreport

import (
	"crypto/sha1" //nolint:gosec // git's blob id is defined as SHA-1; it names content, it secures nothing.
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

// SourceMismatchError is returned when two inputs (or two copies of a block
// in one input) give one block different statement counts, or one Go file
// different block sets: they were taken on different source. Coverage is
// never averaged across source versions; the merge is refused.
type SourceMismatchError struct {
	File  string
	Block string
	// Statements are the two disagreeing statement counts, when that is the
	// disagreement.
	Statements [2]int
	// Detail describes a block-set disagreement.
	Detail string
}

func (e *SourceMismatchError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("testreport: %s: inputs come from different source (%s)", e.File, e.Detail)
	}
	return fmt.Sprintf("testreport: %s:%s: inputs come from different source (block has %d statements in one, %d in another)", e.File, e.Block, e.Statements[0], e.Statements[1])
}

// BuildMismatchError is returned when two inputs carry different build
// fingerprints for one package ([Coverage.Builds]). It is opt-in: only a
// caller that records a true per-package source fingerprint sees it. A
// binary's covmeta hash is not one (two binaries from one tree differ), so
// it must not be passed as a fingerprint.
type BuildMismatchError struct {
	Package      string
	Fingerprints [2]string
}

func (e *BuildMismatchError) Error() string {
	return fmt.Sprintf("testreport: package %s comes from two builds (%s, %s): coverage is never merged across builds", e.Package, e.Fingerprints[0], e.Fingerprints[1])
}

// TreeMismatchError is returned when inputs were taken on different trees.
// A tree hash that is empty (unknown) does not merge with a known one either:
// the result could not say which tree it describes.
type TreeMismatchError struct {
	Trees [2]string
}

func (e *TreeMismatchError) Error() string {
	q := func(s string) string {
		if s == "" {
			return "(unknown)"
		}
		return s
	}
	return fmt.Sprintf("testreport: lanes come from different trees (%s, %s)", q(e.Trees[0]), q(e.Trees[1]))
}

// GranularityMismatchError is returned when one file is statement-granular in
// one input and line-granular in another.
type GranularityMismatchError struct {
	File string
}

func (e *GranularityMismatchError) Error() string {
	return fmt.Sprintf("testreport: %s: statement and line coverage cannot merge", e.File)
}

// OverlapError is returned when a document holds some inputs already merged
// and some not: its counts cannot be separated, so merging it would count
// the shared inputs twice.
type OverlapError struct {
	Digests []string
}

func (e *OverlapError) Error() string {
	return fmt.Sprintf("testreport: a document repeats inputs already merged (%s) beside new ones; merge the new inputs on their own", strings.Join(e.Digests, ", "))
}

func overlapping(ins []InputRef, seen map[string]bool) []string {
	var out []string
	for _, in := range ins {
		if seen[in.Digest] {
			out = append(out, in.Digest)
		}
	}
	return out
}

// MergeOptions configures [Merge].
type MergeOptions struct {
	// ModulePath and ModuleDir derive a Go file's repo-relative Path from its
	// import path: a file under ModulePath gets ModuleDir (the module's
	// directory in the repository, "" for the root) plus the rest. Files
	// that already have a Path keep it.
	ModulePath string
	ModuleDir  string
	// IncludeGenerated counts generated files toward the percentages.
	// Default false: gen/, *_gen.go (mock_gen.go included), *.pb.go,
	// *-hooks.ts and Generated are reported on their own line.
	IncludeGenerated bool
	// Generated are further generated files, as repo-relative paths (or
	// globs), e.g. the files forge's ownership record marks generated.
	Generated []string
	// Include, when set, counts only files matching one of these globs;
	// Exclude drops files matching any. Globs match the repo-relative path
	// (the import path when none is known); '**' matches any number of
	// directories. Dropped files are marked "filtered", not deleted.
	Include []string
	Exclude []string
	// Limits bounds the result (MaxBlocks, MaxCases).
	Limits Limits
}

// Merge unions coverage documents into one. Blocks are keyed by file and
// position; a block's hits are summed per lane. An input whose digest is
// already in an earlier document is skipped, so merging the same upload
// twice changes nothing.
//
// A file is one file however its inputs name it: a Go file named by import
// path (a Go profile) and by repo path (a path-named profile, LCOV) is
// joined once its repo path is known, whether from opts.ModulePath or from
// any input that carries both names, in any input order.
//
// It refuses, with a typed error and never a partial result, inputs that
// cannot describe the same source. Two checks carry that rule: the tree
// check ([*TreeMismatchError]: every input names the same tree hash) and
// per-file block-set identity ([*SourceMismatchError]: a Go file has the
// same blocks in every input, and a block the same statement count, because
// instrumenting one source file always yields one block set, so a shifted
// line is caught even without a tree hash). Neither depends on which binary
// counted the blocks, so two binaries built from one tree (an api and a
// worker sharing a package) merge. It also refuses one file at two
// granularities ([*GranularityMismatchError]), a document that repeats some
// inputs already merged ([*OverlapError]), and, for callers that supply a
// true per-package fingerprint in Builds, two fingerprints for one package
// ([*BuildMismatchError]).
//
// The result's files are marked excluded per opts; the inputs are not
// modified. A file an input already marks generated stays generated (unless
// opts.IncludeGenerated): that mark can come from what only the producer
// could read, the file's "Code generated … DO NOT EDIT." header, and a
// merger without the source (a control plane) must not count the file again.
func Merge(docs []*Coverage, opts MergeOptions) (*Coverage, error) {
	lim := opts.Limits.resolved()
	out := &Coverage{Head: Head{Schema: SchemaCoverage, OK: true}, Lanes: []LaneInfo{}, Inputs: []InputRef{}, Files: []CoverageFile{}}
	digests := map[string]bool{}
	var live []*Coverage
	first := true
	for _, d := range docs {
		if d == nil {
			continue
		}
		seen := 0
		for _, in := range d.Inputs {
			if digests[in.Digest] {
				seen++
			}
		}
		switch {
		case len(d.Inputs) > 0 && seen == len(d.Inputs):
			continue // already merged: idempotent
		case seen > 0:
			return nil, &OverlapError{Digests: overlapping(d.Inputs, digests)}
		}
		if first {
			out.Tree, out.Commit, out.Dirty = d.Tree, d.Commit, d.Dirty
			first = false
		} else if d.Tree != out.Tree {
			return nil, &TreeMismatchError{Trees: [2]string{out.Tree, d.Tree}}
		}
		if out.Commit == "" {
			out.Commit = d.Commit
		}
		out.Dirty = out.Dirty || d.Dirty
		for _, in := range d.Inputs {
			if !digests[in.Digest] {
				digests[in.Digest] = true
				out.Inputs = append(out.Inputs, in)
			}
		}
		for pkg, fp := range d.Builds {
			if have, ok := out.Builds[pkg]; ok && have != fp {
				return nil, &BuildMismatchError{Package: pkg, Fingerprints: [2]string{have, fp}}
			}
			if out.Builds == nil {
				out.Builds = map[string]string{}
			}
			out.Builds[pkg] = fp
		}
		live = append(live, d)
	}

	// The lanes, sorted; a lane any set input joined is a set lane.
	modes := map[Lane]Mode{}
	for _, d := range live {
		for _, l := range d.Lanes {
			if modes[l.Name] != ModeSet {
				modes[l.Name] = l.Mode
			}
		}
	}
	for name, m := range modes {
		if m == "" {
			m = ModeCount
		}
		out.Lanes = append(out.Lanes, LaneInfo{Name: name, Mode: m})
	}
	sort.Slice(out.Lanes, func(i, j int) bool { return out.Lanes[i].Name < out.Lanes[j].Name })
	laneIdx := map[Lane]int{}
	for i, l := range out.Lanes {
		laneIdx[l.Name] = i
	}

	agg := newFileAggregator(len(out.Lanes), lim)
	for _, d := range live {
		remap := make([]int, len(d.Lanes))
		for i, l := range d.Lanes {
			remap[i] = laneIdx[l.Name]
		}
		for _, f := range d.Files {
			f.Path = repoPath(f, opts)
			if err := agg.add(f, remap); err != nil {
				return nil, err
			}
		}
	}
	for _, mf := range agg.order {
		if mf.absorbed {
			continue
		}
		f := mf.file
		for i := range f.Blocks {
			for j, l := range out.Lanes {
				if l.Mode == ModeSet && f.Blocks[i].Hits[j] > 1 {
					f.Blocks[i].Hits[j] = 1
				}
			}
		}
		sortBlocks(f.Blocks)
		f.Excluded = exclusion(f, opts)
		if mf.generated && !opts.IncludeGenerated {
			f.Excluded = ExcludedGenerated
		}
		out.Files = append(out.Files, f)
	}
	sortFiles(out.Files)
	for _, d := range live {
		out.Changed = mergeChanged(out.Changed, d.Changed)
	}
	return out, nil
}

// mergedFile is one file's aggregate in a merge.
type mergedFile struct {
	file  CoverageFile
	index map[blockKey]int
	// absorbed is set when a later input showed this aggregate and another
	// to be one file: its blocks moved into that one.
	absorbed bool
	// generated is set when any input marked the file generated.
	generated bool
}

// fileAggregator collects files across inputs under one canonical identity
// each. A file may be named by its import path (a Go profile), by its repo
// path (an LCOV report, a profile of files outside the module path, or a
// producer that filled Path), or by both (a document an earlier Merge
// derived). Every name an aggregate has been seen under is an alias of it,
// so the same file arriving under either name, in any order, lands in one
// aggregate; an input naming two aggregates at once joins them.
type fileAggregator struct {
	lanes   int
	lim     Limits
	aliases map[string]*mergedFile
	order   []*mergedFile
	live    int
	blocks  int
}

func newFileAggregator(lanes int, lim Limits) *fileAggregator {
	return &fileAggregator{lanes: lanes, lim: lim, aliases: map[string]*mergedFile{}}
}

// fileAliases are the names a file is known by: its language plus its repo
// path, and its language plus its import path. The two are namespaced so a
// path can never collide with an import path.
func fileAliases(f CoverageFile) []string {
	var out []string
	if f.Path != "" {
		out = append(out, string(f.Lang)+":path:"+f.Path)
	}
	if f.ImportPath != "" {
		out = append(out, string(f.Lang)+":import:"+f.ImportPath)
	}
	return out
}

// add merges one input file, its lanes remapped to the output's.
func (a *fileAggregator) add(f CoverageFile, remap []int) error {
	names := fileAliases(f)
	var target *mergedFile
	for _, n := range names {
		mf := a.aliases[n]
		switch {
		case mf == nil:
		case target == nil:
			target = mf
		case mf != target:
			// This input names two aggregates: they are one file.
			if err := a.absorb(target, mf); err != nil {
				return err
			}
		}
	}
	if target == nil {
		if a.live >= a.lim.MaxCases {
			return &LimitError{Limit: "MaxCases", Max: int64(a.lim.MaxCases)}
		}
		nf := f
		nf.Blocks = nil
		nf.Excluded = ""
		target = &mergedFile{file: nf, index: map[blockKey]int{}}
		a.order = append(a.order, target)
		a.live++
	} else if err := a.check(target, f); err != nil {
		return err
	}
	a.adopt(target, f)
	if f.Excluded == ExcludedGenerated {
		target.generated = true
	}
	for _, n := range names {
		a.aliases[n] = target
	}
	for _, b := range f.Blocks {
		hits := make([]int64, a.lanes)
		for j := range hits {
			hits[j] = -1
		}
		for li, h := range b.Hits {
			if li < len(remap) && h >= 0 {
				hits[remap[li]] = h
			}
		}
		if err := a.addBlock(target, b, hits, f.DisplayPath()); err != nil {
			return err
		}
	}
	return nil
}

// check refuses an input file that cannot be the same source as the
// aggregate it resolved to.
func (a *fileAggregator) check(mf *mergedFile, f CoverageFile) error {
	if mf.file.Granularity != f.Granularity {
		return &GranularityMismatchError{File: displayOf(mf.file, f)}
	}
	if f.Lang == LangGo && f.Granularity == GranularityStatement && len(mf.file.Blocks) > 0 && len(f.Blocks) > 0 {
		return sameBlockSet(mf.file, f)
	}
	return nil
}

// adopt fills the aggregate's identity fields from an input that knows more.
func (a *fileAggregator) adopt(mf *mergedFile, f CoverageFile) {
	if mf.file.Path == "" {
		mf.file.Path = f.Path
	}
	if mf.file.ImportPath == "" && f.ImportPath != "" {
		mf.file.ImportPath = f.ImportPath
		mf.file.Package = f.Package
	}
	if mf.file.BlobID == "" {
		mf.file.BlobID = f.BlobID
	}
}

// absorb moves src's blocks into dst and points src's aliases at dst.
func (a *fileAggregator) absorb(dst, src *mergedFile) error {
	if err := a.check(dst, src.file); err != nil {
		return err
	}
	a.adopt(dst, src.file)
	dst.generated = dst.generated || src.generated
	for _, b := range src.file.Blocks {
		a.blocks-- // addBlock counts it again if it is new to dst
		if err := a.addBlock(dst, b, b.Hits, src.file.DisplayPath()); err != nil {
			return err
		}
	}
	for n, mf := range a.aliases {
		if mf == src {
			a.aliases[n] = dst
		}
	}
	src.absorbed = true
	src.file.Blocks, src.index = nil, nil
	a.live--
	return nil
}

// addBlock sums one block's per-lane hits (output lane space; -1 = the lane
// did not instrument it) into the aggregate.
func (a *fileAggregator) addBlock(mf *mergedFile, b Block, hits []int64, name string) error {
	k := b.key()
	i, ok := mf.index[k]
	if !ok {
		if a.blocks >= a.lim.MaxBlocks {
			return &LimitError{Limit: "MaxBlocks", Max: int64(a.lim.MaxBlocks)}
		}
		a.blocks++
		nb := b
		nb.Hits = make([]int64, a.lanes)
		for j := range nb.Hits {
			nb.Hits[j] = -1
		}
		i = len(mf.file.Blocks)
		mf.index[k] = i
		mf.file.Blocks = append(mf.file.Blocks, nb)
	}
	have := &mf.file.Blocks[i]
	if have.Statements != b.Statements {
		return &SourceMismatchError{File: name, Block: blockPos(b), Statements: [2]int{have.Statements, b.Statements}}
	}
	for j, h := range hits {
		if j >= len(have.Hits) || h < 0 {
			continue
		}
		if have.Hits[j] < 0 {
			have.Hits[j] = 0
		}
		have.Hits[j] = addHits(have.Hits[j], h)
	}
	return nil
}

func displayOf(a, b CoverageFile) string {
	if a.Path != "" {
		return a.Path
	}
	return b.DisplayPath()
}

// repoPath is a file's repo-relative path: its own, or for a Go file named
// by import path under opts.ModulePath, ModuleDir plus the rest.
func repoPath(f CoverageFile, opts MergeOptions) string {
	if f.Path != "" || f.ImportPath == "" || opts.ModulePath == "" {
		return f.Path
	}
	if rest, ok := strings.CutPrefix(f.ImportPath, opts.ModulePath+"/"); ok {
		return path.Join(opts.ModuleDir, rest)
	}
	return ""
}

// sameBlockSet checks that a Go file has the same blocks in an input as in
// the merge so far. Instrumenting one source file always yields one block
// set, whichever binary or lane built it, so a difference is proof of
// different source (a shifted line moves every block after it).
func sameBlockSet(have, f CoverageFile) error {
	if len(have.Blocks) != len(f.Blocks) {
		return &SourceMismatchError{File: f.DisplayPath(), Detail: fmt.Sprintf("%d blocks in one, %d in another", len(have.Blocks), len(f.Blocks))}
	}
	seen := make(map[blockKey]bool, len(have.Blocks))
	for _, b := range have.Blocks {
		seen[b.key()] = true
	}
	for _, b := range f.Blocks {
		if !seen[b.key()] {
			return &SourceMismatchError{File: f.DisplayPath(), Block: blockPos(b), Detail: "block " + blockPos(b) + " is in one input only"}
		}
	}
	return nil
}

func mergeChanged(a, b []ChangedFile) []ChangedFile {
	if len(b) == 0 {
		return a
	}
	idx := map[string]int{}
	for i, c := range a {
		idx[c.Path] = i
	}
	for _, c := range b {
		if i, ok := idx[c.Path]; ok {
			a[i].Ranges = append(a[i].Ranges, c.Ranges...)
			continue
		}
		idx[c.Path] = len(a)
		a = append(a, ChangedFile{Path: c.Path, Ranges: append([]LineRange(nil), c.Ranges...)})
	}
	return a
}

// exclusion says why a file does not count, or "".
func exclusion(f CoverageFile, opts MergeOptions) string {
	p := f.DisplayPath()
	if !opts.IncludeGenerated && (IsGenerated(p) || matchAny(opts.Generated, p)) {
		return ExcludedGenerated
	}
	if len(opts.Include) > 0 && !matchAny(opts.Include, p) {
		return ExcludedFiltered
	}
	if matchAny(opts.Exclude, p) {
		return ExcludedFiltered
	}
	return ""
}

// IsGenerated reports whether a path is generated by forge's conventions: a
// directory named gen, *_gen.go (mock_gen.go included), *.pb.go or
// *-hooks.ts.
func IsGenerated(p string) bool {
	p = strings.ReplaceAll(p, `\`, "/")
	base := path.Base(p)
	if strings.HasSuffix(base, "_gen.go") || strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, "-hooks.ts") {
		return true
	}
	dir := path.Dir(p)
	for _, seg := range strings.Split(dir, "/") {
		if seg == "gen" {
			return true
		}
	}
	return false
}

func matchAny(globs []string, p string) bool {
	for _, g := range globs {
		if MatchGlob(g, p) {
			return true
		}
	}
	return false
}

// MatchGlob matches a slash-separated path against a glob: each segment by
// [path.Match], and '**' as any number of segments (zero included). A glob
// without a '/' matches the base name anywhere ("*_test.go").
func MatchGlob(glob, p string) bool {
	glob = strings.TrimPrefix(glob, "./")
	p = strings.TrimPrefix(strings.ReplaceAll(p, `\`, "/"), "./")
	if !strings.Contains(glob, "/") {
		ok, _ := path.Match(glob, path.Base(p))
		if ok {
			return true
		}
	}
	return matchSegs(strings.Split(glob, "/"), strings.Split(p, "/"))
}

func matchSegs(g, p []string) bool {
	for len(g) > 0 {
		if g[0] == "**" {
			g = g[1:]
			if len(g) == 0 {
				return true
			}
			for i := 0; i <= len(p); i++ {
				if matchSegs(g, p[i:]) {
					return true
				}
			}
			return false
		}
		if len(p) == 0 {
			return false
		}
		if ok, _ := path.Match(g[0], p[0]); !ok {
			return false
		}
		g, p = g[1:], p[1:]
	}
	return len(p) == 0
}

func sortBlocks(bs []Block) {
	sort.Slice(bs, func(i, j int) bool {
		a, b := bs[i], bs[j]
		switch {
		case a.StartLine != b.StartLine:
			return a.StartLine < b.StartLine
		case a.StartCol != b.StartCol:
			return a.StartCol < b.StartCol
		case a.EndLine != b.EndLine:
			return a.EndLine < b.EndLine
		}
		return a.EndCol < b.EndCol
	})
}

func sortFiles(fs []CoverageFile) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Lang != fs[j].Lang {
			return fs[i].Lang < fs[j].Lang
		}
		return fs[i].Key() < fs[j].Key()
	})
}

func gitBlobID(content []byte) string {
	h := sha1.New() //nolint:gosec // see the import.
	h.Write([]byte("blob " + strconv.Itoa(len(content)) + "\x00"))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}
