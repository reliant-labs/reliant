// Copyright (c) 2025 Reliant Labs
package reliant

import (
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	llmtools "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// These tests assert on the marshaled wire body rather than on Go structs,
// because cache_control has no typed home in openai-go: it rides as an extra
// field, and the only question that matters is whether it lands at the level
// LiteLLM reads it from. A struct-level assertion would pass for a breakpoint
// nested one level too deep, which is exactly the bug worth catching.

// cacheControlSite is one cache_control occurrence plus the JSON path it was
// found at, so a test can assert on placement and not merely on the count.
type cacheControlSite struct {
	path  string
	value map[string]any
}

// findCacheControls walks arbitrary decoded JSON and collects every
// cache_control object with the path it sits at. Walking generic JSON rather
// than re-marshaling typed structs is deliberate: it sees keys the SDK has no
// field for, which is the whole category this feature lives in.
func findCacheControls(node any, path string, found *[]cacheControlSite) {
	switch typed := node.(type) {
	case map[string]any:
		for key, child := range typed {
			childPath := path + "." + key
			if key == "cache_control" {
				control, _ := child.(map[string]any)
				*found = append(*found, cacheControlSite{path: path, value: control})
				continue
			}
			findCacheControls(child, childPath, found)
		}
	case []any:
		for _, child := range typed {
			findCacheControls(child, path+"[]", found)
		}
	}
}

func cacheControlsIn(t *testing.T, params any) []cacheControlSite {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	var found []cacheControlSite
	findCacheControls(generic, "", &found)
	t.Logf("request body: %s", raw)
	return found
}

func assertBareEphemeral(t *testing.T, sites []cacheControlSite) {
	t.Helper()
	for _, site := range sites {
		if got := site.value["type"]; got != "ephemeral" {
			t.Errorf("cache_control at %s has type %v, want ephemeral", site.path, got)
		}
		if got, ok := site.value["ttl"]; ok {
			t.Errorf("cache_control at %s has ttl %v, want none", site.path, got)
		}
	}
}

func countPaths(sites []cacheControlSite) map[string]int {
	counts := make(map[string]int, len(sites))
	for _, site := range sites {
		counts[site.path]++
	}
	return counts
}

// The full-strategy case: four breakpoints, one per placement LiteLLM accepts,
// and no fifth (Anthropic rejects more than four).
func TestClaudeCacheBreakpoints_SystemToolsAndToolResult(t *testing.T) {
	client := &ReliantClient{Options: llm.DriverOptions{
		Model: models.Model{APIModel: "claude-opus-5-5"},
	}}

	messages := client.ConvertMessages(
		[]string{"system one", "system two"},
		[]message.Message{
			{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do the thing"}}},
			{Role: message.Assistant, Parts: []message.ContentPart{
				message.ToolCall{ID: "call_1", Name: "do_thing", Input: "{}", Finished: true},
			}},
			{Role: message.Tool, Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "call_1", Content: "done"},
			}},
		},
	)
	tools := client.ConvertTools([]llmtools.Tool{schemaOnlyTool("do_thing"), schemaOnlyTool("other_thing")})

	sites := cacheControlsIn(t, client.preparedParams(messages, tools))

	if len(sites) != 4 {
		t.Fatalf("got %d cache_control breakpoints, want exactly 4: %+v", len(sites), sites)
	}
	assertBareEphemeral(t, sites)

	counts := countPaths(sites)
	// Two system prompts, each marked on a text content PART inside the
	// system message's array content.
	if got := counts[".messages[].content[]"]; got != 2 {
		t.Errorf("got %d breakpoints on message content parts, want 2 (the system prompts)", got)
	}
	// The tool result takes a MESSAGE-level key, not a part-level one.
	if got := counts[".messages[]"]; got != 1 {
		t.Errorf("got %d message-level breakpoints, want 1 (the tool result)", got)
	}
	// The last tool definition is marked at the TOP level of the tool object,
	// which is a sibling of "function", never inside it.
	if got := counts[".tools[]"]; got != 1 {
		t.Errorf("got %d top-level tool breakpoints, want 1 (the last tool)", got)
	}
	if got := counts[".tools[].function"]; got != 0 {
		t.Errorf("got %d breakpoints inside tools[].function, want 0 (must be top level)", got)
	}
}

// A request whose last message is a user turn: the breakpoint belongs on that
// message's last content part.
func TestClaudeCacheBreakpoints_LastUserMessage(t *testing.T) {
	client := &ReliantClient{Options: llm.DriverOptions{
		Model: models.Model{APIModel: "claude-opus-5-5"},
	}}

	messages := client.ConvertMessages(nil, []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "first"}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "second and last"}}},
	})

	params := client.preparedParams(messages, nil)
	sites := cacheControlsIn(t, params)

	if len(sites) != 1 {
		t.Fatalf("got %d breakpoints, want exactly 1 (the last message): %+v", len(sites), sites)
	}
	assertBareEphemeral(t, sites)

	// Prove it is on the LAST message's last part, not the first message's.
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var body struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(body.Messages))
	}
	lastParts := body.Messages[1].Content
	if len(lastParts) == 0 {
		t.Fatalf("last message has no array content; cache_control needs a text part: %s", raw)
	}
	if _, ok := lastParts[len(lastParts)-1]["cache_control"]; !ok {
		t.Errorf("last content part of the last message carries no cache_control: %s", raw)
	}
	for i, part := range body.Messages[0].Content {
		if _, ok := part["cache_control"]; ok {
			t.Errorf("first message part %d carries cache_control; only the last message should", i)
		}
	}
}

// Gemini through the same gateway must get nothing: LiteLLM's Gemini path does
// not document what it does with cache_control, and an untraced key on every
// request is not worth a cache we cannot confirm.
func TestClaudeCacheBreakpoints_NoneForGemini(t *testing.T) {
	client := &ReliantClient{Options: llm.DriverOptions{
		Model: models.Model{APIModel: "gemini-3-pro"},
	}}

	messages := client.ConvertMessages(
		[]string{"system one", "system two"},
		[]message.Message{
			{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
			{Role: message.Tool, Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "call_1", Content: "done"},
			}},
		},
	)
	tools := client.ConvertTools([]llmtools.Tool{schemaOnlyTool("do_thing")})

	if sites := cacheControlsIn(t, client.preparedParams(messages, tools)); len(sites) != 0 {
		t.Fatalf("gemini request carries %d cache_control breakpoints, want 0: %+v", len(sites), sites)
	}
}

func TestClaudeCacheBreakpoints_NoneWhenCacheDisabled(t *testing.T) {
	client := &ReliantClient{Options: llm.DriverOptions{
		Model:        models.Model{APIModel: "claude-opus-5-5"},
		DisableCache: true,
	}}

	messages := client.ConvertMessages(
		[]string{"system one", "system two"},
		[]message.Message{
			{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
			{Role: message.Tool, Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "call_1", Content: "done"},
			}},
		},
	)
	tools := client.ConvertTools([]llmtools.Tool{schemaOnlyTool("do_thing")})

	if sites := cacheControlsIn(t, client.preparedParams(messages, tools)); len(sites) != 0 {
		t.Fatalf("DisableCache request carries %d breakpoints, want 0: %+v", len(sites), sites)
	}
}

// An assistant-last message gets no breakpoint: LiteLLM copies a message-level
// cache_control off tool results only, so one there would be dropped silently
// and we would pay a 2x write price for a cache that was never created.
func TestClaudeCacheBreakpoints_SkipsAssistantLastMessage(t *testing.T) {
	client := &ReliantClient{Options: llm.DriverOptions{
		Model: models.Model{APIModel: "claude-opus-5-5"},
	}}

	messages := client.ConvertMessages(nil, []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "hi there"}}},
	})

	sites := cacheControlsIn(t, client.preparedParams(messages, nil))
	if len(sites) != 0 {
		t.Fatalf("assistant-last request carries %d breakpoints, want 0: %+v", len(sites), sites)
	}
}
