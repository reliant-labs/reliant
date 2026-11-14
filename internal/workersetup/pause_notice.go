// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// An automation paused for want of an org account (core.TriggerPausedNoOrgAccount;
// control-plane docs/design/org-provider-accounts.md §11.4) is emailed to its
// org's owners and admins the way a rejected org key is (org_key_notice.go):
// the pause (triggers.paused_at) is the durable intent, one workflow per pause
// delivers it with backoff, triggers.pause_notice_for records that Temporal
// accepted it, and the worker's sweep starts any pause whose email never was.

// OrgAutomationNoOrgAccountNoticeWorkflowName and
// NotifyOrgAutomationNoOrgAccountActivityName are the registered names. Never
// rename them: a running notice would find no worker for its type.
const (
	OrgAutomationNoOrgAccountNoticeWorkflowName = "OrgAutomationNoOrgAccountNoticeWorkflow"
	NotifyOrgAutomationNoOrgAccountActivityName = "NotifyOrgAutomationNoOrgAccount"
)

// OrgAutomationNoOrgAccountNoticeWorkflow delivers one pause's notice, retried
// as a rejected key's is (orgKeyNoticeRetry).
func OrgAutomationNoOrgAccountNoticeWorkflow(ctx workflow.Context, n controlplane.OrgAutomationNoOrgAccountNotice) error {
	ctx = workflow.WithActivityOptions(ctx, orgKeyNoticeRetry)
	return workflow.ExecuteActivity(ctx, NotifyOrgAutomationNoOrgAccountActivityName, n).Get(ctx, nil)
}

// pauseNotifier asks the control plane to email a pause's notice.
// controlplane.Service is one.
type pauseNotifier interface {
	NotifyOrgAutomationNoOrgAccount(ctx context.Context, n controlplane.OrgAutomationNoOrgAccountNotice) (deduplicated bool, err error)
}

// pauseNotices is the notice's activity.
type pauseNotices struct {
	// admins is nil without a control plane: the inbox item is the only notice.
	admins pauseNotifier
}

// Notify asks the control plane to email the notice. A refusal a retry cannot
// change ends it (a control plane that predates the notice kind answers
// invalid_argument); one without the service, or no control plane, has nothing
// to send. Anything else is retried.
func (a pauseNotices) Notify(ctx context.Context, n controlplane.OrgAutomationNoOrgAccountNotice) error {
	if a.admins == nil {
		logging.WarnContext(ctx, "No control plane to email the org's admins about a paused automation; the inbox still lists it",
			"orgID", n.OrgID, "triggerID", n.TriggerID)
		return nil
	}
	deduplicated, err := a.admins.NotifyOrgAutomationNoOrgAccount(ctx, n)
	var refused *controlplane.ProjectOrgMoveError
	switch {
	case err == nil:
		logging.InfoContext(ctx, "Emailed the org's admins about an automation paused for want of an org account",
			"orgID", n.OrgID, "triggerID", n.TriggerID, "incident", n.IncidentKey, "deduplicated", deduplicated)
		return nil
	case errors.Is(err, controlplane.ErrOrgNotifyUnsupported):
		logging.WarnContext(ctx, "The control plane cannot email the org's admins; the inbox still lists the paused automation",
			"orgID", n.OrgID, "triggerID", n.TriggerID, "error", err)
		return nil
	case errors.As(err, &refused) && refused.Permanent():
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("the control plane refused to email org %s's admins about automation %s: %v", n.OrgID, n.TriggerID, err),
			"OrgNoticeRefused", err)
	}
	return err
}

// pauseNoticeWorkflowID names one pause: an automation, paused at one time.
func pauseNoticeWorkflowID(n controlplane.OrgAutomationNoOrgAccountNotice) string {
	return "org-automation-no-org-account/" + n.OrgID + "/" + n.TriggerID + "/" + n.IncidentKey
}

// PauseNoticeStore is what a pause's notice reads and confirms, and what the
// sweep lists. *db.Repo satisfies it.
type PauseNoticeStore interface {
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	ListUnstartedPauseNotices(ctx context.Context, limit int) ([]*core.Trigger, error)
	ConfirmPauseNotice(ctx context.Context, triggerID string, pausedAt time.Time) error
}

// pauseNotice is the notice of t's current pause, nil when t is not paused for
// want of an org account (resumed since, or paused by an older release).
func pauseNotice(t *core.Trigger, model string) *controlplane.OrgAutomationNoOrgAccountNotice {
	if t == nil || t.Enabled || t.PausedReason != core.TriggerPausedNoOrgAccount || t.PausedAt == nil || t.OrgID == nil || *t.OrgID == "" {
		return nil
	}
	return &controlplane.OrgAutomationNoOrgAccountNotice{
		OrgID: *t.OrgID, IncidentKey: t.PausedAt.UTC().Format(time.RFC3339Nano),
		TriggerID: t.ID, Automation: t.Name, Model: model, OwnerUserID: t.UserID,
	}
}

// startAndConfirmPauseNotice starts n's workflow and, once Temporal has
// accepted it (or already had it), records that on the trigger. Either step
// failing leaves the pause unconfirmed, so it is started again by the sweep.
func startAndConfirmPauseNotice(ctx context.Context, store PauseNoticeStore, q orgKeyNoticeQueue, n controlplane.OrgAutomationNoOrgAccountNotice) error {
	pausedAt, err := time.Parse(time.RFC3339Nano, n.IncidentKey)
	if err != nil {
		return fmt.Errorf("read the incident of the notice: %w", err)
	}
	_, err = q.starter.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    pauseNoticeWorkflowID(n),
		TaskQueue:             q.taskQueue,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}, OrgAutomationNoOrgAccountNoticeWorkflowName, n)
	var started *serviceerror.WorkflowExecutionAlreadyStarted
	if err != nil && !errors.As(err, &started) {
		return fmt.Errorf("start the notice: %w", err)
	}
	if err := store.ConfirmPauseNotice(ctx, n.TriggerID, pausedAt); err != nil {
		return fmt.Errorf("record that the notice started: %w", err)
	}
	return nil
}

// noOrgAccountPauseNotifier is triggers.AutoPause's hook for a pause it just
// made: it reads the pause back (its paused_at is the incident) and starts its
// notice. Best effort: the pause stands, and the sweep starts what this did
// not.
func noOrgAccountPauseNotifier(store PauseNoticeStore, q orgKeyNoticeQueue) func(ctx context.Context, triggerID, model string) {
	return func(ctx context.Context, triggerID, model string) {
		ctx = context.WithoutCancel(ctx)
		t, err := store.GetTrigger(ctx, triggerID)
		if err != nil {
			logging.WarnContext(ctx, "Could not read a paused automation to email its org's admins; the sweep retries it", "triggerID", triggerID, "error", err)
			return
		}
		n := pauseNotice(t, model)
		if n == nil {
			return
		}
		if err := startAndConfirmPauseNotice(ctx, store, q, *n); err != nil {
			logging.WarnContext(ctx, "Could not start emailing the org's admins about a paused automation; the sweep retries it",
				"triggerID", triggerID, "error", err)
		}
	}
}

// sweepPauseNotices starts every no-org-account pause whose email was never
// confirmed started, and reports how many it started.
func sweepPauseNotices(ctx context.Context, store PauseNoticeStore, q orgKeyNoticeQueue) int {
	pending, err := store.ListUnstartedPauseNotices(ctx, orgKeyNoticeSweepBatch)
	if err != nil {
		logging.WarnContext(ctx, "Could not list the paused automations whose notice never started", "error", err)
		return 0
	}
	started := 0
	for _, t := range pending {
		n := pauseNotice(t, "")
		if n == nil {
			continue
		}
		if err := startAndConfirmPauseNotice(ctx, store, q, *n); err != nil {
			logging.WarnContext(ctx, "Could not start a paused automation's notice; the next sweep retries it",
				"triggerID", t.ID, "error", err)
			continue
		}
		started++
	}
	if started > 0 {
		logging.InfoContext(ctx, "Started the notices of paused automations whose start was never confirmed", "started", started)
	}
	return started
}
