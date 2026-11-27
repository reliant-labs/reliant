// Copyright (c) 2025 Reliant Labs
package candidate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
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
// changes against Base, from their merge base, rename detection off. A
// request with Stats also gets each changed file's status, line counts and
// blob at Head (fileStats), from the same merge base.
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
	base := strings.TrimSpace(mb)
	paths, err := b.local.changedPaths(ctx, base, req.Head)
	if err != nil {
		return buildjob.PathsResult{}, err
	}
	res := buildjob.PathsResult{Paths: paths}
	if req.Stats {
		files, err := b.local.fileStats(ctx, base, req.Head)
		if err != nil {
			return buildjob.PathsResult{}, err
		}
		if len(files) > buildjob.MaxFileStats {
			files, res.FilesTruncated = files[:buildjob.MaxFileStats], true
		}
		res.Stats, res.Files = true, files
	}
	return res, nil
}

// ReadConfig implements the queue's config job: one file under .reliant/ at
// a commit, as bytes. A path with nothing at the commit is Found false; a
// path that is not a regular file, or is larger than
// buildjob.ConfigMaxBytes, is an error. The bytes are reported as they are:
// the server parses and validates them.
func (j Job) ReadConfig(ctx context.Context, req buildjob.ConfigRequest) (buildjob.ConfigResult, error) {
	if !shaRE.MatchString(req.SHA) || !buildjob.ValidConfigPath(req.Path) {
		return buildjob.ConfigResult{}, fmt.Errorf("candidate: a config read needs a commit sha and a path under %s", buildjob.ConfigDir)
	}
	b := &builder{p: j.Ports, local: localGit{dir: j.Ports.Git.Dir(), id: gitport.DefaultIdentity()}}
	if err := b.ensure(ctx, req.SHA); err != nil {
		return buildjob.ConfigResult{}, err
	}
	out, _, err := b.local.run(ctx, "", "", "ls-tree", "-z", "--full-tree", req.SHA, "--", req.Path)
	if err != nil {
		return buildjob.ConfigResult{}, err
	}
	entry := strings.TrimRight(out, "\x00")
	if entry == "" {
		return buildjob.ConfigResult{Found: false}, nil
	}
	// "<mode> <type> <object>\t<path>"
	meta, name, _ := strings.Cut(entry, "\t")
	fields := strings.Fields(meta)
	if name != req.Path || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return buildjob.ConfigResult{}, fmt.Errorf("candidate: %s at %s is not a regular file", req.Path, req.SHA)
	}
	size, _, err := b.local.run(ctx, "", "", "cat-file", "-s", fields[2])
	if err != nil {
		return buildjob.ConfigResult{}, err
	}
	if n, err := strconv.Atoi(strings.TrimSpace(size)); err != nil || n > buildjob.ConfigMaxBytes {
		return buildjob.ConfigResult{}, fmt.Errorf("candidate: %s at %s is %s bytes; at most %d are read", req.Path, req.SHA, strings.TrimSpace(size), buildjob.ConfigMaxBytes)
	}
	data, _, err := b.local.run(ctx, "", "", "cat-file", "blob", fields[2])
	if err != nil {
		return buildjob.ConfigResult{}, err
	}
	return buildjob.ConfigResult{Found: true, Bytes: []byte(data)}, nil
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
