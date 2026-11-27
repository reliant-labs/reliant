// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"encoding/json"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
)

// Verdict is queue_batches.verdict_json: what the queue decided about a
// batch, and the evidence. M11 adds `unenforced` and `display` beside these.
type Verdict struct {
	// Mode is the repository's mode when the batch was picked; Shadow is
	// mode == shadow, and a shadow batch never lands.
	Mode   core.PipelineRepoMode `json:"mode"`
	Shadow bool                  `json:"shadow,omitempty"`
	// MainSHA is the base branch's real tip when the batch was picked.
	// SimulatedBase is set when the batch was built on a shadow batch's
	// candidate (the would-be main) rather than on MainSHA.
	MainSHA       string `json:"main_sha,omitempty"`
	SimulatedBase bool   `json:"simulated_base,omitempty"`

	// The gate's verdict (the last attempt's).
	Class    GateClass `json:"class,omitempty"`
	Landable bool      `json:"landable"`
	// UnisolatedAccepted marks a landable pass outside an isolation
	// boundary, which the repository's gate.allow_unisolated accepts. The
	// database lets a runc batch land only with it.
	UnisolatedAccepted bool     `json:"unisolated_accepted,omitempty"`
	Isolation          string   `json:"isolation,omitempty"`
	Machine            string   `json:"machine,omitempty"`
	ReasonCode         string   `json:"reason_code,omitempty"`
	Reason             string   `json:"reason,omitempty"`
	FailingTests       []string `json:"failing_tests,omitempty"`
	InfraMatches       []string `json:"infra_matches,omitempty"`
	// Attempts are the earlier gates of this batch (an unproven one retried
	// on another member).
	Attempts []GateAttempt `json:"attempts,omitempty"`

	// WouldLand is a shadow batch's would-land set, in landing order.
	WouldLand []LandEntry `json:"would_land,omitempty"`
	// Ejected are the entries this batch ejected, with the evidence.
	Ejected []EjectedEntry `json:"ejected,omitempty"`
	// Requeued are entries the batch left waiting for a later batch.
	Requeued []string `json:"requeued,omitempty"`
	// Regenerated are entries kept by regenerating their generated files.
	Regenerated []RegenEntry `json:"regenerated,omitempty"`
	// WouldPush are pushes a shadow batch did not make (a regen commit to a
	// PR's branch).
	WouldPush []WouldPush `json:"would_push,omitempty"`
	// Flaky are the tests that failed and then passed on the same tree.
	Flaky []string `json:"flaky,omitempty"`
	// LandNotGranted are the entries whose approver no longer held the
	// land capability when the batch was about to land: the batch was
	// abandoned and nothing merged.
	LandNotGranted []LandRefusal `json:"land_not_granted,omitempty"`
	// Note says, for a person, what happened.
	Note string `json:"note,omitempty"`
}

// LandRefusal is an entry refused the land capability before its batch
// landed, and why.
type LandRefusal struct {
	EntryID string `json:"entry_id"`
	Head    string `json:"head"`
	Reason  string `json:"reason"`
}

// GateAttempt is one earlier gate of a batch.
type GateAttempt struct {
	Attempt    int       `json:"attempt"`
	Class      GateClass `json:"class"`
	Machine    string    `json:"machine,omitempty"`
	Isolation  string    `json:"isolation,omitempty"`
	ReasonCode string    `json:"reason_code,omitempty"`
	Reason     string    `json:"reason,omitempty"`
}

// LandEntry is one PR of a batch, as it lands (or would).
type LandEntry struct {
	EntryID  string `json:"entry_id"`
	RecordID string `json:"record_id"`
	Number   int    `json:"number"`
	Head     string `json:"head"`
	// Tree is T_i, main's tree after this PR's squash merge.
	Tree string `json:"tree"`
}

// EjectedEntry is an entry a batch took out of the queue.
type EjectedEntry struct {
	EntryID  string                `json:"entry_id"`
	RecordID string                `json:"record_id"`
	Number   int                   `json:"number,omitempty"`
	Head     string                `json:"head"`
	Reason   core.QueueEjectReason `json:"reason"`
	// Cause is the candidate builder's reason when it differs from Reason
	// (regen_failed, not_generated_only).
	Cause        string   `json:"cause,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	Detail       string   `json:"detail,omitempty"`
	FailingTests []string `json:"failing_tests,omitempty"`
	// Batches are the gates that judged it red.
	Batches []string `json:"batches,omitempty"`
}

// RegenEntry is an entry kept by regen.
type RegenEntry struct {
	EntryID   string   `json:"entry_id"`
	Head      string   `json:"head"`
	NewHead   string   `json:"new_head"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// WouldPush is a push shadow mode did not make.
type WouldPush struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

func (v Verdict) json() json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// Verdict holds only strings, ints and bools.
		panic(err)
	}
	return b
}

// ParseVerdict reads a batch's verdict_json; an empty or unreadable one is
// the zero Verdict.
func ParseVerdict(raw json.RawMessage) Verdict {
	var v Verdict
	_ = json.Unmarshal(raw, &v)
	return v
}

// ejectReason maps a candidate builder ejection onto queue_entries'
// eject_reason. ok is false for batch_conflict: nothing is wrong with the
// PR, so it waits for a later batch instead of leaving the queue.
//
//	conflict           → conflict
//	batch_conflict     → (requeued)
//	regen_failed       → conflict, cause regen_failed
//	not_generated_only → conflict, cause not_generated_only
//	head_moved         → head_moved
func ejectReason(r buildjob.Reason) (reason core.QueueEjectReason, cause string, ok bool) {
	switch r {
	case buildjob.ReasonBatchConflict:
		return "", "", false
	case buildjob.ReasonHeadMoved:
		return core.QueueEjectHeadMoved, "", true
	case buildjob.ReasonRegenFailed, buildjob.ReasonNotGeneratedOnly:
		return core.QueueEjectConflict, string(r), true
	default:
		return core.QueueEjectConflict, "", true
	}
}
