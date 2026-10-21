// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// A webhook trigger declared `webhook: {source: alerting}` is delivered to by
// the control plane's alerting, not by a sender the user configures
// (research/feedback-loop/AUTOPILOT_TRIGGERS.md, option B). On a hosted
// deployment Alerting wires it: enabling the activation gives it a fresh token
// and hands that token, the trigger's URL and its project's scope to the
// control plane, which provisions an Autopilot alert receiver in the owner's
// organization; disabling or deleting it removes the receiver. The token is
// never shown to anyone.
//
// The writes happen where the user acts (TriggerService), and Sweep converges
// what they miss: a receiver whose activation is gone, paused, or no longer
// declared `source: alerting` is removed, and an enabled activation whose
// receiver is missing (a failed write, a declaration that just became
// `source: alerting`) is provisioned.

// AlertReceivers is the control plane's Autopilot receiver API.
// controlplane.Service satisfies it.
type AlertReceivers interface {
	UpsertAutopilotReceiver(ctx context.Context, r controlplane.AutopilotReceiver) error
	DeleteAutopilotReceiver(ctx context.Context, activationID string) error
	ListAutopilotReceivers(ctx context.Context) ([]string, error)
}

// AlertingStore is what provisioning reads and writes. *db.Repo satisfies it.
type AlertingStore interface {
	SetTriggerWebhookTokenHash(ctx context.Context, id string, hash []byte) error
	GetProject(ctx context.Context, id string) (*core.Project, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	ListAllWorkflowTriggerActivations(ctx context.Context) ([]*core.Trigger, error)
	TryAdvisoryLock(ctx context.Context, key int64) (release func(), ok bool, err error)
}

// AlertingGrace is how long Sweep leaves an enabled activation without a
// receiver before provisioning it: the user's own enable writes the row
// first and the receiver a moment later, and a sweep that provisioned in
// between would rotate the token under it.
const AlertingGrace = 10 * time.Minute

// alertingSweepLockKey makes one replica sweep at a time: two sweeps
// provisioning one activation could each rotate its token and leave the
// control plane holding the other's.
const alertingSweepLockKey int64 = 0x7265_6c69_616e_7402 // "reliant\x02"

// Alerting provisions and reconciles the control plane's alert receivers of
// `source: alerting` webhook triggers. A nil *Alerting (a deployment with no
// control plane) manages nothing: those triggers are plain webhooks.
type Alerting struct {
	receivers AlertReceivers
	store     AlertingStore
	publicURL string
	now       func() time.Time
}

// NewAlerting builds the provisioner. publicURL is this server's externally
// reachable base, which the control plane's Alertmanager posts to.
func NewAlerting(receivers AlertReceivers, store AlertingStore, publicURL string) *Alerting {
	return &Alerting{receivers: receivers, store: store, publicURL: publicURL, now: time.Now}
}

// IsAlertingWebhook reports a webhook trigger declared `source: alerting`.
func IsAlertingWebhook(t *core.Trigger) bool {
	if t == nil || t.Kind != core.TriggerKindWebhook {
		return false
	}
	cfg, err := WebhookConfigFor(t)
	return err == nil && cfg.Source == core.WebhookSourceAlerting
}

// Manages reports whether this deployment delivers to t itself.
func (a *Alerting) Manages(t *core.Trigger) bool { return a != nil && IsAlertingWebhook(t) }

// Provision gives t a fresh token and has the control plane deliver its org's
// alerts to it with that token. The control plane is written first: if it
// refuses, the stored token is untouched and a receiver it already held keeps
// working.
func (a *Alerting) Provision(ctx context.Context, t *core.Trigger) error {
	token, hash, err := NewWebhookToken()
	if err != nil {
		return err
	}
	projects, repos := a.scope(ctx, t.ProjectID)
	if err := a.receivers.UpsertAutopilotReceiver(ctx, controlplane.AutopilotReceiver{
		ActivationID: t.ID, UserID: t.UserID, URL: WebhookURL(a.publicURL, t.ID), Token: token,
		Projects: projects, Repos: repos,
	}); err != nil {
		return fmt.Errorf("provision alert receiver: %w", err)
	}
	if err := a.store.SetTriggerWebhookTokenHash(ctx, t.ID, hash); err != nil {
		return fmt.Errorf("store the alert receiver's token: %w", err)
	}
	logging.InfoContext(ctx, "alert receiver provisioned for a trigger", "trigger_id", t.ID, "user_id", t.UserID,
		"projects", projects, "repos", len(repos))
	return nil
}

// Retire removes t's receiver. Removing one that does not exist succeeds.
func (a *Alerting) Retire(ctx context.Context, triggerID string) error {
	if err := a.receivers.DeleteAutopilotReceiver(ctx, triggerID); err != nil {
		return fmt.Errorf("remove alert receiver: %w", err)
	}
	return nil
}

// scope is what the control plane routes to t: the environments of the
// project's forge project and of every repository it holds. A project whose
// facts cannot be read right now is scoped by what could be read; one with
// none at all receives every environment of the org, which a project with a
// remote never is.
func (a *Alerting) scope(ctx context.Context, projectID string) (projects, repos []string) {
	seen := map[string]bool{}
	addRepo := func(u *string) {
		if u == nil {
			return
		}
		if r := strings.TrimSpace(*u); r != "" && !seen[r] {
			seen[r] = true
			repos = append(repos, r)
		}
	}
	if p, err := a.store.GetProject(ctx, projectID); err != nil || p == nil {
		logging.WarnContext(ctx, "could not read a project to scope its alert receiver", "project_id", projectID, "error", err)
	} else {
		if p.ForgeProjectName != nil && strings.TrimSpace(*p.ForgeProjectName) != "" {
			projects = append(projects, strings.TrimSpace(*p.ForgeProjectName))
		}
		addRepo(p.RemoteURL)
	}
	if rs, err := a.store.ListReposByProject(ctx, projectID); err != nil {
		logging.WarnContext(ctx, "could not list a project's repositories to scope its alert receiver", "project_id", projectID, "error", err)
	} else {
		for _, r := range rs {
			addRepo(r.RemoteURL)
		}
	}
	return projects, repos
}

// AlertingReport is what one sweep did.
type AlertingReport struct {
	// Skipped is true when another replica held the sweep.
	Skipped              bool
	Receivers            int
	Removed, Provisioned int
}

// Sweep converges the control plane's receivers onto the enabled
// `source: alerting` activations. The receivers are listed BEFORE the
// activations: a receiver written after the listing is simply not seen,
// whereas one listed after an activation it belongs to was read would look
// orphaned and be removed.
func (a *Alerting) Sweep(ctx context.Context) (AlertingReport, error) {
	var report AlertingReport
	release, ok, err := a.store.TryAdvisoryLock(ctx, alertingSweepLockKey)
	if err != nil {
		return report, fmt.Errorf("alert receiver sweep lock: %w", err)
	}
	if !ok {
		report.Skipped = true
		return report, nil
	}
	defer release()

	have, err := a.receivers.ListAutopilotReceivers(ctx)
	if err != nil {
		if errors.Is(err, controlplane.ErrAutopilotUnsupported) {
			return report, nil // an older control plane: nothing to converge
		}
		return report, fmt.Errorf("list alert receivers: %w", err)
	}
	report.Receivers = len(have)
	activations, err := a.store.ListAllWorkflowTriggerActivations(ctx)
	if err != nil {
		return report, fmt.Errorf("list activations: %w", err)
	}
	want := map[string]*core.Trigger{}
	for _, t := range activations {
		if t.Enabled && IsAlertingWebhook(t) {
			want[t.ID] = t
		}
	}

	var errs []error
	held := map[string]bool{}
	for _, id := range have {
		held[id] = true
		if want[id] != nil {
			continue
		}
		if err := a.Retire(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("trigger %s: %w", id, err))
			continue
		}
		report.Removed++
		logging.InfoContext(ctx, "removed an alert receiver whose trigger is gone, paused or no longer alerting", "trigger_id", id)
	}
	cutoff := a.now().Add(-AlertingGrace)
	for id, t := range want {
		if held[id] || t.UpdatedAt.After(cutoff) {
			continue
		}
		if err := a.Provision(ctx, t); err != nil {
			var refused *controlplane.AutopilotError
			if errors.As(err, &refused) && refused.Reason == controlplane.ReasonAutopilotNoOrg {
				continue // the owner has no organization yet; nothing to receive
			}
			errs = append(errs, fmt.Errorf("trigger %s: %w", id, err))
			continue
		}
		report.Provisioned++
	}
	return report, errors.Join(errs...)
}
