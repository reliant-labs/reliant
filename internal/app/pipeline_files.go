// Copyright (c) 2025 Reliant Labs

package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/jobrun"
	"github.com/reliant-labs/reliant/internal/pipeline/practions"
	"github.com/reliant-labs/reliant/internal/store"
)

// pipelineFiles is pr.next's changed-file list: a paths job with stats on a
// daemon of the repository's gate pool (jobrun.Files), so a PR's diff never
// reaches the worker. nil, with no router to reach a daemon: pr.next then
// judges every head it reviews high risk.
func pipelineFiles(repo *db.Repo, router *WorkerDaemonRouter) (practions.Files, error) {
	if router == nil {
		return nil, nil
	}
	files, err := jobrun.NewFiles(jobrun.Deps{
		Pools: store.NewMachinePoolExtStore(repo.ORM()), Daemons: router,
		GitHub:       nil, // a changed-file report is checked by its shape; no GitHub read
		Repos:        nil, // the caller names the enrolment
		Configs:      nil, // pipeline_repos.config_json, as mergequeue.StoredConfig reads it
		PollInterval: 0,   // jobrun's default, 5s
	})
	if err != nil {
		return nil, fmt.Errorf("pipeline files: %w", err)
	}
	return jobrunFiles{files: files}, nil
}

// jobrunFiles adapts jobrun.Files to practions.Files: the stats as the risk
// tier reads them, and the answers no member can give soon as
// practions.ErrFilesUnavailable.
type jobrunFiles struct{ files *jobrun.Files }

// ChangedFiles implements practions.Files.
func (j jobrunFiles) ChangedFiles(ctx context.Context, repo *core.PipelineRepo, base, head string) (practions.ChangedFiles, error) {
	res, err := j.files.ChangedFiles(ctx, repo, base, head)
	switch {
	case err == nil:
		return practions.FilesFromStats(res), nil
	case errors.Is(err, jobrun.ErrNoMember), errors.Is(err, jobrun.ErrRefused),
		errors.Is(err, jobrun.ErrUnverified), errors.Is(err, jobrun.ErrInvalidRequest):
		return practions.ChangedFiles{}, fmt.Errorf("%w: %w", practions.ErrFilesUnavailable, err)
	}
	return practions.ChangedFiles{}, err
}
