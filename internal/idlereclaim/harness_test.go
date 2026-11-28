// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/worktreereclaim/openat2test"
)

const day = 24 * time.Hour

// harnessEpoch is the harness's fixed clock. The tests date their fixtures
// relative to it (age); none reads the wall clock. A file a test writes
// without dating it is in this clock's future, so it reads as just used.
var harnessEpoch = time.Date(2020, time.March, 4, 12, 0, 0, 0, time.UTC)

// harness is a scratch root, a worktree root and a state dir in t.TempDir(),
// a fixed clock, and fakes for what only the daemon has.
type harness struct {
	t   *testing.T
	now time.Time
	r   *Reclaimer
	// busy holds the paths a fake process uses.
	busy map[string]bool
	// refuse makes the admission lease refuse.
	refuse bool
	// scratchUse and chatUse are the recorded uses, by path.
	scratchUse, chatUse map[string]time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	// Every strip and removal goes through worktreereclaim's openat2-based
	// inspection and renameat2 quarantine; on a host that answers ENOSYS the
	// reclaimer holds everything, so no harness test can see its outcome.
	openat2test.Require(t)
	base := t.TempDir()
	h := &harness{t: t, now: harnessEpoch, busy: map[string]bool{}, scratchUse: map[string]time.Time{}, chatUse: map[string]time.Time{}}
	state := filepath.Join(base, "state")
	h.r = &Reclaimer{
		Config:       Config{Mode: ModeOn, ScratchStripIdle: DefaultScratchStripIdle, ScratchRemoveIdle: DefaultScratchRemoveIdle, WorktreeNodeModulesIdle: DefaultWorktreeNodeModulesIdle},
		ScratchRoot:  filepath.Join(base, "scratch"),
		WorktreeRoot: filepath.Join(base, "worktrees"),
		StateDir:     state,
		Records:      NewRecords(filepath.Join(state, RecordFile)),
		Deps: Deps{
			Now: func() time.Time { return h.now },
			ProcessUse: func(_ context.Context, paths []string) error {
				for _, p := range paths {
					for b := range h.busy {
						if within(p, b) {
							return errors.New("process 42 has its cwd in " + b)
						}
					}
				}
				return nil
			},
			Acquire: func(ctx context.Context, paths []string) (Lease, error) {
				if h.refuse {
					return nil, errors.New("workspace has active daemon work")
				}
				return fakeLease{h: h, paths: paths}, nil
			},
			Inspect:     fakeInspect,
			ScratchUse:  func(p string) time.Time { return h.scratchUse[p] },
			ChatUse:     func(p string) time.Time { return h.chatUse[p] },
			WorktreeUse: func(string) time.Time { return time.Time{} },
		},
	}
	require.NoError(t, os.MkdirAll(h.r.ScratchRoot, 0o755))
	require.NoError(t, os.MkdirAll(h.r.WorktreeRoot, 0o755))
	return h
}

type fakeLease struct {
	h     *harness
	paths []string
}

func (l fakeLease) Check(ctx context.Context) error { return l.h.r.ProcessUse(ctx, l.paths) }
func (fakeLease) Release()                          {}

// fakeInspect is the regenerable inventory without its Linux-only holds: the
// bytes and the newest mtime.
func fakeInspect(_ context.Context, dir string) (int64, time.Time, error) {
	var n int64
	var newest time.Time
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if info.Mode().IsRegular() {
			n += info.Size()
		}
		return nil
	})
	return n, newest, err
}

// write creates the file rel under dir with content.
func write(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

// age sets the modification time of everything under dir (dir included) to
// h.now minus d.
func (h *harness) age(dir string, d time.Duration) {
	h.t.Helper()
	at := h.now.Add(-d)
	require.NoError(h.t, filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, at, at)
	}))
}

// chat makes a scratch directory named id with a briefing and returns it.
func (h *harness) chat(id string) string {
	h.t.Helper()
	dir := filepath.Join(h.r.ScratchRoot, id)
	write(h.t, dir, "briefing.md", "# what we found\n")
	return dir
}

func (h *harness) pass() Report {
	h.t.Helper()
	return h.r.Pass(context.Background(), false, "test")
}

func (h *harness) entry(rep Report, path string) Entry {
	h.t.Helper()
	for _, e := range append(append([]Entry(nil), rep.Scratch...), rep.Worktrees...) {
		if e.Path == path {
			return e
		}
	}
	h.t.Fatalf("no report entry for %s in %+v", path, rep)
	return Entry{}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// gitRun runs git in dir with a fixed identity.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), //nolint:forbidigo // a test's git child process
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		// No detached `git maintenance run --auto` after a commit or push: it
		// creates and removes objects/maintenance.lock while h.age walks the
		// tree, and the walk's chtimes then fails on the vanished lock.
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=maintenance.auto", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=gc.auto", "GIT_CONFIG_VALUE_1=0")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// pushedClone makes a bare remote and a clone of it at dir with one pushed
// commit that ignores node_modules, and returns the clone.
func pushedClone(t *testing.T, remoteParent, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	remote := filepath.Join(remoteParent, "remote.git")
	require.NoError(t, os.MkdirAll(remote, 0o755))
	gitRun(t, remote, "init", "-q", "--bare", "-b", "main")
	require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o755))
	gitRun(t, filepath.Dir(dir), "clone", "-q", remote, filepath.Base(dir))
	gitRun(t, dir, "checkout", "-q", "-b", "main")
	write(t, dir, ".gitignore", "node_modules/\n")
	write(t, dir, "package.json", "{}\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	gitRun(t, dir, "push", "-q", "origin", "main")
	return dir
}
