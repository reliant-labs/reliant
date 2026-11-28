package filetree

import (
	"io/fs"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/localfs"
)

type deadlineFS struct {
	localfs.FS
	reads     []string
	afterRead func()
}

func (f *deadlineFS) ReadDir(path string) ([]fs.DirEntry, error) {
	f.reads = append(f.reads, path)
	entries, err := f.FS.ReadDir(path)
	f.afterRead()
	return entries, err
}

func TestWalkSlowListingDeadline(t *testing.T) {
	for _, depth := range []int{1, MaxDepth} {
		t.Run(time.Duration(depth).String(), func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "a", "child.txt"), "x")
			write(t, filepath.Join(root, "top.txt"), "x")
			deadline := time.Unix(1000, 0)
			clock := deadline.Add(-time.Second)
			now = func() time.Time { return clock }
			t.Cleanup(func() { now = time.Now })
			filesystem := &deadlineFS{FS: localfs.New(), afterRead: func() { clock = deadline }}
			res, err := Walk(Options{Root: root, Depth: depth, Deadline: deadline, FS: filesystem})
			if err != nil {
				t.Fatal(err)
			}
			if !res.Truncated || !slices.Equal(names(res.Nodes), []string{"a", "top.txt"}) {
				t.Fatalf("slow root must retain listing and report truncation: %+v", res)
			}
			if !slices.Equal(filesystem.reads, []string{root}) {
				t.Fatalf("reads = %v, want root only", filesystem.reads)
			}
			if a := find(res.Nodes, "a"); !a.HasChildren || a.Children != nil {
				t.Fatalf("unread directory = %+v", a)
			}
		})
	}
}

func TestWalkDeadlineAtReadBoundary(t *testing.T) {
	for _, depth := range []int{1, MaxDepth} {
		t.Run(time.Duration(depth).String(), func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "a", "child.txt"), "x")
			deadline := time.Unix(1000, 0)
			// Root pre/post read and directory dispatch are in time; the read
			// boundary itself expires, for both recursion and the depth probe.
			stubClock(t, deadline, 3)
			filesystem := &deadlineFS{FS: localfs.New(), afterRead: func() {}}
			res, err := Walk(Options{Root: root, Depth: depth, Deadline: deadline, FS: filesystem})
			if err != nil {
				t.Fatal(err)
			}
			if !res.Truncated || !find(res.Nodes, "a").HasChildren {
				t.Fatalf("result = %+v", res)
			}
			if !slices.Equal(filesystem.reads, []string{root}) {
				t.Fatalf("reads = %v, want root only", filesystem.reads)
			}
		})
	}
}

func TestWalkSlowEmptyRootDeadline(t *testing.T) {
	root := t.TempDir()
	deadline := time.Unix(1000, 0)
	clock := deadline.Add(-time.Second)
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = time.Now })
	filesystem := &deadlineFS{FS: localfs.New(), afterRead: func() { clock = deadline }}
	res, err := Walk(Options{Root: root, Deadline: deadline, FS: filesystem})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("slow empty root must report truncation")
	}
}
