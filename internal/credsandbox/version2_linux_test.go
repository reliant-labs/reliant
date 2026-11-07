// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Version 2 of the sandbox (SignalVersion), through Wrap as the daemon calls
// it: a private /tmp and /var/tmp, a hidden credentialed scratch, and the
// signal credentialed commands read. A credentialed step keeps what it is about
// to run in one of those; a credentials-none command must not be able to see
// it, let alone change it, however it races.

// wrapHome points Wrap at a home of the test's own (HOME, which
// os.UserHomeDir reads) and at no machine projects or daemon binary, and
// returns the home.
func wrapHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".reliant"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".reliant", "daemon.json"), []byte("PAT-daemon"), 0o600))
	t.Setenv("HOME", home)
	savedProjects, savedBin := managedProjectsDir, daemonBinDir
	managedProjectsDir, daemonBinDir = t.TempDir(), ""
	t.Cleanup(func() { managedProjectsDir, daemonBinDir = savedProjects, savedBin })
	return home
}

// wrapped is sh as the daemon starts it with credentials, in dir.
func wrapped(t *testing.T, credentials, dir, home, sh string, extraEnv ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", "-c", sh)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + home}, extraEnv...)
	require.Empty(t, Wrap(cmd, credentials))
	return cmd
}

func runWrapped(t *testing.T, credentials, dir, home, sh string, extraEnv ...string) string {
	t.Helper()
	out, err := wrapped(t, credentials, dir, home, sh, extraEnv...).CombinedOutput()
	require.NoError(t, err, "%s", out)
	return string(out)
}

// tmpAliases are the other paths path has through other mounts of its disk (on
// a cloud machine /tmp is also /mnt/workspace-disk/tmp).
func tmpAliases(t *testing.T, path string) []string {
	t.Helper()
	mounts, err := readMounts()
	require.NoError(t, err)
	return aliasPaths([]string{path}, mounts)
}

func TestWrap_OnlyCredentialedCommandsGetTheSignal(t *testing.T) {
	testkit.Slow(t, "probes and builds real user, mount and PID namespaces")
	requireSandbox(t)
	home := wrapHome(t)
	sh := `echo "v=${` + SignalEnv + `:-}"`

	assert.Equal(t, "v=2\n", runWrapped(t, "inherit", home, home, sh))
	assert.Equal(t, "v=2\n", runWrapped(t, "", home, home, sh), "the daemon's own environment is credentialed too")
	assert.Equal(t, "v=\n", runWrapped(t, "none", home, home, sh, SignalEnv+"=2"),
		"a credentials-none command never carries it, even inherited")
}

func TestSandbox_TmpAndVarTmpArePrivate(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces for each command")
	requireSandbox(t)
	home := wrapHome(t)

	// A credentialed step's directories in the machine's /tmp and /var/tmp.
	stepTmp, err := os.MkdirTemp("/tmp", "credsandbox-step-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(stepTmp) })
	require.NoError(t, os.WriteFile(filepath.Join(stepTmp, "run.sh"), []byte("echo trusted\n"), 0o700))
	visible := []string{stepTmp}
	visible = append(visible, tmpAliases(t, stepTmp)...)
	if stepVar, err := os.MkdirTemp("/var/tmp", "credsandbox-step-"); err == nil {
		t.Cleanup(func() { _ = os.RemoveAll(stepVar) })
		visible = append(visible, stepVar)
	}
	own := "credsandbox-own-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	var sees strings.Builder
	for _, p := range visible {
		sees.WriteString(`[ -e '` + p + `' ] && echo 'SEES ` + p + `'` + "\n")
	}
	out := runWrapped(t, "none", home, home, `echo "TMPDIR=$TMPDIR"
`+sees.String()+`
echo mine > /tmp/`+own+` && echo mine > /var/tmp/`+own+` && d=$(mktemp -d) && echo x > "$d/f" && echo OWN-TMP-OK
`, "TMPDIR="+stepTmp)
	assert.Contains(t, out, "TMPDIR=/tmp\n", "TMPDIR is inside the private /tmp, whatever the command was given")
	assert.NotContains(t, out, "SEES", "the machine's /tmp and /var/tmp are out of sight, under every path")
	assert.Contains(t, out, "OWN-TMP-OK")
	assert.NoFileExists(t, filepath.Join("/tmp", own), "what it wrote in /tmp went with it")
	assert.NoFileExists(t, filepath.Join("/var/tmp", own))

	// A credentialed command sees the machine's.
	out = runWrapped(t, "inherit", home, home, sees.String()+"true")
	assert.Contains(t, out, "SEES "+stepTmp)
}

func TestSandbox_ADirectoryStartedInUnderTmpIsCarriedThrough(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	home := wrapHome(t)
	work, err := os.MkdirTemp("/tmp", "credsandbox-work-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(work) })
	require.NoError(t, os.MkdirAll(filepath.Join(work, "repo"), 0o755))
	sibling, err := os.MkdirTemp("/tmp", "credsandbox-sibling-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })

	out := runWrapped(t, "none", filepath.Join(work, "repo"), home,
		`echo made > made && echo up > ../up && echo WORK-OK; [ -e '`+sibling+`' ] && echo SEES-SIBLING; true`)
	assert.Contains(t, out, "WORK-OK", "its own work directory, made by an earlier step, is its to write")
	assert.NotContains(t, out, "SEES-SIBLING")
	assert.FileExists(t, filepath.Join(work, "repo", "made"))
	assert.FileExists(t, filepath.Join(work, "up"))
}

func TestWrap_CredentialedScratchIsHiddenFromCredentialsNone(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces for each command")
	requireSandbox(t)
	home := wrapHome(t)
	scratch := filepath.Join(home, CredScratchRel)

	out := runWrapped(t, "none", home, home, `
ls '`+scratch+`' 2>&1; echo "ls-rc=$?"
ls -a '`+filepath.Dir(scratch)+`'
mkdir -p '`+scratch+`/planted' && echo x > '`+scratch+`/planted/run.sh' && echo PLANTED-IN-RUN
cat '`+filepath.Join(home, ".reliant", "daemon.json")+`' 2>/dev/null || true
`)
	fi, err := os.Stat(scratch)
	require.NoError(t, err, "the daemon creates it before any command runs")
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	assert.Contains(t, out, "No such file or directory")
	assert.NotContains(t, out, "ls-rc=0", "credentials: none cannot list it")
	assert.NotContains(t, out, "cred-scratch\n", "nor see it in its parent")
	assert.NotContains(t, out, "PAT-daemon")
	assert.NoDirExists(t, filepath.Join(scratch, "planted"), "what it made there was its own, and is gone")

	out = runWrapped(t, "inherit", home, home,
		`mkdir -p '`+scratch+`/step' && echo ok > '`+scratch+`/step/run.sh' && ls '`+scratch+`' && echo CRED-WROTE`)
	assert.Contains(t, out, "step\nCRED-WROTE")
	assert.FileExists(t, filepath.Join(scratch, "step", "run.sh"))
}

// A credentials-none command loops, as fast as it can, trying to replace what
// a credentialed step running beside it writes to its scratch and its /tmp and
// then executes. Every write is refused: the step runs what it wrote, every
// time.
func TestSandbox_ConcurrentTamperingWithACredentialedStepIsRefused(t *testing.T) {
	testkit.Slow(t, "runs two sandboxed commands against each other for seconds")
	requireSandbox(t)
	home := wrapHome(t)
	ensureCredScratch()
	stepScratch := filepath.Join(home, CredScratchRel, "step-1")
	require.NoError(t, os.MkdirAll(stepScratch, 0o700))
	stepTmp, err := os.MkdirTemp("/tmp", "credsandbox-step-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(stepTmp) })
	targets := []string{filepath.Join(stepScratch, "run.sh"), filepath.Join(stepTmp, "run.sh")}
	for _, p := range append([]string(nil), targets...) {
		targets = append(targets, tmpAliases(t, p)...)
	}

	// It sees the step's file when it reads what the step wrote; what it
	// writes itself (in its private /tmp, its rebuilt ~/.reliant) it reads as
	// "echo pwned".
	var attack strings.Builder
	for _, p := range targets {
		attack.WriteString(`mkdir -p '` + filepath.Dir(p) + `' 2>/dev/null` + "\n")
	}
	attack.WriteString("end=$((SECONDS+3)); n=0\nwhile [ $SECONDS -lt $end ]; do\n")
	for _, p := range targets {
		attack.WriteString(`  [ "$(< '` + p + `')" = 'echo trusted' ] 2>/dev/null && echo 'SEES ` + p + `'` + "\n")
		attack.WriteString(`  printf 'echo pwned\n' > '` + p + `' 2>/dev/null` + "\n")
	}
	attack.WriteString("  n=$((n+1))\ndone\necho \"LOOPS $n\"\n")

	var step strings.Builder
	step.WriteString("end=$((SECONDS+3)); n=0\nwhile [ $SECONDS -lt $end ]; do\n")
	for _, p := range targets[:2] {
		step.WriteString(`  printf 'echo trusted\n' > '` + p + `'; got=$(bash '` + p + `'); [ "$got" = trusted ] || echo "TAMPERED ` + p + `: $got"` + "\n")
	}
	step.WriteString("  n=$((n+1))\ndone\necho \"STEPS $n\"\n")

	attacker := wrapped(t, "none", home, home, attack.String())
	credentialed := wrapped(t, "inherit", home, home, step.String())
	var attackOut, stepOut []byte
	var attackErr, stepErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); attackOut, attackErr = attacker.CombinedOutput() }()
	go func() { defer wg.Done(); stepOut, stepErr = credentialed.CombinedOutput() }()
	wg.Wait()
	require.NoError(t, attackErr, "%s", attackOut)
	require.NoError(t, stepErr, "%s", stepOut)

	assert.Regexp(t, `LOOPS [1-9][0-9]*`, string(attackOut))
	assert.Regexp(t, `STEPS [1-9][0-9]*`, string(stepOut))
	assert.NotContains(t, string(attackOut), "SEES", "the step's scratch and /tmp are out of sight")
	assert.NotContains(t, string(stepOut), "TAMPERED", "every run executed what the step wrote")
	for _, p := range targets[:2] {
		got, err := os.ReadFile(p)
		require.NoError(t, err)
		assert.Equal(t, "echo trusted\n", string(got))
	}
}
