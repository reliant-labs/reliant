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
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Emailing an org's owners and admins about a rejected org key
// (control-plane docs/design/org-provider-accounts.md §4.7) is durable: the
// worker that marks the key starts one OrgKeyRejectedNoticeWorkflow per
// incident, and its activity asks the control plane until the control plane
// answers. A worker that dies mid-call, a control plane that is down, and a
// control plane answering Unavailable because another call holds the
// incident's lease are all retried with backoff; the control plane emails
// each owner and admin once per incident whoever asks how often.

// OrgKeyRejectedNoticeWorkflowName and NotifyOrgKeyRejectedActivityName are
// the registered names. Never rename them: a running notice would find no
// worker for its type.
const (
	OrgKeyRejectedNoticeWorkflowName = "OrgKeyRejectedNoticeWorkflow"
	NotifyOrgKeyRejectedActivityName = "NotifyOrgKeyRejected"
)

// orgKeyNoticeRetry is how a notice is retried: soon enough that a person
// reads the email minutes after the rejection, and past the control plane's
// incident lease (five minutes) so a sender that died is taken over; for a
// day, after which the inbox item is the notice.
var orgKeyNoticeRetry = workflow.ActivityOptions{
	StartToCloseTimeout:    time.Minute,
	ScheduleToCloseTimeout: 24 * time.Hour,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval:    10 * time.Second,
		BackoffCoefficient: 2,
		MaximumInterval:    5 * time.Minute,
	},
}

// OrgKeyRejectedNoticeWorkflow delivers one notice. Its id names the incident
// (orgKeyNoticeWorkflowID), so a second start for the same incident is the
// same workflow.
func OrgKeyRejectedNoticeWorkflow(ctx workflow.Context, n controlplane.OrgKeyRejectedNotice) error {
	ctx = workflow.WithActivityOptions(ctx, orgKeyNoticeRetry)
	return workflow.ExecuteActivity(ctx, NotifyOrgKeyRejectedActivityName, n).Get(ctx, nil)
}

// orgKeyNotices is the notice's activity.
type orgKeyNotices struct {
	// admins is nil without a control plane: there is no one to ask, and the
	// inbox item is the only notice.
	admins OrgAdminNotifier
}

// Notify asks the control plane to email the notice. A refusal that a retry
// cannot change (the org is gone, the request malformed) ends it; a control
// plane without the service, or a worker without one, has nothing to send.
// Anything else is retried.
func (a orgKeyNotices) Notify(ctx context.Context, n controlplane.OrgKeyRejectedNotice) error {
	if a.admins == nil {
		logging.WarnContext(ctx, "No control plane to email the org's admins about a rejected key; the inbox still lists it",
			"orgID", n.OrgID, "accountID", n.AccountID)
		return nil
	}
	deduplicated, err := a.admins.NotifyOrgKeyRejected(ctx, n)
	var refused *controlplane.ProjectOrgMoveError
	switch {
	case err == nil:
		logging.InfoContext(ctx, "Emailed the org's admins about a rejected key",
			"orgID", n.OrgID, "accountID", n.AccountID, "incident", n.IncidentKey, "deduplicated", deduplicated)
		return nil
	case errors.Is(err, controlplane.ErrOrgNotifyUnsupported):
		logging.WarnContext(ctx, "The control plane cannot email the org's admins; the inbox still lists the rejected key",
			"orgID", n.OrgID, "accountID", n.AccountID, "error", err)
		return nil
	case errors.As(err, &refused) && refused.Permanent():
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("the control plane refused to email org %s's admins about key %s: %v", n.OrgID, n.AccountID, err),
			"OrgNoticeRefused", err)
	}
	return err
}

// noticeStarter starts a notice's workflow. client.Client is one.
type noticeStarter interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error)
}

// orgKeyNoticeWorkflowID names one incident: an org's key, rejected at one
// time. The control plane deduplicates on the same triple.
func orgKeyNoticeWorkflowID(n controlplane.OrgKeyRejectedNotice) string {
	return "org-key-rejected/" + n.OrgID + "/" + n.AccountID + "/" + n.IncidentKey
}

// startOrgKeyNotice starts the notice's workflow. A start for an incident
// already started is the same notice, and succeeds.
func startOrgKeyNotice(ctx context.Context, starter noticeStarter, taskQueue string, n controlplane.OrgKeyRejectedNotice) error {
	_, err := starter.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        orgKeyNoticeWorkflowID(n),
		TaskQueue: taskQueue,
		// A notice that gave up after a day may be asked for again (a later
		// rejection of the same mark); a delivered one never.
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}, OrgKeyRejectedNoticeWorkflowName, n)
	var started *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &started) {
		return nil
	}
	return err
}

// orgKeyNoticeQueueFor is where this worker starts notices: its own task
// queue, through its Temporal client. Unset without a client.
func orgKeyNoticeQueueFor(cfg *Config) orgKeyNoticeQueue {
	if cfg.TemporalClient == nil {
		return orgKeyNoticeQueue{}
	}
	return orgKeyNoticeQueue{starter: cfg.TemporalClient, taskQueue: cfg.taskQueueName()}
}

// orgKeyNotice is the notice of acct's current mark, in orgID.
func orgKeyNotice(orgID string, acct core.ProviderAccount, reason string) controlplane.OrgKeyRejectedNotice {
	return controlplane.OrgKeyRejectedNotice{
		OrgID: orgID, IncidentKey: acct.NeedsReauthAt.UTC().Format(time.RFC3339Nano), AccountID: acct.ID,
		Provider: models.ProviderDisplayName(acct.Provider), Label: acct.Label, KeyHint: acct.KeyHint, Reason: reason,
	}
}

// orgKeyNoticeConfirmer records that a notice's workflow was accepted.
type orgKeyNoticeConfirmer interface {
	ConfirmOrgKeyNotice(ctx context.Context, orgID, accountID string, markedAt time.Time) error
}

// startAndConfirmOrgKeyNotice starts n's workflow and, once Temporal has
// accepted it (or already had it), records that on the account. Either step
// failing leaves the mark unconfirmed, so it is started again later.
func startAndConfirmOrgKeyNotice(ctx context.Context, store orgKeyNoticeConfirmer, q orgKeyNoticeQueue, n controlplane.OrgKeyRejectedNotice) error {
	markedAt, err := time.Parse(time.RFC3339Nano, n.IncidentKey)
	if err != nil {
		return fmt.Errorf("read the incident of the notice: %w", err)
	}
	if err := startOrgKeyNotice(ctx, q.starter, q.taskQueue, n); err != nil {
		return fmt.Errorf("start the notice: %w", err)
	}
	if err := store.ConfirmOrgKeyNotice(ctx, n.OrgID, n.AccountID, markedAt); err != nil {
		return fmt.Errorf("record that the notice started: %w", err)
	}
	return nil
}

// OrgKeyNoticeSweepInterval is how often a worker starts the notices whose
// start was never confirmed: a Temporal outage at the moment of the
// rejection delays the email by at most this.
const OrgKeyNoticeSweepInterval = 10 * time.Minute

// orgKeyNoticeSweepBatch bounds one sweep's starts.
const orgKeyNoticeSweepBatch = 100

// orgKeyNoticeSweepLease is how long a sweep's claim on a mark holds: a start
// that failed is retried about hourly, while every other due mark gets its turn
// first (store.ClaimDueOrgKeyNotices).
const orgKeyNoticeSweepLease = time.Hour

// sweepOrgKeyNotices claims the due rejected org keys whose notice's start was
// never confirmed (ClaimDueOrgKeyNotices) and starts each, and reports how many
// it started. The reason is not kept
// with the mark beyond needs_reauth_reason, which is what it sends.
func sweepOrgKeyNotices(ctx context.Context, store OrgKeyNoticeStore, q orgKeyNoticeQueue) int {
	pending, err := store.ClaimDueOrgKeyNotices(ctx, orgKeyNoticeSweepBatch, orgKeyNoticeSweepLease)
	if err != nil {
		logging.WarnContext(ctx, "Could not list the rejected org keys whose notice never started", "error", err)
		return 0
	}
	started := 0
	for _, acct := range pending {
		if acct.NeedsReauthAt == nil || acct.OrgID == "" {
			continue
		}
		if err := startAndConfirmOrgKeyNotice(ctx, store, q, orgKeyNotice(acct.OrgID, acct, acct.NeedsReauthReason)); err != nil {
			logging.WarnContext(ctx, "Could not start a rejected org key's notice; the next sweep retries it",
				"orgID", acct.OrgID, "accountID", acct.ID, "error", err)
			continue
		}
		started++
	}
	if started > 0 {
		logging.InfoContext(ctx, "Started the notices of rejected org keys whose start was never confirmed", "started", started)
	}
	return started
}

// runOrgKeyNoticeSweep sweeps at once and every interval until ctx ends.
func runOrgKeyNoticeSweep(ctx context.Context, store OrgKeyNoticeStore, q orgKeyNoticeQueue, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		sweepOrgKeyNotices(ctx, store, q)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
