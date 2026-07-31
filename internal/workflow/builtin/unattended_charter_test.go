// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// topLevelInjectContent returns a top-level node's `thread.inject.content` — the
// literal prompt text the workflow ships for a node that is not inside a loop.
func topLevelInjectContent(t *testing.T, file, nodeID string) string {
	t.Helper()
	doc := loadWorkflowYAML(t, file)
	node := nodeByID(t, doc, nodeID)
	inject := mapAt(t, mapAt(t, node, "thread"), "inject")
	content, ok := inject["content"].(string)
	require.True(t, ok, "%s.%s inject content must be a string", file, nodeID)
	return content
}

func scaffoldInputs(unattended bool) map[string]interface{} {
	return map[string]interface{}{
		"working_directory": "/srv/app",
		"mode":              "auto",
		"ask":               true,
		"unattended":        unattended,
	}
}

// TestScaffoldCharterDoesNotOpenWithAGateWhenNobodyIsWatching pins the paragraph
// that made forge-one-shot unrunnable unattended.
//
// scaffold_and_verify's charter opened, unconditionally, with "**Start by talking
// to the user — you have `ask_user`.**" Measured across four runs whose intent all
// ended with the literal words "Do not block on me — make reasonable decisions and
// keep going": run 1 raised the gate 14s in and sat on it for 22m43s — 83% of a
// 27-minute run — and produced zero bytes. Runs 2-4 were only unblocked because a
// supervisor agent was babysitting them.
//
// `ask` did not gate it and could not: it is read at exactly one edge condition
// inside get-it-right's loop, and scaffold_and_verify sets review_enabled: false,
// so the edge is unreachable. Setting ask=false changed nothing.
func TestScaffoldCharterDoesNotOpenWithAGateWhenNobodyIsWatching(t *testing.T) {
	template := topLevelInjectContent(t, "forge-one-shot.yaml", "scaffold_and_verify")

	unattended := renderPrompt(t, template, scaffoldInputs(true), nil, nil, 0)

	require.NotContains(t, unattended, "Start by talking to the user",
		"an unattended run has nobody to talk to; opening with this is what cost run 1 "+
			"22m43s of a 27-minute run")
	require.Contains(t, unattended, "There is NO human in this run",
		"the agent must be told plainly why it is not asking, or it will ask anyway")
	require.Contains(t, unattended, "do not call `ask_user`",
		"the tool layer short-circuits the call, but a wasted turn is still a wasted turn")
	require.Contains(t, unattended, "Decisions made without the user",
		"an invented decision must land in FORGE_PLAN.md under its own heading — that section "+
			"is the only artifact separating what the agent chose from what the user chose")
}

// The interactive path must be unchanged when the input is off.
func TestScaffoldCharterStillOpensTheConversationWhenAHumanIsThere(t *testing.T) {
	template := topLevelInjectContent(t, "forge-one-shot.yaml", "scaffold_and_verify")

	interactive := renderPrompt(t, template, scaffoldInputs(false), nil, nil, 0)

	require.Contains(t, interactive, "Start by talking to the user",
		"unattended is opt-in; the default run still scopes with the user")
	require.Contains(t, interactive, "`ask_user`")
	require.NotContains(t, interactive, "There is NO human in this run")
	require.NotContains(t, interactive, "Decisions made without the user",
		"there is a user, so there are no decisions made without them")
}

// TestScaffoldCharterNeverAsksWhatTheOpeningMessageAlreadyAnswered pins the
// narrower bug, which is separate from unattended and survives it.
//
// Three consecutive runs asked for branding against an intent that specified
// "Clinical Minimal styling" verbatim. The old escape clause — "Skip anything
// their opening message already answered — re-asking reads as not having
// listened" — governed the CONTENT of the questions, not whether to ask at all,
// and the model resolved the conflict in favour of the earlier, bolder, more
// specific instruction to open by asking. The escape has to say plainly that an
// explicit instruction overrides the directive to ask.
func TestScaffoldCharterNeverAsksWhatTheOpeningMessageAlreadyAnswered(t *testing.T) {
	template := topLevelInjectContent(t, "forge-one-shot.yaml", "scaffold_and_verify")
	interactive := renderPrompt(t, template, scaffoldInputs(false), nil, nil, 0)

	require.Contains(t, interactive, "do not ask about that topic at all",
		"the escape clause must override the directive to ASK, not merely trim the question — "+
			"three runs asked for branding against an intent that named the style verbatim")
	require.Contains(t, interactive, "do not ask about branding",
		"branding is the surface that actually failed three times; name it, do not leave it "+
			"to be inferred from a general principle the model already had and ignored")
	require.Contains(t, interactive, "do not raise a gate",
		"\"do not block on me\" appeared verbatim in all four measured intents and was ignored "+
			"four times; the charter has to name that instruction as binding")
	require.Contains(t, interactive, "ask nothing and go straight to FORGE_PLAN.md",
		"asking zero questions must be stated as a legitimate outcome, or a charter that opens "+
			"with 'start by talking to the user' reads as requiring at least one round")

	require.NotContains(t, interactive, "Skip anything their opening message already answered",
		"this is the wording that lost to the bolder instruction above it; it must not survive "+
			"alongside its replacement, or the same conflict is back")
}

// The two flags are not the same switch, and merging them would silently change
// every existing caller: `ask` decides whether a PRESENT human is paused for
// between iterations, `unattended` says there is no human at all.
func TestUnattendedIsDeclaredSeparatelyFromAsk(t *testing.T) {
	for _, file := range []string{"forge-one-shot.yaml", "get-it-right.yaml"} {
		doc := loadWorkflowYAML(t, file)
		inputs := mapAt(t, doc, "inputs")

		unattended, ok := inputs["unattended"].(map[string]interface{})
		require.True(t, ok, "%s must declare an `unattended` input", file)
		require.Equal(t, "boolean", unattended["type"], "%s: unattended is a boolean", file)
		require.Equal(t, false, unattended["default"],
			"%s: unattended must default to OFF — a human being present is the normal case, "+
				"and defaulting it on would silently strip the scoping conversation", file)

		ask, ok := inputs["ask"].(map[string]interface{})
		require.True(t, ok, "%s must still declare `ask`", file)
		require.NotEqual(t, ask["default"], unattended["default"],
			"%s: `ask` defaults true and `unattended` defaults false — if these ever converge, "+
				"check that they have not been quietly merged into one switch", file)
	}
}
