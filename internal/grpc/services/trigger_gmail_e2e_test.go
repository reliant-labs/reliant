// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/gmail"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/vault"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
)

// Gmail end to end, with nothing faked but Google and the agent.
//
// Two users connect Gmail through the real OAuth broker against a fake Google
// that serves accounts.google.com, oauth2.googleapis.com and
// gmail.googleapis.com UNDER THOSE NAMES (a certificate for them, a dialer
// that reaches the fake): the shipped manifest is used as is, so the
// credential's own host pin is exercised against the real hostnames. Each user
// creates a gmail/message.received trigger through the real TriggerService
// (which converges a real poll schedule). The real poll workflow then runs the
// real PollTrigger activity: the trigger's credential is resolved from the
// trigger row by the real resolver, the shipped Gmail poller reads history,
// and the real intake records each new message, whose fire launches a run.
//
// Proven: the authorize URL carries access_type=offline and prompt=consent;
// the code exchange and the profile probe make emailAddress the connection's
// identity; the baseline fires nothing; each user's new mail launches a run
// for that user only, carrying trigger.payload.data.subject to CEL (the
// filter reads it); a dead refresh grant lands as needs_reauth and FAILING
// health, with no retry.
func TestGmailPollsIntoLaunchedRunsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a Temporal dev server and a worker; skipped under -short")
	}
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	temporalClient := startTriggerDevServer(t)
	taskQueue := "trigger-gmail-e2e-" + uuid.NewString()
	google := newFakeGoogle(t)

	// --- connections: the real broker, token source and resolver ----------
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(key))
	require.NoError(t, err)
	sealer := vault.New(repo.DB, vault.NewEnvKeyWrapper(ring))
	env := func(k string) string {
		return map[string]string{
			"RELIANT_OAUTH_GMAIL_CLIENT_ID":     "1234-gmail.apps.googleusercontent.com",
			"RELIANT_OAUTH_GMAIL_CLIENT_SECRET": "GOCSPX-fake-secret",
		}[k]
	}
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), env)
	require.NoError(t, err)
	doer := google.client()
	store := repo.Connections()
	tokens := connections.NewTokenSource(store, sealer, providers, doer)
	broker := connections.NewBroker(store, sealer, providers, doer, "https://reliant.example")
	conns := connections.NewService(store, sealer, providers, tokens, broker, doer)
	source := connauth.New(connections.NewResolver(repo, store, tokens))

	connectGmail := func(userID, email string) *core.Connection {
		google.nextEmail = email
		authURL, err := conns.StartOAuth(ctx, connections.StartParams{UserID: userID, IntegrationID: "gmail", ClientOrigin: "https://reliant.example"})
		require.NoError(t, err)
		u, err := url.Parse(authURL)
		require.NoError(t, err)
		q := u.Query()
		assert.Equal(t, "accounts.google.com", u.Host)
		assert.Equal(t, "/o/oauth2/v2/auth", u.Path)
		assert.Equal(t, "offline", q.Get("access_type"), "without it Google issues no refresh token")
		assert.Equal(t, "consent", q.Get("prompt"), "without it a reconnect gets no refresh token")
		assert.Equal(t, "S256", q.Get("code_challenge_method"))
		assert.Equal(t, "code", q.Get("response_type"))
		assert.Equal(t, "https://reliant.example/integrations/oauth/gmail/callback", q.Get("redirect_uri"))
		assert.ElementsMatch(t, []string{
			"https://www.googleapis.com/auth/gmail.send", "https://www.googleapis.com/auth/gmail.readonly",
		}, strings.Fields(q.Get("scope")), "space-joined Google scopes")
		done, err := conns.CompleteOAuth(ctx, userID, q.Get("state"), "4/code-"+email)
		require.NoError(t, err)
		assert.Equal(t, email, *done.Connection.ExternalAccountID, "the profile probe's emailAddress is the identity")
		assert.Equal(t, email, *done.Connection.AccountLabel)
		form := google.lastTokenForm()
		assert.Equal(t, "authorization_code", form.Get("grant_type"))
		assert.NotEmpty(t, form.Get("code_verifier"), "PKCE")
		return done.Connection
	}

	// --- triggers: the real service, a registry carrying the real poller --
	registry := webhook.NewRegistry()
	gm, err := catalog.MustBuiltin().Manifest("gmail", 1)
	require.NoError(t, err)
	poller, err := gmail.NewPoller(gm, google.runner())
	require.NoError(t, err)
	require.NoError(t, registry.RegisterPoller(gmail.ID, poller))
	intake := triggers.NewIntake(repo, temporalClient, taskQueue)
	svc := NewTriggerServiceFor(repo, temporalClient, taskQueue).
		WithPolledIntegrations(registry.IsPolled).
		WithInbound(InboundOptions{PublicURL: "https://reliant.example", Catalog: registry, Intake: intake})

	// --- the worker: poll workflow + activity, event fire, launcher -------
	launcher := launch.NewLauncher(repo, threads.NewService(repo), temporalClient,
		runs.NewService(repo, temporalClient, v2workflow.NewPauseService(temporalClient, repo)), taskQueue)
	w := worker.New(temporalClient, taskQueue, temporaltest.WorkerOptions(worker.Options{}))
	w.RegisterWorkflowWithOptions(triggers.TriggerPollWorkflow, workflow.RegisterOptions{Name: triggers.PollWorkflowName})
	w.RegisterActivityWithOptions(
		triggers.NewTriggerPoller(gmailPollRepo{Repo: repo, conns: store}, registry, intake, source).Poll,
		activity.RegisterOptions{Name: triggers.PollActivityName})
	w.RegisterWorkflowWithOptions(triggers.TriggerEventFireWorkflow, workflow.RegisterOptions{Name: triggers.EventFireWorkflowName})
	w.RegisterActivityWithOptions(triggers.NewEventFirer(repo, launcher).Fire, activity.RegisterOptions{Name: triggers.EventFireActivityName})
	w.RegisterWorkflowWithOptions(func(workflow.Context, any) error { return nil }, workflow.RegisterOptions{Name: v2workflow.WorkflowDynamic})
	require.NoError(t, w.Start())
	defer w.Stop()

	type tenant struct {
		userID  string
		email   string
		conn    *core.Connection
		trigger *reliantv1.Trigger
	}
	newTenant := func(email string) tenant {
		userID := uuid.NewString()
		projectID := uuid.NewString()
		now := time.Now().UTC()
		require.NoError(t, repo.CreateProject(ctx, &db.Project{
			ID: projectID, UserID: userID, Name: "Gmail E2E " + email, Path: t.TempDir(),
			IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		createMainWorktree(t, repo, projectID, now)
		daemonID := uuid.NewString()
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
		conn := connectGmail(userID, email)
		created, err := svc.CreateTrigger(context.WithValue(ctx, auth.UserIDContextKey, userID),
			connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: &reliantv1.TriggerDefinition{
				Name: "triage invoices", ProjectId: projectID, DaemonId: daemonID, Workflow: "builtin://agent",
				Message: "File the invoice.", Params: mockModelTriggerParams(t),
				// CEL over the polled payload: only invoices launch.
				Filter: "trigger.payload.data.subject.contains('Invoice')",
				Source: &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{
					Integration: "gmail", Events: []string{"message.received"},
				}},
			}}))
		require.NoError(t, err)
		require.Equal(t, conn.ID, created.Msg.GetTrigger().GetConnectionId())
		cleanupGmailSchedule(t, temporalClient, created.Msg.GetTrigger().GetId())
		return tenant{userID: userID, email: email, conn: conn, trigger: created.Msg.GetTrigger()}
	}
	ann := newTenant("ann@example.com")
	bob := newTenant("bob@example.org")

	// Creating a polled trigger converged a real poll schedule.
	desc, err := temporalClient.ScheduleClient().GetHandle(ctx, triggers.ScheduleID(ann.trigger.GetId())).Describe(ctx)
	require.NoError(t, err)
	assert.Equal(t, triggers.PollWorkflowName, desc.Schedule.Action.(*client.ScheduleWorkflowAction).Workflow)

	// The schedule's interval is minutes; run its workflow now, as it would.
	poll := func(tn tenant) *triggers.PollOutput {
		t.Helper()
		run, err := temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			ID: "gmail-e2e-poll-" + uuid.NewString(), TaskQueue: taskQueue,
		}, triggers.PollWorkflowName, triggers.PollInput{TriggerID: tn.trigger.GetId()})
		require.NoError(t, err)
		var out triggers.PollOutput
		require.NoError(t, run.Get(ctx, &out))
		return &out
	}

	// Mail that was already there never fires.
	google.deliver("ann@example.com", "a00001", "Old Invoice 0001", "vendor@acme.example")
	base := poll(ann)
	assert.True(t, base.Baseline)
	assert.True(t, poll(bob).Baseline)

	// New mail: an invoice and a newsletter for ann, an invoice for bob.
	google.deliver("ann@example.com", "a00002", "Invoice 1234 from Acme", "Billing <billing@acme.example>")
	google.deliver("ann@example.com", "a00003", "Weekly newsletter", "news@example.net")
	google.deliver("bob@example.org", "b00001", "Invoice 9 for Bob", "billing@other.example")

	out := poll(ann)
	assert.Equal(t, 2, out.Items)
	assert.Equal(t, 2, out.Accepted)
	launched := waitForLaunched(t, repo, ann.userID, ann.trigger.GetId())
	assert.Equal(t, ann.trigger.GetId()+":a00002", launched.DedupeKey, "the message id is the dedupe key")
	assert.Equal(t, "gmail", launched.Payload["integration"])
	assert.Equal(t, "message.received", launched.Payload["event"])
	assert.Equal(t, "ann@example.com", launched.Payload["account"])
	data, _ := launched.Payload["data"].(map[string]any)
	assert.Equal(t, "Invoice 1234 from Acme", data["subject"], "trigger.payload.data.subject reached the run")
	assert.Equal(t, "Billing <billing@acme.example>", data["from"])
	assert.NotContains(t, data, "text", "no body in the payload")
	chat, err := repo.GetChat(ctx, *launched.ChatID)
	require.NoError(t, err)
	assert.Equal(t, ann.userID, chat.UserID)
	// The newsletter was recorded and skipped by the CEL filter.
	assertFirings(t, repo, ann.userID, ann.trigger.GetId(), map[core.TriggerEventOutcome]int{
		core.TriggerEventLaunched: 1, core.TriggerEventSkipped: 1,
	})
	// Polling again re-reads nothing new and launches nothing twice.
	again := poll(ann)
	assert.Zero(t, again.Items)

	// Bob's poll reads Bob's mailbox with Bob's token, and only Bob's.
	poll(bob)
	launchedBob := waitForLaunched(t, repo, bob.userID, bob.trigger.GetId())
	assert.Equal(t, bob.trigger.GetId()+":b00001", launchedBob.DedupeKey)
	assert.Equal(t, "bob@example.org", launchedBob.Payload["account"])
	assert.Equal(t, map[string]int{"ann@example.com": 2, "bob@example.org": 1}, google.metadataReadsByMailbox(),
		"each user's poll read only their own mailbox")

	// Google revokes ann's access token before it expires (the grant still
	// works): the poll's 401 refreshes the token once and carries on.
	google.expireAccessTokens()
	google.deliver("ann@example.com", "a00004", "Invoice 77 after a revoke", "billing@acme.example")
	refreshes := google.refreshCount()
	recovered := poll(ann)
	assert.False(t, recovered.Skipped, recovered.Reason)
	assert.Equal(t, 1, recovered.Accepted)
	assert.Equal(t, refreshes+1, google.refreshCount(), "one rejection, one refresh")

	// Testing-mode Google: seven days later the refresh token is dead too.
	// Google refuses the access token, the refresh it prompts is answered
	// invalid_grant, and the connection needs reconnecting.
	google.refreshDead = true
	google.expireAccessTokens()
	dead := poll(ann)
	assert.True(t, dead.Skipped)
	connRow, err := store.GetConnection(ctx, ann.userID, ann.conn.ID)
	require.NoError(t, err)
	assert.Equal(t, core.ConnectionStatusNeedsReauth, connRow.Status)
	list, err := svc.ListTriggers(context.WithValue(ctx, auth.UserIDContextKey, ann.userID), connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetTriggers(), 1)
	health := list.Msg.GetTriggers()[0].GetHealth()
	assert.Equal(t, reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING, health.GetStatus(), "a dead source is FAILING, not HEALTHY")
	assert.Contains(t, health.GetLastFailureDetail(), "reconnect")
	refreshes = google.refreshCount()
	poll(ann)
	assert.Equal(t, refreshes, google.refreshCount(), "a dead grant is not retried")

	// The Inbox says so too, as one item that stays the same item across
	// polls (so a dismissal holds), while bob's healthy trigger has none.
	inbox := NewInboxService(repo)
	failing := func(userID string) []*reliantv1.InboxItem {
		items, err := inbox.failingAutomations(ctx, userID)
		require.NoError(t, err)
		return items
	}
	items := failing(ann.userID)
	require.Len(t, items, 1)
	assert.Equal(t, ann.trigger.GetId(), items[0].GetTriggerId())
	assert.Contains(t, items[0].GetAutomationFailing().GetHealth().GetLastFailureDetail(), "reconnect")
	poll(ann)
	again2 := failing(ann.userID)
	require.Len(t, again2, 1)
	assert.Equal(t, items[0].GetItemId(), again2[0].GetItemId(), "the same episode, however often it is polled")
	assert.Empty(t, failing(bob.userID))
}

// gmailPollRepo adds the owner-scoped connection read, as the worker does.
type gmailPollRepo struct {
	*db.Repo
	conns core.ConnectionStore
}

func (r gmailPollRepo) GetConnection(ctx context.Context, userID, id string) (*core.Connection, error) {
	return r.conns.GetConnection(ctx, userID, id)
}

func cleanupGmailSchedule(t *testing.T, c client.Client, triggerID string) {
	t.Cleanup(func() {
		_ = c.ScheduleClient().GetHandle(context.Background(), triggers.ScheduleID(triggerID)).Delete(context.Background())
	})
}

// fakeGoogle serves accounts.google.com, oauth2.googleapis.com and
// gmail.googleapis.com from one TLS server whose certificate names them, so
// calls go to the real hostnames.
type fakeGoogle struct {
	t    *testing.T
	srv  *httptest.Server
	pool *x509.CertPool

	mu          sync.Mutex
	nextEmail   string
	tokens      map[string]string // access token -> mailbox
	refreshes   map[string]string // refresh token -> mailbox
	tokenForm   url.Values
	refreshDead bool
	nRefresh    int
	history     map[string]uint64
	records     map[string][]gmailRecord
	messages    map[string]map[string]any // id -> message
	reads       map[string]int
}

type gmailRecord struct {
	id  uint64
	msg map[string]any
}

var googleHosts = []string{"accounts.google.com", "oauth2.googleapis.com", "gmail.googleapis.com"}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	g := &fakeGoogle{t: t, tokens: map[string]string{}, refreshes: map[string]string{}, history: map[string]uint64{},
		records: map[string][]gmailRecord{}, messages: map[string]map[string]any{}, reads: map[string]int{}}
	cert, pool := selfSigned(t, googleHosts)
	g.pool = pool
	g.srv = httptest.NewUnstartedServer(http.HandlerFunc(g.serve))
	g.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	g.srv.StartTLS()
	t.Cleanup(g.srv.Close)
	return g
}

// dial reaches the fake for any of Google's hosts on 443.
func (g *fakeGoogle) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	for _, h := range googleHosts {
		if host == h && port == "443" {
			return (&net.Dialer{}).DialContext(ctx, network, g.srv.Listener.Addr().String())
		}
	}
	return nil, &net.OpError{Op: "dial", Net: network, Err: errNotGoogle}
}

var errNotGoogle = &net.AddrError{Err: "not a Google host in this test", Addr: ""}

// client is the connections layer's HTTP client: no redirects, Google's hosts.
func (g *fakeGoogle) client() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{DialContext: g.dial, TLSClientConfig: &tls.Config{RootCAs: g.pool, MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// runner is the guarded integration runner, dialing the fake through the
// SSRF guard (which sees a loopback address, allowed for the test).
func (g *fakeGoogle) runner() *httpaction.Runner {
	guard := netguard.New()
	guard.AllowLoopback = true
	guard.Resolve = func(_ context.Context, host string) ([]net.IP, error) {
		for _, h := range googleHosts {
			if host == h {
				return []net.IP{net.ParseIP("127.0.0.1")}, nil
			}
		}
		return nil, errNotGoogle
	}
	r := httpaction.NewRunner(guard).WithRootCAs(g.pool)
	tr := r.Client().Transport.(*http.Transport)
	guarded := tr.DialContext
	_, fakePort, _ := net.SplitHostPort(g.srv.Listener.Addr().String())
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err == nil && port == "443" {
			addr = net.JoinHostPort(host, fakePort)
		}
		return guarded(ctx, network, addr)
	}
	return r
}

func (g *fakeGoogle) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	switch {
	case r.Host == "oauth2.googleapis.com" && r.URL.Path == "/token":
		_ = r.ParseForm()
		g.tokenForm = r.PostForm
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			email := g.nextEmail
			access, refresh := "ya29."+uuid.NewString(), "1//"+uuid.NewString()
			g.tokens[access], g.refreshes[refresh] = email, email
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": refresh, "expires_in": 3599,
				"token_type": "Bearer", "scope": "https://www.googleapis.com/auth/gmail.send https://www.googleapis.com/auth/gmail.readonly"})
		case "refresh_token":
			g.nRefresh++
			email, ok := g.refreshes[r.PostForm.Get("refresh_token")]
			if !ok || g.refreshDead {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
				return
			}
			access := "ya29." + uuid.NewString()
			g.tokens[access] = email
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "expires_in": 3599, "token_type": "Bearer"})
		}
	case r.Host == "gmail.googleapis.com" && strings.HasPrefix(r.URL.Path, "/gmail/v1/users/me/"):
		email, ok := g.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`))
			return
		}
		g.gmail(w, r, email, strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me"))
	default:
		http.Error(w, "unexpected "+r.Host+r.URL.Path, http.StatusNotFound)
	}
}

func (g *fakeGoogle) gmail(w http.ResponseWriter, r *http.Request, email, path string) {
	pos := strconv.FormatUint(g.history[email]+100, 10)
	switch {
	case path == "/profile":
		_ = json.NewEncoder(w).Encode(map[string]any{"emailAddress": email, "historyId": pos})
	case path == "/history":
		start, _ := strconv.ParseUint(r.URL.Query().Get("startHistoryId"), 10, 64)
		var hist []any
		for _, rec := range g.records[email] {
			if rec.id > start {
				hist = append(hist, map[string]any{"id": strconv.FormatUint(rec.id, 10), "messagesAdded": []any{map[string]any{"message": rec.msg}}})
			}
		}
		resp := map[string]any{"historyId": pos}
		if len(hist) > 0 {
			resp["history"] = hist
		}
		_ = json.NewEncoder(w).Encode(resp)
	case strings.HasPrefix(path, "/messages/"):
		m, ok := g.messages[strings.TrimPrefix(path, "/messages/")]
		if !ok || m["_mailbox"] != email {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`))
			return
		}
		g.reads[email]++
		out := map[string]any{}
		for k, v := range m {
			if k != "_mailbox" {
				out[k] = v
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		http.Error(w, "no route", http.StatusNotFound)
	}
}

// deliver puts a new INBOX message in a mailbox and logs its history record.
func (g *fakeGoogle) deliver(email, id, subject, from string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.history[email] += 101
	labels := []string{"INBOX", "UNREAD"}
	g.messages[id] = map[string]any{
		"_mailbox": email, "id": id, "threadId": "t" + id, "labelIds": labels, "snippet": "preview of " + subject,
		"internalDate": strconv.FormatInt(time.Now().UnixMilli(), 10),
		"payload": map[string]any{"mimeType": "text/plain", "headers": []any{
			map[string]any{"name": "From", "value": from}, map[string]any{"name": "To", "value": email},
			map[string]any{"name": "Subject", "value": subject}, map[string]any{"name": "Message-ID", "value": "<" + id + "@mail>"},
		}},
	}
	g.records[email] = append(g.records[email], gmailRecord{id: g.history[email] + 99, msg: map[string]any{"id": id, "threadId": "t" + id, "labelIds": labels}})
}

func (g *fakeGoogle) lastTokenForm() url.Values {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tokenForm
}

func (g *fakeGoogle) metadataReadsByMailbox() map[string]int {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]int{}
	for k, v := range g.reads {
		out[k] = v
	}
	return out
}

// expireAccessTokens revokes every access token Google has issued, so the
// next use must refresh.
func (g *fakeGoogle) expireAccessTokens() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tokens = map[string]string{}
}

func (g *fakeGoogle) refreshCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.nRefresh
}

// selfSigned makes a certificate for hosts and a pool that trusts it.
func selfSigned(t *testing.T, hosts []string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: hosts[0]},
		DNSNames: hosts, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k, Leaf: leaf}, pool
}
