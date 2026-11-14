// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ── Rules that make work stricter (§4.4, §4.6) ────────────────────────
//
// Not every rule refuses a use. The approval floors and forced
// `credentials: none` CHANGE the work instead: a floor raises how often
// approvals are asked for, a forced `credentials: none` withholds
// credentials. None of them can loosen what the member, the project or the
// step chose. Each is answered as a Change: whether the org's rule asks for
// one, and whether it is made (the platform switch and the section's own
// mode both enforce) or only counted.
//
// A policy that cannot be read (ErrUnavailable, past Grace) under an
// enforcing switch makes the stricter choice instead of refusing the work:
// never unconstrained, and the conversation goes on.

// The enforcement points of the rules below.
const (
	// PointApprovals is the approvals section (§4.4): the automation-approval
	// floor and the chat mode floor.
	PointApprovals Point = "approvals"
	// PointMachines is the machines section (§4.6): `credentials: none`
	// forced on the commands of an org's runs.
	PointMachines Point = "machines"
)

// Rule names: the reason label of each rule's count and log.
const (
	RuleAutomationApprovalFloor = "automation_approval_floor"
	RuleChatModeFloor           = "chat_mode_floor"
	RuleForceCredentialsNone    = "force_credentials_none" //nolint:gosec // G101: a rule's name, not a credential.
)

// Change is one rule's outcome at one point for one org's work.
type Change struct {
	Point Point
	Rule  string
	OrgID string
	// Version is the policy version the outcome was decided with; 0 when the
	// org has none, or its policy could not be read.
	Version int64
	// Applies: the org's rule asks for a change here, or its policy could
	// not be read while the point enforces.
	Applies bool
	// Enforced: the change is made. Applies without Enforced is audit.
	Enforced bool
	// Unavailable: the policy could not be read; Enforced is then the
	// stricter choice.
	Unavailable bool
	// stale: decided with the last good policy while control-plane could
	// not be reached (within Grace).
	stale bool
}

// Record counts and logs c when it changed, or would have changed,
// something: one count per change on the denials counter (mode enforce or
// audit, reason the rule), one log line naming the policy version. detail is
// extra log attributes (key, value pairs).
func (c Change) Record(ctx context.Context, detail ...any) {
	if !c.Applies {
		return
	}
	mode, reason := "audit", c.Rule
	if c.Enforced {
		mode = "enforce"
	}
	if c.Unavailable {
		reason += "_" + ReasonUnavailable
	}
	denials().Add(ctx, 1, metric.WithAttributes(
		attribute.String("policy", string(c.Point)),
		attribute.String("reason", reason),
		attribute.String("mode", mode),
	))
	msg := "orgpolicy: the organization's policy would change this (audit: it proceeds unchanged)"
	if c.Enforced {
		msg = "orgpolicy: the organization's policy changed this"
	}
	logging.InfoContext(ctx, msg, append([]any{"point", string(c.Point), "rule", c.Rule, "org_id", c.OrgID,
		"policy_version", c.Version, "mode", mode, "stale", c.stale, "unavailable", c.Unavailable}, detail...)...)
}

// change evaluates one rule of point for orgID's work. wants reports whether
// the org's document asks for a change, and the mode of the section that
// does.
func change(ctx context.Context, point Point, rule, orgID string, wants func(*controlplanev1.OrgPolicy) (bool, controlplanev1.OrgPolicyMode)) Change {
	c := Change{Point: point, Rule: rule, OrgID: orgID}
	sw := EnforcementOf(point)
	if orgID == "" || sw == EnforcementOff {
		return c
	}
	pol, err := For(ctx, orgID)
	if err != nil {
		c.Unavailable, c.Applies, c.Enforced = true, true, sw == EnforcementEnforce
		return c
	}
	c.Version, c.stale = pol.Version, pol.Stale
	applies, mode := wants(pol.Doc)
	if !applies {
		return c
	}
	c.Applies, c.Enforced = true, Enforced(point, mode)
	return c
}

// AutomationApprovalFloor is orgID's floor under the automation approvals of
// its triggers (§4.4): the least strict setting a member's or project's
// `automation.approvals` may resolve to. UNSPECIFIED is no floor. The floor is
// the one the rule asks for: the caller applies it only when the Change is
// Enforced, and otherwise records what it would have changed. A policy that
// cannot be read floors at ALWAYS, the strictest.
func AutomationApprovalFloor(ctx context.Context, orgID string) (controlplanev1.AutomationApprovalFloor, Change) {
	var floor controlplanev1.AutomationApprovalFloor
	c := change(ctx, PointApprovals, RuleAutomationApprovalFloor, orgID, func(doc *controlplanev1.OrgPolicy) (bool, controlplanev1.OrgPolicyMode) {
		a := doc.GetApprovals()
		floor = a.GetAutomationFloor()
		return floor != controlplanev1.AutomationApprovalFloor_AUTOMATION_APPROVAL_FLOOR_UNSPECIFIED, a.GetMode()
	})
	if c.Unavailable {
		floor = controlplanev1.AutomationApprovalFloor_AUTOMATION_APPROVAL_FLOOR_ALWAYS
	}
	return floor, c
}

// ChatNoAuto decides whether a chat of orgID set to `auto` runs as `manual`
// (§4.4, chat_mode_floor NO_AUTO). Enforced means coerce; `plan` is never
// coerced. A policy that cannot be read under enforce coerces.
func ChatNoAuto(ctx context.Context, orgID string) Change {
	return change(ctx, PointApprovals, RuleChatModeFloor, orgID, func(doc *controlplanev1.OrgPolicy) (bool, controlplanev1.OrgPolicyMode) {
		a := doc.GetApprovals()
		return a.GetChatModeFloor() == controlplanev1.ChatModeFloor_CHAT_MODE_FLOOR_NO_AUTO, a.GetMode()
	})
}

// ForceCredentialsNone decides whether the commands of orgID's runs run with
// `credentials: none` whatever the step or the model asked for (§4.6).
// Enforced means force. A policy that cannot be read under enforce forces.
func ForceCredentialsNone(ctx context.Context, orgID string) Change {
	return change(ctx, PointMachines, RuleForceCredentialsNone, orgID, func(doc *controlplanev1.OrgPolicy) (bool, controlplanev1.OrgPolicyMode) {
		m := doc.GetMachines()
		return m.GetForceCredentialsNone(), m.GetMode()
	})
}
