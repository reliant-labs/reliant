// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/capacitywake"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// usableFallbackModel checks the "<model>@<driver>" a person asked to continue
// on: it must name a provider they have connected that serves the model. A
// wait that was answered with a model nobody can serve would only fail the
// next turn, so it is refused here, where the person can pick another. A
// refusal is an svcerr.InvalidArgument.
func usableFallbackModel(ctx context.Context, userID, modelAtDriver string) (string, error) {
	modelID, driverID := drivers.ParseModelIDWithDriver(modelAtDriver)
	if driverID == "" {
		return "", svcerr.InvalidArgument("use_model must name a provider as <model>@<provider>")
	}
	if _, ok := models.MustGetRegistry().GetDefinition(modelID); !ok {
		return "", svcerr.InvalidArgument(fmt.Sprintf("unknown model %q", modelID))
	}
	avail := drivers.GetAvailableDrivers(ctx, userID)
	cfg, ok := avail.Drivers[models.DriverID(driverID)]
	if !ok || !cfg.IsConfigured() {
		return "", svcerr.InvalidArgument(fmt.Sprintf("the %s provider is not connected", models.ProviderDisplayName(driverID)))
	}
	if !models.CanDriverUseModel(models.Family(driverID), models.ModelID(modelID)) {
		return "", svcerr.InvalidArgument(fmt.Sprintf("the %s provider does not serve %s", models.ProviderDisplayName(driverID), modelID))
	}
	return modelAtDriver, nil
}

// runCanBeWoken reports that the chat has a live root run (executing or
// paused) to send a capacity wake to. Whether any of its threads is blocked on
// a provider is the run's to know — a sub-agent's usage-limit wait does not
// show on the chat's activity — and a wake that finds nothing blocked is a
// no-op, so the live run is the whole test.
func (s *Service) runCanBeWoken(ctx context.Context, chat *db.Chat) bool {
	return chat.WorkflowID != nil && *chat.WorkflowID != "" && s.rootRunIsLive(ctx, *chat.WorkflowID)
}

// wakeChat sends the chat's run a capacity wake (v2.CapacityWakeSignalName):
// every thread blocked on a provider — parked on a usage limit, stopped on a
// provider failure, or holding the run in a self-pause for one — re-evaluates
// its routing and goes on, as one action for the whole chat. useModel, when
// set, is a "<model>@<driver>" the waits may continue on instead (a provider
// the user just switched the chat to, or "Use <model> now").
//
// Addressed to the workflow's latest run, so it reaches a run a resume has
// just reset. why is for the log.
func (s *Service) wakeChat(ctx context.Context, chat *db.Chat, useModel, why string) error {
	return s.sendWake(ctx, chat, v2.CapacityWakeSignal{UseModel: useModel}, why)
}

// resumeWholeChat is the wake for the user's Resume: as wakeChat, and every
// sub-agent that stopped on a failure goes on too, whatever the failure — the
// user asked the whole chat to continue (CapacityWakeSignal.AllStopped).
func (s *Service) resumeWholeChat(ctx context.Context, chat *db.Chat) error {
	return s.sendWake(ctx, chat, v2.CapacityWakeSignal{AllStopped: true}, "resume")
}

func (s *Service) sendWake(ctx context.Context, chat *db.Chat, sig v2.CapacityWakeSignal, why string) error {
	if chat.WorkflowID == nil || *chat.WorkflowID == "" {
		return nil
	}
	if err := s.deps.Temporal.SignalWorkflow(ctx, *chat.WorkflowID, "", v2.CapacityWakeSignalName, sig); err != nil {
		return err
	}
	logging.InfoContext(ctx, "Sent the chat's run a capacity wake", "chatID", chat.ID, "workflowID", *chat.WorkflowID,
		"why", why, "useModel", sig.UseModel, "allStopped", sig.AllStopped)
	return nil
}

// wakeChatBestEffort is wakeChat for a caller whose own work already
// succeeded: a wake that did not land is logged, and the threads go on at
// their own resets or at the next wake.
func (s *Service) wakeChatBestEffort(ctx context.Context, chat *db.Chat, useModel, why string) {
	if err := s.wakeChat(ctx, chat, useModel, why); err != nil {
		logging.WarnContext(ctx, "Could not send the chat's run a capacity wake", "chatID", chat.ID, "why", why, "error", err)
	}
}

// WakeUserChats sends a capacity wake to every chat of userID whose run may
// have a thread blocked on a provider, and reports how many it reached
// (capacitywake.WakeUser). The account handlers call it when the user's
// providers gain capacity (an account added, re-authed or un-paused), so each
// such chat goes on as a whole, its root and every sub-agent, without the user
// touching it. Each blocked thread re-evaluates its routing, and the account
// router moves a thread off the account it was pinned to when that account is
// benched and the new one is usable.
func (s *Service) WakeUserChats(ctx context.Context, userID, why string) int {
	return capacitywake.WakeUser(ctx, s.deps.Repo, s.deps.Temporal, userID, why)
}

// RetryUsageLimitWait is "Retry now": it sends the chat's run a capacity wake,
// so every thread blocked on a provider — the root's or any sub-agent's — retries
// at once instead of at its reset. If a limit is still enforced the step fails
// the same way and parks again on the new reset, so pressing it early costs one
// refused request per waiting thread.
//
// A chat with no live run is left alone (woken = false).
func (s *Service) RetryUsageLimitWait(
	ctx context.Context,
	req *connect.Request[reliantv1.RetryUsageLimitWaitRequest],
) (*connect.Response[reliantv1.RetryUsageLimitWaitResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if !s.runCanBeWoken(ctx, chat) {
		return connect.NewResponse(&reliantv1.RetryUsageLimitWaitResponse{Woken: false}), nil
	}
	useModel := ""
	if req.Msg.UseModel != nil && *req.Msg.UseModel != "" {
		var err error
		if useModel, err = usableFallbackModel(ctx, userID, *req.Msg.UseModel); err != nil {
			return nil, err
		}
	}
	if err := s.wakeChat(ctx, chat, useModel, "retry now"); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to wake the run"))
	}
	return connect.NewResponse(&reliantv1.RetryUsageLimitWaitResponse{Woken: true}), nil
}
