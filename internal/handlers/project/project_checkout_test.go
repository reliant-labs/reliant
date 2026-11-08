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
