// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/reliant/askuser"
	"github.com/reliant-labs/reliant/sdk/go/reliant/follow"
)

// The wire types the client returns, under names callers can use without
// importing the generated package.
type (
	// Run is one workflow execution. State and StopReason are a pair: a STOPPED
	// run with stop reason PAUSED is live and resumable, COMPLETED is not. Use
	// DisplayState for the one state a person reads.
	Run = reliantv1.Run
	// Trigger is an automation: what starts a run, and with what.
	Trigger = reliantv1.Trigger
	// TriggerEvent is one firing of a trigger and what it did.
	TriggerEvent = reliantv1.TriggerEvent
	// WorkflowState / WorkflowStopReason are the lifecycle pair on a Run.
	WorkflowState      = reliantv1.WorkflowState
	WorkflowStopReason = reliantv1.WorkflowStopReason
	// RunDisplayState is the single status vocabulary a person reads.
	RunDisplayState = reliantv1.RunDisplayState
	// TriggerEventOutcome says whether a firing launched, was skipped or failed.
	TriggerEventOutcome = reliantv1.TriggerEventOutcome

	// Event is one boundary of a followed run (see the follow package).
	Event = follow.Event
	// Answer is one answered sub-question of a gate.
	Answer = askuser.Answer
)

// Event kinds on a followed run.
const (
	EventNodeStarted       = follow.EventNodeStarted
	EventNodeCompleted     = follow.EventNodeCompleted
	EventNodeFailed        = follow.EventNodeFailed
	EventNodeSkipped       = follow.EventNodeSkipped
	EventWorkflowStarted   = follow.EventWorkflowStarted
	EventWorkflowCompleted = follow.EventWorkflowCompleted
	EventWorkflowFailed    = follow.EventWorkflowFailed
	EventWorkflowCancelled = follow.EventWorkflowCancelled
	EventQuestion          = follow.EventQuestion
	EventQuestionAnswered  = follow.EventQuestionAnswered
	EventApproval          = follow.EventApproval
	EventApprovalResolved  = follow.EventApprovalResolved
	EventFollowTimeout     = follow.EventFollowTimeout
)

// Outcome is the verdict a workflow declares for itself, which is a different
// fact from whether it finished.
const (
	OutcomeSuccess = follow.OutcomeSuccess
	OutcomeFailure = follow.OutcomeFailure
)

// Role is who a message is from.
type Role int32

// Roles a caller may send as. The server rejects a run whose first message is
// not from the user, and assistant/tool turns are the engine's to write.
const (
	RoleUser   Role = Role(reliantv1.MessageRole_MESSAGE_ROLE_USER)
	RoleSystem Role = Role(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM)
)

// Message is text delivered into a run.
type Message struct {
	Role    Role
	Content string
}

// UserMessage is a message from the user.
func UserMessage(text string) Message { return Message{Role: RoleUser, Content: text} }

func toInputMessages(msgs []Message) []*reliantv1.InputMessage {
	out := make([]*reliantv1.InputMessage, 0, len(msgs))
	for _, m := range msgs {
		role := reliantv1.MessageRole(m.Role)
		if m.Role == 0 {
			role = reliantv1.MessageRole_MESSAGE_ROLE_USER
		}
		out = append(out, &reliantv1.InputMessage{Role: role, Content: m.Content})
	}
	return out
}

// toStructValues converts workflow inputs to the wire's google.protobuf.Value
// map. Numbers, strings, bools, nil, []any and map[string]any all convert; a
// value structpb cannot represent is an error naming its key.
func toStructValues(in map[string]any) (map[string]*structpb.Value, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]*structpb.Value, len(in))
	for k, v := range in {
		sv, err := structpb.NewValue(v)
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", k, err)
		}
		out[k] = sv
	}
	return out, nil
}
