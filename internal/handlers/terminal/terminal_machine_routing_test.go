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
	"github.com/reliant-labs/reliant/internal/db/core"
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
	// createDirs are the working directories terminal.create was sent.
	createDirs []string
}

var _ toolexec.DaemonRouter = (*machineRecordingRouter)(nil)

func (r *machineRecordingRouter) record(daemonID, commandType string, payload []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reached == nil {
		r.reached = map[string][]string{}
	}
	r.reached[commandType] = append(r.reached[commandType], daemonID)
	if commandType == "terminal.create" {
		var req struct {
			WorkingDir string `json:"working_dir"`
		}
		_ = json.Unmarshal(payload, &req)
		r.createDirs = append(r.createDirs, req.WorkingDir)
		return json.Marshal(map[string]any{"session_id": "sess-1", "pid": 42})
	}
	return json.Marshal(map[string]any{"success": true})
}

func (r *machineRecordingRouter) SendDaemonCommand(_ context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	return r.record(routingDefaultDaemon, commandType, payload)
}

func (r *machineRecordingRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	return r.record(daemonID, commandType, payload)
}

// ResolveDaemonID is default resolution: the account's arbitrary machine.
func (r *machineRecordingRouter) ResolveDaemonID(context.Context, string) (string, error) {
	return routingDefaultDaemon, nil
}

// PlacementDaemonID is daemonplacement.Machines: default resolution for "",
// and every named machine is up.
func (r *machineRecordingRouter) PlacementDaemonID(_ context.Context, _, daemonID string) (string, error) {
	if daemonID == "" {
		return routingDefaultDaemon, nil
	}
	return daemonID, nil
}

func (r *machineRecordingRouter) workingDirs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.createDirs...)
}

func (r *machineRecordingRouter) SubscribeTerminalOutput(ctx context.Context, _, _ string) (<-chan *toolexec.TerminalOutputEvent, func(), error) {
	// Output follow-ups resolve their machine from the context's selector
	// (resolveFollowUpDaemonID), so that is what is recorded for them.
	target := routingDefaultDaemon
	if sel := toolexec.DaemonSelectorFromContext(ctx); sel != nil {
		target = sel.ID
	}
	_, _ = r.record(target, "terminal.output", nil)
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

// The project p1 was created at routingProjectPath (projects.path). It is
// checked out on daemon-c at that same path and on daemon-d at another; it is
// cloning onto daemon-f; daemon-e and the default machine have none of it.
const (
	routingProjectPath = "/home/workspace/projects/p1"
	routingPathOnD     = "/srv/d/p1"
)

func routingCheckouts() fakeTerminalCheckouts {
	ownerB := "daemon-b"
	pinnedC := "daemon-c"
	pinnedD := "daemon-d"
	pinnedE := "daemon-e"
	pinnedF := "daemon-f"
	theirs := "their-machine"
	active := int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	mainID := "wt-main"
	branchID := "wt-branch"
	return fakeTerminalCheckouts{
		worktrees: map[string]*db.Worktree{
			mainID:      {ID: mainID, ProjectID: "p1", IsMain: true, Status: active, Path: routingProjectPath},
			branchID:    {ID: branchID, ProjectID: "p1", DaemonID: &ownerB, Status: active},
			"wt-theirs": {ID: "wt-theirs", ProjectID: "p-theirs", DaemonID: &theirs, Status: active},
		},
		projectOwners: map[string]string{"p-theirs": "someone-else"},
		projectPaths:  map[string]string{"p1": routingProjectPath},
		projectDaemons: map[string][]*db.ProjectDaemon{
			"p1": {
				{ProjectID: "p1", DaemonID: "daemon-c", Path: routingProjectPath, InstallState: core.ProjectInstallInstalled},
				{ProjectID: "p1", DaemonID: "daemon-d", Path: routingPathOnD, InstallState: core.ProjectInstallInstalled},
				{ProjectID: "p1", DaemonID: "daemon-f", Path: routingProjectPath, InstallState: core.ProjectInstallInstalling},
			},
		},
		daemons: map[string]*db.Daemon{
			"daemon-c": {ID: "daemon-c", UserID: terminalTestUserID, Name: "cicd"},
			"daemon-d": {ID: "daemon-d", UserID: terminalTestUserID, Name: "default"},
			"daemon-e": {ID: "daemon-e", UserID: terminalTestUserID, Name: "cloud-2xl"},
			"daemon-f": {ID: "daemon-f", UserID: terminalTestUserID, Name: "fresh"},
		},
		chats: map[string]*db.Chat{
			"chat-on-c":   {ID: "chat-on-c", UserID: terminalTestUserID, ProjectID: "p1", ActiveDaemonID: &pinnedC, WorktreeID: &mainID},
			"chat-on-d":   {ID: "chat-on-d", UserID: terminalTestUserID, ProjectID: "p1", ActiveDaemonID: &pinnedD, WorktreeID: &mainID},
			"chat-on-e":   {ID: "chat-on-e", UserID: terminalTestUserID, ProjectID: "p1", ActiveDaemonID: &pinnedE, WorktreeID: &mainID},
			"chat-on-f":   {ID: "chat-on-f", UserID: terminalTestUserID, ProjectID: "p1", ActiveDaemonID: &pinnedF, WorktreeID: &mainID},
			"chat-other":  {ID: "chat-other", UserID: "someone-else", ProjectID: "p1", ActiveDaemonID: &pinnedC},
			"chat-branch": {ID: "chat-branch", UserID: terminalTestUserID, ProjectID: "p1", ActiveDaemonID: &pinnedC, WorktreeID: &branchID},
		},
	}
}

func TestTerminalWS_RoutesToTheWorkspaceMachine(t *testing.T) {
	cases := []struct {
		name     string
		query    url.Values
		want     string
		wantName string
		// wantDir is the directory terminal.create carries.
		wantDir string
	}{
		{
			// The reported bug: the branch workspace is on B, default is elsewhere.
			name:     "owned worktree opens on its owner",
			query:    url.Values{"worktreeId": {"wt-branch"}, "projectId": {"p1"}, "workingDir": {"/wt/branch"}},
			want:     "daemon-b",
			wantName: "self-hosted machine daemon-b",
			wantDir:  "/wt/branch",
		},
		{
			// A chat pinned elsewhere (a row from before StartChat refused it)
			// does not move the terminal off the only disk with the checkout.
			name:     "owned worktree beats a mismatched chat pin",
			query:    url.Values{"worktreeId": {"wt-branch"}, "projectId": {"p1"}, "chatId": {"chat-branch"}, "workingDir": {"/wt/branch"}},
			want:     "daemon-b",
			wantName: "self-hosted machine daemon-b",
			wantDir:  "/wt/branch",
		},
		{
			name:     "main checkout opens on the chat's machine",
			query:    url.Values{"worktreeId": {"wt-main"}, "projectId": {"p1"}, "chatId": {"chat-on-c"}, "workingDir": {routingProjectPath}},
			want:     "daemon-c",
			wantName: "cicd",
			wantDir:  routingProjectPath,
		},
		{
			// The browser sends projects.path; on a machine whose checkout is
			// elsewhere the shell starts in THAT machine's checkout.
			name:     "main checkout opens in the chat machine's own checkout path",
			query:    url.Values{"worktreeId": {"wt-main"}, "projectId": {"p1"}, "chatId": {"chat-on-d"}, "workingDir": {routingProjectPath + "/sub"}},
			want:     "daemon-d",
			wantName: "default",
			wantDir:  routingPathOnD + "/sub",
		},
		{
			// The prod report: a terminal with no chat landed on whichever of
			// the user's machines default resolution named — here one with no
			// checkout at all. It opens where the project is checked out.
			name:     "main checkout without a chat opens on a machine holding the checkout",
			query:    url.Values{"worktreeId": {"wt-main"}, "projectId": {"p1"}, "workingDir": {routingProjectPath}},
			want:     "daemon-c",
			wantName: "cicd",
			wantDir:  routingProjectPath,
		},
		{
			// No project to place by: default resolution, pinned and named.
			name:     "no project is pinned to default resolution",
			query:    url.Values{"workingDir": {"/tmp"}},
			want:     routingDefaultDaemon,
			wantName: "self-hosted machine daemon-d",
			wantDir:  "/tmp",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := &machineRecordingRouter{}
			first := openTerminal(t, router, routingCheckouts(), tc.query)

			require.Equal(t, "init", first["type"], "%v", first)
			for _, cmd := range []string{"terminal.create", "terminal.output", "terminal.close"} {
				got := router.machinesFor(cmd)
				require.NotEmpty(t, got, "%s was never sent", cmd)
				for _, d := range got {
					require.Equal(t, tc.want, d, "%s went to the wrong machine", cmd)
				}
			}
			require.Equal(t, tc.want, first["daemon_id"], "init must name the machine: the browser shows it and closes the session there")
			require.Equal(t, tc.wantName, first["machine_name"])
			require.Equal(t, []string{tc.wantDir}, router.workingDirs())
		})
	}
}

// A chat whose machine has no checkout of the project: nothing is sent to
// that machine, and the browser is told — with the machine named, and the
// machines that do have it — instead of a shell opening in a directory that
// is not there.
func TestTerminalWS_ChatMachineWithoutTheCheckoutIsRefusedByName(t *testing.T) {
	router := &machineRecordingRouter{}
	first := openTerminal(t, router, routingCheckouts(),
		url.Values{"worktreeId": {"wt-main"}, "projectId": {"p1"}, "chatId": {"chat-on-e"}, "workingDir": {routingProjectPath}})

	require.Equal(t, "error", first["type"])
	require.Equal(t, wsErrorCheckoutMissing, first["code"], "retrying cannot put a checkout there")
	require.Equal(t, "daemon-e", first["daemon_id"])
	require.Equal(t, "cloud-2xl", first["machine_name"])
	require.Contains(t, first["data"], `isn't checked out on "cloud-2xl"`)
	require.Contains(t, first["data"], `"cicd"`, "the way out names a machine that has it")
	require.Empty(t, router.machinesFor("terminal.create"), "nothing may be sent to a machine without the checkout")
}

// A clone still landing on the chat's machine will create the directory: the
// browser waits for it rather than giving up.
func TestTerminalWS_ChatMachineStillCloningWaits(t *testing.T) {
	router := &machineRecordingRouter{}
	first := openTerminal(t, router, routingCheckouts(),
		url.Values{"worktreeId": {"wt-main"}, "projectId": {"p1"}, "chatId": {"chat-on-f"}, "workingDir": {routingProjectPath}})

	require.Equal(t, "error", first["type"])
	require.Equal(t, wsErrorWorkingDirUnavailable, first["code"])
	require.Equal(t, "daemon-f", first["daemon_id"])
	require.Contains(t, first["data"], `still being cloned onto "fresh"`)
	require.Empty(t, router.machinesFor("terminal.create"))
}

// slowCreateRouter is a connected machine that never answers terminal.create:
// it waits until the caller gives up.
type slowCreateRouter struct {
	machineRecordingRouter
}

func (r *slowCreateRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if commandType == "terminal.create" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.machineRecordingRouter.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payload, timeoutMs)
}

// A saturated machine (load 220 in prod) that never answers must end in an
// answer the browser can show — which machine, and that it did not answer —
// within the bound, not a spinner.
func TestTerminalWS_CreateIsBoundedAndNamesTheMachineThatDidNotAnswer(t *testing.T) {
	freshTerminalMachineStateLog(t)
	previous := terminalCreateTimeout
	terminalCreateTimeout = 50 * time.Millisecond
	t.Cleanup(func() { terminalCreateTimeout = previous })

	router := &slowCreateRouter{}
	srv := httptest.NewServer(TerminalWSHandler(router, staticTokenValidator{userID: terminalTestUserID}, WithTerminalCheckouts(routingCheckouts())))
	t.Cleanup(srv.Close)
	query := url.Values{"token": {"test"}, "worktreeId": {"wt-main"}, "projectId": {"p1"}, "chatId": {"chat-on-c"}, "workingDir": {routingProjectPath}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/?"+query.Encode(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	// The read deadline is only a hang guard: the reply arrives once the 50ms
	// create bound fires, so the assertions below need no clock of their own.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var reply map[string]any
	require.NoError(t, conn.ReadJSON(&reply))

	require.Equal(t, "error", reply["type"])
	require.Equal(t, wsErrorMachineNotAnswering, reply["code"])
	require.Equal(t, "daemon-c", reply["daemon_id"])
	require.Equal(t, "cicd", reply["machine_name"])
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
	require.Equal(t, "self-hosted machine daemon-b", first["machine_name"], "the browser says where the folder was looked for")
}

// pinnedCreateRouter answers a pinned terminal.create like an unpinned one.
type pinnedCreateRouter struct {
	*terminalCreateRouter
}

func (r *pinnedCreateRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

// A machine name is the owner's own words: a daemon id that is not the
// caller's is named by its short id, never by the name its owner gave it.
func TestTerminalWS_MachineNameNeverNamesAnotherUsersMachine(t *testing.T) {
	checkouts := routingCheckouts()
	checkouts.daemons["their-machine-id"] = &db.Daemon{ID: "their-machine-id", UserID: "someone-else", Name: "their secret laptop"}
	cfg := terminalWSConfig{checkouts: checkouts}

	require.Equal(t, "cicd", cfg.machineName(context.Background(), terminalTestUserID, "daemon-c"))
	name := cfg.machineName(context.Background(), terminalTestUserID, "their-machine-id")
	require.NotContains(t, name, "their secret laptop")
	require.Equal(t, "machine their-ma", name)
}
