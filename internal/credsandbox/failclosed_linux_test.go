// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// R1.B2/R2.B2: the paths made read-only around an overlaid cache are only its
// other views through other mounts, never the overlay's own path, a mount
// listed again at it, or a mount holding it. This is a cloud machine's table:
// the workspace disk at /mnt/workspace-disk, /home/workspace, /tmp, and the
// cache bound twice under /var.
func TestOverlayAliases_NeverTheOverlaysOwnPathOrAnEnclosingMount(t *testing.T) {
	mounts := parseMounts(strings.Join([]string{
		`1 0 8:0 / /mnt/workspace-disk rw,relatime - ext4 /dev/d rw`,
		`2 0 8:0 /home /home/workspace rw,relatime - ext4 /dev/d rw`,
		`3 0 8:0 /docker /var/lib/docker rw,relatime - ext4 /dev/d rw`,
		`4 0 8:0 /docker /var/lib/docker rw,nosuid - ext4 /dev/d rw`,
		`5 0 8:0 /tmp /tmp rw,relatime - ext4 /dev/d rw`,
		`6 0 8:0 /docker/.reliant-cache /var/cache/workspace rw,relatime - ext4 /dev/d rw`,
		`7 0 8:0 /docker/.reliant-cache /var/cache/workspace rw,nosuid - ext4 /dev/d rw`,
	}, "\n"))
	got := overlayAliases("/var/cache/workspace/go-mod", mounts)
	assert.ElementsMatch(t, []string{
		"/mnt/workspace-disk/docker/.reliant-cache/go-mod",
		"/var/lib/docker/.reliant-cache/go-mod",
	}, got, "the module cache's other views, each once")
	assert.NotContains(t, got, "/var/cache/workspace/go-mod", "never the overlay's own path, though its mount is listed twice")
	for _, a := range got {
		assert.False(t, under("/var/cache/workspace/go-mod", a), "never a directory holding the overlay: %s", a)
	}
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
		old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC) // long before any sweep could have made it
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
	// Apply appends the run directory, its parent, the spec pipe and the
	// liveness pipe, in that order.
	runDir := cmd.ExtraFiles[len(cmd.ExtraFiles)-4].Name()
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

// R2.B5: an opt-in is classified by its real path, resolved through its
// longest existing ancestor, so a hand-off not made yet under a cache root
// reached through a link is the directory later made and mounted, and one
// whose existing ancestry leaves the roots is refused.
func TestPlan_OptInsAreClassifiedByTheirRealPath(t *testing.T) {
	m := newMachine(t)
	realXDG := filepath.Join(m.root, "var-cache", "xdg")
	require.NoError(t, os.MkdirAll(realXDG, 0o755))
	link := filepath.Join(m.root, "xdg-link")
	require.NoError(t, os.Symlink(realXDG, link))
	require.NoError(t, os.Symlink(m.bin, filepath.Join(realXDG, "escape")))
	env := []string{"XDG_CACHE_HOME=" + link, "HOME=" + m.home}
	agent := func(optIn string) Plan {
		return buildPlan(Plan{}, planEnv{home: m.home, cwd: m.multi, env: env,
			projectsDir: m.projects, daemonBinDir: m.bin, optIn: []string{optIn}, handOffsOnly: true})
	}

	p := agent(filepath.Join(link, "pipeline", "run-1", "out"))
	assert.Empty(t, p.Rejected)
	assert.Contains(t, p.Passthrough, filepath.Join(realXDG, "pipeline", "run-1", "out"), "by the path it really is")

	p = agent(filepath.Join(link, "escape", "planted"))
	assert.Equal(t, []string{filepath.Join(m.bin, "planted")}, p.Rejected, "a link out of the root is followed, and refused")

	p = agent(filepath.Join(link, "..", "home", ".gitconfig"))
	assert.Equal(t, []string{filepath.Join(m.home, ".gitconfig")}, p.Rejected)
}

func TestCanonicalDir_ResolvesTheLongestExistingAncestor(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(real, link))
	assert.Equal(t, filepath.Join(real, "a", "b"), canonicalDir(filepath.Join(link, "a", "b")))
	assert.Equal(t, real, canonicalDir(link))
	assert.Equal(t, "/no/such/dir", canonicalDir("/no/such/./dir"))
}

// A path already covered with the null device (by the host, or by the sandbox
// a nested daemon's tests run in: /run/docker.sock on a cloud machine's
// credentials-none gate) hides nothing and is not covered again; the second
// cover failed every credentials-none command there.
func TestIsNullDevice(t *testing.T) {
	fi, err := os.Lstat("/dev/null")
	require.NoError(t, err)
	assert.True(t, isNullDevice(fi))
	f := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(f, nil, 0o600))
	fi, err = os.Lstat(f)
	require.NoError(t, err)
	assert.False(t, isNullDevice(fi))
}

// A directory hidden inside a shared scratch directory (GH_CONFIG_DIR in /tmp,
// a directory in /dev/shm) is covered by an empty read-only tmpfs: the null
// device cannot be mounted on a directory.
func TestSandbox_HidesADirectoryInASharedScratchDirectory(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	shm, err := os.MkdirTemp("/dev/shm", "credsandbox-hidden-")
	if err != nil {
		t.Skipf("needs a writable /dev/shm: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shm) })
	require.NoError(t, os.WriteFile(filepath.Join(shm, "hosts.yml"), []byte("PAT-gh"), 0o600))

	out, code := run(t, Plan{Hide: []string{shm}}, `cat '`+shm+`/hosts.yml' 2>&1; echo x > '`+shm+`/planted' 2>&1; echo done`)
	require.Equal(t, 0, code, out)
	assert.NotContains(t, out, "PAT-gh")
	assert.Contains(t, out, "done")
	assert.NoFileExists(t, filepath.Join(shm, "planted"))
}

// R5.B1: a PATH directory that does not exist yet is sealed wherever it is,
// not only under the home; a PATH entry that is a file is read-only; a cache
// that is not a directory refuses the command.
func TestPrepareOverlays_MissingAndOddPathEntries(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "opt", "tools", "bin")
	file := filepath.Join(root, "bin-file")
	require.NoError(t, os.WriteFile(file, nil, 0o755))
	p, err := prepareOverlays(Plan{Overlay: []Overlay{{Path: missing}, {Path: file}}})
	require.NoError(t, err)
	assert.Contains(t, p.Seal, missing)
	assert.Contains(t, p.ReadOnly, file)
	assert.Empty(t, p.Overlay)

	_, err = prepareOverlays(Plan{Overlay: []Overlay{{Path: file, Env: "GOCACHE", Cache: true}}})
	assert.Error(t, err, "a cache that is not a directory")
}

// A path to seal whose nearest directory the sandbox may not rebuild (a
// shared scratch directory, "/" or a top-level one) but the daemon's user can
// write refuses the plan; one under the private /tmp is fine.
func TestCheckSeals_RefusesWhatCannotBeSealed(t *testing.T) {
	assert.NoError(t, checkSeals([]string{filepath.Join(t.TempDir(), "x", "y")}), "under /tmp: private to the command")
	shm, err := os.MkdirTemp("/dev/shm", "credsandbox-seal-")
	if err != nil {
		t.Skipf("needs a writable /dev/shm: %v", err)
	}
	require.NoError(t, os.Remove(shm))
	assert.Error(t, checkSeals([]string{filepath.Join(shm, "bin")}), "/dev/shm is shared and cannot be rebuilt")
}

// R5.B1 and R6.B2 in a real sandbox, outside both the home and /tmp: a PATH
// directory that does not exist yet, and Go's and git's config under a
// non-default XDG_CONFIG_HOME, cannot be made or changed for a later
// credentialed command.
func TestSandbox_AbsentPathDirsAndXDGConfigOutsideTheHomeAreProtected(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	home := wrapHome(t)
	// Under this checkout: /tmp is private to the command, so only a
	// directory elsewhere tests a write that could persist. The TestMain
	// guard fails the run if it is ever left behind.
	root, err := os.MkdirTemp(".", ".credsandbox-outside-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	require.NoError(t, err)
	bin := filepath.Join(root, "opt", "tools", "bin")
	xdg := filepath.Join(root, "xdg-config")
	require.NoError(t, os.MkdirAll(filepath.Join(xdg, "git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(xdg, "git", "config"), []byte("[core]\n"), 0o644))

	out := runWrapped(t, "none", home, home, `
mkdir -p '`+bin+`' && printf '#!/bin/sh\necho pwned\n' > '`+bin+`/git' && chmod +x '`+bin+`/git' && echo PATH-PLANTED-IN-RUN
mkdir -p '`+xdg+`/go' 2>/dev/null; echo 'GOFLAGS=-toolexec=/x' > '`+xdg+`/go/env' 2>&1 || echo GOENV-DENIED
echo '[core] hooksPath = /x' >> '`+xdg+`/git/config' 2>&1 || echo GITCONFIG-DENIED
true`, "PATH="+bin+":/usr/bin:/bin", "XDG_CONFIG_HOME="+xdg)
	assert.NoDirExists(t, bin, "an absent PATH directory outside the home does not outlive the command")
	assert.NoFileExists(t, filepath.Join(xdg, "go", "env"), "nor does $XDG_CONFIG_HOME/go/env")
	assert.Contains(t, out, "GITCONFIG-DENIED")
	got, err := os.ReadFile(filepath.Join(xdg, "git", "config"))
	require.NoError(t, err)
	assert.Equal(t, "[core]\n", string(got))
}
