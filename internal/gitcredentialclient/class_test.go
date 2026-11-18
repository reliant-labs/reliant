package gitcredentialclient_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
)

// control-plane#2964: GitHub's rate limit on the user's token is
// resource_exhausted with reason github_rate_limited and a Retry-After header.
// It is a typed, user-class outcome that says when to retry.
func TestMintInstallationToken_RateLimitedSaysWhenToRetry(t *testing.T) {
	c := newMintClient(t, &fakeMint{respond: func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "90")
		connectError(http.StatusTooManyRequests, "resource_exhausted", "github_rate_limited",
			"GitHub's API rate limit for your GitHub account is exhausted until 2026-11-18T15:00:00Z; try again then")(w)
	}})
	_, err := c.MintInstallationToken(context.Background(), gitcredentialclient.InstallationRequest{ExternalUserID: "u", Owner: "o", Repo: "r"})
	require.ErrorIs(t, err, gitcredentialclient.ErrRateLimited)
	var limited *gitcredentialclient.RateLimitedError
	require.True(t, errors.As(err, &limited), "got %v", err)
	assert.Equal(t, 90*time.Second, limited.RetryAfter)
	assert.Contains(t, limited.Message, "until 2026-11-18T15:00:00Z")
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err))
	var rpc *gitcredentialclient.RPCError
	assert.False(t, errors.As(err, &rpc), "a typed outcome is not a generic RPCError")
}

// A reason this client has no typed outcome for (a newer control plane)
// stays an RPCError, as before, carrying the reason; only its log grade
// follows who must act.
func TestMintInstallationToken_UnknownReasonIsAUserRefusal(t *testing.T) {
	c := newMintClient(t, &fakeMint{respond: connectError(http.StatusForbidden, "permission_denied", "github_repo_not_visible", "your GitHub account cannot see o/r")})
	_, err := c.MintInstallationToken(context.Background(), gitcredentialclient.InstallationRequest{ExternalUserID: "u", Owner: "o", Repo: "r"})
	var rpc *gitcredentialclient.RPCError
	require.True(t, errors.As(err, &rpc), "got %v", err)
	assert.Equal(t, "github_repo_not_visible", rpc.Reason)
	assert.Equal(t, "your GitHub account cannot see o/r", rpc.Message)
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err))
}

func TestRPCError_ClassFollowsWhoMustAct(t *testing.T) {
	for _, tc := range []struct {
		err  *gitcredentialclient.RPCError
		want svcerr.Class
	}{
		{&gitcredentialclient.RPCError{Code: "failed_precondition"}, svcerr.ClassUser},
		{&gitcredentialclient.RPCError{Code: "not_found"}, svcerr.ClassUser},
		// control-plane's internal-service door refuses OUR bearer as
		// permission_denied with no reason: ours to fix.
		{&gitcredentialclient.RPCError{Code: "permission_denied"}, svcerr.ClassServer},
		{&gitcredentialclient.RPCError{Code: "permission_denied", Reason: "x"}, svcerr.ClassUser},
		{&gitcredentialclient.RPCError{Code: "unauthenticated"}, svcerr.ClassServer},
		{&gitcredentialclient.RPCError{Code: "invalid_argument"}, svcerr.ClassServer},
		{&gitcredentialclient.RPCError{Code: "unavailable"}, svcerr.ClassServer},
		{&gitcredentialclient.RPCError{Code: "unavailable", Reason: "x"}, svcerr.ClassServer},
		{&gitcredentialclient.RPCError{Code: "Forbidden"}, svcerr.ClassServer},
		{&gitcredentialclient.RPCError{Code: "canceled"}, svcerr.ClassCanceled},
	} {
		assert.Equal(t, tc.want, svcerr.Classify(tc.err), "%+v", tc.err)
	}
}

// staleOnSecond answers the first request and drops the keep-alive connection
// under the second without a response: a pooled connection the peer closed.
// It reads the whole second request first, so the client sees the drop on its
// read, never mid-write (a write error is never retried, which made the
// retry case racy), and the server has provably received the request once.
func staleOnSecond(t *testing.T, answer string) (gitcredentialclient.Service, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 2 {
			_, _ = io.Copy(io.Discard, r.Body)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return gitcredentialclient.New(gitcredentialclient.Deps{
		ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL},
		Sign:         func() (string, error) { return auth.SignInternalServiceToken(secret) },
	}), &n
}

// A read is retried once on a fresh connection when its pooled one is dead.
func TestListOrgGitHubInstallations_RetriesADeadKeepAliveConnection(t *testing.T) {
	c, n := staleOnSecond(t, `{"installations":[]}`)
	_, err := c.ListOrgGitHubInstallations(context.Background(), "org-1")
	require.NoError(t, err)
	_, err = c.ListOrgGitHubInstallations(context.Background(), "org-1")
	require.NoError(t, err, "the dead pooled connection was not retried")
	assert.EqualValues(t, 3, n.Load())
}

// A mint is never replayed: the first delivery may have minted.
func TestMintInstallationToken_IsNotRetriedOnADeadConnection(t *testing.T) {
	c, n := staleOnSecond(t, `{"installations":[]}`)
	_, err := c.ListOrgGitHubInstallations(context.Background(), "org-1")
	require.NoError(t, err)
	_, err = c.MintInstallationToken(context.Background(), gitcredentialclient.InstallationRequest{ExternalUserID: "u", Owner: "o", Repo: "r"})
	require.Error(t, err)
	assert.EqualValues(t, 2, n.Load())
}

// A user-token read is never replayed: control-plane may have renewed the
// credential on the first delivery, rotating its single-use refresh token.
func TestUserAccessToken_IsNotRetriedOnADeadConnection(t *testing.T) {
	c, n := staleOnSecond(t, `{"accessToken":"ghu_x"}`)
	_, err := c.UserAccessToken(context.Background(), "idp|alice", "github")
	require.NoError(t, err)
	_, err = c.UserAccessToken(context.Background(), "idp|alice", "github")
	require.Error(t, err, "the dropped request was replayed")
	assert.EqualValues(t, 2, n.Load(), "control-plane must see the dropped request exactly once")
}
