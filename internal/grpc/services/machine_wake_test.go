// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// asleepRouter is a daemon router whose machines can be asleep, and that can
// wake them the way NATSDaemonRouter.Wake does. A command to an asleep machine
// fails exactly as the real router fails it: a pinned send has no NATS
// responder ("no daemon connected", Unavailable), and default resolution finds
// the machine suspended (toolexec.ErrDaemonPending). Waking records the call;
// the test decides when the machine has come up (wakeUp).
type asleepRouter struct {
	placementRouter

	mu        sync.Mutex
	asleep    map[string]bool
	wakes     []asleepWake
	notResume bool // Wake finds the machine already on its way up
}

type asleepWake struct {
	userID   string
	daemonID string // "" = default resolution
	jwt      string
}

func newAsleepRouter(asleep ...string) *asleepRouter {
	r := &asleepRouter{asleep: map[string]bool{}}
	for _, d := range asleep {
		r.asleep[d] = true
	}
	return r
}

func (r *asleepRouter) isAsleep(daemonID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.asleep[daemonID]
}

// wakeUp is the machine finishing its cold start and connecting.
func (r *asleepRouter) wakeUp(daemonID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.asleep, daemonID)
}

func (r *asleepRouter) wakeCalls() []asleepWake {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]asleepWake(nil), r.wakes...)
}

func (r *asleepRouter) Wake(_ context.Context, userID string, selector *toolexec.DaemonSelector) (toolexec.WakeResult, error) {
	daemonID := ""
	if selector != nil {
		daemonID = selector.ID
	}
	jwt, _ := auth.GetUserJWT(userID)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wakes = append(r.wakes, asleepWake{userID: userID, daemonID: daemonID, jwt: jwt})
	if daemonID == "" {
		daemonID = placementDefaultDaemon
	}
	return toolexec.WakeResult{DaemonID: daemonID, Resumed: !r.notResume}, nil
}

func (r *asleepRouter) ResolveDaemonID(context.Context, string) (string, error) {
	if r.isAsleep(placementDefaultDaemon) {
		return "", fmt.Errorf("the machine for this request is suspended and will wake when you next message it: %w", toolexec.ErrDaemonPending)
	}
	return placementDefaultDaemon, nil
}

func (r *asleepRouter) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if _, err := r.ResolveDaemonID(ctx, userID); err != nil {
		return nil, fmt.Errorf("resolving daemon for command: %w", err)
	}
	return r.deliverAwake(placementDefaultDaemon, commandType, payload)
}

func (r *asleepRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	if r.isAsleep(daemonID) {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("no daemon connected for user"))
	}
	return r.deliverAwake(daemonID, commandType, payload)
}

// asleepTestPNG is the image the preview tests serve: a PNG signature and
// enough bytes to be recognisably not text.
var asleepTestPNG = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), make([]byte, 64)...)

func (r *asleepRouter) deliverAwake(daemonID, commandType string, payload []byte) ([]byte, error) {
	switch commandType {
	case "fs.get_tree":
		r.record(daemonID, commandType)
		return json.Marshal(map[string]any{"nodes": []any{}})
	case "fs.preview_info":
		r.record(daemonID, commandType)
		return json.Marshal(map[string]any{
			"name": "diagram.png", "path": "/w/diagram.png", "size": len(asleepTestPNG),
			"viewer_kind": "image", "mime_type": "image/png", "is_binary": true,
		})
	case "fs.read_binary_file":
		r.record(daemonID, commandType)
		return json.Marshal(map[string]any{"data": base64.StdEncoding.EncodeToString(asleepTestPNG)})
	case "worktree.delete_directory", "worktree.delete_branch", "worktree.remove_workspace_dir":
		r.record(daemonID, commandType)
		return json.Marshal(map[string]any{"deleted": true})
	}
	return r.deliver(daemonID, commandType, payload)
}

func (r *asleepRouter) record(daemonID, commandType string) {
	r.placementRouter.mu.Lock()
	defer r.placementRouter.mu.Unlock()
	if r.targets == nil {
		r.targets = make(map[string][]string)
	}
	r.targets[commandType] = append(r.targets[commandType], daemonID)
}

// asleepFixture is a project whose user is signed in (holds a JWT, which a
// wake needs) and owns machine B. Machines named in asleep start asleep.
type asleepFixture struct {
	*placementFixture
	router *asleepRouter
	fs     *FileSystemProxyService
	wt     *WorktreeService
}

const asleepMachineB = "daemon-b"

func newAsleepFixture(t *testing.T, asleep ...string) *asleepFixture {
	t.Helper()
	pf := newPlacementFixture(t)
	router := newAsleepRouter(asleep...)
	auth.SetUserJWT(pf.userID, "user-jwt")
	t.Cleanup(func() { auth.SetUserJWT(pf.userID, "") })
	require.NoError(t, pf.repo.UpsertDaemon(context.Background(), &db.Daemon{ID: asleepMachineB, UserID: pf.userID}))
	return &asleepFixture{
		placementFixture: pf,
		router:           router,
		fs:               NewFileSystemProxyService(router, pf.repo),
		wt:               NewWorktreeService(pf.repo, nil, router),
	}
}

// noMachineChat inserts a chat with no machine by design.
func (f *asleepFixture) noMachineChat(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	c := &db.Chat{
		ID: uuid.NewString(), UserID: f.userID, Title: "No machine", ProjectID: f.projectID,
		State: db.ChatStateIdle, NoMachine: true, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, f.repo.CreateChat(context.Background(), c))
	return c.ID
}

func (f *asleepFixture) getTree(worktreeID, chatID *string) error {
	_, err := f.fs.GetFileTree(f.ctx, connect.NewRequest(&reliantv1.GetFileTreeRequest{
		ProjectId: f.projectID, Path: "/", WorktreeId: worktreeID, ChatId: chatID,
	}))
	return err
}

// asWire passes err through MachineWakingInterceptor, as the server does, and
// returns the Connect error a client receives.
func asWire(t *testing.T, err error) *connect.Error {
	t.Helper()
	wrapped := NewMachineWakingInterceptor().WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, err
	})
	_, wireErr := wrapped(context.Background(), connect.NewRequest(&reliantv1.GetFileTreeRequest{}))
	var cerr *connect.Error
	require.True(t, errors.As(wireErr, &cerr), "not a connect error: %v", wireErr)
	return cerr
}

// requireWaking asserts err reaches the client as "your machine daemonID is
// waking": Unavailable, the marker the UI retries on, and the detail naming it.
func requireWaking(t *testing.T, err error, daemonID string) {
	t.Helper()
	require.Error(t, err)
	cerr := asWire(t, err)
	assert.Equal(t, connect.CodeUnavailable, cerr.Code())
	assert.Contains(t, cerr.Message(), "no daemon connected", "the UI's machine wait keys on this marker")
	var waking []*reliantv1.DaemonWaking
	for _, d := range cerr.Details() {
		if v, derr := d.Value(); derr == nil {
			if w, ok := v.(*reliantv1.DaemonWaking); ok {
				waking = append(waking, w)
			}
		}
	}
	require.Len(t, waking, 1, "the error must name the waking machine")
	assert.Equal(t, daemonID, waking[0].GetDaemonId())
}

// TestFileSystemProxy_AsleepChatMachine_WakesThenServes is the regression for
// "the Files tab of a chat whose machine is asleep fails with no daemon
// connected": the request wakes the chat's machine (once, as the signed-in
// user), reports it as waking, and the retry the UI makes once the machine is
// up is served from it. Before the fix nothing woke the machine, so every
// retry failed the same way for as long as the user waited.
func TestFileSystemProxy_AsleepChatMachine_WakesThenServes(t *testing.T) {
	f := newAsleepFixture(t, asleepMachineB)
	chatID := f.chat(t, f.userID, asleepMachineB, "")

	err := f.getTree(nil, &chatID)
	requireWaking(t, err, asleepMachineB)
	require.Equal(t, []asleepWake{{userID: f.userID, daemonID: asleepMachineB, jwt: "user-jwt"}}, f.router.wakeCalls(),
		"the chat's machine is woken once, as the signed-in user")

	f.router.wakeUp(asleepMachineB)
	require.NoError(t, f.getTree(nil, &chatID), "once the machine is up the retry is served")
	assert.Equal(t, []string{asleepMachineB}, f.router.daemonsFor("fs.get_tree"))
	assert.Len(t, f.router.wakeCalls(), 1, "a request to a connected machine wakes nothing")
}

// Default resolution finds the user's machine suspended: the same wake, for
// the machine resolution would have picked.
func TestFileSystemProxy_AsleepDefaultMachine_Wakes(t *testing.T) {
	f := newAsleepFixture(t, placementDefaultDaemon)

	err := f.getTree(nil, nil)
	requireWaking(t, err, placementDefaultDaemon)
	require.Equal(t, []asleepWake{{userID: f.userID, daemonID: "", jwt: "user-jwt"}}, f.router.wakeCalls())

	f.router.wakeUp(placementDefaultDaemon)
	require.NoError(t, f.getTree(nil, nil))
}

// TestFileSystemProxy_NoMachineChat_NeverWakes: a chat with no machine by
// design must never wake one, even though its Files tab still reads from the
// default machine and that machine is asleep (research/DAEMONLESS_RUNS.md).
func TestFileSystemProxy_NoMachineChat_NeverWakes(t *testing.T) {
	f := newAsleepFixture(t, placementDefaultDaemon, asleepMachineB)
	chatID := f.noMachineChat(t)
	owned := f.worktree(t, asleepMachineB)

	err := f.getTree(nil, &chatID)
	require.Error(t, err)
	assert.False(t, isMachineWaking(err), "got %v", err)
	err = f.getTree(&owned.ID, &chatID)
	require.Error(t, err, "naming another machine's worktree from a no-machine chat does not wake it either")
	assert.False(t, isMachineWaking(err), "got %v", err)

	assert.Empty(t, f.router.wakeCalls(), "a no-machine chat must never wake anything")
}

// A machine that is not the caller's, by the record here, is never woken for
// them: the request names a worktree whose owning machine belongs to another
// user.
func TestFileSystemProxy_AnotherUsersMachine_NeverWakes(t *testing.T) {
	f := newAsleepFixture(t, "daemon-foreign")
	require.NoError(t, f.repo.UpsertDaemon(context.Background(), &db.Daemon{ID: "daemon-foreign", UserID: uuid.NewString()}))
	wt := f.worktree(t, "daemon-foreign")

	err := f.getTree(&wt.ID, nil)
	require.Error(t, err)
	assert.False(t, isMachineWaking(err))
	assert.Empty(t, f.router.wakeCalls(), "another user's machine must never be woken")
}

// A machine with no record here cannot be shown to be the caller's, so it is
// not woken either.
func TestFileSystemProxy_UnknownMachine_NeverWakes(t *testing.T) {
	f := newAsleepFixture(t, "daemon-unknown")
	chatID := f.chat(t, f.userID, "daemon-unknown", "")

	require.Error(t, f.getTree(nil, &chatID))
	assert.Empty(t, f.router.wakeCalls())
}

// Waking needs the user's own Bearer: without a JWT (an API-token caller, or a
// session the server no longer holds) nothing is woken.
func TestFileSystemProxy_NoUserJWT_NeverWakes(t *testing.T) {
	f := newAsleepFixture(t, asleepMachineB)
	auth.SetUserJWT(f.userID, "")
	chatID := f.chat(t, f.userID, asleepMachineB, "")

	require.Error(t, f.getTree(nil, &chatID))
	assert.Empty(t, f.router.wakeCalls())
}

// A machine that is still provisioning, or that an earlier request already
// woke, is not asleep: the wake resumes nothing, and the request is not
// reported as waking (the UI keeps its "starting" copy).
func TestFileSystemProxy_MachineAlreadyComingUp_NotReportedAsWaking(t *testing.T) {
	f := newAsleepFixture(t, asleepMachineB)
	f.router.notResume = true
	chatID := f.chat(t, f.userID, asleepMachineB, "")

	err := f.getTree(nil, &chatID)
	require.Error(t, err)
	assert.False(t, isMachineWaking(err), "got %v", err)
	assert.Contains(t, err.Error(), "no daemon connected", "still the retryable machine error")
}

// TestMachineWakingInterceptor_OnTheWire mounts the real handler with the
// interceptor and calls it over HTTP: what a browser receives.
func TestMachineWakingInterceptor_OnTheWire(t *testing.T) {
	f := newAsleepFixture(t, asleepMachineB)
	chatID := f.chat(t, f.userID, asleepMachineB, "")

	withUser := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			return next(context.WithValue(ctx, auth.UserIDContextKey, f.userID), req)
		}
	})
	path, handler := reliantv1connect.NewFileSystemServiceHandler(f.fs,
		connect.WithInterceptors(withUser), connect.WithInterceptors(NewMachineWakingInterceptor()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := reliantv1connect.NewFileSystemServiceClient(srv.Client(), srv.URL)
	_, err := client.GetFileTree(context.Background(), connect.NewRequest(&reliantv1.GetFileTreeRequest{
		ProjectId: f.projectID, Path: "/", ChatId: &chatID,
	}))
	var cerr *connect.Error
	require.True(t, errors.As(err, &cerr), "got %v", err)
	assert.Equal(t, connect.CodeUnavailable, cerr.Code())
	assert.Contains(t, cerr.Message(), "no daemon connected")
	require.Len(t, cerr.Details(), 1)
	v, derr := cerr.Details()[0].Value()
	require.NoError(t, derr)
	waking, ok := v.(*reliantv1.DaemonWaking)
	require.True(t, ok, "detail is %T", v)
	assert.Equal(t, asleepMachineB, waking.GetDaemonId())
}

// TestWorktreeGitStatus_AsleepOwner_WakesThenServes is the regression for the
// worktree operations: git status (and every git op, which validates the
// checkout first) against an asleep owner wakes it and is served once it is up.
func TestWorktreeGitStatus_AsleepOwner_WakesThenServes(t *testing.T) {
	f := newAsleepFixture(t, asleepMachineB)
	wt := f.worktree(t, asleepMachineB)
	status := func() error {
		_, err := f.wt.GetWorktreeGitStatus(f.ctx, connect.NewRequest(&reliantv1.GetWorktreeGitStatusRequest{WorktreeId: wt.ID}))
		return err
	}

	requireWaking(t, status(), asleepMachineB)
	require.Equal(t, []asleepWake{{userID: f.userID, daemonID: asleepMachineB, jwt: "user-jwt"}}, f.router.wakeCalls())

	f.router.wakeUp(asleepMachineB)
	require.NoError(t, status())
	assert.Equal(t, []string{asleepMachineB}, f.router.daemonsFor("worktree.git_status"))
}

// Creating a worktree for a chat whose machine is asleep wakes it BEFORE
// recording anything. Before the fix the row was written and the checkout then
// failed in the background, leaving a FAILED workspace.
func TestCreateWorktree_AsleepChatMachine_WakesBeforeRecordingAnything(t *testing.T) {
	f := newAsleepFixture(t, asleepMachineB)
	chatID := f.chat(t, f.userID, asleepMachineB, "")
	key := uuid.NewString()
	create := func() (*connect.Response[reliantv1.CreateWorktreeResponse], error) {
		return f.wt.CreateWorktree(f.ctx, connect.NewRequest(&reliantv1.CreateWorktreeRequest{
			ProjectId: f.projectID, Name: "wake-test", Branch: "wake-test", ChatId: &chatID, IdempotencyKey: &key,
		}))
	}

	_, err := create()
	requireWaking(t, err, asleepMachineB)
	rows, lerr := f.repo.ListWorktrees(context.Background(), db.WorktreeFilters{ProjectID: &f.projectID, IncludeArchived: true})
	require.NoError(t, lerr)
	assert.Empty(t, rows, "nothing is recorded while the machine is asleep")
	assert.Empty(t, f.router.daemonsFor("worktree.create"))

	f.router.wakeUp(asleepMachineB)
	resp, err := create()
	require.NoError(t, err, "the retry creates the worktree on the machine that is now up")
	wt := awaitWorktreeStatus(t, f.repo, resp.Msg.Worktree.Id, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	assertPlacedOn(t, &f.router.placementRouter, wt, asleepMachineB)
}

// Deleting a worktree's branch is carried out on its owner after the directory
// is gone, so an asleep owner is woken first rather than the row being archived
// with the request unservable. (The directory itself is the daemon's to settle.)
func TestDeleteWorktree_AsleepOwner_WakesInsteadOfArchiving(t *testing.T) {
	f := newAsleepFixture(t, asleepMachineB)
	wt := f.worktree(t, asleepMachineB)
	del := func() error {
		_, err := f.wt.DeleteWorktree(f.ctx, connect.NewRequest(&reliantv1.DeleteWorktreeRequest{
			WorktreeId: wt.ID, DeleteGitBranch: true,
		}))
		return err
	}

	requireWaking(t, del(), asleepMachineB)
	got, err := f.repo.GetWorktree(context.Background(), wt.ID)
	require.NoError(t, err)
	assert.Nil(t, got.DeletedAt, "an asleep machine's worktree is not archived")

	f.router.wakeUp(asleepMachineB)
	require.NoError(t, del())
	got, err = f.repo.GetWorktree(context.Background(), wt.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.DeletedAt, "the retry on the awake machine archives the row")
}

// TestFileSystemProxy_GetFilePreview_ServesImageFromOwningDaemon is the
// regression for "binary previews fail everywhere": the hosted file service
// did not implement GetFilePreview, so every image/PDF/audio/video preview
// returned Unimplemented. The preview is read on the chat's machine, B, and
// carries the daemon's classification.
func TestFileSystemProxy_GetFilePreview_ServesImageFromOwningDaemon(t *testing.T) {
	f := newAsleepFixture(t)
	chatID := f.chat(t, f.userID, asleepMachineB, "")

	resp, err := f.fs.GetFilePreview(f.ctx, connect.NewRequest(&reliantv1.GetFilePreviewRequest{
		ProjectId: f.projectID, Path: "diagram.png", ChatId: &chatID,
	}))
	require.NoError(t, err)
	assert.Equal(t, asleepTestPNG, resp.Msg.GetContent())
	assert.Equal(t, "image/png", resp.Msg.GetContentType())
	assert.Equal(t, "diagram.png", resp.Msg.GetFilename())
	assert.Equal(t, int64(len(asleepTestPNG)), resp.Msg.GetSize())
	assert.Equal(t, []string{asleepMachineB}, f.router.daemonsFor("fs.preview_info"))
	assert.Equal(t, []string{asleepMachineB}, f.router.daemonsFor("fs.read_binary_file"),
		"the bytes come from the machine that holds the file")
}

// previewRouter answers fs.preview_info with a fixed classification, to drive
// GetFilePreview's refusals.
type previewRouter struct {
	*asleepRouter
	info map[string]any
}

func (r *previewRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if commandType == "fs.preview_info" {
		return json.Marshal(r.info)
	}
	return r.asleepRouter.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payload, timeoutMs)
}

func TestFileSystemProxy_GetFilePreview_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		info map[string]any
		code connect.Code
	}{
		{"text is not a binary preview", map[string]any{"name": "a.go", "viewer_kind": "text", "mime_type": "text/plain", "size": 10}, connect.CodeFailedPrecondition},
		{"over the size cap", map[string]any{"name": "clip.mp4", "viewer_kind": "video", "mime_type": "video/mp4", "size": maxFilePreviewBytes + 1}, connect.CodeFailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAsleepFixture(t)
			router := &previewRouter{asleepRouter: newAsleepRouter(), info: tc.info}
			fs := NewFileSystemProxyService(router, f.repo)
			chatID := f.chat(t, f.userID, asleepMachineB, "")

			_, err := fs.GetFilePreview(f.ctx, connect.NewRequest(&reliantv1.GetFilePreviewRequest{
				ProjectId: f.projectID, Path: "x", ChatId: &chatID,
			}))
			require.Error(t, err)
			assert.Equal(t, tc.code, connect.CodeOf(err))
			assert.Empty(t, router.daemonsFor("fs.read_binary_file"), "a refused preview reads nothing")
		})
	}
}

// A preview against an asleep machine takes the same wake as the tree.
func TestFileSystemProxy_GetFilePreview_AsleepMachineWakes(t *testing.T) {
	f := newAsleepFixture(t, asleepMachineB)
	chatID := f.chat(t, f.userID, asleepMachineB, "")
	preview := func() error {
		_, err := f.fs.GetFilePreview(f.ctx, connect.NewRequest(&reliantv1.GetFilePreviewRequest{
			ProjectId: f.projectID, Path: "diagram.png", ChatId: &chatID,
		}))
		return err
	}

	requireWaking(t, preview(), asleepMachineB)
	f.router.wakeUp(asleepMachineB)
	require.NoError(t, preview())
}
