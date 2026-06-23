// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// =============================================================================
// TEST INPUT BUILDERS
// =============================================================================
// These helper types make it easy to construct test inputs with a flat-struct
// ergonomics. They provide a ProtoInput() method that returns the actual
// handlers.ActivityInput value for Temporal's test framework.

// protoInputter is implemented by test input builders that can produce an ActivityInput.
// Used by IdempotencyTestHelper.ExecuteActivity to auto-convert test inputs.
type protoInputter interface {
	protoInput() interface{}
}

// ExecuteToolsInput is a flat struct matching the old ExecuteToolsInput fields.
// Used in tests to preserve backward-compatible construction patterns.
type ExecuteToolsInput struct {
	ChatID                string                            `json:"-"`
	Thread                string                            `json:"-"`
	StepID                string                            `json:"-"`
	ToolCalls             []message.ToolCall                `json:"-"`
	LoopNodeID            string                            `json:"-"`
	LoopIteration         int                               `json:"-"`
	ProjectPath           string                            `json:"-"`
	ExpectedResponseTools []string                          `json:"-"`
	ResponseToolSchemas   map[string]map[string]interface{} `json:"-"`
}

// V3 returns the ActivityInput for Temporal's test framework.
func (t ExecuteToolsInput) V3() ActivityInput {
	// Convert message.ToolCall to proto ToolCallMsg
	var protoToolCalls []*reliantv1.ToolCallMsg
	for _, tc := range t.ToolCalls {
		protoToolCalls = append(protoToolCalls, &reliantv1.ToolCallMsg{
			Id:    tc.ID,
			Name:  tc.Name,
			Input: encodeToolCallInputForProto(tc),
		})
	}

	return ActivityInput{
		Runtime: RuntimeContext{
			ChatID:        t.ChatID,
			Thread:        t.Thread,
			StepID:        t.StepID,
			LoopNodeID:    t.LoopNodeID,
			LoopIteration: t.LoopIteration,
			ProjectPath:   t.ProjectPath,
		},
		Node: &reliantv1.Node{
			Type: "execute_tools",
			Args: &reliantv1.Node_ExecuteTools{
				ExecuteTools: &reliantv1.ExecuteToolsArgs{
					ResolvedToolCalls:     protoToolCalls,
					ExpectedResponseTools: t.ExpectedResponseTools,
				},
			},
		},
	}
}

func (t ExecuteToolsInput) protoInput() interface{} { return t.V3() }

// MarshalJSON produces the ActivityInput JSON structure.
func (t ExecuteToolsInput) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.V3())
}

// SaveMessageInput is a flat struct matching the old SaveMessageInput fields.
// Used in tests to preserve backward-compatible construction patterns.
type SaveMessageInput struct {
	ChatID          string               `json:"-"`
	Thread          string               `json:"-"`
	StepID          string               `json:"-"`
	Role            string               `json:"-"`
	DisplayStyle    string               `json:"-"`
	Content         string               `json:"-"`
	Attachments     []string             `json:"-"`
	ToolResults     []message.ToolResult `json:"-"`
	ToolCalls       []message.ToolCall   `json:"-"`
	TokenCount      int                  `json:"-"`
	Cost            float64              `json:"-"`
	ContextWindowID string               `json:"-"`
	WorkflowID      string               `json:"-"`
	LoopNodeID      string               `json:"-"`
	LoopIteration   int                  `json:"-"`
	Thinking        ThinkingOutput       `json:"-"`
}

// V3 returns the ActivityInput for Temporal's test framework.
func (t *SaveMessageInput) V3() ActivityInput {
	var thinking *reliantv1.ThinkingOutput
	if t.Thinking.Content != "" || t.Thinking.Signature != "" {
		thinking = &reliantv1.ThinkingOutput{
			Content:   t.Thinking.Content,
			Signature: t.Thinking.Signature,
		}
	}

	// Convert message.ToolCall to proto ToolCallMsg
	var protoToolCalls []*reliantv1.ToolCallMsg
	for _, tc := range t.ToolCalls {
		protoToolCalls = append(protoToolCalls, &reliantv1.ToolCallMsg{
			Id:    tc.ID,
			Name:  tc.Name,
			Input: encodeToolCallInputForProto(tc),
		})
	}

	// Convert message.ToolResult to proto ToolResultMsg
	var protoToolResults []*reliantv1.ToolResultMsg
	for _, tr := range t.ToolResults {
		protoToolResults = append(protoToolResults, &reliantv1.ToolResultMsg{
			ToolCallId: tr.ToolCallID,
			Name:       tr.Name,
			Content:    tr.Content,
			IsError:    tr.IsError,
		})
	}

	return ActivityInput{
		Runtime: RuntimeContext{
			ChatID:        t.ChatID,
			Thread:        t.Thread,
			StepID:        t.StepID,
			WorkflowID:    t.WorkflowID,
			LoopNodeID:    t.LoopNodeID,
			LoopIteration: t.LoopIteration,
		},
		Node: &reliantv1.Node{
			Type: "save_message",
			Args: &reliantv1.Node_SaveMessageNode{
				SaveMessageNode: &reliantv1.SaveMessageNodeArgs{
					ResolvedRole:         t.Role,
					ResolvedContent:      t.Content,
					ResolvedDisplayStyle: t.DisplayStyle,
					ResolvedToolCalls:    protoToolCalls,
					ResolvedToolResults:  protoToolResults,
					ResolvedAttachments:  t.Attachments,
					ResolvedThinking:     thinking,
					TokenCount:           int32(t.TokenCount),
					Cost:                 t.Cost,
				},
			},
		},
	}
}

func (t *SaveMessageInput) protoInput() interface{} { return t.V3() }

// MarshalJSON produces the ActivityInput JSON structure.
func (t *SaveMessageInput) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.V3())
}

// CompactInput is a flat struct matching the old CompactInput fields.
// Used in tests to preserve backward-compatible construction patterns.
type CompactInput struct {
	ChatID        string `json:"-"`
	SessionID     string `json:"-"`
	Thread        string `json:"-"`
	LoopNodeID    string `json:"-"`
	LoopIteration int    `json:"-"`
}

// V3 returns the ActivityInput for Temporal's test framework.
func (t CompactInput) V3() ActivityInput {
	return ActivityInput{
		Runtime: RuntimeContext{
			ChatID:        t.ChatID,
			SessionID:     t.SessionID,
			Thread:        t.Thread,
			LoopNodeID:    t.LoopNodeID,
			LoopIteration: t.LoopIteration,
		},
		Node: &reliantv1.Node{
			Type: "compact",
			Args: &reliantv1.Node_Compact{
				Compact: &reliantv1.CompactArgs{},
			},
		},
	}
}

func (t CompactInput) protoInput() interface{} { return t.V3() }

// MarshalJSON produces the ActivityInput JSON structure.
func (t CompactInput) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.V3())
}
