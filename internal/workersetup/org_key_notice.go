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
