// Copyright (c) 2025 Reliant Labs
package jobrun

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
)

// Files lists a head's changed files with their statuses, line counts and
// blobs (a paths job with Stats) on the repository's gate pool: what
// pr.next's risk tier reads in place of GitHub's pull request files list,
// whose response carries every file's patch into the worker.
type Files struct{ d *dispatcher }

// NewFiles builds the Files. Deps.Pools and Deps.Daemons are required.
func NewFiles(deps Deps) (*Files, error) {
	if deps.Pools == nil || deps.Daemons == nil {
		return nil, errors.New("jobrun: NewFiles needs Pools and Daemons")
	}
	return &Files{d: newDispatcher(deps)}, nil
}

// ChangedFiles lists the files head changes against its merge base with
// base, from a member of repo's gate pool. The report is the member's
// claim: the server reads no diff to check it against, so it is held to its
// shape (checkFiles) and no more.
//
// A member running a Reliant older than Stats answers with paths alone. It
// counts as a member that refused the job, and the job goes to the next
// member; when none is left the answer is ErrRefused.
func (f *Files) ChangedFiles(ctx context.Context, repo *core.PipelineRepo, base, head string) (buildjob.PathsResult, error) {
	if err := codeMode(repo); err != nil {
		return buildjob.PathsResult{}, err
	}
	t, err := f.d.target(ctx, repo)
	if err != nil {
		return buildjob.PathsResult{}, err
	}
	req := buildjob.PathsRequest{Repository: repo.Repository, Base: base, Head: head, Stats: true}
	var old []string
	for {
		reply, member, err := f.d.run(ctx, t, buildjob.StartRequest{Kind: buildjob.KindPaths, Paths: &req}, "")
		switch {
		case err != nil && len(old) > 0 && errors.Is(err, ErrNoMember):
			return buildjob.PathsResult{}, fmt.Errorf("%w: %s: its Reliant predates changed-file stats", ErrRefused, strings.Join(old, ", "))
		case err != nil:
			return buildjob.PathsResult{}, err
		case reply.Paths == nil:
			return buildjob.PathsResult{}, fmt.Errorf("%w: machine %s reported no paths", ErrUnverified, member)
		case !reply.Paths.Stats:
			old = append(old, member)
			t.skip = append(t.skip, member)
			continue
		}
		if err := checkFiles(*reply.Paths); err != nil {
			return buildjob.PathsResult{}, fmt.Errorf("%w: changed files from machine %s: %w", ErrUnverified, member, err)
		}
		return *reply.Paths, nil
	}
}

// maxFileLines bounds one file's reported additions or deletions.
const maxFileLines = 1 << 30

var objectID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// checkFiles holds a paths report with stats to its shape: the paths are
// sorted, unique and repository-relative (checkPaths); the files are sorted
// by path and unique, each with a clean relative path, a known status, a
// previous path exactly when renamed or copied, sane line counts (none for
// a binary file) and a 40- or 64-hex blob, all zeros exactly when removed;
// and, unless the list was truncated, the files' paths and renamed files'
// previous paths are exactly the paths.
func checkFiles(res buildjob.PathsResult) error {
	if err := checkPaths(res.Paths); err != nil {
		return err
	}
	if len(res.Files) > buildjob.MaxFileStats {
		return fmt.Errorf("%d files, at most %d", len(res.Files), buildjob.MaxFileStats)
	}
	if res.FilesTruncated && len(res.Files) != buildjob.MaxFileStats {
		return fmt.Errorf("a truncated list of %d files, not %d", len(res.Files), buildjob.MaxFileStats)
	}
	covered := make(map[string]bool, len(res.Paths))
	for i, f := range res.Files {
		switch {
		case !relPath(f.Path):
			return fmt.Errorf("file path %q", f.Path)
		case i > 0 && res.Files[i-1].Path >= f.Path:
			return fmt.Errorf("files are not sorted and unique at %q", f.Path)
		case f.Additions < 0 || f.Deletions < 0 || f.Additions > maxFileLines || f.Deletions > maxFileLines:
			return fmt.Errorf("%s: %d additions and %d deletions", f.Path, f.Additions, f.Deletions)
		case f.Binary && (f.Additions != 0 || f.Deletions != 0):
			return fmt.Errorf("%s: a binary file with line counts", f.Path)
		case !objectID.MatchString(f.BlobSHA):
			return fmt.Errorf("%s: blob %q", f.Path, f.BlobSHA)
		case (f.Status == buildjob.FileRemoved) != (strings.Trim(f.BlobSHA, "0") == ""):
			return fmt.Errorf("%s: %s with blob %s", f.Path, f.Status, f.BlobSHA)
		}
		switch f.Status {
		case buildjob.FileRenamed, buildjob.FileCopied:
			if !relPath(f.PreviousPath) || f.PreviousPath == f.Path {
				return fmt.Errorf("%s: %s from %q", f.Path, f.Status, f.PreviousPath)
			}
		case buildjob.FileAdded, buildjob.FileModified, buildjob.FileRemoved, buildjob.FileChanged:
			if f.PreviousPath != "" {
				return fmt.Errorf("%s: %s with a previous path", f.Path, f.Status)
			}
		default:
			return fmt.Errorf("%s: status %q", f.Path, f.Status)
		}
		covered[f.Path] = true
		if f.Status == buildjob.FileRenamed {
			// A copy's source is unchanged, so it is not among the paths.
			covered[f.PreviousPath] = true
		}
	}
	if res.FilesTruncated {
		return nil
	}
	if len(covered) != len(res.Paths) {
		return fmt.Errorf("%d files cover %d paths, not the %d changed", len(res.Files), len(covered), len(res.Paths))
	}
	for _, p := range res.Paths {
		if !covered[p] {
			return fmt.Errorf("changed path %q is in no file", p)
		}
	}
	return nil
}

// relPath reports whether p is a clean, relative path inside the
// repository, as git names a tree entry.
func relPath(p string) bool {
	return p != "" && p != "." && p != ".." && path.Clean(p) == p && !strings.HasPrefix(p, "/") &&
		!strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\x00\n")
}
