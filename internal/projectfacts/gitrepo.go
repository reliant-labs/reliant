// Copyright (c) 2025 Reliant Labs

// Package projectfacts keeps the project row's cached copies of facts only a
// daemon can observe about a checkout — whether it is a git repository, and
// the name its forge.yaml declares — in step with what a daemon reports. The
// API tier cannot read the filesystem, so both writers of these facts,
// ProjectService and the daemon connection (ToolsDaemonService), go through
// here.
//
//forge:exclude-contract: stateless functions over the daemon sender and project store their caller passes; no state of their own
package projectfacts

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
)

// ProjectUpdater is what ReconcileGitRepo writes the project through.
type ProjectUpdater interface {
	UpdateProject(ctx context.Context, project *db.Project, userID string) error
}

// ReconcileGitRepo trues up a project's cached is_git_repo flag against
// a fresh filesystem observation from the daemon, persisting only on change.
//
// is_git_repo is a cache of ONE fact: does the daemon see a .git at the
// project path. It is intentionally NOT derived from the repos table — repo
// rows are created only by the multi-repo/worktree flows, so a plain git
// project the user never registers as a worktree repo has zero repo rows yet
// is still a git repo. The API tier can't observe the filesystem itself, so
// it caches the daemon's answer and reconciles here, in BOTH directions,
// every time the daemon reports. Callers must pass a value that came from an
// actual daemon observation (project.check_git / repo.discover), never from a
// repo-count, so the cache stays honest.
func ReconcileGitRepo(ctx context.Context, projects ProjectUpdater, project *db.Project, observed bool) error {
	if project.IsGitRepo == observed {
		return nil
	}
	project.IsGitRepo = observed
	project.UpdatedAt = time.Now().UTC()
	return projects.UpdateProject(ctx, project, project.UserID)
}
