package release

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ─── GateStatus ──────────────────────────────────────────────────────────────

// GateStatus is the verdict a check reported. CLOSED: see the package doc.
//
// There are two parse paths on purpose, because a status arrives from two
// very different places:
//
//   - [ParseGateStatus] is the WRITE path — a --gate flag, an RPC request.
//     It refuses anything outside the set, so a typo fails before the entry
//     is recorded.
//   - [GateStatusFromStored] is the READ path — a ledger line, a database
//     row. Entries written before the set was closed carry free text, and
//     refusing to read them would make old promotions unrenderable. They
//     map to [GateStatusError] and the raw value is kept verbatim on
//     [Gate.RawStatus].
type GateStatus string

const (
	// GateStatusPassed: the check ran and found nothing wrong.
	GateStatusPassed GateStatus = "passed"
	// GateStatusFailed: the check ran and found something wrong. This is a
	// VERDICT, not an error — the check itself worked.
	GateStatusFailed GateStatus = "failed"
	// GateStatusSkipped: the check did not apply. A hosted smoke with no
	// URL-bearing workload is skipped, never passed: a green check that
	// checked nothing is the failure mode this value exists to prevent.
	GateStatusSkipped GateStatus = "skipped"
	// GateStatusError: the check could not reach a verdict — it crashed,
	// timed out, or could not look. Distinct from failed for the same
	// reason exit code 2 is distinct from exit code 1.
	GateStatusError GateStatus = "error"
)

// GateStatuses is the closed set, in a stable order (for error messages and
// for the hosted schema's CHECK, which must list exactly these).
var GateStatuses = []GateStatus{GateStatusPassed, GateStatusFailed, GateStatusSkipped, GateStatusError}

// Valid reports whether s is one of [GateStatuses].
func (s GateStatus) Valid() bool {
	for _, known := range GateStatuses {
		if s == known {
			return true
		}
	}
	return false
}

func gateStatusNames() []string {
	out := make([]string, len(GateStatuses))
	for i, s := range GateStatuses {
		out[i] = string(s)
	}
	return out
}

// ParseGateStatus reads a status a CALLER supplied — a `--gate` flag, an RPC
// request field — and refuses anything outside the closed set. Empty is
// refused too: a check that reported no verdict has not reported.
func ParseGateStatus(s string) (GateStatus, error) {
	if status := GateStatus(s); status.Valid() {
		return status, nil
	}
	return "", fmt.Errorf("%w: unknown gate status %q (expected one of %s)",
		ErrInvalid, s, strings.Join(gateStatusNames(), ", "))
}

// GateStatusFromStored reads a STORED status. It returns the status to
// display and, when the stored value was NOT in the closed set, that value
// verbatim so nothing is lost. An unrecognised status displays as
// [GateStatusError]: a claim nobody can interpret must not render as a pass.
//
// An EMPTY stored status is not mapped. Absent is absent, not an error
// verdict: a gate that recorded no status made no claim, and inventing one
// for it would turn a hole in the data into a reported outcome. It comes
// back empty, which [Gate.Validate] refuses — the same rule as a missing
// artifact kind.
func GateStatusFromStored(stored string) (status GateStatus, raw string) {
	if stored == "" {
		return "", ""
	}
	if parsed, err := ParseGateStatus(stored); err == nil {
		return parsed, ""
	}
	return GateStatusError, stored
}

// UnmarshalJSON decodes through [ParseGateStatus]. It is STRICT, because a
// bare GateStatus is only decoded where a caller supplied one; a stored
// entry is decoded through [Gate.UnmarshalJSON], which is lenient.
func (s *GateStatus) UnmarshalJSON(data []byte) error {
	return decodeClosed(data, "gate status", func(v string) bool { return GateStatus(v).Valid() }, gateStatusNames(), (*string)(s))
}

// ─── Gate ────────────────────────────────────────────────────────────────────

// Gate is one check's result, attached to a promotion. EVIDENCE, not
// enforcement: a gate that must block a promotion is checked BEFORE the
// entry is written, because a recorded claim proves only that it was made,
// and by whom.
//
// A gate carries timing, so a gate IS a run stage (see [Stage]): everything
// a run does that is not derivable from the ledger — build, lint, test,
// smoke, wait — is recorded as one of these.
type Gate struct {
	// Name is the check: "lint", "test", "smoke", "rollout", "qa-signoff".
	Name string `json:"name"`
	// Status is the verdict, from the closed set.
	Status GateStatus `json:"status"`
	// RawStatus is the stored status when it was NOT in the closed set —
	// free text from a ledger written before the set closed. Empty on
	// every gate whose status is recognised, so a non-empty value is
	// exactly the signal "this entry predates the vocabulary".
	// Set only by the READ path; never sent.
	RawStatus string `json:"raw_status,omitempty"`
	// URL is where the full report lives (a CI run, an artifact).
	URL string `json:"url,omitempty"`
	// Summary is one line: "412 passed, 0 failed, 3 skipped".
	Summary string `json:"summary,omitempty"`
	// StartedAt and FinishedAt are the check's own window, not when it was
	// recorded. They are what makes a gate a run stage with a duration.
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// RunID ties this check to one [Run]. Optional: a manual sign-off
	// belongs to a promotion and to no run.
	RunID string `json:"run_id,omitempty"`
	// Details is small, structured and optional — counts and verdicts, not
	// logs. A hosted backend caps it (8 KiB); the full report is behind
	// URL.
	Details map[string]any `json:"details,omitempty"`
	// RecordedBy and RecordedAt are SET BY THE BACKEND that recorded the
	// gate, from the authenticated principal — a user id, or
	// "token:<token-id>". Never accepted from a request, because evidence
	// whose author the author chose is not attributable. A file ledger
	// leaves them empty: it has no principal to attest to.
	RecordedBy string     `json:"recorded_by,omitempty"`
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
}

// gateJSON is Gate with a plain-string status, so the lenient read path can
// see the raw value before it is mapped.
type gateJSON struct {
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	RawStatus  string         `json:"raw_status,omitempty"`
	URL        string         `json:"url,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	StartedAt  *time.Time     `json:"started_at,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	RunID      string         `json:"run_id,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
	RecordedBy string         `json:"recorded_by,omitempty"`
	RecordedAt *time.Time     `json:"recorded_at,omitempty"`
}

// UnmarshalJSON is the LENIENT read path: a status outside the closed set
// becomes [GateStatusError] with the original kept on RawStatus, so a
// promotion recorded before the vocabulary existed still reads back. Use
// [Gate.Validate] to refuse writing one.
func (g *Gate) UnmarshalJSON(data []byte) error {
	var raw gateJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%w: gate: %v", ErrInvalid, err)
	}
	status, unrecognised := GateStatusFromStored(raw.Status)
	*g = Gate{
		Name: raw.Name, Status: status, RawStatus: unrecognised,
		URL: raw.URL, Summary: raw.Summary,
		StartedAt: raw.StartedAt, FinishedAt: raw.FinishedAt,
		RunID: raw.RunID, Details: raw.Details,
		RecordedBy: raw.RecordedBy, RecordedAt: raw.RecordedAt,
	}
	// An explicit raw_status on the wire wins: a backend that already did
	// the mapping says so, and re-deriving it would discard its answer.
	if raw.RawStatus != "" {
		g.RawStatus = raw.RawStatus
	}
	return nil
}

// Validate is the WRITE path. It refuses a gate that cannot be recorded: no
// name, a status outside the closed set, a finish before its start, or a
// status that only survived a read because it was mapped.
func (g Gate) Validate() error {
	switch {
	case strings.TrimSpace(g.Name) == "":
		return fmt.Errorf("%w: gate name is required", ErrInvalid)
	case g.RawStatus != "":
		return fmt.Errorf("%w: gate %q carries unrecognised status %q (expected one of %s)",
			ErrInvalid, g.Name, g.RawStatus, strings.Join(gateStatusNames(), ", "))
	case !g.Status.Valid():
		return fmt.Errorf("%w: gate %q: unknown status %q (expected one of %s)",
			ErrInvalid, g.Name, g.Status, strings.Join(gateStatusNames(), ", "))
	case g.StartedAt != nil && g.FinishedAt != nil && g.FinishedAt.Before(*g.StartedAt):
		return fmt.Errorf("%w: gate %q finished (%s) before it started (%s)",
			ErrInvalid, g.Name, g.FinishedAt.Format(time.RFC3339), g.StartedAt.Format(time.RFC3339))
	}
	return nil
}
