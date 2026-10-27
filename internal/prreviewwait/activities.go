// Copyright (c) 2025 Reliant Labs
package prreviewwait

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/prhandoff"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
)

// IdentityStore resolves the actual executing root's persisted owner/chat.
type IdentityStore interface {
	GetWorkflow(context.Context, string) (*core.Workflow, error)
	GetChat(context.Context, string) (*core.Chat, error)
}

// RepositoryReader authenticates provider identity, not node owner/name alone.
type RepositoryReader interface {
	Repository(context.Context, string, string) (prhandoff.Repository, error)
}

// Activities implements real Temporal activities used by the typed wait node.
// It is never installed by app roots in this disabled stage.
type Activities struct {
	Service  Service
	Identity IdentityStore
	GitHub   RepositoryReader
}

// ConsumeInput is an activity's durable exact subscription/outcome receipt.
type ConsumeInput struct {
	Subscription Subscription
	Outcome      Outcome
}

func (a Activities) request(ctx context.Context, in NodeInput) (Request, error) {
	if a.Service == nil || a.Identity == nil || a.GitHub == nil || !activity.IsActivity(ctx) {
		return Request{}, prhandoff.ErrRefused
	}
	execution := activity.GetInfo(ctx).WorkflowExecution
	root, err := a.Identity.GetWorkflow(ctx, execution.ID)
	if err != nil {
		return Request{}, err
	}
	if root == nil || root.ParentID != nil || !root.Status.Live() {
		return Request{}, ErrUnavailable
	}
	chat, err := a.Identity.GetChat(ctx, root.ChatID)
	if err != nil {
		return Request{}, err
	}
	if chat == nil || chat.UserID == "" || (root.OwnerUserID != nil && *root.OwnerUserID != chat.UserID) {
		return Request{}, prhandoff.ErrRefused
	}
	repo, err := a.GitHub.Repository(ctx, chat.UserID, in.Repo)
	if err != nil {
		return Request{}, err
	}
	exact := runcontinuity.Execution{WorkflowID: execution.ID, RunID: execution.RunID}
	return Request{RequestKey: RequestKey(exact, in), OwnerID: chat.UserID, ChatID: chat.ID, Execution: exact, RepoID: repo.ID, PR: in.PR, HeadSHA: in.HeadSHA}, nil
}

// Arm authenticates the real activity context, then atomically subscribes.
func (a Activities) Arm(ctx context.Context, in NodeInput) (Subscription, error) {
	request, err := a.request(ctx, in)
	if errors.Is(err, ErrUnavailable) {
		return Subscription{HeadSHA: in.HeadSHA, State: "needs_human"}, nil
	}
	if err != nil {
		return Subscription{}, err
	}
	sub, err := a.Service.Arm(ctx, request)
	if errors.Is(err, ErrUnavailable) {
		return Subscription{HeadSHA: in.HeadSHA, State: "needs_human"}, nil
	}
	if errors.Is(err, prhandoff.ErrRefused) {
		return Subscription{}, temporal.NewNonRetryableApplicationError(err.Error(), "PRReviewWaitRefused", err)
	}
	if err == nil {
		sub.RepoID = request.RepoID
		sub.PR = request.PR
	}
	return sub, err
}

func exactCaller(ctx context.Context, sub Subscription) bool {
	if !activity.IsActivity(ctx) {
		return false
	}
	e := activity.GetInfo(ctx).WorkflowExecution
	return e.ID == sub.WorkflowID && e.RunID == sub.CurrentRunID
}

// Ready sets readiness only for the execution which durably armed the wait.
func (a Activities) Ready(ctx context.Context, s Subscription) error {
	if !exactCaller(ctx, s) {
		return prhandoff.ErrRefused
	}
	return a.Service.Ready(ctx, s)
}

// Read fetches authoritative inbox metadata for the exact executing author.
func (a Activities) Read(ctx context.Context, s Subscription) (Outcome, error) {
	if !exactCaller(ctx, s) {
		return Outcome{}, prhandoff.ErrRefused
	}
	return a.Service.Read(ctx, s)
}

// Consume records author consumption separately from successful signal ack.
func (a Activities) Consume(ctx context.Context, in ConsumeInput) error {
	if !exactCaller(ctx, in.Subscription) {
		return prhandoff.ErrRefused
	}
	err := a.Service.Consume(ctx, in.Subscription, in.Outcome)
	if errors.Is(err, ErrStaleRead) {
		return temporal.NewNonRetryableApplicationError(err.Error(), "PRReviewWaitStaleRead", err)
	}
	return err
}

// Cancel closes only this execution's active subscription, never spawning.
func (a Activities) Cancel(ctx context.Context, s Subscription) error {
	if !exactCaller(ctx, s) {
		return prhandoff.ErrRefused
	}
	return a.Service.Cancel(ctx, s)
}

// CancelRequest reconciles unknown Arm results using exact authenticated
// execution + node occurrence. It deliberately does not require a live root or
// a GitHub read: terminal author cancellation must still tombstone late Arm.
func (a Activities) CancelRequest(ctx context.Context, in NodeInput) error {
	if a.Service == nil || a.Identity == nil || !activity.IsActivity(ctx) {
		return prhandoff.ErrRefused
	}
	e := activity.GetInfo(ctx).WorkflowExecution
	root, err := a.Identity.GetWorkflow(ctx, e.ID)
	if err != nil {
		return err
	}
	if root == nil || root.ParentID != nil {
		return prhandoff.ErrRefused
	}
	chat, err := a.Identity.GetChat(ctx, root.ChatID)
	if err != nil {
		return err
	}
	if chat == nil || chat.UserID == "" || (root.OwnerUserID != nil && *root.OwnerUserID != chat.UserID) {
		return prhandoff.ErrRefused
	}
	exact := runcontinuity.Execution{WorkflowID: e.ID, RunID: e.RunID}
	return a.Service.CancelRequest(ctx, Request{RequestKey: RequestKey(exact, in), OwnerID: chat.UserID, ChatID: chat.ID, Execution: exact})
}

// Register installs only the narrow activities on an explicitly supplied worker.
// Production does not call it until trusted event admission/retention are ready.
func (a Activities) Register(registry worker.ActivityRegistry) {
	registry.RegisterActivityWithOptions(a.Arm, activity.RegisterOptions{Name: "PRReviewWaitArm"})
	registry.RegisterActivityWithOptions(a.Ready, activity.RegisterOptions{Name: "PRReviewWaitReady"})
	registry.RegisterActivityWithOptions(a.Read, activity.RegisterOptions{Name: "PRReviewWaitRead"})
	registry.RegisterActivityWithOptions(a.Consume, activity.RegisterOptions{Name: "PRReviewWaitConsume"})
	registry.RegisterActivityWithOptions(a.Cancel, activity.RegisterOptions{Name: "PRReviewWaitCancel"})
	registry.RegisterActivityWithOptions(a.CancelRequest, activity.RegisterOptions{Name: "PRReviewWaitCancelRequest"})
}
