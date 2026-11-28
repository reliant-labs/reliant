// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/pipeline/prpolicy"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// reasonFilesUnavailable is the risk reason of a head whose changed files
// no gate pool daemon could list.
const reasonFilesUnavailable = "changed files unavailable without a gate pool daemon"

// filesUnavailableRisk is the risk of a head whose files are not known:
// high, so a model reviews it. Nothing is let through on a guess.
var filesUnavailableRisk = prpolicy.RiskAssessment{
	Tier: prpolicy.RiskHigh, Rule: prpolicy.RuleModel, Reasons: []string{reasonFilesUnavailable},
}

// FilesFromStats is a paths job's changed files (buildjob.PathsResult with
// Stats) as the risk tier reads them. A list the daemon cut short is marked
// so (Diff.ChangedFiles), which prpolicy judges high risk, as it did
// GitHub's.
func FilesFromStats(res buildjob.PathsResult) ChangedFiles {
	files := make([]prpolicy.ChangedFile, 0, len(res.Files))
	blobs := make(map[string]string, len(res.Files))
	for _, f := range res.Files {
		files = append(files, prpolicy.ChangedFile{
			Path: f.Path, PreviousPath: f.PreviousPath, Status: prpolicy.FileStatus(f.Status),
			Additions: f.Additions, Deletions: f.Deletions,
		})
		blobs[f.Path] = f.BlobSHA
	}
	diff := prpolicy.NewDiff(files...)
	if res.FilesTruncated {
		diff.ChangedFiles = len(files) + 1 // more than were listed
	}
	return ChangedFiles{Diff: diff, Blobs: blobs}
}

// changedFiles lists the head's changed files on a daemon of the
// repository's gate pool, against the merge base with the PR's base branch
// as it is now (its tip read from GitHub: a ref, not contents). known is
// false when no daemon can list them (no Files, no usable member): the
// head is then judged high risk. A GitHub read that failed, or a job that
// may yet answer (the pool busy, a member lost), is a retryable error.
func (s *Service) changedFiles(ctx context.Context, p *prCall, pr PullRequest) (files ChangedFiles, known bool, err error) {
	if s.deps.Files == nil || pr.BaseRef == "" {
		return ChangedFiles{}, false, nil
	}
	base, err := s.deps.GitHub.BranchTip(ctx, p.as, p.rec.Repository, pr.BaseRef)
	if err != nil {
		s.log.WarnContext(ctx, "pr.next could not read the PR's base branch", "pr", prName(p), "base_ref", pr.BaseRef, "error", err)
		return ChangedFiles{}, false, &serveraction.Error{Code: serveraction.CodeUnavailable, Retryable: true,
			Message: fmt.Sprintf("could not read %s's base branch %s from GitHub", prName(p), pr.BaseRef)}
	}
	files, err = s.deps.Files.ChangedFiles(ctx, p.repo, base, p.row.StateHeadSHA)
	switch {
	case errors.Is(err, ErrFilesUnavailable):
		s.log.WarnContext(ctx, "pr.next has no gate pool daemon to list the PR's files; a model reviews it", "pr", prName(p), "error", err)
		return ChangedFiles{}, false, nil
	case err != nil:
		s.log.WarnContext(ctx, "pr.next could not list the PR's files", "pr", prName(p), "error", err)
		return ChangedFiles{}, false, &serveraction.Error{Code: serveraction.CodeUnavailable, Retryable: true,
			Message: fmt.Sprintf("could not list %s's changed files on its gate pool", prName(p))}
	}
	return files, true, nil
}
