package release

// The promotion checks an env's delivery policy declares beside the health
// gate: forge.RequiredGates and forge.ApprovalHook. The types and rules here
// are shared by every place that enforces them — forge for an env whose
// ledger is on this machine, the control plane for an env it holds — so the
// two cannot disagree about what a passed gate or an approval is.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// The names the enforcing ledger records its own verdicts under, on the
// promotion. RESERVED: a caller-supplied gate of either name is refused where
// the policy declares the check, because a caller that could write it could
// forge the ledger's own verdict.
const (
	RequiredGatesGateName = "required_gates"
	ApprovalHookGateName  = "approval_hook"
)

// The refusal reasons of the two checks: the codes a pipeline branches on.
const (
	// RefusalRequiredGateMissing: a required gate is not on the promotion.
	RefusalRequiredGateMissing = "required_gate_missing"
	// RefusalRequiredGateFailed: a required gate is on the promotion with a
	// status other than passed.
	RefusalRequiredGateFailed = "required_gate_failed"
	// RefusalApprovalDenied: the approval hook said deny — or timed out,
	// failed, or answered something unreadable, which enforce reads as deny.
	RefusalApprovalDenied = "approval_denied"
	// RefusalApprovalPending: the approval hook has not decided yet. The one
	// refusal of the two checks that waiting (and a retry) fixes.
	RefusalApprovalPending = "approval_pending"
)

// requiredGateNameRE is a gate name a policy can require: what a CI step would
// call its check. Bounded and printable, so it reads safely in a refusal.
var requiredGateNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$`)

// ValidateRequiredGateName refuses a name no gate could carry, or one the
// ledger records its own verdicts under.
func ValidateRequiredGateName(name string) error {
	if !requiredGateNameRE.MatchString(name) {
		return fmt.Errorf("%q is not a gate name (letters, digits, . _ : / -, at most 64)", name)
	}
	switch name {
	case RequiredGatesGateName, ApprovalHookGateName, "health_gate":
		return fmt.Errorf("%q is the name the ledger records its own verdict under; require the check by its own name", name)
	}
	return nil
}

// ApprovalHookMaxTimeoutSeconds bounds one ask. A promote waits on the
// answer, so an approval that takes a person longer answers pending and the
// pipeline retries.
const ApprovalHookMaxTimeoutSeconds = 60

func (h ShapeApprovalHook) validate() error {
	if !h.Mode.Valid() {
		return fmt.Errorf("mode %q (expected off, audit or enforce)", h.Mode)
	}
	if h.TimeoutSeconds < 1 || h.TimeoutSeconds > ApprovalHookMaxTimeoutSeconds {
		return fmt.Errorf("timeout_seconds %d (expected 1..%d)", h.TimeoutSeconds, ApprovalHookMaxTimeoutSeconds)
	}
	switch {
	case h.URL != "" && len(h.Argv) > 0:
		return fmt.Errorf("declares both url and argv; name one")
	case h.URL == "" && len(h.Argv) == 0:
		return fmt.Errorf("declares neither url nor argv; name the endpoint or the command to ask")
	case h.URL != "":
		return ValidateApprovalHookURL(h.URL)
	}
	if strings.TrimSpace(h.Argv[0]) == "" {
		return fmt.Errorf("argv[0] is empty; name the command")
	}
	return nil
}

// ValidateApprovalHookURL accepts an https URL with a host, or an http one to
// a loopback host (a local approval service, a test). A URL carrying userinfo
// is refused: the shape is recorded in the ledger, so a credential in it
// would be published there.
func ValidateApprovalHookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("url %q is not an absolute URL", raw)
	}
	if u.User != nil {
		return fmt.Errorf("url %q carries credentials; the shape is recorded in the ledger, so authenticate the hook another way", u.Redacted())
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return nil
		}
	}
	return fmt.Errorf("url %q must be https (http only to localhost)", u.Redacted())
}

// ApprovalRequestSchema names the request document's version.
const ApprovalRequestSchema = "forge.approval-request/v1"

// ApprovalRequest is what an approval hook is asked: the promotion about to be
// written. It is the POST body of a URL hook and the stdin of an argv hook.
type ApprovalRequest struct {
	Schema  string `json:"schema"`
	Project string `json:"project,omitempty"`
	Env     string `json:"env"`
	// Release is the version being promoted.
	Release string `json:"release"`
	// FromEnv is the env it is promoted from (`promote --from`), if any.
	FromEnv string `json:"from_env,omitempty"`
	// Commit is the release's source commit, when recorded.
	Commit string `json:"commit,omitempty"`
	// Promoter is who is promoting, as the ledger knows them.
	Promoter string `json:"promoter,omitempty"`
	// Gates are the gates the promotion carries.
	Gates []Gate `json:"gates,omitempty"`
	// Note is the promotion's note.
	Note string `json:"note,omitempty"`
}

// The three decisions a hook may answer.
const (
	ApprovalApprove = "approve"
	ApprovalDeny    = "deny"
	ApprovalPending = "pending"
)

// ApprovalDecision is a hook's answer.
type ApprovalDecision struct {
	// Decision is approve, deny or pending.
	Decision string `json:"decision"`
	// Reason is shown in the refusal and recorded on the promotion.
	Reason string `json:"reason,omitempty"`
	// URL is where a person can see or act on the approval.
	URL string `json:"url,omitempty"`
}

// maxApprovalReason bounds the reason recorded on the promotion.
const maxApprovalReason = 500

// ParseApprovalDecision reads a hook's answer. Anything that is not a JSON
// object whose decision is one of the three is an error, which an enforcing
// caller reads as deny: an answer nobody can interpret must not read as
// approval.
func ParseApprovalDecision(body []byte) (ApprovalDecision, error) {
	var d ApprovalDecision
	if err := json.Unmarshal(body, &d); err != nil {
		return ApprovalDecision{}, fmt.Errorf("the hook's answer is not a JSON decision: %v", err)
	}
	d.Decision = strings.ToLower(strings.TrimSpace(d.Decision))
	switch d.Decision {
	case ApprovalApprove, ApprovalDeny, ApprovalPending:
	default:
		return ApprovalDecision{}, fmt.Errorf("the hook answered decision %q (expected approve, deny or pending)", d.Decision)
	}
	d.Reason = strings.TrimSpace(d.Reason)
	if len(d.Reason) > maxApprovalReason {
		d.Reason = d.Reason[:maxApprovalReason-3] + "..."
	}
	return d, nil
}

// RequiredGatesVerdict is how a promotion's gates meet a requirement.
type RequiredGatesVerdict struct {
	// Missing are required names no gate on the promotion carries.
	Missing []string
	// Failed are required names a gate carries with a status other than
	// passed, as "name=status".
	Failed []string
}

// OK reports whether every required gate is carried, passed.
func (v RequiredGatesVerdict) OK() bool { return len(v.Missing) == 0 && len(v.Failed) == 0 }

// Reason is the refusal reason for a verdict that is not OK: failed before
// missing, because a failure is the more serious fact.
func (v RequiredGatesVerdict) Reason() string {
	if len(v.Failed) > 0 {
		return RefusalRequiredGateFailed
	}
	return RefusalRequiredGateMissing
}

// Summary is one line for a refusal and the recorded gate.
func (v RequiredGatesVerdict) Summary() string {
	var parts []string
	if len(v.Failed) > 0 {
		parts = append(parts, "not passed: "+strings.Join(v.Failed, ", "))
	}
	if len(v.Missing) > 0 {
		parts = append(parts, "missing: "+strings.Join(v.Missing, ", "))
	}
	if len(parts) == 0 {
		return "every required gate passed"
	}
	return strings.Join(parts, "; ")
}

// JudgeRequiredGates holds the gates a promotion carries to the names
// required. A name is met only when at least one gate carries it and every
// gate carrying it is passed: a re-run that passed beside one that failed is
// still a failure on the record the promotion would freeze, and the caller
// that wants it to count sends only the passing run.
func JudgeRequiredGates(required []string, gates []Gate) RequiredGatesVerdict {
	var v RequiredGatesVerdict
	for _, name := range required {
		var seen bool
		bad := ""
		for _, g := range gates {
			if g.Name != name {
				continue
			}
			seen = true
			if g.Status != GateStatusPassed && bad == "" {
				status := string(g.Status)
				if g.RawStatus != "" {
					status = g.RawStatus
				}
				bad = name + "=" + status
			}
		}
		switch {
		case !seen:
			v.Missing = append(v.Missing, name)
		case bad != "":
			v.Failed = append(v.Failed, bad)
		}
	}
	return v
}
