// Copyright (c) 2025 Reliant Labs
package credsandbox

import (
	"os"
	"path/filepath"
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
