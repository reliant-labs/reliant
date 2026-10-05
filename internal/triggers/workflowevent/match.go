// Copyright (c) 2025 Reliant Labs

// Package workflowevent fires workflow_event triggers: "when one of my runs of
// workflow X finishes, fails or gets blocked, start workflow Y".
//
// The signal is the run-event outbox (core.RunEvent), written by
// internal/triggers/runevents in the same transaction as the transition it
// reports. A relay hands each outbox row to Dispatch, which matches it against
// the OWNER's enabled workflow_event triggers and launches one run per match
// through internal/launch — the one door — so the trigger_events
// UNIQUE (kind, dedupe_key) constraint makes each (trigger, event) launch at
// most once however often dispatch is retried.
//
// Matching is written against Source (reliantv1.WorkflowEventSource: source
// workflows and outcomes) plus the trigger's CEL filter, and nothing
// row-specific, so the same spec evaluates identically whether it came from a
// stored trigger row or a `triggers:` block in a workflow definition.
//
// # Loop guards
//
// A run launched by a workflow event carries its LINEAGE in its trigger
// event's payload: the chain of (trigger, source run) links that led to it,
// root first. Lineage is inherited, so it is never walked at match time:
//
//   - a trigger never fires on a run it launched, nor on any descendant of
//     such a run — its id is somewhere in the source run's lineage;
//   - a chain never grows past MaxChainDepth links.
//
// Together they stop A→A (A's trigger is in its own run's lineage) and
// A→B→A (A's trigger is in the lineage B inherited) after one hop, and bound
// any acyclic fan-out of distinct triggers.
//
//forge:exclude-contract: Temporal workflow/activity receivers and a relay loop the worker wires; consumers declare narrow interfaces at the use site, there is no substitutable service
package workflowevent

import (
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
)

// MaxChainDepth caps how many workflow-event links may precede a launch. A
// run started by a human has depth 0; the run its trigger starts has depth 1;
// a run triggered by THAT run has depth 2, and so on. A launch that would sit
// deeper is skipped.
const MaxChainDepth = 5

// Source is a workflow_event trigger's source spec. It mirrors
// reliantv1.WorkflowEventSource field for field (and is the JSON shape of a
// stored trigger's config), so a trigger row and a workflow definition's
// `triggers:` block evaluate identically.
type Source struct {
	// Workflows are the source workflow refs to listen to (a slug, display
	// name or builtin:// reference). Empty listens to every workflow of the
	// owner.
	Workflows []string `json:"workflows,omitempty"`
	// Outcomes narrows the transitions that fire: any subset of finished,
	// failed and blocked. Empty means all three.
	Outcomes []string `json:"outcomes,omitempty"`
}

// ValidOutcome reports whether s names a run-event outcome.
func ValidOutcome(s string) bool {
	switch core.RunEventOutcome(s) {
	case core.RunEventFinished, core.RunEventFailed, core.RunEventBlocked:
		return true
	}
	return false
}

// MatchesWorkflow reports whether the source admits an event from
// workflowName. Both sides are compared as runevents.WorkflowRef, so a display
// name and its slug match.
func (s Source) MatchesWorkflow(workflowName string) bool {
	any := false
	for _, ref := range s.Workflows {
		if strings.TrimSpace(ref) == "" {
			continue
		}
		any = true
		if runevents.WorkflowRef(ref) == runevents.WorkflowRef(workflowName) {
			return true
		}
	}
	return !any
}

// MatchesOutcome reports whether the source listens to outcome.
func (s Source) MatchesOutcome(outcome core.RunEventOutcome) bool {
	any := false
	for _, o := range s.Outcomes {
		o = strings.ToLower(strings.TrimSpace(o))
		if o == "" {
			continue
		}
		any = true
		if core.RunEventOutcome(o) == outcome {
			return true
		}
	}
	return !any
}

// Matches reports whether an event of outcome from workflowName is one this
// source fires on. The CEL filter is evaluated separately, after the guards.
func (s Source) Matches(workflowName string, outcome core.RunEventOutcome) bool {
	return s.MatchesOutcome(outcome) && s.MatchesWorkflow(workflowName)
}
