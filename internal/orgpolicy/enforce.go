// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"context"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ── THE AUDIT / ENFORCE SWITCH (§7, D12) ──────────────────────────────
//
// Two switches decide whether a use a policy does not admit is refused:
//
//   - the PLATFORM's, one per enforcement Point (the design's
//     orgs.policy_enforcement.<point>: ServerProcessConfig
//     org_policy_enforcement.<point>, env
//     RELIANT_ORGS_POLICY_ENFORCEMENT_<POINT>): off, audit or enforce. A new
//     point ships in audit and is flipped with the evidence its counts give;
//   - the POLICY's own, one per section (OrgPolicyMode): ENFORCE or AUDIT, so
//     an admin can trial a rule. UNSPECIFIED reads as AUDIT.
//
// A use is refused only when both say enforce. off never consults a policy
// at all; audit counts and logs what would have been refused and refuses
// nothing, whatever the section says. Either way the decision names the
// policy version it used.

// Point is one enforcement point: the place a policy section is applied.
type Point string

const (
	// PointIntegrations is the integrations section (§4.3): a run's use of a
	// connection, an integration trigger's routing and polling.
	PointIntegrations Point = "integrations"
)

// Enforcement is the platform's switch for one Point.
type Enforcement string

const (
	// EnforcementOff never consults a policy: today's behaviour exactly.
	EnforcementOff Enforcement = "off"
	// EnforcementAudit counts and logs what a policy would refuse, and
	// refuses nothing. A point's default.
	EnforcementAudit Enforcement = "audit"
	// EnforcementEnforce lets each section's own mode decide.
	EnforcementEnforce Enforcement = "enforce"
)

// ParseEnforcement reads a configured value. Anything but off or enforce is
// audit, the value that refuses nothing and still counts: config validation
// refuses an unknown value at load, so this is only a floor.
func ParseEnforcement(s string) Enforcement {
	switch Enforcement(strings.ToLower(strings.TrimSpace(s))) {
	case EnforcementOff:
		return EnforcementOff
	case EnforcementEnforce:
		return EnforcementEnforce
	}
	return EnforcementAudit
}

// switches is the process's Enforcement per Point, set once at boot.
var switches sync.Map // Point -> Enforcement

// SetProcessEnforcement installs the platform switch for point.
func SetProcessEnforcement(point Point, e Enforcement) {
	switches.Store(point, ParseEnforcement(string(e)))
}

// EnforcementOf is the platform switch for point: audit until a process
// installs one.
func EnforcementOf(point Point) Enforcement {
	if v, ok := switches.Load(point); ok {
		return v.(Enforcement)
	}
	return EnforcementAudit
}

// Admission is what a section says about one subject (an integration id, an
// MCP server name).
type Admission struct {
	// Governed is false when the policy has no such section: nothing is
	// judged, and nothing is counted.
	Governed bool
	// Admitted is the section's answer for the subject.
	Admitted bool
	// Mode is the section's own mode.
	Mode controlplanev1.OrgPolicyMode
}

// Decision is one enforcement decision.
type Decision struct {
	Point   Point
	OrgID   string
	Subject string
	// Reason is the refusal reason a denial carries
	// (ReasonIntegrationNotAllowed, ...).
	Reason string
	// Version is the policy version the decision was made with; 0 for none.
	Version int64
	// Denied is true when the policy does not admit the use, whether it is
	// refused or only counted.
	Denied bool
	// Refused is true when the use must not proceed.
	Refused bool
}

// Enforced reports whether a denial under this section mode would be refused
// at point: the platform switch is enforce and the section's mode is ENFORCE.
func Enforced(point Point, mode controlplanev1.OrgPolicyMode) bool {
	return EnforcementOf(point) == EnforcementEnforce && mode == controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE
}

// Judge decides one use of subject in orgID's runs at point. judge reads the
// section the point applies from the policy document. A use with no org, a
// point switched off and a policy that does not govern the point are
// admitted without asking anything further.
//
// The policy is read through For. When it cannot be read (ErrUnavailable,
// past Grace) the use is refused retryably only where the point enforces;
// under audit it proceeds, because audit refuses nothing.
//
// quiet suppresses the count and the log: for a caller that asks often
// without a use behind each answer (offering tools before every model turn),
// so audit counts stay one per use.
func Judge(ctx context.Context, point Point, orgID, subject, reason string, quiet bool, judge func(*controlplanev1.OrgPolicy) Admission) (Decision, error) {
	d := Decision{Point: point, OrgID: orgID, Subject: subject, Reason: reason}
	sw := EnforcementOf(point)
	if orgID == "" || sw == EnforcementOff {
		return d, nil
	}
	pol, err := For(ctx, orgID)
	if err != nil {
		if sw == EnforcementEnforce {
			return d, err
		}
		if !quiet {
			logging.WarnContext(ctx, "orgpolicy: the organization's policy cannot be read; the audited use proceeds",
				"point", string(point), "org_id", orgID, "subject", subject, "error", err)
		}
		return d, nil
	}
	d.Version = pol.Version
	adm := judge(pol.Doc)
	if !adm.Governed || adm.Admitted {
		return d, nil
	}
	d.Denied = true
	d.Refused = sw == EnforcementEnforce && adm.Mode == controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE
	if !quiet {
		record(ctx, d, pol.Stale)
	}
	return d, nil
}

// record counts and logs a denial: the metric is what an alert and the
// flip-to-enforce evidence read; the log names the exact policy version.
func record(ctx context.Context, d Decision, stale bool) {
	mode := "audit"
	if d.Refused {
		mode = "enforce"
	}
	denials().Add(ctx, 1, metric.WithAttributes(
		attribute.String("policy", string(d.Point)),
		attribute.String("reason", d.Reason),
		attribute.String("mode", mode),
	))
	msg := "orgpolicy: the organization's policy would refuse this use (audit: it proceeds)"
	if d.Refused {
		msg = "orgpolicy: the organization's policy refused this use"
	}
	logging.InfoContext(ctx, msg, "point", string(d.Point), "org_id", d.OrgID, "subject", d.Subject,
		"reason", d.Reason, "policy_version", d.Version, "mode", mode, "stale", stale)
}

var (
	denialsOnce sync.Once
	denialsCtr  metric.Int64Counter
)

// denials is the reliant.org_policy.denials counter, built on first use from
// the process's meter provider (otel's global delegates to the one
// observe.Setup installs, even when that runs later).
func denials() metric.Int64Counter {
	denialsOnce.Do(func() {
		var err error
		denialsCtr, err = otel.GetMeterProvider().Meter("github.com/reliant-labs/reliant/internal/orgpolicy").
			Int64Counter("reliant.org_policy.denials",
				metric.WithUnit("{use}"),
				metric.WithDescription("Uses an organization's policy did not admit, by policy, reason and mode (enforce refused it; audit only counted it)."))
		// An instrument the SDK refuses still comes back usable (a no-op).
		otel.Handle(err)
	})
	return denialsCtr
}
