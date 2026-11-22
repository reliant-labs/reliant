package opindex

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// MapRange builds the index of the project at dir and maps base..head
// through it, commit by commit. See [Index.MapRange].
func MapRange(ctx context.Context, dir, base, head string) (Mapping, error) {
	ix, err := Build(dir)
	if err != nil {
		return Mapping{}, err
	}
	return ix.MapRange(ctx, base, head)
}

// MapCommit builds the index of the project at dir and maps one commit
// through it. See [Index.MapCommit].
func MapCommit(ctx context.Context, dir, sha string) (Mapping, error) {
	ix, err := Build(dir)
	if err != nil {
		return Mapping{}, err
	}
	return ix.MapCommit(ctx, sha)
}

// MapRange maps every non-merge commit of base..head on its own, so each
// mapped op lists the commits that changed it. With a head, only those
// commits are read; the checkout's index and working tree are not. head ""
// is the working tree: the commits base..HEAD, then a pseudo-commit with
// SHA "" holding what [Changes] reports for HEAD against the working tree —
// staged and unstaged edits (the working tree's content winning) and every
// untracked file .gitignore does not exclude. At most the newest
// release.MaxRangeCommits commits are mapped; Truncated says when more
// were in the range.
//
// base is used as given. To compare against a branch's fork point, resolve
// it first with [MergeBase].
func (ix *Index) MapRange(ctx context.Context, base, head string) (Mapping, error) {
	baseSHA, err := revParse(ctx, ix.dir, base)
	if err != nil {
		return Mapping{}, err
	}
	tip := head
	if tip == "" {
		tip = "HEAD"
	}
	tipSHA, err := revParse(ctx, ix.dir, tip)
	if err != nil {
		return Mapping{}, err
	}
	spec := baseSHA + ".." + tipSHA
	count, err := gitOut(ctx, ix.dir, "rev-list", "--count", "--no-merges", spec)
	if err != nil {
		return Mapping{}, err
	}
	total, err := strconv.Atoi(strings.TrimSpace(string(count)))
	if err != nil {
		return Mapping{}, fmt.Errorf("opindex: git rev-list --count: %w", err)
	}
	// One `git log -p` for the whole range: a diff per commit, each against
	// its parent, split on the format line's record separator.
	args := append(diffArgs("log"), "--no-merges", "--max-count="+strconv.Itoa(release.MaxRangeCommits),
		"--format="+logRecord+"%H"+logField+"%P"+logField+"%s", spec)
	out, err := gitOut(ctx, ix.dir, args...)
	if err != nil {
		return Mapping{}, err
	}
	commits, err := parseLogPatch(out)
	if err != nil {
		return Mapping{}, err
	}

	blobs, err := ix.openBlobs(ctx)
	if err != nil {
		return Mapping{}, err
	}
	defer blobs.close()

	acc := newAccum()
	if head == "" {
		changes, err := Changes(ctx, ix.dir, tipSHA, "")
		if err != nil {
			return Mapping{}, err
		}
		if len(changes) > 0 {
			c := &Commit{Subject: UncommittedSubject}
			acc.mapped = append(acc.mapped, *c)
			if err := ix.mapInto(acc, changes, ix.readDisk, blobs.at(tipSHA), c); err != nil {
				return Mapping{}, err
			}
		}
	}
	for _, lc := range commits {
		c := &Commit{SHA: lc.sha, Subject: lc.subject, PR: ParsePR(lc.subject)}
		acc.mapped = append(acc.mapped, *c)
		parent := "" // a root commit: every path is missing on the base side
		if len(lc.parents) > 0 {
			parent = lc.parents[0]
		}
		if err := ix.mapInto(acc, lc.changes, blobs.at(lc.sha), blobs.at(parent), c); err != nil {
			return Mapping{}, err
		}
	}
	m := acc.mapping()
	m.Base, m.Total, m.Truncated = baseSHA, total, total > len(commits)
	if head != "" {
		m.Head = tipSHA
	}
	return m, nil
}

// MapCommit maps the one commit sha against its first parent (the empty
// tree for a root commit). A merge commit is mapped as everything it brought
// onto its first parent.
func (ix *Index) MapCommit(ctx context.Context, sha string) (Mapping, error) {
	full, err := revParse(ctx, ix.dir, sha)
	if err != nil {
		return Mapping{}, err
	}
	out, err := gitOut(ctx, ix.dir, "log", "-1", "--format="+logRecord+"%H"+logField+"%P"+logField+"%s", full)
	if err != nil {
		return Mapping{}, err
	}
	commits, err := parseLogPatch(out)
	if err != nil {
		return Mapping{}, err
	}
	if len(commits) != 1 {
		return Mapping{}, fmt.Errorf("opindex: git log %s returned %d commits", full, len(commits))
	}
	lc := commits[0]
	parent, diffBase := "", ""
	if len(lc.parents) > 0 {
		parent, diffBase = lc.parents[0], lc.parents[0]
	} else {
		empty, err := gitOut(ctx, ix.dir, "hash-object", "-t", "tree", "--stdin")
		if err != nil {
			return Mapping{}, err
		}
		diffBase = strings.TrimSpace(string(empty))
	}
	changes, err := Changes(ctx, ix.dir, diffBase, full)
	if err != nil {
		return Mapping{}, err
	}
	blobs, err := ix.openBlobs(ctx)
	if err != nil {
		return Mapping{}, err
	}
	defer blobs.close()
	c := &Commit{SHA: full, Subject: lc.subject, PR: ParsePR(lc.subject)}
	acc := newAccum()
	acc.mapped = append(acc.mapped, *c)
	if err := ix.mapInto(acc, changes, blobs.at(full), blobs.at(parent), c); err != nil {
		return Mapping{}, err
	}
	m := acc.mapping()
	m.Base, m.Head, m.Total = parent, full, 1
	return m, nil
}

// The format line of each commit in a `git log -p` this package reads: a
// record separator no patch line starts with, then %H, %P and %s split by
// the unit separator.
const (
	logRecord = "%x1e"
	logField  = "%x1f"
)

type logCommit struct {
	sha     string
	parents []string
	subject string
	changes []FileChange
}

// parseLogPatch splits `git log -p` output into commits and their diffs.
func parseLogPatch(out []byte) ([]logCommit, error) {
	var (
		commits []logCommit
		pp      patchParser
	)
	for _, line := range strings.Split(string(out), "\n") {
		if !pp.inHunk() && strings.HasPrefix(line, "\x1e") {
			if n := len(commits); n > 0 {
				commits[n-1].changes = pp.finish()
			}
			parts := strings.SplitN(line[1:], "\x1f", 3)
			if len(parts) != 3 {
				return nil, fmt.Errorf("opindex: malformed git log record %q", line)
			}
			commits = append(commits, logCommit{sha: parts[0], parents: strings.Fields(parts[1]), subject: parts[2]})
			continue
		}
		if len(commits) == 0 {
			continue
		}
		if err := pp.feed(line); err != nil {
			return nil, err
		}
	}
	if n := len(commits); n > 0 {
		commits[n-1].changes = pp.finish()
	}
	return commits, nil
}

// readDisk reads a project-relative path from the working tree.
func (ix *Index) readDisk(p string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(ix.dir, filepath.FromSlash(p)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// blobReader reads files at any revision through one long-lived
// `git cat-file --batch`, rather than a git process per file per side.
type blobReader struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	prefix string // the project directory, relative to the repository root
	stderr bytes.Buffer
}

func (ix *Index) openBlobs(ctx context.Context) (*blobReader, error) {
	prefix, err := gitOut(ctx, ix.dir, "rev-parse", "--show-prefix")
	if err != nil {
		return nil, err
	}
	b := &blobReader{prefix: strings.TrimSpace(string(prefix))}
	b.cmd = exec.CommandContext(ctx, "git", "cat-file", "--batch")
	b.cmd.Dir = ix.dir
	b.cmd.Env = gitEnv()
	b.cmd.Stderr = &b.stderr
	if b.in, err = b.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	stdout, err := b.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	b.out = bufio.NewReader(stdout)
	if err := b.cmd.Start(); err != nil {
		return nil, err
	}
	return b, nil
}

// at reads project-relative paths at rev; rev "" has no files.
func (b *blobReader) at(rev string) ReadFunc {
	return func(p string) ([]byte, error) {
		if rev == "" {
			return nil, nil
		}
		return b.read(rev + ":" + b.prefix + p)
	}
}

func (b *blobReader) read(object string) ([]byte, error) {
	if strings.ContainsAny(object, "\n\r") {
		return nil, fmt.Errorf("opindex: cannot read %q through git cat-file", object)
	}
	if _, err := io.WriteString(b.in, object+"\n"); err != nil {
		return nil, b.fail(err)
	}
	header, err := b.out.ReadString('\n')
	if err != nil {
		return nil, b.fail(err)
	}
	header = strings.TrimSuffix(header, "\n")
	if strings.HasSuffix(header, " missing") || strings.HasSuffix(header, " ambiguous") {
		return nil, nil
	}
	fields := strings.Fields(header)
	if len(fields) != 3 {
		return nil, fmt.Errorf("opindex: git cat-file: unexpected header %q", header)
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil {
		return nil, fmt.Errorf("opindex: git cat-file: unexpected header %q", header)
	}
	buf := make([]byte, size+1) // the content, then a newline
	if _, err := io.ReadFull(b.out, buf); err != nil {
		return nil, b.fail(err)
	}
	if fields[1] != "blob" {
		return nil, nil // a directory where a file was expected
	}
	return buf[:size], nil
}

func (b *blobReader) fail(err error) error {
	return fmt.Errorf("opindex: git cat-file --batch: %w: %s", err, strings.TrimSpace(b.stderr.String()))
}

func (b *blobReader) close() {
	_ = b.in.Close()
	_ = b.cmd.Wait()
}

// MergeBase returns the best common ancestor of base and head (head ""
// meaning HEAD), the default base of a branch's blast radius: `origin/main`
// alone would also count every commit main gained since the branch forked.
func MergeBase(ctx context.Context, dir, base, head string) (string, error) {
	if head == "" {
		head = "HEAD"
	}
	out, err := gitOut(ctx, dir, "merge-base", base, head)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

var rePRSuffix = regexp.MustCompile(`\(#(\d+)\)\s*$`)

// ParsePR returns the pull request number a commit subject ends with — the
// " (#123)" GitHub appends to a squash merge — or 0.
func ParsePR(subject string) int {
	m := rePRSuffix.FindStringSubmatch(subject)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

func revParse(ctx context.Context, dir, rev string) (string, error) {
	out, err := gitOut(ctx, dir, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitError is a failed git command, with its stderr.
type gitError struct {
	args   []string
	err    error
	stderr string
}

func (e *gitError) Error() string {
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.args, " "), e.err, e.stderr)
}

func (e *gitError) Unwrap() error { return e.err }

// gitEnv is the environment every git this package runs gets: no optional
// locks (a read must not leave another git in the same checkout facing
// index.lock), the C locale, and never a credential prompt.
func gitEnv() []string {
	return append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
}

// gitOut runs git in dir.
func gitOut(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, &gitError{args: args, err: err, stderr: strings.TrimSpace(stderr.String())}
	}
	return out, nil
}
