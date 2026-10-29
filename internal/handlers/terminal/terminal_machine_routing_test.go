package terminal

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// The owner's report: a branch workspace lives on one machine, but its
// terminal opened on the user's default machine, which has no such folder,
// and said "Its workspace was archived or removed from the machine". Every
// command a terminal sends must reach the machine that holds the workspace.

// routingDefaultDaemon is what default resolution reaches: NOT the
// workspace's machine.
const routingDefaultDaemon = "daemon-default"

// machineRecordingRouter records the machine every daemon command reached;
// a command left to default resolution is recorded as routingDefaultDaemon.
type machineRecordingRouter struct {
	toolexectest.Recorder

	mu      sync.Mutex
	reached map[string][]string
}

var _ toolexec.DaemonRouter = (*machineRecordingRouter)(nil)

func (r *machineRecordingRouter) record(daemonID, commandType string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reached == nil {
		r.reached = map[string][]string{}
	}
	r.reached[commandType] = append(r.reached[commandType], daemonID)
	if commandType == "terminal.create" {
		return json.Marshal(map[string]any{"session_id": "sess-1", "pid": 42})
	}
	return json.Marshal(map[string]any{"success": true})
}

func (r *machineRecordingRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, _ []byte, _ int32) ([]byte, error) {
	return r.record(routingDefaultDaemon, commandType)
}

func (r *machineRecordingRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, _ []byte, _ int32) ([]byte, error) {
	return r.record(daemonID, commandType)
}

func (r *machineRecordingRouter) SubscribeTerminalOutput(ctx context.Context, _, _ string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	// Output follow-ups resolve their machine from the context's selector
	// (resolveFollowUpDaemonID), so that is what is recorded for them.
	target := routingDefaultDaemon
	if sel := toolexec.DaemonSelectorFromContext(ctx); sel != nil {
		target = sel.ID
	}
	_, _ = r.record(target, "terminal.output")
	return make(chan *toolexec.TerminalOutputEvent), func() {}, nil
}

func (r *machineRecordingRouter) machinesFor(commandType string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reached[commandType]...)
}

// openTerminal dials the terminal websocket with query, returns the first
// message, then hangs up and waits for the session's close to be sent.
func openTerminal(t *testing.T, router *machineRecordingRouter, checkouts terminalCheckouts, query url.Values) map[string]any {
	t.Helper()
	srv := httptest.NewServer(TerminalWSHandler(router, staticTokenValidator{userID: terminalTestUserID}, WithTerminalCheckouts(checkouts)))
	t.Cleanup(srv.Close)

	query.Set("token", "test")
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?" + query.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var first map[string]any
	require.NoError(t, conn.ReadJSON(&first))
	_ = conn.Close()
	if first["type"] == "init" {
		require.Eventually(t, func() bool { return len(router.machinesFor("terminal.close")) > 0 },
			5*time.Second, 10*time.Millisecond, "the session was never closed")
	}
	return first
}

func routingCheckouts() fakeTerminalCheckouts {
	ownerB := "daemon-b"
	pinnedC := "daemon-c"
	theirs := "their-machine"
	active := int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	mainID := "wt-main"
	branchID := "wt-branch"
	return fakeTerminalCheckouts{
		worktrees: map[string]*db.Worktree{
			mainID:      {ID: mainID, ProjectID: "p1", IsMain: true, Status: active},
			branchID:    {ID: branchID, ProjectID: "p1", DaemonID: &ownerB, Status: active},
			"wt-theirs": {ID: "wt-theirs", ProjectID: "p-theirs", DaemonID: &theirs, Status: active},
		},
		projectOwners: map[string]string{"p-theirs": "someone-else"},
		chats: map[string]*db.Chat{
			"chat-on-c":   {ID: "chat-on-c", UserID: terminalTestUserID, ProjectID: "p1", ActiveDaemonID: &pinnedC, WorktreeID: &mainID},
			"chat-other":  {ID: "chat-other", UserID: "someone-else", ProjectID: "p1", ActiveDaemonID: &pinnedC},
			"chat-branch": {ID: "chat-branch", UserID: terminalTestUserID, ProjectID: "p1", ActiveDaemonID: &pinnedC, WorktreeID: &branchID},
		},
	}
}

func TestTerminalWS_RoutesToTheWorkspaceMachine(t *testing.T) {
	cases := []struct {
		name  string
		query url.Values
		want  string
	}{
		{
			// The reported bug: the branch workspace is on B, default is elsewhere.
			name:  "owned worktree opens on its owner",
			query: url.Values{"worktreeId": {"wt-branch"}, "projectId": {"p1"}},
			want:  "daemon-b",
		},
		{
			// A chat pinned elsewhere (a row from before StartChat refused it)
			// does not move the terminal off the only disk with the checkout.
			name:  "owned worktree beats a mismatched chat pin",
			query: url.Values{"worktreeId": {"wt-branch"}, "projectId": {"p1"}, "chatId": {"chat-branch"}},
			want:  "daemon-b",
		},
		{
			name:  "main checkout opens on the chat's machine",
			query: url.Values{"worktreeId": {"wt-main"}, "projectId": {"p1"}, "chatId": {"chat-on-c"}},
			want:  "daemon-c",
		},
		{
			name:  "main checkout without a chat keeps default resolution",
			query: url.Values{"worktreeId": {"wt-main"}, "projectId": {"p1"}},
			want:  routingDefaultDaemon,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := &machineRecordingRouter{}
			first := openTerminal(t, router, routingCheckouts(), tc.query)

			require.Equal(t, "init", first["type"])
			for _, cmd := range []string{"terminal.create", "terminal.output", "terminal.close"} {
				got := router.machinesFor(cmd)
				require.NotEmpty(t, got, "%s was never sent", cmd)
				for _, d := range got {
					require.Equal(t, tc.want, d, "%s went to the wrong machine", cmd)
				}
			}
			if tc.want == routingDefaultDaemon {
				require.Nil(t, first["daemon_id"], "default resolution names no machine")
			} else {
				require.Equal(t, tc.want, first["daemon_id"], "init must name the machine, so the browser closes the session there")
			}
		})
	}
}

// Someone else's chat is not a way to aim a terminal.
func TestTerminalWS_RefusesAnotherUsersChat(t *testing.T) {
	router := &machineRecordingRouter{}
	first := openTerminal(t, router, routingCheckouts(), url.Values{"worktreeId": {"wt-main"}, "projectId": {"p1"}, "chatId": {"chat-other"}})

	require.Equal(t, "error", first["type"])
	require.Empty(t, router.machinesFor("terminal.create"), "nothing may be sent for another user's chat")
}

// Another tenant's owned worktree is not found: nothing is sent and no reply
// names their machine, whichever project the request claims.
func TestTerminalWS_RefusesAnotherUsersWorktree(t *testing.T) {
	for _, project := range []string{"p-theirs", "p1"} {
		router := &machineRecordingRouter{}
		first := openTerminal(t, router, routingCheckouts(), url.Values{"worktreeId": {"wt-theirs"}, "projectId": {project}})

		require.Equal(t, "error", first["type"], project)
		require.Empty(t, router.machinesFor("terminal.create"), "nothing may be sent for another user's worktree")
		require.Nil(t, first["daemon_id"], "their machine is never named")
		for _, v := range first {
			if s, ok := v.(string); ok {
				require.NotContains(t, s, "their-machine")
			}
		}
	}
}

// A machine-state error names the machine the terminal was sent to, so the
// browser can say which one is not up.
func TestTerminalWS_WorkingDirErrorNamesTheMachine(t *testing.T) {
	router := &terminalCreateRouter{createErr: errMissingWorkingDirTransport}
	checkouts := routingCheckouts()
	srv := httptest.NewServer(TerminalWSHandler(&pinnedCreateRouter{terminalCreateRouter: router}, staticTokenValidator{userID: terminalTestUserID}, WithTerminalCheckouts(checkouts)))
	t.Cleanup(srv.Close)

	query := url.Values{"token": {"test"}, "workingDir": {projectWorkingDir}, "worktreeId": {"wt-branch"}, "projectId": {"p1"}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/?"+query.Encode(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var first map[string]any
	require.NoError(t, conn.ReadJSON(&first))

	require.Equal(t, "error", first["type"])
	require.Equal(t, "working_dir_missing", first["code"])
	require.Equal(t, "daemon-b", first["daemon_id"])
}

// pinnedCreateRouter answers a pinned terminal.create like an unpinned one.
type pinnedCreateRouter struct {
	*terminalCreateRouter
}

func (r *pinnedCreateRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}
