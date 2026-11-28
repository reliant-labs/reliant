// Copyright (c) 2025 Reliant Labs

package ghdelegated

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// A token control-plane could not mint for GitHub's rate limit is a
// rate-limited call: its cause is a httpaction.RateLimitedError carrying
// control-plane's Retry-After, so a workflow step waits it out. The message,
// the code and the class are what they were: control-plane's words, a
// transient refusal, the user's limit and never our outage.
func TestRateLimitedMintWaitsForControlPlanesRetryAfter(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err       error
		wait      time.Duration
		waitKnown bool
		message   string
	}{
		"with Retry-After": {
			err:  &gitcredentialclient.RateLimitedError{Message: "GitHub's API rate limit for your GitHub account is exhausted until 2026-11-18T15:00:00Z; try again then", RetryAfter: 7 * time.Minute},
			wait: 7 * time.Minute, waitKnown: true, message: "until 2026-11-18T15:00:00Z",
		},
		"without one":       {err: &gitcredentialclient.RateLimitedError{}, message: "rate limit is exhausted"},
		"the bare sentinel": {err: gitcredentialclient.ErrRateLimited, message: "rate limit is exhausted"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ce := rateLimitedCredential(tc.err)
			assert.Equal(t, httpaction.CodeUnavailable, ce.Code)
			assert.Contains(t, ce.Message, tc.message)
			assert.ErrorIs(t, ce, gitcredentialclient.ErrRateLimited, "control-plane's error stays the cause")
			assert.Equal(t, svcerr.ClassUser, svcerr.Classify(ce), "a spent GitHub limit is the user's, not our outage")

			var limited *httpaction.RateLimitedError
			require.ErrorAs(t, ce, &limited)
			assert.Equal(t, httpaction.RateLimitTokenMint, limited.Reason)
			assert.Equal(t, tc.wait, limited.Wait)
			assert.Equal(t, tc.waitKnown, limited.WaitKnown)
			delay, retry := limited.ForAttempt(1).RetryAfter()
			assert.True(t, retry)
			if tc.waitKnown {
				assert.GreaterOrEqual(t, delay, tc.wait)
				assert.LessOrEqual(t, delay, tc.wait+tc.wait/10)
			}
		})
	}
}

// The owner's own token is rate limited the same way when control-plane says
// so; any other failure to fetch it is still the generic outage.
func TestOwnerTokenRateLimitIsARateLimitedCall(t *testing.T) {
	t.Parallel()
	b := newBroker(&fakeCP{err: &gitcredentialclient.RateLimitedError{Message: "exhausted until later", RetryAfter: time.Minute}}, APIHost, nil)
	_, err := b.Credential(context.Background(), "idp|alice")
	var limited *httpaction.RateLimitedError
	require.ErrorAs(t, err, &limited)
	assert.Equal(t, time.Minute, limited.Wait)

	b = newBroker(&fakeCP{err: errors.New("dial tcp: connection refused")}, APIHost, nil)
	_, err = b.Credential(context.Background(), "idp|alice")
	var ce *httpaction.CredentialError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, httpaction.CodeUnavailable, ce.Code)
	assert.False(t, errors.As(err, &limited), "an outage is not a rate limit")
}
