// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/idlereclaim"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// `reliant storage idle`: this machine's idle reclaim (internal/idlereclaim),
// the daemon's lifecycle of the per-chat scratch directories and its
// node_modules strip of idle worktrees. `report` reads the daemon's last
// pass; `scan` runs a dry-run pass in this process. Nothing here removes
// anything: only the daemon holds the admission lease a removal needs.

func newStorageCmd() *cobra.Command {
	storage := &cobra.Command{
		Use:   "storage",
		Short: "Inspect what the daemon reclaims of this machine's disk",
	}
	idle := &cobra.Command{
		Use:   "idle",
		Short: "The idle reclaim of scratch directories and idle worktrees' node_modules",
		Long: `The daemon frees the disk of directories nobody is using, in two places:

  Scratch directories (~/.reliant/scratch/<chat>/), which the system prompt
  gives every chat. After 48h unused (no file changed in it and no daemon work
  named a path in it) and with no process using it, its node_modules, its
  build output (dist, .next, out, build, .turbo, .vite ... beside a
  package.json; target beside a Cargo.toml) and its clean, pushed, unlocked
  git worktrees are removed. After 7 days unused, also counted from its chat's
  last tool call, the whole directory is removed, unless a git working tree in
  it holds work that exists nowhere else. A .keep file at the top of a scratch
  directory keeps it and everything in it.

  Linked worktrees under ~/.reliant/worktrees that are kept for another
  reason (uncommitted or unpushed work, a chat still bound to them): after 72h
  unused, their git-ignored node_modules are removed. Nothing else in them is
  touched.

A command that fails in a directory whose node_modules or build output was
removed gets a note saying so; reinstall with the project's package manager.

RELIANT_IDLE_RECLAIM sets the mode: auto (the default: it runs wherever the
storage reaper runs, RELIANT_STORAGE_REAPER), on, dry-run or off. The
thresholds are RELIANT_IDLE_RECLAIM_SCRATCH_STRIP_IDLE,
RELIANT_IDLE_RECLAIM_SCRATCH_REMOVE_IDLE and
RELIANT_IDLE_RECLAIM_WORKTREE_NODE_MODULES_IDLE.`,
	}
	idle.AddCommand(newStorageIdleReportCmd(), newStorageIdleScanCmd())
	storage.AddCommand(idle)
	return storage
}

func newStorageIdleReportCmd() *cobra.Command {
	var asJSON bool
	var stateDir string
	c := &cobra.Command{
		Use:   "report",
		Short: "Show the daemon's last idle reclaim pass",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := idleStateDir(stateDir)
			if err != nil {
				return err
			}
			rep, err := idlereclaim.ReadReport(dir)
			if errors.Is(err, os.ErrNotExist) {
				return errors.New("no idle reclaim report yet: the daemon writes one each pass where it runs (RELIANT_IDLE_RECLAIM), or run `reliant storage idle scan`")
			}
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), rep)
			}
			printIdleReport(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	stateDirFlag(c, &stateDir)
	return c
}

func newStorageIdleScanCmd() *cobra.Command {
	var asJSON, skipProcesses bool
	var stateDir string
	c := &cobra.Command{
		Use:   "scan",
		Short: "Run a dry-run idle reclaim pass now and print what it would remove",
		Long: `Run one dry-run pass in this process with the daemon's thresholds and its
record of use, and print what it would remove and why it would keep the
rest. It removes nothing and does not replace the daemon's report.

A process can only inspect another process of the same user when that one
allows it, and the daemon does not, so from a shell every directory the
daemon holds a file in looks in use. --skip-process-check leaves the process
check out of the scan to estimate what a pass would free; the daemon's own
passes always make it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadRootConfig(cmd)
			if err != nil {
				return err
			}
			r, err := idlereclaim.NewReclaimer(idleReclaimConfig(cfg))
			if err != nil {
				return err
			}
			dir, err := idleStateDir(stateDir)
			if err != nil {
				return err
			}
			// The daemon's persisted use logs, read and never written here.
			wtRoot, _ := worktreereclaim.DefaultRoot()
			r.ScratchUse = worktreereclaim.NewUseLog(r.ScratchRoot, filepath.Join(dir, idlereclaim.ScratchUseFile), time.Now).LastUse
			r.ChatUse = worktreereclaim.NewUseLog(r.ScratchRoot, filepath.Join(dir, idlereclaim.ScratchChatFile), time.Now).LastUse
			r.WorktreeUse = worktreereclaim.NewUseLog(wtRoot, filepath.Join(dir, worktreereclaim.UseFile), time.Now).LastUse
			r.WorktreeSweepOn = worktreeReclaimConfig(cfg).Mode == worktreereclaim.SweepOn
			r.StateDir = "" // a scan does not replace the daemon's report
			r.Records = nil
			if skipProcesses {
				r.ProcessUse = func(context.Context, []string) error { return nil }
			}
			rep := r.Pass(cmd.Context(), true, "cli")
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), rep)
			}
			printIdleReport(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	c.Flags().BoolVar(&skipProcesses, "skip-process-check", false, "estimate without the process check, which a shell cannot make against the daemon's own process")
	stateDirFlag(c, &stateDir)
	return c
}

func idleStateDir(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	return worktreereclaim.DefaultStateDir()
}

func printIdleReport(w io.Writer, rep idlereclaim.Report) {
	verb := "freed"
	if rep.DryRun {
		verb = "would free"
	}
	fmt.Fprintf(w, "Idle reclaim (%s, mode %s%s) %s\n", rep.Trigger, rep.Mode, map[bool]string{true: ", dry run"}[rep.DryRun], rep.FinishedAt.Local().Format(time.RFC3339))
	fmt.Fprintf(w, "Scratch %s: strip after %s unused, remove after %s\n", rep.ScratchRoot, rep.ScratchStripIdle, rep.ScratchRemoveIdle)
	printIdleEntries(w, rep.Scratch)
	fmt.Fprintf(w, "\nWorktrees %s: node_modules after %s unused (only those idle with node_modules are listed)\n", rep.WorktreeRoot, rep.WorktreeNodeModulesIdle)
	printIdleEntries(w, rep.Worktrees)
	t := rep.Totals
	fmt.Fprintf(w, "\nScratch: %d directories, %d removed whole, %d stripped, %s %s\n", t.ScratchDirs, t.ScratchRemoved, t.ScratchStripped, verb, idlereclaim.HumanBytes(t.ScratchBytes))
	fmt.Fprintf(w, "Worktrees: %d stripped of node_modules, %s %s\n", t.WorktreesStripped, verb, idlereclaim.HumanBytes(t.WorktreeBytes))
	fmt.Fprintf(w, "Total %s %s\n", verb, idlereclaim.HumanBytes(t.ScratchBytes+t.WorktreeBytes))
}

func printIdleEntries(w io.Writer, entries []idlereclaim.Entry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  ACTION\tIDLE\tBYTES\tPATH\tDETAIL")
	for _, e := range entries {
		detail := e.Reason
		if len(e.Removed) > 0 {
			detail = "removes " + strings.Join(e.Removed, ", ")
			if len(e.Held) > 0 {
				detail += "; keeps " + strings.Join(e.Held, "; ")
			}
		}
		fmt.Fprintf(tw, "  %s\t%dh\t%s\t%s\t%s\n", e.Action, e.IdleHours, idlereclaim.HumanBytes(e.Bytes), e.Path, detail)
	}
	_ = tw.Flush()
}
