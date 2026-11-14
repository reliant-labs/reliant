// Copyright (c) 2025 Reliant Labs
package chatselection

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
)

// racingStore is memStore with a hook that runs inside MutateChatSelection,
// after the caller read the record and before its mutation sees the locked
// current row: the deterministic stand-in for another writer landing in that
// window.
type racingStore struct {
	*memStore
	beforeLock func()
}

func (r *racingStore) MutateChatSelection(ctx context.Context, chatID string, mutate func(*Selection) (*Selection, error)) (*Selection, error) {
	if hook := r.beforeLock; hook != nil {
		r.beforeLock = nil
		hook()
	}
	return r.memStore.MutateChatSelection(ctx, chatID, mutate)
}

// A software caller never changes the funding order, even when a person
// changed it between the caller's read and its write: the decision is made
// against the locked current row (review of #2694, round 3, B1).
func TestSetPolicy_SoftwareNeverChangesFunding(t *testing.T) {
	ctx := context.Background()
	person := auth.WithCredential(ctx, auth.CredentialSession)
	for _, c := range []auth.Credential{auth.CredentialAccessToken, auth.CredentialOAuthClient, auth.CredentialUnknown} {
		t.Run(c.String(), func(t *testing.T) {
			software := auth.WithCredential(ctx, c)
			store := &racingStore{memStore: newMemStore()}
			svc := &service{store: store}

			// The software caller read a record with no funding order and
			// sends a change to on_limit only. Before its write takes the
			// lock, the person sets org_only.
			store.beforeLock = func() {
				_, err := svc.SetPolicy(person, "chat-1", "u1", "wf", ProviderPolicy{Funding: FundingOrgOnly}, 0)
				require.NoError(t, err)
			}
			sel, err := svc.SetPolicy(software, "chat-1", "u1", "wf", ProviderPolicy{OnLimit: OnLimitWait}, 0)
			require.NoError(t, err)
			assert.Equal(t, OnLimitWait, sel.Policy.OnLimit, "the other field changes")
			assert.Equal(t, FundingOrgOnly, sel.Policy.Funding, "the person's order, set in the window, survives")

			for _, f := range []Funding{FundingPersonalFirst, FundingOrgFirst} {
				_, err = svc.SetPolicy(software, "chat-1", "u1", "wf", ProviderPolicy{Funding: f}, 0)
				require.Error(t, err, "%s naming %s", c, f)
			}
			got, _ := svc.Get(ctx, "chat-1")
			assert.Equal(t, FundingOrgOnly, got.Policy.Funding)

			sel, err = svc.SetPolicy(person, "chat-1", "u1", "wf", ProviderPolicy{}, 0)
			require.NoError(t, err)
			assert.Equal(t, FundingInherit, sel.Policy.Funding, "a person clears it")
		})
	}
}
