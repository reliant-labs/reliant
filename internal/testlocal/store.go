package testlocal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// The local test record is what `forge test begin` and `forge test capture`
// write under the project (pkg/testreport's manifest.go names the layout).
// This reader is tolerant on purpose: a run whose run.json is missing, a lane
// whose capture.json lists no coverage, a lane directory a newer forge added
// files to. It reads what is there and says what was not.

// Run is one run directory: its run.json and the lanes captured in it.
type Run struct {
	testreport.RunManifest
	// Dir is the run directory's absolute path.
	Dir string
	// Lanes are the lanes with a readable capture.json, sorted by name.
	Lanes []Lane
	// Problems say what the reader could not read.
	Problems []string
}

// Source is where the run happened: "ci" with a CI identity, else "local".
func (r *Run) Source() string {
	if r.CI != nil && r.CI.Provider != "" {
		return "ci"
	}
	return "local"
}

// URL is the CI run's URL, when there is one.
func (r *Run) URL() string {
	if r.CI == nil {
		return ""
	}
	return r.CI.RunURL
}

// Lane is one lane directory's capture.json.
type Lane struct {
	testreport.LaneManifest
	// Dir is the lane directory's absolute path.
	Dir string
}

// ResultsPath is the lane's results file, or "" when it has none.
func (l *Lane) ResultsPath() string {
	name := l.ResultsFile
	if name == "" {
		if l.Results == testreport.LaneResultsMissing {
			return ""
		}
		name = testreport.LaneResultsFile
	}
	p := filepath.Join(l.Dir, filepath.FromSlash(name))
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return ""
	}
	return p
}

// CoverageInputs are the lane's coverage inputs: the ones capture.json
// lists, else the conventional names found in the lane directory.
func (l *Lane) CoverageInputs() []testreport.LaneCoverageInput {
	if len(l.Coverage) > 0 {
		return l.Coverage
	}
	var out []testreport.LaneCoverageInput
	probe := []testreport.LaneCoverageInput{
		{Path: testreport.LaneCoverProfile, Format: testreport.CoverageGoProfile},
		{Path: testreport.LaneCoverDataDir + "/", Format: testreport.CoverageGoCovdata},
		{Path: "lcov.info", Format: testreport.CoverageLCOV},
		{Path: "coverage-final.json", Format: testreport.CoverageIstanbul},
		{Path: "cobertura-coverage.xml", Format: testreport.CoverageCobertura},
	}
	for _, c := range probe {
		fi, err := os.Stat(filepath.Join(l.Dir, filepath.FromSlash(strings.TrimSuffix(c.Path, "/"))))
		if err != nil || fi.IsDir() != strings.HasSuffix(c.Path, "/") {
			continue
		}
		if !fi.IsDir() && fi.Size() == 0 {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ErrNotRun is returned for a directory that holds no run.json and no lane.
var ErrNotRun = errors.New("not a test run directory")

// Open reads one run directory.
func Open(dir string) (*Run, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	r := &Run{Dir: abs}
	m, err := testreport.ReadRunManifest(abs)
	switch {
	case err == nil:
		r.RunManifest = m
	case errors.Is(err, fs.ErrNotExist):
		r.Problems = append(r.Problems, "no run.json: commit, tree and CI identity are unknown")
	default:
		return nil, err
	}
	if r.ID == "" {
		r.ID = filepath.Base(abs)
	}
	lanes, err := testreport.CapturedLanes(abs)
	if err != nil {
		return nil, err
	}
	for _, name := range lanes {
		lm, err := testreport.ReadLaneManifest(abs, name)
		if err != nil {
			r.Problems = append(r.Problems, fmt.Sprintf("lane %s: %v", name, err))
			continue
		}
		if lm.Lane == "" {
			lm.Lane = name
		}
		r.Lanes = append(r.Lanes, Lane{LaneManifest: lm, Dir: testreport.LaneDir(abs, name)})
	}
	if len(r.Lanes) == 0 && r.Schema == "" {
		return nil, fmt.Errorf("%s: %w", abs, ErrNotRun)
	}
	if r.CreatedAt.IsZero() {
		if fi, err := os.Stat(abs); err == nil {
			r.CreatedAt = fi.ModTime().UTC()
		}
	}
	return r, nil
}

// List reads every run under projectRoot, newest first. A directory that
// cannot be read is skipped and named in the returned problems, never fatal.
func List(projectRoot string) ([]*Run, []string, error) {
	root := testreport.RunsRoot(projectRoot)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var (
		runs     []*Run
		problems []string
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := Open(filepath.Join(root, e.Name()))
		if err != nil {
			problems = append(problems, fmt.Sprintf("run %s: %v", e.Name(), err))
			continue
		}
		runs = append(runs, r)
	}
	SortNewestFirst(runs)
	return runs, problems, nil
}

// SortNewestFirst orders runs by creation time, newest first, then by id
// (ULIDs sort by time too).
func SortNewestFirst(runs []*Run) {
	sort.SliceStable(runs, func(i, j int) bool {
		if !runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].CreatedAt.After(runs[j].CreatedAt)
		}
		return runs[i].ID > runs[j].ID
	})
}

// Resolve finds a run by directory path, by id, or by a unique id prefix.
func Resolve(projectRoot, ref string, runs []*Run) (*Run, error) {
	if fi, err := os.Stat(ref); err == nil && fi.IsDir() && strings.ContainsAny(ref, `/\`) {
		return Open(ref)
	}
	var match []*Run
	for _, r := range runs {
		if r.ID == ref {
			return r, nil
		}
		if strings.HasPrefix(strings.ToLower(r.ID), strings.ToLower(ref)) {
			match = append(match, r)
		}
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		return nil, fmt.Errorf("no run %q under %s", ref, testreport.RunsRoot(projectRoot))
	}
	return nil, fmt.Errorf("run %q is ambiguous: %d runs start with it", ref, len(match))
}

// MatchCommit reports whether a run's commit is sha or starts with it (at
// least 4 characters).
func MatchCommit(r *Run, sha string) bool {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if r.Commit == "" || len(sha) < 4 {
		return false
	}
	return strings.HasPrefix(strings.ToLower(r.Commit), sha)
}

// CommitRuns are the clean-tree runs of a commit, newest first: what a
// commit's report merges. Dirty runs never join a commit's report.
func CommitRuns(runs []*Run, sha string) []*Run {
	var out []*Run
	for _, r := range runs {
		if !r.Dirty && MatchCommit(r, sha) {
			out = append(out, r)
		}
	}
	return out
}

// TreeRuns are the runs on the same source as r: the clean runs of its
// commit when r is clean, else the runs on r's dirty tree hash (r alone when
// its tree is unknown).
func TreeRuns(runs []*Run, r *Run) []*Run {
	if !r.Dirty && r.Commit != "" {
		if out := CommitRuns(runs, r.Commit); len(out) > 0 {
			return out
		}
		return []*Run{r}
	}
	if r.TreeHash == "" {
		return []*Run{r}
	}
	var out []*Run
	for _, o := range runs {
		if o.Dirty && o.TreeHash == r.TreeHash {
			out = append(out, o)
		}
	}
	if len(out) == 0 {
		out = []*Run{r}
	}
	return out
}
