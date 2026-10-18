// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// requestRecordingDriver keeps the messages of every request it is handed.
type requestRecordingDriver struct {
	mu       sync.Mutex
	requests [][]message.Message
}

func (d *requestRecordingDriver) Name() string { return "request-recorder" }
func (d *requestRecordingDriver) Model() models.Model {
	return models.Model{ID: "mock-model", Name: "Mock Model"}
}
func (d *requestRecordingDriver) ValidateKey(context.Context) error { return nil }

func (d *requestRecordingDriver) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{Content: "ok", FinishReason: "end_turn"}, nil
}

func (d *requestRecordingDriver) StreamResponse(_ context.Context, _ []string, msgs []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	d.mu.Lock()
	d.requests = append(d.requests, append([]message.Message(nil), msgs...))
	d.mu.Unlock()

	ch := make(chan llm.DriverEvent, 1)
	ch <- llm.DriverEvent{
		Type:     llm.EventComplete,
		Response: &llm.DriverResponse{Content: "ok", FinishReason: "end_turn", Usage: llm.TokenUsage{TokenCount: 10}},
	}
	close(ch)
	return ch
}

type skillCatalogConfigProvider struct{ skills []cfgpkg.StoredSkill }

func (p skillCatalogConfigProvider) GetProjectConfig(context.Context, cfgpkg.ProjectRef) (*cfgpkg.Config, error) {
	return &cfgpkg.Config{Skills: p.skills}, nil
}

func requestTexts(msgs []message.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = string(m.Role) + ": " + m.Content().Text
	}
	return out
}

// The provider's prompt cache is a prefix match with its message breakpoint on
// the LAST message of a request. A message that exists only in one request —
// the old automatic "Potentially relevant skills" reminder, rebuilt at the tail
// of every call from the user's request — carried that breakpoint, so the
// prefix written by one request ended in a message the next request no longer
// had, and the whole conversation was re-written on every step.
//
// This drives two real CallLLM executions on a growing thread whose user
// request matches the project's skill catalog, and pins what the driver
// actually receives: nothing the user or the thread did not write, and every
// earlier request a strict prefix of the next.
func TestCallLLM_RequestHasNoTransientTailAndGrowsAsAPrefix(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)

	catalog := []cfgpkg.StoredSkill{
		{Name: "proto", SkillPath: "forge/proto", Body: "PROTO body", Description: "Proto surface: regenerate the proto files."},
		{Name: "db", SkillPath: "forge/db", Body: "DB body", Description: "Database migrations and the db layer."},
	}
	const request = "regenerate the proto surface and fix the db migration"
	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, request)

	driver := &requestRecordingDriver{}
	resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	})
	activity := NewCallLLMActivity(h.Repo(), nil, nil, skillCatalogConfigProvider{skills: catalog}, resolver, nil)

	var out CallLLMOutput
	require.NoError(t, h.ExecuteActivity(activity.Execute, callLLMInput(chat.ID, chat.ID, "mock-model"), &out))

	// The thread grows the way an agent loop grows it: an assistant turn, then
	// the next user message.
	saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID, "Regenerating the protos now.")
	h.CreateTestUserMessageWithText(ctx, chat.ID, chat.ID, "thanks, continue")
	require.NoError(t, h.ExecuteActivity(activity.Execute, callLLMInput(chat.ID, chat.ID, "mock-model"), &out))

	require.Len(t, driver.requests, 2, "expected one provider request per CallLLM execution")
	first, second := driver.requests[0], driver.requests[1]

	for i, req := range driver.requests {
		for _, m := range req {
			require.NotContains(t, m.Content().Text, "Potentially relevant skills",
				"request %d carries an automatic skill suggestion; it is rebuilt per call and breaks the cache prefix", i+1)
		}
	}

	require.Equal(t, request, first[len(first)-1].Content().Text,
		"the first request must end with the user's own last message, not a transient message after it")
	require.False(t, strings.Contains(second[len(second)-1].Content().Text, "system-reminder"),
		"the second request must end with the user's own last message")

	require.Greater(t, len(second), len(first), "the second request must extend the first")
	require.Equal(t, requestTexts(first), requestTexts(second[:len(first)]),
		"the first request is not a prefix of the second: the cache entry written at its tail can never be read")
}
