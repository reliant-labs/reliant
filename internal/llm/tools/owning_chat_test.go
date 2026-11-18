// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStampOwningChat(t *testing.T) {
	const chat = "0b6f6f7e-1111-4222-8333-944455556666"
	marker := "<!-- reliant-chat: " + chat + " -->"

	t.Run("a pull request body gets the marker", func(t *testing.T) {
		in := map[string]any{"title": "Fix", "body": "Fixes #42.\n"}
		out := stampOwningChat("github", "pr.create", in, chat)
		require.Equal(t, "Fixes #42.\n\n"+marker, out["body"])
		require.Equal(t, "Fixes #42.\n", in["body"], "the caller's params are not modified")
	})
	t.Run("an empty body is the marker alone", func(t *testing.T) {
		out := stampOwningChat("github", "pr.create", map[string]any{"title": "Fix"}, chat)
		require.Equal(t, marker, out["body"])
	})
	t.Run("a body that already names a chat is left alone", func(t *testing.T) {
		in := map[string]any{"body": "x\n<!-- reliant-chat: someone-else -->"}
		require.Equal(t, in, stampOwningChat("github", "pr.create", in, chat))
	})
	t.Run("other actions and chatless calls are untouched", func(t *testing.T) {
		in := map[string]any{"body": "x"}
		require.Equal(t, in, stampOwningChat("github", "issue.create", in, chat))
		require.Equal(t, in, stampOwningChat("github", "pr.create", in, ""))
	})
}

func TestWithChatEnv(t *testing.T) {
	own := map[string]string{"A": "1"}
	got := withChatEnv(own, "c1", "")
	require.Equal(t, map[string]string{"A": "1", ChatIDEnv: "c1"}, got)
	require.Equal(t, map[string]string{"A": "1"}, own, "the caller's env is not modified")
	require.Equal(t, map[string]string{ChatIDEnv: "mine"}, withChatEnv(map[string]string{ChatIDEnv: "mine"}, "c1", ""))
	require.Nil(t, withChatEnv(nil, "", ""))
}

// RELIANT_PROJECT_ID is how forge, run from a chat, names the Reliant project
// a release came from (control-plane unified-shell-and-projects.md §8.1).
func TestWithChatEnvSetsTheProjectID(t *testing.T) {
	own := map[string]string{"A": "1"}
	got := withChatEnv(own, "c1", "p1")
	require.Equal(t, map[string]string{"A": "1", ChatIDEnv: "c1", ProjectIDEnv: "p1"}, got)
	require.Equal(t, map[string]string{"A": "1"}, own, "the caller's env is not modified")
	require.Equal(t, "RELIANT_PROJECT_ID", ProjectIDEnv, "forge reads this name; it is a public surface")

	require.Equal(t, map[string]string{ChatIDEnv: "c1", ProjectIDEnv: "mine"},
		withChatEnv(map[string]string{ProjectIDEnv: "mine"}, "c1", "p1"), "a caller's own value wins")
	require.Equal(t, map[string]string{ChatIDEnv: "c1"}, withChatEnv(nil, "c1", ""), "no project, no variable")
}
