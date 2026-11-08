package project_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// pinnedRecorder records the machine and path every command went to, and
// fails a command sent by default resolution.
type pinnedRecorder struct {
	fakeDaemon
	daemons, paths, commands []string
}

func newPinnedRecorder() *pinnedRecorder {
	r := &pinnedRecorder{}
	r.fakeDaemon = func(string, []byte) ([]byte, error) { return []byte(`{"success":true}`), nil }
	return r
}

func (r *pinnedRecorder) SendDaemonCommand(_ context.Context, _, commandType string, _ []byte, _ int32) ([]byte, error) {
	return nil, svcerr.Internal(commandType + " sent by default resolution: it names one machine's path")
}

func (r *pinnedRecorder) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	var p struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(payload, &p)
	r.daemons, r.paths, r.commands = append(r.daemons, daemonID), append(r.paths, p.Path), append(r.commands, commandType)
	return r.fakeDaemon(commandType, payload)
}

// Pull, remove and reclone act on ONE machine's clone, at its row's path. The
// commands are pinned to that machine: by default resolution, git.remove
// deleted whatever sat at the same path on whichever machine answered.
func TestProjectOnDaemonCommandsArePinnedToThatDaemon(t *testing.T) {
	t.Parallel()
	ctx := project.AuthedContext(t)
	calls := map[string]func(*project.Service) error{
		"git.pull": func(s *project.Service) error {
			_, err := s.PullProjectOnDaemon(ctx, connect.NewRequest(&pb.PullProjectOnDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"}))
			return err
		},
		"git.remove": func(s *project.Service) error {
			_, err := s.RemoveProjectFromDaemon(ctx, connect.NewRequest(&pb.RemoveProjectFromDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"}))
			return err
		},
		"git.reclone": func(s *project.Service) error {
			_, err := s.RecloneProjectOnDaemon(ctx, connect.NewRequest(&pb.RecloneProjectOnDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"}))
			return err
		},
	}
	for command, call := range calls {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			store := onDaemon(t)
			store.UpsertProjectDaemonFunc = func(context.Context, string, string, string, *string) error { return nil }
			store.DeleteProjectDaemonFunc = func(context.Context, string, string) error { return nil }
			router := newPinnedRecorder()
			require.NoError(t, call(newService(t, store, router)))
			require.Equal(t, []string{command}, router.commands)
			assert.Equal(t, "d-1", router.daemons[0])
			assert.Equal(t, "/clone", router.paths[0])
		})
	}
}

// No machine holds the project: git changes say so, actionably, with the
// detail the web renders — nothing is sent, and no machine's path is in it.
func TestGetProjectChanges_NoCheckoutAnywhere(t *testing.T) {
	t.Parallel()
	store := ownedProjectRepo(t)
	store.ListProjectDaemonsForProjectFunc = func(context.Context, string) ([]*core.ProjectDaemon, error) { return nil, nil }
	router := newPinnedRecorder()

	_, err := newService(t, store, router).GetProjectChanges(project.AuthedContext(t),
		connect.NewRequest(&pb.GetProjectChangesRequest{ProjectId: "p-1"}))

	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	msg := svcerr.ToConnect(err).Message()
	assert.Contains(t, msg, "clone it onto a machine")
	assert.NotContains(t, msg, creatorPath)
	assert.Empty(t, router.commands)
	var found bool
	for _, d := range svcerr.ToConnect(err).Details() {
		if v, verr := d.Value(); verr == nil {
			if m, ok := v.(*pb.ProjectCheckoutMissing); ok {
				found = true
				assert.Equal(t, pb.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT, m.State)
			}
		}
	}
	assert.True(t, found, "the web renders the ProjectCheckoutMissing detail")
}

// The git commands go to the machine holding the checkout, at its path.
func TestGitCommandsGoToTheCheckoutsMachine(t *testing.T) {
	t.Parallel()
	router := newPinnedRecorder()
	_, err := newService(t, ownedProjectRepo(t), router).GetProjectGitInfo(project.AuthedContext(t),
		connect.NewRequest(&pb.GetProjectGitInfoRequest{ProjectId: "p-1"}))
	require.NoError(t, err)
	assert.Equal(t, []string{"daemon-1"}, router.daemons)
	assert.Equal(t, []string{"/work"}, router.paths)
}

// The row is the only record of a checkout on that machine. It goes only once
// the machine has removed the checkout: dropped after a failed or unreachable
// remove, the clone stays on disk unrecorded and the machine is refused the
// project it still holds.
func TestRemoveProjectFromDaemon_KeepsTheRowUntilTheRemoveRan(t *testing.T) {
	t.Parallel()
	ctx := project.AuthedContext(t)
	for name, answer := range map[string]func(string, []byte) ([]byte, error){
		"machine unreachable": func(string, []byte) ([]byte, error) {
			return nil, svcerr.Unavailable("no daemon connected for user")
		},
		"remove failed": func(string, []byte) ([]byte, error) {
			return []byte(`{"success":false,"error":"permission denied"}`), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := onDaemon(t)
			deleted := 0
			store.DeleteProjectDaemonFunc = func(context.Context, string, string) error { deleted++; return nil }
			router := newPinnedRecorder()
			router.fakeDaemon = answer
			_, err := newService(t, store, router).RemoveProjectFromDaemon(ctx,
				connect.NewRequest(&pb.RemoveProjectFromDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"}))
			require.Error(t, err)
			assert.Zero(t, deleted, "the row outlives a remove that did not run")
		})
	}
}

// projectOnDaemonCalls are the three commands that act on one machine's
// checkout, by request.
func projectOnDaemonCalls(ctx context.Context, projectID, daemonID string) map[string]func(*project.Service) error {
	return map[string]func(*project.Service) error{
		"pull": func(s *project.Service) error {
			_, err := s.PullProjectOnDaemon(ctx, connect.NewRequest(&pb.PullProjectOnDaemonRequest{ProjectId: projectID, DaemonId: daemonID}))
			return err
		},
		"remove": func(s *project.Service) error {
			_, err := s.RemoveProjectFromDaemon(ctx, connect.NewRequest(&pb.RemoveProjectFromDaemonRequest{ProjectId: projectID, DaemonId: daemonID}))
			return err
		},
		"reclone": func(s *project.Service) error {
			_, err := s.RecloneProjectOnDaemon(ctx, connect.NewRequest(&pb.RecloneProjectOnDaemonRequest{ProjectId: projectID, DaemonId: daemonID}))
			return err
		},
	}
}

// Pull, remove and reclone reach a machine only for the caller's own project
// and only on a machine that project is recorded on. Another user's project,
// or a machine with no checkout of it, sends nothing: pull and reclone are
// NotFound, and remove (idempotent) succeeds with nothing to remove.
func TestProjectOnDaemonCommands_SendNothingForAnotherUsersProjectOrAnUnrecordedMachine(t *testing.T) {
	t.Parallel()
	ctx := project.AuthedContext(t)
	for target, ids := range map[string][2]string{
		"another user's project":        {"p-other", "d-1"},
		"a machine without the project": {"p-1", "d-foreign"},
	} {
		for name, call := range projectOnDaemonCalls(ctx, ids[0], ids[1]) {
			t.Run(target+"/"+name, func(t *testing.T) {
				t.Parallel()
				store := onDaemon(t)
				store.DeleteProjectDaemonFunc = func(context.Context, string, string) error {
					t.Error("no row may be deleted")
					return nil
				}
				router := newPinnedRecorder()
				err := call(newService(t, store, router))
				if name == "remove" {
					require.NoError(t, err)
				} else {
					requireCode(t, err, connect.CodeNotFound)
				}
				assert.Empty(t, router.commands, "nothing reaches any machine")
			})
		}
	}
}

// Remove and reclone delete the checkout, and the project's branch workspaces
// on that machine are git worktrees of it. While one lives there, both are
// refused (FailedPrecondition, saying to archive them), nothing is sent and
// the row stays, as a detach is refused. A workspace on another machine, or
// an archived one whose clean-up finished, does not hold the checkout. Pull
// deletes nothing.
func TestProjectOnDaemonCommands_RemoveAndRecloneRefuseWhileWorkspacesLiveThere(t *testing.T) {
	t.Parallel()
	ctx := project.AuthedContext(t)
	here, elsewhere := "d-1", "d-2"
	archived := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, call := range projectOnDaemonCalls(ctx, "p-1", "d-1") {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := onDaemon(t)
			store.ListWorktreesFunc = func(_ context.Context, f db.WorktreeFilters) ([]*db.Worktree, error) {
				require.NotNil(t, f.ProjectID)
				assert.Equal(t, "p-1", *f.ProjectID)
				assert.True(t, f.IncludeArchived, "archived workspaces can still hold the checkout")
				return []*db.Worktree{
					{ID: "main", IsMain: true, DaemonID: &here},
					{ID: "live", DaemonID: &here},
					{ID: "gone", DaemonID: &here, DeletedAt: &archived, CleanupMetadata: &db.CleanupMetadata{DirectoryDeleted: true}},
					{ID: "gone-branch", DaemonID: &here, DeletedAt: &archived, CleanupMetadata: &db.CleanupMetadata{
						DirectoryDeleted: true, DeleteBranch: true, BranchDeleted: true,
					}},
					{ID: "other", DaemonID: &elsewhere},
				}, nil
			}
			store.UpsertProjectDaemonFunc = func(context.Context, string, string, string, *string) error { return nil }
			deleted := false
			store.DeleteProjectDaemonFunc = func(context.Context, string, string) error { deleted = true; return nil }
			router := newPinnedRecorder()
			err := call(newService(t, store, router))
			if name == "pull" {
				require.NoError(t, err)
				assert.Equal(t, []string{"git.pull"}, router.commands)
				return
			}
			requireCode(t, err, connect.CodeFailedPrecondition)
			msg := svcerr.ToConnect(err).Message()
			assert.Contains(t, msg, "1 workspace of")
			assert.Contains(t, msg, "archive")
			assert.Empty(t, router.commands, "nothing is deleted on the machine")
			assert.False(t, deleted, "the row stays")
		})
	}
}

// An archived workspace whose clean-up has not finished still depends on the
// checkout: archiving keeps a directory it cannot safely remove (unmerged
// work), and a branch it asked to delete, for a later settle there. Remove and
// reclone are refused until both are gone; the archive stamp alone proves
// nothing about the disk.
func TestProjectOnDaemonCommands_RemoveAndRecloneRefuseWhileArchivedWorkspacesRetainFiles(t *testing.T) {
	t.Parallel()
	ctx := project.AuthedContext(t)
	here := "d-1"
	archived := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	retained := map[string]*db.CleanupMetadata{
		"no metadata":        nil,
		"directory retained": {HeldReason: "unmerged"},
		"branch pending":     {DirectoryDeleted: true, DeleteBranch: true},
	}
	for name, call := range projectOnDaemonCalls(ctx, "p-1", "d-1") {
		if name == "pull" {
			continue
		}
		for why, meta := range retained {
			t.Run(name+"/"+why, func(t *testing.T) {
				t.Parallel()
				store := onDaemon(t)
				store.ListWorktreesFunc = func(_ context.Context, f db.WorktreeFilters) ([]*db.Worktree, error) {
					if !f.IncludeArchived {
						return nil, nil
					}
					return []*db.Worktree{{ID: "kept", DaemonID: &here, DeletedAt: &archived, CleanupMetadata: meta}}, nil
				}
				deleted := false
				store.DeleteProjectDaemonFunc = func(context.Context, string, string) error { deleted = true; return nil }
				router := newPinnedRecorder()
				err := call(newService(t, store, router))
				requireCode(t, err, connect.CodeFailedPrecondition)
				msg := svcerr.ToConnect(err).Message()
				assert.Contains(t, msg, "1 workspace of")
				assert.Contains(t, msg, "archived")
				assert.Empty(t, router.commands, "nothing is deleted on the machine")
				assert.False(t, deleted, "the row stays")
			})
		}
	}
}

// The retained-workspace guard reads every page of the machine's workspaces:
// ListWorktrees returns at most one page (100 by default), so a guard that
// read only the first refused nothing once the live workspaces on the machine
// sat past it. The mock pages as the store does.
func TestProjectOnDaemonCommands_WorkspaceGuardReadsEveryPage(t *testing.T) {
	t.Parallel()
	ctx := project.AuthedContext(t)
	here, elsewhere := "d-1", "d-2"
	var all []*db.Worktree
	for i := 0; i < 150; i++ {
		all = append(all, &db.Worktree{ID: fmt.Sprintf("other-%d", i), DaemonID: &elsewhere})
	}
	for i := 0; i < 130; i++ {
		all = append(all, &db.Worktree{ID: fmt.Sprintf("live-%d", i), DaemonID: &here})
	}
	for name, call := range projectOnDaemonCalls(ctx, "p-1", "d-1") {
		if name == "pull" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := onDaemon(t)
			store.ListWorktreesFunc = func(_ context.Context, f db.WorktreeFilters) ([]*db.Worktree, error) {
				var rows []*db.Worktree
				for _, wt := range all {
					if f.DaemonID == nil || *wt.DaemonID == *f.DaemonID {
						rows = append(rows, wt)
					}
				}
				limit := f.Limit
				if limit == 0 {
					limit = 100
				}
				rows = rows[min(f.Offset, len(rows)):]
				return rows[:min(limit, len(rows))], nil
			}
			store.DeleteProjectDaemonFunc = func(context.Context, string, string) error {
				t.Error("the row stays")
				return nil
			}
			router := newPinnedRecorder()
			err := call(newService(t, store, router))
			requireCode(t, err, connect.CodeFailedPrecondition)
			assert.Contains(t, svcerr.ToConnect(err).Message(), "130 workspaces of")
			assert.Empty(t, router.commands, "nothing is deleted on the machine")
		})
	}
}

// lockedRecorder records, per daemon command, whether it ran inside the
// checkout's exclusive section.
type lockedRecorder struct {
	*pinnedRecorder
	inLock   *bool
	commands []bool
}

func (r *lockedRecorder) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	r.commands = append(r.commands, *r.inLock)
	return r.pinnedRecorder.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payload, timeoutMs)
}

// Remove and reclone read the machine's workspaces and send the command that
// deletes the checkout inside ONE TearDownProjectCheckout on that checkout
// (its exclusive lock, which a workspace's insert shares, and its pending
// mark; internal/db TestTearDownProjectCheckout_*): a workspace created after
// the guard read cannot land before the delete.
func TestProjectOnDaemonCommands_RemoveAndRecloneGuardAndDeleteUnderTheCheckoutLock(t *testing.T) {
	t.Parallel()
	ctx := project.AuthedContext(t)
	for name, call := range projectOnDaemonCalls(ctx, "p-1", "d-1") {
		if name == "pull" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := onDaemon(t)
			inLock, sections := false, 0
			var settled []bool
			store.TearDownProjectCheckoutFunc = func(ctx context.Context, projectID, daemonID string, guard func(context.Context) error, destroy func(context.Context, bool) bool) error {
				assert.Equal(t, "p-1", projectID)
				assert.Equal(t, "d-1", daemonID)
				sections++
				inLock = true
				defer func() { inLock = false }()
				if err := guard(ctx); err != nil {
					return err
				}
				settled = append(settled, destroy(ctx, false))
				return nil
			}
			var guardInLock []bool
			store.ListWorktreesFunc = func(context.Context, db.WorktreeFilters) ([]*db.Worktree, error) {
				guardInLock = append(guardInLock, inLock)
				return nil, nil
			}
			store.UpsertProjectDaemonFunc = func(context.Context, string, string, string, *string) error { return nil }
			store.DeleteProjectDaemonFunc = func(context.Context, string, string) error { return nil }
			router := &lockedRecorder{pinnedRecorder: newPinnedRecorder(), inLock: &inLock}
			require.NoError(t, call(newService(t, store, router)))
			assert.Equal(t, 1, sections)
			require.NotEmpty(t, guardInLock)
			for _, in := range guardInLock {
				assert.True(t, in, "the workspace guard reads under the checkout lock")
			}
			assert.Equal(t, []bool{true}, router.commands, "the delete runs under the same lock")
			assert.Equal(t, []bool{true}, settled, "an answered delete settles the checkout's workspaces")
		})
	}
}

// A remove or reclone whose wait for the machine ends without an answer (a
// timeout, an interrupted connection, the wait cancelled) leaves its outcome
// unknown: the machine may still be deleting the checkout. It reports the
// round trip unsettled, so TearDownProjectCheckout keeps the checkout's
// workspaces refused and the remove's row stays. An answer, the machine's own
// failure, or a command that never reached it settles it.
func TestProjectOnDaemonCommands_RemoveAndRecloneSettleOnlyOnAKnownOutcome(t *testing.T) {
	t.Parallel()
	ctx := project.AuthedContext(t)
	outcomes := map[string]struct {
		err     error
		settled bool
	}{
		"answered":           {nil, true},
		"machine's failure":  {toolexec.DaemonReportedFailure("git.remove", "remove clone: permission denied"), true},
		"never dispatched":   {fmt.Errorf("send: %w", toolexec.ErrNotDispatched), true},
		"timed out":          {fmt.Errorf("daemon command git.remove via NATS failed: %w: %w", toolexec.ErrDaemonTimeout, context.DeadlineExceeded), false},
		"wait cancelled":     {fmt.Errorf("daemon command git.remove via NATS failed: %w", context.Canceled), false},
		"connection dropped": {fmt.Errorf("daemon command failed: %w", toolexec.ErrDaemonConnectionInterrupted), false},
	}
	for name, call := range projectOnDaemonCalls(ctx, "p-1", "d-1") {
		if name == "pull" {
			continue
		}
		for outcome, want := range outcomes {
			t.Run(name+"/"+outcome, func(t *testing.T) {
				t.Parallel()
				store := onDaemon(t)
				var settled []bool
				store.TearDownProjectCheckoutFunc = func(ctx context.Context, _, _ string, guard func(context.Context) error, destroy func(context.Context, bool) bool) error {
					if err := guard(ctx); err != nil {
						return err
					}
					settled = append(settled, destroy(ctx, false))
					return nil
				}
				deleted := false
				store.DeleteProjectDaemonFunc = func(context.Context, string, string) error { deleted = true; return nil }
				store.UpsertProjectDaemonFunc = func(context.Context, string, string, string, *string) error { return nil }
				router := newPinnedRecorder()
				router.fakeDaemon = func(string, []byte) ([]byte, error) {
					if want.err != nil {
						return nil, want.err
					}
					return []byte(`{"success":true}`), nil
				}
				err := call(newService(t, store, router))
				assert.Equal(t, []bool{want.settled}, settled)
				if want.err == nil {
					require.NoError(t, err)
					return
				}
				require.Error(t, err)
				assert.False(t, deleted, "the row stays while the remove did not report success")
			})
		}
	}
}

// An earlier remove or reclone timed out and left the checkout marked: the
// machine may still be deleting it. A retry clears the mark only when the
// machine says no delete of that checkout is running any more
// (git.await_teardown, answered once every earlier one finished) AND the
// retry's own outcome is known. A retry that never reached the machine, or
// one answered while the earlier delete may still run beside it (the machine
// runs commands concurrently), proves nothing about the earlier one.
func TestProjectOnDaemonCommands_ARetryClearsAnEarlierUnknownTeardownOnlyWhenTheMachineSaysItIsOver(t *testing.T) {
	t.Parallel()
	ctx := project.AuthedContext(t)
	notDispatched := fmt.Errorf("send: %w", toolexec.ErrNotDispatched)
	timedOut := fmt.Errorf("daemon command via NATS failed: %w: %w", toolexec.ErrDaemonTimeout, context.DeadlineExceeded)
	offline := svcerr.WithCause(svcerr.Unavailable("no daemon connected"), notDispatched)
	cases := map[string]struct {
		await, retry error
		awaitAnswer  string
		settled      bool
	}{
		"retry never dispatched":                    {await: notDispatched, retry: notDispatched},
		"machine offline":                           {await: offline, retry: offline},
		"earlier delete still running, retry ran":   {await: timedOut},
		"machine does not know git.await_teardown":  {await: toolexec.DaemonReportedFailure("git.await_teardown", "unknown command type: git.await_teardown")},
		"machine idle, retry timed out":             {awaitAnswer: `{"idle":true}`, retry: timedOut},
		"machine idle, retry failed on the machine": {awaitAnswer: `{"idle":true}`, retry: toolexec.DaemonReportedFailure("git.remove", "permission denied"), settled: true},
		"machine idle, retry answered":              {awaitAnswer: `{"idle":true}`, settled: true},
		"an answer that does not say idle":          {awaitAnswer: `{}`},
	}
	for name, call := range projectOnDaemonCalls(ctx, "p-1", "d-1") {
		if name == "pull" {
			continue
		}
		for c, want := range cases {
			t.Run(name+"/"+c, func(t *testing.T) {
				t.Parallel()
				store := onDaemon(t)
				var settled []bool
				store.TearDownProjectCheckoutFunc = func(ctx context.Context, _, _ string, guard func(context.Context) error, destroy func(context.Context, bool) bool) error {
					if err := guard(ctx); err != nil {
						return err
					}
					settled = append(settled, destroy(ctx, true))
					return nil
				}
				store.DeleteProjectDaemonFunc = func(context.Context, string, string) error { return nil }
				store.UpsertProjectDaemonFunc = func(context.Context, string, string, string, *string) error { return nil }
				router := newPinnedRecorder()
				router.fakeDaemon = func(commandType string, _ []byte) ([]byte, error) {
					if commandType == "git.await_teardown" {
						if want.await != nil {
							return nil, want.await
						}
						return []byte(want.awaitAnswer), nil
					}
					if want.retry != nil {
						return nil, want.retry
					}
					return []byte(`{"success":true}`), nil
				}
				_ = call(newService(t, store, router))
				assert.Equal(t, []bool{want.settled}, settled)
				require.Len(t, router.commands, 2)
				assert.Equal(t, "git.await_teardown", router.commands[0], "the machine is asked about the earlier delete first")
				assert.Equal(t, []string{"d-1", "d-1"}, router.daemons)
				assert.Equal(t, []string{"/clone", "/clone"}, router.paths, "about the same checkout")
			})
		}
	}
}

// With no earlier outcome unknown, the machine is not asked about one.
func TestProjectOnDaemonCommands_NoEarlierUnknownTeardownAsksNothingExtra(t *testing.T) {
	t.Parallel()
	ctx := project.AuthedContext(t)
	for name, call := range projectOnDaemonCalls(ctx, "p-1", "d-1") {
		if name == "pull" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := onDaemon(t)
			store.DeleteProjectDaemonFunc = func(context.Context, string, string) error { return nil }
			store.UpsertProjectDaemonFunc = func(context.Context, string, string, string, *string) error { return nil }
			router := newPinnedRecorder()
			require.NoError(t, call(newService(t, store, router)))
			assert.NotContains(t, router.commands, "git.await_teardown")
		})
	}
}
