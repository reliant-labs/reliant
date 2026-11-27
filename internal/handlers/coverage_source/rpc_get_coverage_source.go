package coverage_source

import (
	"context"
	"crypto/sha1" //nolint:gosec // git's object id, not a security boundary
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/githubrepo"
	"github.com/reliant-labs/reliant/internal/integrations/ghaccess"
	"github.com/reliant-labs/reliant/internal/orgs"
)

const (
	// projectsLimit bounds the viewer's projects searched for the forge
	// project's remote.
	projectsLimit = 500
	// maxRepos bounds the GitHub repositories one read tries. A blob id
	// names the same bytes in every repository that has it, so any of them
	// answers; a second is asked only when the first does not show it.
	maxRepos = 3
)

// GetCoverageSource reads one covered file's bytes from GitHub, as the
// viewer, after control-plane confirms a retained coverage report of the
// forge project names the blob. Every reason there are none to show is an
// answer, not an error.
func (s *Service) GetCoverageSource(
	ctx context.Context,
	req *connect.Request[pb.GetCoverageSourceRequest],
) (*connect.Response[pb.GetCoverageSourceResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Wrap(svcerr.Unauthenticated("user ID not found in context"))
	}
	// The blob is read with the VIEWER's GitHub token, so the viewer is a
	// person: a machine credential holding tests:read must not read its
	// owner's code through their GitHub connection.
	if err := auth.RequirePerson(ctx, "read a covered file's source"); err != nil {
		return nil, svcerr.Wrap(err)
	}
	project := strings.TrimSpace(req.Msg.GetProject())
	blobID := strings.ToLower(strings.TrimSpace(req.Msg.GetBlobId()))
	if project == "" {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("project is required"))
	}
	if !isBlobID(blobID) {
		return nil, svcerr.Wrap(svcerr.InvalidArgument("blob_id must be a git object id (40 or 64 hex digits)"))
	}
	scope, orgID, err := s.org(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	// Fail closed: nothing is fetched without control-plane's answer.
	if s.deps.Reports == nil || s.deps.Blobs == nil {
		return unavailable(pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_NOT_YET_AVAILABLE), nil
	}
	bearer := auth.BearerToken(req.Header().Get("Authorization"))
	found, err := s.deps.Reports.HasCoverageBlob(ctx, bearer, orgID, project, blobID)
	switch {
	case errors.Is(err, controlplane.ErrCoverageBlobUnsupported):
		return unavailable(pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_NOT_YET_AVAILABLE), nil
	case err != nil:
		return nil, reportsRefusal(ctx, err)
	case !found.Present:
		return nil, svcerr.Wrap(svcerr.NotFound("coverage blob"))
	case found.Uncommitted:
		return unavailable(pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_UNCOMMITTED), nil
	}

	if content, ok := s.cache.get(userID, blobID); ok {
		return contentResponse(content), nil
	}
	repos, err := s.gitHubRepos(ctx, userID, scope, project)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if len(repos) == 0 {
		return unavailable(pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_NO_REPO), nil
	}
	var failed error
	for _, r := range repos {
		content, err := s.deps.Blobs.Read(ctx, userID, r.owner, r.repo, blobID)
		switch {
		case err == nil && blobIDOf(blobID, content) == blobID:
			s.cache.put(userID, blobID, content)
			return contentResponse(content), nil
		case err == nil:
			// Bytes that are not the blob are no answer; never cached.
			s.deps.Logger.WarnContext(ctx, "coverage source: GitHub answered other bytes for a blob id",
				"owner", r.owner, "repo", r.repo, "blob_id", blobID)
		case errors.Is(err, ghaccess.ErrNotConnected), errors.Is(err, ghaccess.ErrNeedsReconnect), errors.Is(err, ErrTokenRejected):
			return unavailable(pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_REVOKED), nil
		case errors.Is(err, ErrTooLarge):
			return unavailable(pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_TOO_LARGE), nil
		case errors.Is(err, ErrNotFound), errors.Is(err, ErrInvalid):
		default:
			failed = err
		}
	}
	if failed != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, svcerr.Wrap(ctxErr)
		}
		return nil, svcerr.Wrap(svcerr.WithCause(svcerr.Unavailable("GitHub did not answer; try again"), failed))
	}
	return unavailable(pb.CoverageSourceUnavailable_COVERAGE_SOURCE_UNAVAILABLE_GONE), nil
}

// org is the org the read acts in, and its project scope: the named org,
// which the viewer must be a live member of, else their default org. A
// deployment with no orgs is no scope and org "".
func (s *Service) org(ctx context.Context, userID, requested string) (*core.OrgScope, string, error) {
	if s.deps.Orgs == nil {
		return nil, strings.TrimSpace(requested), nil
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		def, err := s.deps.Orgs.DefaultOrg(ctx, userID)
		if err != nil {
			return nil, "", orgs.LookupFailed(ctx, "could not resolve your default organization; try again", err)
		}
		requested = def
	}
	// A read changes nothing: no backfill stamp.
	scope, err := s.deps.Orgs.ListScope(ctx, userID, requested, nil)
	if err != nil {
		return nil, "", err
	}
	if scope == nil {
		return nil, requested, nil
	}
	return scope, scope.OrgID, nil
}

// reportsRefusal is control-plane's refusal as this rpc's answer: no
// tests:read is PermissionDenied, a project not of the org NotFound, and
// anything else Unavailable.
func reportsRefusal(ctx context.Context, err error) error {
	switch connect.CodeOf(err) {
	case connect.CodePermissionDenied, connect.CodeUnauthenticated:
		return svcerr.Wrap(svcerr.WithCause(svcerr.PermissionDenied(
			"Reading this project's coverage needs the tests:read permission in its organization."), err))
	case connect.CodeNotFound:
		return svcerr.Wrap(svcerr.WithCause(svcerr.NotFound("coverage blob"), err))
	case connect.CodeCanceled, connect.CodeDeadlineExceeded:
		if ctxErr := ctx.Err(); ctxErr != nil {
			return svcerr.Wrap(ctxErr)
		}
	}
	return svcerr.Wrap(svcerr.WithCause(svcerr.Unavailable("the control plane could not check the coverage report; try again"), err))
}

// gitHubRepo is one github.com repository a blob may be read from.
type gitHubRepo struct{ owner, repo string }

// gitHubRepos is every github.com repository one of the viewer's own projects
// in the org holds the forge project in: a repo whose daemon scan recorded a
// forge.yaml of that name (repo_forge_apps), or a project whose root
// forge.yaml declares it (the legacy forge_project_name). Most recently
// active project first, each repository once, at most maxRepos.
//
// Only the viewer's OWN projects lend a remote. A project shared with them
// does not: where a project lives is its owner's to see (a viewer's project
// read redacts its remotes), so a viewer with no project of their own for
// the forge project is NO_REPO.
func (s *Service) gitHubRepos(ctx context.Context, userID string, scope *core.OrgScope, project string) ([]gitHubRepo, error) {
	// ListProjects by UserID is the viewer's own projects, never ones shared
	// with them.
	candidates, err := s.deps.Projects.ListProjects(ctx, db.ProjectFilters{UserID: userID, Org: scope, Limit: projectsLimit})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list projects"))
	}
	readable := make([]*core.Project, 0, len(candidates))
	byID := make(map[string]*core.Project, len(candidates))
	for _, p := range authz.Visible(ctx, userID, candidates, authz.CanReadProject) {
		if p == nil || byID[p.ID] != nil {
			continue
		}
		byID[p.ID] = p
		readable = append(readable, p)
	}
	if len(readable) == 0 {
		return nil, nil
	}
	sort.SliceStable(readable, func(i, j int) bool {
		a, b := readable[i], readable[j]
		if !a.LastActive.Equal(b.LastActive) {
			return a.LastActive.After(b.LastActive)
		}
		return a.ID < b.ID
	})
	ids := make([]string, len(readable))
	for i, p := range readable {
		ids[i] = p.ID
	}
	repos, err := s.deps.Projects.ListReposHoldingForgeApp(ctx, project, ids)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list the projects' repos"))
	}
	remotes := make(map[string][]string, len(readable))
	for _, r := range repos {
		p := byID[r.ProjectID]
		if p == nil {
			continue
		}
		switch {
		case r.RemoteURL != nil && strings.TrimSpace(*r.RemoteURL) != "":
			remotes[p.ID] = append(remotes[p.ID], *r.RemoteURL)
		case strings.Trim(r.RelativePath, "/.") == "" && p.RemoteURL != nil:
			// The root repo of a project that recorded its remote only on
			// the project.
			remotes[p.ID] = append(remotes[p.ID], *p.RemoteURL)
		}
	}
	var out []gitHubRepo
	seen := map[gitHubRepo]bool{}
	for _, p := range readable {
		urls := remotes[p.ID]
		if p.ForgeProjectName != nil && strings.TrimSpace(*p.ForgeProjectName) == project && p.RemoteURL != nil {
			urls = append(urls, *p.RemoteURL)
		}
		for _, u := range urls {
			owner, repo, ok := githubrepo.Of(u)
			if !ok {
				continue
			}
			r := gitHubRepo{owner: owner, repo: repo}
			if seen[r] {
				continue
			}
			seen[r] = true
			out = append(out, r)
			if len(out) == maxRepos {
				return out, nil
			}
		}
	}
	return out, nil
}

// isBlobID reports whether id is a lower-case git object id.
func isBlobID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && id == strings.ToLower(id)
}

// blobIDOf is git's id of content as a blob, in the hash the id's length
// names: SHA-1 for 40 hex digits, SHA-256 for 64.
func blobIDOf(id string, content []byte) string {
	var h hash.Hash
	if len(id) == 64 {
		h = sha256.New()
	} else {
		h = sha1.New() //nolint:gosec // git's object id
	}
	_, _ = fmt.Fprintf(h, "blob %s\x00", strconv.Itoa(len(content)))
	_, _ = h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

func unavailable(reason pb.CoverageSourceUnavailable) *connect.Response[pb.GetCoverageSourceResponse] {
	return connect.NewResponse(&pb.GetCoverageSourceResponse{Result: &pb.GetCoverageSourceResponse_Unavailable{Unavailable: reason}})
}

func contentResponse(content []byte) *connect.Response[pb.GetCoverageSourceResponse] {
	return connect.NewResponse(&pb.GetCoverageSourceResponse{Result: &pb.GetCoverageSourceResponse_Content{Content: content}})
}
