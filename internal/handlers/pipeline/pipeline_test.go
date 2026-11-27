package pipeline_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/handlers/pipeline"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/pkg/config"
)

// These tests run the service over a real database: who may act is the
// handler's, what is enrolled where is the store's.

const (
	org      = "org-1"
	adminID  = "ada"
	memberID = "mia"
	strayID  = "nick" // an admin of another org only
	repoName = "acme/widgets"
	repoGHID = "4242"
	install  = "777"
)

// roles is control-plane's answer: org -> user -> role.
type roles struct {
	mu   sync.Mutex
	role map[string]map[string]string
}

func (r *roles) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.role[orgID][userID] != "", true, nil
}

func (r *roles) DefaultOrg(context.Context, string) (string, error) { return "", nil }

func (r *roles) RoleFresh(_ context.Context, userID, orgID string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.role[orgID][userID], true, nil
}

type env struct {
	t     *testing.T
	repo  *db.Repo
	roles *roles
	svc   *pipeline.Service
	now   time.Time
}

func newEnv(t *testing.T, enabled bool) *env {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	e := &env{t: t, repo: repo, now: time.Now().UTC(), roles: &roles{role: map[string]map[string]string{
		org:     {adminID: orgs.RoleAdmin, memberID: orgs.RoleMember},
		"org-2": {strayID: orgs.RoleAdmin},
	}}}
	e.svc = pipeline.NewTestPipeline(t, pipeline.WithPipelineDeps(pipeline.Deps{
		Logger:   testkit.DiscardLogger(),
		Config:   &config.Config{},
		Pipeline: &configv1.PipelineConfig{Enabled: enabled},
		Store:    repo,
		Now:      func() time.Time { return e.now },
	}))
	e.project("p-ada", adminID, org)
	e.project("p-ada-2", adminID, org)
	e.project("p-mia", memberID, org)
	return e
}

func (e *env) project(id, owner, orgID string) {
	e.t.Helper()
	_, err := e.repo.ORM().Bun().NewRaw(`INSERT INTO projects (id, name, path, user_id, created_at, updated_at, last_active, org_id)
		VALUES (?, ?, ?, ?, now(), now(), now(), ?)`, id, id, "/tmp/"+id, owner, orgID).Exec(context.Background())
	require.NoError(e.t, err)
}

// grant gives user a GitHub access grant to the repository, refreshed at.
func (e *env) grant(user string, at time.Time) {
	e.t.Helper()
	require.NoError(e.t, store.ReplaceIntegrationAccess(context.Background(), e.repo.ORM(), user, "github", "gh-"+user, at,
		[]core.IntegrationAccessGrant{{AccountKey: install, ResourceKey: repoGHID, ResourceLabel: repoName}}))
}

// as is user's request through credential c, with control-plane's
// membership on the context as the API server puts it there.
func (e *env) as(user string, c auth.Credential) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, user)
	return auth.WithCredential(authz.WithMembership(ctx, e.roles), c)
}

func (e *env) person(user string) context.Context { return e.as(user, auth.CredentialSession) }

// token is user's request with the access token p.
func (e *env) token(user string, p *fat.Principal) context.Context {
	return auth.WithMachineToken(e.as(user, auth.CredentialAccessToken), p)
}

func (e *env) enrol(ctx context.Context, project string) (*pb.PipelineRepository, error) {
	resp, err := e.svc.EnrolRepository(ctx, connect.NewRequest(&pb.EnrolRepositoryRequest{ProjectId: project, Repository: repoName}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetRepository(), nil
}

func reasonOf(err error) string {
	r, _ := svcerr.ReasonOf(err)
	return r
}

func TestPipelineServiceIsOffBehindTheSwitch(t *testing.T) {
	e := newEnv(t, false)
	ctx := e.person(adminID)
	_, err := e.enrol(ctx, "p-ada")
	requireSwitchOff(t, err)
	_, err = e.svc.SetPipelineMode(ctx, connect.NewRequest(&pb.SetPipelineModeRequest{RepoId: "r", Mode: "on"}))
	requireSwitchOff(t, err)
	_, err = e.svc.GetPipelineState(ctx, connect.NewRequest(&pb.GetPipelineStateRequest{RepoId: "r"}))
	requireSwitchOff(t, err)
	_, err = e.svc.ListQueue(ctx, connect.NewRequest(&pb.ListQueueRequest{RepoId: "r"}))
	requireSwitchOff(t, err)
	_, err = e.svc.ListCapabilityGrants(ctx, connect.NewRequest(&pb.ListCapabilityGrantsRequest{}))
	requireSwitchOff(t, err)
	_, err = e.svc.RevokeCapabilityGrant(ctx, connect.NewRequest(&pb.RevokeCapabilityGrantRequest{GrantId: "g"}))
	requireSwitchOff(t, err)
}

func requireSwitchOff(t *testing.T, err error) {
	t.Helper()
	require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "%v", err)
	assert.Equal(t, pipeline.ReasonPipelineDisabled, reasonOf(err))
	assert.Contains(t, err.Error(), "pipeline.enabled")
}

func TestEnrolRepository(t *testing.T) {
	e := newEnv(t, true)
	ctx := e.person(adminID)

	_, err := e.enrol(ctx, "p-ada")
	require.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "no GitHub access: %v", err)
	assert.Equal(t, pipeline.ReasonNoRepositoryAccess, reasonOf(err))

	e.grant(adminID, e.now.Add(-2*core.IntegrationAccessFreshness))
	_, err = e.enrol(ctx, "p-ada")
	assert.Equal(t, pipeline.ReasonNoRepositoryAccess, reasonOf(err), "a stale grant is not trusted: %v", err)

	e.grant(adminID, e.now)
	first, err := e.enrol(ctx, "p-ada")
	require.NoError(t, err)
	assert.Equal(t, org, first.GetOrgId())
	assert.Equal(t, "p-ada", first.GetProjectId())
	assert.Equal(t, "github", first.GetProvider())
	assert.Equal(t, install, first.GetAccountKey(), "the installation, as pr_records keys it")
	assert.Equal(t, repoGHID, first.GetRepositoryId())
	assert.Equal(t, repoName, first.GetRepository())
	assert.Equal(t, "off", first.GetMode(), "a new enrolment acts on nothing")
	assert.Equal(t, adminID, first.GetCreatedByUserId())

	again, err := e.enrol(ctx, "p-ada")
	require.NoError(t, err, "enrolling again is idempotent")
	assert.Equal(t, first.GetId(), again.GetId())
	assert.Equal(t, "off", again.GetMode())

	_, err = e.enrol(ctx, "p-ada-2")
	require.Equal(t, connect.CodeAlreadyExists, svcerr.Code(err), "another project's enrolment: %v", err)
	assert.Equal(t, pipeline.ReasonEnrolledElsewhere, reasonOf(err))

	_, err = e.svc.EnrolRepository(ctx, connect.NewRequest(&pb.EnrolRepositoryRequest{ProjectId: "p-ada", Repository: "widgets"}))
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	_, err = e.svc.EnrolRepository(ctx, connect.NewRequest(&pb.EnrolRepositoryRequest{ProjectId: "p-ada", Provider: "gitlab", Repository: repoName}))
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	_, err = e.enrol(ctx, "p-none")
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
}

// Writes need an owner or admin of the org, as a person or with a personal
// access token; machine, automation, OAuth-client and other users' tokens
// are refused whatever the role.
func TestPipelineWritesNeedAnAdminWithAPersonalCredential(t *testing.T) {
	e := newEnv(t, true)
	e.grant(adminID, e.now)
	e.grant(memberID, e.now)

	_, err := e.enrol(e.person(memberID), "p-mia")
	require.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "a member: %v", err)
	assert.Equal(t, orgs.ReasonOrgAdminRequired, reasonOf(err))

	personal := &fat.Principal{TokenID: "tok-1", ActingUserID: adminID, Scopes: fat.SetOf(fat.ScopeReliantAPI)}
	refused := map[string]context.Context{
		"machine-bound token": e.token(adminID, &fat.Principal{TokenID: "tok-2", ActingUserID: adminID,
			Resource: &fat.Resource{Kind: fat.ResourceDaemon, ID: "d-1"}}),
		"daemon wildcard token":   e.token(adminID, &fat.Principal{TokenID: "tok-3", ActingUserID: adminID, AllScopes: true}),
		"org automation token":    e.token(adminID, &fat.Principal{TokenID: "tok-4", OrgID: org, Scopes: fat.SetOf(fat.ScopeReliantAPI)}),
		"another user's token":    e.token(adminID, &fat.Principal{TokenID: "tok-5", ActingUserID: memberID, Scopes: fat.SetOf(fat.ScopeReliantAPI)}),
		"OAuth client's token":    e.token(adminID, &fat.Principal{TokenID: "tok-6", ActingUserID: adminID, ClientID: "mcp", Scopes: fat.SetOf(fat.ScopeReliantAPI)}),
		"access token, no record": e.as(adminID, auth.CredentialAccessToken),
		"OAuth client JWT":        e.as(adminID, auth.CredentialOAuthClient),
		"identity token":          e.as(adminID, auth.CredentialIdentityToken),
		"unclassified":            e.as(adminID, auth.CredentialUnknown),
	}
	for name, ctx := range refused {
		_, err := e.enrol(ctx, "p-ada")
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "%s: %v", name, err)
		assert.Equal(t, pipeline.ReasonPersonalCredentialRequired, reasonOf(err), name)
	}

	r, err := e.enrol(e.token(adminID, personal), "p-ada")
	require.NoError(t, err, "an admin's personal access token (the CLI login)")
	_, err = e.svc.SetPipelineMode(e.token(adminID, personal), connect.NewRequest(&pb.SetPipelineModeRequest{
		RepoId: r.GetId(), Mode: "shadow", ExpectedVersion: r.GetVersion()}))
	require.NoError(t, err)

	for name, ctx := range refused {
		_, err := e.svc.SetPipelineMode(ctx, connect.NewRequest(&pb.SetPipelineModeRequest{RepoId: r.GetId(), Mode: "on", ExpectedVersion: r.GetVersion() + 1}))
		assert.Equal(t, connect.CodePermissionDenied, svcerr.Code(err), "%s: %v", name, err)
	}
	_, err = e.svc.SetPipelineMode(e.person(memberID), connect.NewRequest(&pb.SetPipelineModeRequest{RepoId: r.GetId(), Mode: "on", ExpectedVersion: r.GetVersion() + 1}))
	assert.Equal(t, orgs.ReasonOrgAdminRequired, reasonOf(err), "a member: %v", err)
	_, err = e.svc.SetPipelineMode(e.person(strayID), connect.NewRequest(&pb.SetPipelineModeRequest{RepoId: r.GetId(), Mode: "on", ExpectedVersion: r.GetVersion() + 1}))
	assert.Equal(t, orgs.ReasonOrgAdminRequired, reasonOf(err), "another org's admin: %v", err)
}

func TestSetPipelineModeIsACompareAndSet(t *testing.T) {
	e := newEnv(t, true)
	e.grant(adminID, e.now)
	ctx := e.person(adminID)
	r, err := e.enrol(ctx, "p-ada")
	require.NoError(t, err)

	set := func(mode string, version int64) (*pb.PipelineRepository, error) {
		resp, err := e.svc.SetPipelineMode(ctx, connect.NewRequest(&pb.SetPipelineModeRequest{RepoId: r.GetId(), Mode: mode, ExpectedVersion: version}))
		if err != nil {
			return nil, err
		}
		return resp.Msg.GetRepository(), nil
	}
	shadow, err := set("shadow", r.GetVersion())
	require.NoError(t, err)
	assert.Equal(t, "shadow", shadow.GetMode())
	assert.Greater(t, shadow.GetVersion(), r.GetVersion())

	_, err = set("on", r.GetVersion())
	require.Equal(t, connect.CodeAborted, svcerr.Code(err), "a stale version: %v", err)
	got, err := e.repo.GetPipelineRepo(context.Background(), r.GetId())
	require.NoError(t, err)
	assert.Equal(t, core.PipelineRepoModeShadow, got.Mode, "a lost race changes nothing")

	_, err = set("bogus", shadow.GetVersion())
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	_, err = e.svc.SetPipelineMode(ctx, connect.NewRequest(&pb.SetPipelineModeRequest{RepoId: "none", Mode: "on"}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))

	on, err := set("on", shadow.GetVersion())
	require.NoError(t, err)
	assert.Equal(t, "on", on.GetMode())
}

func TestPipelineReadsNeedOrgMembership(t *testing.T) {
	e := newEnv(t, true)
	e.grant(adminID, e.now)
	r, err := e.enrol(e.person(adminID), "p-ada")
	require.NoError(t, err)

	// A batch, an entry in it and main's health, so the reads have rows.
	ctx := context.Background()
	_, err = e.repo.ORM().Bun().NewRaw(`INSERT INTO pr_records (id, provider, account_key, repository_id, repository, number, state,
		head_sha, snapshot_at, observed_at) VALUES ('rec-1', 'github', ?, ?, ?, 17, 'open', 'aaaa', now(), now())`,
		install, repoGHID, repoName).Exec(ctx)
	require.NoError(t, err)
	_, _, err = e.repo.EnsurePRPipeline(ctx, core.PRPipelineNew{RecordID: "rec-1", OrgID: org, ProjectID: "p-ada", RepoID: r.GetId(),
		State: core.PRPipelineAwaitingReview, StateHeadSHA: "aaaa"})
	require.NoError(t, err)
	_, _, err = e.repo.EnqueueQueueEntry(ctx, core.QueueEntryNew{ID: "q-1", OrgID: org, ProjectID: "p-ada", RepoID: r.GetId(),
		RecordID: "rec-1", HeadSHA: "aaaa", Priority: 50})
	require.NoError(t, err)
	batch, err := e.repo.CreateQueueBatch(ctx, core.QueueBatchNew{ID: "b-1", OrgID: org, ProjectID: "p-ada", RepoID: r.GetId(), BaseSHA: "bbbb"})
	require.NoError(t, err)
	_, err = e.repo.InsertMainHealth(ctx, core.MainHealthRecord{OrgID: org, ProjectID: "p-ada", RepoID: r.GetId(), SHA: "bbbb",
		Source: core.AllMainHealthSources[0], Verdict: core.AllMainHealthVerdicts[0]})
	require.NoError(t, err)

	for _, reader := range []string{adminID, memberID} {
		byName, err := e.svc.GetPipelineState(e.person(reader), connect.NewRequest(&pb.GetPipelineStateRequest{Repository: "ACME/Widgets"}))
		require.NoError(t, err, reader)
		assert.Equal(t, r.GetId(), byName.Msg.GetRepository().GetId())
		require.Len(t, byName.Msg.GetPrStates(), 1)
		assert.Equal(t, "awaiting_review", byName.Msg.GetPrStates()[0].GetState())
		assert.Equal(t, int32(1), byName.Msg.GetPrStates()[0].GetCount())
		require.Len(t, byName.Msg.GetLatestBatches(), 1)
		assert.Equal(t, batch.ID, byName.Msg.GetLatestBatches()[0].GetId())
		assert.Equal(t, "bbbb", byName.Msg.GetMainHealth().GetSha())

		q, err := e.svc.ListQueue(e.person(reader), connect.NewRequest(&pb.ListQueueRequest{RepoId: r.GetId()}))
		require.NoError(t, err, reader)
		require.Len(t, q.Msg.GetEntries(), 1)
		assert.Equal(t, int32(17), q.Msg.GetEntries()[0].GetNumber())
		assert.Equal(t, "waiting", q.Msg.GetEntries()[0].GetState())
		assert.Equal(t, batch.ID, q.Msg.GetCurrentBatch().GetId(), "a building batch is current")
	}

	_, err = e.svc.GetPipelineState(e.person(strayID), connect.NewRequest(&pb.GetPipelineStateRequest{RepoId: r.GetId()}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "a non-member: %v", err)
	_, err = e.svc.GetPipelineState(e.person(strayID), connect.NewRequest(&pb.GetPipelineStateRequest{Repository: repoName}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "a non-member, by name: %v", err)
	_, err = e.svc.ListQueue(e.person(strayID), connect.NewRequest(&pb.ListQueueRequest{RepoId: r.GetId()}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "a non-member: %v", err)
	_, err = e.svc.GetPipelineState(e.person(adminID), connect.NewRequest(&pb.GetPipelineStateRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}
