// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

// Slack end to end, with nothing faked but Slack itself and the agent. Two
// users connect Slack through the real OAuth broker — against a fake
// slack.com whose oauth.v2.access nests a user token beside the bot token —
// one to workspace T0ACME, one to T0OTHER. Each creates an app_mention trigger
// through the real TriggerService. Signed Events API deliveries are POSTed
// over HTTP to the real app-level receiver (the shipped Slack provider,
// registered from RELIANT_SLACK_SIGNING_SECRET), recorded by the real intake,
// and launched by the real event-fire workflow on a real Temporal dev server.
//
// Each user's trigger fires only for its own workspace, a Slack retry of the
// same event_id never launches twice, a stale replay is refused, and the
// bot's own messages never fire anything.
func TestSlackEventsLaunchRunsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a Temporal dev server and a worker; skipped under -short")
	}
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	temporalClient := startTriggerDevServer(t)
	taskQueue := "trigger-slack-e2e-" + uuid.NewString()

	launcher := launch.NewLauncher(repo, threads.NewService(repo), temporalClient,
		runs.NewService(repo, temporalClient, v2workflow.NewPauseService(temporalClient, repo)), taskQueue, nil)
	w := worker.New(temporalClient, taskQueue, temporaltest.WorkerOptions(worker.Options{}))
	w.RegisterWorkflowWithOptions(triggers.TriggerEventFireWorkflow, workflow.RegisterOptions{Name: triggers.EventFireWorkflowName})
	w.RegisterActivityWithOptions(triggers.NewEventFirer(repo, launcher).Fire, activity.RegisterOptions{Name: triggers.EventFireActivityName})
	w.RegisterWorkflowWithOptions(func(workflow.Context, any) error { return nil }, workflow.RegisterOptions{Name: v2workflow.WorkflowDynamic})
	require.NoError(t, w.Start())
	defer w.Stop()

	// --- Slack connections through the real OAuth broker ------------------
	slack := newSlackOAuthFake(t)
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(key))
	require.NoError(t, err)
	sealer := vault.New(repo.DB, vault.NewEnvKeyWrapper(ring))
	providers, err := connections.ProvidersFromCatalog(catalog.MustBuiltin().Manifests(), func(k string) string {
		return map[string]string{"RELIANT_OAUTH_SLACK_CLIENT_ID": "1234.5678", "RELIANT_OAUTH_SLACK_CLIENT_SECRET": "s3cret"}[k]
	})
	require.NoError(t, err)
	doer := slackDoer{target: slack}
	store := repo.Connections()
	tokens := connections.NewTokenSource(store, sealer, providers, doer)
	broker := connections.NewBroker(store, sealer, providers, doer, "https://reliant.example")
	conns := connections.NewService(store, sealer, providers, tokens, broker, doer)
	connectSlack := func(userID, team string) *core.Connection {
		slack.team = team
		authURL, err := conns.StartOAuth(ctx, connections.StartParams{UserID: userID, IntegrationID: "slack", ClientOrigin: "https://reliant.example"})
		require.NoError(t, err)
		u, _ := url.Parse(authURL)
		require.Contains(t, u.Query().Get("scope"), "chat:write,", "comma-joined bot scopes")
		done, err := conns.CompleteOAuth(ctx, userID, u.Query().Get("state"), "code-"+team)
		require.NoError(t, err)
		require.Equal(t, team, *done.Connection.ExternalAccountID)
		return done.Connection
	}

	// --- receivers -------------------------------------------------------
	const signingSecret = "slack-signing-secret-e2e"
	registry, err := webhook.RegistryFromEnv(func(k string) string {
		if k == webhook.SlackSigningSecretEnv {
			return signingSecret
		}
		return ""
	})
	require.NoError(t, err)
	intake := triggers.NewIntake(repo, temporalClient, taskQueue)
	const publicURL = "https://hooks.example.com"
	inbound := webhook.NewInbound(repo, intake, registry, nil, publicURL)
	mux := http.NewServeMux()
	inbound.Register(func(p string, h http.Handler) { mux.Handle(p, h) })
	server := httptest.NewServer(mux)
	defer server.Close()
	svc := NewTriggerService(repo, nil, nil).WithInbound(InboundOptions{PublicURL: publicURL, Catalog: registry, Intake: intake})

	// --- two users, two workspaces ---------------------------------------
	type tenant struct {
		userID  string
		conn    *core.Connection
		trigger *reliantv1.Trigger
	}
	newTenant := func(team string) tenant {
		userID := uuid.NewString()
		projectID := uuid.NewString()
		now := time.Now().UTC()
		require.NoError(t, repo.CreateProject(ctx, &db.Project{
			ID: projectID, UserID: userID, Name: "Slack E2E " + team, Path: t.TempDir(),
			IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		createMainWorktree(t, repo, projectID, now)
		daemonID := uuid.NewString()
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
		conn := connectSlack(userID, team)
		created, err := svc.CreateTrigger(context.WithValue(ctx, auth.UserIDContextKey, userID),
			connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: &reliantv1.TriggerDefinition{
				Name: "answer mentions", ProjectId: projectID, DaemonId: daemonID, Workflow: "builtin://agent",
				Message: "Answer the mention.", Params: mockModelTriggerParams(t),
				Filter: "!trigger.payload.data.text.contains('ignore me')",
				Source: &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{
					Integration: "slack", Events: []string{"app_mention"},
				}},
			}}))
		require.NoError(t, err)
		require.Equal(t, conn.ID, created.Msg.GetTrigger().GetConnectionId(), "the trigger listens through the user's Slack connection")
		return tenant{userID: userID, conn: conn, trigger: created.Msg.GetTrigger()}
	}
	acme := newTenant("T0ACME")
	other := newTenant("T0OTHER")

	deliver := func(body string, at time.Time, retry int) int {
		ts := strconv.FormatInt(at.Unix(), 10)
		mac := hmac.New(sha256.New, []byte(signingSecret))
		mac.Write([]byte("v0:" + ts + ":" + body))
		req, err := http.NewRequest(http.MethodPost, server.URL+"/integrations/slack/events", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Slack-Request-Timestamp", ts)
		req.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
		if retry > 0 {
			req.Header.Set("X-Slack-Retry-Num", strconv.Itoa(retry))
			req.Header.Set("X-Slack-Retry-Reason", "http_timeout")
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	mention := func(team, eventID, text string) string {
		return `{"token":"x","team_id":"` + team + `","api_app_id":"A0APP","type":"event_callback","event_id":"` + eventID + `",` +
			`"event_time":` + strconv.FormatInt(time.Now().Unix(), 10) + `,"event":{"type":"app_mention","user":"U0ADA",` +
			`"text":"<@U0BOT> ` + text + `","ts":"1700000000.000100","channel":"C0GEN","event_ts":"1700000000.000100"},` +
			`"authorizations":[{"team_id":"` + team + `","user_id":"U0BOT","is_bot":true}]}`
	}
	now := time.Now()

	// Saving the Request URL in Slack's app config sends a signed handshake.
	assert.Equal(t, http.StatusOK, deliver(`{"token":"x","challenge":"hello-slack","type":"url_verification"}`, now, 0))
	// A replay of a real event, correctly signed but ten minutes old.
	assert.Equal(t, http.StatusUnauthorized, deliver(mention("T0ACME", "EvREPLAY", "replayed"), now.Add(-10*time.Minute), 0))
	// The bot's own message in acme's workspace: acked, never recorded.
	assert.Equal(t, http.StatusOK, deliver(`{"token":"x","team_id":"T0ACME","api_app_id":"A0APP","type":"event_callback",`+
		`"event_id":"EvBOT","event_time":1,"event":{"type":"message","channel":"C0GEN","bot_id":"B0BOT","text":"done","ts":"1.1","channel_type":"channel"}}`, now, 0))
	// A mention the owner's CEL filter skips.
	assert.Equal(t, http.StatusOK, deliver(mention("T0ACME", "EvSKIP", "ignore me please"), now, 0))
	// The real one in acme, then Slack's retries of it.
	for retry := 0; retry <= 2; retry++ {
		assert.Equal(t, http.StatusOK, deliver(mention("T0ACME", "EvACME1", "summarize the thread"), now, retry))
	}
	// One in the other workspace.
	assert.Equal(t, http.StatusOK, deliver(mention("T0OTHER", "EvOTHER1", "status?"), now, 0))

	launchedAcme := waitForLaunched(t, repo, acme.userID, acme.trigger.GetId())
	assert.Equal(t, acme.trigger.GetId()+":EvACME1", launchedAcme.DedupeKey)
	assert.Equal(t, "app_mention", launchedAcme.Payload["event"])
	assert.Equal(t, "T0ACME", launchedAcme.Payload["account"])
	data, _ := launchedAcme.Payload["data"].(map[string]any)
	assert.Equal(t, "<@U0BOT> summarize the thread", data["text"])
	chat, err := repo.GetChat(ctx, *launchedAcme.ChatID)
	require.NoError(t, err)
	assert.Equal(t, acme.userID, chat.UserID)
	assertFirings(t, repo, acme.userID, acme.trigger.GetId(), map[core.TriggerEventOutcome]int{
		core.TriggerEventLaunched: 1, core.TriggerEventSkipped: 1,
	})

	launchedOther := waitForLaunched(t, repo, other.userID, other.trigger.GetId())
	assert.Equal(t, other.trigger.GetId()+":EvOTHER1", launchedOther.DedupeKey)
	assert.Equal(t, "T0OTHER", launchedOther.Payload["account"])
	assertFirings(t, repo, other.userID, other.trigger.GetId(), map[core.TriggerEventOutcome]int{core.TriggerEventLaunched: 1})
}

// slackOAuthFake is slack.com for the OAuth half: oauth.v2.access (bot token
// top-level, a user token nested under authed_user) and auth.test.
type slackOAuthFake struct {
	srv  *httptest.Server
	team string
}

func newSlackOAuthFake(t *testing.T) *slackOAuthFake {
	t.Helper()
	f := &slackOAuthFake{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth.v2.access", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "access_token": "xoxb-" + f.team, "token_type": "bot", "scope": "chat:write,app_mentions:read",
			"bot_user_id": "U0BOT", "app_id": "A0APP", "team": map[string]any{"id": f.team, "name": f.team + " Inc"},
			"authed_user": map[string]any{"id": "U0ADA", "access_token": "xoxp-decoy", "token_type": "user"},
		})
	})
	mux.HandleFunc("/api/auth.test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		team := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer xoxb-")
		if team == r.Header.Get("Authorization") {
			_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "team_id": team, "team": team + " Inc", "user_id": "U0BOT"})
	})
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// slackDoer delivers every slack.com call to the fake.
type slackDoer struct{ target *slackOAuthFake }

func (d slackDoer) Do(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(d.target.srv.URL)
	cp := req.Clone(req.Context())
	cp.URL.Scheme, cp.URL.Host, cp.Host = u.Scheme, u.Host, u.Host
	return d.target.srv.Client().Do(cp)
}
