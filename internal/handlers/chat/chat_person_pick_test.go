// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// Only the chat's owner, signed in as a person, picks the root's provider
// (chat_person_picks), and only with a model the server did not substitute;
// every other write of the root model removes the pick it supersedes.
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
		chosen bool
		want   []string
	}{
		{"the owner, as a person", person, "alice", inOrg, true, []string{"c1/alice/org-acme/gpt-5.6-sol@codex/codex"}},
		{"a substituted root model", person, "alice", inOrg, false, []string{"cleared c1"}},
		{"the CLI login or an agent's token", cli, "alice", inOrg, true, []string{"cleared c1"}},
		{"a teammate", person, "bob", inOrg, true, []string{"cleared c1"}},
		{"a chat with no org", person, "alice", noOrg, true, []string{"cleared c1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			repo := &MockRepository{
				RecordChatPersonPickFunc: func(_ context.Context, chatID, userID, orgID, model, provider string) error {
					got = append(got, chatID+"/"+userID+"/"+orgID+"/"+model+"/"+provider)
					return nil
				},
				ClearChatPersonPickFunc: func(_ context.Context, chatID string) error {
					got = append(got, "cleared "+chatID)
					return nil
				},
			}
			(&Service{deps: Deps{Repo: repo}}).recordPersonPick(tc.ctx, tc.sender, tc.chat, sel, tc.chosen)
			assert.Equal(t, tc.want, got)
		})
	}
}

// An ordinary send whose root model the server substitutes onto the person's
// own provider (#685) is no pick of that provider: nothing is recorded, a
// pick it supersedes is removed, and the call still asks.
func TestSendMessage_SubstitutedRootModelIsNoPick(t *testing.T) {
	ctx, fx, repo, temporal, service := selectionFixture(t, db.Completed())
	_, err := repo.ORM().Bun().NewRaw(`UPDATE chats SET org_id = 'org-acme' WHERE id = ?`, fx.chatID).Exec(ctx)
	require.NoError(t, err)
	ctx = authz.WithMembership(auth.WithCredential(ctx, auth.CredentialSession), memberOfAcme{})
	pin := paramsOf(t, map[string]any{"model": map[string]any{"id": codexOnlyModel}})

	_, err = service.SendMessage(ctx, sendContinue(fx.chatID, pin))
	require.NoError(t, err)
	moved := modelID(t, temporal.lastStart(t).Inputs["model"])
	require.True(t, assertAnthropic(moved), "the pin moved onto the person's own Anthropic key, got %q", moved)
	driver, err := repo.ChatPersonPickDriver(ctx, fx.chatID, "org-acme")
	require.NoError(t, err)
	assert.Empty(t, driver, "a substituted provider is never the person's pick")

	// An earlier pick the selection now matches again is superseded by the
	// next substituted send, not revived by it.
	require.NoError(t, repo.RecordChatPersonPick(ctx, fx.chatID, "test-user", "org-acme", moved, ""))
	_, err = service.SendMessage(ctx, sendContinue(fx.chatID, pin))
	require.NoError(t, err)
	driver, err = repo.ChatPersonPickDriver(ctx, fx.chatID, "org-acme")
	require.NoError(t, err)
	assert.Empty(t, driver, "the substituted send removed the superseded pick")
}

// The person's own unsubstituted pick of the root model, in an org, is
// recorded: the positive control for the test above.
func TestSendMessage_PersonsRootPickIsRecorded(t *testing.T) {
	ctx, fx, repo, _, service := selectionFixture(t, db.Completed())
	_, err := repo.ORM().Bun().NewRaw(`UPDATE chats SET org_id = 'org-acme' WHERE id = ?`, fx.chatID).Exec(ctx)
	require.NoError(t, err)
	ctx = authz.WithMembership(auth.WithCredential(ctx, auth.CredentialSession), memberOfAcme{})
	_, err = service.SendMessage(ctx, sendContinue(fx.chatID, paramsOf(t, map[string]any{"model": map[string]any{"id": sonnetOnClaude}})))
	require.NoError(t, err)
	driver, err := repo.ChatPersonPickDriver(ctx, fx.chatID, "org-acme")
	require.NoError(t, err)
	_, want := db.RootModelPick(map[string]any{"model": map[string]any{"id": sonnetOnClaude}})
	assert.Equal(t, want, driver)
}

// memberOfAcme says every user is a member of org-acme.
type memberOfAcme struct{}

func (memberOfAcme) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	return orgID == "org-acme", true, nil
}
