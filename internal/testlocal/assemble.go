package testlocal

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// Options configures [Assemble].
type Options struct {
	// ProjectRoot is the forge project's root (where forge.yaml and the
	// runs live).
	ProjectRoot string
	// Runs are the runs to report on. They must be on one source.
	Runs []*Run
	// AllRuns are every local run, where the base commit's report is looked
	// up. Nil skips the base lookup (no delta).
	AllRuns []*Run
	// Base is the revision diff coverage and the delta are measured
	// against. Empty: the runs' recorded base, else the merge base with the
	// default branch. NoDiff skips the diff and the base entirely.
	Base   string
	NoDiff bool
	// Lane, when set, keeps only that lane's results and coverage.
	Lane string
	// Git runs git; nil is [ExecGit].
	Git Git
	// Covdata converts a GOCOVERDIR; nil is [testreport.ConvertCovdata],
	// which runs the go on PATH (goBin is always "").
	Covdata func(ctx context.Context, goBin, dir string) ([]testreport.CovdataBuild, error)
}

// Assembly is what a set of runs adds up to: the inputs of a
// forge.test.report/v1 document, plus what the local reader learned on the
// way.
type Assembly struct {
	Commit string
	Tree   string
	Dirty  bool
	// Project is the runs' recorded project name.
	Project string
	// RepoRoot is the git toplevel ("" outside git); ModuleDir is the
	// project's directory inside it.
	RepoRoot string
	Runs     []testreport.RunRef
	// Lanes are the lanes read, in run order, aligned with Results (a lane
	// whose results could not be read has a nil entry).
	Lanes   []*Lane
	Results []*testreport.Results
	// Coverage is the merged coverage of every lane, nil when no lane
	// carried any. Files carry repo paths and blob ids where known.
	Coverage *testreport.Coverage
	BaseSHA  string
	Base     *testreport.Coverage
	// Changed are the diff's changed lines; nil when no diff was computed.
	Changed []testreport.ChangedFile
	// Undetermined are lane-level facts that could not be obtained: a lane
	// with no readable results, or a lane that exited non-zero with no
	// failure in its results. Any of them makes the report undetermined.
	Undetermined []string
	// Notes are things worth saying that change no verdict.
	Notes []string
}

// SourceError is returned when the runs to report on were taken on
// different source: they never merge.
type SourceError struct{ Detail string }

func (e *SourceError) Error() string {
	return "lanes come from different trees: " + e.Detail
}

// Assemble reads the runs' lanes, merges their coverage and computes the
// diff against the base. It refuses runs from different trees with a
// [*SourceError], and coverage that cannot merge with pkg/testreport's typed
// errors.
func Assemble(ctx context.Context, opts Options) (*Assembly, error) {
	if len(opts.Runs) == 0 {
		return nil, errors.New("no runs to report on")
	}
	if opts.Git == nil {
		opts.Git = ExecGit
	}
	if opts.Covdata == nil {
		opts.Covdata = testreport.ConvertCovdata
	}
	if err := sameSource(opts.Runs); err != nil {
		return nil, err
	}
	first := opts.Runs[0]
	a := &Assembly{Commit: first.Commit, Tree: first.TreeHash, Dirty: first.Dirty, Project: first.Project}
	for _, r := range opts.Runs {
		if a.Commit == "" {
			a.Commit = r.Commit
		}
		if a.Tree == "" {
			a.Tree = r.TreeHash
		}
		if a.Project == "" {
			a.Project = r.Project
		}
	}
	// The key every coverage document is stamped with: the runs are on one
	// source (checked above), so they must all merge.
	sourceKey := a.Tree
	if sourceKey == "" {
		sourceKey = a.Commit
	}

	repo := newRepoView(ctx, opts, a)
	a.RepoRoot = repo.top

	var docs []*testreport.Coverage
	for _, r := range opts.Runs {
		for i := range r.Lanes {
			l := &r.Lanes[i]
			if opts.Lane != "" && l.Lane != opts.Lane {
				continue
			}
			a.Runs = append(a.Runs, testreport.RunRef{ID: r.ID, Lane: testreport.Lane(l.Lane), Source: r.Source(), URL: r.URL(), ExitCode: l.ExitCode})
			a.Lanes = append(a.Lanes, l)
			res, why := readResults(opts.ProjectRoot, l)
			a.Results = append(a.Results, res)
			if why != "" {
				a.Undetermined = append(a.Undetermined, fmt.Sprintf("run %s lane %s: %s", r.ID, l.Lane, why))
			}
			ds, notes, err := readCoverage(ctx, opts, l, sourceKey, a)
			if err != nil {
				return nil, fmt.Errorf("run %s lane %s: %w", r.ID, l.Lane, err)
			}
			a.Notes = append(a.Notes, notes...)
			for _, d := range ds {
				repo.fixPaths(d)
			}
			docs = append(docs, ds...)
		}
	}
	if opts.Lane != "" && len(a.Runs) == 0 {
		return nil, fmt.Errorf("no lane %q in the runs", opts.Lane)
	}

	if len(docs) > 0 {
		cov, err := testreport.Merge(docs, testreport.MergeOptions{
			ModulePath: repo.modulePath, ModuleDir: repo.moduleDir,
		})
		if err != nil {
			return nil, err
		}
		repo.fillBlobs(cov)
		repo.markGenerated(cov)
		a.Coverage = cov
	}

	if !opts.NoDiff && repo.top != "" && a.Commit != "" {
		a.diffAndBase(ctx, opts, repo)
	}
	return a, nil
}

// sameSource refuses runs that were not taken on one source: clean runs of
// one commit (and one tree, when recorded), or dirty runs of one tree hash.
func sameSource(runs []*Run) error {
	r0 := runs[0]
	for _, r := range runs[1:] {
		switch {
		case r.Dirty != r0.Dirty:
			return &SourceError{Detail: fmt.Sprintf("run %s is on a dirty tree and run %s is not; a dirty run never joins a commit's report", dirtyOf(r0, r).ID, cleanOf(r0, r).ID)}
		case r.TreeHash != "" && r0.TreeHash != "" && r.TreeHash != r0.TreeHash:
			return &SourceError{Detail: fmt.Sprintf("run %s is on tree %s, run %s on tree %s", r0.ID, short(r0.TreeHash), r.ID, short(r.TreeHash))}
		case r0.Dirty && (r.TreeHash == "" || r0.TreeHash == ""):
			return &SourceError{Detail: fmt.Sprintf("dirty runs %s and %s have no recorded tree, so they cannot be shown to be the same source", r0.ID, r.ID)}
		case !r0.Dirty && r.Commit != r0.Commit:
			return &SourceError{Detail: fmt.Sprintf("run %s is on commit %s, run %s on %s", r0.ID, short(r0.Commit), r.ID, short(r.Commit))}
		}
	}
	return nil
}

func dirtyOf(a, b *Run) *Run {
	if a.Dirty {
		return a
	}
	return b
}

func cleanOf(a, b *Run) *Run {
	if a.Dirty {
		return b
	}
	return a
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "(unknown)"
	}
	return s
}

// readResults parses a lane's results. The second value says why the lane's
// facts could not be obtained, or "" when they were.
func readResults(projectRoot string, l *Lane) (*testreport.Results, string) {
	if l.Error != "" {
		return nil, "the command did not run: " + l.Error
	}
	p := l.ResultsPath()
	if p == "" {
		return nil, "no results file was captured"
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err.Error()
	}
	defer func() { _ = f.Close() }()
	format := testreport.Format(l.Format)
	if !format.IsResults() {
		format = ""
	}
	res, err := testreport.ParseResults(f, testreport.ResultsOptions{Format: format, Lane: testreport.Lane(l.Lane), Root: projectRoot})
	if err != nil {
		if errors.Is(err, testreport.ErrNoInput) {
			return nil, "the results file is empty"
		}
		return nil, "unreadable results: " + err.Error()
	}
	if l.ExitCode != 0 && res.Totals.Failed == 0 && res.Totals.PackagesFailed == 0 {
		return res, fmt.Sprintf("the command exited %d but its results record no failure", l.ExitCode)
	}
	return res, ""
}

// coverageFormat maps a capture.json coverage format to a parser format.
func coverageFormat(f string) testreport.Format {
	switch f {
	case testreport.CoverageGoProfile:
		return testreport.FormatGoCover
	case testreport.CoverageLCOV:
		return testreport.FormatLCOV
	case testreport.CoverageIstanbul:
		return testreport.FormatIstanbul
	case testreport.CoverageCobertura:
		return testreport.FormatCobertura
	}
	return ""
}

func readCoverage(ctx context.Context, opts Options, l *Lane, sourceKey string, a *Assembly) ([]*testreport.Coverage, []string, error) {
	var (
		docs  []*testreport.Coverage
		notes []string
	)
	base := testreport.CoverageOptions{Lane: testreport.Lane(l.Lane), Root: opts.ProjectRoot, Tree: sourceKey, Commit: a.Commit, Dirty: a.Dirty}
	for _, in := range l.CoverageInputs() {
		p := filepath.Join(l.Dir, filepath.FromSlash(strings.TrimSuffix(in.Path, "/")))
		if in.Format == testreport.CoverageGoCovdata {
			builds, err := opts.Covdata(ctx, "", p)
			if err != nil {
				notes = append(notes, fmt.Sprintf("lane %s: covdata in %s not converted: %v", l.Lane, in.Path, err))
				continue
			}
			for _, b := range builds {
				o := base
				o.Format = testreport.FormatGoCover
				o.BuildFingerprint = b.MetaHash
				d, err := testreport.ParseCoverage(strings.NewReader(string(b.Profile)), o)
				if errors.Is(err, testreport.ErrNoInput) {
					continue
				}
				if err != nil {
					return nil, nil, err
				}
				docs = append(docs, d)
			}
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			notes = append(notes, fmt.Sprintf("lane %s: coverage %s unreadable: %v", l.Lane, in.Path, err))
			continue
		}
		o := base
		o.Format = coverageFormat(in.Format)
		d, err := testreport.ParseCoverage(f, o)
		_ = f.Close()
		if errors.Is(err, testreport.ErrNoInput) {
			continue
		}
		if err != nil {
			var sm *testreport.SourceMismatchError
			if errors.As(err, &sm) {
				return nil, nil, err
			}
			notes = append(notes, fmt.Sprintf("lane %s: coverage %s not read: %v", l.Lane, in.Path, err))
			continue
		}
		docs = append(docs, d)
	}
	return docs, notes, nil
}

// repoView is what the reader knows about the repository the runs were taken
// in: its root, the project's place in it, the Go module, and the blob id of
// every file of the runs' source.
type repoView struct {
	top        string
	projectRel string // the project's directory relative to top, "" at the root
	modulePath string
	moduleDir  string
	blobs      map[string]string
	// blobsExact says blobs are the runs' own source (from the recorded
	// tree or commit), not the checkout as it is now.
	blobsExact bool
}

func newRepoView(ctx context.Context, opts Options, a *Assembly) *repoView {
	v := &repoView{}
	if data, err := os.ReadFile(filepath.Join(opts.ProjectRoot, "go.mod")); err == nil {
		v.modulePath = modfile.ModulePath(data)
	}
	top, err := Toplevel(ctx, opts.Git, opts.ProjectRoot)
	if err != nil {
		a.Notes = append(a.Notes, "not a git checkout: coverage files carry no repo path or blob id")
		return v
	}
	v.top = top
	if rel, err := filepath.Rel(evalPath(top), evalPath(opts.ProjectRoot)); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		v.projectRel = filepath.ToSlash(rel)
	}
	v.moduleDir = v.projectRel
	for _, treeish := range []string{a.Tree, a.Commit} {
		if treeish == "" || (treeish == a.Commit && a.Dirty) {
			continue
		}
		if m, err := BlobIDs(ctx, opts.Git, top, treeish); err == nil {
			v.blobs, v.blobsExact = m, true
			return v
		}
	}
	// The recorded tree is not in this repository's objects (a run copied
	// from elsewhere, or a dirty tree that was never written). The checkout
	// is the runs' source only if it is still at their commit.
	head, _ := ResolveRev(ctx, opts.Git, top, "HEAD")
	if a.Commit != "" && head == a.Commit {
		hash := func(rel string) (string, bool) {
			b, err := os.ReadFile(filepath.Join(top, filepath.FromSlash(rel)))
			if err != nil {
				return "", false
			}
			return testreport.GitBlobID(b), true
		}
		if m, err := IndexBlobIDs(ctx, opts.Git, top, hash); err == nil {
			v.blobs = m
			if a.Dirty {
				a.Notes = append(a.Notes, "the run's tree was not recorded in git; blob ids are of the checkout as it is now")
			}
			return v
		}
	}
	a.Notes = append(a.Notes, "the runs' source is not in this checkout: coverage files carry no blob id")
	return v
}

func evalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// fixPaths makes a parsed document's non-Go paths repo-relative. A Go
// profile names files by import path, which Merge maps with the module path.
// Other formats name them absolutely (made relative to the repository) or
// relative to whatever directory their tool ran in: the project root, or a
// frontend under it. A relative path that names no file of the source is
// matched by its unique suffix.
func (v *repoView) fixPaths(d *testreport.Coverage) {
	if v.top == "" {
		return
	}
	top := filepath.ToSlash(evalPath(v.top))
	for i := range d.Files {
		f := &d.Files[i]
		if f.ImportPath != "" || f.Path == "" {
			continue
		}
		p := filepath.ToSlash(f.Path)
		switch {
		case path.IsAbs(p) || filepath.IsAbs(f.Path):
			if rest, ok := strings.CutPrefix(filepath.ToSlash(evalPath(f.Path)), top+"/"); ok {
				p = rest
			} else if rest, ok := strings.CutPrefix(p, filepath.ToSlash(v.top)+"/"); ok {
				p = rest
			}
		default:
			p = path.Join(v.projectRel, p)
		}
		if v.blobs != nil {
			if _, ok := v.blobs[p]; !ok {
				if m := v.uniqueSuffix(f.Path); m != "" {
					p = m
				}
			}
		}
		if p != f.Path {
			f.Path = p
			f.Package = path.Dir(p)
		}
	}
}

func (v *repoView) uniqueSuffix(p string) string {
	suffix := "/" + strings.TrimPrefix(filepath.ToSlash(p), "./")
	var hit string
	for k := range v.blobs {
		if strings.HasSuffix(k, suffix) {
			if hit != "" {
				return ""
			}
			hit = k
		}
	}
	return hit
}

func (v *repoView) fillBlobs(c *testreport.Coverage) {
	if v.blobs == nil {
		return
	}
	for i := range c.Files {
		f := &c.Files[i]
		if f.BlobID == "" && f.Path != "" {
			f.BlobID = v.blobs[f.Path]
		}
	}
}

// generatedRe is the Go convention for a generated file's header
// (https://go.dev/s/generatedcode), which forge's own Tier-1 marker follows.
var generatedRe = regexp.MustCompile(`(?m)^(//|#|/\*|<!--)\s*Code generated .* DO NOT EDIT\.`)

// markGenerated excludes counted files whose header says they are
// generated. It reads only files whose content on disk is the exact blob the
// counts describe, so a header added or removed since the run is never
// trusted.
func (v *repoView) markGenerated(c *testreport.Coverage) {
	if v.top == "" {
		return
	}
	for i := range c.Files {
		f := &c.Files[i]
		if f.Excluded != "" || f.Path == "" || f.BlobID == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(v.top, filepath.FromSlash(f.Path)))
		if err != nil || testreport.GitBlobID(b) != f.BlobID {
			continue
		}
		if hasGeneratedHeader(b) {
			f.Excluded = testreport.ExcludedGenerated
		}
	}
}

func hasGeneratedHeader(b []byte) bool {
	sc := bufio.NewScanner(strings.NewReader(string(b[:min(len(b), 4<<10)])))
	for n := 0; sc.Scan() && n < 40; n++ {
		line := sc.Text()
		if generatedRe.MatchString(line) {
			return true
		}
		if strings.HasPrefix(strings.TrimSpace(line), "package ") {
			return false
		}
	}
	return false
}

func (a *Assembly) diffAndBase(ctx context.Context, opts Options, repo *repoView) {
	base := opts.Base
	if base == "" {
		for _, r := range opts.Runs {
			if r.BaseSHA != "" {
				base = r.BaseSHA
				break
			}
		}
	}
	var baseSHA string
	if base != "" {
		sha, err := ResolveRev(ctx, opts.Git, repo.top, base)
		if err != nil || sha == "" {
			a.Notes = append(a.Notes, fmt.Sprintf("base %s is not a commit here: no diff coverage", base))
			return
		}
		// A branch name means its merge base with the commit, which is
		// what `git diff base...head` compares against.
		if mb, err := MergeBase(ctx, opts.Git, repo.top, sha, a.Commit); err == nil && mb != "" {
			sha = mb
		}
		baseSHA = sha
	} else {
		def := DefaultBranch(ctx, opts.Git, repo.top)
		if def == "" {
			a.Notes = append(a.Notes, "no default branch found: no diff coverage (pass --base)")
			return
		}
		mb, err := MergeBase(ctx, opts.Git, repo.top, def, a.Commit)
		if err != nil || mb == "" {
			a.Notes = append(a.Notes, fmt.Sprintf("no merge base with %s: no diff coverage", def))
			return
		}
		baseSHA = mb
	}
	a.BaseSHA = baseSHA
	head := a.Commit
	if a.Dirty {
		head = "" // the worktree: the dirty changes are part of the diff
	}
	if baseSHA == a.Commit && !a.Dirty {
		a.Changed = []testreport.ChangedFile{}
	} else if changed, err := ChangedLines(ctx, opts.Git, repo.top, baseSHA, head); err != nil {
		a.Notes = append(a.Notes, "diff not computed: "+err.Error())
	} else {
		a.Changed = changed
	}
	if a.Coverage != nil {
		a.Coverage.Changed = a.Changed
	}
	if opts.AllRuns == nil || a.Coverage == nil {
		return
	}
	baseRuns := CommitRuns(opts.AllRuns, baseSHA)
	if len(baseRuns) == 0 {
		return
	}
	sub := opts
	sub.Runs, sub.AllRuns, sub.NoDiff = baseRuns, nil, true
	b, err := Assemble(ctx, sub)
	if err != nil {
		a.Notes = append(a.Notes, fmt.Sprintf("base %s has local runs that could not be read (%v): no delta", short(baseSHA), err))
		return
	}
	a.Base = b.Coverage
}

// Report is the forge.test.report/v1 document of the assembly. Its head is
// pkg/testreport's (1 on a failed test, 2 with no results), with this
// reader's undetermined lanes making an otherwise passing report 2.
func (a *Assembly) Report(project string) *testreport.Report {
	if project == "" {
		project = a.Project
	}
	in := testreport.ReportInput{
		Project: project, Commit: a.Commit, Tree: a.Tree, Dirty: a.Dirty,
		Runs: a.Runs, Coverage: a.Coverage, BaseSHA: a.BaseSHA, Base: a.Base, Changed: a.Changed,
	}
	for _, r := range a.Results {
		if r != nil {
			in.Results = append(in.Results, r)
		}
	}
	rep := testreport.NewReport(in)
	if rep.ExitCode == 0 && len(a.Undetermined) > 0 {
		rep.ExitCode = 2
		rep.Error = "undetermined: " + a.Undetermined[0]
		if n := len(a.Undetermined) - 1; n > 0 {
			rep.Error += fmt.Sprintf(" (and %d more)", n)
		}
	}
	rep.OK = rep.ExitCode == 0
	return rep
}

// LaneStreams are the go-json results files of the assembly's lanes, the
// streams verify-test-run's skip rules read.
func (a *Assembly) LaneStreams() []LaneStream {
	var out []LaneStream
	for _, l := range a.Lanes {
		if l.Format != testreport.LaneFormatGoJSON {
			continue
		}
		if p := l.ResultsPath(); p != "" {
			out = append(out, LaneStream{Lane: l.Lane, Path: p})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Lane < out[j].Lane })
	return out
}

// LaneStream is one lane's `go test -json` file.
type LaneStream struct {
	Lane string
	Path string
}

// Open opens the stream.
func (s LaneStream) Open() (io.ReadCloser, error) { return os.Open(s.Path) }
