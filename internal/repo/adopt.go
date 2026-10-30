// Copyright (c) 2025 Reliant Labs

// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
//
//forge:exclude-contract: discovers nested git repositories under a project directory; local filesystem walk only
package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DaemonCommander is the one daemon capability adoption needs: send a command
// to the machine that holds the project's checkout and return its raw
// response. Callers bind it to that machine (see OnDaemon) — the user's
// default daemon is project-blind, and asking a machine without the checkout
// reports nothing.
type DaemonCommander interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// PinnedSender sends a command to one named daemon.
type PinnedSender interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// OnDaemon binds a router to daemonID, so adoption asks the machine a
// worktree is about to be created on. An empty daemonID keeps default
// resolution.
func OnDaemon(router PinnedSender, daemonID string) DaemonCommander {
	return pinnedCommander{router: router, daemonID: daemonID}
}

type pinnedCommander struct {
	router   PinnedSender
	daemonID string
}

func (p pinnedCommander) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if p.daemonID == "" {
		return p.router.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	}
	return p.router.SendDaemonCommandToDaemon(ctx, userID, p.daemonID, commandType, payload, timeoutMs)
}

// adoptDiscoverTimeoutMs bounds the repo.discover round trip.
const adoptDiscoverTimeoutMs = 30_000

// NoReposMessage is the sentence a caller refuses worktree creation with when
// the registry is empty AND the machine confirmed there is no git repository
// in the project folder. It names the folder and how deep the search went, so
// a user whose repo is real but elsewhere can see why it was not found.
func NoReposMessage(project *db.Project) string {
	return fmt.Sprintf("no git repository found in %s (searched it and %d levels of subfolders); run `git init` there or clone a repository into it before creating a workspace",
		project.Path, DefaultMaxDepth)
}

// AdoptFromDaemon reconciles the repos registry with the filesystem. Registry
// rows are written by the local create/init/add flows, so a repo that came to
// exist any other way — a project cloned onto a cloud machine, a project the
// daemon announced, `git init` run by hand in the workspace terminal — exists
// on disk but not in the DB, and everything gating on ListReposByProject
// wrongly refuses. Callers use this before refusing: ask the project's machine
// (repo.discover) what actually exists under the project path, register
// anything missing, and true up project.IsGitRepo alongside. Returns the
// project's repos after adoption.
//
// An error means the machine could not answer (unreachable, asleep, the
// project folder missing on it): the caller must say so rather than claim the
// project has no repos. A nil error with no repos means the machine looked and
// found none (NoReposMessage).
func AdoptFromDaemon(ctx context.Context, database AdoptStore, machine DaemonCommander, project *db.Project) ([]*core.Repo, error) {
	payload, err := json.Marshal(map[string]string{"path": project.Path})
	if err != nil {
		return nil, fmt.Errorf("marshal repo.discover payload: %w", err)
	}
	respBytes, err := machine.SendDaemonCommand(ctx, project.UserID, "repo.discover", payload, adoptDiscoverTimeoutMs)
	if err != nil {
		logging.WarnContext(ctx, "repo adoption: discover failed", "error", err, "project_id", project.ID, "path", project.Path)
		return nil, fmt.Errorf("look for git repositories in %s: %w", project.Path, err)
	}
	var resp struct {
		Discovered []struct {
			RelativePath string `json:"relative_path"`
			Name         string `json:"name"`
			RemoteURL    string `json:"remote_url,omitempty"`
		} `json:"discovered"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("read repo.discover answer for %s: %w", project.Path, err)
	}
	if len(resp.Discovered) == 0 {
		return nil, nil
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
			// A concurrent adoption may have registered the same path first
			// (UNIQUE (project_id, relative_path)); the re-read below sees it.
			logging.WarnContext(ctx, "repo adoption: failed to register repo", "error", err, "project_id", project.ID, "relative_path", found.RelativePath)
			continue
		}
		adopted++
	}
	if adopted > 0 {
		logging.InfoContext(ctx, "repo adoption: registered repos found on disk", "project_id", project.ID, "count", adopted)
		if !project.IsGitRepo {
			project.IsGitRepo = true
			if err := database.UpdateProject(ctx, project, project.UserID); err != nil {
				logging.WarnContext(ctx, "repo adoption: failed to update project is_git_repo", "error", err, "project_id", project.ID)
			}
		}
	}

	repos, err := database.ListReposByProject(ctx, project.ID)
	if err != nil {
		return nil, fmt.Errorf("list repos for project %s: %w", project.ID, err)
	}
	return repos, nil
}
