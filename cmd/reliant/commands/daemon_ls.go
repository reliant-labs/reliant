// Copyright (c) 2025 Reliant Labs
package commands

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
	"github.com/spf13/cobra"
)

// newDaemonLsCmd builds `reliant daemon ls`.
//
// The question it answers is "what daemons are alive on this machine", which
// nothing could answer before. `daemon status` reports on ONE data directory —
// whichever the flags resolve to — so a daemon belonging to another worktree,
// another account or another backend was invisible to it, and the usual next
// move was to grep the process table. That does not work: liveness was found in
// practice to include a daemon running 29 hours out of a `go run` temp binary
// at /var/folders/.../exe/reliant, which matches no name pattern anyone would
// think to write.
//
// So liveness here is the flock on each instance's daemon.lock, taken
// non-blocking and released at once. The kernel drops an advisory lock when its
// holder dies, whatever killed it, so the answer cannot go stale, cannot be
// fooled by PID reuse, and does not depend on what the binary is called.
//
// This command REPORTS. It never kills a process, never clears a record, and
// never touches a lock file — a diagnostic that mutates what it is diagnosing
// is how a healthy sibling stack's daemon gets reaped.
func newDaemonLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List every daemon instance on this machine",
		Long: `Lists every daemon instance directory under ~/.reliant/instances and reports,
for each, whether a daemon is alive in it and what its runtime record says.

Liveness comes from the instance's advisory lock, not from the runtime record
and not from the process table: the kernel releases the lock when the holding
process dies, so RUNNING cannot be stale and does not depend on what the daemon
binary is named or where it lives.

RUNNING and the record are reported separately because they can disagree, and
the disagreement is the diagnosis. A running instance with no record is a daemon
that died before publishing one, or one still starting up. A record with no
running daemon is a leftover from a process that is gone.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := daemonstate.DefaultRoot()
			if err != nil {
				return fmt.Errorf("resolving instances directory: %w", err)
			}
			instances, err := daemonstate.List(root)
			if err != nil {
				return fmt.Errorf("listing daemon instances: %w", err)
			}

			out := cmd.OutOrStdout()
			if len(instances) == 0 {
				fmt.Fprintf(out, "No daemon instances found under %s\n", root)
				return nil
			}
			printDaemonInstances(out, instances, root, time.Now().UTC())
			return nil
		},
	}
}

func printDaemonInstances(w io.Writer, instances []daemonstate.Instance, root string, now time.Time) {
	fmt.Fprintf(w, "%s\n\n", root)

	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "RUNNING\tPID\tSTREAM\tUPTIME\tINSTANCE")
	for _, instance := range instances {
		running := "-"
		if instance.Locked {
			running = "yes"
		}

		pid, stream, uptime := "-", "-", "-"
		switch {
		case instance.RecordErr != nil:
			stream = "unreadable"
		case instance.HasRecord:
			pid = fmt.Sprintf("%d", instance.Record.PID)
			stream = describeInstanceStream(instance.Record, now)
			if !instance.Record.StartedAt.IsZero() {
				uptime = now.Sub(instance.Record.StartedAt).Round(time.Second).String()
			}
		case instance.Locked:
			// Locked with no record: a daemon is alive but has not published
			// yet, or died between claiming and writing.
			stream = "no record"
		}

		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", running, pid, stream, uptime, instance.Slug)
	}
	table.Flush()

	for _, instance := range instances {
		if instance.RecordErr != nil {
			fmt.Fprintf(w, "\n%s: %v\n", instance.Slug, instance.RecordErr)
		}
	}

	// A record stamped with an instance other than the directory it sits in is
	// a record written by a daemon that believed it was somewhere else. Nothing
	// should ever produce one, and a reader that trusted it would act on the
	// wrong daemon — so it is called out rather than rendered as an ordinary
	// row.
	for _, instance := range instances {
		if instance.HasRecord && instance.Record.Instance != "" && instance.Record.Instance != instance.Slug {
			fmt.Fprintf(w, "\nWARNING: the record in %s claims instance %s\n", instance.Slug, instance.Record.Instance)
		}
	}
}

// describeInstanceStream renders the stream state, flagging an established
// stream that is flapping. "connected" alone is misleading on a stream that has
// reconnected four times in a minute: it is true at the instant of the read and
// says nothing about whether the daemon can carry a long run.
func describeInstanceStream(state daemonstate.State, now time.Time) string {
	if state.Stream == daemonstate.StreamUnknown {
		return "unknown"
	}
	if state.Stream.Established() && !state.Stable(now) {
		return string(state.Stream) + " (flapping)"
	}
	return string(state.Stream)
}
