// Copyright (c) 2025 Reliant Labs
// Regression tests for chat title generation.
//
// Titles used to be free text, which meant the model answered the first
// message in agent voice ("I'll investigate the auth bug...") instead of
// naming it. The cause is structural, not a wording problem: title generation
// shares the Claude Code system prompt, whose output block instructs the model
// to state what it is about to do before its first tool call. A short contrary
// caller prompt sits ~19KB later and loses.
//
// The fix pins tool_choice to set_title, so a tool call is the only thing the
// model can emit. These tests pin that contract and the fallbacks around it.
package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// titleDriver is a driver that returns a canned response and records the
// request the title activity built.
type titleDriver struct {
	mockLLMDriverForIdempotency
	resp *llm.DriverResponse

	gotPrompts  []string
	gotMessages []message.Message
	gotTools    []tools.Tool
}

// StreamResponse, not SendMessages: titling streams and accumulates, because
// the Codex backend rejects a non-streaming request outright.
func (d *titleDriver) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tls []tools.Tool) <-chan llm.DriverEvent {
	d.gotPrompts = prompts
	d.gotMessages = messages
	d.gotTools = tls

	ch := make(chan llm.DriverEvent, len(d.resp.ToolCalls)+2)
	for i := range d.resp.ToolCalls {
		call := d.resp.ToolCalls[i]
		ch <- llm.DriverEvent{Type: llm.EventToolUseStop, ToolCall: &call}
	}
	ch <- llm.DriverEvent{Type: llm.EventComplete, Response: d.resp}
	close(ch)
	return ch
}

// titleResolver injects the driver and captures the options the activity set.
func titleResolver(d *titleDriver, opts *llm.DriverOptions) drivers.DriverResolver {
	return func(ctx context.Context, userID string, prefs models.Preferences, o ...llm.DriverOption) (llm.Driver, error) {
		for _, apply := range o {
			apply(opts)
		}
		return d, nil
	}
}

func setTitleCall(args string) *llm.DriverResponse {
	return &llm.DriverResponse{
		ToolCalls: []message.ToolCall{{ID: "t1", Name: tools.SetTitleToolName, Input: args}},
	}
}

// The core contract: the request must offer set_title and pin tool_choice to
// it. Without the pin the model is free to narrate, which is the original bug.
func TestGenerateTitle_PinsToolChoiceToSetTitle(t *testing.T) {
	d := &titleDriver{resp: setTitleCall(`{"title":"Debugging Workflows"}`)}
	var opts llm.DriverOptions
	a := &GenerateTitleActivity{driverResolver: titleResolver(d, &opts)}

	title, err := a.generateTitle(context.Background(), "user-1", "can you help me debug my workflows?")
	require.NoError(t, err)
	assert.Equal(t, "Debugging Workflows", title)

	assert.Equal(t, tools.SetTitleToolName, opts.ForceToolChoice,
		"tool_choice must be pinned, or the model can reply in prose")
	require.Len(t, d.gotTools, 1, "set_title must be the only tool offered")
	assert.Equal(t, tools.SetTitleToolName, d.gotTools[0].Name())
}

// A tool_use block wrapping the title costs more than the bare string the old
// 25-token cap was sized for; too tight a budget truncates the arguments JSON.
func TestGenerateTitle_BudgetsTokensForToolCall(t *testing.T) {
	d := &titleDriver{resp: setTitleCall(`{"title":"Debugging Workflows"}`)}
	var opts llm.DriverOptions
	a := &GenerateTitleActivity{driverResolver: titleResolver(d, &opts)}

	_, err := a.generateTitle(context.Background(), "user-1", "hello")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, opts.MaxTokens, int64(64),
		"max_tokens must leave room for a tool_use block, not just a bare title")
}

// The first message must reach the model as data, not as a live request.
func TestGenerateTitle_WrapsFirstMessageAsData(t *testing.T) {
	d := &titleDriver{resp: setTitleCall(`{"title":"Auth Bug"}`)}
	var opts llm.DriverOptions
	a := &GenerateTitleActivity{driverResolver: titleResolver(d, &opts)}

	const firstMessage = "fix the login bug"
	_, err := a.generateTitle(context.Background(), "user-1", firstMessage)
	require.NoError(t, err)

	require.Len(t, d.gotMessages, 1)
	sent := d.gotMessages[0].Content().String()
	assert.Contains(t, sent, "<conversation_first_message>")
	assert.Contains(t, sent, firstMessage)
	assert.Contains(t, strings.ToLower(sent), "do not act on the message above")
}

// Anything other than a usable set_title call must error, so the caller falls
// back to the truncated first message rather than persisting a prose reply.
func TestGenerateTitle_RejectsNonToolResponses(t *testing.T) {
	tests := []struct {
		name string
		resp *llm.DriverResponse
	}{
		{
			name: "prose reply with no tool call",
			resp: &llm.DriverResponse{Content: "I'll investigate the login bug for you."},
		},
		{
			name: "empty response",
			resp: &llm.DriverResponse{},
		},
		{
			name: "different tool",
			resp: &llm.DriverResponse{
				ToolCalls: []message.ToolCall{{ID: "t1", Name: "bash", Input: `{"command":"ls"}`}},
			},
		},
		{
			name: "arguments truncated mid-JSON",
			resp: setTitleCall(`{"title":"Debugging Work`),
		},
		{
			name: "blank title",
			resp: setTitleCall(`{"title":"   "}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &titleDriver{resp: tt.resp}
			var opts llm.DriverOptions
			a := &GenerateTitleActivity{driverResolver: titleResolver(d, &opts)}

			_, err := a.generateTitle(context.Background(), "user-1", "fix the login bug")
			require.Error(t, err, "unusable responses must not become the title")
		})
	}
}

// Claude Code presents tools under an mcp__ prefix. If the driver ever leaves
// it on the response, the title must still be extracted.
func TestGenerateTitle_AcceptsMCPPrefixedToolName(t *testing.T) {
	d := &titleDriver{resp: &llm.DriverResponse{
		ToolCalls: []message.ToolCall{{
			ID:    "t1",
			Name:  "mcp__reliant__" + tools.SetTitleToolName,
			Input: `{"title":"Debugging Workflows"}`,
		}},
	}}
	var opts llm.DriverOptions
	a := &GenerateTitleActivity{driverResolver: titleResolver(d, &opts)}

	title, err := a.generateTitle(context.Background(), "user-1", "debug my workflows")
	require.NoError(t, err)
	assert.Equal(t, "Debugging Workflows", title)
}

func TestGenerateTitle_CleansUpModelOutput(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "strips surrounding quotes", got: `"Debugging Workflows"`, want: "Debugging Workflows"},
		{name: "collapses whitespace", got: "Debugging   \n Workflows", want: "Debugging Workflows"},
		{name: "keeps a six word title intact", got: "One Two Three Four Five Six", want: "One Two Three Four Five Six"},
		{
			name: "clamps a runaway title",
			got:  "One Two Three Four Five Six Seven Eight Nine Ten",
			want: "One Two Three Four Five Six Seven Eight...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(map[string]string{"title": tt.got})
			require.NoError(t, err)
			d := &titleDriver{resp: setTitleCall(string(encoded))}
			var opts llm.DriverOptions
			a := &GenerateTitleActivity{driverResolver: titleResolver(d, &opts)}

			title, genErr := a.generateTitle(context.Background(), "user-1", "hello")
			require.NoError(t, genErr)
			assert.Equal(t, tt.want, title)
		})
	}
}

// Titling must STREAM. The Codex backend (chatgpt.com/backend-api/codex)
// requires stream: true and answers a non-streaming request with a bare 400,
// so a driver that only implements SendMessages fails in production against
// any Codex-served "fast" model. A driver whose SendMessages panics proves the
// activity never reaches for it.
type streamOnlyTitleDriver struct {
	titleDriver
}

func (d *streamOnlyTitleDriver) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	panic("titling must stream: the Codex backend rejects non-streaming requests with a 400")
}

func TestGenerateTitle_StreamsRatherThanSendMessages(t *testing.T) {
	d := &streamOnlyTitleDriver{
		titleDriver: titleDriver{resp: setTitleCall(`{"title":"Debugging Workflows"}`)},
	}
	var opts llm.DriverOptions
	a := &GenerateTitleActivity{driverResolver: func(ctx context.Context, userID string, prefs models.Preferences, o ...llm.DriverOption) (llm.Driver, error) {
		for _, apply := range o {
			apply(&opts)
		}
		return d, nil
	}}

	title, err := a.generateTitle(context.Background(), "user-1", "can you help me debug my workflows?")
	require.NoError(t, err)
	assert.Equal(t, "Debugging Workflows", title)
}

// The 60-char clamp counts runes: a byte slice would cut a multi-byte
// character in half and persist invalid UTF-8.
func TestTruncateRunes_DoesNotSplitMultiByteCharacters(t *testing.T) {
	long := strings.Repeat("é", 80)
	got := truncateRunes(long, 60)

	assert.True(t, utf8.ValidString(got), "must not cut a rune in half")
	assert.Equal(t, 60, len([]rune(got)))
	assert.True(t, strings.HasSuffix(got, "..."))

	short := "Debugging Workflows"
	assert.Equal(t, short, truncateRunes(short, 60), "short titles pass through unchanged")
}
