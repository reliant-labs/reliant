// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// fakeLink is a fake transport for one daemon connection, standing in for the
// stream and the daemon behind it: it reads what the gateway queues for the
// daemon (conn.sendCh) and answers each daemon command the way the link
// behaves right now. A dead link swallows commands, as a laptop that
// re-registered while still mostly asleep does.
type fakeLink struct {
	mu   sync.Mutex
	dead bool
	// drop, when set, swallows the commands it returns true for even on a
	// live link: the link dropping again mid-pass.
	drop func(commandType string, payload []byte) bool
	// log is every command the daemon received, in order, with "!" in front
	// of each the link swallowed.
	log []string
}

func (f *fakeLink) setDead(dead bool) {
	f.mu.Lock()
	f.dead = dead
	f.mu.Unlock()
}

func (f *fakeLink) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *fakeLink) run(t *testing.T, conn *daemonConnection) {
	t.Helper()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case msg := <-conn.sendCh:
				cmd := msg.GetDaemonCommand()
				if cmd == nil {
					continue
				}
				f.mu.Lock()
				swallowed := f.dead || (f.drop != nil && f.drop(cmd.GetCommandType(), cmd.GetPayload()))
				entry := cmd.GetCommandType()
				if cmd.GetCommandType() == "repo.discover" {
					var p struct {
						Path string `json:"path"`
					}
					_ = json.Unmarshal(cmd.GetPayload(), &p)
					entry += " " + p.Path
				}
				if swallowed {
					entry = "!" + entry
				}
				f.log = append(f.log, entry)
				f.mu.Unlock()
				if swallowed {
					continue
				}
				conn.routeInboundReply(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_DaemonCommandResponse{
					DaemonCommandResponse: &reliantv1.DaemonCommandResponse{
						RequestId: cmd.GetRequestId(), CommandType: cmd.GetCommandType(),
						Success: true, Payload: []byte(`{"discovered":[]}`),
					},
				}})
			}
		}
	}()
}

// readyRecorder is a connection listener that records OnDaemonLinkReady.
type readyRecorder struct {
	mu    sync.Mutex
	ready []string
}

func (r *readyRecorder) OnDaemonConnected(string, string)    {}
func (r *readyRecorder) OnDaemonDisconnected(string, string) {}
func (r *readyRecorder) OnDaemonLinkReady(_, daemonID, _, _, _ string) {
	r.mu.Lock()
	r.ready = append(r.ready, daemonID)
	r.mu.Unlock()
}

func (r *readyRecorder) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ready...)
}

// shrinkLinkTimings makes the link probe and its backoff test-sized.
func shrinkLinkTimings(t *testing.T) {
	t.Helper()
	probe, healthy, lo, hi, perCmd := linkProbeTimeout, linkHealthyRTT, linkRetryMin, linkRetryMax, reconcilePerCommandTimeoutMs
	linkProbeTimeout, linkHealthyRTT = 40*time.Millisecond, 30*time.Millisecond
	linkRetryMin, linkRetryMax = 5*time.Millisecond, 10*time.Millisecond
	reconcilePerCommandTimeoutMs = 40
	t.Cleanup(func() {
		linkProbeTimeout, linkHealthyRTT, linkRetryMin, linkRetryMax, reconcilePerCommandTimeoutMs = probe, healthy, lo, hi, perCmd
	})
}

// linkTestService is a gateway holding one freshly registered connection whose
// link is unproven, over a fake transport, with three of the user's projects
// on the daemon's disk. upserts records the project_daemons rows reconcile
// writes, by project path.
func linkTestService(t *testing.T) (svc *Service, conn *daemonConnection, link *fakeLink, ready *readyRecorder, upserts func() []string, paths []string) {
	t.Helper()
	shrinkLinkTimings(t)
	const userID = "link-user"
	var mu sync.Mutex
	var upserted []string
	var projects []*db.Project
	for i := 0; i < 3; i++ {
		p := &db.Project{ID: uuid.NewString(), UserID: userID, Path: "/p/" + string(rune('a'+i)), RemoteURL: ptrTo("https://example.com/r.git")}
		projects = append(projects, p)
		paths = append(paths, p.Path)
	}
	repo := &MockRepository{
		ListProjectsFunc: func(context.Context, db.ProjectFilters) ([]*db.Project, error) { return projects, nil },
		// The machine has no checkout on record yet: every project is new.
		ListProjectDaemonsForDaemonFunc: func(context.Context, string) ([]*core.ProjectDaemon, error) { return nil, nil },
		ListProjectDaemonDetachmentsForDaemonFunc: func(context.Context, string) ([]*core.ProjectDaemonDetachment, error) {
			return nil, nil
		},
		RecordObservedProjectDaemonFunc: func(_ context.Context, _, _, path string, _ *string) (bool, error) {
			mu.Lock()
			upserted = append(upserted, path)
			mu.Unlock()
			return true, nil
		},
	}
	svc = newTestService(repo)
	t.Cleanup(svc.Close)
	ready = &readyRecorder{}
	svc.AddConnectionListener(ready)

	conn = newTestConn(userID, "laptop-"+uuid.NewString(), nil)
	conn.sendCh = make(chan *reliantv1.ServerMessage, 64)
	conn.link = newLinkState(time.Now())
	svc.mu.Lock()
	svc.connections[conn.daemonID] = conn
	svc.userDaemons[userID] = []string{conn.daemonID}
	svc.mu.Unlock()
	t.Cleanup(conn.closeDone)

	link = &fakeLink{}
	link.run(t, conn)
	upserts = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), upserted...)
	}
	return svc, conn, link, ready, upserts, paths
}

func ptrTo[T any](v T) *T { return &v }

// A daemon that re-registers while its link is dead (prod 2026-10-30: a macOS
// laptop answering nothing for a minute after "Daemon registered") gets only
// probes until the link answers: no reconcile command, no link-ready
// listener (the connected event, the pending-command drain). Once the link
// answers, the deferred work runs, once.
func TestConnectWork_DefersEverythingUntilTheLinkAnswers(t *testing.T) {
	svc, conn, link, ready, upserts, paths := linkTestService(t)
	link.setDead(true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.runConnectWork(conn, true)
	}()

	require.Eventually(t, func() bool { return len(link.received()) >= 3 }, 5*time.Second, time.Millisecond,
		"the gateway must keep probing a link that does not answer")
	for _, cmd := range link.received() {
		require.Equal(t, "!"+toolexec.LinkProbeCommand, cmd,
			"nothing but the probe may be sent into a link that has not answered")
	}
	require.Empty(t, ready.calls(), "the link-ready listeners wait for a link that answers")
	require.Empty(t, upserts())
	select {
	case <-conn.linkProven():
		t.Fatal("a link that never answered must not read as proven")
	default:
	}

	link.setDead(false)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connect work did not finish once the link answered")
	}
	require.Equal(t, []string{conn.daemonID}, ready.calls(), "link-ready fires once the probe answers")
	require.ElementsMatch(t, paths, upserts(), "the deferred reconcile ran in full")
	select {
	case <-conn.linkProven():
	default:
		t.Fatal("the answered probe proves the link")
	}
}

// A reconcile command that meets a transport failure stops the pass there —
// it does not run every later project into the same dead link — and the pass
// is retried in full once the link answers a probe again.
func TestConnectWork_ReconcileStopsAtTheFirstTransportFailureAndRetries(t *testing.T) {
	svc, conn, link, ready, upserts, paths := linkTestService(t)
	dropped := false
	link.drop = func(commandType string, payload []byte) bool {
		// The link drops once, under the second project's check.
		if commandType != "repo.discover" || dropped || !json.Valid(payload) {
			return false
		}
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(payload, &p)
		if p.Path == paths[1] {
			dropped = true
			return true
		}
		return false
	}

	svc.runConnectWork(conn, true)

	require.Equal(t, []string{
		toolexec.LinkProbeCommand,
		"repo.discover " + paths[0],
		"!repo.discover " + paths[1],
		// The pass stopped: paths[2] was never sent into the dropped link.
		toolexec.LinkProbeCommand,
		"repo.discover " + paths[0],
		"repo.discover " + paths[1],
		"repo.discover " + paths[2],
	}, link.received())
	require.Equal(t, []string{conn.daemonID}, ready.calls(), "link-ready fires once, not per pass")
	require.ElementsMatch(t, append([]string{paths[0]}, paths...), upserts())
}

// A command for a connection whose link has not answered yet is held at the
// gateway, not sent into the link. Still unproven at half its budget, it is
// answered ErrDaemonReconnecting — "your machine is reconnecting" — unsent, so
// asking again is safe. Proven while held, it is sent then, with the budget it
// has left.
func TestSendCommand_HeldUntilTheLinkAnswers(t *testing.T) {
	svc, conn, link, _, _, _ := linkTestService(t)
	link.setDead(true)

	_, err := svc.sendCommandToConn(context.Background(), conn, &reliantv1.DaemonCommandRequest{
		RequestId: uuid.NewString(), CommandType: "terminal.create", TimeoutMs: 60,
	})
	require.True(t, toolexec.IsDaemonReconnecting(err), "got %v", err)
	require.Empty(t, link.received(), "a held command is never sent into an unproven link")

	type result struct {
		resp *reliantv1.DaemonCommandResponse
		err  error
	}
	req := &reliantv1.DaemonCommandRequest{RequestId: uuid.NewString(), CommandType: "fs.mkdir", TimeoutMs: 5000}
	got := make(chan result, 1)
	go func() {
		resp, err := svc.sendCommandToConn(context.Background(), conn, req)
		got <- result{resp, err}
	}()
	// forge:lint-disable-next-line forge-test-fixed-sleep: asserting nothing happens — a command correctly held sends nothing, so there is no event to wait for
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, link.received(), "still held while the link has not answered")

	link.setDead(false)
	_, err = svc.probeLink(context.Background(), conn)
	require.NoError(t, err)
	select {
	case r := <-got:
		require.NoError(t, r.err)
		require.True(t, r.resp.GetSuccess())
	case <-time.After(5 * time.Second):
		t.Fatal("the held command was not sent once the link answered")
	}
	require.Equal(t, []string{toolexec.LinkProbeCommand, "fs.mkdir"}, link.received())
	require.Less(t, req.TimeoutMs, int32(5000), "the daemon gets the budget the hold left, not the whole of it")
}

// A connection assembled without link state (as most tests build one) reads
// as proven: nothing holds its commands.
func TestLinkProven_NilLinkStateReadsProven(t *testing.T) {
	conn := newTestConn("u", "d", nil)
	select {
	case <-conn.linkProven():
	default:
		t.Fatal("a connection without link state must not hold commands")
	}
}
