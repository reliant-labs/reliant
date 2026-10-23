package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Capture budget for the dirty-tree hash (F-16). Past either bound the tree is
// recorded as unhashed rather than making a developer wait: a clean tree's
// hash is HEAD^{tree} and costs nothing, so the bound only ever applies to a
// dirty checkout, which no protected path relies on.
const (
	CaptureTreeTimeout  = 5 * time.Second
	CaptureMaxDirtySize = 2 << 30 // bytes of untracked-unignored + modified content
)

// CaptureOptions are the facts CaptureProvenance cannot learn from git.
type CaptureOptions struct {
	// ForgeVersion is the rendering binary's version.
	ForgeVersion string
	// WorktreeKey is forge's devstack key for the checkout ("" = primary).
	WorktreeKey string
	// Host names the machine (a daemon id, or HostID()). Never a raw
	// hostname.
	Host string
	// Attestation is the CI identity, when there is one.
	Attestation *Attest
}

// CaptureProvenance records where the content in dir came from. It is
// best-effort in exactly one way: a directory that is not a git checkout
// yields a Provenance with no commit (and an error only when git itself is
// broken in a way the caller should hear about). It NEVER touches the user's
// index: the tree hash is computed through a temporary one.
//
// Exported so the daemon and CI capture provenance with the same code forge
// does — the field set is ledger vocabulary every backend must agree on.
func CaptureProvenance(ctx context.Context, dir string, opts CaptureOptions) (Provenance, error) {
	p := Provenance{
		ForgeVersion: opts.ForgeVersion,
		Attestation:  opts.Attestation,
		Worktree:     Worktree{Key: opts.WorktreeKey, Host: opts.Host},
	}
	top, err := gitOut(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		// Not a git checkout: nothing more to record, and not an error.
		p.Worktree.Label = filepath.Base(dir)
		p.Worktree.Path = dir
		return p, nil
	}
	p.Worktree.Path = top

	commit, err := gitOut(ctx, top, "rev-parse", "HEAD")
	if err != nil {
		// A repository with no commits yet.
		p.Worktree.Label = filepath.Base(top)
		return p, nil
	}
	p.Commit = commit
	p.CommitSubject, p.CommitAuthor = commitIdentity(ctx, top, commit)
	if b, err := gitOut(ctx, top, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		p.Branch = b
	}
	if t, err := gitOut(ctx, top, "describe", "--tags", "--exact-match", "HEAD"); err == nil {
		p.Tag = t
	}
	p.Worktree.Label = p.Branch
	if p.Worktree.Label == "" {
		p.Worktree.Label = filepath.Base(top)
	}
	p.Repo = canonicalRemote(ctx, top)

	changes, err := InspectCheckout(ctx, top)
	if err != nil {
		return p, fmt.Errorf("capture provenance: %w", err)
	}
	p.Dirty = changes.Dirty()
	if !p.Dirty {
		p.CommitTime = commitTime(ctx, top)
		tree, err := gitOut(ctx, top, "rev-parse", "HEAD^{tree}")
		if err != nil {
			return p, fmt.Errorf("capture provenance: resolve HEAD tree: %w", err)
		}
		p.Tree = tree
		return p, nil
	}
	if dirtySize(top, changes.Paths) > CaptureMaxDirtySize {
		return p, nil // unhashed: Provenance.Unhashed reports it
	}
	tree, err := dirtyTree(ctx, top, changes.Nested)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return p, nil // unhashed, by budget
		}
		return p, fmt.Errorf("capture provenance: hash working tree: %w", err)
	}
	p.Tree = tree
	return p, nil
}

// commitTime is HEAD's committer date in UTC, or nil when git cannot say. A
// unix timestamp is read rather than a formatted date so the result does not
// depend on git's date configuration.
func commitTime(ctx context.Context, top string) *time.Time {
	out, err := gitOut(ctx, top, "log", "-1", "--format=%ct", "HEAD")
	if err != nil {
		return nil
	}
	secs, err := strconv.ParseInt(out, 10, 64)
	if err != nil || secs <= 0 {
		return nil
	}
	t := time.Unix(secs, 0).UTC()
	return &t
}

// commitIdentity is commit's subject line and author name, each made safe to
// record (one line, bounded), or empty strings when git cannot say. Read as
// raw bytes with a unit separator between the two, so neither field's content
// can shift the other.
func commitIdentity(ctx context.Context, top, commit string) (subject, author string) {
	out, err := gitRaw(ctx, top, nil, "log", "-1", "--format=%s%x1f%an", commit)
	if err != nil {
		return "", ""
	}
	s, a, _ := strings.Cut(strings.TrimRight(string(out), "\n"), "\x1f")
	return CommitText(s, MaxCommitSubjectBytes), CommitText(a, MaxCommitAuthorBytes)
}

// dirtyTree hashes exactly what is in the working tree — tracked changes and
// untracked-unignored files — through a TEMPORARY index, so the user's staged
// state is never read or written. Two captures of the same dirty state give
// the same tree.
//
// Nested git checkouts are left out of the hash, as InspectCheckout leaves
// them out of the dirty verdict: they are another checkout's content, and
// `git add -A` would otherwise record each as an embedded-repo gitlink.
func dirtyTree(ctx context.Context, top string, nested []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, CaptureTreeTimeout)
	defer cancel()
	tmp, err := os.CreateTemp("", "forge-provenance-index-*")
	if err != nil {
		return "", err
	}
	idx := tmp.Name()
	_ = tmp.Close()
	// git refuses to read an empty file as an index; it must not exist.
	_ = os.Remove(idx)
	defer os.Remove(idx)
	env := append(os.Environ(), "GIT_INDEX_FILE="+idx)
	addArgs := []string{"add", "-A", "--", "."}
	for _, n := range nested {
		addArgs = append(addArgs, ":(exclude,literal)"+strings.TrimSuffix(n, "/"))
	}
	for _, args := range [][]string{{"read-tree", "HEAD"}, addArgs} {
		if _, err := gitOutEnv(ctx, top, env, args...); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", err
		}
	}
	out, err := gitOutEnv(ctx, top, env, "write-tree")
	if err != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	return out, err
}

// dirtySize estimates the bytes `git add -A` would hash: the sizes of the
// given changed paths. Deleted paths cost nothing.
func dirtySize(top string, paths []string) int64 {
	var total int64
	for _, path := range paths {
		if fi, err := os.Stat(filepath.Join(top, path)); err == nil && !fi.IsDir() {
			total += fi.Size()
		}
		if total > CaptureMaxDirtySize {
			return total
		}
	}
	return total
}

// Changes is what `git status` reports for a checkout, split by whether it is
// this checkout's own content.
type Changes struct {
	// Paths are the checkout's own uncommitted changes — tracked edits,
	// deletions, and untracked-unignored files — relative to the top of the
	// checkout, in git's order.
	Paths []string
	// Nested are untracked directories that are the root of ANOTHER git
	// checkout (a parked worktree, a cloned repo), with git's trailing slash.
	// They are not this checkout's content and are not in Paths.
	Nested []string
}

// Dirty reports whether the checkout has changes of its own.
func (c Changes) Dirty() bool { return len(c.Paths) > 0 }

// InspectCheckout reads `git status` for the checkout whose top is top.
//
// A NESTED GIT CHECKOUT IS NOT DIRT. An untracked directory with its own .git
// is another repository (agents park linked worktrees inside the project
// directory), and the status of the repository around it says nothing about
// the bytes this checkout would build. Counting it made a clean checkout
// "dirty", which cut and built a whole new release for an unchanged tree.
// Every other untracked file still counts.
func InspectCheckout(ctx context.Context, top string) (Changes, error) {
	out, err := gitRaw(ctx, top, nil, "--no-optional-locks", "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return Changes{}, err
	}
	var c Changes
	entries := strings.Split(string(out), "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		xy, path := e[:2], e[3:]
		// A rename or copy is followed by its origin as a separate entry.
		if strings.ContainsAny(xy, "RC") {
			i++
		}
		if xy == "??" && strings.HasSuffix(path, "/") && IsNestedCheckout(filepath.Join(top, path)) {
			c.Nested = append(c.Nested, path)
			continue
		}
		c.Paths = append(c.Paths, path)
	}
	return c, nil
}

// InspectDir is InspectCheckout for any directory inside a checkout. A
// directory that is not in a git checkout has no changes and is not an error,
// as in CaptureProvenance.
func InspectDir(ctx context.Context, dir string) (Changes, error) {
	top, err := gitOut(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Changes{}, nil
	}
	return InspectCheckout(ctx, top)
}

// IsNestedCheckout reports whether dir is the root of a git checkout: a
// repository (`.git` directory) or a linked worktree (`.git` file), whatever
// it is named.
func IsNestedCheckout(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// canonicalRemote returns origin's URL as "host/owner/repo": no scheme, no
// credentials, no ".git". "" when there is no origin.
func canonicalRemote(ctx context.Context, top string) string {
	url, err := gitOut(ctx, top, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return CanonicalRepo(url)
}

// CanonicalRepo normalizes a git remote URL to "host/owner/repo", so the SSH
// and HTTPS spellings of one repository compare equal and no credential in a
// URL is ever recorded.
func CanonicalRepo(url string) string {
	u := strings.TrimSpace(url)
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if at := strings.Index(u, "@"); at >= 0 && at < strings.Index(u+"/", "/") {
			u = u[at+1:]
		}
	} else if at := strings.Index(u, "@"); at >= 0 && strings.Contains(u[at:], ":") {
		// scp-like: git@github.com:owner/repo
		u = strings.Replace(u[at+1:], ":", "/", 1)
	}
	if i := strings.Index(u, "/"); i >= 0 {
		host := u[:i]
		if c := strings.LastIndex(host, ":"); c >= 0 {
			host = host[:c] // drop a port
		}
		u = strings.ToLower(host) + u[i:]
	}
	return u
}

// HostID is a stable, non-identifying machine id: the first 16 hex of
// sha256(hostname). Used when no daemon id is available.
func HostID() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(h))
	return hex.EncodeToString(sum[:])[:16]
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	return gitOutEnv(ctx, dir, nil, args...)
}

func gitOutEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, err := gitRaw(ctx, dir, env, args...)
	return strings.TrimSpace(string(out)), err
}

// gitRaw is git's stdout untouched: a leading space is meaningful in
// `status --porcelain` and trimming it shifts every field.
func gitRaw(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}
