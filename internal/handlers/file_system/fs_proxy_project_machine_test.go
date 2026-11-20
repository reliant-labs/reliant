package file_system

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// placingRouter is a router whose default resolution names def — project
// blind, as the real one is — and which places any machine it is asked about
// (daemonplacement.Machines). Every pinned command is recorded.
type placingRouter struct {
	machineRecordingRouter
	def string
}

func (r *placingRouter) ResolveDaemonID(context.Context, string) (string, error) { return r.def, nil }

func (r *placingRouter) PlacementDaemonID(_ context.Context, _, daemonID string) (string, error) {
	if daemonID == "" {
		return r.def, nil
	}
	return daemonID, nil
}

// The owner, 2026-11-18: the Files tab with no chat said "reliant-labs isn't
// on <their Mac>" — the account's default machine, which has no checkout —
// while "default" and "cicd" both had one. A request no chat or workspace
// placed is read on a machine that HAS the project: the default when it is
// one of them, else another of the user's.
func TestFileSystemProxy_ChatlessTreeIsReadOnAMachineThatHasIt(t *testing.T) {
	const mac = "46e3682b-33cd-4e88-aecb-52a3a919a354"
	for _, tc := range []struct {
		desc      string
		installed []string
		want      string
	}{
		{"the default lacks it: another machine that has it", []string{"cicd"}, "cicd"},
		{"the default has it: the default", []string{"cicd", mac}, mac},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			repo, projectID, _ := seedFSProxyProjectOn(t, tc.installed...)
			ctx := fsProxyContext()
			require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: mac, UserID: fsProxyTestUser}))

			router := &placingRouter{def: mac}
			svc := newProxy(t, router, repo)
			_, err := svc.GetFileTree(ctx, &reliantv1.GetFileTreeRequest{ProjectId: projectID, Path: "/"})
			require.NoError(t, err)
			assert.Equal(t, []string{tc.want + " fs.get_tree"}, router.sent)
		})
	}
}

// A chat pinned to a machine without the checkout is still refused: the
// chat's files are on its machine. The refusal names the machines that do
// have it, in the detail the panel renders, and nothing is sent.
func TestFileSystemProxy_AChatPinnedToAMachineWithoutTheCheckoutIsRefused(t *testing.T) {
	const mac = "46e3682b-33cd-4e88-aecb-52a3a919a354"
	repo, projectID, _ := seedFSProxyProjectOn(t, "cicd")
	ctx := fsProxyContext()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: mac, UserID: fsProxyTestUser}))
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) // a fixture, not the wall clock
	pinned := mac
	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: projectID, UserID: fsProxyTestUser,
		ActiveDaemonID: &pinned, CreatedAt: now, UpdatedAt: now, LastActive: now}))

	router := &placingRouter{def: mac}
	svc := newProxy(t, router, repo)
	_, err := svc.GetFileTree(ctx, &reliantv1.GetFileTreeRequest{ProjectId: projectID, ChatId: &chatID, Path: "/"})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.CodeV2(err))
	assert.Contains(t, svcerr.ToConnectV2(err).Message(), "switch to")
	assert.Empty(t, router.sent, "a machine without the checkout is never sent a path")

	d := checkoutMissingDetail(t, err)
	assert.Equal(t, mac, d.GetDaemonId())
	assert.Equal(t, []string{"cicd"}, d.GetAvailableDaemonIds())
}

// The machine reached answers that the checkout's root is gone (moved or
// deleted since it reconciled): the detail still lists the user's other
// machines that have the project.
func TestFileSystemProxy_MissingRootListsTheOtherMachinesThatHaveIt(t *testing.T) {
	repo, projectID, _ := seedFSProxyProjectOn(t, "2aab1465", "cicd")
	svc := newProxy(t, &fsMissingDirRouter{daemonID: "2aab1465"}, repo)
	_, err := svc.GetFileTree(fsProxyContext(), &reliantv1.GetFileTreeRequest{ProjectId: projectID})
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.CodeV2(err))

	d := checkoutMissingDetail(t, err)
	other := map[string]string{"2aab1465": "cicd", "cicd": "2aab1465"}[d.GetDaemonId()]
	require.NotEmpty(t, other, "reached one of the machines with a checkout, got %q", d.GetDaemonId())
	assert.Equal(t, []string{other}, d.GetAvailableDaemonIds(), "the other machine, never the one reached")
}

// orgMembership answers every membership question with member.
type orgMembership struct{ member bool }

func (m orgMembership) IsMember(context.Context, string, string) (bool, bool, error) {
	return m.member, true, nil
}

func (orgMembership) DefaultOrg(context.Context, string) (string, error) { return "home", nil }

// A request no chat or worktree placed names only a project id, so the
// project must be the caller's to work in before any checkout is resolved:
// a refusal's detail names the project and its remote. Another user's
// project, and the caller's own when their org membership or credential does
// not reach it, are not found, with no project metadata and nothing sent.
func TestFileSystemProxy_ChatlessRequestForAProjectTheCallerMayNotUseIsNotFound(t *testing.T) {
	const mac = "46e3682b-33cd-4e88-aecb-52a3a919a354"
	org := func(s string) *string { return &s }
	for _, tc := range []struct {
		desc  string
		owner string
		orgID *string
		ctx   func(context.Context) context.Context
	}{
		{"another user's project", "someone-else", nil, func(ctx context.Context) context.Context { return ctx }},
		{"the owner's project in an org they left", fsProxyTestUser, org("acme"), func(ctx context.Context) context.Context {
			return authz.WithMembership(ctx, orgMembership{member: false})
		}},
		{"the owner's project in an org their credential is not bound to", fsProxyTestUser, org("acme"), func(ctx context.Context) context.Context {
			return authz.WithCredentialOrg(authz.WithMembership(ctx, orgMembership{member: true}), "home")
		}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			repo, cleanup := db.SetupTestDB(t)
			t.Cleanup(cleanup)
			setup := authz.WithMembership(context.Background(), authz.NoOrgs{})
			projectID := "fs-proxy-" + uuid.NewString()
			remote := "https://github.com/secret-org/secret-repo"
			require.NoError(t, repo.CreateProject(setup, &db.Project{
				ID: projectID, Name: "secret-project", Path: t.TempDir(), UserID: tc.owner, OrgID: tc.orgID, RemoteURL: &remote,
			}))
			require.NoError(t, repo.UpsertDaemon(setup, &db.Daemon{ID: mac, UserID: fsProxyTestUser}))

			router := &placingRouter{def: mac}
			svc := newProxy(t, router, repo)
			_, err := svc.GetFileTree(tc.ctx(fsProxyContext()), &reliantv1.GetFileTreeRequest{ProjectId: projectID, Path: "/"})
			require.Error(t, err)
			assert.Equal(t, connect.CodeNotFound, svcerr.CodeV2(err))
			cerr := svcerr.ToConnectV2(err)
			assert.Empty(t, cerr.Details(), "no detail for a project the caller may not use")
			msg := cerr.Message()
			assert.NotContains(t, msg, "secret-project")
			assert.NotContains(t, msg, remote)
			assert.Empty(t, router.sent, "nothing is sent for a project the caller may not use")
		})
	}
}
