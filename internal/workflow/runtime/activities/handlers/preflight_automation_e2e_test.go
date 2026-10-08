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

	daemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
	"github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/daemonquery"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// enforcingControlPlane behaves like control-plane#582: a daemon-bound
// daemon:resume token works for its own daemon only, and only when daemon_id is
// sent. It serves controlplane.v1.DaemonService/ResumeDaemon, the one call
// reliant makes to wake a machine; the daemon's lifecycle itself is read from
// reliant's own daemons records.
type enforcingControlPlane struct {
	controlplanev1connect.UnimplementedDaemonServiceHandler
	mu       sync.Mutex
	token    string
	daemonID string
	requests int // every call received, authorized or not
	resumes  int
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

func (c *enforcingControlPlane) ResumeDaemon(_ context.Context, r *connect.Request[daemonv1.ResumeDaemonRequest]) (*connect.Response[daemonv1.ResumeDaemonResponse], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests++
	if err := c.authorize(r.Header(), r.Msg.GetDaemonId()); err != nil {
		return nil, err
	}
	c.resumes++
	return connect.NewResponse(&daemonv1.ResumeDaemonResponse{}), nil
}

func (c *enforcingControlPlane) resumeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resumes
}

func (c *enforcingControlPlane) requestCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

type automationHarness struct {
	repo        *db.Repo
	cp          *enforcingControlPlane
	activity    *PreflightDaemonCheckActivity
	userID      string
	daemonID    string
	chatID      string
	suspendedAt time.Time
	toolCalls   *int
	router      *toolexec.NATSDaemonRouter
	nc          *nats.Conn
	mu          *sync.Mutex
}

// newAutomationHarness wires the REAL router, executor, preflight activity and
// database to an in-process NATS server, a fake daemon that answers the tool
// call, and the binding-enforcing control plane. The daemon's record says it
// is suspended, as the control plane's lifecycle mirror would. No user JWT is
// ever set.
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

	// A managed machine the control plane has parked: its record is the
	// registry's, and its phase is the control plane's lifecycle, mirrored.
	managed := "managed"
	now := time.Now().UTC()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{
		ID: daemonID, UserID: userID, DaemonType: &managed, CreatedAt: now, UpdatedAt: now,
	}))
	suspendedAt := now
	applied, err := repo.ApplyDaemonLifecycle(ctx, db.DaemonLifecycleUpdate{
		DaemonID: daemonID, Phase: string(daemonstate.LifecyclePhaseSuspended), ChangedAt: suspendedAt,
	})
	require.NoError(t, err)
	require.True(t, applied)

	cp := &enforcingControlPlane{token: "rlat_stored", daemonID: daemonID}
	mux := http.NewServeMux()
	mux.Handle(controlplanev1connect.NewDaemonServiceHandler(cp))
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
		toolexec.WithDaemonResumer(controlplane.NewDaemonClient(cpSrv.URL)),
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
		repo: repo, cp: cp, router: router, nc: nc, userID: userID, daemonID: daemonID, chatID: chatID,
		suspendedAt: suspendedAt, toolCalls: &calls, mu: &mu,
		activity: newFastPollPreflight(repo, router),
	}
}

func newFastPollPreflight(repo db.Repository, router toolexec.DaemonRouter) *PreflightDaemonCheckActivity {
	a := NewPreflightDaemonCheckActivity(repo, toolexec.NewRemoteExecutor(router))
	a.pollInterval = 20 * time.Millisecond
	return a
}

// preflight runs the check as a single, non-waiting execution.
func (h *automationHarness) preflight(t *testing.T) (PreflightDaemonCheckOutput, error) {
	t.Helper()
	return h.preflightWait(t, 0)
}

// preflightWait runs the check with a wait slice of waitSeconds.
func (h *automationHarness) preflightWait(t *testing.T, waitSeconds int) (PreflightDaemonCheckOutput, error) {
	t.Helper()
	env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(h.activity.Execute)
	val, err := env.ExecuteActivity(h.activity.Execute, PreflightDaemonCheckInput{
		ChatID: h.chatID, DaemonSelector: &toolexec.DaemonSelector{ID: h.daemonID}, WaitSeconds: waitSeconds})
	if err != nil {
		return PreflightDaemonCheckOutput{}, err
	}
	var out PreflightDaemonCheckOutput
	require.NoError(t, val.Get(&out))
	return out, nil
}

// attach makes the woken daemon reachable the way a gateway holding its stream
// does: by answering its per-daemon status query.
func (h *automationHarness) attach(t *testing.T) {
	t.Helper()
	sub, err := h.nc.Subscribe(daemonquery.SubjectStatus(h.daemonID), func(m *nats.Msg) {
		body, _ := json.Marshal(daemonquery.Status{Connected: true, LastActiveMs: time.Now().UnixMilli()})
		_ = m.Respond(body)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, h.nc.Flush())
}

// markReady mirrors the lifecycle event the control plane publishes once the
// resumed machine is back up — newer than the suspension, so it wins.
func (h *automationHarness) markReady(t *testing.T) {
	t.Helper()
	applied, err := h.repo.ApplyDaemonLifecycle(context.Background(), db.DaemonLifecycleUpdate{
		DaemonID: h.daemonID, Phase: string(daemonstate.LifecyclePhaseReady), ChangedAt: h.suspendedAt.Add(time.Second),
	})
	require.NoError(t, err)
	require.True(t, applied, "a later lifecycle event must replace the suspension")
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

			// The resume is accepted but the daemon is not attached yet: the
			// check waits for it, and wakes it only once while it does.
			go func() {
				for h.cp.resumeCount() == 0 {
					time.Sleep(10 * time.Millisecond)
				}
				h.markReady(t)
				h.attach(t)
			}()
			out, err := h.preflightWait(t, 5)
			require.NoError(t, err)
			assert.True(t, out.DaemonAvailable)
			assert.False(t, out.Waiting)
			assert.Equal(t, 1, h.cp.resumeCount(), "the daemon is resumed exactly once")
			assert.Equal(t, h.daemonID, out.DaemonID)

			// The woken daemon now answers a tool call, and that call wakes
			// nothing. Tool time holds no credential at all — the token is
			// wake-only — so this also shows the run needs none after preflight.
			_, err = h.router.SendToolRequestSyncWithSelector(context.Background(), h.userID,
				&toolexec.ToolExecutionRequest{RequestID: "r1", ToolName: "ping"}, &toolexec.DaemonSelector{ID: h.daemonID})
			require.NoError(t, err)
			assert.Equal(t, 1, h.cp.resumeCount(), "tool-time traffic never resumes")
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
			assert.Zero(t, h.cp.resumeCount(), "an attended run must not wake the daemon with the stored token")
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
			assert.Zero(t, h.cp.requestCount(), "a no-machine run never calls the control plane")
			assert.Zero(t, h.cp.resumeCount(), "a no-machine run never wakes one")
		})
	}
}
