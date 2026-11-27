// Copyright (c) 2025 Reliant Labs
package candidate

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
)

// Job runs the merge queue's code operations (buildjob) against a local git
// directory. It is the daemon side of the queue: it runs on a machine of the
// repository's gate pool, never on a server role, which holds no repository
// bytes (internal/architecture's TestServerRolesRunNoCode).
type Job struct {
	Ports Ports
}

// Build implements the queue's build job: Build over the request, with a
// shadow request's pushes outside the queue's refs recorded, not made.
func (j Job) Build(ctx context.Context, req buildjob.BuildRequest) (buildjob.BuildResult, error) {
	repo, err := ghport.ParseRepo(req.Repository)
	if err != nil {
		return buildjob.BuildResult{}, err
	}
	p := j.Ports
	var shadow *shadowGit
	if req.Shadow {
		shadow = &shadowGit{Port: p.Git}
		p.Git = shadow
	}
	entries := make([]Entry, len(req.Entries))
	for i, e := range req.Entries {
		entries[i] = Entry{Repo: repo, Number: e.Number, Head: e.Head, Title: e.Title}
	}
	cand, ejections, err := Build(ctx, p, req.Base, entries, Config{BatchID: req.BatchID, Generated: slices.Clone(req.Generated), Regen: req.Regen})
	if err != nil {
		return buildjob.BuildResult{}, err
	}
	out := buildjob.BuildResult{Base: cand.Base, Commit: cand.Commit, Ref: cand.Ref, Trees: cand.Trees}
	for _, e := range cand.Included {
		out.Included = append(out.Included, wireEntry(e))
	}
	for _, ej := range ejections {
		out.Ejections = append(out.Ejections, buildjob.Ejection{Entry: wireEntry(ej.Entry), Reason: buildjob.Reason(ej.Reason), Paths: ej.Paths, Detail: ej.Detail})
	}
	for _, rg := range cand.Regenerated {
		out.Regenerated = append(out.Regenerated, buildjob.Regenerated{Entry: wireEntry(rg.Entry), NewHead: rg.NewHead, Base: rg.Base, Conflicts: rg.Conflicts})
	}
	if shadow != nil {
		out.WouldPush = shadow.would
	}
	return out, nil
}

// ChangedPaths implements the queue's changed-paths job: the paths Head
// changes against Base, from their merge base, rename detection off.
func (j Job) ChangedPaths(ctx context.Context, req buildjob.PathsRequest) (buildjob.PathsResult, error) {
	if !shaRE.MatchString(req.Base) || !shaRE.MatchString(req.Head) {
		return buildjob.PathsResult{}, errors.New("candidate: changed paths need two commit shas")
	}
	b := &builder{p: j.Ports, local: localGit{dir: j.Ports.Git.Dir(), id: gitport.DefaultIdentity()}}
	if err := b.ensure(ctx, req.Base, req.Head); err != nil {
		return buildjob.PathsResult{}, err
	}
	mb, _, err := b.local.run(ctx, "", "", "merge-base", req.Base, req.Head)
	if err != nil {
		return buildjob.PathsResult{}, err
	}
	paths, err := b.local.changedPaths(ctx, strings.TrimSpace(mb), req.Head)
	if err != nil {
		return buildjob.PathsResult{}, err
	}
	return buildjob.PathsResult{Paths: paths}, nil
}

func wireEntry(e Entry) buildjob.Entry {
	return buildjob.Entry{Number: e.Number, Head: e.Head, Title: e.Title}
}

// shadowGit pushes only the candidate refs (refs/heads/reliant-queue/*) and
// records every other push (a regen commit to a PR's branch) instead of
// making it. A shadow build makes no other GitHub write.
type shadowGit struct {
	gitport.Port
	would []buildjob.Push
}

// Push implements gitport.Port.
func (g *shadowGit) Push(ctx context.Context, url string, in gitport.PushInput) error {
	if strings.HasPrefix(in.Ref, "refs/heads/reliant-queue/") {
		return g.Port.Push(ctx, url, in)
	}
	g.would = append(g.would, buildjob.Push{Ref: in.Ref, SHA: in.SHA})
	return nil
}
