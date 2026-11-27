package coverage_source_test

import (
	"context"
	"crypto/sha1" //nolint:gosec // git's object id
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/handlers/coverage_source"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/pkg/config"
)

var (
	source = []byte("package shop\n\nfunc Refund() {}\n")
	blob   = gitBlobID(source)
	t0     = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
)

func gitBlobID(content []byte) string {
	h := sha1.New() //nolint:gosec // git's object id
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(content))
	_, _ = h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

func ptr(s string) *string { return &s }

// projects is the database: who owns which project, and which repos hold
// which forge app.
type projects struct {
	owned map[string][]*core.Project // user -> projects
	repos map[string][]*core.Repo    // forge app name -> repos holding it
}

func (p *projects) ListProjects(_ context.Context, f db.ProjectFilters) ([]*db.Project, error) {
	return p.owned[f.UserID], nil
}

func (p *projects) ListReposHoldingForgeApp(_ context.Context, name string, ids []string) ([]*core.Repo, error) {
	in := map[string]bool{}
	for _, id := range ids {
		in[id] = true
	}
	var out []*core.Repo
	for _, r := range p.repos[name] {
		if in[r.ProjectID] {
			out = append(out, r)
		}
	}
	return out, nil
}

// controlPlane is TestsService.HasCoverageBlob: which blobs each project's
// retained reports name, and whom it refuses.
type controlPlane struct {
	mu     sync.Mutex
	blobs  map[string]controlplane.CoverageBlob // project/blob -> answer
	err    error
	asked  []string // token|org|project|blob
	denied map[string]bool
}

func (c *controlPlane) HasCoverageBlob(_ context.Context, token, orgID, project, blobID string) (controlplane.CoverageBlob, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, token+"|"+orgID+"|"+project+"|"+blobID)
	if c.err != nil {
		return controlplane.CoverageBlob{}, c.err
	}
	if c.denied[token] {
		return controlplane.CoverageBlob{}, connect.NewError(connect.CodePermissionDenied, errors.New("needs tests:read"))
	}
	return c.blobs[project+"/"+blobID], nil
}

// gitHub answers a read per (user, owner/repo); it counts reads.
type gitHub struct {
	mu      sync.Mutex
	answers map[string]func() ([]byte, error) // "user owner/repo" -> answer; missing: 404
	reads   []string
}

func (g *gitHub) Read(_ context.Context, userID, owner, repo, blobID string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := userID + " " + owner + "/" + repo
	g.reads = append(g.reads, key+" "+blobID)
	if a, ok := g.answers[key]; ok {
		return a()
	}
	return nil, coverage_source.ErrNotFound
}

func (g *gitHub) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.reads)
}

func serves(content []byte) func() ([]byte, error) {
	return func() ([]byte, error) { return content, nil }
}

func fails(err error) func() ([]byte, error) { return func() ([]byte, error) { return nil, err } }

// orgResolver: everyone's default org is org-1, and they are its members.
type orgResolver struct{}

func (orgResolver) DefaultOrg(context.Context, string) (string, error) { return "org-1", nil }

func (orgResolver) ListScope(_ context.Context, _, requested string, _ orgs.Backfill) (*core.OrgScope, error) {
	if requested != "org-1" {
		return nil, orgs.NotMember(requested)
	}
	return &core.OrgScope{OrgID: requested, IncludeUnassigned: true}, nil
}

type fixture struct {
	svc      *coverage_source.Service
	cp       *controlPlane
	gh       *gitHub
	projects *projects
	clock    *time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ps := &projects{
		owned: map[string][]*core.Project{
			"alice": {{ID: "p-alice", UserID: "alice", Name: "shop", LastActive: t0}},
			"bob":   {{ID: "p-bob", UserID: "bob", Name: "shop", LastActive: t0}},
		},
		repos: map[string][]*core.Repo{
			"shop": {
				{ID: "r-alice", ProjectID: "p-alice", RelativePath: "", RemoteURL: ptr("git@github.com:Acme/Shop.git")},
				{ID: "r-bob", ProjectID: "p-bob", RelativePath: "", RemoteURL: ptr("https://github.com/acme/shop")},
			},
		},
	}
	f := &fixture{
		cp:       &controlPlane{blobs: map[string]controlplane.CoverageBlob{"shop/" + blob: {Present: true}}, denied: map[string]bool{}},
		gh:       &gitHub{answers: map[string]func() ([]byte, error){"alice acme/shop": serves(source), "bob acme/shop": serves(source)}},
		projects: ps,
		clock:    &[]time.Time{t0}[0],
	}
	svc, err := coverage_source.New(coverage_source.Deps{
		Logger:   slog.New(slog.DiscardHandler),
		Config:   &config.Config{},
		Projects: ps,
		Orgs:     orgResolver{},
		Reports:  f.cp,
		Blobs:    f.gh,
		Now:      func() time.Time { return *f.clock },
	})
	require.NoError(t, err)
	f.svc = svc
	return f
}

func as(userID string) context.Context {
	return auth.WithCredential(auth.WithUser(context.Background(), userID, "", ""), auth.CredentialSession)
}

func (f *fixture) get(t *testing.T, userID, project, blobID string) (*pb.GetCoverageSourceResponse, error) {
	t.Helper()
	req := connect.NewRequest(&pb.GetCoverageSourceRequest{Project: project, BlobId: blobID})
	req.Header().Set("Authorization", "Bearer jwt-"+userID)
	resp, err := f.svc.GetCoverageSource(as(userID), req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func reason(t *testing.T, resp *pb.GetCoverageSourceResponse) pb.CoverageSourceUnavailable {
	t.Helper()
	require.NotNil(t, resp)
	require.Nil(t, resp.GetContent(), "no source")
	return resp.GetUnavailable()
}

func TestGetCoverageSource_ReadsTheBlobAsTheViewer(t *testing.T) {
	f := newFixture(t)
	resp, err := f.get(t, "alice", "shop", blob)
	require.NoError(t, err)
	assert.Equal(t, source, resp.GetContent())
	assert.Equal(t, []string{"jwt-alice|org-1|shop|" + blob}, f.cp.asked, "control-plane asked first, as the viewer, in their default org")
	assert.Equal(t, []string{"alice acme/shop " + blob}, f.gh.reads, "the project's remote, read as alice")
}

func TestGetCoverageSource_NeverFetchesWithoutControlPlanesYes(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(f *fixture)
		check func(t *testing.T, resp *pb.GetCoverageSourceResponse, err error)
	}{
		"not in any retained report": {
			setup: func(f *fixture) { f.cp.blobs = nil },
			check: func(t *testing.T, _ *pb.GetCoverageSourceResponse, err error) {
				assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			},
		},
		"another org's project": {
			setup: func(f *fixture) { f.cp.err = connect.NewError(connect.CodeNotFound, errors.New("no such project")) },
			check: func(t *testing.T, _ *pb.GetCoverageSourceResponse, err error) {
				assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			},
		},
		"no tests:read": {
			setup: func(f *fixture) { f.cp.denied["jwt-alice"] = true },
			check: func(t *testing.T, _ *pb.GetCoverageSourceResponse, err error) {
				assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			},
		},
		"control plane without HasCoverageBlob": {
			setup: func(f *fixture) { f.cp.err = fmt.Errorf("x: %w", controlplane.ErrCoverageBlobUnsupported) },
			check: func(t *testing.T, resp *pb.GetCoverageSourceResponse, err error) {
				require.NoError(t, err)
				assert.Equal(t, pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_NOT_YET_AVAILABLE, reason(t, resp))
			},
		},
		"control plane failing": {
			setup: func(f *fixture) { f.cp.err = connect.NewError(connect.CodeUnavailable, errors.New("down")) },
			check: func(t *testing.T, _ *pb.GetCoverageSourceResponse, err error) {
				assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
			},
		},
		"uncommitted": {
			setup: func(f *fixture) {
				f.cp.blobs["shop/"+blob] = controlplane.CoverageBlob{Present: true, Uncommitted: true}
			},
			check: func(t *testing.T, resp *pb.GetCoverageSourceResponse, err error) {
				require.NoError(t, err)
				assert.Equal(t, pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_UNCOMMITTED, reason(t, resp))
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			// A read alice already made is cached: even it is not served
			// without control-plane's answer.
			_, err := f.get(t, "alice", "shop", blob)
			require.NoError(t, err)
			reads := f.gh.count()
			tc.setup(f)
			resp, err := f.get(t, "alice", "shop", blob)
			tc.check(t, resp, err)
			assert.Equal(t, reads, f.gh.count(), "GitHub was not asked")
		})
	}
}

func TestGetCoverageSource_NoControlPlaneIsNotYetAvailable(t *testing.T) {
	gh := &gitHub{}
	svc, err := coverage_source.New(coverage_source.Deps{
		Logger: slog.New(slog.DiscardHandler), Config: &config.Config{}, Projects: &projects{}, Blobs: gh,
	})
	require.NoError(t, err)
	req := connect.NewRequest(&pb.GetCoverageSourceRequest{Project: "shop", BlobId: blob})
	resp, err := svc.GetCoverageSource(as("alice"), req)
	require.NoError(t, err)
	assert.Equal(t, pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_NOT_YET_AVAILABLE, reason(t, resp.Msg))
	assert.Empty(t, gh.reads)
}

func TestGetCoverageSource_NoGitHubRemoteIsNoRepo(t *testing.T) {
	f := newFixture(t)
	f.projects.repos["shop"][0].RemoteURL = ptr("git@gitlab.com:acme/shop.git")
	resp, err := f.get(t, "alice", "shop", blob)
	require.NoError(t, err)
	assert.Equal(t, pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_NO_REPO, reason(t, resp))

	// Nor does someone else's project lend its remote: carol has none.
	resp, err = f.get(t, "carol", "shop", blob)
	require.NoError(t, err)
	assert.Equal(t, pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_NO_REPO, reason(t, resp))
	assert.Empty(t, f.gh.reads)
}

func TestGetCoverageSource_LegacyForgeNameLendsTheProjectsRemote(t *testing.T) {
	f := newFixture(t)
	f.projects.repos = map[string][]*core.Repo{}
	f.projects.owned["alice"][0].IsForge = true
	f.projects.owned["alice"][0].ForgeProjectName = ptr("shop")
	f.projects.owned["alice"][0].RemoteURL = ptr("https://github.com/acme/shop.git")
	resp, err := f.get(t, "alice", "shop", blob)
	require.NoError(t, err)
	assert.Equal(t, source, resp.GetContent())
}

func TestGetCoverageSource_CacheNeverServesAnotherViewer(t *testing.T) {
	f := newFixture(t)
	_, err := f.get(t, "alice", "shop", blob)
	require.NoError(t, err)
	_, err = f.get(t, "alice", "shop", blob)
	require.NoError(t, err)
	assert.Equal(t, 1, f.gh.count(), "alice's second read is hers, from the cache")

	// Bob's GitHub connection is revoked: alice's cached bytes are not his.
	f.gh.answers["bob acme/shop"] = fails(fmt.Errorf("x: %w", ghaccess.ErrNotConnected))
	resp, err := f.get(t, "bob", "shop", blob)
	require.NoError(t, err)
	assert.Equal(t, pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_REVOKED, reason(t, resp))
	assert.Equal(t, 2, f.gh.count(), "bob's read went to GitHub, as bob")

	// The cache is brief: after its TTL alice's read goes to GitHub again.
	*f.clock = t0.Add(10 * time.Minute)
	_, err = f.get(t, "alice", "shop", blob)
	require.NoError(t, err)
	assert.Equal(t, 3, f.gh.count())
}

func TestGetCoverageSource_MapsGitHubsAnswers(t *testing.T) {
	for name, tc := range map[string]struct {
		answer func() ([]byte, error)
		want   pb.CoverageSourceUnavailable
	}{
		"not connected":      {fails(ghaccess.ErrNotConnected), pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_REVOKED},
		"needs reconnect":    {fails(ghaccess.ErrNeedsReconnect), pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_REVOKED},
		"401":                {fails(coverage_source.ErrTokenRejected), pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_REVOKED},
		"404":                {fails(coverage_source.ErrNotFound), pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_GONE},
		"too large":          {fails(coverage_source.ErrTooLarge), pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_TOO_LARGE},
		"bytes not the blob": {serves([]byte("something else\n")), pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_GONE},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.gh.answers["alice acme/shop"] = tc.answer
			resp, err := f.get(t, "alice", "shop", blob)
			require.NoError(t, err)
			assert.Equal(t, tc.want, reason(t, resp))
		})
	}
	t.Run("GitHub unreachable is an error the page shows as unreachable", func(t *testing.T) {
		f := newFixture(t)
		f.gh.answers["alice acme/shop"] = fails(errors.New("ghblobs: GET … answered 502"))
		_, err := f.get(t, "alice", "shop", blob)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	})
}

// A blob id names the same bytes in every repository that has it, so a
// second repository is asked when the first does not show it.
func TestGetCoverageSource_TriesTheNextRepository(t *testing.T) {
	f := newFixture(t)
	f.projects.owned["alice"] = append(f.projects.owned["alice"],
		&core.Project{ID: "p-alice-fork", UserID: "alice", Name: "shop fork", LastActive: t0.Add(-time.Hour)})
	f.projects.repos["shop"] = append(f.projects.repos["shop"],
		&core.Repo{ID: "r-fork", ProjectID: "p-alice-fork", RelativePath: "", RemoteURL: ptr("git@github.com:alice/shop.git")})
	delete(f.gh.answers, "alice acme/shop") // 404: not shown to alice
	f.gh.answers["alice alice/shop"] = serves(source)

	resp, err := f.get(t, "alice", "shop", blob)
	require.NoError(t, err)
	assert.Equal(t, source, resp.GetContent())
	assert.Equal(t, []string{"alice acme/shop " + blob, "alice alice/shop " + blob}, f.gh.reads, "most recently active project first")
}

func TestGetCoverageSource_Refusals(t *testing.T) {
	f := newFixture(t)
	t.Run("a machine credential", func(t *testing.T) {
		ctx := auth.WithCredential(auth.WithUser(context.Background(), "alice", "", ""), auth.CredentialAccessToken)
		_, err := f.svc.GetCoverageSource(ctx, connect.NewRequest(&pb.GetCoverageSourceRequest{Project: "shop", BlobId: blob}))
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})
	t.Run("an org the viewer is not in", func(t *testing.T) {
		req := connect.NewRequest(&pb.GetCoverageSourceRequest{Project: "shop", BlobId: blob, OrgId: ptr("org-2")})
		_, err := f.svc.GetCoverageSource(as("alice"), req)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})
	t.Run("not a blob id", func(t *testing.T) {
		_, err := f.get(t, "alice", "shop", "HEAD")
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})
	assert.Empty(t, f.cp.asked)
	assert.Empty(t, f.gh.reads)
}
