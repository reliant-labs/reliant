// Copyright (c) 2025 Reliant Labs

// Package authortagtest holds what every driver's author-tag golden test
// shares: a single-person history as the prompt-assembly boundary loads it,
// and the assertion that tagging leaves that driver's request bytes exactly
// as they were (control-plane docs/design/chat-collaboration.md §6.2: a
// single-user prompt must stay byte-identical, or it loses its cache).
//
// Each driver test passes its own converter (messages to the request payload
// it sends) to RequireSingleAuthorRequestUnchanged.
package authortagtest

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"testing"

	"github.com/reliant-labs/reliant/internal/authortag"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// OwnerName is the owner's display name the histories render with.
const OwnerName = "Alex"

// SingleAuthorHistory is a one-person chat: text, an image, a tool round
// trip, and a follow-up, with every human turn the owner's (no author
// recorded), one Turn per message. It returns fresh values on every call, so
// a converter that rewrites what it is given cannot affect the next call.
func SingleAuthorHistory() ([]message.Message, []authortag.Turn) {
	owner := authortag.Turn{Human: true}
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{
			message.TextContent{Text: "Look at this screenshot and fix the layout."},
			message.BinaryContent{MIMEType: "image/png", Data: tinyPNG()},
		}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "Reading the stylesheet first."},
			message.ToolCall{ID: "call_read_1", Name: "view", Input: `{"file_path":"src/app.css"}`, Finished: true},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call_read_1", Name: "view", Content: ".app { display: grid; }"},
		}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "The grid has no columns; adding them."}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Also check the mobile breakpoint."}}},
	}
	turns := []authortag.Turn{owner, {}, {}, {}, owner}
	return msgs, turns
}

// TwoAuthorHistory is SingleAuthorHistory with a collaborator's turn after it,
// the history that must render differently.
func TwoAuthorHistory() ([]message.Message, []authortag.Turn) {
	msgs, turns := SingleAuthorHistory()
	msgs = append(msgs,
		message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "Checked; it collapses to one column."}}},
		message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Can you add a screenshot test too?"}}},
	)
	turns = append(turns, authortag.Turn{}, authortag.Turn{Human: true, AuthorUserID: "u-dana", AuthorName: "Dana"})
	return msgs, turns
}

// RequireSingleAuthorRequestUnchanged asserts that convert, the driver's
// messages-to-request conversion, produces the same bytes for a single-author
// history whether or not it went through authortag.Render. So that the check
// cannot pass vacuously (a converter that ignored its input), it also asserts
// that a two-author history's request DOES change.
func RequireSingleAuthorRequestUnchanged(t *testing.T, convert func([]message.Message) any) {
	t.Helper()
	marshal := func(msgs []message.Message) []byte {
		t.Helper()
		b, err := json.Marshal(convert(msgs))
		if err != nil {
			t.Fatalf("marshalling the converted request: %v", err)
		}
		return b
	}

	raw, _ := SingleAuthorHistory()
	before := marshal(raw)
	msgs, turns := SingleAuthorHistory()
	after := marshal(authortag.Render(msgs, turns, OwnerName))
	if !bytes.Equal(before, after) {
		t.Fatalf("a single-author request changed after author tagging:\nbefore %s\nafter  %s", before, after)
	}

	raw2, _ := TwoAuthorHistory()
	untagged := marshal(raw2)
	msgs2, turns2 := TwoAuthorHistory()
	tagged := marshal(authortag.Render(msgs2, turns2, OwnerName))
	if bytes.Equal(untagged, tagged) {
		t.Fatalf("a two-author request did not change after author tagging; the converter does not carry the tag: %s", tagged)
	}
}

func tinyPNG() []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
