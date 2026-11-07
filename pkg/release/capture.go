package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Capture budget for the dirty-tree hash (F-16). Past either bound the tree is
// left unhashed rather than making a developer wait, and CaptureProvenance
// says so with [ErrUnhashed]: a clean tree's hash is HEAD^{tree} and costs
// nothing, so the bound only ever applies to a dirty checkout, which no
// protected path relies on.
const (
	CaptureTreeTimeout  = 5 * time.Second
	CaptureMaxDirtySize = 2 << 30 // bytes of untracked-unignored + modified content
)

// ErrUnhashed is returned by CaptureProvenance, alongside the provenance it
// did capture, when the checkout names a commit but the built tree was not
// hashed: the dirty content exceeded [CaptureMaxDirtySize], or hashing it
// exceeded [CaptureTreeTimeout]. The provenance is still usable — its commit,
// branch and dirty flag are recorded and [Provenance.Unhashed] is true — so a
// best-effort caller tests for this with errors.Is and carries on. It is an
// error rather than a quiet empty Tree so that no caller mistakes "not
// hashed" for a hash: an empty tree is never a silent success.
var ErrUnhashed = errors.New("working tree not hashed")

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

	// treeBudget overrides CaptureTreeTimeout (0 = the default). Unexported:
	// it exists so this package's tests can pin the budget path without
	// depending on how fast the machine runs git.
	treeBudget time.Duration
}

// CaptureProvenance records where the content in dir came from. It is
// best-effort in exactly one way: a directory that is not a git checkout
// yields a Provenance with no commit (and an error only when git itself is
// broken in a way the caller should hear about). It NEVER touches the user's
// index: the tree hash is computed through a temporary one.
//
// A checkout with a commit always comes back with its Tree hashed or with an
// error: [ErrUnhashed] when a capture bound stopped the hash (the returned
// provenance is still complete otherwise), any other error when git failed or
// ctx ended. An empty Tree with a nil error is never returned for a commit.
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
		if ctx.Err() != nil {
			return p, fmt.Errorf("capture provenance: %w", ctx.Err())
		}
		// Not a git checkout: nothing more to record, and not an error.
		p.Worktree.Label = filepath.Base(dir)
		p.Worktree.Path = dir
		return p, nil
	}
	p.Worktree.Path = top

	// Every remaining read is independent and read-only, so they run at
	// once. A capture is a handful of git process starts, and on a loaded
	// machine it is the process start, not git's work, that costs: run one
	// after another they took seconds, long enough for a build to wait on.
	var (
		head                headFacts
		born                bool
		headErr, changesErr error
		branch, tag, repo   string
		changes             Changes
		wg                  sync.WaitGroup
	)
	wg.Go(func() { head, born, headErr = readHead(ctx, top) })
	wg.Go(func() {
		if b, err := gitOut(ctx, top, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
			branch = b
		}
	})
	wg.Go(func() {
		if t, err := gitOut(ctx, top, "describe", "--tags", "--exact-match", "HEAD"); err == nil {
			tag = t
		}
	})
	wg.Go(func() { repo = canonicalRemote(ctx, top) })
	wg.Go(func() { changes, changesErr = InspectCheckout(ctx, top) })
	wg.Wait()

	if ctx.Err() != nil {
		return p, fmt.Errorf("capture provenance: %w", ctx.Err())
	}
	if headErr != nil {
		return p, fmt.Errorf("capture provenance: %w", headErr)
	}
	if !born {
		// A repository with no commits yet.
		p.Worktree.Label = filepath.Base(top)
		return p, nil
	}
	p.Commit = head.commit
	p.CommitSubject = CommitText(head.subject, MaxCommitSubjectBytes)
	p.CommitAuthor = CommitText(head.author, MaxCommitAuthorBytes)
	p.Branch, p.Tag, p.Repo = branch, tag, repo
	p.Worktree.Label = p.Branch
	if p.Worktree.Label == "" {
		p.Worktree.Label = filepath.Base(top)
	}

	if changesErr != nil {
		return p, fmt.Errorf("capture provenance: %w", changesErr)
	}
	p.Dirty = changes.Dirty()
	if !p.Dirty {
		p.CommitTime = head.time
		p.Tree = head.tree
		return p, nil
	}
	if dirtySize(top, changes.Paths) > CaptureMaxDirtySize {
		return p, fmt.Errorf("capture provenance: %w: the uncommitted content exceeds the %d-byte hashing bound", ErrUnhashed, int64(CaptureMaxDirtySize))
	}
	budget := opts.treeBudget
	if budget <= 0 {
		budget = CaptureTreeTimeout
	}
	tctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	tree, err := dirtyTree(tctx, top, changes.Nested)
	switch {
	case err == nil:
		p.Tree = tree
		return p, nil
	case ctx.Err() != nil:
		return p, fmt.Errorf("capture provenance: hash working tree: %w", ctx.Err())
	case tctx.Err() != nil:
		// The capture's own budget, not the caller's context: git was
		// killed mid-hash, so its error says only "signal: killed".
		return p, fmt.Errorf("capture provenance: %w: hashing the uncommitted content took longer than %s: %w", ErrUnhashed, budget, tctx.Err())
	default:
		return p, fmt.Errorf("capture provenance: hash working tree: %w", err)
	}
}

// headFacts is HEAD's commit as one `git log` reports it.
type headFacts struct {
	commit, tree    string
	author, subject string // raw; CaptureProvenance bounds them
	// time is the committer date in UTC, or nil when git gave none.
	time *time.Time
}

// readHead reads HEAD's commit, tree, committer date, author and subject in
// one git process. born is false, with no error, only when HEAD is unborn — a
// repository (or orphan branch) with no commits yet. Every other failed read
// is an error: a HEAD that resolves to a missing or corrupt commit, or a
// broken ref, must not pass for an empty repository. So must output git
// should never produce: a commit recorded without its tree would be an
// unhashed record that no bound explains.
//
// Read as one NUL-framed record (logRecords), so no byte of a subject or a
// name can shift another field. The committer date is a unix timestamp so
// the result does not depend on git's date configuration.
func readHead(ctx context.Context, top string) (f headFacts, born bool, err error) {
	out, err := gitRaw(ctx, top, nil, "log", "-1", "--no-show-signature", "--format="+logFormat(headFields...), "HEAD")
	if err != nil {
		if ctx.Err() == nil && unbornHead(ctx, top) {
			return headFacts{}, false, nil
		}
		return headFacts{}, false, fmt.Errorf("read HEAD commit: %w", err)
	}
	f, err = parseHead(out)
	if err != nil {
		return headFacts{}, false, err
	}
	return f, true, nil
}

// headFields are readHead's `git log` placeholders, in parseHead's order.
var headFields = []string{"%H", "%T", "%ct", "%an", "%s"}

// parseHead is readHead's parse of `git log -1` output.
func parseHead(out []byte) (headFacts, error) {
	recs, err := logRecords(out, len(headFields))
	if err != nil {
		return headFacts{}, err
	}
	if len(recs) != 1 {
		return headFacts{}, fmt.Errorf("git log -1 HEAD: %d records, want 1: %q", len(recs), out)
	}
	fields := recs[0]
	if !ValidObjectID(fields[0]) || !ValidObjectID(fields[1]) {
		return headFacts{}, fmt.Errorf("git log -1 HEAD: unexpected output %q", out)
	}
	f := headFacts{commit: fields[0], tree: fields[1], author: fields[3], subject: fields[4]}
	if secs, err := strconv.ParseInt(fields[2], 10, 64); err == nil && secs > 0 {
		t := time.Unix(secs, 0).UTC()
		f.time = &t
	}
	return f, nil
}

// logFormat is a `git log --format` that frames every commit as a NUL, then
// each placeholder followed by a NUL. NUL is the one byte no field can hold:
// git refuses it in a commit message and in an identity, and hashes and
// timestamps are hex and digits. Any other separator is a byte a subject may
// legally contain.
func logFormat(placeholders ...string) string {
	return "%x00" + strings.Join(placeholders, "%x00") + "%x00"
}

// logRecords splits the output of `git log --format=logFormat(n placeholders)`
// into its records of exactly n fields, in order.
//
// Split on NUL, the output is the text before the first record, then for
// each record its n fields and the text after it (git's newline between
// commits, and anything else git prints between them, such as the signature
// verification log.showSignature adds ahead of a commit). The text outside
// the records is ignored — machineReadConfig and --no-show-signature already
// turn the known sources off, and this keeps an unforeseen one from shifting
// a field — so the field count is fixed by the framing alone. Output that
// does not divide into whole records is an error, never a dropped or a
// shifted commit.
func logRecords(out []byte, n int) ([][]string, error) {
	tokens := strings.Split(string(out), "\x00")
	if (len(tokens)-1)%(n+1) != 0 {
		return nil, fmt.Errorf("git log: output does not frame records of %d fields: %q", n, out)
	}
	recs := make([][]string, 0, (len(tokens)-1)/(n+1))
	for i := 1; i < len(tokens); i += n + 1 {
		recs = append(recs, tokens[i:i+n])
	}
	return recs, nil
}

// unbornHead reports whether HEAD names a branch that does not exist yet: it
// is a symbolic ref, and its target ref is ABSENT. It runs only after a HEAD
// read failed, so a capture of a repository with commits never pays for it.
//
// Absent is asked of git's ref store, never inferred from a failed
// resolution: `rev-parse --verify` fails alike for a missing ref and for one
// that exists but is malformed (a garbage loose file, a directory where the
// file should be), and only the first is unborn. `show-ref --exists` reports
// the two apart — exit 2 is "does not exist", 1 a failed lookup. Git before
// 2.43 has no --exists (exit 129), and also no ref store but the files one,
// so there the loose and packed ref files are read directly.
//
// Not unborn, so the failed read is the caller's error: a target that exists
// but is malformed, a HEAD that resolves to a missing or corrupt commit, and
// a HEAD symbolic-ref cannot read.
func unbornHead(ctx context.Context, top string) bool {
	target, err := gitOut(ctx, top, "symbolic-ref", "-q", "HEAD")
	if err != nil || target == "" {
		return false
	}
	_, err = gitOut(ctx, top, "show-ref", "--exists", target)
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false // the ref exists (or git did not run at all)
	}
	switch exit.ExitCode() {
	case 2:
		// Newer git's files backend reports a loose ref path that is a
		// DIRECTORY as "does not exist" (exit 2) where 2.43 fails the lookup
		// (exit 1). That is damage, not an unborn branch, so the exit is
		// confirmed against the loose path. A path git cannot name (a
		// non-files store) leaves git's answer standing.
		return !loosePathPresent(ctx, top, target)
	case 129:
		return refAbsentFromFiles(ctx, top, target)
	}
	return false
}

// loosePathPresent reports whether ref's loose path exists on disk in any
// form (a file, a directory, a symlink). A missing path, or a parent that is
// not a directory (reftable's stub `refs/heads` file), is not present; a path
// it cannot stat is, since unborn is only ever a positive finding.
func loosePathPresent(ctx context.Context, top, ref string) bool {
	p, err := gitOut(ctx, top, "rev-parse", "--git-path", ref)
	if err != nil || p == "" {
		return false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(top, p)
	}
	_, err = os.Lstat(p)
	return !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR)
}

// refAbsentFromFiles is unbornHead's check on a git without `show-ref
// --exists`: the ref has no loose file and no packed-refs line.
func refAbsentFromFiles(ctx context.Context, top, ref string) bool {
	out, err := gitOut(ctx, top, "rev-parse", "--git-path", ref, "--git-path", "packed-refs")
	if err != nil {
		return false
	}
	paths := strings.Split(out, "\n")
	if len(paths) != 2 {
		return false
	}
	for i, p := range paths {
		if !filepath.IsAbs(p) {
			paths[i] = filepath.Join(top, p)
		}
	}
	return refFileAbsent(paths[0], paths[1], ref)
}

// refFileAbsent reports whether ref is in neither its loose file nor the
// packed-refs file. Anything it cannot vouch for — a loose path that exists
// in any form or cannot be stat'ed, a packed-refs it cannot read or parse —
// is not absent: unborn is only ever a positive finding.
func refFileAbsent(loose, packed, ref string) bool {
	if _, err := os.Lstat(loose); !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	data, err := os.ReadFile(packed)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || !ValidObjectID(f[0]) || f[1] == ref {
			return false
		}
	}
	return true
}

// dirtyTree hashes exactly what is in the working tree — tracked changes and
// untracked-unignored files — through a TEMPORARY index, so the user's staged
// state is never read or written. Two captures of the same dirty state give
// the same tree.
//
// Nested git checkouts are left out of the hash, as InspectCheckout leaves
// them out of the dirty verdict: they are another checkout's content, and
// `git add -A` would otherwise record each as an embedded-repo gitlink.
//
// The caller bounds it through ctx; a result that is not a git object id is
// an error, never an empty hash.
func dirtyTree(ctx context.Context, top string, nested []string) (string, error) {
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
			return "", err
		}
	}
	out, err := gitOutEnv(ctx, top, env, "write-tree")
	if err != nil {
		return "", err
	}
	if !ValidObjectID(out) {
		return "", fmt.Errorf("git write-tree: reported %q, not a tree hash", out)
	}
	return out, nil
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

// machineReadConfig overrides the user configuration that changes what git
// prints, for every call this package parses. log.showSignature=true makes
// `git log` print a signed commit's verification text on stdout ahead of the
// --format record; color.ui=always colors that text; i18n.logOutputEncoding
// re-encodes subjects and names away from the UTF-8 the ledger records.
// (log.mailmap needs no override: %an is the raw author, %aN the mapped one.)
var machineReadConfig = []string{
	"-c", "log.showSignature=false",
	"-c", "color.ui=never",
	"-c", "i18n.logOutputEncoding=UTF-8",
}

// gitRaw is git's stdout untouched: a leading space is meaningful in
// `status --porcelain` and trimming it shifts every field. It runs git under
// machineReadConfig.
func gitRaw(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append(slices.Clone(machineReadConfig), args...)...)
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
