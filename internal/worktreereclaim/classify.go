// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// verdict is what the classifier concluded about one checkout (or workspace).
type verdict struct {
	Reason HeldReason // "" means safe to remove
	Detail string
}

func (v verdict) safe() bool { return v.Reason == "" }

// reasonRank orders reasons by how firmly they block removal; the worst one is
// what a multi-checkout workspace reports.
func reasonRank(r HeldReason) int {
	switch r {
	case ReasonUnmanaged:
		return 9
	case ReasonNestedRepo:
		return 8
	case ReasonInUse:
		return 7
	case ReasonOutsideFiles:
		return 6
	case ReasonData:
		return 5
	case ReasonUnverified:
		return 4
	case ReasonTooLarge:
		return 3
	case ReasonDirty:
		return 2
	case ReasonUnpushed:
		return 1
	}
	return 0
}

func worse(a, b verdict) verdict {
	if reasonRank(b.Reason) > reasonRank(a.Reason) {
		return b
	}
	return a
}

// maxSnapshotBytes caps the untracked bytes a snapshot may add to the shared
// object store. Snapshots run on machines that are short of disk.
const maxSnapshotBytes int64 = 512 << 20

// workTree returns the tree of the checkout's files as they are on disk now,
// built in a temporary index so the real index (and its skip-worktree and
// assume-unchanged flags) is never consulted or modified. It is the single
// definition of "the work": the snapshot saves it and the classifier compares
// it with HEAD. Ignored files are not part of it; data-bearing ignored files
// hold the worktree separately, see dataHold.
func workTree(ctx context.Context, checkout, tmp string, nested []string) (string, error) {
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	_ = os.Remove(filepath.Join(tmp, "index"))
	if _, err := git(ctx, checkout, env, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := git(ctx, checkout, env, append([]string{"add", "-A"}, excludeSpecs(checkout, nested)...)...); err != nil {
		return "", err
	}
	return git(ctx, checkout, env, "write-tree")
}

// excludeSpecs builds the pathspec that hides nested checkouts from their
// parent's view: git reports one as an untracked directory, but it is
// classified in its own right.
func excludeSpecs(checkout string, nested []string) []string {
	if len(nested) == 0 {
		return nil
	}
	specs := []string{"--", "."}
	for _, n := range nested {
		if rel, err := filepath.Rel(resolvePath(checkout), resolvePath(n)); err == nil {
			specs = append(specs, ":(exclude)"+filepath.ToSlash(rel))
		}
	}
	return specs
}

// dirtyPaths lists the files that differ from HEAD, from `git status` alone.
// It writes nothing: the automatic path classifies with no consent and must not
// copy a user's untracked files into the shared object store, and `status` is
// also far cheaper than hashing every tracked byte. Edits hidden behind
// skip-worktree or assume-unchanged are not in `status` at all; hiddenFlags
// holds those worktrees separately.
//
// Known gap, accepted: content that was staged and then reverted in the working
// tree (the "intermediate" blob). `status` reports the file as modified, so the
// automatic path holds it; the confirmed path saves the working-tree state and
// the intermediate staged blob is the one thing it does not keep.
func dirtyPaths(ctx context.Context, checkout string, nested []string) ([]string, error) {
	args := append([]string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames"}, excludeSpecs(checkout, nested)...)
	out, err := gitRaw(ctx, checkout, nil, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range strings.Split(string(out), "\x00") {
		if len(e) > 3 {
			paths = append(paths, e[3:])
		}
	}
	return paths, nil
}

// classifyCheckout decides whether a checkout's work is fully preserved
// elsewhere: nothing differs from HEAD (per `git status`), no index entry hides
// an edit behind skip-worktree or assume-unchanged, HEAD is reachable from a
// remote-tracking ref or the base branch, and nothing the worktree alone
// references (its own reflog, refs/worktree, refs/bisect, pseudo-refs, a
// detached HEAD) is on no branch, remote or tag. Anything git cannot answer is
// unverified; it never guesses toward removal.
//
// worktreeID names the snapshot refs that count as "saved elsewhere".
func classifyCheckout(ctx context.Context, checkout, baseBranch string, nested []string, worktreeID string) verdict {
	paths, err := dirtyPaths(ctx, checkout, nested)
	if err != nil {
		return verdict{ReasonUnverified, err.Error()}
	}
	if len(paths) > 0 {
		return verdict{ReasonDirty, changedSummary(strings.Join(paths, "\n"))}
	}
	if hidden := hiddenFlags(ctx, checkout); hidden != "" {
		return verdict{ReasonDirty, hidden}
	}

	head, err := git(ctx, checkout, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return verdict{ReasonUnverified, err.Error()}
	}
	pushed, err := headIsSafe(ctx, checkout, head, baseBranch)
	if err != nil {
		return verdict{ReasonUnverified, err.Error()}
	}
	if !pushed {
		return verdict{ReasonUnpushed, "HEAD is not on any remote branch and not merged into " + describeBase(baseBranch)}
	}

	tips, err := orphanTips(ctx, checkout, worktreeID)
	if err != nil {
		return verdict{ReasonUnverified, err.Error()}
	}
	if len(tips) > 0 {
		return verdict{ReasonUnreachable, fmt.Sprintf("its own history reaches %d commit tip(s) on no branch, remote or tag (reflog, detached HEAD or per-worktree refs)", len(tips))}
	}
	return verdict{}
}

func headIsSafe(ctx context.Context, checkout, head, baseBranch string) (bool, error) {
	onRemote, err := git(ctx, checkout, nil, "for-each-ref", "--contains", head, "--count=1", "--format=%(refname)", "refs/remotes")
	if err != nil {
		return false, err
	}
	if onRemote != "" {
		return true, nil
	}
	for _, ref := range baseRefs(ctx, checkout, baseBranch) {
		if _, err := git(ctx, checkout, nil, "merge-base", "--is-ancestor", head, ref); err == nil {
			return true, nil
		}
	}
	return false, nil
}

// orphanTips returns the minimal set of commits that only this worktree's own
// admin directory refers to. Removing the worktree deletes that directory and
// with it the reflog, refs/worktree/*, refs/bisect/* and the ORIG_HEAD-style
// pseudo-refs, so any commit reachable from them but from no branch, remote,
// tag or snapshot of this worktree would be left with no reference at all.
func orphanTips(ctx context.Context, checkout, worktreeID string) ([]string, error) {
	shas, err := worktreeOnlyShas(ctx, checkout)
	if err != nil {
		return nil, err
	}
	if len(shas) == 0 {
		return nil, nil
	}
	// Keep only objects that exist and are commits: a reflog can name commits
	// long since pruned, which nothing can lose twice.
	checked, err := gitStdin(ctx, checkout, strings.Join(shas, "\n")+"\n", "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range strings.Split(checked, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == "commit" {
			commits = append(commits, f[0])
		}
	}
	if len(commits) == 0 {
		return nil, nil
	}
	args := []string{"rev-list", "--stdin", "--not", "--branches", "--remotes", "--tags"}
	if worktreeID != "" {
		args = append(args, "--glob="+SnapshotRefPrefix(worktreeID)+"*")
	}
	reach, err := gitStdin(ctx, checkout, strings.Join(commits, "\n")+"\n", args...)
	if err != nil {
		return nil, err
	}
	if reach == "" {
		return nil, nil
	}
	orphan := map[string]bool{}
	for _, c := range strings.Fields(reach) {
		orphan[c] = true
	}
	var seeds []string
	for _, c := range commits {
		if orphan[c] {
			seeds = append(seeds, c)
		}
	}
	if len(seeds) == 0 {
		return nil, nil
	}
	tips, err := git(ctx, checkout, nil, append([]string{"merge-base", "--independent"}, seeds...)...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(tips), nil
}

// worktreeOnlyShas collects every commit id the worktree's private state names:
// both columns of its HEAD reflog, the per-worktree refs, the pseudo-refs in its
// admin directory, and a detached HEAD.
func worktreeOnlyShas(ctx context.Context, checkout string) ([]string, error) {
	seen := map[string]bool{}
	var shas []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if len(s) < 40 || strings.Trim(s, "0") == "" {
			return // not an object id, or the all-zero id a reflog uses for "none"
		}
		if !seen[s] {
			seen[s] = true
			shas = append(shas, s)
		}
	}
	admin, err := git(ctx, checkout, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(admin, "logs", "HEAD")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if f := strings.Fields(line); len(f) >= 2 {
				add(f[0])
				add(f[1])
			}
		}
	}
	for _, name := range []string{"ORIG_HEAD", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD"} {
		// A pseudo-ref can be empty or hold anything; take its first field only
		// when there is one. (An unguarded [0] here crashed the daemon.)
		if b, err := os.ReadFile(filepath.Join(admin, name)); err == nil {
			if f := strings.Fields(string(b)); len(f) > 0 {
				add(f[0])
			}
		}
	}
	refs, err := git(ctx, checkout, nil, "for-each-ref", "--format=%(objectname)", "refs/worktree", "refs/bisect")
	if err != nil {
		return nil, err
	}
	for _, s := range strings.Fields(refs) {
		add(s)
	}
	// A detached HEAD is the only thing naming its commit.
	if _, err := git(ctx, checkout, nil, "symbolic-ref", "-q", "HEAD"); err != nil {
		if head, herr := git(ctx, checkout, nil, "rev-parse", "--verify", "HEAD^{commit}"); herr == nil {
			add(head)
		}
	}
	return shas, nil
}

func changedSummary(names string) string {
	lines := strings.Fields(names)
	if len(lines) == 0 {
		return "files differ from HEAD"
	}
	shown := lines
	if len(shown) > 3 {
		shown = shown[:3]
	}
	more := ""
	if len(lines) > len(shown) {
		more = fmt.Sprintf(" and %d more", len(lines)-len(shown))
	}
	return fmt.Sprintf("%d changed or untracked file(s): %s%s", len(lines), strings.Join(shown, ", "), more)
}

// hiddenFlags reports index entries marked skip-worktree or assume-unchanged.
// `git status` does not report edits to those, and a worktree that uses them
// is one whose owner keeps local overrides there.
func hiddenFlags(ctx context.Context, checkout string) string {
	out, err := git(ctx, checkout, nil, "ls-files", "-v")
	if err != nil {
		return "could not read index flags: " + err.Error()
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 3 {
			continue
		}
		// 'S'/'s' skip-worktree; lowercase letters are assume-unchanged.
		if c := line[0]; c == 'S' || c == 's' || (c >= 'a' && c <= 'z') {
			return "has files hidden from git status (skip-worktree or assume-unchanged): " + strings.TrimSpace(line[2:])
		}
	}
	return ""
}

func describeBase(base string) string {
	if base == "" {
		return "the base branch"
	}
	return base
}

// baseRefs lists the existing refs HEAD may be merged into, remote first.
func baseRefs(ctx context.Context, checkout, base string) []string {
	if base == "" {
		return nil
	}
	var refs []string
	for _, c := range []string{"refs/remotes/origin/" + base, "refs/heads/" + base} {
		if _, err := git(ctx, checkout, nil, "rev-parse", "--verify", "--quiet", c+"^{commit}"); err == nil {
			refs = append(refs, c)
		}
	}
	return refs
}

// dataHold lists ignored files that are not known to be rebuildable. They are
// the user's data (a ./data directory, .env files, sqlite databases, a local
// postgres under .forge/hostinfra); only a person may delete them.
func dataHold(ctx context.Context, checkout string, nested []string) verdict {
	out, err := git(ctx, checkout, nil, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return verdict{ReasonUnverified, "could not list ignored files: " + err.Error()}
	}
	var data []string
	for _, e := range strings.Split(out, "\x00") {
		if e == "" {
			continue
		}
		unrecognizedIgnored(checkout, e, &data)
	}
	var kept []string
	for _, d := range data {
		full := filepath.Join(checkout, filepath.FromSlash(d))
		inNested := false
		for _, n := range nested {
			if samePath(full, n) || within(n, full) {
				inNested = true
			}
		}
		if !inNested {
			kept = append(kept, d)
		}
	}
	if len(kept) == 0 {
		return verdict{}
	}
	shown := kept
	if len(shown) > 3 {
		shown = shown[:3]
	}
	return verdict{ReasonData, "ignored files that are not rebuildable: " + strings.Join(shown, ", ")}
}

// untrackedBytes sums the size of untracked, non-ignored files: what a
// snapshot would add to the object store.
func untrackedBytes(ctx context.Context, checkout string) int64 {
	out, err := git(ctx, checkout, nil, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return 0
	}
	var total int64
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(checkout, rel)); err == nil && fi.Mode().IsRegular() {
			total += fi.Size()
		}
	}
	return total
}

// baseFor picks the base branch of one checkout: the server's entry for its
// path relative to the workspace root, else the workspace default.
func baseFor(w Worktree, checkout string) string {
	rel := relTo(w.Path, checkout)
	for _, c := range w.Checkouts {
		if filepath.ToSlash(filepath.Clean("/"+c.Rel)) == filepath.ToSlash(filepath.Clean("/"+rel)) && c.BaseBranch != "" {
			return c.BaseBranch
		}
	}
	return w.BaseBranch
}

// relTo is checkout's path under root in slash form, "" for the root itself.
func relTo(root, checkout string) string {
	rel, err := filepath.Rel(resolvePath(root), resolvePath(checkout))
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

func relDetail(root, checkout, detail string) string {
	rel := relTo(root, checkout)
	if rel == "" {
		return detail
	}
	return rel + ": " + detail
}

// nestedOf lists the other checkouts that live inside checkout.
func nestedOf(checkout string, all []string) []string {
	var out []string
	for _, c := range all {
		if c != checkout && within(checkout, c) {
			out = append(out, c)
		}
	}
	return out
}
