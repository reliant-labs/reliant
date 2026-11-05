package cli

// An environment's managed identity (forge.ManagedIdentity), as its control
// plane reports it: the identity line of `forge env status <env>` and the
// "waiting on identity" state of `forge env deploy`'s readiness wait (FI7).
//
// THE CONTROL PLANE IS THE WITNESS. The instance is created and converged by
// the control plane's identity reconciler, which forge cannot see, so the line
// is read from IdentityService/GetIdentityStatus. That is also where "does
// this env declare one" is answered: the control plane reports it from the
// shape the env was last deployed with, which is exactly the declaration a
// deploy just recorded. An env it says declares none gets no line.
//
// IDENTITY GATES A DEPLOY ONLY ONCE IT IS KNOWN. A wait starts gating on
// identity after a read shows the env declares one. A read that fails before
// that (a control plane without the API, a credential that cannot read
// identity) is reported and never fails or holds a deploy whose rollout
// succeeded: forge could not tell whether there was anything to wait for.

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/internal/cloud"
)

// envIdentityState is the identity line's vocabulary.
type envIdentityState string

const (
	identityReady        envIdentityState = "ready"
	identityProvisioning envIdentityState = "provisioning"
	// identityDegraded: the instance serves, a delivery of its outputs does
	// not land.
	identityDegraded envIdentityState = "degraded"
	identityFailed   envIdentityState = "failed"
	// identityWithdrawn: the env no longer declares one; it is being removed.
	identityWithdrawn envIdentityState = "withdrawn"
	// identityUnknown: the control plane could not be asked.
	identityUnknown envIdentityState = "unknown"
)

// envIdentityReport is the identity line, as --json carries it.
type envIdentityReport struct {
	State envIdentityState `json:"state"`
	// Reason says why it is not ready: the reconciler's last error, a refusal,
	// or what it is waiting for.
	Reason string `json:"reason,omitempty"`
	// InstanceState is the reconciler's own state, lower-cased ("creating").
	InstanceState string `json:"instance_state,omitempty"`
	Issuer        string `json:"issuer,omitempty"`
	// terminal: only a new deploy can change it (the declaration was
	// refused), so a wait does not wait it out.
	terminal bool
}

// line is the one human line: "identity: ready", "identity: failed: <why>".
func (r envIdentityReport) line() string {
	switch r.State {
	case identityFailed, identityDegraded, identityUnknown:
		return "identity: " + string(r.State) + ": " + emptyOr(r.Reason, "no reason reported")
	}
	return "identity: " + string(r.State) + parenthesize(r.Reason)
}

// blocking reports whether the identity holds a deploy's readiness wait.
func (r envIdentityReport) blocking() bool {
	switch r.State {
	case identityProvisioning, identityDegraded, identityFailed:
		return true
	}
	return false
}

// failing reports whether it is a failure rather than progress.
func (r envIdentityReport) failing() bool {
	return r.State == identityFailed || r.State == identityDegraded
}

// identityReader reads GetIdentityStatus for an env, poll after poll. The org
// is the credential's own (cloud.ResolveOrganization), never a flag, and is
// resolved once.
func identityReader(client cloudCaller, envID string) func(ctx context.Context) (identityStatusDocument, error) {
	org := ""
	return func(ctx context.Context) (identityStatusDocument, error) {
		if org == "" {
			resolved, err := cloud.ResolveOrganization(ctx, client)
			if err != nil {
				return identityStatusDocument{}, err
			}
			org = resolved
		}
		var doc identityStatusDocument
		if err := client.Call(ctx, procIdentityStatus, map[string]any{"orgId": org, "environmentId": envID}, &doc); err != nil {
			return identityStatusDocument{}, err
		}
		return doc, nil
	}
}

// classifyIdentity maps one GetIdentityStatus answer onto the line. Nil means
// the env declares no identity (or the control plane cannot say it does), so
// there is nothing to report. Pure, so the table is tested without a server.
func classifyIdentity(doc identityStatusDocument) *envIdentityReport {
	if doc.Refused != "" {
		// Refused on the reconciler's latest sweep: the declaration as
		// deployed is wrong, whatever an earlier one created.
		return &envIdentityReport{State: identityFailed, Reason: "the control plane refused the declaration: " + doc.Refused,
			InstanceState: identityInstanceState(doc.State), Issuer: doc.Issuer, terminal: true}
	}
	if !doc.HasInstance {
		if doc.NotEnabledReason == "IDENTITY_NOT_ENABLED_REASON_PENDING" {
			return &envIdentityReport{State: identityProvisioning, Reason: "the identity reconciler has not created the instance yet"}
		}
		// Not declared, or no deployed shape to tell from. A control plane
		// that predates the reason field lands here too.
		return nil
	}
	r := &envIdentityReport{InstanceState: identityInstanceState(doc.State), Issuer: doc.Issuer}
	switch doc.State {
	case "IDENTITY_STATE_READY":
		r.State = identityReady
	case "IDENTITY_STATE_PENDING", "IDENTITY_STATE_CREATING", "IDENTITY_STATE_PROVISIONING":
		r.State, r.Reason = identityProvisioning, r.InstanceState
		if doc.LastError != "" {
			r.Reason += "; last error: " + doc.LastError
		}
	case "IDENTITY_STATE_DEGRADED":
		r.State, r.Reason = identityDegraded, emptyOr(failingDelivery(doc), doc.LastError)
	case "IDENTITY_STATE_FAILING":
		r.State, r.Reason = identityFailed, emptyOr(doc.LastError, failingDelivery(doc))
	case "IDENTITY_STATE_WITHDRAWN", "IDENTITY_STATE_GRACE", "IDENTITY_STATE_DELETING", "IDENTITY_STATE_DELETED":
		r.State, r.Reason = identityWithdrawn, r.InstanceState
	default:
		r.State, r.Reason = identityUnknown, "the control plane reports state "+emptyOr(doc.State, "unspecified")
	}
	return r
}

// failingDelivery names the first delivery that is not landing.
func failingDelivery(doc identityStatusDocument) string {
	for _, d := range doc.Deliveries {
		if d.State == "IDENTITY_DELIVERY_STATE_FAILING" {
			return fmt.Sprintf("delivery %s/%s/%s: %s", d.Cluster, d.Namespace, d.Name, emptyOr(d.LastError, "failing"))
		}
	}
	return ""
}

func identityInstanceState(wire string) string {
	return lowerWireEnum(wire, "IDENTITY_STATE_")
}

// identityWatch follows an env's identity across a wait's polls.
type identityWatch struct {
	read func(ctx context.Context) (identityStatusDocument, error)
	// tracked: a read showed the env declares one, so it gates.
	tracked bool
	// stopped: a read failed in a way polling will not change.
	stopped bool
	last    *envIdentityReport
}

// observe reads the identity once and returns the current line (nil: nothing
// to report).
func (w *identityWatch) observe(ctx context.Context) *envIdentityReport {
	if w == nil || w.read == nil || w.stopped {
		return w.current()
	}
	doc, err := w.read(ctx)
	if err != nil {
		if !w.tracked {
			// Never known to be declared: report, never gate.
			w.stopped = identityReadIsPermanent(err)
			w.last = &envIdentityReport{State: identityUnknown, Reason: "could not read it from the control plane: " + err.Error()}
			return w.last
		}
		// Known to be declared: keep the last state (it still gates) and
		// say the latest read failed.
		if w.last != nil {
			r := *w.last
			r.Reason = strings.TrimPrefix(r.Reason+"; latest read failed: "+err.Error(), "; ")
			return &r
		}
		return nil
	}
	w.last = classifyIdentity(doc)
	if w.last != nil && w.last.State != identityWithdrawn {
		w.tracked = true
	}
	return w.last
}

func (w *identityWatch) current() *envIdentityReport {
	if w == nil {
		return nil
	}
	return w.last
}

// gating reports whether the identity is known to be declared and not ready.
func (w *identityWatch) gating() bool {
	return w != nil && w.tracked && w.last != nil && w.last.blocking()
}

// identityReadIsPermanent: a control plane that does not serve the API, or a
// credential refused it, will not answer differently on the next poll.
func identityReadIsPermanent(err error) bool {
	for _, c := range []string{cloud.CodeUnimplemented, cloud.CodePermissionDenied, cloud.CodeUnauthenticated, cloud.CodeNotFound} {
		if hostedErrorHasCode(err, c) {
			return true
		}
	}
	return false
}
