// Copyright (c) 2025 Reliant Labs
package project

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"
	"github.com/reliant-labs/reliant/internal/store"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// unreadableMachines is a repository whose machine reads fail (not "no
// such machine": an outage).
type unreadableMachines struct{ *db.Repo }

func (unreadableMachines) GetDaemon(context.Context, string) (*db.Daemon, error) {
	return nil, errors.New("connection reset")
}

// Review B4: CreateProjectFromRepo authorizes the machine it clones onto
// before the clone is dispatched. A failed read of the machine is the
// request's failure, never a skipped check; another user's machine is
// NotFound; a machine of another org than the request's is refused. In each
// case nothing is cloned and nothing is persisted. A machine reliant has never
// registered (it never connected) is still cloned onto: the control plane
// decides whose it is.
func TestCreateProjectFromRepo_AuthorizesTheMachineBeforeTheClone(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	userID := "user-from-repo-org-" + uuid.NewString()
	resolver := orgs.New(twoOrgs{userID: userID})
	ctx := authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, userID), resolver)
	globex := "globex"
	globexMachine := "globex-machine-" + uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: globexMachine, UserID: userID, OrgID: &globex}))
	theirs := "their-machine-" + uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: theirs, UserID: "someone-else"}))

	clone := func(t *testing.T, r Repository, daemonID, org string) (*cloneStubControlPlane, error) {
		t.Helper()
		cp := &cloneStubControlPlane{result: controlplane.CloneRepoResult{ClonedPath: "/home/workspace/projects/w-" + uuid.NewString(), Queued: true}}
		// The machine read is Deps.Machines, always set here: a repository that
		// fails the read fails it there too, and every other one reads the
		// machine from its own database.
		var machines DaemonReader
		if u, ok := r.(unreadableMachines); ok {
			machines = u
		} else {
			machines = store.NewDaemonExtStore(repo.ORM())
		}
		svc := NewTestProject(t, WithProjectDeps(withStores(Deps{Repo: r, Machines: machines, Projects: projectaccesstest.For(r), Router: &createProjectRowRouter{resolveID: daemonID}, ControlPlane: cp, Orgs: resolver})))
		_, err := svc.CreateProjectFromRepo(ctx, connect.NewRequest(&reliantv1.CreateProjectFromRepoRequest{
			CloneUrl: "https://github.com/acme/w-" + uuid.NewString() + ".git", DaemonId: daemonID, OrgId: &org,
		}))
		return cp, err
	}

	cp, err := clone(t, unreadableMachines{repo}, globexMachine, "globex")
	assert.Equal(t, connect.CodeInternal, codeOf(err), "%v", err)
	assert.Zero(t, cp.calls, "cloned with the machine's org unchecked")

	cp, err = clone(t, repo, theirs, "acme")
	assert.Equal(t, connect.CodeNotFound, codeOf(err), "%v", err)
	assert.Zero(t, cp.calls)

	cp, err = clone(t, repo, globexMachine, "acme")
	assert.Equal(t, connect.CodeFailedPrecondition, codeOf(err), "%v", err)
	assert.Contains(t, err.Error(), "organization globex")
	assert.Zero(t, cp.calls)

	cp, err = clone(t, repo, globexMachine, "globex")
	require.NoError(t, err)
	assert.Equal(t, 1, cp.calls)

	cp, err = clone(t, repo, "never-connected-"+uuid.NewString(), "acme")
	require.NoError(t, err, "a machine reliant never registered is the control plane's to decide")
	assert.Equal(t, 1, cp.calls)
}
