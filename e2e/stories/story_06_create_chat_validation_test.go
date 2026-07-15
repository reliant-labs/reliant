// Copyright (c) 2025 Reliant Labs
//
//go:build e2e

package stories

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// Story 06: invalid CreateChat requests must surface a clean InvalidArgument
// error and leave NOTHING behind — no chat row, no workflow row, no dead chat
// in the sidebar.
//
// KNOWN GAP (documented, not pinned here): validation runs before the chat
// insert, but the Temporal ExecuteWorkflow call happens AFTER the chat +
// workflow rows and the first messages are persisted (chat_crud.go). If
// ExecuteWorkflow itself fails (Temporal outage), a dead chat is left behind.
// TODO: pin that with a story once CreateChat is made atomic (or gains
// compensation) — simulating a Temporal outage against the shared dev server
// would poison the other stories today.
func TestStory06_CreateChatValidationFailsClean(t *testing.T) {
	t.Parallel()

	// No LLM turns should ever be consumed.
	script := NewScriptedLLM()
	h := newHarness(t, script)

	cases := []struct {
		name   string
		run    func() error
		errHas string
	}{
		{
			name: "unknown model id",
			run: func() error {
				_, err := h.TryCreateChat("builtin://agent", "hello", map[string]any{
					"model": map[string]any{"id": "definitely-not-a-model"},
				})
				return err
			},
			errHas: "workflow input validation failed",
		},
		{
			name: "unknown builtin workflow",
			run: func() error {
				_, err := h.TryCreateChat("builtin://does-not-exist", "hello", nil)
				return err
			},
			errHas: "not found",
		},
		{
			name: "empty message",
			run: func() error {
				_, err := h.TryCreateChat("builtin://agent", "", nil)
				return err
			},
			errHas: "at least one user message",
		},
		{
			name: "dotted workflow param key",
			run: func() error {
				_, err := h.TryCreateChat("builtin://agent", "hello", map[string]any{
					"agent.model": "mock",
				})
				return err
			},
			errHas: "dotted key",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			require.Error(t, err, "CreateChat must reject the request")
			var cerr *connect.Error
			require.ErrorAs(t, err, &cerr)
			assert.Equal(t, connect.CodeInvalidArgument, cerr.Code(),
				"validation failures must be InvalidArgument, got %s: %s", cerr.Code(), cerr.Message())
			assert.Contains(t, cerr.Message(), tc.errHas)
		})
	}

	// Nothing leaked: the project has zero chats (no dead chat rows).
	chats, err := h.Stack.Repo.ListChats(h.Ctx, db.ChatFilters{
		UserID:    h.UserID,
		ProjectID: &h.ProjectID,
		Limit:     100,
	})
	require.NoError(t, err)
	assert.Empty(t, chats, "failed CreateChat calls must not leave dead chats behind")

	// And no LLM calls happened.
	assert.Empty(t, h.LLM.StreamCalls())
}
