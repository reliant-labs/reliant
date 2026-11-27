// Package suspect finds the commits most likely to have introduced an error:
// the commits of a release's range that touched the files the error was
// raised from. It is the local half of `forge obs issue <id>`: the control
// plane knows which release an issue first appeared in and what range that
// release deployed, but only the developer's checkout has the history.
//
// It reads the checkout with git and nothing else. Outside a checkout, or
// where the history is shallow, it returns an error the caller reports as a
// note and degrades to an empty list; it never invents a commit.
//
//forge:exclude-contract: a thin reader over the git binary in the developer's checkout; the CLI calls it directly and tests run it against a temp repository, so there is nothing to substitute
package suspect

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/releasetimeline"
)

// DefaultMax bounds the commits Find returns: with no release range known, the
// last 20 commits touching the culprit file.
const DefaultMax = 20

// maxFiles bounds how many culprit files one lookup passes to git.
const maxFiles = 8

// gitTimeout bounds one git run, so a hung checkout cannot hang a verb.
const gitTimeout = 10 * time.Second

// ErrNotCheckout says the directory is not inside a git work tree (or git is
// not installed).
var ErrNotCheckout = errors.New("not inside a git checkout")

// ErrNoFiles says none of the culprit files is tracked in this checkout, so
// there is no history to search.
var ErrNoFiles = errors.New("none of the culprit files is tracked in this checkout")

// Commit is one suspect commit.
type Commit struct {
	SHA     string
	Subject string
	Author  string
	Time    time.Time
	// Files are the culprit files the commit touched, repo-relative.
	Files []string
	// PR is the pull request the commit was merged as, parsed from a "(#123)"
	// squash subject; 0 when the subject names none.
	PR int
}

// Finder lists suspect commits from the git checkout around Dir.
type Finder struct {
	Dir string
}

var hexRef = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// ParseRange splits a "<from>..<to>" commit range. ok is false for anything
// else, including a side that is not a hex object name: the range arrives from
// a server and is passed to git, so only a plain commit id is accepted.
func ParseRange(s string) (from, to string, ok bool) {
	from, to, found := strings.Cut(strings.TrimSpace(s), "..")
	if !found || !hexRef.MatchString(to) || (from != "" && !hexRef.MatchString(from)) {
		return "", "", false
	}
	return from, to, true
}

// CulpritFile is the file of an issue's culprit ("<file> in <function>"), or ""
// when the culprit is a function alone.
func CulpritFile(culprit string) string {
	file, _, ok := strings.Cut(culprit, " in ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(file)
}

// Find lists the commits in commitRange ("<from>..<to>", or "" for the history
// of HEAD) that touched any of files, newest first. files are the paths the
// error reported, which are usually build paths ("/app/internal/x/y.go",
// "github.com/acme/app/internal/x/y.go"): each is matched to the tracked file
// it ends with.
func (f Finder) Find(ctx context.Context, commitRange string, files []string) ([]Commit, error) {
	if err := f.git(ctx, nil, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotCheckout, err)
	}
	tracked, err := f.trackedFiles(ctx)
	if err != nil {
		return nil, err
	}
	paths := Match(tracked, files)
	if len(paths) == 0 {
		return nil, ErrNoFiles
	}
	if len(paths) > maxFiles {
		paths = paths[:maxFiles]
	}

	spec := "HEAD"
	if from, to, ok := ParseRange(commitRange); ok {
		spec = to
		if from != "" {
			spec = from + ".." + to
		}
	} else if strings.TrimSpace(commitRange) != "" {
		return nil, fmt.Errorf("commit range %q is not <from>..<to> with full or abbreviated commit ids", commitRange)
	}
	args := append([]string{"log", "--max-count=" + strconv.Itoa(DefaultMax), "--name-only",
		"--format=%x1e%H%x1f%ct%x1f%an%x1f%s", spec, "--"}, paths...)
	var out strings.Builder
	if err := f.git(ctx, &out, args...); err != nil {
		return nil, err
	}
	return parseLog(out.String())
}

// Match maps reported file paths to tracked ones: a path that is tracked as
// given, or else the one tracked path it ends with at a directory boundary
// (the longest, so "internal/a/x.go" beats "a/x.go"). A bare file name matches
// only exactly. The result is sorted by first appearance and has no repeats.
func Match(tracked, reported []string) []string {
	set := make(map[string]bool, len(tracked))
	for _, t := range tracked {
		set[t] = true
	}
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, r := range reported {
		r = strings.TrimPrefix(path.Clean(strings.ReplaceAll(strings.TrimSpace(r), "\\", "/")), "./")
		if r == "" || r == "." {
			continue
		}
		if set[r] {
			add(r)
			continue
		}
		segs := strings.Split(r, "/")
		for i := 0; i < len(segs)-1; i++ {
			if s := strings.Join(segs[i:], "/"); set[s] {
				add(s)
				break
			}
		}
	}
	return out
}

func (f Finder) trackedFiles(ctx context.Context) ([]string, error) {
	var out strings.Builder
	if err := f.git(ctx, &out, "ls-files", "-z"); err != nil {
		return nil, err
	}
	var files []string
	for _, p := range strings.Split(out.String(), "\x00") {
		if p != "" {
			files = append(files, p)
		}
	}
	return files, nil
}

func (f Finder) git(ctx context.Context, out *strings.Builder, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--literal-pathspecs"}, args...)...)
	cmd.Dir = f.Dir
	raw, err := cmd.Output()
	if err != nil {
		msg := ""
		if ee, ok := err.(*exec.ExitError); ok {
			msg = ": " + strings.TrimSpace(string(ee.Stderr))
		}
		return fmt.Errorf("git %s: %w%s", args[0], err, msg)
	}
	if out != nil {
		out.Write(raw)
	}
	return nil
}

func parseLog(raw string) ([]Commit, error) {
	var out []Commit
	for _, rec := range strings.Split(raw, "\x1e") {
		rec = strings.Trim(rec, "\n")
		if rec == "" {
			continue
		}
		head, body, _ := strings.Cut(rec, "\n")
		fs := strings.SplitN(head, "\x1f", 4)
		if len(fs) != 4 {
			return nil, fmt.Errorf("unexpected git log record %q", head)
		}
		secs, err := strconv.ParseInt(fs[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected commit time %q for %s", fs[1], fs[0])
		}
		c := Commit{SHA: fs[0], Time: time.Unix(secs, 0).UTC(), Author: fs[2], Subject: fs[3]}
		c.PR, _ = releasetimeline.PRNumber(c.Subject)
		for _, file := range strings.Split(body, "\n") {
			if file = strings.TrimSpace(file); file != "" {
				c.Files = append(c.Files, file)
			}
		}
		out = append(out, c)
	}
	return out, nil
}
