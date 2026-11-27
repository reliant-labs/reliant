// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// `reliant pipeline` enrols a repository in Reliant's PR pipeline and merge
// queue, sets its mode, and reads where it stands (PipelineService). Every
// command is refused while the server's pipeline.enabled switch is off.

func newPipelineServiceClient(cmd *cobra.Command) (reliantv1connect.PipelineServiceClient, *connection, error) {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return nil, nil, err
	}
	return reliantv1connect.NewPipelineServiceClient(conn.httpClient(), conn.ServerURL), conn, nil
}

func newPipelineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Enrol a repository in Reliant's PR pipeline and merge queue, and see where it stands",
		Long: `Enrol a GitHub repository in Reliant's PR pipeline and merge queue, set how far
the pipeline acts on it, and read its state and queue.

A repository is enrolled with mode off: nothing acts on it. shadow computes
and records every decision without writing to GitHub; on acts.

Enrolling and changing a mode need an owner or admin of the project's
organization, signed in to the app or with a personal access token (the CLI
login is one). Reading needs membership of the organization.`,
	}
	cmd.AddCommand(newPipelineEnrolCmd(), newPipelineModeCmd(), newPipelineStatusCmd(), newPipelineQueueCmd())
	return cmd
}

func newPipelineEnrolCmd() *cobra.Command {
	var project string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "enrol <owner/repo>",
		Short: "Enrol a repository, with mode off",
		Long: `Enrol a GitHub repository under a project of an organization, with mode off.
Your own GitHub access must reach the repository (Reliant's GitHub App
installed on it, and GitHub connected in Reliant). Enrolling again under the
same project is harmless.

--project names the project by id, name or path ('reliant project list');
without it, the project whose folder holds the current directory.`,
		Example:      `  reliant pipeline enrol reliant-labs/forge --project forge`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := newPipelineServiceClient(cmd)
			if err != nil {
				return err
			}
			p, err := resolveWorkspaceProject(cmd.Context(), newProjectServiceClient(conn), project)
			if err != nil {
				return conn.annotate(err)
			}
			resp, err := client.EnrolRepository(cmd.Context(), connect.NewRequest(&reliantv1.EnrolRepositoryRequest{
				ProjectId: p.GetId(), Repository: strings.TrimSpace(args[0]),
			}))
			if err != nil {
				return conn.annotate(fmt.Errorf("enrolling %s: %w", args[0], err))
			}
			if asJSON {
				return writeRecordsJSON(cmd.OutOrStdout(), false, resp.Msg)
			}
			r := resp.Msg.GetRepository()
			fmt.Fprintf(cmd.OutOrStdout(), "Enrolled %s (repo %s, mode %s) under project %s.\n", r.GetRepository(), r.GetId(), r.GetMode(), p.GetName())
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "the project, by id, name or path (default: the one holding the current directory)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the enrolled repository as JSON")
	return cmd
}

func newPipelineModeCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "mode <owner/repo> off|shadow|on",
		Short: "Set how far the pipeline acts on an enrolled repository",
		Long: `Set an enrolled repository's mode: off (nothing acts; this is unenrolling),
shadow (decisions are computed and recorded, nothing is written to GitHub) or
on (the pipeline acts). The mode is changed only if nobody changed the
repository since it was read.`,
		Example:      `  reliant pipeline mode reliant-labs/forge shadow`,
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := strings.TrimSpace(args[1])
			switch mode {
			case "off", "shadow", "on":
			default:
				return fmt.Errorf("mode must be off, shadow or on, not %q", mode)
			}
			client, conn, err := newPipelineServiceClient(cmd)
			if err != nil {
				return err
			}
			state, err := client.GetPipelineState(cmd.Context(), connect.NewRequest(&reliantv1.GetPipelineStateRequest{Repository: strings.TrimSpace(args[0])}))
			if err != nil {
				return conn.annotate(fmt.Errorf("reading %s: %w", args[0], err))
			}
			repo := state.Msg.GetRepository()
			resp, err := client.SetPipelineMode(cmd.Context(), connect.NewRequest(&reliantv1.SetPipelineModeRequest{
				RepoId: repo.GetId(), Mode: mode, ExpectedVersion: repo.GetVersion(),
			}))
			if err != nil {
				return conn.annotate(fmt.Errorf("setting %s to %s: %w", args[0], mode, err))
			}
			if asJSON {
				return writeRecordsJSON(cmd.OutOrStdout(), false, resp.Msg)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: mode %s (was %s).\n", resp.Msg.GetRepository().GetRepository(), resp.Msg.GetRepository().GetMode(), repo.GetMode())
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the repository as JSON")
	return cmd
}

func newPipelineStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:          "status <owner/repo>",
		Short:        "Show an enrolled repository's mode, its PRs by pipeline state, its newest batches and main's health",
		Example:      `  reliant pipeline status reliant-labs/forge --json`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := newPipelineServiceClient(cmd)
			if err != nil {
				return err
			}
			resp, err := client.GetPipelineState(cmd.Context(), connect.NewRequest(&reliantv1.GetPipelineStateRequest{Repository: strings.TrimSpace(args[0])}))
			if err != nil {
				return conn.annotate(fmt.Errorf("reading %s: %w", args[0], err))
			}
			if asJSON {
				return writeRecordsJSON(cmd.OutOrStdout(), false, resp.Msg)
			}
			printPipelineState(cmd.OutOrStdout(), resp.Msg)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the state as JSON")
	return cmd
}

func newPipelineQueueCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:          "queue <owner/repo>",
		Short:        "List an enrolled repository's merge queue and its current batch",
		Example:      `  reliant pipeline queue reliant-labs/forge`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, conn, err := newPipelineServiceClient(cmd)
			if err != nil {
				return err
			}
			resp, err := listPipelineQueue(cmd.Context(), client, strings.TrimSpace(args[0]))
			if err != nil {
				return conn.annotate(fmt.Errorf("reading %s's queue: %w", args[0], err))
			}
			if asJSON {
				return writeRecordsJSON(cmd.OutOrStdout(), false, resp)
			}
			printPipelineQueue(cmd.OutOrStdout(), resp)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the queue as JSON")
	return cmd
}

// listPipelineQueue resolves owner/name to its enrolment, then lists its queue.
func listPipelineQueue(ctx context.Context, client reliantv1connect.PipelineServiceClient, repo string) (*reliantv1.ListQueueResponse, error) {
	state, err := client.GetPipelineState(ctx, connect.NewRequest(&reliantv1.GetPipelineStateRequest{Repository: repo}))
	if err != nil {
		return nil, err
	}
	resp, err := client.ListQueue(ctx, connect.NewRequest(&reliantv1.ListQueueRequest{RepoId: state.Msg.GetRepository().GetId()}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func printPipelineState(out io.Writer, s *reliantv1.GetPipelineStateResponse) {
	r := s.GetRepository()
	fmt.Fprintf(out, "%s  repo %s  mode %s  queue %s  base %s  version %d\n",
		r.GetRepository(), r.GetId(), r.GetMode(), r.GetQueueState(), r.GetBaseBranch(), r.GetVersion())
	var states []string
	for _, c := range s.GetPrStates() {
		states = append(states, fmt.Sprintf("%s=%d", c.GetState(), c.GetCount()))
	}
	if len(states) == 0 {
		states = []string{"none"}
	}
	fmt.Fprintf(out, "PRs: %s\n", strings.Join(states, " "))
	if h := s.GetMainHealth(); h != nil {
		fmt.Fprintf(out, "%s: %s at %s (%s, %s)\n", r.GetBaseBranch(), h.GetVerdict(), recordsShortSHA(h.GetSha()), h.GetSource(),
			h.GetObservedAt().AsTime().UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintf(out, "%s: no verdict yet\n", r.GetBaseBranch())
	}
	if len(s.GetLatestBatches()) == 0 {
		fmt.Fprintln(out, "Batches: none")
		return
	}
	printPipelineBatches(out, s.GetLatestBatches())
}

func printPipelineBatches(out io.Writer, batches []*reliantv1.PipelineQueueBatch) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "BATCH\tKIND\tSTATE\tATTEMPT\tBASE\tCANDIDATE\tSTARTED")
	for _, b := range batches {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n", b.GetId(), b.GetKind(), b.GetState(), b.GetAttempt(),
			recordsShortSHA(b.GetBaseSha()), recordsDash(recordsShortSHA(b.GetCandidateSha())),
			b.GetStartedAt().AsTime().UTC().Format(time.RFC3339))
	}
	_ = w.Flush()
}

func printPipelineQueue(out io.Writer, q *reliantv1.ListQueueResponse) {
	if b := q.GetCurrentBatch(); b != nil {
		fmt.Fprintln(out, "Current batch:")
		printPipelineBatches(out, []*reliantv1.PipelineQueueBatch{b})
	} else {
		fmt.Fprintln(out, "Current batch: none")
	}
	if len(q.GetEntries()) == 0 {
		fmt.Fprintln(out, "Queue: empty")
		return
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PR\tSTATE\tPRIORITY\tHEAD\tBATCH\tENQUEUED")
	for _, e := range q.GetEntries() {
		fmt.Fprintf(w, "#%d\t%s\t%d\t%s\t%s\t%s\n", e.GetNumber(), e.GetState(), e.GetPriority(), recordsShortSHA(e.GetHeadSha()),
			recordsDash(e.GetBatchId()), e.GetEnqueuedAt().AsTime().UTC().Format(time.RFC3339))
	}
	_ = w.Flush()
}
