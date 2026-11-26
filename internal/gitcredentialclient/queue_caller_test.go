package gitcredentialclient_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
)

const callerSecret = "queue-caller-secret-at-least-32-bytes!!"

// queueMint is control-plane's queue mint as the wire shows it: it checks the
// bearer, then the X-Reliant-Queue-Caller assertion exactly as
// control-plane's VerifyQueueMintCallerAssertion does.
type queueMint struct {
	got       map[string]any
	assertion string
	respond   func(w http.ResponseWriter)
}

func (f *queueMint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.assertion = r.Header.Get("X-Reliant-Queue-Caller")
	_ = json.NewDecoder(r.Body).Decode(&f.got)
	f.respond(w)
}

func newQueueClient(t *testing.T, f *queueMint) gitcredentialclient.Service {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return gitcredentialclient.New(gitcredentialclient.Deps{
		ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL},
		Sign:         func() (string, error) { return auth.SignInternalServiceToken(secret) },
		HTTPClient:   srv.Client(),
	})
}

func TestMintQueueInstallationToken_SendsTheQueueAppAndTheAssertion(t *testing.T) {
	f := &queueMint{respond: func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"token":"ghs_queue","expiresAt":"2099-01-01T00:00:00Z","botLogin":"reliant-queue[bot]","repository":"acme/widgets"}`)
	}}
	c := newQueueClient(t, f)
	assertion, err := gitcredentialclient.SignQueueCallerAssertion(callerSecret, time.Now())
	require.NoError(t, err)
	got, err := c.MintQueueInstallationToken(context.Background(), gitcredentialclient.QueueInstallationRequest{
		Owner: "acme", Repo: "widgets", CallerAssertion: assertion,
	})
	require.NoError(t, err)
	assert.Equal(t, "reliant-queue[bot]", got.BotLogin)
	assert.Equal(t, "GITHUB_APP_QUEUE", f.got["app"], "the enum's Connect JSON name, pinned by control-plane")
	_, hasUser := f.got["userId"]
	_, hasOrg := f.got["orgId"]
	assert.False(t, hasUser || hasOrg, "the queue mints for no user and no org: %v", f.got)

	// The assertion verifies exactly as control-plane checks it.
	var claims jwt.RegisteredClaims
	_, err = jwt.ParseWithClaims(f.assertion, &claims, func(*jwt.Token) (any, error) { return []byte(callerSecret), nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithSubject("reliant-worker"),
		jwt.WithAudience("control-plane/github-queue-mint"), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	require.NoError(t, err)
	assert.LessOrEqual(t, claims.ExpiresAt.Sub(claims.IssuedAt.Time), 10*time.Minute)
}

func TestMintQueueInstallationToken_TypedOutcomes(t *testing.T) {
	for reason, want := range map[string]error{
		"github_queue_app_not_configured": gitcredentialclient.ErrQueueAppNotConfigured,
		"github_queue_app_not_installed":  gitcredentialclient.ErrQueueAppNotInstalled,
		"github_queue_app_permissions":    gitcredentialclient.ErrQueueAppPermissions,
		"github_queue_caller_not_worker":  gitcredentialclient.ErrQueueCallerNotWorker,
	} {
		t.Run(reason, func(t *testing.T) {
			c := newQueueClient(t, &queueMint{respond: connectError(http.StatusBadRequest, "failed_precondition", reason, "msg")})
			_, err := c.MintQueueInstallationToken(context.Background(), gitcredentialclient.QueueInstallationRequest{Owner: "o", Repo: "r", CallerAssertion: "a"})
			assert.ErrorIs(t, err, want)
		})
	}
}

func TestMintQueueInstallationToken_RefusesLocally(t *testing.T) {
	f := &queueMint{respond: func(http.ResponseWriter) { t.Fatal("nothing should reach control-plane") }}
	c := newQueueClient(t, f)
	_, err := c.MintQueueInstallationToken(context.Background(), gitcredentialclient.QueueInstallationRequest{Owner: "o", Repo: "r"})
	assert.Error(t, err, "no assertion")
	_, err = gitcredentialclient.SignQueueCallerAssertion("short", time.Now())
	assert.ErrorIs(t, err, gitcredentialclient.ErrQueueCallerSecret)
}
