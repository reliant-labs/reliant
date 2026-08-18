// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	"fmt"
	"os"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/cliconfig"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/execfollow"
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

// resolveHooks merges hook sources: --hook flags win outright; otherwise the
// resolved context's hooks: block applies.
func resolveHooks(flagHooks []string, contextHooks []cliconfig.HookSpec) ([]execfollow.Hook, error) {
	if len(flagHooks) > 0 {
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

	hooks := make([]execfollow.Hook, 0, len(contextHooks))
	for _, spec := range contextHooks {
		h := execfollow.Hook{On: spec.On, Cmd: spec.Cmd}
		if err := execfollow.ValidateHook(h); err != nil {
			return nil, fmt.Errorf("invalid hook in CLI config: %w", err)
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
A failing hook is logged and never stops the follow. Hooks may also be
declared under the context in the CLI config:

  {"contexts": {"prod": {"hooks": [{"on": "workflow_failed", "cmd": "notify.sh"}]}}}

Flags win over config hooks.`,
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

	hooks, err := resolveHooks(flags.hooks, conn.Hooks)
	if err != nil {
		return err
	}

	engine := &execfollow.Engine{
		Source:      newChatUpdateSource(conn.httpClient(), conn.ServerURL, executionID),
		ExecutionID: executionID,
		Out:         cmd.OutOrStdout(),
		Log:         cmd.ErrOrStderr(),
		Hooks:       hooks,
		Interval:    flags.interval,
		Timeout:     flags.timeout,
		Tail:        flags.tail,
		ExitOnGate:  flags.exitOnGate,
	}

	code, err := engine.Run(cmd.Context())
	if err != nil {
		return err
	}
	if code != execfollow.ExitSuccess {
		os.Exit(code)
	}
	return nil
}

// chatUpdateSource adapts ChatService (GetChatUpdates + GetWorkflowExecutions)
// plus QuestionService/ApprovalService (open-gate reconciliation) to the
// execfollow.Source interface. This is a cursor-based poll of the same durable
// per-chat update feed the web UI streams; the richer
// StreamingService.StreamUserUpdates surface can replace it without touching
// the engine.
type chatUpdateSource struct {
	client   reliantv1connect.ChatServiceClient
	question reliantv1connect.QuestionServiceClient
	approval reliantv1connect.ApprovalServiceClient
	chatID   string
}

// newChatUpdateSource builds the follow/watch source with every client the
// engine needs: the chat update feed plus the question/approval surfaces used
// to reconcile a gate that is already open when the follower attaches.
func newChatUpdateSource(httpClient connect.HTTPClient, server, chatID string) *chatUpdateSource {
	return &chatUpdateSource{
		client:   reliantv1connect.NewChatServiceClient(httpClient, server),
		question: reliantv1connect.NewQuestionServiceClient(httpClient, server),
		approval: reliantv1connect.NewApprovalServiceClient(httpClient, server),
		chatID:   chatID,
	}
}

func (s *chatUpdateSource) Updates(ctx context.Context, sinceSeq int64) ([]execfollow.RawUpdate, int64, error) {
	resp, err := s.client.GetChatUpdates(ctx, connect.NewRequest(&reliantv1.GetChatUpdatesRequest{
		ChatId:   s.chatID,
		SinceSeq: sinceSeq,
	}))
	if err != nil {
		return nil, sinceSeq, err
	}

	updates := make([]execfollow.RawUpdate, 0, len(resp.Msg.GetUpdates()))
	for _, u := range resp.Msg.GetUpdates() {
		createdAt, err := time.Parse(time.RFC3339, u.GetCreatedAt())
		if err != nil {
			createdAt = time.Now().UTC()
		}
		updates = append(updates, execfollow.RawUpdate{
			Seq:       u.GetSequenceNumber(),
			Type:      u.GetUpdateType(),
			Data:      []byte(u.GetData()),
			CreatedAt: createdAt,
		})
	}
	return updates, resp.Msg.GetLatestSequence(), nil
}

func (s *chatUpdateSource) Root(ctx context.Context) (execfollow.RootState, error) {
	resp, err := s.client.GetWorkflowExecutions(ctx, connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{
		ChatId: s.chatID,
	}))
	if err != nil {
		return execfollow.RootState{}, err
	}

	root := resp.Msg.GetRootWorkflow()
	if root == nil {
		if all := resp.Msg.GetAllRootWorkflows(); len(all) > 0 {
			root = all[0]
		}
	}
	if root == nil {
		return execfollow.RootState{}, nil
	}
	return execfollow.RootState{
		Found:  true,
		Status: chatWorkflowStatusString(root),
		// The verdict rides along so a follower attaching AFTER the run ended
		// still learns that it did not pass — the terminal event it would have
		// carried is long gone from the tail it is watching.
		Outcome: root.GetOutcome(),
	}, nil
}

// Pending reports the gates currently awaiting input so the engine can surface
// a question/approval that was already open when the follower attached (or
// whose edge update it missed). Errors are non-fatal to the engine — it logs
// and retries next poll — so a transient failure here never wedges a follow.
func (s *chatUpdateSource) Pending(ctx context.Context) ([]execfollow.PendingGate, error) {
	var gates []execfollow.PendingGate

	if q, err := s.question.GetPendingQuestion(ctx, connect.NewRequest(&reliantv1.GetPendingQuestionRequest{
		ChatId: s.chatID,
	})); err == nil {
		if qi := q.Msg.GetQuestion(); qi != nil {
			gates = append(gates, execfollow.PendingGate{
				Kind:     execfollow.GateQuestion,
				ID:       qi.GetQuestionId(),
				StepID:   qi.GetStepId(),
				ThreadID: qi.GetThreadId(),
				Metadata: qi.GetMetadata(),
			})
		}
	} else {
		return nil, err
	}

	if a, err := s.approval.ListApprovalsByChat(ctx, connect.NewRequest(&reliantv1.ListApprovalsByChatRequest{
		ChatId: s.chatID,
	})); err == nil {
		for _, ap := range a.Msg.GetApprovals() {
			if ap.GetStatus() != reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING {
				continue
			}
			gates = append(gates, execfollow.PendingGate{
				Kind:  execfollow.GateApproval,
				ID:    ap.GetId(),
				Title: ap.GetTitle(),
			})
		}
	} else {
		return gates, err
	}

	return gates, nil
}

// chatWorkflowStatusString renders a workflow's lifecycle as the lowercase
// state names used on the NDJSON stream ("completed", "failed", ...). The
// vocabulary is unchanged from when this derived the name from a single enum;
// core.WorkflowStatus.Label produces the same strings, so the stream contract
// holds. "expired" is no longer among them — nothing ever produced it.
func chatWorkflowStatusString(wf *reliantv1.WorkflowExecution) string {
	return core.WorkflowStatus{State: wf.GetState(), StopReason: wf.GetStopReason()}.Label()
}
