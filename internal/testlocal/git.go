package testlocal

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// Git runs one git command in dir and returns its stdout. It is the seam
// every git read in this package goes through, so tests hand in a fake
// instead of exec'ing git.
type Git func(ctx context.Context, dir string, args ...string) ([]byte, error)

// ExecGit is the Git that runs the git on PATH.
func ExecGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

func gitLine(ctx context.Context, git Git, dir string, args ...string) (string, error) {
	out, err := git(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Toplevel is the repository root around dir.
func Toplevel(ctx context.Context, git Git, dir string) (string, error) {
	return gitLine(ctx, git, dir, "rev-parse", "--show-toplevel")
}

// BlobIDs maps every file of a tree-ish (a commit, or the tree hash a run
// recorded) to its git blob id, by repo-relative path. It is `git ls-tree
// -r`: the exact blobs the run's source was, whatever the checkout holds now.
func BlobIDs(ctx context.Context, git Git, dir, treeish string) (map[string]string, error) {
	out, err := git(ctx, dir, "ls-tree", "-r", "-z", "--full-tree", treeish)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		// "<mode> blob <sha>\t<path>"
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		f := strings.Fields(string(rec[:tab]))
		if len(f) == 3 && f[1] == "blob" {
			m[string(rec[tab+1:])] = f[2]
		}
	}
	return m, nil
}

// IndexBlobIDs is BlobIDs for the checkout as it is now: `git ls-files -s`
// for the index, with each file the worktree has changed hashed from disk
// (what `git hash-object` prints), so a dirty file's id is the content the
// tests ran on rather than the committed one. hash reads a repo-relative
// file and returns its blob id.
func IndexBlobIDs(ctx context.Context, git Git, dir string, hash func(rel string) (string, bool)) (map[string]string, error) {
	out, err := git(ctx, dir, "ls-files", "-s", "-z")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		// "<mode> <sha> <stage>\t<path>"
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		f := strings.Fields(string(rec[:tab]))
		if len(f) == 3 {
			m[string(rec[tab+1:])] = f[1]
		}
	}
	changed, err := git(ctx, dir, "diff", "--name-only", "-z", "HEAD")
	if err != nil {
		return m, nil //nolint:nilerr // no HEAD yet: the index is all there is.
	}
	for _, p := range bytes.Split(changed, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		if id, ok := hash(string(p)); ok {
			m[string(p)] = id
		} else {
			delete(m, string(p))
		}
	}
	return m, nil
}

// MergeBase is the best common ancestor of a and b.
func MergeBase(ctx context.Context, git Git, dir, a, b string) (string, error) {
	return gitLine(ctx, git, dir, "merge-base", a, b)
}

// ResolveRev resolves a revision to a full commit sha.
func ResolveRev(ctx context.Context, git Git, dir, rev string) (string, error) {
	return gitLine(ctx, git, dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
}

// DefaultBranch is the remote's default branch ("origin/main"), falling back
// to a local main or master. Empty when none is found.
func DefaultBranch(ctx context.Context, git Git, dir string) string {
	if ref, err := gitLine(ctx, git, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
		return ref
	}
	for _, c := range []string{"origin/main", "origin/master", "main", "master"} {
		if _, err := ResolveRev(ctx, git, dir, c); err == nil {
			return c
		}
	}
	return ""
}

// ChangedLines is the line ranges a diff adds or changes, per repo-relative
// file: `git diff -U0 base...head` for a commit, or `git diff -U0 base` (the
// worktree against base) when head is empty. Deleted files and pure
// deletions add no lines.
func ChangedLines(ctx context.Context, git Git, dir, base, head string) ([]testreport.ChangedFile, error) {
	args := []string{"diff", "-U0", "--no-color", "--no-ext-diff", "--no-renames"}
	if head != "" {
		args = append(args, base+"..."+head)
	} else {
		args = append(args, base)
	}
	out, err := git(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	return ParseUnifiedZero(out), nil
}

// ParseUnifiedZero reads `git diff -U0` output into each file's added line
// ranges (the "+start,count" side of every hunk).
func ParseUnifiedZero(diff []byte) []testreport.ChangedFile {
	var (
		out []testreport.ChangedFile
		cur *testreport.ChangedFile
	)
	sc := bufio.NewScanner(bytes.NewReader(diff))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			cur = nil
		case strings.HasPrefix(line, "+++ "):
			p := strings.TrimPrefix(line, "+++ ")
			if p == "/dev/null" {
				cur = nil
				continue
			}
			p = unquoteDiffPath(p)
			p = strings.TrimPrefix(p, "b/")
			out = append(out, testreport.ChangedFile{Path: p})
			cur = &out[len(out)-1]
		case strings.HasPrefix(line, "@@ ") && cur != nil:
			// @@ -a,b +c,d @@
			f := strings.Fields(line)
			if len(f) < 3 || !strings.HasPrefix(f[2], "+") {
				continue
			}
			start, count := parseHunkSide(f[2][1:])
			if count > 0 && start > 0 {
				cur.Ranges = append(cur.Ranges, testreport.LineRange{Start: start, End: start + count - 1})
			}
		}
	}
	// Drop files with no added lines.
	kept := out[:0]
	for _, f := range out {
		if len(f.Ranges) > 0 {
			kept = append(kept, f)
		}
	}
	return kept
}

func parseHunkSide(s string) (start, count int) {
	count = 1
	if i := strings.IndexByte(s, ','); i >= 0 {
		count, _ = strconv.Atoi(s[i+1:])
		s = s[:i]
	}
	start, _ = strconv.Atoi(s)
	return start, count
}

func unquoteDiffPath(p string) string {
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}
