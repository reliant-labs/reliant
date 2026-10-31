// Copyright (c) 2025 Reliant Labs
package automationpolicy

import (
	"context"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// ApprovalSettingKey is the setting that says when an agent's change to a
// trigger activation needs the owner's approval card. A row with no project is
// the owner's account-wide choice; a row for a project overrides it there.
const ApprovalSettingKey = "automation.approvals"

// ApprovalMode is the value of ApprovalSettingKey.
type ApprovalMode string

const (
	// ApprovalAlways asks for a card on every create, update and enable.
	ApprovalAlways ApprovalMode = "always"
	// ApprovalOnCapabilityChange asks only when a change widens the
	// activation's power (Capabilities.Widened); everything else is applied
	// and recorded in the activation's audit log. The default.
	ApprovalOnCapabilityChange ApprovalMode = "on_capability_change"
	// ApprovalNever applies every change without a card and records it.
	ApprovalNever ApprovalMode = "never"
)

// DefaultApprovalMode applies when neither the project nor the account sets one.
const DefaultApprovalMode = ApprovalOnCapabilityChange

// ParseApprovalMode reads a stored value. A value this server does not know
// fails closed to ApprovalAlways: a typo must not be read as "never".
func ParseApprovalMode(value string) ApprovalMode {
	switch mode := ApprovalMode(strings.TrimSpace(value)); mode {
	case ApprovalAlways, ApprovalOnCapabilityChange, ApprovalNever:
		return mode
	case "":
		return DefaultApprovalMode
	default:
		return ApprovalAlways
	}
}

// ResolveApprovalMode is the mode that governs a change touching projects:
// each project's own setting, else the account's, else the default. A change
// that moves an activation between projects gets the stricter of the two.
// settings are the owner's ApprovalSettingKey rows across every scope.
func ResolveApprovalMode(settings []*core.Setting, projects ...string) ApprovalMode {
	account := DefaultApprovalMode
	byProject := map[string]ApprovalMode{}
	for _, s := range settings {
		if s == nil || s.Key != ApprovalSettingKey {
			continue
		}
		if s.ProjectID == nil || *s.ProjectID == "" {
			account = ParseApprovalMode(s.Value)
			continue
		}
		byProject[*s.ProjectID] = ParseApprovalMode(s.Value)
	}
	var mode ApprovalMode
	for _, project := range projects {
		m, ok := byProject[project]
		if !ok {
			m = account
		}
		if mode == "" {
			mode = m
		} else {
			mode = mode.Stricter(m)
		}
	}
	if mode == "" {
		return account
	}
	return mode
}

// strictness orders modes so the stricter of two can be chosen.
func (m ApprovalMode) strictness() int {
	switch m {
	case ApprovalNever:
		return 0
	case ApprovalOnCapabilityChange:
		return 1
	default:
		return 2
	}
}

// Stricter is whichever of m and other asks for a card more often.
func (m ApprovalMode) Stricter(other ApprovalMode) ApprovalMode {
	if other.strictness() > m.strictness() {
		return other
	}
	return m
}

// Capabilities is the set of powers an activation holds, each one a stable,
// display-safe item such as "connection:github/conn_1" or "machine:d_1". Items
// are compared as strings, so two activations with the same items hold the same
// power for the purposes of approval.
//
// Kinds (the part before the first colon):
//   - connection: an integration connection the activation or one of its
//     fixed action nodes authenticates with
//   - scope: a scope granted to the activation's own connection
//   - identity: who integration writes act as (owner or app)
//   - machine: the machine the activation runs on (absent when it needs none)
//   - secret: a credential the closure is handed — a hosted use grant, or a
//     shell step that inherits the machine's credentials
//   - deploy: an unattended deploy step to a production environment, where the
//     engine can read one from a fixed command
type Capabilities map[string]bool

// Add records items; empty strings are ignored.
func (c Capabilities) Add(items ...string) {
	for _, item := range items {
		if item != "" {
			c[item] = true
		}
	}
}

// Sorted lists the items in a stable order.
func (c Capabilities) Sorted() []string {
	out := make([]string, 0, len(c))
	for item := range c {
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

// Widened is every item c holds that base does not.
func (c Capabilities) Widened(base Capabilities) []string {
	var out []string
	for item := range c {
		if !base[item] {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

// Operation is what an agent asked to do to an activation.
type Operation int

const (
	OperationCreate Operation = iota
	OperationUpdate
	OperationEnable
	OperationPause
)

// ApprovalDecision says whether a change needs the owner's card and why.
type ApprovalDecision struct {
	Mode ApprovalMode
	// CardRequired: the change applies only with an approved card.
	CardRequired bool
	// Reason is one line for the card or the agent: why a card is or is not
	// needed. Display-safe (it names capability items, never values).
	Reason string
	// Added and Removed are the capability items the change grants and drops.
	Added, Removed []string
}

// Decide applies mode to one change. base is the activation's capabilities
// before the change and nil when they are unknown (a create, or a saved
// activation whose current configuration could not be resolved), which
// ApprovalOnCapabilityChange treats as widening. Pausing only reduces power,
// so it never needs a card, whatever the mode.
func Decide(mode ApprovalMode, op Operation, base, next Capabilities) ApprovalDecision {
	d := ApprovalDecision{Mode: mode}
	if next != nil {
		d.Added = next.Widened(base)
	}
	if base != nil {
		d.Removed = base.Widened(next)
	}
	switch {
	case op == OperationPause:
		d.Reason = "Pausing only reduces an activation's power; applied without a card and recorded."
	case mode == ApprovalNever:
		d.Reason = "Automation approvals are set to never; applied without a card and recorded."
	case mode == ApprovalAlways:
		d.CardRequired = true
		d.Reason = "Automation approvals are set to always: every activation change needs a card."
	case op == OperationCreate:
		d.CardRequired = true
		d.Reason = "A new activation is new standing power: it needs a card (automation approvals: on capability change)."
	case base == nil:
		d.CardRequired = true
		d.Reason = "The activation's current capabilities could not be resolved, so this change is treated as widening its power."
	case len(d.Added) > 0:
		d.CardRequired = true
		d.Reason = "This change widens the activation's power: " + strings.Join(d.Added, ", ") + "."
	default:
		d.Reason = "This change does not widen the activation's power; applied without a card and recorded (automation approvals: on capability change)."
	}
	return d
}

// Approval is how an applied change was approved, for its audit entry.
type Approval struct {
	// How: "card" (the owner approved a card), "auto:<mode>" (the policy
	// approved it), or "auto:pause".
	How            string
	Added, Removed []string
}

type approvalKey struct{}

// WithApproval attributes the audit entry written in ctx to an approval.
func WithApproval(ctx context.Context, a Approval) context.Context {
	return context.WithValue(ctx, approvalKey{}, a)
}

// ApprovalFrom is the approval attributed in ctx, or the zero Approval for a
// change the owner made directly in the app.
func ApprovalFrom(ctx context.Context) Approval {
	a, _ := ctx.Value(approvalKey{}).(Approval)
	return a
}

// AuditHow is the Approval.How recorded for d once the change is applied.
func (d ApprovalDecision) AuditHow(op Operation) string {
	switch {
	case d.CardRequired:
		return "card"
	case op == OperationPause:
		return "auto:pause"
	default:
		return "auto:" + string(d.Mode)
	}
}
