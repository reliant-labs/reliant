// Copyright (c) 2025 Reliant Labs
package package_commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// Package commands used to run on the api-server's own shell. They run on the
// machine holding the workspace, and every process rpc asks that machine.

const routedDefault = "daemon-default"

// sentCommand is one daemon command and the machine it reached.
type sentCommand struct {
	machine string
	command string
	payload []byte
}

// machineRouter answers daemon commands per machine and records where each
// went. A command left to default resolution reaches routedDefault.
type machineRouter struct {
	toolexectest.Stub

	mu     sync.Mutex
	sent   []sentCommand
	online map[string]bool
	listed map[string][]*daemon.ProcessInfo
	lines  []daemon.ProcessOutputLine
}

func (r *machineRouter) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommandToDaemon(ctx, userID, routedDefault, commandType, payload, timeoutMs)
}

func (r *machineRouter) SendDaemonCommandToDaemon(_ context.Context, _, machine, commandType string, payload []byte, _ int32) ([]byte, error) {
	r.mu.Lock()
	r.sent = append(r.sent, sentCommand{machine: machine, command: commandType, payload: payload})
	r.mu.Unlock()
	switch commandType {
	case "pkg.list_commands":
		var req pkgListCommandsCmd
		_ = json.Unmarshal(payload, &req)
		return json.Marshal(pkgmgr.CommandListResponse{Commands: map[pkgmgr.PackageType][]pkgmgr.Command{
			pkgmgr.PackageTypeNPM: {
				{Name: "build", Command: "npm run build", PackageType: pkgmgr.PackageTypeNPM, WorkingDir: req.WorkingDir},
				{Name: "build", Command: "npm run build --workspace web", PackageType: pkgmgr.PackageTypeNPM, RelativePath: "web"},
			},
		}})
	case "exec.bg_start":
		// The daemon keeps what it was asked to tag the process with, and
		// exec.bg_list reports it (cmd_exec.handleExecBGList).
		var req daemon.RunCommandRequest
		_ = json.Unmarshal(payload, &req)
		r.mu.Lock()
		if r.listed == nil {
			r.listed = map[string][]*daemon.ProcessInfo{}
		}
		r.listed[machine] = append(r.listed[machine], &daemon.ProcessInfo{
			ID: "proc-1", Command: req.Command, Status: "running", StartTime: time.Now(),
			WorkingDir: req.WorkingDir, WorktreeID: req.WorktreeID, PackageCommand: req.PackageCommand,
		})
		r.mu.Unlock()
		return json.Marshal(map[string]string{"process_id": "proc-1"})
	case "exec.bg_list":
		r.mu.Lock()
		defer r.mu.Unlock()
		return json.Marshal(r.listed[machine])
	case "exec.bg_output":
		return json.Marshal(daemon.ProcessOutput{Lines: r.lines})
	case "exec.bg_kill":
		return json.Marshal(struct{}{})
	}
	return nil, fmt.Errorf("unexpected command %s", commandType)
}

func (r *machineRouter) IsDaemonOnline(_ context.Context, _ string, selector *toolexec.DaemonSelector) (bool, error) {
	if r.online == nil {
		return true, nil
	}
	return r.online[selector.ID], nil
}

// machinesFor returns the machines command reached, in send order.
func (r *machineRouter) machinesFor(command string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.sent {
		if s.command == command {
			out = append(out, s.machine)
		}
	}
	return out
}

func (r *machineRouter) payloadOf(command string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sent {
		if s.command == command {
			return s.payload
		}
	}
	return nil
}

// processesRepo holds the user's worktrees, projects and machines.
type processesRepo struct {
	Repository

	mu        sync.Mutex
	worktrees map[string]*db.Worktree
	owners    map[string]string // project -> user
	daemons   map[string]*db.Daemon
	installs  map[string][]*core.ProjectDaemon // project -> its installs
	chats     map[string]*db.Chat
}

func newProcessesRepo() *processesRepo {
	return &processesRepo{
		worktrees: map[string]*db.Worktree{},
		owners:    map[string]string{"p1": "test-user", "p-other": "someone-else"},
		daemons:   map[string]*db.Daemon{},
	}
}

func (r *processesRepo) machine(id, userID string) {
	r.daemons[id] = &db.Daemon{ID: id, UserID: userID}
}

func (r *processesRepo) worktree(id, projectID, owner string) {
	wt := &db.Worktree{ID: id, ProjectID: projectID, Path: "/home/workspace/.reliant/worktrees/" + id}
	if owner != "" {
		wt.DaemonID = &owner
	}
	r.worktrees[id] = wt
}

func (r *processesRepo) GetWorktree(_ context.Context, id string) (*db.Worktree, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if wt, ok := r.worktrees[id]; ok {
		cp := *wt
		return &cp, nil
	}
	return nil, core.ErrWorktreeNotFound
}

func (r *processesRepo) GetProjectWithUserCheck(_ context.Context, id, userID string) (*db.Project, error) {
	if r.owners[id] != userID {
		return nil, core.ErrProjectNotFound
	}
	return &db.Project{ID: id, UserID: userID}, nil
}

func (r *processesRepo) GetProject(_ context.Context, id string) (*db.Project, error) {
	return &db.Project{ID: id, UserID: r.owners[id]}, nil
}

func (r *processesRepo) GetChat(_ context.Context, id string) (*db.Chat, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.chats[id]; ok {
		cp := *c
		return &cp, nil
	}
	return nil, core.ErrChatNotFound
}

func (r *processesRepo) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	if d, ok := r.daemons[id]; ok {
		return d, nil
	}
	return nil, errors.New("no such daemon")
}

func (r *processesRepo) ListDaemonsByUserID(_ context.Context, userID string) ([]*db.Daemon, error) {
	var out []*db.Daemon
	for _, d := range r.daemons {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (r *processesRepo) ListProjectDaemonsForProject(_ context.Context, projectID string) ([]*core.ProjectDaemon, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.installs[projectID], nil
}

func newProcessesService(t *testing.T, router *machineRouter, repo *processesRepo) *Service {
	t.Helper()
	return NewTestPackageCommands(t, WithPackageCommandsDeps(Deps{Router: router, Repo: repo}))
}

func runBuild(svc *Service, worktreeID string) (*connect.Response[reliantv1.RunPackageCommandResponse], error) {
	return svc.RunCommand(authedCtx(), connect.NewRequest(&reliantv1.RunPackageCommandRequest{
		WorktreeId: ptr.Of(worktreeID), CommandName: "build", PackageType: reliantv1.PackageType_PACKAGE_TYPE_NPM,
	}))
}

// The reported shape: a workspace on machine B, the user's default
// elsewhere. Discovery and the process both go to B, with the command line
// discovery found, not one the client sent.
func TestRunCommand_RunsOnTheWorkspacesMachine(t *testing.T) {
	router := &machineRouter{}
	repo := newProcessesRepo()
	repo.worktree("wt-b", "p1", "daemon-b")
	svc := newProcessesService(t, router, repo)

	resp, err := runBuild(svc, "wt-b")
	require.NoError(t, err)

	assert.Equal(t, []string{"daemon-b"}, router.machinesFor("pkg.list_commands"))
	assert.Equal(t, []string{"daemon-b"}, router.machinesFor("exec.bg_start"))
	var started daemon.RunCommandRequest
	require.NoError(t, json.Unmarshal(router.payloadOf("exec.bg_start"), &started))
	assert.Equal(t, "npm run build", started.Command, "the root's build, not a subdirectory's")
	assert.Equal(t, "/home/workspace/.reliant/worktrees/wt-b", started.WorkingDir)
	assert.Equal(t, "wt-b", started.WorktreeID, "tagged so the workspace's list finds it")
	assert.Equal(t, "proc-1", resp.Msg.ProcessId)
	assert.Equal(t, "daemon-b", resp.Msg.DaemonId)
}

func TestRunCommand_UnknownCommandStartsNothing(t *testing.T) {
	router := &machineRouter{}
	repo := newProcessesRepo()
	repo.worktree("wt-b", "p1", "daemon-b")
	svc := newProcessesService(t, router, repo)

	_, err := svc.RunCommand(authedCtx(), connect.NewRequest(&reliantv1.RunPackageCommandRequest{
		WorktreeId: ptr.Of("wt-b"), CommandName: "deploy", PackageType: reliantv1.PackageType_PACKAGE_TYPE_NPM,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	assert.Empty(t, router.machinesFor("exec.bg_start"))
}

func TestRunCommand_AnotherUsersWorkspaceIsNotFound(t *testing.T) {
	router := &machineRouter{}
	repo := newProcessesRepo()
	repo.worktree("wt-other", "p-other", "daemon-x")
	svc := newProcessesService(t, router, repo)

	_, err := runBuild(svc, "wt-other")
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	assert.Empty(t, router.sent, "nothing is sent for someone else's workspace")
}

// placingRouter is a machineRouter that can place a machine: the user's
// default is defaultMachine, and every machine of theirs can be placed.
type placingRouter struct {
	*machineRouter
	defaultMachine string
}

func (r *placingRouter) PlacementDaemonID(_ context.Context, _ string, daemonID string) (string, error) {
	if daemonID == "" {
		return r.defaultMachine, nil
	}
	return daemonID, nil
}

// The main checkout is on every machine with the project, so it has no owner.
// Its commands go to the project-aware default (a machine the project is
// installed on), never to a project-blind default that may not have it and
// would run whatever sits at the same path there.
func TestRunCommand_MainCheckoutRunsWhereTheProjectIsInstalled(t *testing.T) {
	router := &placingRouter{machineRouter: &machineRouter{}, defaultMachine: "daemon-a"}
	repo := newProcessesRepo()
	repo.machine("daemon-a", "test-user")
	repo.machine("daemon-b", "test-user")
	repo.installs = map[string][]*core.ProjectDaemon{"p1": {{ProjectID: "p1", DaemonID: "daemon-b", InstallState: core.ProjectInstallInstalled}}}
	repo.worktree("wt-main", "p1", "")
	repo.worktrees["wt-main"].IsMain = true
	svc := NewTestPackageCommands(t, WithPackageCommandsDeps(Deps{Router: router, Repo: repo}))

	resp, err := runBuild(svc, "wt-main")
	require.NoError(t, err)

	assert.Equal(t, []string{"daemon-b"}, router.machinesFor("pkg.list_commands"), "discovery asks the machine with the project")
	assert.Equal(t, []string{"daemon-b"}, router.machinesFor("exec.bg_start"))
	assert.Equal(t, "daemon-b", resp.Msg.DaemonId, "the concrete machine is carried to the process rpcs")
}

// The main checkout beside a chat pinned to B runs on B, whatever the
// project default is: the chat's Files tab and terminal are on B, so a build
// clicked beside it builds B's checkout. Another user's chat is not found.
func TestRunCommand_MainCheckoutBesideAChatRunsOnTheChatsMachine(t *testing.T) {
	router := &placingRouter{machineRouter: &machineRouter{}, defaultMachine: "daemon-a"}
	repo := newProcessesRepo()
	repo.machine("daemon-a", "test-user")
	repo.machine("daemon-b", "test-user")
	repo.installs = map[string][]*core.ProjectDaemon{"p1": {
		{ProjectID: "p1", DaemonID: "daemon-a", InstallState: core.ProjectInstallInstalled},
		{ProjectID: "p1", DaemonID: "daemon-b", InstallState: core.ProjectInstallInstalled},
	}}
	repo.worktree("wt-main", "p1", "")
	repo.worktrees["wt-main"].IsMain = true
	pinned := "daemon-b"
	repo.chats = map[string]*db.Chat{
		"chat-b":     {ID: "chat-b", UserID: "test-user", ProjectID: "p1", ActiveDaemonID: &pinned},
		"chat-other": {ID: "chat-other", UserID: "someone-else", ProjectID: "p1", ActiveDaemonID: &pinned},
	}
	svc := NewTestPackageCommands(t, WithPackageCommandsDeps(Deps{Router: router, Repo: repo}))

	resp, err := svc.RunCommand(authedCtx(), connect.NewRequest(&reliantv1.RunPackageCommandRequest{
		WorktreeId: ptr.Of("wt-main"), ChatId: ptr.Of("chat-b"), CommandName: "build", PackageType: reliantv1.PackageType_PACKAGE_TYPE_NPM,
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{"daemon-b"}, router.machinesFor("pkg.list_commands"))
	assert.Equal(t, []string{"daemon-b"}, router.machinesFor("exec.bg_start"))
	assert.Equal(t, "daemon-b", resp.Msg.DaemonId)

	_, err = svc.ListCommands(authedCtx(), connect.NewRequest(&reliantv1.ListPackageCommandsRequest{
		WorktreeId: ptr.Of("wt-main"), ChatId: ptr.Of("chat-other"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "another user's chat is not found")
}

// failingInstallsRepo fails the project-install lookup.
type failingInstallsRepo struct{ *processesRepo }

func (failingInstallsRepo) ListProjectDaemonsForProject(context.Context, string) ([]*core.ProjectDaemon, error) {
	return nil, errors.New("database unavailable")
}

// A failed install lookup is reported, never routed around: project-blind
// default resolution could discover and run a command on a machine without
// this project.
func TestRunCommand_InstallLookupFailureDispatchesNothing(t *testing.T) {
	router := &placingRouter{machineRouter: &machineRouter{}, defaultMachine: "daemon-a"}
	repo := newProcessesRepo()
	repo.machine("daemon-a", "test-user")
	repo.worktree("wt-main", "p1", "")
	repo.worktrees["wt-main"].IsMain = true
	svc := NewTestPackageCommands(t, WithPackageCommandsDeps(Deps{Router: router, Repo: failingInstallsRepo{repo}}))

	_, err := runBuild(svc, "wt-main")
	require.Error(t, err)
	assert.Empty(t, router.machinesFor("pkg.list_commands"), "nothing is discovered on a guessed machine")
	assert.Empty(t, router.machinesFor("exec.bg_start"), "nothing is run on a guessed machine")
}

func TestListProcesses_ForAWorkspaceReadsItsMachine(t *testing.T) {
	now := time.Now()
	router := &machineRouter{listed: map[string][]*daemon.ProcessInfo{
		"daemon-b": {
			{ID: "mine", Command: "npm run build", Status: "running", StartTime: now, WorktreeID: "wt-b", WorkingDir: "/w"},
			{ID: "other-workspace", Command: "make", Status: "running", StartTime: now, WorktreeID: "wt-c"},
			{ID: "agent", Command: "sleep 5", Status: "running", StartTime: now},
		},
	}}
	repo := newProcessesRepo()
	repo.worktree("wt-b", "p1", "daemon-b")
	svc := newProcessesService(t, router, repo)

	resp, err := svc.ListProcesses(authedCtx(), connect.NewRequest(&reliantv1.ListPackageProcessesRequest{WorktreeId: ptr.Of("wt-b")}))
	require.NoError(t, err)

	require.Len(t, resp.Msg.Processes, 1)
	assert.Equal(t, "mine", resp.Msg.Processes[0].Id)
	assert.Equal(t, "daemon-b", resp.Msg.Processes[0].DaemonId, "the process says which machine to ask about it")
	assert.Equal(t, []string{"daemon-b"}, router.machinesFor("exec.bg_list"))
}

// A command run by path (no workspace) is still a package command: listed by
// the panel and found by GetProcess right after it starts, while an agent's
// own background command on the same machine stays out.
func TestRunCommand_PathOnlyProcessIsListedAndFound(t *testing.T) {
	router := &machineRouter{listed: map[string][]*daemon.ProcessInfo{
		routedDefault: {{ID: "agent", Command: "sleep 5", Status: "running", StartTime: time.Now()}},
	}}
	repo := newProcessesRepo()
	repo.machine(routedDefault, "test-user")
	svc := newProcessesService(t, router, repo)

	started, err := svc.RunCommand(authedCtx(), connect.NewRequest(&reliantv1.RunPackageCommandRequest{
		Path: ptr.Of("/home/u/project"), CommandName: "build", PackageType: reliantv1.PackageType_PACKAGE_TYPE_NPM,
	}))
	require.NoError(t, err)
	var req daemon.RunCommandRequest
	require.NoError(t, json.Unmarshal(router.payloadOf("exec.bg_start"), &req))
	assert.True(t, req.PackageCommand, "the process is tagged as a package command")
	assert.Empty(t, req.WorktreeID)

	listed, err := svc.ListProcesses(authedCtx(), connect.NewRequest(&reliantv1.ListPackageProcessesRequest{}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.Processes, 1, "the package command is listed, the agent's command is not")
	assert.Equal(t, started.Msg.ProcessId, listed.Msg.Processes[0].Id)

	got, err := svc.GetProcess(authedCtx(), connect.NewRequest(&reliantv1.GetPackageProcessRequest{ProcessId: started.Msg.ProcessId}))
	require.NoError(t, err, "a process just started by path is found")
	assert.Equal(t, "npm run build", got.Msg.Process.Command)
}

// A list never wakes a machine.
func TestListProcesses_AsleepWorkspaceMachineListsNothing(t *testing.T) {
	router := &machineRouter{online: map[string]bool{"daemon-b": false}}
	repo := newProcessesRepo()
	repo.worktree("wt-b", "p1", "daemon-b")
	svc := newProcessesService(t, router, repo)

	resp, err := svc.ListProcesses(authedCtx(), connect.NewRequest(&reliantv1.ListPackageProcessesRequest{WorktreeId: ptr.Of("wt-b")}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.Processes)
	assert.Empty(t, router.machinesFor("exec.bg_list"))
}

func TestListProcesses_EverywhereAsksEachConnectedMachine(t *testing.T) {
	now := time.Now()
	router := &machineRouter{
		online: map[string]bool{"daemon-a": true, "daemon-b": true, "daemon-asleep": false},
		listed: map[string][]*daemon.ProcessInfo{
			"daemon-a": {{ID: "on-a", Status: "running", StartTime: now, WorktreeID: "wt-a"}},
			"daemon-b": {{ID: "on-b", Status: "completed", StartTime: now, WorktreeID: "wt-b"}, {ID: "agent", Status: "running", StartTime: now}},
		},
	}
	repo := newProcessesRepo()
	repo.machine("daemon-a", "test-user")
	repo.machine("daemon-b", "test-user")
	repo.machine("daemon-asleep", "test-user")
	repo.machine("daemon-foreign", "someone-else")
	svc := newProcessesService(t, router, repo)

	resp, err := svc.ListProcesses(authedCtx(), connect.NewRequest(&reliantv1.ListPackageProcessesRequest{}))
	require.NoError(t, err)

	byID := map[string]string{}
	for _, p := range resp.Msg.Processes {
		byID[p.Id] = p.DaemonId
	}
	assert.Equal(t, map[string]string{"on-a": "daemon-a", "on-b": "daemon-b"}, byID)
	assert.ElementsMatch(t, []string{"daemon-a", "daemon-b"}, router.machinesFor("exec.bg_list"))
}

func TestGetProcessLogs_ReadsTheProcessesMachine(t *testing.T) {
	router := &machineRouter{lines: []daemon.ProcessOutputLine{
		{Stream: "stdout", Text: "building"},
		{Stream: "stderr", Text: "warning: x"},
		{Stream: "stdout", Text: "done"},
	}}
	repo := newProcessesRepo()
	repo.machine("daemon-b", "test-user")
	svc := newProcessesService(t, router, repo)

	resp, err := svc.GetProcessLogs(authedCtx(), connect.NewRequest(&reliantv1.GetPackageProcessLogsRequest{
		ProcessId: "proc-1", DaemonId: ptr.Of("daemon-b"),
	}))
	require.NoError(t, err)

	assert.Equal(t, []string{"daemon-b"}, router.machinesFor("exec.bg_output"))
	assert.Equal(t, "building\ndone\n", resp.Msg.Stdout)
	assert.Equal(t, "warning: x\n", resp.Msg.Stderr)
	require.Len(t, resp.Msg.Combined, 3)
	assert.Equal(t, reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_STDERR, resp.Msg.Combined[1].Type)
}

func TestKillProcess_StopsItOnItsMachine(t *testing.T) {
	router := &machineRouter{}
	repo := newProcessesRepo()
	repo.machine("daemon-b", "test-user")
	repo.machine("daemon-foreign", "someone-else")
	svc := newProcessesService(t, router, repo)

	_, err := svc.KillProcess(authedCtx(), connect.NewRequest(&reliantv1.KillPackageProcessRequest{ProcessId: "proc-1", DaemonId: ptr.Of("daemon-b")}))
	require.NoError(t, err)
	assert.Equal(t, []string{"daemon-b"}, router.machinesFor("exec.bg_kill"))

	_, err = svc.KillProcess(authedCtx(), connect.NewRequest(&reliantv1.KillPackageProcessRequest{ProcessId: "proc-1", DaemonId: ptr.Of("daemon-foreign")}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "another user's machine reads like a missing one")
	assert.Len(t, router.machinesFor("exec.bg_kill"), 1, "nothing is sent to it")
}

// An older daemon sends one text, not per-stream lines: it is read as stdout.
func TestProcessLogs_FromAnOlderDaemon(t *testing.T) {
	logs := processLogs(&daemon.ProcessOutput{Output: "a\nb\n"})
	assert.Equal(t, "a\nb\n", logs.Stdout)
	assert.Empty(t, logs.Stderr)
	assert.Len(t, logs.Combined, 2)
}
