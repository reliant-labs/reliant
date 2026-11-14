// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"regexp"
	"strings"
)

// A pull request a chat opens says which chat it is, so automations that react
// to it later (a review asking for changes, a merge queue ejecting it) can hand
// the news to that chat while it is still working instead of starting another
// agent on the same branch. The marker is an HTML comment, invisible on GitHub:
//
//	<!-- reliant-chat: <chat id> -->
//
// A shell command carries the same id as ChatIDEnv, for a pull request opened
// with `gh pr create`.

// owningChatMarker is the marker for chatID.
func owningChatMarker(chatID string) string {
	return fmt.Sprintf("<!-- reliant-chat: %s -->", chatID)
}

// owningChatMarkerRe finds any chat marker, whoever's.
var owningChatMarkerRe = regexp.MustCompile(`<!--\s*reliant-chat:\s*[^\s>]+\s*-->`)

// owningChatStamped names the integration actions that create something a
// chat owns, and the param that carries the marker.
var owningChatStamped = map[string]string{
	"github/pr.create": "body",
}

// stampOwningChat returns params with the owning chat's marker appended to the
// action's stamped param, unless the action stamps nothing, there is no chat,
// or the text already names a chat (the caller's own marker wins). params is
// never modified.
func stampOwningChat(integration, action string, params map[string]any, chatID string) map[string]any {
	field, ok := owningChatStamped[integration+"/"+action]
	if !ok || strings.TrimSpace(chatID) == "" {
		return params
	}
	text, _ := params[field].(string)
	if owningChatMarkerRe.MatchString(text) {
		return params
	}
	out := make(map[string]any, len(params)+1)
	for k, v := range params {
		out[k] = v
	}
	if strings.TrimSpace(text) == "" {
		out[field] = owningChatMarker(chatID)
	} else {
		out[field] = strings.TrimRight(text, "\n") + "\n\n" + owningChatMarker(chatID)
	}
	return out
}
