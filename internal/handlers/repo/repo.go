// Copyright (c) 2025 Reliant Labs
package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
)

// sendToCheckout forwards a JSON command to the machine holding the
// project's checkout (projectcheckout.Target): every command here names a
// path under that checkout, which means nothing on any other machine.
func (s *Service) sendToCheckout(ctx context.Context, userID string, t projectcheckout.Target, commandType string, payload, resp interface{}) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	respBytes, err := t.Send(ctx, s.deps.Router, userID, commandType, payloadBytes, 30_000)
	if err != nil {
		return fmt.Errorf("daemon command %s: %w", commandType, err)
	}
	if resp != nil {
		if err := json.Unmarshal(respBytes, resp); err != nil {
			return fmt.Errorf("unmarshal response for %s: %w", commandType, err)
		}
	}
	return nil
}

// checkout resolves the machine holding project's checkout and its path
// there (projectcheckout.Resolve) — never projects.path, the creating
// machine's. purpose finishes "clone it onto a machine to …".
func (s *Service) checkout(ctx context.Context, userID string, project *db.Project, purpose string) (projectcheckout.Target, error) {
	if s.deps.Router == nil {
		return projectcheckout.Target{}, svcerr.Unavailable("no daemon available")
	}
	return projectcheckout.Resolve(ctx, s.deps.Repo, s.deps.Router, userID, project, purpose)
}

// authorizeRepo loads a repo and verifies the caller owns its parent project.
func (s *Service) authorizeRepo(ctx context.Context, repoID, userID string) (*core.Repo, *db.Project, error) {
	repo, err := s.deps.Repo.GetRepo(ctx, repoID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to get repo"))
	}
	project, err := s.deps.Projects.GetProjectWithUserCheck(ctx, repo.ProjectID, userID)
	if err != nil {
		// Someone else's project: the repo is not found, so the answer never
		// reveals that it exists.
		if svcerr.IsNotFound(err) {
			return nil, nil, svcerr.NotFound("repo")
		}
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	return repo, project, nil
}

// authorizeProject verifies the caller owns the named project.
func (s *Service) authorizeProject(ctx context.Context, projectID, userID string) (*db.Project, error) {
	project, err := s.deps.Projects.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		// Missing and someone else's are the store's one NotFound.
		return nil, svcerr.WrapOr(err, svcerr.Internal("database error"))
	}
	return project, nil
}

// repoToProto is r on the wire, with apps, the forge apps recorded for it
// (repo_forge_apps). forge_apps_scanned_at stays unset until a daemon
// finished a scan of the repo: its apps are unknown until then.
func repoToProto(r *core.Repo, apps []core.RepoForgeApp) *reliantv1.Repo {
	out := &reliantv1.Repo{
		Id:           r.ID,
		ProjectId:    r.ProjectID,
		Name:         r.Name,
		RelativePath: r.RelativePath,
		CreatedAt:    r.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    r.UpdatedAt.Format(time.RFC3339),
	}
	if r.RemoteURL != nil {
		out.RemoteUrl = *r.RemoteURL
	}
	for _, app := range apps {
		out.ForgeApps = append(out.ForgeApps, &reliantv1.RepoForgeApp{Name: app.Name, Subpath: app.Subpath})
	}
	if r.ForgeAppsScannedAt != nil {
		scanned := r.ForgeAppsScannedAt.UTC().Format(time.RFC3339)
		out.ForgeAppsScannedAt = &scanned
	}
	return out
}
