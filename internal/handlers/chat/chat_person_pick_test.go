// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// Only the chat's owner, signed in as a person, picks the root's provider,
// and only with a model the server did not substitute: only then is the
// selection write marked to record their pick (db.WithPersonPick).
func TestPersonPickContext(t *testing.T) {
	person := auth.WithCredential(context.Background(), auth.CredentialSession)
	cli := auth.WithCredential(context.Background(), auth.CredentialAccessToken)
	inOrg := &db.Chat{ID: "c1", UserID: "alice", OrgID: ptr.Of("org-acme")}
	noOrg := &db.Chat{ID: "c1", UserID: "alice"}
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		sender string
		chat   *db.Chat
		chosen bool
		want   bool
	}{
		{"the owner, as a person", person, "alice", inOrg, true, true},
		{"a substituted root model", person, "alice", inOrg, false, false},
		{"the CLI login or an agent's token", cli, "alice", inOrg, true, false},
		{"a teammate", person, "bob", inOrg, true, false},
		{"a chat with no org", person, "alice", noOrg, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pick, ok := db.PersonPickFrom(personPickContext(tc.ctx, tc.sender, tc.chat, tc.chosen))
			assert.Equal(t, tc.want, ok)
			if tc.want {
				assert.Equal(t, db.PersonPick{UserID: "alice", OrgID: "org-acme"}, pick)
			}
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

	// The person then picks the model that runs; a later send whose pin the
	// server substitutes onto another model supersedes that pick: the write
	// advances the selection's version, so the pick is void by construction.
	_, err = service.SendMessage(ctx, sendContinue(fx.chatID, paramsOf(t, map[string]any{"model": map[string]any{"id": sonnetOnClaude}})))
	require.NoError(t, err)
	driver, err = repo.ChatPersonPickDriver(ctx, fx.chatID, "org-acme")
	require.NoError(t, err)
	require.NotEmpty(t, driver, "the person's own pick")
	_, err = service.SendMessage(ctx, sendContinue(fx.chatID, pin))
	require.NoError(t, err)
	driver, err = repo.ChatPersonPickDriver(ctx, fx.chatID, "org-acme")
	require.NoError(t, err)
	assert.Empty(t, driver, "the substituted send superseded the pick")
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
