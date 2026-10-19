// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

type markerFake struct {
	store  *routerFake
	marked []string
	err    error
}

// MarkNeedsSignIn mirrors the real store: the account stops being offered.
func (m *markerFake) MarkNeedsSignIn(_ context.Context, _, accountID, _ string) error {
	if m.err != nil {
		return m.err
	}
	m.marked = append(m.marked, accountID)
	return nil
}

func rejectedKey() error {
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	return fmt.Errorf("stream llm response: %w", &anthropic.Error{StatusCode: 401, Request: req, Response: &http.Response{StatusCode: 401}})
}

func apiKeyAccount(id, email string, priority int) models.AccountConfig {
	a := claudeAccount(id, email, priority)
	a.DriverID = "anthropic"
	return a
}

func resolveOnAnthropic(t *testing.T, env *routingEnv, thread string) *resolvedLLMCall {
	t.Helper()
	res, err := resolveLLMCall(context.Background(), nil, llmCallSpec{
		UserID: "u1", SessionID: "s", ThreadID: thread,
		Selector: models.ModelSelector{ID: routeModel + "@anthropic"}, Routing: env.routing,
	})
	require.NoError(t, err)
	return res
}

// A revoked API key leaves rotation and the turn moves to the next account.
func TestCredentialRejection_MovesToTheNextAccount(t *testing.T) {
	a, b := apiKeyAccount("a", "alice@acme.com", 0), apiKeyAccount("b", "bob@acme.com", 1)
	withAvailableDrivers(t, driversWith(a, b))
	env := newRoutingEnv(t)
	marker := &markerFake{store: env.store}
	env.routing.Credentials = marker

	first := resolveOnAnthropic(t, env, "t")
	require.Equal(t, "a", first.Account.Config.AccountID)

	got := env.routing.credentialRejection(context.Background(), rejectedKey(), usageLimitContext{
		UserID: "u1", ThreadID: "t", Driver: "anthropic", ModelID: routeModel, Account: first.Account})
	var failure *types.UsageLimitFailure
	require.ErrorAs(t, got, &failure, "reported as a failover so the workflow retries at once")
	assert.Equal(t, []string{"a"}, marker.marked)
	assert.True(t, failure.Facts.AlternativeAccount)
	assert.Equal(t, "a", failure.Facts.AccountID)

	// What the real store does with the mark: the rejected account is no longer offered.
	withAvailableDrivers(t, driversWith(b))
	next := resolveOnAnthropic(t, env, "t")
	assert.Equal(t, "b", next.Account.Config.AccountID)
	assert.Equal(t, "a", next.Account.Change.Previous.AccountID)
}

// With no account left, the chat's opted-in fallback model takes over; with
// neither, the caller's normal path runs (resolution then says sign in again).
func TestCredentialRejection_NoAccountLeft(t *testing.T) {
	codex := models.AccountConfig{
		DriverConfig: models.DriverConfig{DriverID: "codex", AccountID: "cx", AccountEmail: "c@x.com", Enabled: true, APIKey: "k"},
		Status:       models.AccountAvailable,
	}
	withAvailableDrivers(t, driversWith(apiKeyAccount("a", "alice@acme.com", 0), codex))
	env := newRoutingEnv(t)
	marker := &markerFake{store: env.store}
	env.routing.Credentials = marker
	first := resolveOnAnthropic(t, env, "t")
	uc := usageLimitContext{UserID: "u1", ThreadID: "t", Driver: "anthropic", ModelID: routeModel, Account: first.Account}

	t.Run("fallback opted in", func(t *testing.T) {
		uc := uc
		uc.Policy = types.FailoverPolicy{FallbackModel: routeFallback}
		var failure *types.UsageLimitFailure
		require.ErrorAs(t, env.routing.credentialRejection(context.Background(), rejectedKey(), uc), &failure)
		assert.False(t, failure.Facts.AlternativeAccount)
		assert.Equal(t, routeFallback, failure.Facts.FallbackModel)
	})
	t.Run("nothing else can serve", func(t *testing.T) {
		assert.NoError(t, env.routing.credentialRejection(context.Background(), rejectedKey(), uc))
		assert.Contains(t, marker.marked, "a", "the dead key is out of rotation regardless")
	})
}

func TestCredentialRejection_LeavesOtherFailuresAlone(t *testing.T) {
	withAvailableDrivers(t, driversWith(apiKeyAccount("a", "alice@acme.com", 0), apiKeyAccount("b", "bob@acme.com", 1)))
	env := newRoutingEnv(t)
	marker := &markerFake{store: env.store}
	env.routing.Credentials = marker
	first := resolveOnAnthropic(t, env, "t")
	uc := usageLimitContext{UserID: "u1", ThreadID: "t", Driver: "anthropic", ModelID: routeModel, Account: first.Account}
	ctx := context.Background()

	assert.NoError(t, env.routing.credentialRejection(ctx, errors.New("dial tcp: connection refused"), uc), "not a credential verdict")

	oauth := uc
	acct := *first.Account
	acct.Config.RefreshToken = "refresh"
	oauth.Account = &acct
	assert.NoError(t, env.routing.credentialRejection(ctx, rejectedKey(), oauth), "OAuth sessions belong to the refresh coordinator")

	noAccount := uc
	noAccount.Account = nil
	assert.NoError(t, env.routing.credentialRejection(ctx, rejectedKey(), noAccount))

	var none *AccountRouting
	assert.NoError(t, none.credentialRejection(ctx, rejectedKey(), uc))

	env.routing.Credentials = &markerFake{err: errors.New("store down")}
	assert.NoError(t, env.routing.credentialRejection(ctx, rejectedKey(), uc), "an account that could not be marked is not reported as handled")

	assert.Empty(t, marker.marked)
}
