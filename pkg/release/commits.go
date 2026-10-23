package release

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The source history a release adds, recorded when it is cut.
//
// WHY AT CUT AND NOT AT READ. Every reader of a release — the control plane,
// a console, `forge env status` on another machine — has the ledger and no
// repository. Git ancestry is only answerable where the checkout is, so the
// one moment the range can be computed for everyone is the cut.
//
// WHY RELATIVE TO A BASE RELEASE. A range is "the commits reachable from this
// release's commit and not from an earlier release's", and recording which
// earlier release makes ranges compose: the commits between any two releases
// on one line of history are the ranges of the releases between them, walked
// back through their bases ([CompareCommits]).
//
// WHY CAPPED, AND WHY THE CAP IS EXPLICIT. A first release of a long-lived
// repository would otherwise record its whole history, and CI checks out
// shallow clones whose history ends after a commit or two. Both are recorded
// as what they are — the newest [MaxRangeCommits] commits, with Truncated set
// and Total saying how many there were when the checkout could count them —
// never as a short list that reads as complete.

const (
	// MaxRangeCommits is the most commits one release records.
	MaxRangeCommits = 100
	// MaxCommitSubjectBytes bounds a recorded subject line.
	MaxCommitSubjectBytes = 256
	// MaxCommitAuthorBytes bounds a recorded author name.
	MaxCommitAuthorBytes = 128
	// CaptureRangeTimeout bounds the git work [CaptureCommitRange] does. Past
	// it the cut records no range rather than making anyone wait.
	CaptureRangeTimeout = 10 * time.Second
)

// rangeSearchDepth is how far back [CaptureCommitRange] looks for an earlier
// release's commit. A base further back than this is treated as unreachable,
// and the range as incomplete.
const rangeSearchDepth = 20000

// Commit is one commit in a recorded range. A claim, like [Provenance]'s
// Branch: what the cutting checkout's git said.
type Commit struct {
	// SHA is the full object id.
	SHA string `json:"sha"`
	// Subject is the first line of the message, bounded at
	// [MaxCommitSubjectBytes].
	Subject string `json:"subject"`
	// Author is the author's name. Never an email: the ledger is shown to
	// every member of an org.
	Author string `json:"author,omitempty"`
	// Time is the committer date, UTC — the same clock as
	// [Provenance.CommitTime].
	Time time.Time `json:"time"`
}

// CommitRange is the source history a release adds over the release it was
// cut after.
type CommitRange struct {
	// BaseRelease is the earlier release this range starts after: the newest
	// release of the project, by cut time, whose clean commit is an ancestor
	// of this release's. BaseCommit is that release's commit. Both are empty
	// when the cutting checkout held no such release in its history — the
	// project's first release, or a shallow clone ([CommitRange.Truncated]).
	BaseRelease string `json:"base_release,omitempty"`
	BaseCommit  string `json:"base_commit,omitempty"`
	// Commits are reachable from the release's commit and not from
	// BaseCommit, newest first, at most [MaxRangeCommits]. Empty, never nil,
	// when the release re-cuts its base's commit.
	Commits []Commit `json:"commits"`
	// Total is how many commits the range holds. It equals len(Commits)
	// unless Truncated, and is 0 when Truncated and the count is unknown.
	Total int `json:"total,omitempty"`
	// Truncated says Commits is not the whole range. Either the range is
	// longer than [MaxRangeCommits] (Total says how long), or the checkout's
	// history ended before the base was reached — a shallow clone, which is
	// how CI checks out by default — so the rest, and the count, are
	// unknowable (Total is 0).
	Truncated bool `json:"truncated,omitempty"`
}

// CountKnown reports whether Total is the range's real length.
func (r CommitRange) CountKnown() bool { return !r.Truncated || r.Total > 0 }

// Validate checks the range's shape. Like [Provenance.Validate], it cannot
// check the claims' truth.
func (r CommitRange) Validate() error {
	if (r.BaseRelease == "") != (r.BaseCommit == "") {
		return fmt.Errorf("%w: commit range names a base release or a base commit, not both", ErrInvalid)
	}
	if r.BaseCommit != "" && !ValidObjectID(r.BaseCommit) {
		return fmt.Errorf("%w: commit range base %q is not a full git object id", ErrInvalid, r.BaseCommit)
	}
	if len(r.Commits) > MaxRangeCommits {
		return fmt.Errorf("%w: commit range records %d commits, over the cap of %d", ErrInvalid, len(r.Commits), MaxRangeCommits)
	}
	switch {
	case r.Total < 0:
		return fmt.Errorf("%w: commit range total %d is negative", ErrInvalid, r.Total)
	case !r.Truncated && r.Total != len(r.Commits):
		return fmt.Errorf("%w: commit range is complete but its total %d is not its %d commits", ErrInvalid, r.Total, len(r.Commits))
	case r.Truncated && r.Total > 0 && r.Total <= len(r.Commits):
		return fmt.Errorf("%w: commit range is truncated but its total %d does not exceed its %d commits", ErrInvalid, r.Total, len(r.Commits))
	}
	seen := make(map[string]bool, len(r.Commits))
	for i, c := range r.Commits {
		if !ValidObjectID(c.SHA) {
			return fmt.Errorf("%w: commit range entry %d: %q is not a full git object id", ErrInvalid, i, c.SHA)
		}
		if seen[c.SHA] {
			return fmt.Errorf("%w: commit range lists %s twice", ErrInvalid, c.SHA)
		}
		seen[c.SHA] = true
		if err := validateCommitText("commit subject", c.Subject, MaxCommitSubjectBytes); err != nil {
			return err
		}
		if err := validateCommitText("commit author", c.Author, MaxCommitAuthorBytes); err != nil {
			return err
		}
	}
	return nil
}

// CommitText makes s safe to record as one line of at most max bytes: control
// characters become spaces, runs of space collapse, and an over-long value is
// cut at a rune boundary and marked with "…".
func CommitText(s string, max int) string { return oneLine(s, max) }

// oneLine is CommitText's rule, shared with the object summaries.
func oneLine(s string, max int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f || r == ' ' }), " ")
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	cut := max - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// validateCommitText refuses what [CommitText] would have changed.
func validateCommitText(what, s string, max int) error {
	if len(s) > max {
		return fmt.Errorf("%w: %s is %d bytes, over the %d-byte bound", ErrInvalid, what, len(s), max)
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalid, what)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %s must be one line without control characters", ErrInvalid, what)
		}
	}
	return nil
}

// CaptureCommitRange records the history head adds over the newest of earlier
// whose commit is an ancestor of it, read from the git checkout around dir.
//
// earlier is the project's other releases; dirty ones and ones with no commit
// are never a base (their commit does not describe their bytes). The release
// being cut must not be among them.
//
// It returns (nil, nil) when there is nothing to record: head is not a commit,
// or dir is not a git checkout. An error means git failed or ran out of
// [CaptureRangeTimeout]; a cut records no range then, and should say so.
func CaptureCommitRange(ctx context.Context, dir, head string, earlier []Release) (*CommitRange, error) {
	if !ValidObjectID(head) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, CaptureRangeTimeout)
	defer cancel()
	top, err := gitOut(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil
	}
	raw, err := gitRaw(ctx, top, nil, "rev-list", "--max-count="+strconv.Itoa(rangeSearchDepth), head)
	if err != nil {
		return nil, fmt.Errorf("capture commit range: %w", err)
	}
	ancestry := strings.Fields(string(raw))
	reachable := make(map[string]bool, len(ancestry))
	for _, sha := range ancestry {
		reachable[sha] = true
	}
	// The history ends early in a shallow clone; the walk ends early when it
	// hits its depth bound. Either way an absent base proves nothing.
	boundary, err := shallowBoundary(ctx, top)
	if err != nil {
		return nil, fmt.Errorf("capture commit range: %w", err)
	}
	searchCut := len(ancestry) >= rangeSearchDepth

	out := &CommitRange{Commits: []Commit{}}
	spec := head
	var inRange []string
	if base := nearestBase(earlier, reachable); base != nil {
		out.BaseRelease, out.BaseCommit = base.Version, base.Git.Commit
		spec = base.Git.Commit + ".." + head
		raw, err := gitRaw(ctx, top, nil, "rev-list", spec)
		if err != nil {
			return nil, fmt.Errorf("capture commit range: %w", err)
		}
		inRange = strings.Fields(string(raw))
	} else {
		inRange = ancestry
	}
	// Complete only when every commit in the range has its parents here: a
	// shallow boundary inside the range means history beyond it is missing,
	// and with no base, so is the place the range should have stopped.
	complete := !boundaryWithin(boundary, inRange)
	if out.BaseRelease == "" && (len(boundary) > 0 || searchCut) {
		complete = false
	}
	if len(inRange) > 0 {
		out.Commits, err = logCommits(ctx, top, spec, MaxRangeCommits)
		if err != nil {
			return nil, fmt.Errorf("capture commit range: %w", err)
		}
	}
	if complete {
		out.Total = len(inRange)
		out.Truncated = out.Total > len(out.Commits)
	} else {
		out.Truncated = true
	}
	return out, nil
}

// nearestBase is the newest release, by cut time, whose clean commit is in
// reachable.
func nearestBase(earlier []Release, reachable map[string]bool) *Release {
	candidates := make([]Release, 0, len(earlier))
	for _, r := range earlier {
		if r.Git.Dirty || !ValidObjectID(r.Git.Commit) {
			continue
		}
		candidates = append(candidates, r)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].CreatedAt.After(candidates[j].CreatedAt)
		}
		return candidates[i].Version > candidates[j].Version
	})
	for i := range candidates {
		if reachable[candidates[i].Git.Commit] {
			return &candidates[i]
		}
	}
	return nil
}

// shallowBoundary lists the commits whose parents a shallow clone omits; nil
// for a complete repository.
func shallowBoundary(ctx context.Context, top string) (map[string]bool, error) {
	path, err := gitOut(ctx, top, "rev-parse", "--git-path", "shallow")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(top, path)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, sha := range strings.Fields(string(data)) {
		out[sha] = true
	}
	return out, nil
}

func boundaryWithin(boundary map[string]bool, commits []string) bool {
	if len(boundary) == 0 {
		return false
	}
	for _, sha := range commits {
		if boundary[sha] {
			return true
		}
	}
	return false
}

// logCommits reads up to max commits of spec, newest first. Fields are split
// on unit separators and records on record separators, which no subject or
// name can contain once git has formatted it.
func logCommits(ctx context.Context, top, spec string, max int) ([]Commit, error) {
	raw, err := gitRaw(ctx, top, nil, "log", "--max-count="+strconv.Itoa(max),
		"--format=%H%x1f%ct%x1f%an%x1f%s%x1e", spec)
	if err != nil {
		return nil, err
	}
	out := []Commit{}
	for _, rec := range strings.Split(string(raw), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\x1f", 4)
		if len(f) != 4 || !ValidObjectID(f[0]) {
			return nil, fmt.Errorf("unexpected git log record %q", rec)
		}
		secs, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected commit time %q for %s", f[1], f[0])
		}
		out = append(out, Commit{
			SHA:     f[0],
			Time:    time.Unix(secs, 0).UTC(),
			Author:  CommitText(f[2], MaxCommitAuthorBytes),
			Subject: CommitText(f[3], MaxCommitSubjectBytes),
		})
	}
	return out, nil
}
