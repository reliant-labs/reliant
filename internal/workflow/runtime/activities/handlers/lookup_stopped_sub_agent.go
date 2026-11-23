// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// LookupStoppedSubAgentInput names the thread a doorbell rang for.
type LookupStoppedSubAgentInput struct {
	ChatID string `json:"chat_id" reliant:"-"`
	Thread string `json:"thread"`
	All    bool   `json:"all,omitempty"`
}

// LookupStoppedSubAgentOutput is the sub-agent on the thread if it stopped on
// a failure, and nothing if it did not.
type LookupStoppedSubAgentOutput struct {
	Agent  *core.StoppedSpawnAgent   `json:"agent,omitempty"`
	Agents []*core.StoppedSpawnAgent `json:"agents,omitempty"`
}

// StoppedSubAgentReader is the one read this activity makes.
// store.ToolCallExtStore satisfies it.
type StoppedSubAgentReader interface {
	GetStoppedSpawnAgent(ctx context.Context, chatID, threadID string) (*core.StoppedSpawnAgent, error)
	ListStoppedSpawnAgents(ctx context.Context, chatID string) ([]*core.StoppedSpawnAgent, error)
}

// LookupStoppedSubAgentActivity answers, from durable rows, whether a thread
// is a sub-agent that stopped on a failure — what a message to it resumes.
//
// The workflow asks when a doorbell rings for a thread its execution has no
// record of: an agent that stopped in an earlier execution of the chat (one
// that completed, or died and was restarted), or before executions recorded
// stopped agents at all. See runtime/subagent_resume.go.
type LookupStoppedSubAgentActivity struct {
	repo StoppedSubAgentReader
}

// NewLookupStoppedSubAgentActivity creates a LookupStoppedSubAgentActivity.
func NewLookupStoppedSubAgentActivity(repo StoppedSubAgentReader) *LookupStoppedSubAgentActivity {
	return &LookupStoppedSubAgentActivity{repo: repo}
}

// Name returns the activity name for registration. It must match
// runtime.LookupStoppedSubAgentActivityName.
func (a *LookupStoppedSubAgentActivity) Name() string {
	return "LookupStoppedSubAgent"
}

// DisplayName returns human-readable name for UI
func (a *LookupStoppedSubAgentActivity) DisplayName() string {
	return "Lookup Stopped Sub-Agent"
}

// Description returns what the activity does
func (a *LookupStoppedSubAgentActivity) Description() string {
	return "Read whether a thread is a sub-agent that stopped on an error, for a message to resume it"
}

// Category returns the activity category for UI grouping
func (a *LookupStoppedSubAgentActivity) Category() schema.ActivityCategory {
	return schema.CategoryWorkflowManagement
}

// Execute reads the thread's stopped sub-agent, if there is one.
func (a *LookupStoppedSubAgentActivity) Execute(ctx context.Context, input LookupStoppedSubAgentInput) (LookupStoppedSubAgentOutput, error) {
	if input.ChatID == "" {
		return LookupStoppedSubAgentOutput{}, fmt.Errorf("chat_id is required")
	}
	if input.All {
		agents, err := a.repo.ListStoppedSpawnAgents(ctx, input.ChatID)
		return LookupStoppedSubAgentOutput{Agents: agents}, err
	}
	if input.ChatID == "" || input.Thread == "" {
		return LookupStoppedSubAgentOutput{}, fmt.Errorf("chat_id and thread are required")
	}
	agent, err := a.repo.GetStoppedSpawnAgent(ctx, input.ChatID, input.Thread)
	if err != nil {
		return LookupStoppedSubAgentOutput{}, err
	}
	return LookupStoppedSubAgentOutput{Agent: agent}, nil
}
