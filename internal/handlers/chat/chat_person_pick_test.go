// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// Only the chat's owner, signed in as a person, picks the root's provider
// (chat_person_picks); every other writer of the selection records nothing.
func TestRecordPersonPick(t *testing.T) {
	person := auth.WithCredential(context.Background(), auth.CredentialSession)
	cli := auth.WithCredential(context.Background(), auth.CredentialAccessToken)
	inOrg := &db.Chat{ID: "c1", UserID: "alice", OrgID: ptr.Of("org-acme")}
	noOrg := &db.Chat{ID: "c1", UserID: "alice"}
	sel := &chatselection.Selection{Params: map[string]any{"model": map[string]any{"id": "gpt-5.6-sol@codex"}}, ChatProvider: "codex"}

	for _, tc := range []struct {
		name   string
		ctx    context.Context
		sender string
		chat   *db.Chat
		want   []string
	}{
		{"the owner, as a person", person, "alice", inOrg, []string{"c1/alice/org-acme/gpt-5.6-sol@codex/codex"}},
		{"the CLI login or an agent's token", cli, "alice", inOrg, nil},
		{"a teammate", person, "bob", inOrg, nil},
		{"a chat with no org", person, "alice", noOrg, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			repo := &MockRepository{RecordChatPersonPickFunc: func(_ context.Context, chatID, userID, orgID, model, provider string) error {
				got = append(got, chatID+"/"+userID+"/"+orgID+"/"+model+"/"+provider)
				return nil
			}}
			(&Service{deps: Deps{Repo: repo}}).recordPersonPick(tc.ctx, tc.sender, tc.chat, sel)
			assert.Equal(t, tc.want, got)
		})
	}
}
