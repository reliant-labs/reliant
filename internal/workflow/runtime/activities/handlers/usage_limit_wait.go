// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/activity"
)

// UsageLimitWaitInput is the input for the UsageLimitWait activity.
type UsageLimitWaitInput struct {
	ChatID string `json:"chat_id" reliant:"-"`
	// Thread is the thread parked on the limit. The chat's root thread is the
	// chat's workflow id, which is what makes the chat read as waiting.
	Thread string `json:"thread"`
	// Clear releases the thread instead of parking it.
	Clear bool `json:"clear,omitempty"`
	// Provider names who imposed the limit; empty when unknown.
	Provider string `json:"provider,omitempty"`
	// ResumeAt is when the thread retries, RFC 3339 UTC.
	ResumeAt string `json:"resume_at,omitempty"`
	// Probing is true when the provider named no reset and ResumeAt is only
	// the next rung of the probe ladder.
	Probing bool `json:"probing,omitempty"`

	// Notice, when set, posts a transcript notice instead of writing or
	// clearing a marker. "switch_provider": the thread continues on Model
	// (another provider) until ResumeAt, because every account of Provider is
	// limited.
	Notice     string `json:"notice,omitempty"`
	WorkflowID string `json:"workflow_id,omitempty" reliant:"-"`
	// Model is the "<model>@<driver>" the thread continues on.
	Model string `json:"model,omitempty"`
}

// UsageLimitWaitOutput is the output from the UsageLimitWait activity.
type UsageLimitWaitOutput struct {
	Success bool `json:"success"`
}

// UsageLimitWaitActivity writes the durable marker for a run parked until a
// provider's usage limit resets, and releases it.
//
// The workflow calls it BEFORE it parks on its timer: a marker written after
// the park cannot tell a run that is waiting from one that is wedged, which is
// the whole reason provider_backoff exists. See db.ProviderBackoff.
type UsageLimitWaitActivity struct {
	repo UsageLimitWaitStore
}

// NewUsageLimitWaitActivity creates a new UsageLimitWaitActivity.
func NewUsageLimitWaitActivity(repo UsageLimitWaitStore) *UsageLimitWaitActivity {
	return &UsageLimitWaitActivity{repo: repo}
}

// Name returns the activity name for registration.
func (a *UsageLimitWaitActivity) Name() string {
	return "UsageLimitWait"
}

// Execute records or clears the wait marker.
func (a *UsageLimitWaitActivity) Execute(ctx context.Context, input UsageLimitWaitInput) (UsageLimitWaitOutput, error) {
	if input.Notice == "switch_provider" {
		until, _ := time.Parse(time.RFC3339, input.ResumeAt)
		key := ""
		if activity.IsActivity(ctx) {
			key = "provider-switch-" + activity.GetInfo(ctx).ActivityID + "-" + input.Thread
		}
		postNotice(ctx, a.repo, input.ChatID, input.Thread, input.WorkflowID, key,
			providerSwitchNotice(input.Provider, input.Model, until, input.Probing))
		return UsageLimitWaitOutput{Success: true}, nil
	}
	if input.Clear {
		if err := a.repo.ClearUsageLimitWait(ctx, input.ChatID, input.Thread, time.Now()); err != nil {
			return UsageLimitWaitOutput{}, err
		}
		return UsageLimitWaitOutput{Success: true}, nil
	}
	resumeAt, err := time.Parse(time.RFC3339, input.ResumeAt)
	if err != nil {
		return UsageLimitWaitOutput{}, fmt.Errorf("invalid resume_at %q: %w", input.ResumeAt, err)
	}
	if err := a.repo.RecordUsageLimitWait(ctx, input.ChatID, input.Thread, input.Provider, input.Probing, resumeAt, time.Now()); err != nil {
		return UsageLimitWaitOutput{}, err
	}
	return UsageLimitWaitOutput{Success: true}, nil
}
