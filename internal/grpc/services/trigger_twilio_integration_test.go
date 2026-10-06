// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/vault"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
)

// Twilio end to end, with nothing faked but Twilio itself and the agent.
// Three users save Twilio connections through the real ConnectionService
// path (CreateAPIKey, which probes a fake api.twilio.com for the account the
// Auth Token belongs to): Alice on account A, Bob on account B, and Carol on
// account A too. Each creates a message.received trigger through the real
// TriggerService, which shows them the URL to set on their numbers. Signed
// form deliveries are POSTed over HTTP to the real app-level receiver,
// verified against each candidate connection's sealed Auth Token, recorded
// by the real intake, and launched by the real event-fire workflow on a real
// Temporal dev server.
//
// Each account's messages reach only its own users' triggers; a shared
// account reaches every user on it whose trigger matches; a Twilio retry of
// the same MessageSid launches once; a delivery signed for the internal URL
// is refused; and the launched run's `trigger.payload.data.body` is the text
// that was sent.
func TestTwilioMessagesLaunchRunsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a Temporal dev server and a worker; skipped under -short")
	}
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	temporalClient := startTriggerDevServer(t)
	taskQueue := "trigger-twilio-e2e-" + uuid.NewString()

	launcher := launch.NewLauncher(repo, threads.NewService(repo), temporalClient,
		runs.NewService(repo, temporalClient, v2workflow.NewPauseService(temporalClient, repo)), taskQueue, nil)
	w := worker.New(temporalClient, taskQueue, temporaltest.WorkerOptions(worker.Options{}))
	w.RegisterWorkflowWithOptions(triggers.TriggerEventFireWorkflow, workflow.RegisterOptions{Name: triggers.EventFireWorkflowName})
	w.RegisterActivityWithOptions(triggers.NewEventFirer(repo, launcher).Fire, activity.RegisterOptions{Name: triggers.EventFireActivityName})
	w.RegisterWorkflowWithOptions(func(workflow.Context, any) error { return nil }, workflow.RegisterOptions{Name: v2workflow.WorkflowDynamic})
	require.NoError(t, w.Start())
	defer w.Stop()

	// --- Twilio connections through the real connection service ----------
	const (
		accountA = "AC00000000000000000000000000000aaa"
		accountB = "AC00000000000000000000000000000bbb"
		tokenA   = "auth-token-for-account-a"
		tokenB   = "auth-token-for-account-b"
	)
	twilio := newTwilioAccountsFake(t, map[string]string{accountA: tokenA, accountB: tokenB})
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(key))
	require.NoError(t, err)
	sealer := vault.New(repo.DB, vault.NewEnvKeyWrapper(ring))
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), func(string) string { return "" })
	require.NoError(t, err)
	doer := twilioDoer{target: twilio}
	store := repo.Connections()
	tokens := connections.NewTokenSource(store, sealer, providers, doer)
	broker := connections.NewBroker(store, sealer, providers, doer, "https://reliant.example")
	conns := connections.NewService(store, sealer, providers, tokens, broker, doer)
	connectTwilio := func(userID, account, token string) *core.Connection {
		conn, err := conns.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
			UserID: userID, IntegrationID: "twilio", Name: "texts", Kind: connections.APIKeyKindBasic,
			Fields: map[string]string{"password": token}, Params: map[string]string{"account_sid": account},
		})
		require.NoError(t, err)
		require.NotNil(t, conn.ExternalAccountID)
		require.Equal(t, account, *conn.ExternalAccountID, "the account is the one Twilio names")
		return conn
	}
	// Someone who knows account B's SID but not its token cannot claim it.
	_, err = conns.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: "mallory", IntegrationID: "twilio", Name: "theirs", Kind: connections.APIKeyKindBasic,
		Fields: map[string]string{"password": tokenA}, Params: map[string]string{"account_sid": accountB},
	})
	require.ErrorIs(t, err, connections.ErrInvalidArgument)

	// --- receivers -------------------------------------------------------
	const publicURL = "https://hooks.example.com"
	registry, err := webhook.RegistryFromEnv(func(k string) string {
		if k == "PUBLIC_URL" {
			return publicURL
		}
		return ""
	})
	require.NoError(t, err)
	intake := triggers.NewIntake(repo, temporalClient, taskQueue)
	inbound := webhook.NewInbound(repo, intake, registry, nil, publicURL).WithConnectionSecrets(tokens)
	mux := http.NewServeMux()
	inbound.Register(func(p string, h http.Handler) { mux.Handle(p, h) })
	server := httptest.NewServer(mux)
	defer server.Close()
	svc := NewTriggerService(repo, nil, nil).WithInbound(InboundOptions{PublicURL: publicURL, Catalog: registry, Intake: intake})

	// --- three users -----------------------------------------------------
	type tenant struct {
		userID  string
		trigger *reliantv1.Trigger
	}
	newTenant := func(account, token string, match map[string]string) tenant {
		userID := uuid.NewString()
		projectID := uuid.NewString()
		now := time.Now().UTC()
		require.NoError(t, repo.CreateProject(ctx, &db.Project{
			ID: projectID, UserID: userID, Name: "Twilio E2E " + userID[:6], Path: t.TempDir(),
			IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		createMainWorktree(t, repo, projectID, now)
		daemonID := uuid.NewString()
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
		conn := connectTwilio(userID, account, token)
		created, err := svc.CreateTrigger(context.WithValue(ctx, auth.UserIDContextKey, userID),
			connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: &reliantv1.TriggerDefinition{
				Name: "answer texts", ProjectId: projectID, DaemonId: daemonID, Workflow: "builtin://agent",
				Message: "Answer the text.", Params: mockModelTriggerParams(t),
				Filter: "!trigger.payload.data.body.contains('unsubscribe')",
				Source: &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{
					Integration: "twilio", Events: []string{"message.received"}, Match: match,
				}},
			}}))
		require.NoError(t, err)
		trig := created.Msg.GetTrigger()
		require.Equal(t, conn.ID, trig.GetConnectionId())
		require.Equal(t, publicURL+"/integrations/twilio/events", trig.GetWebhookUrl(),
			"the trigger tells its owner what to set as each number's webhook")
		return tenant{userID: userID, trigger: trig}
	}
	alice := newTenant(accountA, tokenA, map[string]string{"to": "+15550000001"})
	bob := newTenant(accountB, tokenB, nil)
	carol := newTenant(accountA, tokenA, map[string]string{"channel": "whatsapp"})

	deliver := func(form url.Values, token, signedURL string) int {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/integrations/twilio/events", strings.NewReader(form.Encode()))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Twilio-Signature", signTwilio(token, signedURL, form))
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			assert.Equal(t, "text/xml", resp.Header.Get("Content-Type"), "Twilio gets TwiML back")
		}
		return resp.StatusCode
	}
	sms := func(account, sid, to, body string) url.Values {
		return url.Values{"MessageSid": {sid}, "SmsMessageSid": {sid}, "AccountSid": {account}, "From": {"+15557770000"},
			"To": {to}, "Body": {body}, "NumMedia": {"0"}, "NumSegments": {"1"}, "ApiVersion": {"2010-04-01"}}
	}
	events := publicURL + "/integrations/twilio/events"

	// Signed for the internal URL the request actually hit: refused.
	assert.Equal(t, http.StatusUnauthorized, deliver(sms(accountA, "SMinternal", "+15550000001", "x"), tokenA, server.URL+"/integrations/twilio/events"))
	// Account B's message signed with A's token: refused.
	assert.Equal(t, http.StatusUnauthorized, deliver(sms(accountB, "SMforged", "+15550000002", "x"), tokenA, events))
	// A text Alice's CEL filter skips.
	assert.Equal(t, http.StatusOK, deliver(sms(accountA, "SMskip", "+15550000001", "please unsubscribe me"), tokenA, events))
	// The real one for Alice's number, then Twilio's retries of it, one with
	// the default port in the signed URL.
	assert.Equal(t, http.StatusOK, deliver(sms(accountA, "SMalice1", "+15550000001", "is the deploy done?"), tokenA, events))
	assert.Equal(t, http.StatusOK, deliver(sms(accountA, "SMalice1", "+15550000001", "is the deploy done?"), tokenA, "https://hooks.example.com:443/integrations/twilio/events"))
	// One for Bob's account.
	assert.Equal(t, http.StatusOK, deliver(sms(accountB, "SMbob1", "+15550000002", "status?"), tokenB, events))
	// A WhatsApp message on account A: Carol's trigger matches it, Alice's
	// (narrowed to her SMS number) does not.
	wa := sms(accountA, "SMwa1", "whatsapp:+14155238886", "hola")
	wa.Set("From", "whatsapp:+15557770000")
	wa.Set("ProfileName", "Ada")
	assert.Equal(t, http.StatusOK, deliver(wa, tokenA, events))

	launchedAlice := waitForLaunched(t, repo, alice.userID, alice.trigger.GetId())
	assert.Equal(t, alice.trigger.GetId()+":SMalice1", launchedAlice.DedupeKey)
	assert.Equal(t, "message.received", launchedAlice.Payload["event"])
	assert.Equal(t, accountA, launchedAlice.Payload["account"])
	chat, err := repo.GetChat(ctx, *launchedAlice.ChatID)
	require.NoError(t, err)
	assert.Equal(t, alice.userID, chat.UserID)
	assertRunSees(t, repo, *launchedAlice.ChatID, "trigger.payload.data.body == 'is the deploy done?'")
	assertRunSees(t, repo, *launchedAlice.ChatID, "trigger.payload.attributes.channel == 'sms' && trigger.payload.attributes.to == '+15550000001'")
	assertFirings(t, repo, alice.userID, alice.trigger.GetId(), map[core.TriggerEventOutcome]int{
		core.TriggerEventLaunched: 1, core.TriggerEventSkipped: 1,
	})

	launchedBob := waitForLaunched(t, repo, bob.userID, bob.trigger.GetId())
	assert.Equal(t, bob.trigger.GetId()+":SMbob1", launchedBob.DedupeKey)
	assertRunSees(t, repo, *launchedBob.ChatID, "trigger.payload.data.body == 'status?' && trigger.payload.account == '"+accountB+"'")
	assertFirings(t, repo, bob.userID, bob.trigger.GetId(), map[core.TriggerEventOutcome]int{core.TriggerEventLaunched: 1})

	launchedCarol := waitForLaunched(t, repo, carol.userID, carol.trigger.GetId())
	assert.Equal(t, carol.trigger.GetId()+":SMwa1", launchedCarol.DedupeKey)
	assertRunSees(t, repo, *launchedCarol.ChatID, "trigger.payload.data.body == 'hola' && trigger.payload.data.profile_name == 'Ada'")
	assertFirings(t, repo, carol.userID, carol.trigger.GetId(), map[core.TriggerEventOutcome]int{core.TriggerEventLaunched: 1})
}

// assertRunSees evaluates a CEL condition over the `trigger` root the launched
// run's nodes see (the chat's launch event, as LoadChatTrigger rebuilds it).
func assertRunSees(t *testing.T, repo *db.Repo, chatID, condition string) {
	t.Helper()
	info := launch.LoadChatTrigger(context.Background(), repo, chatID)
	require.NotNil(t, info)
	f, err := triggers.CompileFilter(condition)
	require.NoError(t, err)
	ok, err := f.MatchRoot(info.CELValue())
	require.NoError(t, err, "evaluating %s", condition)
	raw, _ := json.Marshal(info.Payload)
	assert.True(t, ok, "the run sees %s; payload: %s", condition, raw)
}

// signTwilio is X-Twilio-Signature, computed from Twilio's documented
// algorithm independently of the provider under test.
func signTwilio(token, rawURL string, form url.Values) string {
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := rawURL
	for _, k := range keys {
		for _, v := range form[k] {
			s += k + v
		}
	}
	mac := hmac.New(sha1.New, []byte(token))
	mac.Write([]byte(s))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// twilioAccountsFake is api.twilio.com for the identity probe: GET
// /2010-04-01/Accounts/{Sid}.json answers for the account whose Auth Token
// the request carries, and 401 for anything else.
type twilioAccountsFake struct{ srv *httptest.Server }

func newTwilioAccountsFake(t *testing.T, tokens map[string]string) *twilioAccountsFake {
	t.Helper()
	f := &twilioAccountsFake{}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		sid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2010-04-01/Accounts/"), ".json")
		w.Header().Set("Content-Type", "application/json")
		if !ok || user != sid || tokens[sid] == "" || tokens[sid] != pass {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code": 20003, "message": "Authenticate", "more_info": "https://www.twilio.com/docs/errors/20003", "status": 401}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sid": sid, "friendly_name": "Account " + sid[len(sid)-3:], "status": "active", "type": "Full"})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// twilioDoer delivers every api.twilio.com call to the fake.
type twilioDoer struct{ target *twilioAccountsFake }

func (d twilioDoer) Do(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(d.target.srv.URL)
	cp := req.Clone(req.Context())
	cp.URL.Scheme, cp.URL.Host, cp.Host = u.Scheme, u.Host, u.Host
	return d.target.srv.Client().Do(cp)
}
