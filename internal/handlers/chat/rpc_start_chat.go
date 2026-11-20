package chat

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
)

// StartChat starts a chat's root run: it creates the chat, or starts an
// existing pending one (a branch's first send).
//
// The start path itself lives in internal/launch, because a scheduled trigger
// fires on the worker and the worker cannot import this package. What is left
// here is the handler's own job: read the caller from auth, translate the wire
// request into a launch.Spec, and translate launch's domain errors back into
// the connect codes this RPC has always returned.
func (s *Service) StartChat(
	ctx context.Context,
	req *reliantv1.StartChatRequest,
) (*reliantv1.StartChatResponse, error) {
	userID := auth.MustGetUserID(ctx)
	// The launch kind is the server's decision. A client can ask for exactly
	// two non-default kinds, builder.test through builder_test and cli.run
	// through cli_run; schedule, agent.start_run and manual.run have no way in
	// through this RPC.
	eventKind := core.TriggerEventKindChatStart
	msg := req
	switch {
	case msg.GetBuilderTest() && msg.GetCliRun():
		return nil, svcerr.InvalidArgument("builder_test and cli_run are different launch kinds; set at most one")
	case msg.GetBuilderTest():
		if err := s.validateBuilderTest(ctx, userID, msg); err != nil {
			return nil, err
		}
		eventKind = core.TriggerEventKindBuilderTest
	case msg.GetCliRun():
		if msg.GetChatId() != "" {
			return nil, svcerr.InvalidArgument("cli_run starts a new run; chat_id must be empty")
		}
		eventKind = core.TriggerEventKindCLIRun
		if msg.Title == nil {
			// A command-line run's first message is usually an instruction
			// to the workflow, not a subject a generated title could name.
			msg = proto.Clone(msg).(*reliantv1.StartChatRequest)
			title := launch.RunTitle(msg.GetWorkflow(), firstUserContent(msg.GetMessages()), time.Now())
			msg.Title = &title
		}
	}
	return s.startChat(ctx, userID, msg, eventKind, nil)
}

// firstUserContent is the text of the first user message, or "".
func firstUserContent(messages []*reliantv1.InputMessage) string {
	for _, m := range messages {
		if m.GetRole() == reliantv1.MessageRole_MESSAGE_ROLE_USER {
			return m.GetContent()
		}
	}
	return ""
}

// validateBuilderTest checks a builder_test start: it names a workflow draft
// the caller owns, and it starts a new chat. The draft is what the builder just
// saved; running someone else's draft, or a builtin or project workflow, is not
// a test run of anything the caller is editing.
func (s *Service) validateBuilderTest(ctx context.Context, userID string, msg *reliantv1.StartChatRequest) error {
	if msg.GetChatId() != "" {
		return svcerr.InvalidArgument("builder_test starts a new chat; chat_id must be empty")
	}
	if msg.Workflow == "" {
		return svcerr.InvalidArgument("builder_test requires the workflow to run")
	}
	draft, err := s.deps.Repo.GetWorkflowDraftBySlug(ctx, userID, launch.NormalizeWorkflowSlug(msg.Workflow))
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to look up workflow draft"))
	}
	if !authz.CanReadWorkflow(ctx, userID, draft) {
		return svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("builder_test requires a saved workflow draft you own: %q", msg.Workflow))
	}
	return nil
}
