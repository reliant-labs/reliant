// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// newStoredOutbox is an outbox backed by a fresh store in dir.
func newStoredOutbox(t *testing.T, dir string) (*resultOutbox, outboxLoadReport) {
	t.Helper()
	store, err := openOutboxStore(dir)
	require.NoError(t, err)
	o := &resultOutbox{}
	return o, o.restore(store)
}

// startRunning is what a call does on arrival (start) and just before it
// runs (persistRunning).
func startRunning(o *resultOutbox, id string) {
	o.start(id)
	o.persistRunning(id)
}

func outboxFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func resultMsg(requestID, content string) *reliantv1.DaemonMessage {
	return (&daemonClient{}).toolResponse(requestID, finished(content))
}

// A finished call's result is on disk before anything can send it, a restart
// reloads it, and an acknowledgement removes the file.
func TestOutboxResultSurvivesRestartAndIsRemovedOnAck(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	first, _ := newStoredOutbox(t, dir)

	startRunning(first, "req-1")
	require.Equal(t, []string{"req-1.running"}, outboxFiles(t, dir), "a running call leaves a marker")
	first.hold("req-1", resultMsg("req-1", "built"), nil)
	require.Equal(t, []string{"req-1.result"}, outboxFiles(t, dir), "the result replaces the marker")

	second, report := newStoredOutbox(t, dir) // the restart
	require.Equal(t, 1, report.Restored)
	require.Empty(t, report.Corrupt)
	require.Equal(t, toolexec.ToolRequestCompleted, second.state("req-1"))
	held := second.matching(func(*heldResult) bool { return true })
	require.Len(t, held, 1)
	require.Equal(t, "built", held[0].msg.GetToolResponse().GetContent())
	require.Nil(t, held[0].sentOn, "a reloaded result has not been sent on any stream of this process")

	require.NotNil(t, second.ack("req-1"))
	require.Empty(t, outboxFiles(t, dir), "an acknowledged result leaves nothing on disk")

	third, report := newStoredOutbox(t, dir)
	require.Zero(t, report.Restored)
	require.Equal(t, toolexec.ToolRequestUnknown, third.state("req-1"))
}

// A call that was running when the daemon died cannot be resumed: after the
// restart it has a result of its own, so the worker is told promptly and in
// words instead of being left to wait or told "unknown".
func TestOutboxRunningCallBecomesInterruptedByRestartResult(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	first, _ := newStoredOutbox(t, dir)
	startRunning(first, "req-run")

	second, report := newStoredOutbox(t, dir)
	require.Equal(t, 1, report.Interrupted)
	require.Equal(t, toolexec.ToolRequestCompleted, second.state("req-run"))
	resp := second.matching(func(*heldResult) bool { return true })[0].msg.GetToolResponse()
	require.Equal(t, "req-run", resp.RequestId)
	require.True(t, resp.IsError)
	require.False(t, resp.Success)
	require.Equal(t, toolexec.ErrorCodeDaemonRestarted, resp.ErrorCode)
	require.Contains(t, resp.Content, "interrupted by a daemon restart")
	require.Contains(t, resp.Content, "Verify its effects")
	require.True(t, toolexec.IsTransportErrorCode(resp.ErrorCode),
		"it is not a verdict on the command: gates must retry it, not count it as a failure")

	// Its answer is itself durable: a second restart before any gateway took it
	// gives the same answer, with the marker gone.
	third, report := newStoredOutbox(t, dir)
	require.Equal(t, 1, report.Restored)
	require.Zero(t, report.Interrupted)
	require.Equal(t, []string{"req-run.result"}, outboxFiles(t, dir))
	require.Equal(t, toolexec.ToolRequestCompleted, third.state("req-run"))
}

// A call that finished, and whose marker the crash left behind, is finished.
func TestOutboxResultWinsOverALeftoverRunningMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	first, _ := newStoredOutbox(t, dir)
	startRunning(first, "req-1")
	first.hold("req-1", resultMsg("req-1", "real answer"), nil)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "req-1.running"), nil, 0o600))

	second, report := newStoredOutbox(t, dir)
	require.Zero(t, report.Interrupted)
	require.Equal(t, "real answer", second.matching(func(*heldResult) bool { return true })[0].msg.GetToolResponse().GetContent())
	require.Equal(t, []string{"req-1.result"}, outboxFiles(t, dir))
}

// The bounds hold on disk exactly as in memory: by age, and by bytes with the
// oldest going first.
func TestOutboxBoundsHoldOnDiskAndAtLoad(t *testing.T) {
	t.Run("age while running", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "outbox")
		o, _ := newStoredOutbox(t, dir)
		o.ttl = 50 * time.Millisecond
		o.hold("old", resultMsg("old", "x"), nil)
		time.Sleep(120 * time.Millisecond)
		o.hold("new", resultMsg("new", "y"), nil) // any activity sweeps
		require.Equal(t, []string{"new.result"}, outboxFiles(t, dir))
		require.Equal(t, toolexec.ToolRequestUnknown, o.state("old"))
	})

	t.Run("bytes while running", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "outbox")
		o, _ := newStoredOutbox(t, dir)
		big := strings.Repeat("a", 1000)
		o.budget = 2*messageSize(resultMsg("x", big)) + 10
		for _, id := range []string{"a", "b", "c", "d"} {
			o.hold(id, resultMsg(id, big), nil)
			time.Sleep(2 * time.Millisecond) // distinct hold times
		}
		require.Equal(t, []string{"c.result", "d.result"}, outboxFiles(t, dir), "the oldest results go first")
		require.LessOrEqual(t, o.heldBytes, o.budget)
	})

	t.Run("one result larger than the whole budget is not kept", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "outbox")
		o, _ := newStoredOutbox(t, dir)
		o.budget = 100
		o.hold("huge", resultMsg("huge", strings.Repeat("a", 5000)), nil)
		require.Empty(t, outboxFiles(t, dir))
		require.Zero(t, o.heldBytes)
	})

	t.Run("age and bytes at load", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "outbox")
		store, err := openOutboxStore(dir)
		require.NoError(t, err)
		now := time.Now()
		big := strings.Repeat("a", 1000)
		write := func(id string, age time.Duration) {
			require.NoError(t, store.putResult(id, resultMsg(id, big), now.Add(-age)))
		}
		write("expired", 40*time.Minute) // past resultHoldTTL
		write("oldest", 5*time.Minute)
		write("middle", 3*time.Minute)
		write("newest", time.Minute)
		// A stale running marker is as old as the call it records.
		stale := filepath.Join(dir, "stale.running")
		require.NoError(t, os.WriteFile(stale, nil, 0o600))
		require.NoError(t, os.Chtimes(stale, now.Add(-time.Hour), now.Add(-time.Hour)))

		o := &resultOutbox{budget: 2*messageSize(resultMsg("x", big)) + 10}
		report := o.restore(store)
		require.Equal(t, 2, report.Expired, "the old result and the stale marker")
		require.Equal(t, 1, report.OverBudget)
		require.Equal(t, 2, report.Restored)
		require.Equal(t, []string{"middle.result", "newest.result"}, outboxFiles(t, dir))
	})

	t.Run("the production bounds are the documented ones", func(t *testing.T) {
		o := &resultOutbox{}
		require.Equal(t, 30*time.Minute, o.holdTTL())
		require.Equal(t, 64<<20, o.holdBudget())
	})
}

// A file the daemon cannot read back is skipped, reported by name and removed;
// the rest of the outbox loads, and nothing stops the daemon starting.
func TestOutboxToleratesCorruptFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	store, err := openOutboxStore(dir)
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, store.putResult("good", resultMsg("good", "kept"), now))
	require.NoError(t, store.putResult("trunc", resultMsg("trunc", strings.Repeat("a", 500)), now))
	require.NoError(t, store.putResult("flip", resultMsg("flip", strings.Repeat("b", 500)), now))
	require.NoError(t, store.putResult("other", resultMsg("other", "belongs elsewhere"), now))

	path := func(id string) string { return filepath.Join(dir, id+".result") }
	data, err := os.ReadFile(path("trunc"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path("trunc"), data[:len(data)/2], 0o600)) // a full disk, a torn write

	data, err = os.ReadFile(path("flip"))
	require.NoError(t, err)
	data[len(data)-3] ^= 0xff // bit rot in the payload
	require.NoError(t, os.WriteFile(path("flip"), data, 0o600))

	data, err = os.ReadFile(path("other"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path("renamed"), data, 0o600)) // a result filed under another request's id
	require.NoError(t, os.Remove(path("other")))

	require.NoError(t, os.WriteFile(path("garbage"), []byte("not an outbox file at all, but long enough to have a header"), 0o600))
	require.NoError(t, os.WriteFile(path("empty"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "half.result.tmp-123"), []byte("partial"), 0o600)) // a write that never renamed
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("stray"), 0o600))

	o := &resultOutbox{}
	report := o.restore(store)
	require.Equal(t, 1, report.Restored)
	sort.Strings(report.Corrupt)
	require.Equal(t, []string{"empty.result", "flip.result", "garbage.result", "renamed.result", "trunc.result"}, report.Corrupt,
		"every unreadable file is reported, by name only")
	require.Equal(t, toolexec.ToolRequestCompleted, o.state("good"))
	for _, id := range []string{"trunc", "flip", "renamed", "garbage", "empty"} {
		require.Equal(t, toolexec.ToolRequestUnknown, o.state(id), id)
	}
	require.Equal(t, []string{"README", "good.result"}, outboxFiles(t, dir),
		"corrupt files and torn writes are cleaned up; unrelated files are left alone")
}

// A request id comes off the wire. It can only ever name a file inside the
// outbox directory, and an id that cannot is simply not persisted.
func TestOutboxRefusesRequestIDsThatAreNotFileNames(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "outbox")
	o, _ := newStoredOutbox(t, dir)
	for _, id := range []string{"../escape", "a/b", `a\b`, "..", "a.b", "a b", "x\x00y", strings.Repeat("a", 200)} {
		startRunning(o, id)
		o.hold(id, resultMsg(id, "secret"), nil)
		o.ack(id)
	}
	require.Empty(t, outboxFiles(t, dir))
	require.Empty(t, outboxFiles(t, root)[1:], "nothing was written beside the outbox either")
}

// Results can hold file contents and command output.
func TestOutboxFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := filepath.Join(t.TempDir(), "outbox")
	require.NoError(t, os.MkdirAll(dir, 0o755)) // left wide by something else
	o, _ := newStoredOutbox(t, dir)
	startRunning(o, "run")
	o.hold("req-1", resultMsg("req-1", "token=hunter2"), nil)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "the directory is tightened to 0700")
	for _, name := range outboxFiles(t, dir) {
		info, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), name)
	}
}

// The write is atomic: nothing is ever visible under the result's name except
// a whole file, and no temp file outlives a write.
func TestOutboxWriteLeavesNoTempFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	o, _ := newStoredOutbox(t, dir)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("req-%d", i)
			startRunning(o, id)
			o.hold(id, resultMsg(id, strings.Repeat("z", 10_000)), nil)
		}()
	}
	wg.Wait()
	for _, name := range outboxFiles(t, dir) {
		require.True(t, strings.HasSuffix(name, ".result"), "unexpected file %s", name)
	}
	require.Len(t, outboxFiles(t, dir), 20)

	// Overwriting the same id (a resend of a re-run) replaces, never appends.
	o.hold("req-0", resultMsg("req-0", "second"), nil)
	again, report := newStoredOutbox(t, dir)
	require.Equal(t, 20, report.Restored)
	for _, h := range again.matching(func(h *heldResult) bool { return h.requestID == "req-0" }) {
		require.Equal(t, "second", h.msg.GetToolResponse().GetContent())
	}
}

// A daemon with no data directory keeps the in-memory outbox it always had.
func TestOutboxWithoutAStoreIsInMemory(t *testing.T) {
	var o resultOutbox
	o.start("req-1")
	o.hold("req-1", resultMsg("req-1", "ok"), nil)
	require.Equal(t, toolexec.ToolRequestCompleted, o.state("req-1"))
	require.NotNil(t, o.ack("req-1"))

	d := newTestDaemonClient("d", "u")
	d.restoreResultOutbox()
	require.Nil(t, d.results.store, "no data dir, no store")
}

// outboxGateway is a fake gateway for the restart tests. Each stream: after
// registration it sends the requests it was given, then asks tool.status about
// every id in statusFor, and records the tool responses and status answers it
// gets. It acknowledges tool responses only when ack is set.
type outboxGateway struct {
	reliantv1connect.UnimplementedToolsDaemonServiceHandler
	requests  []*reliantv1.ToolRequest
	statusFor []string
	ack       bool

	mu        sync.Mutex
	responses map[string]*reliantv1.ToolResponse
	states    map[string]toolexec.ToolRequestState
	arrived   chan string
}

func newOutboxGateway() *outboxGateway {
	return &outboxGateway{
		responses: make(map[string]*reliantv1.ToolResponse),
		states:    make(map[string]toolexec.ToolRequestState),
		arrived:   make(chan string, 64),
	}
}

func (g *outboxGateway) response(id string) *reliantv1.ToolResponse {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.responses[id]
}

func (g *outboxGateway) state(id string) toolexec.ToolRequestState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.states[id]
}

func (g *outboxGateway) ConnectDaemon(ctx context.Context, stream *connect.BidiStream[reliantv1.DaemonMessage, reliantv1.ServerMessage]) error {
	if _, err := stream.Receive(); err != nil {
		return err
	}
	send := func(m *reliantv1.ServerMessage) error { return stream.Send(m) }
	if err := send(&reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_RegistrationAck{
		RegistrationAck: &reliantv1.RegistrationAck{Accepted: true, DaemonId: "daemon-1", UserId: "user-1"},
	}}); err != nil {
		return err
	}
	for _, r := range g.requests {
		if err := send(&reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_ToolRequest{ToolRequest: r}}); err != nil {
			return err
		}
	}
	for _, id := range g.statusFor {
		payload, _ := json.Marshal(toolexec.ToolStatusRequest{RequestID: id})
		if err := send(&reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_DaemonCommand{
			DaemonCommand: &reliantv1.DaemonCommandRequest{RequestId: "status-" + id, CommandType: toolexec.ToolStatusCommand, Payload: payload},
		}}); err != nil {
			return err
		}
	}
	for {
		msg, err := stream.Receive()
		if err != nil {
			return err
		}
		if r := msg.GetToolResponse(); r != nil {
			g.mu.Lock()
			g.responses[r.RequestId] = r
			g.mu.Unlock()
			select {
			case g.arrived <- r.RequestId:
			default:
			}
			if g.ack {
				if err := send(&reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_ToolResultAck{
					ToolResultAck: &reliantv1.ToolResultAck{RequestId: r.RequestId},
				}}); err != nil {
					return err
				}
			}
		}
		if c := msg.GetDaemonCommandResponse(); c != nil && strings.HasPrefix(c.RequestId, "status-") {
			var st toolexec.ToolStatusResponse
			if json.Unmarshal(c.Payload, &st) == nil {
				g.mu.Lock()
				g.states[strings.TrimPrefix(c.RequestId, "status-")] = st.State
				g.mu.Unlock()
			}
		}
	}
}

func waitForResponse(t *testing.T, g *outboxGateway, id string) *reliantv1.ToolResponse {
	t.Helper()
	require.Eventually(t, func() bool { return g.response(id) != nil }, 20*time.Second, 10*time.Millisecond,
		"the gateway never received the result of %s", id)
	return g.response(id)
}

// copyDir copies the regular files of src into a new directory: the disk as a
// SIGKILL left it, before the process could run any shutdown code.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dst, 0o700))
	for _, name := range outboxFiles(t, src) {
		data, err := os.ReadFile(filepath.Join(src, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dst, name), data, 0o600))
	}
}

// The whole thing, against real streams: a daemon finishes a tool call whose
// result no gateway acknowledges, and is killed. The restarted daemon answers
// tool.status "completed" and delivers the recorded result on its first
// stream, where the gateway acknowledges it and the file goes away.
func TestKilledDaemonsFinishedResultIsDeliveredByItsSuccessor(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs a real shell command over real streams; runs in task test")
	}
	killedDir := t.TempDir()
	g1 := newOutboxGateway()
	g1.requests = []*reliantv1.ToolRequest{slowShellRequest(t, "req-done", t.TempDir(), "echo survived-the-restart")}
	first := newDaemonClientForTest(t, killedDir, startGateway(t, g1))
	startDaemon(t, first)

	got := waitForResponse(t, g1, "req-done")
	require.Contains(t, got.Content, "survived-the-restart")
	require.Equal(t, toolexec.ToolRequestCompleted, first.results.state("req-done"), "no gateway acknowledged it")

	// kill -9: the successor starts from a copy of what is on disk, with a
	// brand-new process state.
	survivor := t.TempDir()
	copyDir(t, filepath.Join(killedDir, outboxDirName), filepath.Join(survivor, outboxDirName))

	g2 := newOutboxGateway()
	g2.ack = true
	g2.statusFor = []string{"req-done"}
	second := newDaemonClientForTest(t, survivor, startGateway(t, g2))
	require.Equal(t, toolexec.ToolRequestCompleted, second.results.state("req-done"),
		"the restarted daemon still knows the call, before it has even connected")
	startDaemon(t, second)

	redelivered := waitForResponse(t, g2, "req-done")
	require.Equal(t, got.Content, redelivered.Content, "the recorded result is delivered byte for byte")
	require.True(t, redelivered.Success)
	require.Eventually(t, func() bool { return g2.state("req-done") != "" }, 20*time.Second, 10*time.Millisecond)
	require.Contains(t, []toolexec.ToolRequestState{toolexec.ToolRequestCompleted, toolexec.ToolRequestDelivered}, g2.state("req-done"),
		"tool.status knows the call (completed, or delivered if the gateway's ack won the race), not unknown")
	require.Eventually(t, func() bool { return second.results.state("req-done") == toolexec.ToolRequestDelivered },
		20*time.Second, 10*time.Millisecond, "the acknowledgement is processed")
	require.Empty(t, outboxFiles(t, filepath.Join(survivor, outboxDirName)), "an acknowledged result is cleaned up on disk")
}

// A call that was running when the daemon was killed gets its "interrupted by
// daemon restart" result from the successor, promptly, instead of hanging or
// coming back unknown.
func TestKilledDaemonsRunningCallIsAnsweredAsInterrupted(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs a real shell command over real streams; runs in task test")
	}
	killedDir := t.TempDir()
	g1 := newOutboxGateway()
	g1.requests = []*reliantv1.ToolRequest{slowShellRequest(t, "req-long", t.TempDir(), "sleep 60")}
	first := newDaemonClientForTest(t, killedDir, startGateway(t, g1))
	startDaemon(t, first)

	require.Eventually(t, func() bool { return first.results.state("req-long") == toolexec.ToolRequestRunning },
		20*time.Second, 5*time.Millisecond)
	// The call is running in memory as soon as the receive loop takes it; its
	// marker is written by the goroutine that executes it, just before the
	// tool runs (persistRunning: the receive loop never waits on a disk). So
	// the marker follows the state, and is what the restart below needs.
	require.Eventually(t, func() bool {
		return slices.Equal([]string{"req-long.running"}, outboxFiles(t, filepath.Join(killedDir, outboxDirName)))
	}, 20*time.Second, 5*time.Millisecond, "the running call's marker is on disk: %v", outboxFiles(t, filepath.Join(killedDir, outboxDirName)))

	survivor := t.TempDir()
	copyDir(t, filepath.Join(killedDir, outboxDirName), filepath.Join(survivor, outboxDirName))

	g2 := newOutboxGateway()
	g2.ack = true
	g2.statusFor = []string{"req-long", "req-never-seen"}
	second := newDaemonClientForTest(t, survivor, startGateway(t, g2))
	startDaemon(t, second)

	resp := waitForResponse(t, g2, "req-long")
	require.True(t, resp.IsError)
	require.Equal(t, toolexec.ErrorCodeDaemonRestarted, resp.ErrorCode)
	require.Contains(t, resp.Content, "interrupted by a daemon restart")
	require.Eventually(t, func() bool { return g2.state("req-long") != "" && g2.state("req-never-seen") != "" },
		20*time.Second, 10*time.Millisecond)
	require.Contains(t, []toolexec.ToolRequestState{toolexec.ToolRequestCompleted, toolexec.ToolRequestDelivered}, g2.state("req-long"),
		"the restarted daemon knows the interrupted call")
	assert.Equal(t, toolexec.ToolRequestUnknown, g2.state("req-never-seen"),
		"a request the daemon never saw is still unknown")
}

// A daemon whose outbox directory is wrecked still starts and serves.
func TestDaemonStartsWithAnUnusableOutboxDirectory(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, outboxDirName), []byte("a file where the directory should be"), 0o600))
	d := newDaemonClientForTest(t, dataDir, "http://127.0.0.1:1")
	require.Nil(t, d.results.store)
	d.results.start("req-1")
	d.results.hold("req-1", resultMsg("req-1", "ok"), nil)
	require.Equal(t, toolexec.ToolRequestCompleted, d.results.state("req-1"))
}
