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
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard"
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
	assert.Equal(t, map[string]string{"issues": "write"}, got.Permissions, "a comment needs issues, not contents")
	assert.Empty(t, e.cp.asked, "the owner's own token is never fetched for the bot's write")

	assert.Equal(t, "Bearer ghs_bot_token_5d8e", applied(t, cred, "https://api.github.com/repos/acme/widgets/issues/7/comments"))
	assert.Equal(t, BotConnectionID, cred.ConnectionID())
	assert.NotContains(t, fmt.Sprintf("%v %+v %#v", cred, cred, cred), "ghs_bot_token", "never prints the token")
}

func TestActingAsApp_PermissionsFollowTheAction(t *testing.T) {
	for action, want := range map[string]map[string]string{
		"issue.create":            {"issues": "write"},
		"issue.labels.add":        {"issues": "write"},
		"pr.review.create":        {"pull_requests": "write"},
		"pr.review_comment.reply": {"pull_requests": "write"},
		"pr.create":               {"pull_requests": "write", "contents": "read"},
		"workflow.dispatch":       nil, // not in the table: control-plane's default cap
	} {
		t.Run(action, func(t *testing.T) {
			e := newEnv(t)
			inst := &fakeInstallations{}
			_, err := e.botSource(APIHost, inst).Credential(context.Background(), botReq(t, e.newRun("idp|alice"), action, true, repoParams))
			require.NoError(t, err)
			assert.Equal(t, want, inst.asked[0].Permissions)
		})
	}
}

// A mint that cannot be done must FAIL the write. It never falls through to the
// owner's token: the activation said the bot makes this write.
func TestActingAsApp_RefusalsNeverFallBackToTheOwner(t *testing.T) {
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

// The one fallback: a deployment that cannot mint at all (no App key) keeps its
// pre-existing behaviour and acts as the owner, with a log line.
func TestActingAsApp_NoAppKeyDegradesToTheOwner(t *testing.T) {
	t.Run("control-plane holds no key", func(t *testing.T) {
		e := newEnv(t)
		inst := &fakeInstallations{err: gitcredentialclient.ErrAppNotConfigured}
		cred, err := e.botSource(APIHost, inst).Credential(context.Background(),
			botReq(t, e.newRun("idp|alice"), "issue.comment", true, repoParams))
		require.NoError(t, err, "a dev or e2e stack with no key must keep working")
		assert.Equal(t, "Bearer ghu_alice_token_9f3c1e", applied(t, cred, "https://api.github.com/repos/acme/widgets/issues/7/comments"))
		assert.Equal(t, ConnectionID, cred.ConnectionID(), "it is the owner's credential, and says so")
	})
	t.Run("no installation source wired (self-hosted)", func(t *testing.T) {
		e := newEnv(t)
		cred, err := e.botSource(APIHost, nil).Credential(context.Background(),
			botReq(t, e.newRun("idp|alice"), "issue.comment", true, repoParams))
		require.NoError(t, err)
		assert.Equal(t, ConnectionID, cred.ConnectionID())
	})
}

// A bot token is for writes. A read authors nothing, and a call that names its
// own connection chose its credential.
func TestActingAsApp_AppliesToWritesOnly(t *testing.T) {
	e := newEnv(t)
	inst := &fakeInstallations{}
	src := e.botSource(APIHost, inst)

	read, err := src.Credential(context.Background(), botReq(t, e.newRun("idp|alice"), "issue.get", false, repoParams))
	require.NoError(t, err)
	assert.Equal(t, ConnectionID, read.ConnectionID(), "a read uses the owner's access")

	named := botReq(t, e.newRun("idp|alice"), "issue.comment", true, repoParams)
	named.ConnectionID = "conn_pat"
	_, err = src.Credential(context.Background(), named)
	require.Error(t, err, "an unknown named connection is an error, not the bot")
	assert.Empty(t, inst.asked, "nothing was minted for either")
}

// A call that names no repository has nothing to scope a bot token to. It is
// refused, never widened to "every repository the App reaches".
func TestActingAsApp_RequiresARepository(t *testing.T) {
	for name, params := range map[string]map[string]any{
		"none":       nil,
		"no repo":    {"owner": "acme"},
		"no owner":   {"repo": "widgets"},
		"blank":      {"owner": " ", "repo": " "},
		"wrong type": {"owner": 1, "repo": 2},
	} {
		t.Run(name, func(t *testing.T) {
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
			e := newEnv(t)
			inst := &fakeInstallations{}
			var gotAuth string
			var gotBody map[string]any
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = fmt.Fprintf(w, `{"id":1,"number":1,"html_url":"https://github.com/acme/widgets/x/1","title":"t","state":"open","body":%q}`, gotAuth)
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			src := e.botSource(u.Host, inst)

			cat := githubCatalogAt(t, srv.URL)
			resolved, err := cat.Resolve("github/" + tc.action + "@1")
			require.NoError(t, err)
			pool := x509.NewCertPool()
			pool.AddCert(srv.Certificate())
			g := netguard.New()
			g.AllowLoopback = true
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
	e := newEnv(t)
	inst := &fakeInstallations{}
	var gotBody map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	src := e.botSource(u.Host, inst)
	cat := githubCatalogAt(t, srv.URL)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	g := netguard.New()
	g.AllowLoopback = true
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
