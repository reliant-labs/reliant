// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
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
		return 10
	case ReasonNestedRepo:
		return 9
	case ReasonInUse:
		return 8
	case ReasonOutsideFiles:
		return 7
	case ReasonData:
		return 6
	case ReasonUnverified:
		return 5
	case ReasonTooLarge:
		return 4
	case ReasonDirty:
		return 3
	case ReasonUnpushed:
		return 2
	case ReasonUnreachable:
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
// an edit behind skip-worktree or assume-unchanged, HEAD is on a remote ref or
// its work is already in the base branch (headIsSafe), and nothing the worktree alone
// references (its own reflog, refs/worktree, refs/bisect, pseudo-refs, a
// detached HEAD) is on no branch, remote or tag. Anything git cannot answer is
// unverified; it never guesses toward removal.
//
// worktreeID names the snapshot refs that count as "saved elsewhere".
func classifyCheckout(ctx context.Context, checkout, baseBranch string, nested []string, worktreeID string, probe mergeProbe) verdict {
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
	// Which refs hold HEAD, remote and saved for this worktree (savedCommits,
	// or a snapshot taken on HEAD), in one walk.
	saved := ""
	if worktreeID != "" {
		saved = SnapshotRefPrefix(worktreeID)
	}
	onRemote, onSaved, err := refsContaining(ctx, checkout, head, saved)
	if err != nil {
		return verdict{ReasonUnverified, err.Error()}
	}
	pushed, err := headIsSafe(ctx, checkout, head, baseBranch, onRemote, probe)
	if err != nil {
		return verdict{ReasonUnverified, err.Error()}
	}
	if !pushed && onSaved {
		pushed = true
	}
	if !pushed {
		return verdict{ReasonUnpushed, "HEAD is not on any remote branch and its work is not in " + describeBase(baseBranch) + " (neither as commits nor as an equivalent patch)"}
	}

	tips, err := orphanTips(ctx, checkout, worktreeID)
	if err != nil {
		return verdict{ReasonUnverified, err.Error()}
	}
	tips, err = tipsNotOnBase(ctx, checkout, baseBranch, tips, probe.memo)
	if err != nil {
		return verdict{ReasonUnverified, err.Error()}
	}
	if len(tips) > 0 {
		return verdict{ReasonUnreachable, fmt.Sprintf("its own history reaches %d commit tip(s) on no branch, remote or tag (reflog, detached HEAD or per-worktree refs)", len(tips))}
	}
	return verdict{}
}

// tipsNotOnBase drops the orphan tips whose work is already in the base branch
// (an ancestor of it, or equal by patch). They are what a rebase, amend or
// squash leaves in the reflog: removing the worktree loses a name for a commit
// whose change lives on, so holding for it kept nearly every rebased worktree
// forever. A tip with a change the base lacks, or one the comparison cannot
// vouch for (a binary file, a merge commit), stays: that is the history the
// automatic path (saveCommits) and a confirmed clean-up save to a ref. Tips
// dropped here are never saved: nothing prunes refs/reliant/wip, and a ref
// pins its objects forever.
func tipsNotOnBase(ctx context.Context, checkout, baseBranch string, tips []string, memo *commitMemo) ([]string, error) {
	var lost []string
	for _, tip := range tips {
		merged, err := inBase(ctx, checkout, tip, baseBranch, memo)
		if err != nil {
			return nil, err
		}
		if !merged {
			lost = append(lost, tip)
		}
	}
	return lost, nil
}

// refsContaining reports whether a remote ref, and whether a ref under prefix
// (none when prefix is ""), contains commit: one `for-each-ref --contains`
// over both namespaces.
func refsContaining(ctx context.Context, checkout, commit, prefix string) (onRemote, underPrefix bool, err error) {
	args := []string{"for-each-ref", "--contains", commit, "--format=%(refname)", "refs/remotes"}
	saved := strings.TrimSuffix(prefix, "/")
	if saved != "" {
		args = append(args, saved)
	}
	out, err := git(ctx, checkout, nil, args...)
	if err != nil {
		return false, false, err
	}
	for _, ref := range strings.Fields(out) {
		switch {
		case strings.HasPrefix(ref, "refs/remotes/"):
			onRemote = true
		case saved != "" && (ref == saved || strings.HasPrefix(ref, saved+"/")):
			underPrefix = true
		}
	}
	return onRemote, underPrefix, nil
}

// mergeProbe is the part of "is this work preserved" that needs the network. A
// nil field is skipped, so a Reclaimer built without them decides from local
// refs alone.
type mergeProbe struct {
	// prMerged reports whether a merged pull request of branch had exactly head
	// as its head commit: everything HEAD holds was in that PR, so it is in the
	// base. False when it cannot tell (no gh, no network, not GitHub).
	prMerged func(ctx context.Context, checkout, branch, head string) bool
	// fetchBase refreshes the remote-tracking copy of the base branch, which is
	// where a merge made since this clone last fetched would appear.
	fetchBase func(ctx context.Context, checkout, base string)
	// memo remembers inBase answers; nil remembers nothing.
	memo *commitMemo
}

// headIsSafe reports whether HEAD's work is preserved somewhere other than this
// worktree. It is, when HEAD is
//
//   - on any remote ref (a branch that was pushed);
//   - an ancestor of the base branch (a fast-forward or merge-commit merge);
//   - already in the base branch by content (patchesOnBase): what a squash or
//     rebase merge leaves behind, the usual case once the PR branch is deleted
//     after merging, when no remote ref names HEAD and it is an ancestor of
//     nothing; or
//   - the head commit of a merged pull request (probe.prMerged).
//
// The content check sees the base as this clone last fetched it, so when it
// finds nothing the base is refreshed (probe.fetchBase) and checked once more.
// A false return means the work exists only here. An error means git could not
// say, and the caller holds the worktree.
//
// onRemote is whether a remote ref contains HEAD (refsContaining).
func headIsSafe(ctx context.Context, checkout, head, baseBranch string, onRemote bool, probe mergeProbe) (bool, error) {
	if onRemote {
		return true, nil
	}
	if merged, err := inBase(ctx, checkout, head, baseBranch, probe.memo); err != nil || merged {
		return merged, err
	}
	if probe.prMerged != nil {
		if branch, err := git(ctx, checkout, nil, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && branch != "" && probe.prMerged(ctx, checkout, branch, head) {
			return true, nil
		}
	}
	if probe.fetchBase != nil && baseBranch != "" {
		probe.fetchBase(ctx, checkout, baseBranch)
		return inBase(ctx, checkout, head, baseBranch, probe.memo)
	}
	return false, nil
}

// inBase reports whether head's work is in the base branch as this clone has
// it: head is an ancestor of it, or every change head makes is there by patch.
//
// The refs are resolved afresh every time (a fetch moves them); what is asked
// of the commits they resolve to is remembered in memo, because the answer is
// a property of those commits alone and the reaper asks it of the same pair
// up to four times a pass.
func inBase(ctx context.Context, checkout, head, baseBranch string, memo *commitMemo) (bool, error) {
	return inCommits(ctx, checkout, head, baseRefs(ctx, checkout, baseBranch), memo)
}

// inCommits is inBase against base commits already resolved.
func inCommits(ctx context.Context, checkout, head string, bases []string, memo *commitMemo) (bool, error) {
	if len(bases) == 0 {
		return false, nil
	}
	key := memo.key(ctx, checkout, head, bases)
	if merged, ok := memo.get(key); ok {
		return merged, nil
	}
	merged, err := inBaseUncached(ctx, checkout, head, bases)
	if err == nil {
		memo.put(key, merged)
	}
	return merged, err
}

func inBaseUncached(ctx context.Context, checkout, head string, bases []string) (bool, error) {
	for _, base := range bases {
		if _, err := git(ctx, checkout, nil, "merge-base", "--is-ancestor", head, base); err == nil {
			return true, nil
		}
	}
	for _, base := range bases {
		merged, err := patchesOnBase(ctx, checkout, base, head)
		if err != nil {
			return false, err
		}
		if merged {
			return true, nil
		}
	}
	return false, nil
}

// patchesOnBase reports whether head's changes are in base by patch, in either
// of the two shapes a merge that rewrites commits leaves:
//
//   - a rebase merge: every commit base..head has a commit on base with the
//     same patch-id (`git cherry` prints "-" for each such commit), or
//   - a squash merge: head's whole change since it forked from base is the
//     patch of one commit on base, which is what folding its commits into one
//     produces.
//
// Either way what is compared is the change itself, so it holds whatever the
// commits are called and wherever the PR branch has gone.
//
// It declines (false, so the worktree is held) whenever the comparison cannot
// be trusted: a branch that touches a binary file, whose patch-id does not see
// the bytes; a merge commit inside head, whose changes `git cherry` skips; or
// histories with no common ancestor.
func patchesOnBase(ctx context.Context, checkout, base, head string) (bool, error) {
	mb, err := git(ctx, checkout, nil, "merge-base", base, head)
	if err != nil || mb == "" {
		return false, nil
	}
	if binary, err := touchesBinary(ctx, checkout, mb, head); err != nil || binary {
		return false, err
	}
	merges, err := git(ctx, checkout, nil, "rev-list", "--merges", "--count", base+".."+head)
	if err != nil {
		return false, err
	}
	if merges == "0" {
		out, err := git(ctx, checkout, nil, "cherry", base, head)
		if err != nil {
			return false, err
		}
		if out != "" && !strings.Contains("\n"+out, "\n+ ") {
			return true, nil
		}
	}
	return squashedOnto(ctx, checkout, base, head, mb)
}

// touchesBinary reports whether the change from..to includes a binary file
// (numstat shows "-" for both counts).
func touchesBinary(ctx context.Context, checkout, from, to string) (bool, error) {
	out, err := git(ctx, checkout, nil, "diff", "--numstat", "--no-ext-diff", from, to)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "-\t-\t") {
			return true, nil
		}
	}
	return false, nil
}

// squashedOnto reports whether head's whole change against mb (its merge base
// with base) is the patch of one commit on base made since the fork: what a
// squash merge of head's branch produces. The base side is one `git log -p`
// piped through one `git patch-id`, bounded to maxSquashScan commits so a busy
// base is not scanned back to its root.
func squashedOnto(ctx context.Context, checkout, base, head, mb string) (bool, error) {
	diff, err := gitRaw(ctx, checkout, nil, "diff", "--no-ext-diff", mb, head)
	if err != nil {
		return false, err
	}
	if len(diff) == 0 {
		return false, nil
	}
	want, err := patchID(ctx, checkout, diff)
	if err != nil || want == "" {
		return false, err
	}
	log, err := gitRaw(ctx, checkout, nil, "log", "-p", "--no-merges", "--no-ext-diff", "--max-count="+strconv.Itoa(maxSquashScan), mb+".."+base)
	if err != nil {
		return false, err
	}
	ids, err := gitStdin(ctx, checkout, string(log), "patch-id", "--stable")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(ids, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == want {
			return true, nil
		}
	}
	return false, nil
}

// maxSquashScan bounds how many base commits since the fork are compared with
// the branch's whole diff. A branch whose squash is further back than this is
// reported unpushed, which only keeps the worktree.
const maxSquashScan = 1000

// patchID is the stable patch-id (`git patch-id --stable`) of a diff, or "" for
// an empty one.
func patchID(ctx context.Context, checkout string, diff []byte) (string, error) {
	out, err := gitStdin(ctx, checkout, string(diff), "patch-id", "--stable")
	if err != nil {
		return "", err
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return "", nil
	}
	return f[0], nil
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
	admin, ok := linkedAdminDir(checkout)
	if !ok {
		var err error
		if admin, err = git(ctx, checkout, nil, "rev-parse", "--absolute-git-dir"); err != nil {
			return nil, err
		}
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
	if symbolic, sha, ok := headFromFiles(admin); ok {
		if !symbolic {
			add(sha) // cat-file below drops it if it names no commit, as rev-parse did
		}
	} else if _, err := git(ctx, checkout, nil, "symbolic-ref", "-q", "HEAD"); err != nil {
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

// baseRefs resolves the refs HEAD may be merged into, remote first, to the
// commits they name now; a ref that does not exist, or names no commit, is
// left out. One for-each-ref reads both; a ref that names a tag is peeled by
// rev-parse, as `<ref>^{commit}` always was.
func baseRefs(ctx context.Context, checkout, base string) []string {
	if base == "" {
		return nil
	}
	want := []string{"refs/remotes/origin/" + base, "refs/heads/" + base}
	out, err := git(ctx, checkout, nil, append([]string{"for-each-ref", "--format=%(refname) %(objectname) %(objecttype)"}, want...)...)
	if err != nil {
		return nil
	}
	type named struct{ sha, kind string }
	found := map[string]named{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 3 {
			found[f[0]] = named{f[1], f[2]}
		}
	}
	var shas []string
	for _, ref := range want {
		n, ok := found[ref]
		switch {
		case !ok:
		case n.kind == "commit":
			if !slices.Contains(shas, n.sha) { // both name one commit: one answer
				shas = append(shas, n.sha)
			}
		default:
			if sha, err := git(ctx, checkout, nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
				shas = append(shas, sha)
			}
		}
	}
	return shas
}

// commitMemo remembers whether a commit's work is in a base, keyed by the
// repository and the commit ids involved, for commitMemoTTL: long enough to
// span a sweep's two phases, short enough that a change to the repository's
// diff configuration (which patch comparison reads) is soon seen. A nil memo
// remembers nothing.
type commitMemo struct {
	mu      sync.Mutex
	answers map[string]memoAnswer
	now     func() time.Time
}

type memoAnswer struct {
	merged bool
	at     time.Time
}

const (
	commitMemoTTL = 10 * time.Minute
	commitMemoCap = 4096
)

func (m *commitMemo) key(ctx context.Context, checkout, head string, bases []string) string {
	if m == nil {
		return ""
	}
	return commonDirKey(ctx, checkout) + "\x00" + head + "\x00" + strings.Join(bases, ",")
}

func (m *commitMemo) get(key string) (bool, bool) {
	if m == nil {
		return false, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.answers[key]
	if !ok || m.now().Sub(a.at) >= commitMemoTTL {
		return false, false
	}
	return a.merged, true
}

func (m *commitMemo) put(key string, merged bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.answers == nil || len(m.answers) >= commitMemoCap {
		m.answers = map[string]memoAnswer{}
	}
	m.answers[key] = memoAnswer{merged, m.now()}
}

// dataHold lists ignored files that are the user's data (a ./data directory,
// .env files, sqlite databases, a local postgres under .forge/hostinfra): not
// rebuildable, not generated, and not a copy of the source checkout's file
// (dataWalk). Only a person may delete them.
func dataHold(ctx context.Context, checkout string, nested []string) verdict {
	out, err := git(ctx, checkout, nil, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return verdict{ReasonUnverified, "could not list ignored files: " + err.Error()}
	}
	walk := newDataWalk(ctx, checkout, nested)
	for _, e := range strings.Split(out, "\x00") {
		if e != "" {
			walk.visit(e)
		}
	}
	if len(walk.out) == 0 {
		return verdict{}
	}
	shown := walk.out
	if len(shown) > 3 {
		shown = shown[:3]
	}
	detail := "ignored files that are not rebuildable: " + strings.Join(shown, ", ")
	if source := walk.source(); source != "" {
		// Say how to free it: these exist only here, and a copy identical to
		// the source checkout's would not hold it.
		detail += " (not in " + source + ", or different there)"
	}
	return verdict{ReasonData, detail}
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
