// Copyright (c) 2025 Reliant Labs

// Package authortag names the human author of each turn in the model's view
// of a chat that more than one person writes in (control-plane
// docs/design/chat-collaboration.md §6.2).
//
// A provider request has no field for the author: Anthropic's Messages API has
// no `name`, and neither does OpenAI's Responses API. So the author is text, a
// leading `<message from="Dana">` part on the human turn, following the
// pattern the mailbox drain already uses for agent messages.
//
// The rule is shaped by the prompt cache:
//
//   - A history in which no message has a recorded author is returned
//     UNCHANGED (the same slice), so every single-person chat sends
//     byte-identical requests and keeps its cache.
//   - Turns before the first message with an author stay untagged, so the
//     cached prefix survives the first collaborator's message. That first
//     tagged turn says whose the untagged ones were
//     (`<message from="Dana" earlier_turns_from="Alex">`); from there on every
//     human turn is tagged.
//   - The threshold and every name come from the loaded history alone (the
//     stored author_name, never a lookup at turn time), so a replay renders the
//     same bytes and a rename never rewrites a turn already sent.
//
// Names are display names only, never an email or an id. Two authors with the
// same name are told apart by a suffix (" (2)") assigned in the order they
// first appear, which keeps every earlier assignment fixed as people join.
//
//forge:exclude-contract: a pure transform over loaded messages; no state, nothing to mock
package authortag

import (
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// FallbackMemberName is the name of an author whose name was not recorded
// (they set none, or control-plane predates display_name). Never an email or
// an id.
const FallbackMemberName = "a member"

// FallbackOwnerName is the owner's name when the caller has none for them.
const FallbackOwnerName = "the owner"

// Turn describes one loaded message's authorship. Render takes one per message,
// in the same order.
type Turn struct {
	// Human is true for a turn a person wrote: a user-role message that no
	// agent sent and that is visible in the transcript. Everything else (the
	// model's output, tool results, agent and mailbox messages, LLM-only
	// rows) is never tagged.
	Human bool
	// AuthorUserID is the author when it is not the chat's owner
	// (messages.author_user_id); empty means the owner. A turn with an author
	// is human whatever Human says.
	AuthorUserID string
	// AuthorName is the author's display name as stored at send time
	// (messages.author_name); empty renders FallbackMemberName.
	AuthorName string
}

func (t Turn) human() bool { return t.Human || t.AuthorUserID != "" }

// Render returns msgs with every human turn from the first authored one on
// prefixed by its author. turns[i] describes msgs[i]; ownerName is the chat
// owner's display name, used for the owner's turns once tagging starts (empty
// renders FallbackOwnerName).
//
// When no turn has an author, or turns does not match msgs, it returns msgs
// itself, untouched. Otherwise it returns a new slice; the messages it tags
// get new Parts, and msgs is never modified.
func Render(msgs []message.Message, turns []Turn, ownerName string) []message.Message {
	if len(turns) != len(msgs) {
		return msgs
	}
	threshold := -1
	for i, t := range turns {
		if t.AuthorUserID != "" {
			threshold = i
			break
		}
	}
	if threshold < 0 {
		return msgs
	}

	earlierHuman := false
	for _, t := range turns[:threshold] {
		if t.human() {
			earlierHuman = true
			break
		}
	}

	names := newNamer()
	owner := strings.TrimSpace(ownerName)
	if owner == "" {
		owner = FallbackOwnerName
	}
	// The untagged turns are the owner's, so the owner appears first.
	var earlier string
	if earlierHuman {
		earlier = names.name("", owner)
	}

	out := make([]message.Message, len(msgs))
	copy(out, msgs)
	for i := threshold; i < len(msgs); i++ {
		t := turns[i]
		if !t.human() {
			continue
		}
		var from string
		if t.AuthorUserID == "" {
			from = names.name("", owner)
		} else {
			from = names.name(t.AuthorUserID, t.AuthorName)
		}
		tag := "<message from=" + strconv.Quote(from)
		if i == threshold && earlierHuman {
			tag += " earlier_turns_from=" + strconv.Quote(earlier)
		}
		tag += ">"
		parts := make([]message.ContentPart, 0, len(msgs[i].Parts)+1)
		parts = append(parts, message.TextContent{Text: tag})
		parts = append(parts, msgs[i].Parts...)
		out[i].Parts = parts
	}
	return out
}

// namer assigns each author one rendered name, at first appearance, and never
// changes it: the second distinct author with a name already taken gets
// " (2)", the third " (3)".
type namer struct {
	byAuthor map[string]string // author key ("" is the owner) -> rendered name
	taken    map[string]bool   // rendered names in use
	count    map[string]int    // base name -> authors seen with it
}

func newNamer() *namer {
	return &namer{byAuthor: map[string]string{}, taken: map[string]bool{}, count: map[string]int{}}
}

func (n *namer) name(key, base string) string {
	if got, ok := n.byAuthor[key]; ok {
		return got
	}
	base = strings.TrimSpace(base)
	if base == "" {
		base = FallbackMemberName
	}
	name := base
	for n.taken[name] {
		n.count[base]++
		name = base + " (" + strconv.Itoa(n.count[base]+1) + ")"
	}
	n.taken[name] = true
	n.byAuthor[key] = name
	return name
}
