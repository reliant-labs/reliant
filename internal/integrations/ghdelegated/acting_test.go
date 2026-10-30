// Copyright (c) 2025 Reliant Labs

package ghdelegated

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
)

// fakeInstallations is control-plane's MintInstallationToken: it records what
// it was asked and answers with a scripted token or error.
type fakeInstallations struct {
	mu    sync.Mutex
	asked []gitcredentialclient.InstallationRequest
	err   error
}

func (f *fakeInstallations) MintInstallationToken(_ context.Context, req gitcredentialclient.InstallationRequest) (gitcredentialclient.InstallationToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, req)
	if f.err != nil {
		return gitcredentialclient.InstallationToken{}, f.err
	}
	exp := time.Now().Add(time.Hour)
	return gitcredentialclient.InstallationToken{
		Token: gitcredentialclient.NewToken("ghs_bot_token_5d8e", &exp), ExpiresAt: exp,
		BotLogin: "reliant-labs[bot]", ActingForLogin: "alice", Repository: req.Owner + "/" + req.Repo,
	}, nil
}

// botSource is the hosted composition with the installation source wired.
func (e *env) botSource(host string, inst InstallationSource) connauth.Service {
	brokers := connauth.NewBrokers()
	b := newBroker(e.cp, host, nil)
	b.installations = inst
	require.NoError(e.t, brokers.Register(BrokerID, b))
	return connauth.New(connauth.Deps{Resolver: e.resolver, Broker: brokers.Get})
}

func botReq(t *testing.T, runID, action string, mutates bool, params map[string]any) httpaction.CredentialRequest {
	req := githubReq(t, runID)
	req.ActingIdentity = httpaction.IdentityApp
	req.Automation = "pr-review"
	req.ActionID = action
	req.Mutates = mutates
	req.Params = params
	return req
}

var repoParams = map[string]any{"owner": "acme", "repo": "widgets", "issue_number": 7, "body": "hi"}

// The bot's token is minted for the OWNER, scoped to the ONE repository the call
// names, with only what the action needs.
func TestActingAsApp_MintsAScopedTokenForTheRunOwner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	inst := &fakeInstallations{}
	cred, err := e.botSource(APIHost, inst).Credential(context.Background(),
		botReq(t, e.newRun("idp|alice"), "issue.comment", true, repoParams))
	require.NoError(t, err)

	require.Len(t, inst.asked, 1)
	got := inst.asked[0]
	assert.Equal(t, "idp|alice", got.ExternalUserID, "the owner comes from the run record, by external id")
	assert.Equal(t, "acme", got.Owner)
	assert.Equal(t, "widgets", got.Repo)
	assert.Equal(t, map[string]string{"issues": "write", "pull_requests": "write"}, got.Permissions, "a comment needs issues and (its number may be a pull request) pull_requests, not contents")
	assert.Empty(t, e.cp.asked, "the owner's own token is never fetched for the bot's write")

	assert.Equal(t, "Bearer ghs_bot_token_5d8e", applied(t, cred, "https://api.github.com/repos/acme/widgets/issues/7/comments"))
	assert.Equal(t, BotConnectionID, cred.ConnectionID())
	assert.NotContains(t, fmt.Sprintf("%v %+v %#v", cred, cred, cred), "ghs_bot_token", "never prints the token")
}

func TestActingAsApp_PermissionsFollowTheAction(t *testing.T) {
	testkit.Slow(t, "DB-backed acting-as-app runs, one per case")
	t.Parallel()
	for action, want := range map[string]map[string]string{
		"issue.create":            {"issues": "write"},
		"issue.labels.add":        {"issues": "write", "pull_requests": "write"},
		"issue.labels.remove":     {"issues": "write", "pull_requests": "write"},
		"pr.review.create":        {"pull_requests": "write"},
		"pr.review_comment.reply": {"pull_requests": "write"},
		"pr.create":               {"pull_requests": "write", "contents": "read"},
		"check_run.create":        {"checks": "write"},
		"check_run.update":        {"checks": "write"},
		"workflow.dispatch":       nil, // not in the table: control-plane's default cap
	} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			inst := &fakeInstallations{}
			_, err := e.botSource(APIHost, inst).Credential(context.Background(), botReq(t, e.newRun("idp|alice"), action, true, repoParams))
			require.NoError(t, err)
			assert.Equal(t, want, inst.asked[0].Permissions)
		})
	}
}

// A check run is the one write GitHub lets only an App make, and it touches
// nothing but checks: the bot's token for it carries checks and no other area.
// The DB-free half of PermissionsFollowTheAction, so the -short tier pins it.
func TestCheckRunWritesAskForChecksAlone(t *testing.T) {
	for _, action := range []string{"check_run.create", "check_run.update"} {
		assert.Equal(t, map[string]string{"checks": "write"}, permissionsFor(action), action)
	}
	assert.Nil(t, permissionsFor("check_run.list"), "a read is the owner's: never a bot token")
}

// A mint that cannot be done must FAIL the write. It never falls through to the
// owner's token: the activation said the bot makes this write.
func TestActingAsApp_RefusalsNeverFallBackToTheOwner(t *testing.T) {
	testkit.Slow(t, "DB-backed acting-as-app runs, one per case")
	t.Parallel()
	cases := map[string]struct {
		err      error
		wantCode string
		wantText string
	}{
		"owner cannot push": {gitcredentialclient.ErrOwnerCannotPush, httpaction.CodeFailedPrecondition, "owner cannot push to acme/widgets"},
		"app not installed": {gitcredentialclient.ErrAppNotInstalled, httpaction.CodeFailedPrecondition, "not installed on acme/widgets"},
		"app permissions":   {gitcredentialclient.ErrAppPermissions, httpaction.CodeFailedPrecondition, "has not been granted"},
		"not connected":     {gitcredentialclient.ErrNotConnected, httpaction.CodeFailedPrecondition, "Connect GitHub"},
		"needs reconnect":   {gitcredentialclient.ErrNeedsReconnect, httpaction.CodeNeedsReauth, "reconnected"},
		"outage":            {&gitcredentialclient.RPCError{Code: "unavailable", Message: "http://admin-server:8090 down"}, httpaction.CodeUnavailable, "try again"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			inst := &fakeInstallations{err: tc.err}
			cred, err := e.botSource(APIHost, inst).Credential(context.Background(),
				botReq(t, e.newRun("idp|alice"), "issue.comment", true, repoParams))
			require.Error(t, err)
			assert.Nil(t, cred)
			var ce *httpaction.CredentialError
			require.True(t, errors.As(err, &ce), "got %v", err)
			assert.Equal(t, tc.wantCode, ce.Code)
			assert.Contains(t, ce.Message, tc.wantText)
			assert.NotContains(t, ce.Message, "admin-server", "an internal URL never reaches the user")
			assert.Empty(t, e.cp.asked, "the owner's own token was never used as a fallback")
		})
	}
}

// A deployment that cannot mint at all (no App key, no installation source)
// FAILS the write with a precondition error that says what to fix. It never
// degrades to the owner: choosing the bot is a promise about whose name is on
// the write.
func TestActingAsApp_UnavailableIdentityFailsClosed(t *testing.T) {
	t.Parallel()
	t.Run("control-plane holds no key", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		inst := &fakeInstallations{err: gitcredentialclient.ErrAppNotConfigured}
		cred, err := e.botSource(APIHost, inst).Credential(context.Background(),
			botReq(t, e.newRun("idp|alice"), "issue.comment", true, repoParams))
		require.Error(t, err)
		assert.Nil(t, cred)
		var ce *httpaction.CredentialError
		require.True(t, errors.As(err, &ce), "got %v", err)
		assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
		assert.Contains(t, ce.Message, "no GitHub App key")
		assert.Contains(t, ce.Message, "write as its owner", "the error says how to recover")
		assert.Len(t, inst.asked, 1, "the mint was attempted")
		assert.Empty(t, e.cp.asked, "the owner's own token was never looked up")
	})
	t.Run("no installation source wired (self-hosted)", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		cred, err := e.botSource(APIHost, nil).Credential(context.Background(),
			botReq(t, e.newRun("idp|alice"), "issue.comment", true, repoParams))
		require.Error(t, err)
		assert.Nil(t, cred)
		var ce *httpaction.CredentialError
		require.True(t, errors.As(err, &ce), "got %v", err)
		assert.Equal(t, httpaction.CodeFailedPrecondition, ce.Code)
		assert.Contains(t, ce.Message, "no GitHub App")
		assert.Empty(t, e.cp.asked, "the owner's own token was never looked up")
	})
}

// A broker with no second identity cannot speak as the bot, and an integration
// with no delegated broker at all has none either. Both fail the write; neither
// reaches the owner's saved connection or delegated token.
func TestActingAsApp_UnsupportedBrokerFailsClosed(t *testing.T) {
	t.Parallel()
	t.Run("a plain DelegatedBroker", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		brokers := connauth.NewBrokers()
		require.NoError(t, brokers.Register(BrokerID, plainBroker{b: newBroker(e.cp, APIHost, nil)}))
		src := connauth.New(connauth.Deps{Resolver: e.resolver, Broker: brokers.Get})
		cred, err := src.Credential(context.Background(), botReq(t, e.newRun("idp|alice"), "issue.comment", true, repoParams))
		requireBotRefusal(t, cred, err, httpaction.CodeFailedPrecondition)
		assert.Empty(t, e.cp.asked, "the owner's own token was never looked up")
	})
	t.Run("no broker registered", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		src := connauth.New(connauth.Deps{Resolver: e.resolver})
		cred, err := src.Credential(context.Background(), botReq(t, e.newRun("idp|alice"), "issue.comment", true, repoParams))
		requireBotRefusal(t, cred, err, httpaction.CodeFailedPrecondition)
		assert.Empty(t, e.cp.asked)
	})
}

// githubPAT saves a GitHub personal access token for userID: a saved connection
// that is the owner's own credential.
func (e *env) githubPAT(userID string) string {
	e.t.Helper()
	conn, err := e.svc.CreateAPIKey(context.Background(), connections.CreateAPIKeyParams{
		UserID: userID, IntegrationID: IntegrationID, Name: "pat", Kind: connections.APIKeyKindAPIKey,
		Fields: map[string]string{"api_key": "ghp_owner_pat_canary_1a2b"},
	})
	require.NoError(e.t, err)
	return conn.ID
}

// plainBroker hides ActingCredential: the broker interface and nothing more.
type plainBroker struct{ b *broker }

func (p plainBroker) Credential(ctx context.Context, owner string) (httpaction.Credential, error) {
	return p.b.Credential(ctx, owner)
}

func requireBotRefusal(t *testing.T, cred httpaction.Credential, err error, wantCode string) {
	t.Helper()
	require.Error(t, err)
	assert.Nil(t, cred)
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce), "got %v", err)
	assert.Equal(t, wantCode, ce.Code)
}

// A bot token is for writes. A read authors nothing and keeps the owner's own
// access, including a connection the read names. A WRITE that asked for the bot
// and also names a saved connection is a contradiction: it fails, and neither
// the connection nor the owner's delegated token is used, nor is a bot token
// minted for a call that cannot be made as the bot.
func TestActingAsApp_AppliesToWritesOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	inst := &fakeInstallations{}
	src := e.botSource(APIHost, inst)

	read, err := src.Credential(context.Background(), botReq(t, e.newRun("idp|alice"), "issue.get", false, repoParams))
	require.NoError(t, err)
	assert.Equal(t, ConnectionID, read.ConnectionID(), "a read uses the owner's access")

	pat := e.githubPAT("idp|alice")
	namedRead := botReq(t, e.newRun("idp|alice"), "issue.get", false, repoParams)
	namedRead.ConnectionID = pat
	got, err := src.Credential(context.Background(), namedRead)
	require.NoError(t, err, "a read may name its own connection")
	assert.Equal(t, pat, got.ConnectionID())
	assert.Empty(t, inst.asked, "nothing was minted for either read")
}

func TestActingAsApp_WriteNamingAConnectionFailsClosed(t *testing.T) {
	t.Parallel()
	for name, connection := range map[string]func(*env) string{
		"an unknown connection":        func(*env) string { return "conn_pat" },
		"the owner's saved github PAT": func(e *env) string { return e.githubPAT("idp|alice") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			inst := &fakeInstallations{}
			named := botReq(t, e.newRun("idp|alice"), "issue.comment", true, repoParams)
			named.ConnectionID = connection(e)
			cred, err := e.botSource(APIHost, inst).Credential(context.Background(), named)
			requireBotRefusal(t, cred, err, httpaction.CodeFailedPrecondition)
			assert.Contains(t, err.Error(), "cannot name a saved connection")
			assert.Empty(t, inst.asked, "no bot token was minted for a contradictory request")
			assert.Empty(t, e.cp.asked, "the owner's delegated token was never looked up")
		})
	}
}

// A request that carries an identity the source does not know is refused, never
// treated as the owner.
func TestActingAsApp_UnknownIdentityOnAWriteFailsClosed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	req := botReq(t, e.newRun("idp|alice"), "issue.comment", true, repoParams)
	req.ActingIdentity = "admin"
	cred, err := e.botSource(APIHost, &fakeInstallations{}).Credential(context.Background(), req)
	requireBotRefusal(t, cred, err, httpaction.CodeInvalidArgument)
	assert.Empty(t, e.cp.asked)
}

// A run with no owner record cannot be acted for; the owner lookup failing is
// not an invitation to try another credential.
func TestActingAsApp_UnresolvableOwnerFailsClosed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	inst := &fakeInstallations{}
	cred, err := e.botSource(APIHost, inst).Credential(context.Background(),
		botReq(t, "no-such-run", "issue.comment", true, repoParams))
	require.Error(t, err)
	assert.Nil(t, cred)
	assert.Empty(t, inst.asked)
	assert.Empty(t, e.cp.asked)
}

// A push is a write too: it asks for contents only, and nothing for the owner.
func TestActingAsApp_GitPushMintsContentsWrite(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	inst := &fakeInstallations{}
	_, err := e.botSource(APIHost, inst).Credential(context.Background(),
		botReq(t, e.newRun("idp|alice"), "git.push", true, map[string]any{"owner": "acme", "repo": "widgets"}))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"contents": "write"}, inst.asked[0].Permissions)
	assert.Empty(t, e.cp.asked)
}

// A call that names no repository has nothing to scope a bot token to. It is
// refused, never widened to "every repository the App reaches".
func TestActingAsApp_RequiresARepository(t *testing.T) {
	testkit.Slow(t, "DB-backed acting-as-app runs, one per case")
	t.Parallel()
	for name, params := range map[string]map[string]any{
		"none":       nil,
		"no repo":    {"owner": "acme"},
		"no owner":   {"repo": "widgets"},
		"blank":      {"owner": " ", "repo": " "},
		"wrong type": {"owner": 1, "repo": 2},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			inst := &fakeInstallations{}
			_, err := e.botSource(APIHost, inst).Credential(context.Background(), botReq(t, e.newRun("idp|alice"), "issue.comment", true, params))
			var ce *httpaction.CredentialError
			require.True(t, errors.As(err, &ce), "got %v", err)
			assert.Equal(t, httpaction.CodeInvalidArgument, ce.Code)
			assert.Empty(t, inst.asked)
		})
	}
}

func TestActingAsApp_UnknownIdentityIsRefusedByTheBroker(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := newBroker(e.cp, APIHost, nil)
	b.installations = &fakeInstallations{}
	req := botReq(t, e.newRun("idp|alice"), "issue.comment", true, repoParams)
	req.ActingIdentity = "admin"
	_, err := b.ActingCredential(context.Background(), "idp|alice", req)
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeInvalidArgument, ce.Code)
}

func TestFooter(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "<sub>Posted by Reliant for pr-review, an automation of @alice.</sub>", Footer("pr-review", "alice"))
	assert.Equal(t, "<sub>Posted by Reliant for pr-review.</sub>", Footer("pr-review", ""))
	assert.Equal(t, "<sub>Posted by Reliant for an automation, an automation of @alice.</sub>", Footer(" ", "alice"))

	// The trigger's name is the owner's text in a PUBLIC comment: it cannot
	// inject markup or ping anyone.
	hostile := Footer("<img src=x>@everyone\nsecond line", "alice")
	assert.NotContains(t, hostile, "<img")
	assert.NotContains(t, hostile, "\n")
	assert.NotContains(t, hostile, "@everyone")
}

// END TO END through the real runner and the real catalog: the bot's bearer
// reaches the host, the body carries the footer, and the token is scrubbed.
func TestActingAsApp_EndToEndWriteCarriesTheBotTokenAndTheFooter(t *testing.T) {
	testkit.Slow(t, "DB-backed acting-as-app runs, one per case")
	t.Parallel()
	cases := []struct {
		name   string
		action string
		params map[string]any
		field  string
		want   string // substring the field must end up containing
		exact  string // when set, the field must equal this
	}{
		{"comment", "issue.comment", map[string]any{"owner": "acme", "repo": "widgets", "issue_number": 7, "body": "Looks good."}, "body", "Looks good.\n\n<sub>Posted by Reliant for pr-review, an automation of @alice.</sub>", ""},
		{"review", "pr.review.create", map[string]any{"owner": "acme", "repo": "widgets", "pull_number": 7, "event": "COMMENT", "body": "One nit."}, "body", "One nit.\n\n<sub>", ""},
		{"pr with no description", "pr.create", map[string]any{"owner": "acme", "repo": "widgets", "head": "fix", "base": "main", "title": "Fix"}, "body", "", "<sub>Posted by Reliant for pr-review, an automation of @alice.</sub>"},
		{"inline reply", "pr.review_comment.reply", map[string]any{"owner": "acme", "repo": "widgets", "pull_number": 7, "comment_id": 9, "body": "Fixed."}, "body", "Fixed.\n\n<sub>", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			inst := &fakeInstallations{}
			var gotAuth string
			var gotBody map[string]any
			srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = fmt.Fprintf(w, `{"id":1,"number":1,"html_url":"https://github.com/acme/widgets/x/1","title":"t","state":"open","body":%q}`, gotAuth)
			}))
			u, _ := url.Parse(srv.URL)
			src := e.botSource(u.Host, inst)

			cat := githubCatalogAt(t, srv.URL)
			resolved, err := cat.Resolve("github/" + tc.action + "@1")
			require.NoError(t, err)
			pool := x509.NewCertPool()
			pool.AddCert(srv.Certificate())
			g := netguardtest.Guard()
			runner := httpaction.NewRunner(g).WithRootCAs(pool)

			site := httpaction.CallSite{RunID: e.newRun("idp|alice"), ActingIdentity: httpaction.IdentityApp, Automation: "pr-review"}
			res, err := runner.RunAuthenticated(context.Background(), resolved.Manifest, resolved.Spec, tc.params, src, site)
			require.NoError(t, err)

			assert.Equal(t, "Bearer ghs_bot_token_5d8e", gotAuth, "the bot's installation token reached GitHub")
			assert.Equal(t, BotConnectionID, res.ConnectionID)
			assert.NotContains(t, res.Content, "ghs_bot_token_5d8e", "scrubbed from the result")
			assert.Empty(t, e.cp.asked, "the owner's token was never involved")
			require.Contains(t, gotBody, tc.field)
			if tc.exact != "" {
				assert.Equal(t, tc.exact, gotBody[tc.field])
			} else {
				assert.Contains(t, gotBody[tc.field], tc.want)
			}
		})
	}
}

// What a bot does NOT author carries no footer: a label, a state change with no
// new text, an update that leaves the body alone.
func TestActingAsApp_ActionsThatAuthorNothingAreNotSigned(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	inst := &fakeInstallations{}
	var gotBody map[string]any
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = nil
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			_, _ = w.Write([]byte(`{"number":7,"id":1,"title":"t","state":"closed","html_url":"https://github.com/acme/widgets/issues/7"}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	u, _ := url.Parse(srv.URL)
	src := e.botSource(u.Host, inst)
	cat := githubCatalogAt(t, srv.URL)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	g := netguardtest.Guard()
	runner := httpaction.NewRunner(g).WithRootCAs(pool)
	site := httpaction.CallSite{RunID: e.newRun("idp|alice"), ActingIdentity: httpaction.IdentityApp, Automation: "pr-review"}

	label, err := cat.Resolve("github/issue.labels.add@1")
	require.NoError(t, err)
	_, err = runner.RunAuthenticated(context.Background(), label.Manifest, label.Spec,
		map[string]any{"owner": "acme", "repo": "widgets", "issue_number": 7, "label": "reviewed"}, src, site)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"labels": []any{"reviewed"}}, gotBody, "a label change is not text anyone wrote")

	closeIssue, err := cat.Resolve("github/issue.update@1")
	require.NoError(t, err)
	_, err = runner.RunAuthenticated(context.Background(), closeIssue.Manifest, closeIssue.Spec,
		map[string]any{"owner": "acme", "repo": "widgets", "issue_number": 7, "state": "closed"}, src, site)
	require.NoError(t, err)
	assert.NotContains(t, gotBody, "body", "an update that omits the body leaves it unchanged, not replaced by a footer")
}

// An owner run is untouched: no identity, no footer, the owner's own token.
func TestOwnerIdentityIsUnchanged(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	inst := &fakeInstallations{}
	req := githubReq(t, e.newRun("idp|alice"))
	req.ActionID, req.Mutates, req.Params = "issue.comment", true, repoParams
	cred, err := e.botSource(APIHost, inst).Credential(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, ConnectionID, cred.ConnectionID())
	assert.Empty(t, inst.asked)
	_, signs := cred.(httpaction.Attributing)
	assert.False(t, signs, "the owner's credential never signs")
}
