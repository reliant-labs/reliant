package project_test

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
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
	return []byte(`{"success":true}`), nil
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
