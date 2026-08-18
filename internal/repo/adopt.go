// Copyright (c) 2025 Reliant Labs

// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package repo

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// AdoptFromDaemon reconciles the repos registry with the filesystem. Registry
// rows are written by the clone/init/add flows, so a repo created any other
// way — `git init` run by hand in the workspace terminal is the canonical
// case — exists on disk but not in the DB, and everything gating on
// ListReposByProject wrongly refuses. Callers use this before refusing: ask
// the user's daemon (repo.discover) what actually exists under the project
// path, register anything missing, and true up project.IsGitRepo alongside.
// Returns the project's repos after adoption.
//
// Best-effort by design: on daemon or discovery failure it returns nil so the
// caller keeps its own precondition semantics.
func AdoptFromDaemon(ctx context.Context, database db.Repository, router toolexec.DaemonRouter, project *db.Project) []*core.Repo {
	payload, err := json.Marshal(map[string]string{"path": project.Path})
	if err != nil {
		return nil
	}
	respBytes, err := router.SendDaemonCommand(ctx, project.UserID, "repo.discover", payload, 30_000)
	if err != nil {
		logging.Warn("repo adoption: discover failed", "error", err, "project_id", project.ID)
		return nil
	}
	var resp struct {
		Discovered []struct {
			RelativePath string `json:"relative_path"`
			Name         string `json:"name"`
			RemoteURL    string `json:"remote_url,omitempty"`
		} `json:"discovered"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil || len(resp.Discovered) == 0 {
		return nil
	}

	now := time.Now().UTC()
	adopted := 0
	for _, found := range resp.Discovered {
		if existing, err := database.GetRepoByProjectAndPath(ctx, project.ID, found.RelativePath); err == nil && existing != nil {
			continue
		}
		name := found.Name
		if name == "" {
			name = filepath.Base(found.RelativePath)
			if name == "" || name == "." {
				name = project.Name
			}
		}
		var remoteURL *string
		if found.RemoteURL != "" {
			r := found.RemoteURL
			remoteURL = &r
		}
		if err := database.CreateRepo(ctx, &core.Repo{
			ID:           uuid.New().String(),
			ProjectID:    project.ID,
			Name:         name,
			RelativePath: found.RelativePath,
			RemoteURL:    remoteURL,
			CreatedAt:    now,
			UpdatedAt:    now,
		}); err != nil {
			logging.Warn("repo adoption: failed to register repo", "error", err, "project_id", project.ID, "relative_path", found.RelativePath)
			continue
		}
		adopted++
	}
	if adopted > 0 {
		logging.Info("repo adoption: registered repos found on disk", "project_id", project.ID, "count", adopted)
		if !project.IsGitRepo {
			project.IsGitRepo = true
			if err := database.UpdateProject(ctx, project, project.UserID); err != nil {
				logging.Warn("repo adoption: failed to update project is_git_repo", "error", err, "project_id", project.ID)
			}
		}
	}

	repos, err := database.ListReposByProject(ctx, project.ID)
	if err != nil {
		return nil
	}
	return repos
}
