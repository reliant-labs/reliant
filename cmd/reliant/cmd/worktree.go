// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// `reliant worktree` is this machine's view of the daemon's worktree reclaim
// (internal/worktreereclaim/sweep.go): what it found, what it would or did
// reclaim, the decisions a person makes about the worktrees it only
// proposes, and the restore of any capture. It reads and writes the daemon's
// state directory (~/.reliant/reclaim) and the repositories' capture refs;
// it never deletes a worktree itself. Deletion is the daemon's alone, under
// its admission lease.

func init() { registerCommand(newWorktreeCmd) }

func newWorktreeCmd(Deps) *cobra.Command {
	wt := &cobra.Command{
		Use:   "worktree",
		Short: "Inspect and restore the worktrees the daemon reclaims",
		Long: `The daemon reclaims the disk of linked git worktrees nobody is using, on
every machine it runs on: worktrees Reliant made and ones agents made with
` + "`git worktree add`" + ` alike. Before it deletes one it commits everything
git can see of it (HEAD, the staged index, unstaged edits, untracked files,
commits only its reflog reached) into a local ref of the repository,
refs/reliant/reclaimed/<key>/<time>, and checks that ref against the files
immediately before deleting them.

A clean worktree whose HEAD is on a remote is reclaimed after a day idle.
One with work only it holds is never reclaimed: after about five days idle
it is proposed in the report, for a person to free by hand.

RELIANT_WORKTREE_RECLAIM sets the mode: off, dry-run (report only) or on.`,
	}
	reclaim := &cobra.Command{Use: "reclaim", Short: "The daemon's last reclaim report, and decisions about proposed worktrees"}
	reclaim.AddCommand(newWorktreeReportCmd(), newWorktreeScanCmd(), newWorktreeDecideCmd(worktreereclaim.DecisionApprove), newWorktreeDecideCmd(worktreereclaim.DecisionDecline),
		newWorktreeProposalsCmd(), newWorktreeNoteCmd())
	wt.AddCommand(reclaim, newWorktreeReclaimedCmd(), newWorktreeRestoreCmd())
	return wt
}

func stateDirFlag(c *cobra.Command, dir *string) {
	c.Flags().StringVar(dir, "state-dir", "", "the reclaim state directory (default ~/.reliant/reclaim)")
}

func newWorktreeReportCmd() *cobra.Command {
	var asJSON bool
	var class, stateDir string
	c := &cobra.Command{
		Use:   "report",
		Short: "Show the daemon's last reclaim report",
		Long: `Show the last pass's report: every linked worktree found, its class
(auto_safe, needs_approval, held), what was done with it and why, and its
size. --class needs_approval lists the worktrees awaiting a decision, with
the key and fingerprint ` + "`approve`" + ` and ` + "`decline`" + ` take.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := worktreereclaim.ReadSweepReport(stateDir)
			if errors.Is(err, os.ErrNotExist) {
				return errors.New("no reclaim report yet: the daemon writes one each pass (RELIANT_WORKTREE_RECLAIM=dry-run or on), or run `reliant worktree reclaim scan`")
			}
			if err != nil {
				return err
			}
			if class != "" {
				kept := rep.Entries[:0]
				for _, e := range rep.Entries {
					if string(e.Class) == class {
						kept = append(kept, e)
					}
				}
				rep.Entries = kept
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), rep)
			}
			printSweepReport(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	c.Flags().StringVar(&class, "class", "", "only entries of this class: auto_safe, needs_approval or held")
	stateDirFlag(c, &stateDir)
	return c
}

func newWorktreeScanCmd() *cobra.Command {
	var asJSON bool
	var stateDir, roots string
	c := &cobra.Command{
		Use:   "scan",
		Short: "Run a dry-run pass now and write its report",
		Long: `Run one dry-run pass in this process: discover every linked worktree,
classify it, and write the report and journal the daemon's passes write. It
captures nothing and deletes nothing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := worktreereclaim.DefaultRoot()
			if err != nil {
				return err
			}
			r := worktreereclaim.NewReclaimer(root)
			rep := r.SweepMachine(cmd.Context(), worktreereclaim.SweepOptions{
				Mode: worktreereclaim.SweepDryRun, Roots: worktreereclaim.ParseSweepRoots(roots), StateDir: stateDir, Trigger: "cli",
			})
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), rep)
			}
			printSweepReport(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	c.Flags().StringVar(&roots, "roots", "", "directories to walk, comma-separated path[:depth] (default: the daemon's)")
	stateDirFlag(c, &stateDir)
	return c
}

func newWorktreeDecideCmd(decision worktreereclaim.Decision) *cobra.Command {
	var stateDir string
	verb := string(decision)
	c := &cobra.Command{
		Use:   verb + " <key>...",
		Short: map[worktreereclaim.Decision]string{worktreereclaim.DecisionApprove: "Approve reclaiming proposed worktrees", worktreereclaim.DecisionDecline: "Decline reclaiming proposed worktrees"}[decision],
		Long: `Record a person's decision about worktrees the last report proposes
(class needs_approval), by key. The decision binds to the worktree's
fingerprint in that report: one that changes after it (a commit, an edit, a
new file) is proposed again.

Approving is not available yet: nothing can show that an approval came from
a person rather than an agent running as the same user, so the daemon never
deletes a proposed worktree, and approve refuses. Free one yourself, after
checking its work, with ` + "`git worktree remove`" + `. Decline records that a
proposed worktree is to be kept; an agent's shell is refused that too.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if decision == worktreereclaim.DecisionApprove {
				// Refused whoever runs it: any check made here (an environment
				// marker, a tty) is one an agent's shell can defeat, and the
				// approvals file it would write is one an agent can write
				// directly. The sweep does not act on approvals for the same
				// reason (SweepOptions.ApprovalsVerified).
				return fmt.Errorf("approving a reclaim is not available: nothing can verify the approval is a person's, so the daemon never deletes a proposed worktree; to free one, check its work and run `git worktree remove` yourself (keys: %s)", strings.Join(args, " "))
			}
			agent := os.Getenv(daemonpolicy.AgentShellEnv) != "" //nolint:forbidigo // the marker the daemon sets on agent shells (daemonpolicy.AgentShellEnv), a process signal, not configuration
			if agent {
				return fmt.Errorf("an agent cannot %s reclaiming a worktree: this is the user's decision, so ask them to run `reliant worktree reclaim %s %s` in their own terminal", verb, verb, strings.Join(args, " "))
			}
			by := "cli"
			if u, err := user.Current(); err == nil {
				by = "cli:" + u.Username
			}
			got, err := worktreereclaim.RecordDecisions(cmd.Context(), stateDir, decision, args, by)
			if err != nil {
				return err
			}
			for _, d := range got {
				fmt.Fprintf(cmd.OutOrStdout(), "%sd %s (%s)\n", strings.TrimSuffix(verb, "e"), d.Path, d.Key)
			}
			return nil
		},
	}
	stateDirFlag(c, &stateDir)
	return c
}

func newWorktreeProposalsCmd() *cobra.Command {
	var stateDir string
	c := &cobra.Command{
		Use:   "proposals",
		Short: "Print the pending proposals as JSON",
		Long: `Print the worktrees the last pass proposes for removal and that are not yet
reclaimed, as JSON: each one's key, fingerprint, path, branch, what its unsaved
work is, how long it has been idle, any reviewing agent's note and any decision
already recorded. This is what the machine's Inbox item lists.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			set, err := worktreereclaim.PendingProposals(stateDir)
			if errors.Is(err, os.ErrNotExist) {
				return writeJSON(cmd.OutOrStdout(), worktreereclaim.ProposalSet{Proposals: []worktreereclaim.Proposal{}})
			}
			if err != nil {
				return err
			}
			if set.Proposals == nil {
				set.Proposals = []worktreereclaim.Proposal{}
			}
			return writeJSON(cmd.OutOrStdout(), set)
		},
	}
	stateDirFlag(c, &stateDir)
	return c
}

func newWorktreeNoteCmd() *cobra.Command {
	var stateDir, fingerprint, fromFile string
	c := &cobra.Command{
		Use:   "note [<key> <text>]",
		Short: "Attach a reviewing note to proposed worktrees",
		Long: `Attach a short assessment to a proposal: what the unsaved work looks like,
how recently it was touched, whether it looks abandoned. The person sees it on
the machine's Inbox item next to approve and decline. A note binds to the
proposal's fingerprint (--fingerprint, from ` + "`proposals`" + `), so a note about a
state that has since changed is refused. It decides nothing.

--from <file> (or - for stdin) records many at once: a JSON list of
{"key", "fingerprint", "text"}.`,
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var notes []worktreereclaim.Note
			switch {
			case fromFile != "":
				raw, err := readNotesInput(cmd.InOrStdin(), fromFile)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(raw, &notes); err != nil {
					return fmt.Errorf("--from: a JSON list of {key, fingerprint, text}: %w", err)
				}
			case len(args) == 2 && fingerprint != "":
				notes = []worktreereclaim.Note{{Key: args[0], Fingerprint: fingerprint, Text: args[1]}}
			default:
				return errors.New("name <key> <text> with --fingerprint, or --from <file>")
			}
			by := "cli"
			if os.Getenv(daemonpolicy.AgentShellEnv) != "" { //nolint:forbidigo // the marker the daemon sets on agent shells (daemonpolicy.AgentShellEnv), a process signal, not configuration
				by = "agent"
			}
			got, err := worktreereclaim.RecordNotes(cmd.Context(), stateDir, notes, by)
			for _, n := range got {
				fmt.Fprintf(cmd.OutOrStdout(), "noted %s\n", n.Key)
			}
			return err
		},
	}
	c.Flags().StringVar(&fingerprint, "fingerprint", "", "the proposal's fingerprint the note is about")
	c.Flags().StringVar(&fromFile, "from", "", "read notes from this JSON file (- for stdin)")
	stateDirFlag(c, &stateDir)
	return c
}

// readNotesInput reads `note --from`'s JSON (- for stdin), refusing more than
// worktreereclaim.MaxNotesInputBytes before it is all in memory.
func readNotesInput(stdin io.Reader, from string) ([]byte, error) {
	r := stdin
	if from != "-" {
		f, err := os.Open(from)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, worktreereclaim.MaxNotesInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > worktreereclaim.MaxNotesInputBytes {
		return nil, fmt.Errorf("--from: more than %d bytes of notes", worktreereclaim.MaxNotesInputBytes)
	}
	return raw, nil
}

// reclaimedCapture is one row of `reliant worktree reclaimed`.
type reclaimedCapture struct {
	Ref        string    `json:"ref"`
	Repo       string    `json:"repo"`
	Path       string    `json:"path"`
	Branch     string    `json:"branch,omitempty"`
	Class      string    `json:"class,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
	Clean      bool      `json:"clean"`
	Orphans    int       `json:"orphan_commits,omitempty"`
}

func newWorktreeReclaimedCmd() *cobra.Command {
	var asJSON bool
	var stateDir, repo string
	c := &cobra.Command{
		Use:   "reclaimed",
		Short: "List the captures of reclaimed worktrees",
		Long: `List every capture (refs/reliant/reclaimed/...) in the repositories the
reclaim journal names, or in --repo, newest first. A capture is kept until it
is restored or deleted by hand; none expires.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repos := map[string]bool{}
			if repo != "" {
				repos[repo] = true
			} else {
				lines, err := worktreereclaim.ReadJournal(stateDir)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				for _, l := range lines {
					if l.CaptureRef != "" && l.CommonDir != "" {
						repos[l.CommonDir] = true
					}
				}
			}
			var rows []reclaimedCapture
			for r := range repos {
				infos, err := worktreereclaim.ListCaptures(cmd.Context(), r, "")
				if err != nil {
					continue
				}
				for _, i := range infos {
					rows = append(rows, reclaimedCapture{Ref: i.Ref, Repo: r, Path: i.Meta.Path, Branch: i.Meta.Branch, Class: i.Meta.Class,
						CapturedAt: i.Meta.CapturedAt, Clean: i.Clean, Orphans: len(i.Orphans)})
				}
			}
			sort.Slice(rows, func(a, b int) bool { return rows[a].CapturedAt.After(rows[b].CapturedAt) })
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), rows)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "CAPTURED\tPATH\tBRANCH\tWORK\tREF\tREPO")
			for _, r := range rows {
				work := "clean"
				if !r.Clean {
					work = "unsaved work"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.CapturedAt.Local().Format(time.DateTime), r.Path, r.Branch, work, r.Ref, r.Repo)
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the list as JSON")
	c.Flags().StringVar(&repo, "repo", "", "list this repository's captures (a checkout or git dir)")
	stateDirFlag(c, &stateDir)
	return c
}

func newWorktreeRestoreCmd() *cobra.Command {
	var stateDir, repo, to, branch string
	c := &cobra.Command{
		Use:   "restore <capture-ref | reclaimed path>",
		Short: "Recreate a reclaimed worktree from its capture",
		Long: `Recreate a reclaimed worktree as a new linked worktree: HEAD at the
captured commit, the files on disk as they were (untracked files untracked
again), and the index as it was staged. Commits only the old worktree's
reflog reached stay reachable from the capture ref, which is kept.

Name the capture by its ref (with --repo, unless the journal or a tombstone
names its repository), or by the path the worktree had. It is restored at
that path unless --to names another; the path must not exist. The branch is
reattached when that changes no file, otherwise the worktree is left on a
detached HEAD and the output says why.

Plain git does the same:
  git -C <repo> worktree add --detach --no-checkout <dir> <ref>^1
  git -C <dir> read-tree --reset -u <ref>^{tree}
  git -C <dir> read-tree <ref>^2^{tree}`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, rrepo, err := resolveCapture(cmd.Context(), stateDir, args[0], repo)
			if err != nil {
				return err
			}
			res, err := worktreereclaim.RestoreCapture(cmd.Context(), worktreereclaim.RestoreOptions{Repo: rrepo, Ref: ref, Dest: to, Branch: branch})
			if err != nil {
				return err
			}
			_ = os.Remove(res.Path + worktreereclaim.TombstoneSuffix)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "restored %s at %s\n", ref, res.Path)
			if res.Branch != "" {
				fmt.Fprintf(out, "on branch %s\n", res.Branch)
			} else {
				fmt.Fprintf(out, "on a detached HEAD at %s: %s\n", res.Head, res.BranchNote)
			}
			if len(res.Orphans) > 0 {
				fmt.Fprintf(out, "commits only the old worktree's reflog reached (kept reachable from %s): %s\n", ref, strings.Join(res.Orphans, " "))
			}
			return nil
		},
	}
	c.Flags().StringVar(&repo, "repo", "", "the capture's repository (a checkout or git dir)")
	c.Flags().StringVar(&to, "to", "", "restore into this directory instead of the original path")
	c.Flags().StringVar(&branch, "branch", "", "put the restored worktree on this branch (default: the one it was on)")
	stateDirFlag(c, &stateDir)
	return c
}

// resolveCapture turns a capture ref or a reclaimed path into the ref and
// its repository: from --repo, a tombstone beside the path, or the journal.
func resolveCapture(ctx context.Context, stateDir, arg, repo string) (ref, repoOut string, err error) {
	isRef := strings.HasPrefix(arg, worktreereclaim.CaptureRefRoot)
	if isRef && repo != "" {
		return arg, repo, nil
	}
	if !isRef {
		abs, aerr := filepath.Abs(arg)
		if aerr == nil {
			if b, rerr := os.ReadFile(abs + worktreereclaim.TombstoneSuffix); rerr == nil {
				var ts worktreereclaim.Tombstone
				if json.Unmarshal(b, &ts) == nil && ts.Ref != "" {
					return ts.Ref, firstNonEmpty(repo, ts.CommonDir), nil
				}
			}
			arg = abs
		}
	}
	lines, jerr := worktreereclaim.ReadJournal(stateDir)
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if l.Outcome != worktreereclaim.OutcomeReclaimed || l.CaptureRef == "" {
			continue
		}
		if (isRef && l.CaptureRef == arg) || (!isRef && filepath.Clean(l.Path) == filepath.Clean(arg)) {
			return l.CaptureRef, firstNonEmpty(repo, l.CommonDir), nil
		}
	}
	if !isRef && repo != "" {
		// No record: the newest capture of that path in the named repository.
		if infos, lerr := worktreereclaim.ListCaptures(ctx, repo, ""); lerr == nil {
			for _, i := range infos {
				if filepath.Clean(i.Meta.Path) == filepath.Clean(arg) {
					return i.Ref, repo, nil
				}
			}
		}
	}
	if jerr != nil && !errors.Is(jerr, os.ErrNotExist) {
		return "", "", jerr
	}
	if isRef {
		return "", "", fmt.Errorf("no record of %s: name its repository with --repo", arg)
	}
	return "", "", fmt.Errorf("no reclaimed worktree is recorded at %s: name its capture ref (reliant worktree reclaimed)", arg)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func printSweepReport(w io.Writer, rep worktreereclaim.SweepReport) {
	t := rep.Totals
	fmt.Fprintf(w, "mode %s, pass finished %s: %d worktrees; auto-safe %d (%s), needs approval %d (%s), held %d, reclaimed %d (%s freed)\n\n",
		rep.Mode, rep.FinishedAt.Local().Format(time.DateTime), t.Seen, t.AutoSafe, humanBytes(t.AutoSafeBytes),
		t.NeedsApproval, humanBytes(t.NeedsApprovalBytes), t.Held, t.Reclaimed, humanBytes(t.ReclaimedBytes))
	for _, b := range rep.Blockers {
		fmt.Fprintf(w, "BLOCKED: %s\n", b)
	}
	if len(rep.Blockers) > 0 {
		fmt.Fprintln(w)
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "CLASS\tOUTCOME\tSIZE\tIDLE\tPATH\tKEY\tWHY")
	for _, e := range rep.Entries {
		why := string(e.Reason)
		if e.Detail != "" {
			why = strings.TrimPrefix(why+": "+firstLine(e.Detail), ": ")
		}
		idle := ""
		if e.IdleSeconds > 0 {
			idle = (time.Duration(e.IdleSeconds) * time.Second).Round(time.Hour).String()
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Class, e.Outcome, humanBytes(e.Bytes), idle, e.Path, e.Key, why)
	}
	_ = tw.Flush()
	if len(rep.Clones) > 0 {
		fmt.Fprintf(w, "\n%d main checkouts and clones found (never reclaimed)\n", len(rep.Clones))
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func humanBytes(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.0fK", float64(n)/(1<<10))
	}
}
