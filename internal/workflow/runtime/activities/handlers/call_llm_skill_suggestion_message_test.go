// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/require"
)

// TestSkillSuggestionsDoNotEditTheUserMessage pins the boundary this engine
// must not cross: whatever the engine adds to a request, the user's own turn is
// theirs.
//
// The suggester used to append its <system-reminder> onto the last user
// message's text. That made the user's words not be the user's words — which is
// a correctness problem in the IDE (the transcript no longer matches what was
// sent) and a trust problem for an API caller, whose one certainty is what they
// put in the request. It also meant the model saw the user asking for skills
// they never mentioned.
//
// The suggestion is still delivered; it arrives as its own message.
func TestSkillSuggestionsDoNotEditTheUserMessage(t *testing.T) {
	catalog := preloadTestCatalog()

	const userText = "PROTO body — regenerate the proto surface"
	history := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: userText}}},
	}

	n := injectSkillSuggestions(&history, catalog, nil)
	require.Positive(t, n, "precondition: this input must produce suggestions")

	// Find the user's turn wherever it now sits.
	var userTurns []message.Message
	for _, m := range history {
		if m.Role == message.User {
			userTurns = append(userTurns, m)
		}
	}
	require.NotEmpty(t, userTurns, "the user's turn must survive")

	require.Equal(t, userText, userTurns[0].Content().Text,
		"the user's message was modified; suggestions must be their own message")

	// ...and the suggestion still reaches the model.
	var combined strings.Builder
	for _, m := range history {
		combined.WriteString(m.Content().Text)
		combined.WriteString("\n")
	}
	require.Contains(t, combined.String(), "Potentially relevant skills",
		"the suggestion must still be delivered, just not inside the user's turn")
}

// TestSkillSuggestionsArriveAfterTheUserTurn keeps the reminder positioned where
// it is useful. A suggestion about the current request has to follow it, or the
// model reads guidance about a request it has not seen yet.
func TestSkillSuggestionsArriveAfterTheUserTurn(t *testing.T) {
	catalog := preloadTestCatalog()

	history := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "PROTO body — regenerate the proto surface"}}},
	}

	require.Positive(t, injectSkillSuggestions(&history, catalog, nil))

	lastUser, reminder := -1, -1
	for i, m := range history {
		if m.Role == message.User && !strings.Contains(m.Content().Text, "Potentially relevant skills") {
			lastUser = i
		}
		if strings.Contains(m.Content().Text, "Potentially relevant skills") {
			reminder = i
		}
	}
	require.GreaterOrEqual(t, lastUser, 0, "expected a user turn")
	require.GreaterOrEqual(t, reminder, 0, "expected a reminder message")
	require.Greater(t, reminder, lastUser,
		"the reminder must follow the request it is about")
}
