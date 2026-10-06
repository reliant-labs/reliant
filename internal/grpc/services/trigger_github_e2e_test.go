// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/triggers"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
)

// fakeGitHubAPI answers the three endpoints an access refresh reads, per
// user token: GitHub's own view of which repositories, in which App
// installations, each user can see.
type fakeGitHubAPI struct {
	// token -> GitHub user id -> installation id -> repositories (id, name)
	users map[string]struct {
		id    int64
		repos map[int64][][2]any
	}
}

func (f *fakeGitHubAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u, ok := f.users[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/user":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": u.id})
	case r.URL.Path == "/user/installations":
		var out []map[string]any
		for id := range u.repos {
			out = append(out, map[string]any{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"installations": out})
	case strings.HasSuffix(r.URL.Path, "/repositories"):
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/user/installations/"), "/repositories"), 10, 64)
		var out []map[string]any
		for _, rp := range u.repos[id] {
			out = append(out, map[string]any{"id": rp[0], "full_name": rp[1]})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"repositories": out})
	default:
		http.NotFound(w, r)
	}
}

// userTokens maps a reliant user to their GitHub App user token (what
// control-plane would hand out).
type userTokens map[string]string

func (u userTokens) Token(_ context.Context, userID string) (string, error) {
	if tok, ok := u[userID]; ok {
		return tok, nil
	}
	return "", ghaccess.ErrNotConnected
}

type e2eGitHubAccess struct{ *ghaccess.Refresher }

func (e2eGitHubAccess) IsPermanent(err error) bool { return ghaccess.IsPermanent(err) }

// GitHub triggers end to end, with nothing faked but the agent and GitHub
// itself: triggers created through the real TriggerService (which refreshes
// each owner's access through the real ghaccess refresher, against a fake
// GitHub API), signed deliveries POSTed to the real app-level receiver,
// recorded by the real intake and launched by the real event-fire workflow on
// a Temporal dev server.
//
// alice and bob are both members of one org installation (98765). alice can
// see acme/app; bob only acme/other. A delivery about acme/app must reach
// alice's trigger and never bob's, even though bob's trigger listens to the
// same event type in the same installation.
func TestGitHubTriggersFireEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a Temporal dev server and a worker; skipped under -short")
	}
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	temporalClient := startTriggerDevServer(t)
	taskQueue := "trigger-github-e2e-" + uuid.NewString()

	launcher := launch.NewLauncher(repo, threads.NewService(repo), temporalClient,
		runs.NewService(repo, temporalClient, v2workflow.NewPauseService(temporalClient, repo)), taskQueue, nil)
	w := worker.New(temporalClient, taskQueue, temporaltest.WorkerOptions(worker.Options{}))
	w.RegisterWorkflowWithOptions(triggers.TriggerEventFireWorkflow, workflow.RegisterOptions{Name: triggers.EventFireWorkflowName})
	w.RegisterActivityWithOptions(triggers.NewEventFirer(repo, launcher).Fire, activity.RegisterOptions{Name: triggers.EventFireActivityName})
	w.RegisterWorkflowWithOptions(func(workflow.Context, any) error { return nil }, workflow.RegisterOptions{Name: v2workflow.WorkflowDynamic})
	require.NoError(t, w.Start())
	defer w.Stop()

	// --- the deployment: GitHub provider + access refresher ---------------
	const secret = "gh-e2e-secret"
	registry, err := webhook.RegistryFromEnv(func(k string) string {
		if k == "RELIANT_GITHUB_WEBHOOK_SECRET" {
			return secret
		}
		return ""
	})
	require.NoError(t, err)

	alice, bob := uuid.NewString(), uuid.NewString()
	gh := &fakeGitHubAPI{users: map[string]struct {
		id    int64
		repos map[int64][][2]any
	}{
		"tok-alice": {id: 11, repos: map[int64][][2]any{98765: {{123456, "acme/app"}}}},
		"tok-bob":   {id: 22, repos: map[int64][][2]any{98765: {{777, "acme/other"}}}},
	}}
	ghServer := httptest.NewServer(gh)
	defer ghServer.Close()
	refresher := ghaccess.New(repo, userTokens{alice: "tok-alice", bob: "tok-bob"},
		ghaccess.Options{APIBaseURL: ghServer.URL, HTTPClient: ghServer.Client()})

	intake := triggers.NewIntake(repo, temporalClient, taskQueue)
	const publicURL = "https://hooks.example.com"
	inbound := webhook.NewInbound(repo, intake, registry, nil, publicURL)
	mux := http.NewServeMux()
	inbound.Register(func(p string, h http.Handler) { mux.Handle(p, h) })
	server := httptest.NewServer(mux)
	defer server.Close()

	svc := NewTriggerService(repo, nil, nil).WithInbound(InboundOptions{
		PublicURL: publicURL, Catalog: registry, Intake: intake,
		Access: map[string]IntegrationAccess{"github": e2eGitHubAccess{refresher}},
	})

	// --- two users, one project each ---------------------------------------
	now := time.Now().UTC()
	type owner struct {
		id, project, daemon string
		ctx                 context.Context
	}
	setup := func(userID string) owner {
		o := owner{id: userID, project: uuid.NewString(), daemon: uuid.NewString()}
		require.NoError(t, repo.CreateProject(ctx, &db.Project{
			ID: o.project, UserID: userID, Name: "GitHub E2E", Path: t.TempDir(),
			IsGitRepo: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
		}))
		createMainWorktree(t, repo, o.project, now)
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: o.daemon, UserID: userID}))
		o.ctx = context.WithValue(ctx, auth.UserIDContextKey, userID)
		return o
	}
	a, b := setup(alice), setup(bob)
	create := func(o owner, name string, src *reliantv1.IntegrationSource) *reliantv1.Trigger {
		t.Helper()
		resp, err := svc.CreateTrigger(o.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: &reliantv1.TriggerDefinition{
			Name: name, ProjectId: o.project, DaemonId: o.daemon, Workflow: "builtin://agent",
			Message: "Triage it.", Params: mockModelTriggerParams(t),
			Source: &reliantv1.TriggerDefinition_Integration{Integration: src},
		}}))
		require.NoError(t, err)
		return resp.Msg.GetTrigger()
	}

	aliceIssues := create(a, "alice issues", &reliantv1.IntegrationSource{Integration: "github", Events: []string{"issues.opened"}})
	alicePushMain := create(a, "alice push main", &reliantv1.IntegrationSource{
		Integration: "github", Events: []string{"push"}, Match: map[string]string{"repository": "acme/app", "branch": "main"},
	})
	alicePushOther := create(a, "alice push other repo", &reliantv1.IntegrationSource{
		Integration: "github", Events: []string{"push"}, Match: map[string]string{"repository": "acme/other"},
	})
	bobIssues := create(b, "bob issues", &reliantv1.IntegrationSource{Integration: "github", Events: []string{"issues.*"}})
	assert.Empty(t, aliceIssues.GetConnectionId(), "hosted GitHub: no connection row")

	// --- deliveries ----------------------------------------------------------
	deliver := func(event, delivery string, body []byte, sig string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/integrations/github/events", strings.NewReader(string(body)))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-GitHub-Delivery", delivery)
		if sig != "" {
			req.Header.Set("X-Hub-Signature-256", sig)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	sign := func(body []byte) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	fixture := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "..", "integrations", "webhook", "github", "testdata", name+".json"))
		require.NoError(t, err)
		return b
	}

	issue := fixture("issues.opened") // installation 98765, repository 123456 acme/app
	assert.Equal(t, http.StatusUnauthorized, deliver("issues", "d-forged", issue, sign([]byte("other"))), "a bad signature is refused")
	assert.Equal(t, http.StatusUnauthorized, deliver("issues", "d-unsigned", issue, ""), "a missing signature is refused")
	assert.Equal(t, http.StatusOK, deliver("ping", "d-ping", fixture("ping"), sign(fixture("ping"))), "ping is acked")
	for i := 0; i < 3; i++ {
		assert.Equal(t, http.StatusOK, deliver("issues", "issue-delivery-1", issue, sign(issue)), "redelivery %d", i)
	}

	launched := waitForLaunched(t, repo, alice, aliceIssues.GetId())
	assert.Equal(t, aliceIssues.GetId()+":issue-delivery-1", launched.DedupeKey)
	assert.Equal(t, "issues.opened", launched.Payload["event"])
	assert.Equal(t, "98765", launched.Payload["account"])
	assert.Equal(t, "123456", launched.Payload["resource"])
	chat, err := repo.GetChat(ctx, *launched.ChatID)
	require.NoError(t, err)
	assert.Equal(t, alice, chat.UserID)
	assertFirings(t, repo, alice, aliceIssues.GetId(), map[core.TriggerEventOutcome]int{core.TriggerEventLaunched: 1})
	// bob is in the installation, listens to issues.*, and cannot see the
	// repository: nothing is recorded for him at all.
	assertFirings(t, repo, bob, bobIssues.GetId(), map[core.TriggerEventOutcome]int{})

	// A push to main on acme/app: alice's main-branch trigger, not her
	// other-repo one; a tag push matches neither branch trigger.
	push := fixture("push")
	assert.Equal(t, http.StatusOK, deliver("push", "push-1", push, sign(push)))
	tag := fixture("push.tag")
	assert.Equal(t, http.StatusOK, deliver("push", "push-tag-1", tag, sign(tag)))
	pushLaunch := waitForLaunched(t, repo, alice, alicePushMain.GetId())
	assert.Equal(t, alicePushMain.GetId()+":push-1", pushLaunch.DedupeKey)
	assertFirings(t, repo, alice, alicePushMain.GetId(), map[core.TriggerEventOutcome]int{core.TriggerEventLaunched: 1})
	assertFirings(t, repo, alice, alicePushOther.GetId(), map[core.TriggerEventOutcome]int{})

	// The repository is removed from the installation: from that delivery
	// on, its events reach no one — before any refresh notices.
	removed := fixture("installation_repositories.removed")
	assert.Equal(t, http.StatusOK, deliver("installation_repositories", "rm-1", removed, sign(removed)))
	assert.Equal(t, http.StatusOK, deliver("issues", "issue-delivery-2", issue, sign(issue)))
	assertFirings(t, repo, alice, aliceIssues.GetId(), map[core.TriggerEventOutcome]int{core.TriggerEventLaunched: 1})

	// A refresh restores what GitHub still reports: here, still visible.
	require.NoError(t, refresher.Refresh(ctx, alice))
	assert.Equal(t, http.StatusOK, deliver("issues", "issue-delivery-3", issue, sign(issue)))
	waitForEventCount(t, repo, alice, aliceIssues.GetId(), 2)
	assertFirings(t, repo, bob, bobIssues.GetId(), map[core.TriggerEventOutcome]int{})

	// A user whose GitHub is not connected cannot activate a GitHub trigger.
	carol := setup(uuid.NewString())
	_, err = svc.CreateTrigger(carol.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: &reliantv1.TriggerDefinition{
		Name: "carol", ProjectId: carol.project, DaemonId: carol.daemon, Workflow: "builtin://agent", Message: "x",
		Source: &reliantv1.TriggerDefinition_Integration{Integration: &reliantv1.IntegrationSource{Integration: "github", Events: []string{"push"}}},
	}}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), fmt.Sprint(err))
}

func waitForEventCount(t *testing.T, repo *db.Repo, userID, triggerID string, want int) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		events, _, err := repo.ListTriggerEvents(context.Background(), core.TriggerEventFilters{UserID: userID, TriggerID: triggerID, Limit: 50})
		if err == nil {
			launched := 0
			for _, ev := range events {
				if ev.Event.Outcome == core.TriggerEventLaunched && ev.Event.ChatID != nil {
					launched++
				}
			}
			if launched >= want {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("trigger %s: fewer than %d launched events within 60s", triggerID, want)
}
