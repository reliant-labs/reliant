// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// A webhook trigger declared `source: alerting` is MANAGED on a deployment
// with a control plane: its sender is the control plane's alerting, which
// Reliant wires itself (triggers.Alerting). The user never sees a token —
// enabling provisions the receiver with a fresh one, disabling and deleting
// remove it, and rotating re-provisions. triggers.Alerting.Sweep converges
// whatever these writes miss. Without a control plane the trigger is a plain
// webhook and every path below is the ordinary one.

// AlertingProvisioner wires the control plane's alert receiver of a managed
// webhook trigger. *triggers.Alerting satisfies it.
type AlertingProvisioner interface {
	Manages(t *core.Trigger) bool
	Provision(ctx context.Context, t *core.Trigger) error
	Retire(ctx context.Context, triggerID string) error
}

// managed reports whether this deployment delivers to t itself.
func (s *Service) managed(t *core.Trigger) bool {
	return s.deps.Alerting != nil && s.deps.Alerting.Manages(t)
}

// provisionAlerting connects an enabled managed trigger to its org's alerts.
func (s *Service) provisionAlerting(ctx context.Context, t *core.Trigger) error {
	if err := s.deps.Alerting.Provision(ctx, t); err != nil {
		logging.WarnContext(ctx, "could not connect a trigger to its organization's alerts", "trigger_id", t.ID, "error", err)
		return alertingError(err)
	}
	return nil
}

// retireAlerting disconnects a managed trigger. Best effort: the trigger is
// already paused or gone, so a delivery the control plane still makes is
// refused, and the sweep removes the receiver on its next pass.
func (s *Service) retireAlerting(ctx context.Context, triggerID string) {
	if err := s.deps.Alerting.Retire(ctx, triggerID); err != nil {
		logging.WarnContext(ctx, "could not remove a trigger's alert receiver; the reconcile sweep removes it",
			"trigger_id", triggerID, "error", err)
	}
}

// alertingError is a provisioning failure as the person enabling sees it.
func alertingError(err error) error {
	var refused *controlplane.AutopilotError
	switch {
	case errors.As(err, &refused) && refused.Reason == controlplane.ReasonAutopilotNoOrg:
		return connect.NewError(connect.CodeFailedPrecondition, errors.New(
			"on-call receives your organization's alerts, and your account belongs to no organization yet"))
	case errors.Is(err, controlplane.ErrAutopilotUnsupported):
		return connect.NewError(connect.CodeUnavailable, errors.New(
			"this deployment's control plane cannot deliver alerts to automations yet"))
	case errors.As(err, &refused):
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("could not connect your organization's alerts: %s", refused.Message))
	default:
		return connect.NewError(connect.CodeUnavailable, errors.New("could not connect your organization's alerts right now; try again"))
	}
}

// Ensure *triggers.Alerting stays an AlertingProvisioner.
var _ AlertingProvisioner = (*triggers.Alerting)(nil)
