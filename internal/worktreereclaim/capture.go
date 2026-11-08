// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A capture is everything git can see of one linked checkout, committed into
// a local ref of the checkout's own repository before the checkout is
// deleted, so a wrong decision to reclaim it costs a restore, not lost work
// (the all-worktrees reclaim design, owner decision 2026-11-08).
//
// The capture is one commit C, written with commit-tree and never checked
// out, whose tree is the files on disk and whose parents are, in order:
//
//	C^1   HEAD at capture time
//	C^2   a commit whose tree is the staged index (parent: HEAD)
//	C^3…  the tips of history only this checkout's reflog, refs/worktree,
//	      refs/bisect, pseudo-refs or detached HEAD reach (orphanTips)
//
// so every one of those stays reachable from the ref after the checkout's
// admin directory is gone. The ref lives in the shared repository,
//
//	refs/reliant/reclaimed/<key>/<unix-nanos>-<rand>
//
// where <key> is the Reliant row id for a Reliant workspace and
// p-<sha256(canonical path)[:16]> for any other checkout. Nothing is pushed.
// Ignored files are not captured: the eligibility rules hold a checkout whose
// ignored files are not rebuildable output (dataHold).
//
// Captures are never expired automatically. A capture that has been restored
// by worktree.ensure is moved to refs/reliant/restored/, never deleted, so it
// is not applied twice.

const (
	// CaptureRefRoot is the namespace of reclaimed checkouts' captures.
	CaptureRefRoot = "refs/reliant/reclaimed/"
	// RestoredCaptureRefRoot is where a capture moves once worktree.ensure
	// has put it back, so it is never applied again.
	RestoredCaptureRefRoot = "refs/reliant/restored/"
	// captureMetaTrailer carries the capture's metadata, as one JSON line,
	// in the capture commit's message.
	captureMetaTrailer = "Reliant-Reclaim: "

	// DefaultCaptureMaxBytes and DefaultCaptureMaxFiles bound the untracked,
	// non-ignored content one capture writes into the object store. A
	// checkout beyond either is held (ReasonTooLarge), never captured in
	// part.
	DefaultCaptureMaxBytes int64 = 64 << 20
	DefaultCaptureMaxFiles       = 10_000
)

// CaptureMeta is what a capture records about the checkout it came from. It
// rides in the commit message, so `git log -1 <ref>` explains a capture to a
// person without any Reliant tool.
type CaptureMeta struct {
	Path        string    `json:"path"`
	Branch      string    `json:"branch,omitempty"`
	Head        string    `json:"head"`
	Key         string    `json:"key"`
	RowID       string    `json:"row_id,omitempty"`
	Class       string    `json:"class,omitempty"`
	IdleSeconds int64     `json:"idle_seconds,omitempty"`
	CapturedAt  time.Time `json:"captured_at"`
	Version     string    `json:"daemon_version,omitempty"`
}

// Capture is one capture written for a checkout.
type Capture struct {
	Ref       string   `json:"ref"`
	Commit    string   `json:"commit"`
	CommonDir string   `json:"common_dir"`
	Head      string   `json:"head"`
	IndexTree string   `json:"index_tree"`
	DiskTree  string   `json:"disk_tree"`
	Orphans   []string `json:"orphans,omitempty"`
	// UntrackedBytes and UntrackedFiles are the untracked, non-ignored
	// content the capture added.
	UntrackedBytes int64 `json:"untracked_bytes,omitempty"`
	UntrackedFiles int   `json:"untracked_files,omitempty"`
}

// CaptureLimits bounds one capture; zero fields mean the defaults.
type CaptureLimits struct {
	MaxBytes int64
	MaxFiles int
}

func (l CaptureLimits) withDefaults() CaptureLimits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultCaptureMaxBytes
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = DefaultCaptureMaxFiles
	}
	return l
}

// CaptureKey is the capture key of a checkout that is not a Reliant row:
// p-<first 16 hex of sha256 of its canonical path>. Two checkouts with the
// same name in different places never share a key.
func CaptureKey(path string) string {
	sum := sha256.Sum256([]byte(resolvePath(path)))
	return "p-" + hex.EncodeToString(sum[:])[:16]
}

// captureRefDir is the ref directory of one key's captures.
func captureRefDir(key string) string { return CaptureRefRoot + sanitizeRefComponent(key) + "/" }

var errCaptureEnvelope = errors.New("outside the capture envelope")

// untrackedEnvelope measures the untracked, non-ignored content of a
// checkout and refuses what a capture must not take: more than the limits, or
// anything that is not a regular file or a symlink (git cannot record it).
func untrackedEnvelope(ctx context.Context, checkout string, limits CaptureLimits) (bytes int64, files int, err error) {
	limits = limits.withDefaults()
	out, err := gitRaw(ctx, checkout, nil, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return 0, 0, err
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		fi, err := os.Lstat(filepath.Join(checkout, filepath.FromSlash(rel)))
		if err != nil {
			return 0, 0, err
		}
		switch {
		case fi.Mode().IsRegular():
			bytes += fi.Size()
		case fi.Mode()&os.ModeSymlink != 0:
		default:
			return 0, 0, fmt.Errorf("%w: %s is not a regular file or a symlink", errCaptureEnvelope, rel)
		}
		files++
		if files > limits.MaxFiles {
			return 0, 0, fmt.Errorf("%w: more than %d untracked files", errCaptureEnvelope, limits.MaxFiles)
		}
		if bytes > limits.MaxBytes {
			return 0, 0, fmt.Errorf("%w: more than %d MiB of untracked files", errCaptureEnvelope, limits.MaxBytes>>20)
		}
	}
	return bytes, files, nil
}

// stagedTree is the tree of the checkout's index, written from a COPY of the
// index so the real one is never locked or refreshed. A checkout with no
// index file stages nothing (HEAD's tree). An index with unmerged entries
// (a conflict in progress) fails: there is no tree to record.
func stagedTree(ctx context.Context, checkout, tmp string) (string, error) {
	admin := adminDir(checkout)
	if admin == "" {
		return "", errors.New("not a linked worktree")
	}
	src, err := os.Open(filepath.Join(admin, "index"))
	if errors.Is(err, os.ErrNotExist) {
		return git(ctx, checkout, nil, "rev-parse", "--verify", "HEAD^{tree}")
	}
	if err != nil {
		return "", err
	}
	defer src.Close()
	copyPath := filepath.Join(tmp, "staged-index")
	dst, err := os.OpenFile(copyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return git(ctx, checkout, []string{"GIT_INDEX_FILE=" + copyPath}, "write-tree")
}

// captureCheckout writes a capture of checkout under key. The checkout's
// HEAD, index and files are never touched: both trees are built in temporary
// indexes. It refuses what it cannot record faithfully (a gitlink HEAD lacks,
// a conflicted index, content outside the envelope) and, once written,
// proves the ref names the trees it computed.
func captureCheckout(ctx context.Context, checkout, key string, meta CaptureMeta, limits CaptureLimits) (Capture, error) {
	common, err := commonDir(ctx, checkout)
	if err != nil {
		return Capture{}, err
	}
	head, err := git(ctx, checkout, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return Capture{}, err
	}
	ub, uf, err := untrackedEnvelope(ctx, checkout, limits)
	if err != nil {
		return Capture{}, err
	}
	tmp, err := os.MkdirTemp("", "reliant-capture-")
	if err != nil {
		return Capture{}, err
	}
	defer os.RemoveAll(tmp)
	index, err := stagedTree(ctx, checkout, tmp)
	if err != nil {
		return Capture{}, fmt.Errorf("staged state: %w", err)
	}
	disk, err := workTree(ctx, checkout, tmp, nil)
	if err != nil {
		return Capture{}, fmt.Errorf("files on disk: %w", err)
	}
	if err := checkGitlinks(ctx, checkout, disk); err != nil {
		return Capture{}, err
	}
	orphans, err := orphanTips(ctx, checkout, "")
	if err != nil {
		return Capture{}, fmt.Errorf("private history: %w", err)
	}
	sort.Strings(orphans)

	ident := []string{"-c", "user.name=reliant", "-c", "user.email=reliant@localhost"}
	staged, err := git(ctx, checkout, nil, append(ident, "commit-tree", index, "-p", head, "-m", "reliant: staged state of "+meta.Path)...)
	if err != nil {
		return Capture{}, err
	}
	meta.Head, meta.Key = head, key
	if meta.CapturedAt.IsZero() {
		meta.CapturedAt = time.Now().UTC()
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return Capture{}, err
	}
	msg := fmt.Sprintf("reliant: captured %s before reclaiming it\n\nRestore: reliant worktree restore <this ref>\n\n%s%s\n", meta.Path, captureMetaTrailer, metaJSON)
	args := append(ident, "commit-tree", disk, "-p", head, "-p", staged)
	for _, o := range orphans {
		args = append(args, "-p", o)
	}
	commit, err := git(ctx, checkout, nil, append(args, "-m", msg)...)
	if err != nil {
		return Capture{}, err
	}
	ref, err := createRef(ctx, checkout, captureRefDir(key), commit)
	if err != nil {
		return Capture{}, err
	}
	if got, err := git(ctx, checkout, nil, "rev-parse", "--verify", ref+"^{tree}"); err != nil || got != disk {
		return Capture{}, fmt.Errorf("capture ref %s does not hold the captured tree", ref)
	}
	return Capture{Ref: ref, Commit: commit, CommonDir: common, Head: head, IndexTree: index, DiskTree: disk,
		Orphans: orphans, UntrackedBytes: ub, UntrackedFiles: uf}, nil
}

// errCaptureMismatch means a checkout no longer equals its capture.
var errCaptureMismatch = errors.New("checkout differs from its capture")

// verifyCapture proves checkout still equals capture c: the same HEAD, the
// same staged tree, the same files on disk, and no private history the
// capture does not reach. It runs on the quarantined checkout immediately
// before deletion; any difference stops the deletion.
func verifyCapture(ctx context.Context, checkout string, c Capture) error {
	head, err := git(ctx, checkout, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if head != c.Head {
		return fmt.Errorf("%w: HEAD is %s, captured %s", errCaptureMismatch, short(head), short(c.Head))
	}
	if saved, err := git(ctx, checkout, nil, "rev-parse", "--verify", c.Ref+"^{tree}"); err != nil || saved != c.DiskTree {
		return fmt.Errorf("%w: capture ref %s is unreadable or moved", errCaptureMismatch, c.Ref)
	}
	tmp, err := os.MkdirTemp("", "reliant-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	index, err := stagedTree(ctx, checkout, tmp)
	if err != nil {
		return err
	}
	if index != c.IndexTree {
		return fmt.Errorf("%w: the staged state changed", errCaptureMismatch)
	}
	disk, err := workTree(ctx, checkout, tmp, nil)
	if err != nil {
		return err
	}
	if disk != c.DiskTree {
		return fmt.Errorf("%w: files changed (tree %s, captured %s)", errCaptureMismatch, short(disk), short(c.DiskTree))
	}
	tips, err := orphanTips(ctx, checkout, "")
	if err != nil {
		return err
	}
	for _, tip := range tips {
		if _, err := git(ctx, checkout, nil, "merge-base", "--is-ancestor", tip, c.Ref); err != nil {
			return fmt.Errorf("%w: commit %s is reachable only from this checkout and not from its capture", errCaptureMismatch, short(tip))
		}
	}
	return nil
}

// CaptureInfo describes one capture ref, read back from the repository.
type CaptureInfo struct {
	Ref       string      `json:"ref"`
	Commit    string      `json:"commit"`
	Head      string      `json:"head"`
	IndexTree string      `json:"index_tree"`
	DiskTree  string      `json:"disk_tree"`
	Orphans   []string    `json:"orphans,omitempty"`
	Meta      CaptureMeta `json:"meta"`
	// Clean: the capture holds nothing HEAD does not (no staged or
	// unstaged change, no untracked file, no private history).
	Clean bool `json:"clean"`
}

// ReadCapture reads capture ref from the repository whose git dir (or any
// checkout of it) is repo.
func ReadCapture(ctx context.Context, repo, ref string) (CaptureInfo, error) {
	dir, pre := gitDirArgs(repo)
	line, err := git(ctx, dir, nil, append(pre, "rev-list", "--parents", "-n", "1", ref)...)
	if err != nil {
		return CaptureInfo{}, err
	}
	shas := strings.Fields(line)
	if len(shas) < 3 {
		return CaptureInfo{}, fmt.Errorf("%s is not a reclaim capture (it has %d parent(s))", ref, len(shas)-1)
	}
	info := CaptureInfo{Ref: ref, Commit: shas[0], Head: shas[1], Orphans: shas[3:]}
	if info.DiskTree, err = git(ctx, dir, nil, append(pre, "rev-parse", "--verify", shas[0]+"^{tree}")...); err != nil {
		return CaptureInfo{}, err
	}
	if info.IndexTree, err = git(ctx, dir, nil, append(pre, "rev-parse", "--verify", shas[2]+"^{tree}")...); err != nil {
		return CaptureInfo{}, err
	}
	headTree, err := git(ctx, dir, nil, append(pre, "rev-parse", "--verify", shas[1]+"^{tree}")...)
	if err != nil {
		return CaptureInfo{}, err
	}
	body, err := git(ctx, dir, nil, append(pre, "log", "-1", "--format=%B", shas[0])...)
	if err != nil {
		return CaptureInfo{}, err
	}
	for _, l := range strings.Split(body, "\n") {
		if rest, ok := strings.CutPrefix(l, captureMetaTrailer); ok {
			_ = json.Unmarshal([]byte(rest), &info.Meta)
		}
	}
	info.Clean = info.DiskTree == headTree && info.IndexTree == headTree && len(info.Orphans) == 0
	return info, nil
}

// ListCaptures lists the captures in repo, newest first; key "" lists every
// key's.
func ListCaptures(ctx context.Context, repo, key string) ([]CaptureInfo, error) {
	dir, pre := gitDirArgs(repo)
	prefix := strings.TrimSuffix(CaptureRefRoot, "/")
	if key != "" {
		prefix = strings.TrimSuffix(captureRefDir(key), "/")
	}
	out, err := git(ctx, dir, nil, append(pre, "for-each-ref", "--format=%(refname)", prefix)...)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, r := range strings.Split(out, "\n") {
		if r != "" {
			refs = append(refs, r)
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refTime(refs[i]) > refTime(refs[j]) })
	infos := make([]CaptureInfo, 0, len(refs))
	for _, r := range refs {
		info, err := ReadCapture(ctx, repo, r)
		if err != nil {
			continue // not one of ours, or damaged: never guessed at
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// LatestCapture is the newest capture of key in repo that was taken of the
// checkout at path, or "" when there is none.
func LatestCapture(ctx context.Context, repo, key, path string) (CaptureInfo, bool) {
	infos, err := ListCaptures(ctx, repo, key)
	if err != nil {
		return CaptureInfo{}, false
	}
	for _, info := range infos {
		if info.Meta.Path != "" && samePath(info.Meta.Path, path) {
			return info, true
		}
	}
	return CaptureInfo{}, false
}

// gitDirArgs runs git against repo, which is either a checkout (run in it)
// or a git directory (named with --git-dir).
func gitDirArgs(repo string) (dir string, pre []string) {
	if fi, err := os.Stat(filepath.Join(repo, "HEAD")); err == nil && fi.Mode().IsRegular() {
		if _, err := os.Stat(filepath.Join(repo, "objects")); err == nil {
			return "", []string{"--git-dir=" + repo}
		}
	}
	return repo, nil
}
