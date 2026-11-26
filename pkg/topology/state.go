package topology

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ImageState is one image's reconciliation state in a topology report.
//
// It is the five verification verdicts `forge env status <env>` produces plus
// a SIXTH that only a topology report can be in: [NotVerified]. The sixth is
// not a verdict at all — it is the absence of one — which is why it is a
// distinct state rather than a default of the others.
//
// THE ZERO VALUE IS NotVerified, DELIBERATELY. A struct that was never
// populated reads as "not checked" rather than as a clean bill of health.
// UnmarshalJSON still refuses unknown strings: a newer forge with a seventh
// state must fail loudly here instead of decoding into whatever this binary's
// zero value happens to mean.
type ImageState int

const (
	// NotVerified: the ledger declares this digest and nothing has been
	// compared against a running system. Says NOTHING about what is running.
	NotVerified ImageState = iota
	// Match: the running digest is the declared one.
	Match
	// Drift: something else is running.
	Drift
	// Missing: the image is declared and nothing runs it.
	Missing
	// Untagged: a workload runs a mutable tag, so the digest cannot be proven.
	Untagged
	// Unreachable: the system could not be read (context, credentials,
	// network). Nothing was learned.
	Unreachable
)

// String renders the fixed-width label the CLI's text report uses.
func (s ImageState) String() string {
	switch s {
	case NotVerified:
		return "NOT-VERIFIED"
	case Match:
		return "MATCH"
	case Drift:
		return "DRIFT"
	case Missing:
		return "MISSING"
	case Untagged:
		return "UNTAGGED"
	case Unreachable:
		return "UNREACHABLE"
	default:
		return "UNKNOWN"
	}
}

// MarshalJSON emits the lowercase, underscore-separated string form, derived
// from String() so the text column and the JSON value cannot disagree about
// which state a cell is in.
func (s ImageState) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strings.ReplaceAll(strings.ToLower(s.String()), "-", "_") + `"`), nil
}

// UnmarshalJSON reads the string form back. An unrecognized value is an ERROR:
// decoding it as a default would turn a state this binary does not understand
// into one it does, and every wrong guess in this type's direction ends as a
// green cell over an unchecked environment.
func (s *ImageState) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return fmt.Errorf("topology image state must be a string: %w", err)
	}
	switch strings.ToLower(name) {
	case "not_verified":
		*s = NotVerified
	case "match":
		*s = Match
	case "drift":
		*s = Drift
	case "missing":
		*s = Missing
	case "untagged":
		*s = Untagged
	case "unreachable":
		*s = Unreachable
	default:
		return fmt.Errorf("unknown topology image state %q (expected not_verified, match, drift, missing, untagged or unreachable) — "+
			"refusing to decode it as a default, which would report an unchecked environment as clean", name)
	}
	return nil
}
