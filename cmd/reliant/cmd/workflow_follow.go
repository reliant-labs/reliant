// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/execfollow"
	"github.com/reliant-labs/reliant/sdk/go/reliant"
)

// followFlags are the follow-related flags shared by `workflow follow` and
// `workflow run --follow`.
type followFlags struct {
	hooks      []string
	timeout    time.Duration
	interval   time.Duration
	tail       bool
	exitOnGate bool
}

func (f *followFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&f.hooks, "hook", nil,
		"Exec hook 'on=<event> cmd=<shell>' (repeatable); event is one of node_started|node_completed|node_failed|workflow_completed|workflow_failed|question|approval|any")
	cmd.Flags().DurationVar(&f.timeout, "timeout", 0, "Stop following after this long (exit code 2); 0 means no timeout")
	cmd.Flags().DurationVar(&f.interval, "interval", execfollow.DefaultInterval, "Poll interval")
	cmd.Flags().BoolVar(&f.tail, "tail", false, "Skip historical events and follow from now")
	cmd.Flags().BoolVar(&f.exitOnGate, "exit-on-gate", false, "Stop and exit 3 as soon as a question/approval gate opens (for scripted supervision)")
}

// resolveHooks parses the --hook flags. They are the only hook source: the
// per-context hooks block went with `reliant context`.
func resolveHooks(flagHooks []string) ([]execfollow.Hook, error) {
	hooks := make([]execfollow.Hook, 0, len(flagHooks))
	for _, raw := range flagHooks {
		h, err := execfollow.ParseHookFlag(raw)
		if err != nil {
			return nil, err
		}
		hooks = append(hooks, h)
	}
	return hooks, nil
}

func newWorkflowFollowCmd() *cobra.Command {
	var flags followFlags

	cmd := &cobra.Command{
		Use:   "follow <execution-id>",
		Short: "Follow a workflow execution and stream NDJSON lifecycle events",
		Long: `Follows a workflow execution, printing one JSON event per line to stdout:
node and workflow state transitions (old_state -> new_state) with
timestamps. All diagnostics go to stderr, so stdout is pipeline-safe.

The execution ID is the chat/execution ID returned by 'reliant workflow run'.

A 'question' or 'approval' event is emitted the moment a gate opens — and,
because the follower reconciles currently-open gates every poll, one is also
emitted if you attach (or --tail) while a gate is already open. Pass
--exit-on-gate to stop at the next gate with exit code 3.

Exit codes:
  0  the root workflow completed successfully
  1  the root workflow failed, was cancelled, or expired (or follow errored)
  2  --timeout elapsed before the workflow reached a terminal state
  3  --exit-on-gate: a question/approval gate opened

Hooks run matching events through 'sh -c <cmd>' with the event JSON on
stdin and RELIANT_EVENT_* environment variables (RELIANT_EVENT,
RELIANT_EVENT_EXECUTION_ID, RELIANT_EVENT_NODE_ID, RELIANT_EVENT_STATE, ...).
A failing hook is logged and never stops the follow.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowFollow(cmd, args[0], &flags)
		},
	}

	flags.register(cmd)
	return cmd
}

// runWorkflowFollow drives a follow session and exits the process with the
// engine's exit code when it is non-zero.
func runWorkflowFollow(cmd *cobra.Command, executionID string, flags *followFlags) error {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return err
	}

	hooks, err := resolveHooks(flags.hooks)
	if err != nil {
		return err
	}

	client, err := conn.sdkClient()
	if err != nil {
		return err
	}

	res, err := followExecution(cmd, client, executionID, followSettings{
		opts: reliant.FollowOptions{
			Interval:   flags.interval,
			Timeout:    flags.timeout,
			Tail:       flags.tail,
			StopOnGate: flags.exitOnGate,
		},
		out:     cmd.OutOrStdout(),
		hooks:   hooks,
		funding: reliantv1connect.NewProviderAccountServiceClient(conn.httpClient(), conn.ServerURL),
	})
	if err != nil {
		return err
	}
	if res.ExitCode != execfollow.ExitSuccess {
		os.Exit(res.ExitCode)
	}
	return nil
}

// followSettings is what the CLI layers over the SDK's follow: where the event
// stream is written, how an event is rendered, and the --hook commands.
type followSettings struct {
	opts reliant.FollowOptions
	// out receives one line per event; nil writes none.
	out io.Writer
	// render formats an event for out; nil writes the canonical NDJSON line.
	// Hooks always receive the NDJSON form.
	render func(execfollow.Event) string
	hooks  []execfollow.Hook
	// funding reads the chat's question before it spends the person's own
	// AI account (workflow_funding.go); nil reads none. The follow prints one
	// line to stderr each time the run starts waiting on it.
	funding fundingQuestionReader
}

// followExecution follows the chat/execution id through the SDK and relays its
// events the way the CLI always has: the NDJSON line (or rendered text) to out,
// then the matching hooks. The exit code, the false-green verdict and gate
// reconciliation are the SDK engine's.
func followExecution(cmd *cobra.Command, client *reliant.Client, executionID string, set followSettings) (reliant.FollowResult, error) {
	// Every diagnostic of the follow goes through one serialized writer: the
	// SDK's log, the hooks' output and the funding watcher write from
	// different goroutines, and cmd's stderr may be any io.Writer.
	stderr := &lockedWriter{w: cmd.ErrOrStderr()}
	set.opts.Log = stderr
	f, err := client.Runs.FollowSession(cmd.Context(), executionID, set.opts)
	if err != nil {
		return reliant.FollowResult{}, err
	}
	defer f.Close()
	stopFunding := startFundingWatch(cmd.Context(), set.funding, executionID, stderr)
	defer stopFunding()

	runner := &execfollow.HookRunner{Stderr: stderr}
	for ev := range f.Events() {
		line, err := json.Marshal(ev)
		if err != nil {
			fmt.Fprintf(stderr, "reliant: failed to encode event: %v\n", err)
			continue
		}
		if set.out != nil {
			if set.render != nil {
				fmt.Fprintf(set.out, "%s\n", set.render(ev))
			} else {
				fmt.Fprintf(set.out, "%s\n", line)
			}
		}
		runner.Run(cmd.Context(), set.hooks, ev, line)
	}
	return f.Wait()
}

// lockedWriter serializes writes to w, so lines written from several
// goroutines never interleave or race.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
