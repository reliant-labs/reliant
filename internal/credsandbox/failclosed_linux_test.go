// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Every step that prepares the sandbox fails closed: a command whose plan
// cannot be carried out in full is refused, never run with less protection, or
// less than it declared, than the plan says.

// blocked is a path under which nothing can be made: its parent is a file.
func blocked(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(f, nil, 0o600))
	return filepath.Join(f, "below")
}

// R1.B1: without overlays, a cache's variable must point at a private
// directory; one that cannot be made refuses the command rather than leave
// the variable on the shared cache.
func TestWithoutOverlays_APrivateCacheItCannotMakeIsAnError(t *testing.T) {
	runDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "tmp"), nil, 0o600)) // the private /tmp is a file
	shared := t.TempDir()
	_, env, err := withoutOverlays(Plan{Overlay: []Overlay{{Path: shared, Env: "GOCACHE", Cache: true}}},
		[]string{"GOCACHE=" + shared}, runDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCACHE")
	assert.Equal(t, "GOCACHE="+shared, env[0], "the error is returned; the caller refuses the command")
}

func TestPrepareOverlays_FailsClosed(t *testing.T) {
	// A cache it cannot create.
	_, err := prepareOverlays(Plan{Overlay: []Overlay{{Path: blocked(t), Cache: true}}})
	assert.Error(t, err, "a missing cache the daemon cannot create")

	// A hand-off it cannot create.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "f"), nil, 0o600))
	_, err = prepareOverlays(Plan{Overlay: []Overlay{{Path: root, Cache: true, Notice: true}},
		Passthrough: []string{filepath.Join(root, "f", "out")}})
	assert.Error(t, err, "a hand-off directory the daemon cannot create")
}

// R2.B5: a hand-off is made from its cache root down, never through a link, so
// a link planted on the way cannot make the daemon create a directory
// elsewhere.
func TestMkdirBeneath_NeverFollowsALink(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(root, "link")))
	assert.Error(t, mkdirBeneath(root, filepath.Join(root, "link", "run", "out")))
	entries, err := os.ReadDir(elsewhere)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing was made through the link")

	require.NoError(t, mkdirBeneath(root, filepath.Join(root, "pipeline", "run-1", "out")))
	assert.DirExists(t, filepath.Join(root, "pipeline", "run-1", "out"))
	assert.Error(t, mkdirBeneath(root, filepath.Join(filepath.Dir(root), "outside")))
}

// R2.B6: a declared passthrough the init cannot open refuses the command; it
// is never left private. The open comes before any mount, so this runs
// unprivileged.
func TestOverlayWithPassthrough_RefusesAPassthroughItCannotOpen(t *testing.T) {
	dir := t.TempDir()
	err := overlayWithPassthrough(dir, t.TempDir(), []string{filepath.Join(dir, "gone")})
	var pe *passthroughError
	require.True(t, errors.As(err, &pe), "err = %v", err)
}

// R2.B7: a run directory is swept only once its owner is gone. One with no
// owner record is in the middle of being made and is never swept, whatever
// its age.
func TestSweepRunDirs_RemovesOnlyWhatADeadOwnerLeft(t *testing.T) {
	tmp := t.TempDir()
	mk := func(name, owner string) string {
		d := filepath.Join(tmp, runDirPrefix+name)
		require.NoError(t, os.Mkdir(d, 0o700))
		if owner != "" {
			require.NoError(t, os.WriteFile(filepath.Join(d, runDirOwnerFile), []byte(owner+"\n"), 0o600))
		}
		old := time.Now().Add(-24 * time.Hour)
		require.NoError(t, os.Chtimes(d, old, old))
		return d
	}
	self := strconv.Itoa(os.Getpid())
	ownerless := mk("ownerless", "")
	live := mk("live", self+" "+processStart(self))
	dead := mk("dead", self+" not-its-start-time")

	sweepRunDirs(tmp)
	assert.DirExists(t, ownerless, "an old run directory with no owner record is not swept")
	assert.DirExists(t, live, "nor one whose owner runs")
	assert.NoDirExists(t, dead, "one whose owner is gone is")
}

// newRunDir records the daemon as the owner before it returns the directory:
// there is no window in which it is ownerless.
func TestNewRunDir_RecordsItsOwnerAtOnce(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dir, f, err := newRunDir()
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	raw, err := os.ReadFile(filepath.Join(dir, runDirOwnerFile))
	require.NoError(t, err)
	self := strconv.Itoa(os.Getpid())
	assert.Equal(t, self+" "+processStart(self)+"\n", string(raw))
	require.NoError(t, checkRunDir(int(f.Fd())))
}

func TestCheckRunDir_RefusesADirectoryOthersCanEnter(t *testing.T) {
	d := t.TempDir()
	require.NoError(t, os.Chmod(d, 0o755))
	fd, err := unix.Open(d, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	require.NoError(t, err)
	defer func() { _ = unix.Close(fd) }()
	assert.Error(t, checkRunDir(fd))
}

// R2.B8: the init uses the run directory the daemon made and handed it, never
// whatever has that name by the time it starts: a directory put in its place
// refuses the command, and nothing is written to or removed from it.
func TestSandbox_ARunDirectoryReplacedBeforeTheInitStartsRefusesTheCommand(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	t.Setenv("TMPDIR", t.TempDir())
	cmd := exec.Command("bash", "-c", "echo RAN")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	require.Empty(t, Apply(cmd, selfPath(t), Plan{}))
	runDir := cmd.ExtraFiles[len(cmd.ExtraFiles)-3].Name()
	require.NoError(t, os.Rename(runDir, runDir+".moved"))
	require.NoError(t, os.Mkdir(runDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "keep"), []byte("theirs"), 0o600))

	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "err = %v, out = %s", err, out)
	assert.Equal(t, RefusedExitCode, exitErr.ExitCode())
	assert.Contains(t, string(out), "is no longer the one the daemon made")
	assert.NotContains(t, string(out), "RAN")
	got, err := os.ReadFile(filepath.Join(runDir, "keep"))
	require.NoError(t, err, "what was put in its place is untouched")
	assert.Equal(t, "theirs", string(got))
	assert.NoFileExists(t, filepath.Join(runDir, runDirOwnerFile))
}
