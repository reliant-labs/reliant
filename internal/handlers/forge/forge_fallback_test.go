// Copyright (c) 2025 Reliant Labs
package forge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A read about the forge project (topology, env status, verify) whose machine
// is not reachable is asked of another checkout of the same forge project. The
// prod incident: the console's Reliant project was the one created on the
// owner's laptop, checked out only on two laptops, both asleep, while their
// cloud machines held the same forge project under a second Reliant project.
// Every GetTopology answered 503 "no daemon connected".

const (
	macPath   = "/Users/owner/src/reliant-labs/control-plane"
	cloudPath = "/home/workspace/projects/reliant-labs/control-plane"
)

// notConnected is what a pinned command to a machine with no NATS responder
// fails with.
var notConnected = errors.New("daemon command forge.topology failed: unavailable: no daemon connected for user")

type fallbackProject struct {
	id        string
	forgeName string // "" is not a forge project
	installs  []*core.ProjectDaemon
}

func installed(daemonID, path string) *core.ProjectDaemon {
	return &core.ProjectDaemon{DaemonID: daemonID, Path: path, InstallState: core.ProjectInstallInstalled}
}

// fallbackRepo holds the test user's projects, listed in the given order (the
// store's: most recently active first). A machine whose id starts "theirs-"
// belongs to another user.
func fallbackRepo(projects ...fallbackProject) *MockRepository {
	row := func(p fallbackProject) *db.Project {
		out := &db.Project{ID: p.id, Name: "control-plane", Path: macPath}
		if p.forgeName != "" {
			name := p.forgeName
			out.IsForge, out.ForgeProjectName = true, &name
		}
		return out
	}
	byID := map[string]fallbackProject{}
	for _, p := range projects {
		byID[p.id] = p
	}
	return &MockRepository{
		GetProjectWithUserCheckFunc: func(_ context.Context, id, _ string) (*db.Project, error) {
			p, ok := byID[id]
			if !ok {
				return nil, svcerr.NotFound("project not found")
			}
			return row(p), nil
		},
		SetProjectForgeNameFunc: func(context.Context, string, string, string) (bool, error) { return false, nil },
		ListProjectsFunc: func(_ context.Context, filters db.ProjectFilters) ([]*db.Project, error) {
			if filters.UserID != "test-user" {
				return nil, fmt.Errorf("listed another user's projects: %q", filters.UserID)
			}
			out := make([]*db.Project, 0, len(projects))
			for _, p := range projects {
				out = append(out, row(p))
			}
			return out, nil
		},
		ListProjectDaemonsForProjectFunc: func(_ context.Context, projectID string) ([]*core.ProjectDaemon, error) {
			var out []*core.ProjectDaemon
			for _, r := range byID[projectID].installs {
				cp := *r
				cp.ProjectID = projectID
				out = append(out, &cp)
			}
			return out, nil
		},
		GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
			owner := "test-user"
			if strings.HasPrefix(id, "theirs-") {
				owner = "someone-else"
			}
			return &db.Daemon{ID: id, UserID: owner}, nil
		},
	}
}

func asleep(ids ...string) map[string]error {
	out := map[string]error{}
	for _, id := range ids {
		out[id] = notConnected
	}
	return out
}

// The incident, end to end.
func TestForgeService_TopologyIsAskedOfAnotherCheckoutWhenTheMachineIsAsleep(t *testing.T) {
	repo := fallbackRepo(
		fallbackProject{id: "proj-1", forgeName: "control-plane", installs: []*core.ProjectDaemon{installed("mac-a", macPath), installed("mac-b", macPath)}},
		fallbackProject{id: "proj-2", forgeName: "control-plane", installs: []*core.ProjectDaemon{installed("mac-a", macPath), installed("cloud", cloudPath)}},
	)
	router := &forgeTestRouter{
		reply:       forgeDaemonReply(t, true, true, 0, `{"project":"control-plane"}`),
		replyErrFor: asleep("mac-a", "mac-b"),
	}

	res, err := newForgeService(router, repo).GetTopology(authedCtx(),
		connect.NewRequest(&reliantv1.GetForgeTopologyRequest{ProjectId: "proj-1"}))

	require.NoError(t, err)
	assert.True(t, res.Msg.GetMeta().GetIsForgeProject())
	assert.Equal(t, []string{"mac-a", "mac-b", "cloud"}, router.asked,
		"the project's own checkouts first, then the other project's; the asleep mac-a is asked once")
	assert.Equal(t, cloudPath, sentProjectPath(t, router), "the answering machine is sent its own path")
}

func TestForgeService_TheProjectsOwnOtherCheckoutAnswersFirst(t *testing.T) {
	repo := fallbackRepo(
		fallbackProject{id: "proj-1", forgeName: "control-plane", installs: []*core.ProjectDaemon{installed("mac", macPath), installed("cloud-1", cloudPath)}},
		fallbackProject{id: "proj-2", forgeName: "control-plane", installs: []*core.ProjectDaemon{installed("cloud-2", "/elsewhere")}},
	)
	router := &forgeTestRouter{reply: forgeDaemonReply(t, true, true, 0, `{}`), replyErrFor: asleep("mac")}

	require.NoError(t, topology(newForgeService(router, repo)))

	assert.Equal(t, []string{"mac", "cloud-1"}, router.asked)
}

// Only a checkout of the SAME forge project, on one of the caller's own
// machines, may answer.
func TestForgeService_FallbackStaysWithinTheForgeProjectAndTheCallersMachines(t *testing.T) {
	repo := fallbackRepo(
		fallbackProject{id: "proj-1", forgeName: "control-plane", installs: []*core.ProjectDaemon{installed("mac", macPath)}},
		fallbackProject{id: "other-app", forgeName: "trader", installs: []*core.ProjectDaemon{installed("cloud-trader", "/t")}},
		fallbackProject{id: "plain", installs: []*core.ProjectDaemon{installed("cloud-plain", "/p")}},
		fallbackProject{id: "proj-3", forgeName: "control-plane", installs: []*core.ProjectDaemon{installed("theirs-cloud", cloudPath)}},
	)
	router := &forgeTestRouter{reply: forgeDaemonReply(t, true, true, 0, `{}`), replyErrFor: asleep("mac")}

	err := topology(newForgeService(router, repo))

	assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err), "nothing else could answer: the machine's own state is the answer")
	assert.Equal(t, []string{"mac"}, router.asked)
}

// A project whose forge name is not known yet still tries its own other
// checkouts, and never guesses at another project's.
func TestForgeService_AnUnnamedProjectFallsBackOnlyToItsOwnCheckouts(t *testing.T) {
	repo := fallbackRepo(
		fallbackProject{id: "proj-1", installs: []*core.ProjectDaemon{installed("mac", macPath), installed("cloud", cloudPath)}},
		fallbackProject{id: "proj-2", forgeName: "control-plane", installs: []*core.ProjectDaemon{installed("cloud-2", cloudPath)}},
	)
	router := &forgeTestRouter{reply: forgeDaemonReply(t, true, true, 0, `{}`), replyErrFor: asleep("mac", "cloud")}

	err := topology(newForgeService(router, repo))

	assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
	assert.Equal(t, []string{"mac", "cloud"}, router.asked)
}

// Only "not reachable" moves on. A machine that answered with a failure has
// answered.
func TestForgeService_AnAnsweredFailureIsNotRetriedElsewhere(t *testing.T) {
	repo := fallbackRepo(
		fallbackProject{id: "proj-1", forgeName: "control-plane", installs: []*core.ProjectDaemon{installed("mac", macPath)}},
		fallbackProject{id: "proj-2", forgeName: "control-plane", installs: []*core.ProjectDaemon{installed("cloud", cloudPath)}},
	)
	router := &forgeTestRouter{replyErrFor: map[string]error{
		"mac": toolexec.DaemonReportedFailure("forge.topology", "forge command failed: exit 1: kcl render error"),
	}}

	err := topology(newForgeService(router, repo))

	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Equal(t, []string{"mac"}, router.asked)
}

// The reads about the forge project fall back; nothing else does. A deploy's
// status lives on the machine that started it, secrets are the checkout's own
// store, and a write ships the chosen machine's working tree.
func TestForgeService_OnlyForgeProjectReadsFallBack(t *testing.T) {
	newRepo := func() *MockRepository {
		return fallbackRepo(
			fallbackProject{id: "proj-1", forgeName: "control-plane", installs: []*core.ProjectDaemon{installed("mac", macPath)}},
			fallbackProject{id: "proj-2", forgeName: "control-plane", installs: []*core.ProjectDaemon{installed("cloud", cloudPath)}},
		)
	}
	rpcs := []struct {
		name      string
		call      func(*Service) error
		fallsBack bool
	}{
		{"GetTopology", func(s *Service) error {
			_, err := s.GetTopology(authedCtx(), connect.NewRequest(&reliantv1.GetForgeTopologyRequest{ProjectId: "proj-1"}))
			return err
		}, true},
		{"GetEnvStatus", func(s *Service) error {
			_, err := s.GetEnvStatus(authedCtx(), connect.NewRequest(&reliantv1.GetForgeEnvStatusRequest{ProjectId: "proj-1", Env: "prod"}))
			return err
		}, true},
		{"VerifyEnv", func(s *Service) error {
			_, err := s.VerifyEnv(authedCtx(), connect.NewRequest(&reliantv1.VerifyForgeEnvRequest{ProjectId: "proj-1", Env: "prod"}))
			return err
		}, true},
		{"ListSecrets", func(s *Service) error {
			_, err := s.ListSecrets(authedCtx(), connect.NewRequest(&reliantv1.ListForgeSecretsRequest{ProjectId: "proj-1", Env: "prod"}))
			return err
		}, false},
		{"GetDeployStatus", func(s *Service) error {
			_, err := s.GetDeployStatus(authedCtx(), connect.NewRequest(&reliantv1.GetForgeDeployStatusRequest{ProjectId: "proj-1", Handle: "h-1"}))
			return err
		}, false},
	}
	for _, rpc := range rpcs {
		t.Run(rpc.name, func(t *testing.T) {
			router := &forgeTestRouter{reply: forgeDaemonReply(t, true, true, 0, `{}`), replyErrFor: asleep("mac")}
			err := rpc.call(newForgeService(router, newRepo()))
			if rpc.fallsBack {
				require.NoError(t, err)
				assert.Equal(t, []string{"mac", "cloud"}, router.asked)
				return
			}
			assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
			assert.Equal(t, []string{"mac"}, router.asked)
		})
	}
}
