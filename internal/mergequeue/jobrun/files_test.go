// Copyright (c) 2025 Reliant Labs
package jobrun_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/mergequeue/fakegithub"
	"github.com/reliant-labs/reliant/internal/mergequeue/jobrun"
)

func newFiles(t *testing.T, deps jobrun.Deps) *jobrun.Files {
	t.Helper()
	f, err := jobrun.NewFiles(deps)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// The real daemon-side host lists the files, with a rename, a removal and
// line counts, and the report passes the server's shape check.
func TestChangedFilesRunOnAPoolMember(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	e := w.pr("one", map[string]string{"x.txt": fakegithub.Deleted, "moved/x.txt": "x\n", "y.txt": "y1\ny2\n"})
	res, err := newFiles(t, w.deps()).ChangedFiles(t.Context(), enrolled(core.PipelineRepoModeShadow), w.r.Head("main"), e.Head)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if !res.Stats || !slices.Equal(res.Paths, []string{"moved/x.txt", "x.txt", "y.txt"}) || len(res.Files) != 2 {
		t.Fatalf("result: %+v", res)
	}
	if f := res.Files[0]; f.Path != "moved/x.txt" || f.PreviousPath != "x.txt" || f.Status != buildjob.FileRenamed {
		t.Fatalf("the move is one renamed file: %+v", f)
	}
	if f := res.Files[1]; f.Path != "y.txt" || f.Status != buildjob.FileModified || f.Additions != 2 || f.Deletions != 1 {
		t.Fatalf("y.txt: %+v", f)
	}
}

// A member older than stats answers with paths alone: it counts as one that
// refused, and the next member lists the files. With no newer member left,
// the answer is ErrRefused.
func TestChangedFilesSkipAMemberOlderThanStats(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.daemons.daemons["d-a"] = answer(buildjob.StartReply{State: buildjob.JobRunning},
		buildjob.StatusReply{State: buildjob.JobDone, Paths: &buildjob.PathsResult{Paths: []string{"x.txt"}}})
	w.pools.rows = append([]core.MachinePoolMemberRow{member("d-a", "a")}, w.pools.rows...)
	e := w.pr("one", map[string]string{"x.txt": "x1\n"})
	res, err := newFiles(t, w.deps()).ChangedFiles(t.Context(), enrolled(core.PipelineRepoModeOn), w.r.Head("main"), e.Head)
	if err != nil || len(res.Files) != 1 || res.Files[0].Path != "x.txt" {
		t.Fatalf("the newer member lists the files: %+v, %v", res, err)
	}
	if got := w.daemons.sentTo("d-b"); len(got) == 0 || got[0] != buildjob.CommandStart {
		t.Fatalf("the newer member was sent %v", got)
	}

	w.pools.rows = w.pools.rows[:1]
	_, err = newFiles(t, w.deps()).ChangedFiles(t.Context(), enrolled(core.PipelineRepoModeOn), w.r.Head("main"), e.Head)
	if !errors.Is(err, jobrun.ErrRefused) || !strings.Contains(err.Error(), "predates changed-file stats") {
		t.Fatalf("only an old member: %v", err)
	}
}

// The request asks for stats, so a member that knows them computes them.
func TestChangedFilesAskForStats(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	var asked buildjob.StartRequest
	d := &fakeDaemons{daemons: map[string]handler{"d-a": func(_ context.Context, command string, payload []byte) ([]byte, error) {
		if command == buildjob.CommandStart {
			_ = json.Unmarshal(payload, &asked)
			return json.Marshal(buildjob.StartReply{State: buildjob.JobRunning})
		}
		return json.Marshal(buildjob.StatusReply{State: buildjob.JobDone, Paths: &buildjob.PathsResult{Paths: []string{}, Stats: true}})
	}}}
	f := newFiles(t, jobrun.Deps{Pools: gatePool(member("d-a", "a")), Daemons: d, PollInterval: time.Millisecond})
	if _, err := f.ChangedFiles(t.Context(), enrolled(core.PipelineRepoModeOn), sha, sha); err != nil {
		t.Fatal(err)
	}
	if asked.Kind != buildjob.KindPaths || asked.Paths == nil || !asked.Paths.Stats || asked.Paths.Base != sha {
		t.Fatalf("asked %+v", asked)
	}
}

func TestChangedFilesCheckTheReportsShape(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	blob := strings.Repeat("b", 40)
	zero := strings.Repeat("0", 40)
	mod := func(p string) buildjob.FileStat {
		return buildjob.FileStat{Path: p, Status: buildjob.FileModified, Additions: 1, BlobSHA: blob}
	}
	many := make([]buildjob.FileStat, buildjob.MaxFileStats)
	manyPaths := make([]string, buildjob.MaxFileStats)
	for i := range many {
		manyPaths[i] = fmt.Sprintf("f/%05d", i)
		many[i] = mod(manyPaths[i])
	}
	for _, tc := range []struct {
		name string
		res  buildjob.PathsResult
		ok   bool
	}{
		{"modified, renamed, removed, binary, sha-256", buildjob.PathsResult{Paths: []string{"a", "b", "c", "d", "e", "old"}, Files: []buildjob.FileStat{
			mod("a"),
			{Path: "b", PreviousPath: "old", Status: buildjob.FileRenamed, BlobSHA: blob},
			{Path: "c", Status: buildjob.FileRemoved, Deletions: 3, BlobSHA: zero},
			{Path: "d", Status: buildjob.FileAdded, Binary: true, BlobSHA: blob},
			{Path: "e", Status: buildjob.FileChanged, BlobSHA: strings.Repeat("c", 64)},
		}}, true},
		{"nothing changed", buildjob.PathsResult{Paths: []string{}}, true},
		{"a truncated list", buildjob.PathsResult{Paths: manyPaths, Files: many, FilesTruncated: true}, true},
		{"unsorted files", buildjob.PathsResult{Paths: []string{"a", "b"}, Files: []buildjob.FileStat{mod("b"), mod("a")}}, false},
		{"a duplicate file", buildjob.PathsResult{Paths: []string{"a"}, Files: []buildjob.FileStat{mod("a"), mod("a")}}, false},
		{"an absolute path", buildjob.PathsResult{Paths: []string{"/a"}, Files: []buildjob.FileStat{mod("/a")}}, false},
		{"a path out of the repository", buildjob.PathsResult{Paths: []string{"../a"}, Files: []buildjob.FileStat{mod("../a")}}, false},
		{"an unclean path", buildjob.PathsResult{Paths: []string{"a//b"}, Files: []buildjob.FileStat{mod("a//b")}}, false},
		{"negative lines", buildjob.PathsResult{Paths: []string{"a"}, Files: []buildjob.FileStat{{Path: "a", Status: buildjob.FileModified, Deletions: -1, BlobSHA: blob}}}, false},
		{"too many lines", buildjob.PathsResult{Paths: []string{"a"}, Files: []buildjob.FileStat{{Path: "a", Status: buildjob.FileModified, Additions: 1 << 31, BlobSHA: blob}}}, false},
		{"a binary file with lines", buildjob.PathsResult{Paths: []string{"a"}, Files: []buildjob.FileStat{{Path: "a", Status: buildjob.FileModified, Binary: true, Additions: 1, BlobSHA: blob}}}, false},
		{"a short blob", buildjob.PathsResult{Paths: []string{"a"}, Files: []buildjob.FileStat{{Path: "a", Status: buildjob.FileModified, BlobSHA: "abc"}}}, false},
		{"an upper-case blob", buildjob.PathsResult{Paths: []string{"a"}, Files: []buildjob.FileStat{{Path: "a", Status: buildjob.FileModified, BlobSHA: strings.Repeat("B", 40)}}}, false},
		{"a removed file with a blob", buildjob.PathsResult{Paths: []string{"a"}, Files: []buildjob.FileStat{{Path: "a", Status: buildjob.FileRemoved, BlobSHA: blob}}}, false},
		{"a kept file with no blob", buildjob.PathsResult{Paths: []string{"a"}, Files: []buildjob.FileStat{{Path: "a", Status: buildjob.FileModified, BlobSHA: zero}}}, false},
		{"an unknown status", buildjob.PathsResult{Paths: []string{"a"}, Files: []buildjob.FileStat{{Path: "a", Status: "moved", BlobSHA: blob}}}, false},
		{"a rename from nowhere", buildjob.PathsResult{Paths: []string{"a"}, Files: []buildjob.FileStat{{Path: "a", Status: buildjob.FileRenamed, BlobSHA: blob}}}, false},
		{"a modification with a previous path", buildjob.PathsResult{Paths: []string{"a", "b"}, Files: []buildjob.FileStat{{Path: "a", PreviousPath: "b", Status: buildjob.FileModified, BlobSHA: blob}}}, false},
		{"a changed path in no file", buildjob.PathsResult{Paths: []string{"a", "b"}, Files: []buildjob.FileStat{mod("a")}}, false},
		{"a file that is not a changed path", buildjob.PathsResult{Paths: []string{"a"}, Files: []buildjob.FileStat{mod("a"), mod("b")}}, false},
		{"truncated short of the cap", buildjob.PathsResult{Paths: []string{"a"}, Files: []buildjob.FileStat{mod("a")}, FilesTruncated: true}, false},
		{"over the cap", buildjob.PathsResult{Paths: append(slices.Clone(manyPaths), "g"), Files: append(slices.Clone(many), mod("g")), FilesTruncated: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.res
			res.Stats = true
			d := &fakeDaemons{daemons: map[string]handler{
				"d-a": answer(buildjob.StartReply{State: buildjob.JobRunning}, buildjob.StatusReply{State: buildjob.JobDone, Paths: &res}),
			}}
			f := newFiles(t, jobrun.Deps{Pools: gatePool(member("d-a", "a")), Daemons: d, PollInterval: time.Millisecond})
			_, err := f.ChangedFiles(t.Context(), enrolled(core.PipelineRepoModeOn), sha, sha)
			if tc.ok != (err == nil) || (err != nil && !errors.Is(err, jobrun.ErrUnverified)) {
				t.Fatalf("ok %v, got %v", tc.ok, err)
			}
		})
	}
}

func TestChangedFilesOnlyForARepositoryThatRunsTheQueue(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	d := &fakeDaemons{}
	f := newFiles(t, jobrun.Deps{Pools: gatePool(member("d-a", "a")), Daemons: d, PollInterval: time.Millisecond})
	if _, err := f.ChangedFiles(t.Context(), enrolled(core.PipelineRepoModeOff), sha, sha); !errors.Is(err, jobrun.ErrInvalidRequest) {
		t.Fatalf("a repository that is off: %v", err)
	}
	if _, err := f.ChangedFiles(t.Context(), enrolled(core.PipelineRepoModeOn), "main", sha); !errors.Is(err, jobrun.ErrInvalidRequest) {
		t.Fatalf("a branch for a base: %v", err)
	}
	if len(d.calls) != 0 {
		t.Fatalf("a refused job reached a daemon: %v", d.calls)
	}
}
