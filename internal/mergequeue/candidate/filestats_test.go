// Copyright (c) 2025 Reliant Labs
package candidate_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/mergequeue/fakegithub"
)

// blobAt is the object id of path at rev in the fake repository, read with
// git directly, apart from the code under test.
func (f *fixture) blobAt(rev, path string) string {
	f.t.Helper()
	out, err := exec.Command("git", "--git-dir="+f.r.Dir(), "rev-parse", rev+":"+path).Output()
	if err != nil {
		f.t.Fatalf("rev-parse %s:%s: %v", rev, path, err)
	}
	return strings.TrimSpace(string(out))
}

// A paths job with stats lists each changed file against the merge base as
// GitHub's pull request files list does: an addition, a modification with
// its line counts, a removal, a rename (one entry with its previous path, by
// git's 50% similarity), and a binary file with no line counts; each with
// its blob at the head. A change on main after the branch point is not the
// PR's, so it is not listed.
func TestHostChangedFilesWithStats(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.r.Commit("main", "seed", map[string]string{
		"docs/long.md": strings.Repeat("a line of the guide\n", 20),
		"img/logo.png": "\x89PNG\x00\x01\x02",
	})
	f.r.Branch("one", "main")
	head := f.r.Commit("one", "work", map[string]string{
		"x.txt":         "x\nx2\nx3\n",                                                 // modified: +2 -0
		"y.txt":         fakegithub.Deleted,                                            // removed: -1
		"new/file.txt":  "n\n",                                                         // added: +1
		"docs/long.md":  fakegithub.Deleted,                                            // renamed …
		"guide/long.md": strings.Repeat("a line of the guide\n", 19) + "one changed\n", // … with one line changed
		"img/logo.png":  "\x89PNG\x00\x03\x04",                                         // binary
	})
	base := f.r.Commit("main", "main moves on", map[string]string{"src/a.txt": "a2\n"})

	reply := runJob(t, f.host(), buildjob.StartRequest{JobID: "paths-stats", Kind: buildjob.KindPaths, Paths: &buildjob.PathsRequest{
		Repository: "acme/widgets", Base: base, Head: head, Stats: true,
	}})
	if reply.Error != "" || reply.Paths == nil || !reply.Paths.Stats || reply.Paths.FilesTruncated {
		t.Fatalf("reply: %+v", reply)
	}
	wantPaths := []string{"docs/long.md", "guide/long.md", "img/logo.png", "new/file.txt", "x.txt", "y.txt"}
	if !slices.Equal(reply.Paths.Paths, wantPaths) {
		t.Fatalf("paths %v, want %v (rename detection stays off for Paths)", reply.Paths.Paths, wantPaths)
	}
	zero := strings.Repeat("0", len(head))
	want := []buildjob.FileStat{
		{Path: "guide/long.md", PreviousPath: "docs/long.md", Status: buildjob.FileRenamed, Additions: 1, Deletions: 1, BlobSHA: f.blobAt(head, "guide/long.md")},
		{Path: "img/logo.png", Status: buildjob.FileModified, Binary: true, BlobSHA: f.blobAt(head, "img/logo.png")},
		{Path: "new/file.txt", Status: buildjob.FileAdded, Additions: 1, BlobSHA: f.blobAt(head, "new/file.txt")},
		{Path: "x.txt", Status: buildjob.FileModified, Additions: 2, BlobSHA: f.blobAt(head, "x.txt")},
		{Path: "y.txt", Status: buildjob.FileRemoved, Deletions: 1, BlobSHA: zero},
	}
	if !slices.Equal(reply.Paths.Files, want) {
		t.Fatalf("files\n got %+v\nwant %+v", reply.Paths.Files, want)
	}
}

// Without Stats, the reply is what it always was: paths alone, so a server
// that never asks sees no change.
func TestHostChangedPathsWithoutStatsReportsNoFiles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e := f.pr("one", map[string]string{"x.txt": "x1\n"})
	reply := runJob(t, f.host(), buildjob.StartRequest{JobID: "paths-plain", Kind: buildjob.KindPaths, Paths: &buildjob.PathsRequest{
		Repository: "acme/widgets", Base: f.r.Head("main"), Head: e.Head,
	}})
	if reply.Paths == nil || reply.Paths.Stats || reply.Paths.Files != nil || !slices.Equal(reply.Paths.Paths, []string{"x.txt"}) {
		t.Fatalf("reply: %+v", reply)
	}
}
