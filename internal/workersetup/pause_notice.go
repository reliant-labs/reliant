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
// the pause (triggers.paused_at) is the durable intent, and one workflow per
// pause delivers it. The workflow records triggers.pause_notice_for only once
// the email is DELIVERED (the control plane sent it, or had already sent it),
// so a pause whose email has not gone out is still owed one, and the worker's
// sweep starts its workflow again whenever none is running.
//
// The notice kind is newer than the service: a control plane that predates it
// cannot send it, and a release of reliant may reach production first. That
// is not a refusal: the workflow waits, with backoff, and asks again, so the
// email goes out once the control plane is upgraded (pauseNoticeUpgradeWaits).

// OrgAutomationNoOrgAccountNoticeWorkflowName, NotifyOrgAutomationNoOrgAccountActivityName
// and ConfirmPauseNoticeActivityName are the registered names. Never rename
// them: a running notice would find no worker for its type.
const (
	OrgAutomationNoOrgAccountNoticeWorkflowName = "OrgAutomationNoOrgAccountNoticeWorkflow"
	NotifyOrgAutomationNoOrgAccountActivityName = "NotifyOrgAutomationNoOrgAccount"
	ConfirmPauseNoticeActivityName              = "ConfirmPauseNotice"
)

// pauseNoticeUpgradeWaits is how long the notice waits, before each new ask,
// for a control plane that cannot send it yet: doubling from half an hour to a
// day, about a week in all. Then the workflow fails, unconfirmed, and the next
// sweep starts another.
var pauseNoticeUpgradeWaits = func() []time.Duration {
	waits := []time.Duration{}
	for w := 30 * time.Minute; len(waits) < 12; w = min(2*w, 24*time.Hour) {
		waits = append(waits, w)
	}
	return waits
}()

// PauseNoticeAnswer is what one ask of the control plane came to.
type PauseNoticeAnswer struct {
	// Unsupported: the control plane cannot send this notice (it predates the
	// kind, or has no OrgNotifyInternalService). Nothing was delivered; ask
	// again later.
	Unsupported bool
}

// OrgAutomationNoOrgAccountNoticeWorkflow delivers one pause's notice: asks the
// control plane, retried as a rejected key's is (orgKeyNoticeRetry), waits and
// asks again while it cannot send the kind, and records the pause as notified
// once it is delivered. A refusal that a retry cannot change fails it
// unconfirmed (temporal.NewNonRetryableApplicationError, from the activity).
func OrgAutomationNoOrgAccountNoticeWorkflow(ctx workflow.Context, n controlplane.OrgAutomationNoOrgAccountNotice) error {
	ctx = workflow.WithActivityOptions(ctx, orgKeyNoticeRetry)
	for attempt := 0; ; attempt++ {
		var answer PauseNoticeAnswer
		if err := workflow.ExecuteActivity(ctx, NotifyOrgAutomationNoOrgAccountActivityName, n).Get(ctx, &answer); err != nil {
			return err
		}
		if !answer.Unsupported {
			break
		}
		if attempt == len(pauseNoticeUpgradeWaits) {
			return temporal.NewApplicationError(
				fmt.Sprintf("the control plane still cannot email org %s's admins about automation %s; the sweep asks again", n.OrgID, n.TriggerID),
				"OrgNoticeUnsupported")
		}
		if err := workflow.Sleep(ctx, pauseNoticeUpgradeWaits[attempt]); err != nil {
			return err
		}
	}
	return workflow.ExecuteActivity(ctx, ConfirmPauseNoticeActivityName, n).Get(ctx, nil)
}

// pauseNotifier asks the control plane to email a pause's notice.
// controlplane.Service is one.
type pauseNotifier interface {
	NotifyOrgAutomationNoOrgAccount(ctx context.Context, n controlplane.OrgAutomationNoOrgAccountNotice) (deduplicated bool, err error)
}

// pauseNoticeConfirmer records that a pause's notice was delivered.
type pauseNoticeConfirmer interface {
	ConfirmPauseNotice(ctx context.Context, triggerID string, pausedAt time.Time) error
}

// pauseNotices is the notice's activities.
type pauseNotices struct {
	// admins is nil without a control plane: the inbox item is the only notice.
	admins pauseNotifier
	// store records the delivery. Nil (no database on this worker) records
	// nothing, and the sweep of a worker with one starts the notice again.
	store pauseNoticeConfirmer
}

// Notify asks the control plane to email the notice. Sent, already sent, or no
// control plane to send it (the inbox item is the notice) is delivered. A
// control plane that cannot send the kind yet answers Unsupported; a refusal a
// retry cannot change ends the workflow unconfirmed; anything else is retried.
func (a pauseNotices) Notify(ctx context.Context, n controlplane.OrgAutomationNoOrgAccountNotice) (PauseNoticeAnswer, error) {
	if a.admins == nil {
		logging.WarnContext(ctx, "No control plane to email the org's admins about a paused automation; the inbox still lists it",
			"orgID", n.OrgID, "triggerID", n.TriggerID)
		return PauseNoticeAnswer{}, nil
	}
	deduplicated, err := a.admins.NotifyOrgAutomationNoOrgAccount(ctx, n)
	var refused *controlplane.ProjectOrgMoveError
	switch {
	case err == nil:
		logging.InfoContext(ctx, "Emailed the org's admins about an automation paused for want of an org account",
			"orgID", n.OrgID, "triggerID", n.TriggerID, "incident", n.IncidentKey, "deduplicated", deduplicated)
		return PauseNoticeAnswer{}, nil
	case cannotSendNoticeKind(err):
		logging.WarnContext(ctx, "The control plane cannot email the org's admins about a paused automation yet; asking again later",
			"orgID", n.OrgID, "triggerID", n.TriggerID, "error", err)
		return PauseNoticeAnswer{Unsupported: true}, nil
	case errors.As(err, &refused) && refused.Permanent():
		return PauseNoticeAnswer{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("the control plane refused to email org %s's admins about automation %s: %v", n.OrgID, n.TriggerID, err),
			"OrgNoticeRefused", err)
	}
	return PauseNoticeAnswer{}, err
}

// noticeKindUnknown is how a control plane that predates a notice kind answers
// it: the request's oneof arrives empty (protojson drops the unknown field),
// so NotifyOrgAdmins says no notice was given. reliant always sends one.
const noticeKindUnknown = "a notice is required"

// cannotSendNoticeKind reports whether err says the control plane cannot send
// this kind of notice at all (yet), rather than refusing this one: it has no
// OrgNotifyInternalService, the rpc is unimplemented, or it predates the kind.
func cannotSendNoticeKind(err error) bool {
	if errors.Is(err, controlplane.ErrOrgNotifyUnsupported) {
		return true
	}
	var refused *controlplane.ProjectOrgMoveError
	if !errors.As(err, &refused) {
		return false
	}
	return refused.Code == "unimplemented" || (refused.Code == "invalid_argument" && refused.Message == noticeKindUnknown)
}

// Confirm records that n's pause was notified. A pause resumed or re-paused
// since is not written over (ConfirmPauseNotice): that pause's notice is its
// own.
func (a pauseNotices) Confirm(ctx context.Context, n controlplane.OrgAutomationNoOrgAccountNotice) error {
	if a.store == nil {
		return nil
	}
	pausedAt, err := time.Parse(time.RFC3339Nano, n.IncidentKey)
	if err != nil {
		return temporal.NewNonRetryableApplicationError("read the incident of the notice", "OrgNoticeIncident", err)
	}
	return a.store.ConfirmPauseNotice(ctx, n.TriggerID, pausedAt)
}

// pauseNoticeWorkflowID names one pause: an automation, paused at one time.
func pauseNoticeWorkflowID(n controlplane.OrgAutomationNoOrgAccountNotice) string {
	return "org-automation-no-org-account/" + n.OrgID + "/" + n.TriggerID + "/" + n.IncidentKey
}

// PauseNoticeStore is what a pause's notice is started from: the pause read
// back, and what the sweep lists. *db.Repo satisfies it.
type PauseNoticeStore interface {
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	ListUndeliveredPauseNotices(ctx context.Context, limit int) ([]*core.Trigger, error)
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

// startPauseNotice starts n's workflow. One already running for the pause is
// the same notice; one that failed (refused, or the control plane still could
// not send it) is started again (ALLOW_DUPLICATE_FAILED_ONLY). The workflow,
// not the start, records the delivery.
func startPauseNotice(ctx context.Context, q orgKeyNoticeQueue, n controlplane.OrgAutomationNoOrgAccountNotice) (started bool, err error) {
	_, err = q.starter.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    pauseNoticeWorkflowID(n),
		TaskQueue:             q.taskQueue,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}, OrgAutomationNoOrgAccountNoticeWorkflowName, n)
	var running *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &running) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("start the notice: %w", err)
	}
	return true, nil
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
		if _, err := startPauseNotice(ctx, q, *n); err != nil {
			logging.WarnContext(ctx, "Could not start emailing the org's admins about a paused automation; the sweep retries it",
				"triggerID", triggerID, "error", err)
		}
	}
}

// sweepPauseNotices starts the notice of every no-org-account pause whose email
// was never confirmed delivered and has no workflow running, and reports how
// many it started.
func sweepPauseNotices(ctx context.Context, store PauseNoticeStore, q orgKeyNoticeQueue) int {
	pending, err := store.ListUndeliveredPauseNotices(ctx, orgKeyNoticeSweepBatch)
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
		ok, err := startPauseNotice(ctx, q, *n)
		if err != nil {
			logging.WarnContext(ctx, "Could not start a paused automation's notice; the next sweep retries it",
				"triggerID", t.ID, "error", err)
			continue
		}
		if ok {
			started++
		}
	}
	if started > 0 {
		logging.InfoContext(ctx, "Started the notices of paused automations whose email was never delivered", "started", started)
	}
	return started
}
