package cli

// `forge project checkouts --json` — main plus every worktree, as the
// Preview picker's source (doc §8.1).
//
// WHAT THE PICKER NEEDS, AND WHY IT IS THIS SHAPE. Preview renders an
// arbitrary checkout and diffs it against Live, so the user first has to
// choose one. The daemon cannot offer an arbitrary directory — `forge
// deploy_start` accepts ONLY a path this command returned, which is what
// keeps the UI from pointing forge at anything on the machine — so this list
// is also the allowlist.
//
// "main" IS origin/<main_ref>, NOT THE LOCAL BRANCH. That distinction is the
// whole reason the remote entry exists separately from the worktrees: a
// protected env is judged against what the repository's main holds, and a
// local `main` that has not been fetched for a week is a different tree. The
// remote entry is reported with no path, because there is nothing on disk to
// render it from until someone checks it out.
//
// GIT-ONLY, AND KEPT FAST. The picker opens on every visit to Preview, so
// the budget is tight. No tree hash by default — hashing one checkout costs
// ≈0.5s, and hashing every worktree would make the list unusable to answer a
// question nobody has asked yet. `--tree` hashes exactly ONE, the selected
// checkout, which is the only one whose cache key is about to be needed.
//
// MEASURED, on control-plane's checkout with 36 worktrees: 0.6–0.8s warm
// (4.1s on a cold FS cache). The doc's §8.4 figure of ≈0.1s with 22
// worktrees is the cost of `git worktree list` ALONE; the per-worktree dirty
// flag and ahead/behind counts the §8.1 contract also asks for are two more
// subprocesses each, and those dominate. Running them concurrently is what
// brought this down from 3.0s — see listCheckouts.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/devstack"
	"github.com/reliant-labs/forge/pkg/release"
)

// checkoutKind is what a listed checkout IS. Closed, and the two differ in
// the one way that matters to a consumer: a worktree has a path and can be
// rendered, a remote ref does not and cannot.
type checkoutKind string

const (
	// checkoutRemote is origin/<main_ref> — the reference point, not a
	// directory.
	checkoutRemote checkoutKind = "remote"
	// checkoutWorktree is a checkout on disk, including the primary one.
	checkoutWorktree checkoutKind = "worktree"
)

// checkoutDoc is one entry in the picker.
type checkoutDoc struct {
	// Label is what the picker shows: "main" for the remote, else the
	// branch, else the directory basename.
	Label string       `json:"label"`
	Kind  checkoutKind `json:"kind"`
	// Ref is the remote ref for the remote entry ("origin/main"); empty
	// for a worktree, which is named by Branch.
	Ref string `json:"ref,omitempty"`
	// Path is where the checkout is on disk. EMPTY for the remote entry,
	// and that is the field a consumer branches on: a checkout with no
	// path cannot be rendered or deployed from.
	Path string `json:"path,omitempty"`
	// Branch is the checked-out branch, empty on a detached HEAD.
	Branch string `json:"branch,omitempty"`
	// Head is the short commit.
	Head string `json:"head"`
	// Dirty means uncommitted changes. Always false for the remote entry.
	Dirty bool `json:"dirty"`
	// DevstackKey is forge's worktree key, "" on the primary checkout. It
	// is included so a session row can be matched to the port block the
	// stack actually holds (§7.4) — the same key `forge env devstack key`
	// prints.
	DevstackKey string `json:"devstack_key,omitempty"`
	// AheadOfMain and BehindMain count commits against the REMOTE main.
	// Both nil when the comparison could not be made (no origin, main not
	// fetched), which is distinct from zero: "in step with main" and "I
	// could not tell" must not render alike.
	AheadOfMain *int `json:"ahead_of_main,omitempty"`
	BehindMain  *int `json:"behind_main,omitempty"`
	// Tree is release.CaptureProvenance's tree hash, present only with
	// --tree and only for the SELECTED checkout. Empty means unhashed,
	// never "no content".
	Tree string `json:"tree,omitempty"`
	// Selected marks the checkout this command ran in.
	Selected bool `json:"selected,omitempty"`
}

// checkoutsDoc is what the command prints.
type checkoutsDoc struct {
	// MainRef is the remote ref everything is compared against, so a
	// consumer showing "4 ahead" can say ahead of WHAT.
	MainRef   string        `json:"main_ref"`
	Checkouts []checkoutDoc `json:"checkouts"`
}

// checkoutGitTimeout bounds each git call. The picker is interactive, so a
// wedged git must fail fast rather than hang the UI.
const checkoutGitTimeout = 10 * time.Second

// newProjectCheckoutsCmd is `forge project checkouts`.
func newProjectCheckoutsCmd() *cobra.Command {
	var (
		asJSON   bool
		withTree bool
	)

	cmd := &cobra.Command{
		Use:   "checkouts",
		Short: "List origin/main plus every git worktree — the Preview picker's source",
		Args:  cobra.NoArgs,
		Long: `List the checkouts of this project: ` + "`origin/<main>`" + ` plus every git worktree,
each with its branch, HEAD, dirty flag, dev-stack key, and how far it is from
main.

WHAT IT IS FOR. Preview renders an arbitrary checkout and diffs it against
Live, so something has to offer the choice. The reliant daemon runs this and
the picker shows the result — and because the daemon accepts only a path this
command returned, the list is also the allowlist that keeps the UI from
pointing forge at an arbitrary directory.

"main" MEANS ` + "`origin/<main>`" + `, NOT your local main branch. That is what a
protected environment is judged against, and a local main that has not been
fetched is a different tree. The remote entry carries no path, because there
is nothing on disk to render until someone checks it out.

GIT ONLY, AND FAST. No tree hash by default: hashing one checkout costs about
half a second, so hashing twenty would make the picker unusable to answer a
question nobody has asked yet. ` + "`--tree`" + ` hashes exactly one — the checkout you
are in — which is the only one whose cache key is about to be needed.

ahead/behind are OMITTED rather than zero when the comparison cannot be made
(no origin, or main never fetched). "In step with main" and "I could not tell"
are different answers.

Examples:
  ` + Name() + ` project checkouts
  ` + Name() + ` project checkouts --json          # the form the daemon's hook returns
  ` + Name() + ` project checkouts --json --tree   # plus the tree hash of this checkout`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := listCheckouts(cmd.Context(), projectDirForKCL(), withTree)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(doc)
			}
			writeCheckouts(cmd.OutOrStdout(), doc)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the checkouts as JSON")
	cmd.Flags().BoolVar(&withTree, "tree", false, "Also compute the tree hash of the checkout you are in (adds ~0.5s)")
	return cmd
}

// listCheckouts builds the picker's list.
func listCheckouts(ctx context.Context, projectDir string, withTree bool) (checkoutsDoc, error) {
	if _, isGit := gitPrefix(ctx, projectDir); !isGit {
		return checkoutsDoc{}, fmt.Errorf("%s is not a git checkout, so it has no worktrees to list.\n"+
			"  Preview renders a checkout, which needs a repository", projectDir)
	}
	mainRef := remoteMainRef(ctx, projectDir)
	doc := checkoutsDoc{MainRef: mainRef, Checkouts: []checkoutDoc{}}

	// The remote entry first, because it is the reference point every
	// other row's ahead/behind is relative to.
	if head, err := checkoutGit(ctx, projectDir, "rev-parse", "--short", mainRef); err == nil {
		doc.Checkouts = append(doc.Checkouts, checkoutDoc{
			Label: "main",
			Kind:  checkoutRemote,
			Ref:   mainRef,
			Head:  head,
		})
	}
	// A missing remote ref is NOT an error. A project with no origin, or
	// one whose main has never been fetched, still has worktrees worth
	// listing — and the ahead/behind fields below already say "I could
	// not tell" for that case.

	worktrees, err := listWorktrees(ctx, projectDir)
	if err != nil {
		return doc, err
	}
	selected := resolveCheckoutPath(ctx, projectDir)

	// PER-WORKTREE WORK RUNS CONCURRENTLY, and this is not premature.
	// Each worktree costs two git subprocesses (status, rev-list) and
	// they are independent, so in series the cost is linear in worktree
	// count: measured on control-plane's checkout, 36 worktrees took 3.0s
	// serially against the ≈0.1s §8.4 budget — the picker opens on every
	// visit to Preview, so that is the difference between usable and not.
	// Nearly all of it is process spawn and wait rather than CPU, which is
	// exactly the shape concurrency fixes.
	//
	// UNBOUNDED, deliberately, unlike the render path's 2-way bound. A
	// render is CPU-bound KCL that shares a machine with the dev stack;
	// these are short git reads that spend their time blocked on the
	// kernel, and capping them would reintroduce the serialization this
	// exists to remove. The count is bounded in practice by how many
	// worktrees a human has made.
	entries := make([]checkoutDoc, len(worktrees))
	var wg sync.WaitGroup
	for i, wt := range worktrees {
		wg.Add(1)
		go func(i int, wt worktreeEntry) {
			defer wg.Done()
			entry := describeWorktree(ctx, wt, mainRef)
			entry.Selected = entry.Path != "" && entry.Path == selected
			if withTree && entry.Selected {
				entry.Tree = checkoutTreeHash(ctx, entry.Path)
			}
			entries[i] = entry
		}(i, wt)
	}
	wg.Wait()
	// Written to a pre-sized slice by index rather than appended from the
	// goroutines, so the ORDER is git's (primary first, then creation
	// order) and not scheduling order. A picker whose rows moved between
	// openings would be unusable for a different reason.
	doc.Checkouts = append(doc.Checkouts, entries...)
	return doc, nil
}

// remoteMainRef is the ref "main" names: origin/<default branch>.
//
// Resolved from the remote's own HEAD rather than assumed to be "main",
// because a repository whose default branch is `master` or `trunk` would
// otherwise compare every worktree against a ref that does not exist and
// report "I could not tell" for all of them. The fallback is origin/main,
// which is right far more often than any alternative.
func remoteMainRef(ctx context.Context, projectDir string) string {
	if out, err := checkoutGit(ctx, projectDir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && out != "" {
		return out
	}
	return "origin/main"
}

// worktreeEntry is one `git worktree list --porcelain` record.
type worktreeEntry struct {
	path     string
	head     string
	branch   string
	detached bool
	// bare and prunable worktrees are skipped by the caller: neither can
	// be rendered, and offering one in the picker would hand the user a
	// choice that fails on selection.
	bare     bool
	prunable bool
}

// listWorktrees parses `git worktree list --porcelain`.
//
// The porcelain form is read rather than the human one because the human
// form's columns are ambiguous: a branch named like a sha, or a path with a
// space, both parse wrong. Porcelain is one `key value` per line with a blank
// line between records, which is stable across git versions.
func listWorktrees(ctx context.Context, projectDir string) ([]worktreeEntry, error) {
	out, err := checkoutGit(ctx, projectDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	var (
		entries []worktreeEntry
		current worktreeEntry
		have    bool
	)
	flush := func() {
		if have && !current.bare && !current.prunable {
			entries = append(entries, current)
		}
		current, have = worktreeEntry{}, false
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			current.path, have = value, true
		case "HEAD":
			current.head = value
		case "branch":
			current.branch = strings.TrimPrefix(value, "refs/heads/")
		case "detached":
			current.detached = true
		case "bare":
			current.bare = true
		case "prunable":
			current.prunable = true
		}
	}
	flush()
	return entries, nil
}

// describeWorktree fills in the fields that need a per-worktree git call:
// the dirty flag and the ahead/behind counts.
func describeWorktree(ctx context.Context, wt worktreeEntry, mainRef string) checkoutDoc {
	entry := checkoutDoc{
		Kind:   checkoutWorktree,
		Path:   wt.path,
		Branch: wt.branch,
		Head:   shortCommit(wt.head),
		Label:  worktreeLabel(wt),
		Dirty:  worktreeIsDirty(ctx, wt.path),
		// The devstack key is derived from the PATH, with no git call,
		// so it costs nothing and lets a session row be matched to the
		// port block its stack holds.
		DevstackKey: devstack.Worktree(wt.path),
	}
	// Counted from the worktree's own HEAD rather than its branch, so a
	// detached checkout still reports a position relative to main.
	if ahead, behind, ok := countAheadBehind(ctx, wt.path, mainRef, wt.head); ok {
		entry.AheadOfMain, entry.BehindMain = &ahead, &behind
	}
	return entry
}

// worktreeLabel is what the picker shows: the branch, else the directory
// basename for a detached checkout.
//
// The basename is the right fallback rather than the sha: a user recognises
// "forge-dsot-f7" and does not recognise "a7d63543", and the sha is already
// in the Head field for whoever needs it.
func worktreeLabel(wt worktreeEntry) string {
	if wt.branch != "" {
		return wt.branch
	}
	return filepath.Base(wt.path)
}

// worktreeIsDirty reports uncommitted changes.
//
// `status --porcelain` with untracked files INCLUDED, because an untracked
// file changes the tree hash and therefore the provenance a Preview render
// would record — a checkout reported clean whose render is not reproducible
// is the exact mismatch the dirty flag exists to prevent.
func worktreeIsDirty(ctx context.Context, path string) bool {
	out, err := checkoutGit(ctx, path, "status", "--porcelain")
	if err != nil {
		// A worktree git cannot stat is reported NOT dirty rather than
		// dirty: "dirty" is a claim about content, and a failed status
		// is a claim about nothing. The path is still listed, because
		// its existence is a fact git already gave us.
		return false
	}
	return strings.TrimSpace(out) != ""
}

// countAheadBehind is how far head is from mainRef, in both directions.
//
// `rev-list --left-right --count` answers both in ONE call, which matters
// because this runs per worktree and the picker's budget is ≈0.1s total. The
// ok return is false when the comparison is impossible (no origin, main never
// fetched) — reported as absent rather than as zero, because "in step" and
// "I could not tell" are different answers.
func countAheadBehind(ctx context.Context, path, mainRef, head string) (ahead, behind int, ok bool) {
	if head == "" {
		return 0, 0, false
	}
	out, err := checkoutGit(ctx, path, "rev-list", "--left-right", "--count", mainRef+"..."+head)
	if err != nil {
		return 0, 0, false
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, false
	}
	// left is main's side (commits this checkout is BEHIND by), right is
	// this checkout's (commits it is AHEAD by).
	behind, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, false
	}
	ahead, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false
	}
	return ahead, behind, true
}

// checkoutTreeHash is release.CaptureProvenance's tree hash for one
// checkout.
//
// It goes through CaptureProvenance rather than running `git write-tree`
// here, because the tree hash is a LEDGER value: it is what a release's
// provenance records and what Preview's cache is keyed by, and a second
// implementation could disagree with the one a build recorded — which would
// show as a cache that never hits, or worse, one that hits wrongly.
//
// An empty result means unhashed (a tree too large for the capture budget,
// F-16), never "no content". Preview falls back to HEAD plus the dirty flag
// for that checkout and disables caching for it.
func checkoutTreeHash(ctx context.Context, path string) string {
	p, err := release.CaptureProvenance(ctx, path, release.CaptureOptions{
		ForgeVersion: buildinfo.Version(),
		WorktreeKey:  devstack.Worktree(path),
		Host:         release.HostID(),
	})
	if err != nil {
		return ""
	}
	return p.Tree
}

// resolveCheckoutPath is the toplevel of the checkout this command ran in,
// so the matching entry can be marked selected.
//
// Asked of git rather than compared as strings, because macOS's /var →
// /private/var symlink makes a path comparison fail on exactly the machines
// this runs on: `git worktree list` reports the resolved path and the project
// dir may be the symlinked one.
func resolveCheckoutPath(ctx context.Context, projectDir string) string {
	if out, err := checkoutGit(ctx, projectDir, "rev-parse", "--show-toplevel"); err == nil {
		return out
	}
	return ""
}

// checkoutGit runs one git command in dir, trimmed.
func checkoutGit(ctx context.Context, dir string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, checkoutGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = dir
	// Stderr is discarded on purpose: every caller here treats a failure
	// as "could not tell" and reports that in the document, so git's
	// message would only appear as noise on a successful command's
	// output.
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// writeCheckouts is the human form: one line per checkout.
func writeCheckouts(w io.Writer, doc checkoutsDoc) {
	fmt.Fprintf(w, "checkouts (main is %s)\n", doc.MainRef)
	for _, c := range doc.Checkouts {
		marker := " "
		if c.Selected {
			marker = "*"
		}
		// The LABEL is not unique across kinds — the remote row is
		// "main" and the primary checkout is usually on a branch of
		// that name — so the kind is printed on every line rather than
		// leaving two rows that read identically.
		fmt.Fprintf(w, "%s %-24s %-10s %s", marker, c.Label, c.Head, c.Kind)
		if c.Dirty {
			fmt.Fprintf(w, " dirty")
		}
		if c.AheadOfMain != nil && c.BehindMain != nil && (*c.AheadOfMain > 0 || *c.BehindMain > 0) {
			fmt.Fprintf(w, " (+%d/-%d)", *c.AheadOfMain, *c.BehindMain)
		}
		if c.Tree != "" {
			fmt.Fprintf(w, " tree %s", shortCommit(c.Tree))
		}
		fmt.Fprintln(w)
		if c.Path != "" {
			fmt.Fprintf(w, "    %s\n", c.Path)
		}
	}
}

// THE ORDER IS DELIBERATE, which is worth saying because the absence of a
// sort in a list command reads as an oversight. `git worktree list` reports
// the PRIMARY checkout first and then the others in creation order, which is
// more useful to a human than alphabetical: the primary is where most work
// happens, and creation order roughly tracks recency. listCheckouts prepends
// the remote entry, so "main, then the primary checkout, then the worktrees
// oldest-first" is the whole rule.
