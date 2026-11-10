// Copyright (c) 2025 Reliant Labs
package credsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The control-plane pipeline's hand-off, as its run nodes write it: started in
// the project root (not the run directory), the command cd's into a per-run
// directory under the cache root and writes a patch there. The node names the
// workflow's cache directory, the run directory's parent, in writable_checkouts.
// That keeps the patch; without it the patch is discarded, which is how
// pr-regen and pr-fix lost theirs (cp pr-regen run 210d, pr-fix 1313063d). A
// directory the command only cd's into is never its own: the sandbox keeps the
// directory it is STARTED in.
func TestSandbox_AHandOffUnderANamedCacheDirectorySurvivesACd(t *testing.T) {
	testkit.Slow(t, "runs sandboxed commands")
	requireSandbox(t)
	home := wrapHome(t)
	xdg := filepath.Join(filepath.Dir(home), "xdg")
	project := filepath.Join(filepath.Dir(home), "project")
	require.NoError(t, os.MkdirAll(project, 0o755))
	env := "XDG_CACHE_HOME=" + xdg
	write := func(runDir string) string {
		return `mkdir -p '` + runDir + `' && cd '` + runDir + `' && echo patch > regen.patch && echo msg > regen.msg && echo wrote`
	}

	t.Run("named parent: kept", func(t *testing.T) {
		run := filepath.Join(xdg, "pr-regen", "run-a")
		out, err := wrappedWritable(t, project, home, []string{filepath.Join(xdg, "pr-regen")}, write(run), env).CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.FileExists(t, filepath.Join(run, "regen.patch"))
		assert.FileExists(t, filepath.Join(run, "regen.msg"))
		assert.NotContains(t, string(out), "discarded", "nothing of the hand-off may be reported discarded")
	})

	t.Run("nothing named: a cd alone is discarded", func(t *testing.T) {
		run := filepath.Join(xdg, "pr-fix", "run-b")
		out, err := wrappedWritable(t, project, home, nil, write(run), env).CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.NoFileExists(t, filepath.Join(run, "regen.patch"))
		assert.Contains(t, string(out), "credsandbox: discarded")
	})

	// No process a command started outlives it to write a kept directory after
	// it exits: a background job, a setsid one and a nohup one. The control-plane
	// pipeline's hand-off slots rely on it: once a run's command has ended,
	// nothing of that run writes the slot the next run takes.
	t.Run("no child writes after the command exits", func(t *testing.T) {
		keep := filepath.Join(xdg, "pr-fix", "slot-1")
		require.NoError(t, os.MkdirAll(keep, 0o755))
		late := filepath.Join(keep, "late")
		sh := `( sleep 1; echo a > '` + late + `-bg' ) & ` +
			`setsid sh -c "sleep 1; echo b > '` + late + `-setsid'" & ` +
			`nohup sh -c "sleep 1; echo c > '` + late + `-nohup'" >/dev/null 2>&1 & echo started`
		out, err := wrappedWritable(t, project, home, []string{keep}, sh, env).CombinedOutput()
		require.NoError(t, err, "%s", out)
		time.Sleep(3 * time.Second)
		for _, f := range []string{"-bg", "-setsid", "-nohup"} {
			assert.NoFileExists(t, late+f, "a %s child wrote after its command exited", f[1:])
		}
	})

	// A JS gate: pnpm installs into a store under the cache root, and a later
	// process of the same command (generate) reads it. The store is the
	// command's own: visible to every process it starts, gone when it exits.
	t.Run("a store written and read within one command", func(t *testing.T) {
		store := filepath.Join(xdg, ".pnpm-store", "v11", "files")
		sh := `mkdir -p '` + store + `' && bash -c "echo dep > '` + store + `/dep'" && bash -c "cat '` + store + `/dep'"`
		out, err := wrappedWritable(t, project, home, []string{filepath.Join(xdg, "pr-fix")}, sh, env).CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.Contains(t, string(out), "dep\n", "the install's store is visible to the generate the same command runs")
		assert.NoFileExists(t, filepath.Join(store, "dep"), "and the command's own: discarded when it exits")
	})
}

// The control-plane pipeline's slot check (cp #2390): credentialed steps mirror each
// hand-off slot's lease to a file under the cache root that no node keeps, and every
// slot-bound credentials:none command reads it first, before any of the PR's code runs.
// It relies on what this test holds:
//   - a none command cannot forge the mirror for a later command: its write is its own,
//     discarded when it exits, and the next command reads the credentialed value;
//   - a running none command reads the mirror as it was when it started (the check is
//     made then, and only then);
//   - a shared flock it takes on its kept slot directory holds against an exclusive one
//     taken outside (and one on a file in the overlaid cache does not);
//   - a kept directory is bound by inode when it starts (renamed while it runs, the command
//     still writes only the old one);
//   - an undumpable parent is out of reach of what it starts (no ptrace), so the slot
//     check's lock holder cannot have its lock taken away.
func TestSandbox_ALeaseMirrorUnderTheCacheIsReadOnlyToANoneCommand(t *testing.T) {
	testkit.Slow(t, "runs sandboxed commands")
	requireSandbox(t)
	home := wrapHome(t)
	xdg := filepath.Join(filepath.Dir(home), "xdg")
	project := filepath.Join(filepath.Dir(home), "project")
	require.NoError(t, os.MkdirAll(project, 0o755))
	env := "XDG_CACHE_HOME=" + xdg
	slot := filepath.Join(xdg, "pr-fix", "slot-1")
	mirror := filepath.Join(xdg, "pipeline-slots", "pr-fix", "slot-1")
	require.NoError(t, os.MkdirAll(slot, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(mirror), 0o755))
	require.NoError(t, os.WriteFile(mirror, []byte("runA 100 3\n"), 0o644))

	t.Run("a forged write is the command's own", func(t *testing.T) {
		out, err := wrappedWritable(t, project, home, []string{slot},
			`cat '`+mirror+`' && echo 'runEVIL 9999999999 9' > '`+mirror+`' && cat '`+mirror+`'`, env).CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.Contains(t, string(out), "runA 100 3\nrunEVIL 9999999999 9\n", "the command reads the credentialed value, then its own write")
		assert.Contains(t, string(out), "credsandbox: discarded")
		got, err := os.ReadFile(mirror)
		require.NoError(t, err)
		assert.Equal(t, "runA 100 3\n", string(got), "the forgery is discarded")
		out, err = wrappedWritable(t, project, home, []string{slot}, `cat '`+mirror+`'`, env).CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.Equal(t, "runA 100 3\n", string(out), "a later command reads the credentialed value")
	})

	t.Run("a running command reads the mirror as it started", func(t *testing.T) {
		cmd := wrappedWritable(t, project, home, []string{slot}, `cat '`+mirror+`'; sleep 2; cat '`+mirror+`'`, env)
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		require.NoError(t, cmd.Start())
		time.Sleep(time.Second)
		require.NoError(t, os.WriteFile(mirror+".new", []byte("runB 200 4\n"), 0o644))
		require.NoError(t, os.Rename(mirror+".new", mirror))
		require.NoError(t, cmd.Wait(), "%s", out.String())
		assert.Equal(t, "runA 100 3\nrunA 100 3\n", out.String())
	})

	// The slot check's lifetime reservation: a shared flock a none command takes on its kept
	// slot directory conflicts with an exclusive one a credentialed step takes outside (a
	// lock on a file in the overlaid cache does not: the overlay's inode is not the real one).
	lockedInside := func(t *testing.T, path string) (outside string) {
		cmd := wrappedWritable(t, project, home, []string{slot}, `exec 9<'`+path+`' && /usr/bin/flock -s -n 9 && echo locked && sleep 2`, env)
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		require.NoError(t, cmd.Start())
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(out.String(), "locked") && time.Now().Before(deadline); {
			time.Sleep(50 * time.Millisecond)
		}
		o, _ := exec.Command("bash", "-c", `exec 9<'`+path+`' && /usr/bin/flock -x -n 9 && echo free || echo held`).CombinedOutput()
		require.NoError(t, cmd.Wait(), "%s", out.String())
		require.Contains(t, out.String(), "locked")
		return strings.TrimSpace(string(o))
	}
	t.Run("a lock on the kept slot directory is held across the sandbox", func(t *testing.T) {
		assert.Equal(t, "held", lockedInside(t, slot))
		o, _ := exec.Command("bash", "-c", `exec 9<'`+slot+`' && /usr/bin/flock -x -n 9 && echo free`).CombinedOutput()
		assert.Equal(t, "free", strings.TrimSpace(string(o)), "and released when the command ends")
	})
	t.Run("a lock on a file in the overlaid cache is not", func(t *testing.T) {
		lock := mirror + ".lock"
		require.NoError(t, os.WriteFile(lock, nil, 0o644))
		assert.Equal(t, "free", lockedInside(t, lock))
	})

	// A kept directory is bound by inode when the command starts: replaced (removed and made
	// again) or renamed while it runs, the command still writes only the one it was given.
	t.Run("a kept directory is bound by inode", func(t *testing.T) {
		cmd := wrappedWritable(t, project, home, []string{slot}, `sleep 1.5; echo late > '`+slot+`/late' && echo wrote`, env)
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		require.NoError(t, cmd.Start())
		time.Sleep(500 * time.Millisecond)
		require.NoError(t, os.Rename(slot, slot+".old"))
		require.NoError(t, os.MkdirAll(slot, 0o755))
		require.NoError(t, cmd.Wait(), "%s", out.String())
		assert.NoFileExists(t, filepath.Join(slot, "late"), "the new directory gets nothing")
		assert.FileExists(t, filepath.Join(slot+".old", "late"), "the write went to the directory it was bound to")
		require.NoError(t, os.RemoveAll(slot+".old"))
	})

	// What a none command starts can ptrace (and so take descriptors from) a dumpable parent
	// of the same user, and not one that made itself undumpable: the slot check's lock holder
	// does, so nothing the PR runs can drop its lock early.
	t.Run("an undumpable parent is out of its children's reach", func(t *testing.T) {
		py := `/usr/bin/python3 -I -c '
import ctypes, os, sys
libc = ctypes.CDLL(None, use_errno=True)
if sys.argv[1] == "undumpable": libc.prctl(4, 0, 0, 0, 0)
pid = os.fork()
if pid == 0:
    p = os.getppid()
    r = libc.ptrace(16, p, 0, 0)
    if r == 0:
        os.waitpid(p, 0x40000000)  # __WALL: the attach has stopped it
        libc.ptrace(17, p, 0, 0)   # detach: it runs on
    print("attach=" + ("ok" if r == 0 else "refused"), flush=True)
    os._exit(0)
os.waitpid(pid, 0)
' `
		for mode, want := range map[string]string{"dumpable": "attach=ok", "undumpable": "attach=refused"} {
			out, err := wrappedWritable(t, project, home, []string{slot}, py+mode, env).CombinedOutput()
			require.NoError(t, err, "%s", out)
			assert.Contains(t, string(out), want, mode)
		}
	})

}
