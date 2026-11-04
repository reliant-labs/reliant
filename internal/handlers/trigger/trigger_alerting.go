// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
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
	if deferActivationEffect(ctx, func(ctx context.Context) error { return s.provisionAlerting(ctx, t) }) {
		return nil
	}
	// Only a managed trigger reaches here, and managed needs Alerting; the
	// guard keeps that true for a caller that forgets to ask.
	if s.deps.Alerting == nil {
		return nil
	}
	// Provision's error is already what the person enabling is told; its
	// control-plane cause stays server-side.
	return svcerr.Wrap(s.deps.Alerting.Provision(ctx, t))
}

// retireAlerting disconnects a managed trigger. Best effort: the trigger is
// already paused or gone, so a delivery the control plane still makes is
// refused, and the sweep removes the receiver on its next pass.
func (s *Service) retireAlerting(ctx context.Context, triggerID string) {
	if deferActivationEffect(ctx, func(ctx context.Context) error { s.retireAlerting(ctx, triggerID); return nil }) {
		return
	}
	if s.deps.Alerting == nil {
		return
	}
	if err := s.deps.Alerting.Retire(ctx, triggerID); err != nil {
		logging.WarnContext(ctx, "could not remove a trigger's alert receiver; the reconcile sweep removes it",
			"trigger_id", triggerID, "error", err)
	}
}

// Ensure *triggers.Alerting stays an AlertingProvisioner.
var _ AlertingProvisioner = (*triggers.Alerting)(nil)
