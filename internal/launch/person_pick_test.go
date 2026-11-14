// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// A start records its root model as the chat's person pick (chat_person_picks)
// only when the chat's owner made it, signed in as a person, in a chat with an
// org, and it names a root model: StartChat's recording path
// (recordStartSelection). Every other start records none.
func TestRecordStartSelection_PersonPickMatrix(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	l, _ := newTestLauncher(t, repo, &fakeStarter{})
	userCtx := context.WithValue(ctx, auth.UserIDContextKey, launchTestUserID)
	rootModel := map[string]*structpb.Value{"model": structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{
		"id": structpb.NewStringValue("gpt-5.6-sol@codex"),
	}})}
	modeOnly := map[string]*structpb.Value{"mode": structpb.NewStringValue("plan")}
	org := "org-acme"

	for _, tc := range []struct {
		name   string
		cred   *auth.Credential
		owner  string
		org    *string
		params map[string]*structpb.Value
		pick   bool
	}{
		{name: "the owner's session: a pick", cred: credential(auth.CredentialSession), owner: launchTestUserID, org: &org, params: rootModel, pick: true},
		{name: "a software credential (an access token)", cred: credential(auth.CredentialAccessToken), owner: launchTestUserID, org: &org, params: rootModel},
		{name: "an OAuth client", cred: credential(auth.CredentialOAuthClient), owner: launchTestUserID, org: &org, params: rootModel},
		{name: "an unknown credential", owner: launchTestUserID, org: &org, params: rootModel},
		{name: "a starter who is not the chat's owner", cred: credential(auth.CredentialSession), owner: "someone-else", org: &org, params: rootModel},
		{name: "a chat with no org", cred: credential(auth.CredentialSession), owner: launchTestUserID, params: rootModel},
		{name: "a start that names no root model", cred: credential(auth.CredentialSession), owner: launchTestUserID, org: &org, params: modeOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			chat := &db.Chat{ID: uuid.NewString(), UserID: tc.owner, Title: "start", ProjectID: projectID,
				State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now}
			require.NoError(t, repo.CreateChat(ctx, chat))
			chat.OrgID = tc.org
			c := userCtx
			if tc.cred != nil {
				c = auth.WithCredential(userCtx, *tc.cred)
			}
			require.NoError(t, l.recordStartSelection(c, launchTestUserID, chat, "builtin://agent", tc.params))

			pick, err := store.GetChatPersonPick(ctx, repo.ORM(), chat.ID)
			require.NoError(t, err)
			driver, err := repo.ChatPersonPickDriver(ctx, chat.ID, org)
			require.NoError(t, err)
			if tc.pick {
				require.NotNil(t, pick, "the owner's own start is their pick")
				assert.Equal(t, "codex", driver)
				return
			}
			assert.Nil(t, pick, "no pick recorded")
			assert.Empty(t, driver)
		})
	}
}

func credential(c auth.Credential) *auth.Credential { return &c }
