package deploystate

import (
	"encoding/json"
	"fmt"
)

// Policy is what forge is permitted to do about a difference between
// what an environment declares and what is actually running.
//
// # Three values, and the default is deliberately the timid one
//
// Policy is an int enum precisely so its ZERO VALUE is [PolicyObserve].
// A Policy that arrives by any path — decoded from a file that predates
// this field, built as a struct literal in a test, returned by a backend
// that has not been taught about policy yet — reads as "report, change
// nothing" until something deliberately says otherwise. The failure mode
// of a forgotten field must be inaction.
//
// That is not timidity for its own sake. Silent correction during an
// incident, while an engineer is deliberately holding a thing together
// by hand, is the single behaviour that makes people rip these systems
// out — and they are right to, because at that moment the reconciler is
// not enforcing an invariant, it is fighting the only person who
// understands the situation. Converge is opt-in, per environment,
// forever.
type Policy int

const (
	// PolicyObserve reports drift and changes nothing. The zero value,
	// and the default for an environment that has never said otherwise.
	PolicyObserve Policy = iota

	// PolicyConverge permits forge to correct drift it detects. Opt-in.
	PolicyConverge

	// PolicyPinned refuses ALL changes, including ones a human asked
	// for through a normal deploy.
	//
	// Distinct from observe, and the distinction is the whole reason
	// there are three values rather than a bool. Observe says "I will
	// not act on my own initiative"; pinned says "nothing may change
	// this environment right now", which is what you set during a
	// freeze, an incident, or a migration you are driving by hand.
	// Collapsing them would mean the only way to stop a deploy is to
	// also stop drift REPORTING — losing the signal at the exact moment
	// it is most wanted.
	PolicyPinned
)

// String renders the policy for logs, files and JSON.
func (p Policy) String() string {
	switch p {
	case PolicyConverge:
		return "converge"
	case PolicyPinned:
		return "pinned"
	default:
		return "observe"
	}
}

// AllowsConverge reports whether forge may correct drift under this
// policy. A method rather than a `== PolicyConverge` at each call site,
// so that adding a fourth value later cannot silently leave a caller
// comparing against a stale set.
func (p Policy) AllowsConverge() bool { return p == PolicyConverge }

// AllowsChange reports whether ANY write to the target is permitted —
// an ordinary deploy as well as a reconcile. Only pinned says no.
func (p Policy) AllowsChange() bool { return p != PolicyPinned }

// ParsePolicy decodes the string form. The EMPTY STRING IS NOT AN ERROR:
// it decodes to PolicyObserve, which is what makes a policy file that
// predates this field, or a backend that returns nothing, land on the
// safe value rather than failing a pass.
//
// An unrecognized non-empty value IS an error. "converg" must not
// silently become observe — a user who typed a value meant to change
// behaviour, and a typo that reads as the default is indistinguishable
// from the setting having been applied.
func ParsePolicy(s string) (Policy, error) {
	switch s {
	case "", "observe":
		return PolicyObserve, nil
	case "converge":
		return PolicyConverge, nil
	case "pinned":
		return PolicyPinned, nil
	default:
		return PolicyObserve, fmt.Errorf(
			"deploystate: unknown reconcile policy %q (want observe, converge or pinned)", s)
	}
}

// MarshalJSON emits the string form, so a policy file is something a
// human can read and edit in an incident without a decoder ring.
func (p Policy) MarshalJSON() ([]byte, error) { return json.Marshal(p.String()) }

// UnmarshalJSON accepts the string form, including "" and JSON null,
// both of which yield PolicyObserve.
func (p *Policy) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*p = PolicyObserve
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("deploystate: reconcile policy must be a string: %w", err)
	}
	parsed, err := ParsePolicy(s)
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}

// policyFile is the on-disk shape of a per-environment policy. A struct
// rather than a bare string so the file has somewhere to grow a comment
// or a "set by" field without becoming a different format.
type policyFile struct {
	Policy Policy `json:"policy"`
}
