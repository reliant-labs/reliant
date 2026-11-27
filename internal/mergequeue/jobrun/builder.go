// Copyright (c) 2025 Reliant Labs
package jobrun

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
)

// Builder runs the queue's build and changed-paths jobs on the repository's
// gate pool: the mergequeue.Builder the scheduler takes.
type Builder struct{ d *dispatcher }

// NewBuilder builds the Builder. Deps.Pools, Deps.Daemons and Deps.GitHub
// are required.
func NewBuilder(deps Deps) (*Builder, error) {
	if deps.Pools == nil || deps.Daemons == nil || deps.GitHub == nil {
		return nil, errors.New("jobrun: NewBuilder needs Pools, Daemons and GitHub")
	}
	return &Builder{d: newDispatcher(deps)}, nil
}

// codeMode refuses a job for a repository whose stored mode runs no queue:
// only shadow and on do.
func codeMode(repo *core.PipelineRepo) error {
	if repo == nil {
		return fmt.Errorf("%w: no repository", ErrInvalidRequest)
	}
	if repo.Mode != core.PipelineRepoModeShadow && repo.Mode != core.PipelineRepoModeOn {
		return fmt.Errorf("%w: repository %s is %q; only shadow and on run the queue", ErrInvalidRequest, repo.Repository, repo.Mode)
	}
	return nil
}

// Build builds req's candidate on a member of repo's gate pool, checks the
// report against req and through GitHub, and returns it with Machine set to
// the member that built it. A repository in shadow mode is built only in
// shadow, which pushes nothing but the queue ref. An error means no
// candidate was established; a retry with the same request is safe (the
// daemon adopts what an earlier attempt pushed).
func (b *Builder) Build(ctx context.Context, repo *core.PipelineRepo, req buildjob.BuildRequest) (buildjob.BuildResult, error) {
	if err := codeMode(repo); err != nil {
		return buildjob.BuildResult{}, err
	}
	if repo.Mode == core.PipelineRepoModeShadow && !req.Shadow {
		return buildjob.BuildResult{}, fmt.Errorf("%w: repository %s is in shadow mode; its builds are shadow builds", ErrInvalidRequest, repo.Repository)
	}
	if !strings.EqualFold(req.Repository, repo.Repository) {
		return buildjob.BuildResult{}, fmt.Errorf("%w: a build of %s for repository %s", ErrInvalidRequest, req.Repository, repo.Repository)
	}
	t, err := b.d.target(ctx, repo)
	if err != nil {
		return buildjob.BuildResult{}, err
	}
	gh, err := b.d.deps.GitHub.Client(ctx, repo)
	if err != nil {
		return buildjob.BuildResult{}, fmt.Errorf("jobrun: GitHub client for %s: %w", repo.Repository, err)
	}
	reply, member, err := b.d.run(ctx, t, buildjob.StartRequest{Kind: buildjob.KindBuild, Build: &req}, "")
	if err != nil {
		return buildjob.BuildResult{}, err
	}
	if reply.Build == nil {
		return buildjob.BuildResult{}, fmt.Errorf("%w: machine %s reported no build", ErrUnverified, member)
	}
	res := *reply.Build
	res.Machine = member
	if err := VerifyBuild(ctx, gh, req, res); err != nil {
		return buildjob.BuildResult{}, fmt.Errorf("%w: build of batch %s on machine %s: %w", ErrUnverified, req.BatchID, member, err)
	}
	return res, nil
}

// ChangedPaths lists the paths req.Head changes against req.Base, from a
// member of repo's gate pool. The paths are the member's claim: the server
// cannot read a diff to check them, so they are held to their shape (sorted,
// unique, repository-relative) and no more.
func (b *Builder) ChangedPaths(ctx context.Context, repo *core.PipelineRepo, req buildjob.PathsRequest) (buildjob.PathsResult, error) {
	if err := codeMode(repo); err != nil {
		return buildjob.PathsResult{}, err
	}
	if !strings.EqualFold(req.Repository, repo.Repository) {
		return buildjob.PathsResult{}, fmt.Errorf("%w: paths of %s for repository %s", ErrInvalidRequest, req.Repository, repo.Repository)
	}
	t, err := b.d.target(ctx, repo)
	if err != nil {
		return buildjob.PathsResult{}, err
	}
	reply, member, err := b.d.run(ctx, t, buildjob.StartRequest{Kind: buildjob.KindPaths, Paths: &req}, "")
	if err != nil {
		return buildjob.PathsResult{}, err
	}
	if reply.Paths == nil {
		return buildjob.PathsResult{}, fmt.Errorf("%w: machine %s reported no paths", ErrUnverified, member)
	}
	if err := checkPaths(reply.Paths.Paths); err != nil {
		return buildjob.PathsResult{}, fmt.Errorf("%w: paths from machine %s: %w", ErrUnverified, member, err)
	}
	return *reply.Paths, nil
}

// maxPaths bounds a paths report.
const maxPaths = 100_000

func checkPaths(paths []string) error {
	if len(paths) > maxPaths {
		return fmt.Errorf("%d paths, at most %d", len(paths), maxPaths)
	}
	for i, p := range paths {
		switch {
		case p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\n"):
			return fmt.Errorf("path %q", p)
		case i > 0 && paths[i-1] >= p:
			return fmt.Errorf("paths are not sorted and unique at %q", p)
		}
	}
	return nil
}

// VerifyBuild checks a daemon's build report against the request and,
// through GitHub (refs and commits only), against what is actually pushed:
//
//   - the report is on the request's base, and an empty candidate (every
//     entry ejected) reports no commit and no trees;
//   - the candidate is on the batch's queue ref, which points at the
//     reported commit;
//   - walking down from that commit, there is one single-parent commit per
//     included entry, each with the reported tree T_i, ending on the base;
//   - outside shadow, each regenerated entry's new head is its PR's head,
//     and a commit whose parents are the reviewed head and the base.
//
// The scheduler's own check (mergequeue checkBuild) holds the report's
// entries to the batch's; this holds the commits to GitHub.
func VerifyBuild(ctx context.Context, gh ghport.Client, req buildjob.BuildRequest, res buildjob.BuildResult) error {
	repo, err := ghport.ParseRepo(req.Repository)
	if err != nil {
		return err
	}
	if res.Base != req.Base {
		return fmt.Errorf("built on %s, not the batch's base %s", res.Base, req.Base)
	}
	if len(res.Included) == 0 {
		if res.Commit != "" || len(res.Trees) != 0 {
			return errors.New("a candidate with no entry reports a commit or trees")
		}
		return nil
	}
	if want := buildjob.QueueRef(req.BatchID); res.Ref != want {
		return fmt.Errorf("the candidate is on %q, not %q", res.Ref, want)
	}
	if len(res.Trees) != len(res.Included) {
		return fmt.Errorf("%d trees for %d entries", len(res.Trees), len(res.Included))
	}
	ref, err := gh.GetRef(ctx, repo, res.Ref)
	if err != nil {
		return fmt.Errorf("read %s: %w", res.Ref, err)
	}
	if ref.SHA != res.Commit {
		return fmt.Errorf("%s is at %s, the report says %s", res.Ref, ref.SHA, res.Commit)
	}
	sha := res.Commit
	for i := len(res.Trees) - 1; i >= 0; i-- {
		c, err := gh.GetCommit(ctx, repo, sha)
		if err != nil {
			return fmt.Errorf("read %s: %w", sha, err)
		}
		if len(c.Parents) != 1 {
			return fmt.Errorf("candidate commit %s has %d parents, not one", sha, len(c.Parents))
		}
		if c.TreeSHA != res.Trees[i] {
			return fmt.Errorf("candidate commit %s has tree %s, the report says T_%d is %s", sha, c.TreeSHA, i+1, res.Trees[i])
		}
		sha = c.Parents[0]
	}
	if sha != req.Base {
		return fmt.Errorf("the candidate's commits end on %s, not the base %s", sha, req.Base)
	}
	if req.Shadow {
		return nil
	}
	for _, rg := range res.Regenerated {
		pr, err := gh.GetPullRequest(ctx, repo, rg.Entry.Number)
		if err != nil {
			return fmt.Errorf("read #%d: %w", rg.Entry.Number, err)
		}
		if pr.HeadSHA != rg.NewHead {
			return fmt.Errorf("#%d's head is %s, the report says its regen commit %s is", rg.Entry.Number, pr.HeadSHA, rg.NewHead)
		}
		c, err := gh.GetCommit(ctx, repo, rg.NewHead)
		if err != nil {
			return fmt.Errorf("read %s: %w", rg.NewHead, err)
		}
		if !slices.Equal(c.Parents, []string{rg.Entry.Head, req.Base}) {
			return fmt.Errorf("#%d's regen commit %s has parents %v, not the reviewed head %s and the base %s", rg.Entry.Number, rg.NewHead, c.Parents, rg.Entry.Head, req.Base)
		}
	}
	return nil
}
