// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// A chat's funding order is consent to spend the person's own AI accounts in
// its org (org-provider-accounts §4.4, §10): only a person changes it. An
// agent's or a script's credential may still change every other field of the
// policy; leaving funding unspecified (an older client) keeps the stored order,
// and naming another order is refused, a loosening and a tightening alike.
func TestUpdateChatProviderPolicy_FundingIsPersonOnly(t *testing.T) {
	ctx, fx, _, _, service := selectionFixture(t, db.Completed())
	as := func(c auth.Credential) context.Context { return auth.WithCredential(ctx, c) }
	update := func(ctx context.Context, p *reliantv1.ProviderPolicy) (*reliantv1.ProviderPolicy, error) {
		resp, err := service.UpdateChatProviderPolicy(ctx, connect.NewRequest(&reliantv1.UpdateChatProviderPolicyRequest{ChatId: fx.chatID, ProviderPolicy: p}))
		if err != nil {
			return nil, err
		}
		return resp.Msg.Selection.GetProviderPolicy(), nil
	}
	software := []auth.Credential{auth.CredentialAccessToken, auth.CredentialOAuthClient, auth.CredentialUnknown}

	for _, c := range software {
		for _, f := range []reliantv1.ProviderPolicy_Funding{reliantv1.ProviderPolicy_FUNDING_PERSONAL_FIRST, reliantv1.ProviderPolicy_FUNDING_ORG_ONLY} {
			_, err := update(as(c), &reliantv1.ProviderPolicy{Funding: f})
			require.Error(t, err, "%s setting %s", c, f)
			assert.True(t, svcerr.IsPermissionDenied(err), "%s setting %s: %v", c, f, err)
		}
	}

	got, err := update(as(auth.CredentialSession), &reliantv1.ProviderPolicy{Funding: reliantv1.ProviderPolicy_FUNDING_ORG_ONLY})
	require.NoError(t, err, "a person sets it")
	assert.Equal(t, reliantv1.ProviderPolicy_FUNDING_ORG_ONLY, got.GetFunding())

	for _, c := range software {
		// Clearing a person's restrictive choice is refused too.
		_, err := update(as(c), &reliantv1.ProviderPolicy{Funding: reliantv1.ProviderPolicy_FUNDING_PERSONAL_FIRST})
		assert.True(t, svcerr.IsPermissionDenied(err), "%s: %v", c, err)
		// Another field changes; the unspecified funding keeps the person's order.
		got, err := update(as(c), &reliantv1.ProviderPolicy{OnLimit: reliantv1.ProviderPolicy_ON_LIMIT_WAIT})
		require.NoError(t, err, "%s changes on_limit", c)
		assert.Equal(t, reliantv1.ProviderPolicy_ON_LIMIT_WAIT, got.GetOnLimit())
		assert.Equal(t, reliantv1.ProviderPolicy_FUNDING_ORG_ONLY, got.GetFunding(), "%s kept the person's funding", c)
	}

	got, err = update(as(auth.CredentialSession), &reliantv1.ProviderPolicy{})
	require.NoError(t, err)
	assert.Equal(t, reliantv1.ProviderPolicy_FUNDING_UNSPECIFIED, got.GetFunding(), "a person clears it")
}
