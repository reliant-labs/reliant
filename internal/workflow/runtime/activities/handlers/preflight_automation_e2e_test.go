// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// enforcingControlPlane behaves like control-plane#582: a daemon-bound
// daemon:resume token works for its own daemon only, and only when daemon_id is
// sent. Resuming flips the daemon from suspended to active.
type enforcingControlPlane struct {
	reliantv1connect.UnimplementedDaemonRegistryServiceHandler
	mu        sync.Mutex
	token     string
	daemonID  string
	suspended bool
	resolves  int
	resumes   int
}

func (c *enforcingControlPlane) authorize(h http.Header, daemonID string) error {
	if h.Get("Authorization") != "Bearer "+c.token {
		return connect.NewError(connect.CodeUnauthenticated, nil)
	}
	if daemonID == "" {
		return connect.NewError(connect.CodeInvalidArgument, nil)
	}
	if daemonID != c.daemonID {
		return connect.NewError(connect.CodePermissionDenied, nil)
	}
	return nil
}

func (c *enforcingControlPlane) ResolveDaemon(_ context.Context, r *connect.Request[reliantv1.ResolveDaemonRequest]) (*connect.Response[reliantv1.ResolveDaemonResponse], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resolves++
	if err := c.authorize(r.Header(), r.Msg.GetDaemonId()); err != nil {
		return nil, err
	}
	status := reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE
	if c.suspended {
		status = reliantv1.DaemonStatus_DAEMON_STATUS_IDLE
	}
	return connect.NewResponse(&reliantv1.ResolveDaemonResponse{
		Found: true, Daemon: &reliantv1.DaemonInfo{DaemonId: c.daemonID, Status: status}}), nil
}

func (c *enforcingControlPlane) ResumeDaemon(_ context.Context, r *connect.Request[reliantv1.ResumeDaemonRequest]) (*connect.Response[reliantv1.ResumeDaemonResponse], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.authorize(r.Header(), r.Msg.GetDaemonId()); err != nil {
		return nil, err
	}
	c.suspended = false
	c.resumes++
	return connect.NewResponse(&reliantv1.ResumeDaemonResponse{Resumed: true}), nil
}

type automationHarness struct {
	repo      *db.Repo
	cp        *enforcingControlPlane
	activity  *PreflightDaemonCheckActivity
	userID    string
	daemonID  string
	chatID    string
	toolCalls *int
	router    *toolexec.NATSDaemonRouter
	mu        *sync.Mutex
}

// newAutomationHarness wires the REAL router, executor, preflight activity and
// database to an in-process NATS server, a fake daemon that answers the tool
// call, and the binding-enforcing control plane. No user JWT is ever set.
func newAutomationHarness(t *testing.T, launchKind core.TriggerEventKind) *automationHarness {
	t.Helper()
	return newAutomationHarnessWithChat(t, launchKind, nil)
}

// newAutomationHarnessWithChat is newAutomationHarness with editChat applied to
// the launched chat before it is stored.
func newAutomationHarnessWithChat(t *testing.T, launchKind core.TriggerEventKind, editChat func(*db.Chat)) *automationHarness {
	t.Helper()
	ctx := context.Background()
	repo := db.NewTestRepo(t)

	opts := natstest.DefaultTestOptions
	opts.Port = -1
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	require.True(t, srv.ReadyForConnections(2*time.Second))
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	userID, daemonID := "user-"+uuid.NewString(), "daemon-"+uuid.NewString()
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, automationcred.Provider(daemonID), "rlat_stored"))
	auth.SetUserJWT(userID, "") // unattended: no JWT

	cp := &enforcingControlPlane{token: "rlat_stored", daemonID: daemonID, suspended: true}
	mux := http.NewServeMux()
	mux.Handle(reliantv1connect.NewDaemonRegistryServiceHandler(cp))
	cpSrv := httptest.NewServer(mux)
	t.Cleanup(cpSrv.Close)

	// The suspended daemon answers once it has been woken.
	calls := 0
	var mu sync.Mutex
	_, err = nc.Subscribe("tools.request.sync."+userID+"."+daemonID, func(m *nats.Msg) {
		mu.Lock()
		calls++
		mu.Unlock()
		body, _ := json.Marshal(&toolexec.ToolExecutionResponse{Success: true, Content: "pong"})
		_ = m.Respond(body)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	router := toolexec.NewNATSDaemonRouter(nc,
		toolexec.WithDatabase(repo),
		toolexec.WithControlPlaneClient(reliantv1connect.NewDaemonRegistryServiceClient(http.DefaultClient, cpSrv.URL)),
		toolexec.WithControlPlaneCredentials(automationcred.NewResolver(repo)))

	projectID, chatID := uuid.NewString(), uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: userID, Name: "p", Path: "/tmp/p",
		CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	chat := &db.Chat{ID: chatID, UserID: userID, Title: "t", ProjectID: projectID,
		State: db.ChatStateIdle, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if editChat != nil {
		editChat(chat)
	}
	require.NoError(t, repo.CreateChat(ctx, chat))
	created, err := repo.CreateTriggerEvent(ctx, &core.TriggerEvent{
		ID: uuid.NewString(), UserID: userID, Kind: launchKind, DedupeKey: chatID,
		OccurredAt: time.Now(), Outcome: core.TriggerEventLaunched, ChatID: &chatID, CreatedAt: time.Now()})
	require.NoError(t, err)
	require.True(t, created)

	return &automationHarness{
		repo: repo, cp: cp, router: router, userID: userID, daemonID: daemonID, chatID: chatID, toolCalls: &calls, mu: &mu,
		activity: NewPreflightDaemonCheckActivity(repo, toolexec.NewRemoteExecutor(router)),
	}
}

func (h *automationHarness) preflight(t *testing.T) (PreflightDaemonCheckOutput, error) {
	t.Helper()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(h.activity.Execute)
	val, err := env.ExecuteActivity(h.activity.Execute, PreflightDaemonCheckInput{
		ChatID: h.chatID, DaemonSelector: &toolexec.DaemonSelector{ID: h.daemonID}})
	if err != nil {
		return PreflightDaemonCheckOutput{}, err
	}
	var out PreflightDaemonCheckOutput
	require.NoError(t, val.Get(&out))
	return out, nil
}

// Every launch a stored trigger makes — a schedule, a webhook delivery, a
// provider event, another run's outcome — fires with nobody signed in. Each,
// with no user JWT, wakes the suspended daemon its trigger names, once, with
// the stored token, and the tool call then runs.
func TestUnattendedFireWithoutJWTResumesPinnedDaemonOnce(t *testing.T) {
	for _, kind := range []core.TriggerEventKind{
		core.TriggerEventKindSchedule,
		core.TriggerEventKindWebhook,
		core.TriggerEventKindIntegration,
		core.TriggerEventKindWorkflowEvent,
	} {
		t.Run(string(kind), func(t *testing.T) {
			h := newAutomationHarness(t, kind)

			out, err := h.preflight(t)
			require.NoError(t, err)
			assert.True(t, out.DaemonAvailable)

			assert.Equal(t, 1, h.cp.resumes, "the daemon is resumed exactly once")
			assert.Equal(t, h.daemonID, out.DaemonID)

			// The woken daemon now answers a tool call, and that call wakes
			// nothing. Tool time holds no credential at all — the token is
			// wake-only — so this also shows the run needs none after preflight.
			_, err = h.router.SendToolRequestSyncWithSelector(context.Background(), h.userID,
				&toolexec.ToolExecutionRequest{RequestID: "r1", ToolName: "ping"}, &toolexec.DaemonSelector{ID: h.daemonID})
			require.NoError(t, err)
			assert.Equal(t, 1, h.cp.resumes, "tool-time traffic never resumes")
			h.mu.Lock()
			defer h.mu.Unlock()
			assert.Equal(t, 1, *h.toolCalls, "the tool call reached the woken daemon")
		})
	}
}

// An attended run — a human's first send, an agent's start_run, a builder test
// — pinned to a daemon must not borrow the automation token: only runs a
// stored trigger launched may.
func TestAttendedRunWithoutJWTDoesNotUseAutomationToken(t *testing.T) {
	for _, kind := range []core.TriggerEventKind{
		core.TriggerEventKindChatStart,
		core.TriggerEventKindAgentStartRun,
		core.TriggerEventKindBuilderTest,
	} {
		t.Run(string(kind), func(t *testing.T) {
			h := newAutomationHarness(t, kind)

			_, err := h.preflight(t)
			require.Error(t, err)
			assert.Zero(t, h.cp.resumes, "an attended run must not wake the daemon with the stored token")
		})
	}
}

// Widening the token to every unattended kind must not reach a run with no
// machine: such a run never resolves or wakes a daemon, token or not.
func TestNoMachineUnattendedRunNeverWakes(t *testing.T) {
	for _, kind := range []core.TriggerEventKind{
		core.TriggerEventKindSchedule,
		core.TriggerEventKindWebhook,
		core.TriggerEventKindIntegration,
		core.TriggerEventKindWorkflowEvent,
	} {
		t.Run(string(kind), func(t *testing.T) {
			h := newAutomationHarnessWithChat(t, kind, func(c *db.Chat) { c.NoMachine = true })

			out, err := h.preflight(t)
			require.NoError(t, err)
			assert.False(t, out.DaemonAvailable)
			assert.Empty(t, out.DaemonID)
			assert.Zero(t, h.cp.resolves, "a no-machine run never asks the control plane for a daemon")
			assert.Zero(t, h.cp.resumes, "a no-machine run never wakes one")
		})
	}
}
