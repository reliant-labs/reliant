package releasetimeline

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// CommitLister lists the commits a recorded range names, from a local
// checkout. A ledger holds a bounded sample (release.MaxRangeCommits); git
// holds the rest.
type CommitLister interface {
	// Commits returns the commits reachable from `to` and not from `from`,
	// newest first, at most max. An empty `from` lists the history of `to`.
	Commits(ctx context.Context, from, to string, max int) ([]release.Commit, error)
}

// gitTimeout bounds one git run, so a hung checkout cannot hang a verb.
const gitTimeout = 10 * time.Second

// NewGit lists commits with the git checkout around dir.
func NewGit(dir string) Git { return Git{Dir: dir} }

// Git lists commits with the git in Dir.
type Git struct {
	Dir string
}

// Commits runs `git log from..to`. A commit this checkout lacks (a shallow
// clone, another branch) is an error the caller reports; it never invents a
// list.
func (g Git) Commits(ctx context.Context, from, to string, max int) ([]release.Commit, error) {
	for _, s := range []string{from, to} {
		if s != "" && !release.ValidObjectID(s) {
			return nil, fmt.Errorf("%q is not a full git object id", s)
		}
	}
	if !release.ValidObjectID(to) {
		return nil, fmt.Errorf("a commit range needs its end commit")
	}
	if max <= 0 {
		max = release.MaxRangeCommits
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	spec := to
	if from != "" {
		spec = from + ".." + to
	}
	cmd := exec.CommandContext(ctx, "git", "log", "--max-count="+strconv.Itoa(max),
		"--format=%H%x1f%ct%x1f%an%x1f%s%x1e", spec)
	cmd.Dir = g.Dir
	raw, err := cmd.Output()
	if err != nil {
		msg := ""
		if ee, ok := err.(*exec.ExitError); ok {
			msg = ": " + strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("git log %s: %w%s", spec, err, msg)
	}
	return parseLog(string(raw))
}

func parseLog(raw string) ([]release.Commit, error) {
	out := []release.Commit{}
	for _, rec := range strings.Split(raw, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\x1f", 4)
		if len(f) != 4 {
			return nil, fmt.Errorf("unexpected git log record %q", rec)
		}
		secs, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected commit time %q for %s", f[1], f[0])
		}
		out = append(out, release.Commit{
			SHA: f[0], Time: time.Unix(secs, 0).UTC(),
			Author:  release.CommitText(f[2], release.MaxCommitAuthorBytes),
			Subject: release.CommitText(f[3], release.MaxCommitSubjectBytes),
		})
	}
	return out, nil
}

// CommitsOf lists the commits behind an entry's range, newest first, with the
// pull requests they merged. For a BEHIND move the range runs the other way,
// so the commits listed are the ones the move takes away.
func CommitsOf(ctx context.Context, l CommitLister, r CommitRange, max int) ([]release.Commit, []int, error) {
	if r.ToCommit == "" {
		return nil, nil, fmt.Errorf("the release records no commit")
	}
	from, to := r.FromCommit, r.ToCommit
	if r.Reverts {
		from, to = r.ToCommit, r.FromCommit
	}
	commits, err := l.Commits(ctx, from, to, max)
	if err != nil {
		return nil, nil, err
	}
	return commits, PRNumbers(commits), nil
}

// PathChange is one path a range touched.
type PathChange struct {
	// Status is git's one-letter status: A added, M modified, D deleted.
	Status string `json:"status"`
	Path   string `json:"path"`
}

// ChangedPaths lists the paths under pathspec that differ between two
// commits, for "which migrations did this release carry". An empty from lists
// nothing: a first release has no earlier commit to differ from.
func (g Git) ChangedPaths(ctx context.Context, from, to string, pathspec ...string) ([]PathChange, error) {
	if from == "" {
		return nil, nil
	}
	if !release.ValidObjectID(from) || !release.ValidObjectID(to) {
		return nil, fmt.Errorf("a path diff needs two full git object ids")
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	args := append([]string{"diff", "--name-status", "--no-renames", from + ".." + to, "--"}, pathspec...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Dir
	raw, err := cmd.Output()
	if err != nil {
		msg := ""
		if ee, ok := err.(*exec.ExitError); ok {
			msg = ": " + strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("git diff %s..%s: %w%s", from, to, err, msg)
	}
	var out []PathChange
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		status, path, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("unexpected git diff line %q", line)
		}
		out = append(out, PathChange{Status: status, Path: path})
	}
	return out, nil
}
