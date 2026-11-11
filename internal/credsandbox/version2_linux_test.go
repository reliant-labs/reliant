// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
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
	require.Empty(t, Wrap(cmd, credentials, Writable{}))
	return cmd
}

// wrappedWritable is wrapped for a run node that names writable directories.
func wrappedWritable(t *testing.T, dir, home string, writable []string, sh string, extraEnv ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", "-c", sh)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + home}, extraEnv...)
	require.Empty(t, Wrap(cmd, "none", Writable{Paths: writable}))
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
	// Run inside another sandbox (a gate's tests under credentials: none), the
	// disk has more mounts (the outer sandbox's rebuilt directories), and
	// aliasPaths can name path itself, or one view twice. The other views are
	// the ones under test.
	var out []string
	for _, a := range dedupe(aliasPaths([]string{path}, mounts)) {
		if a != path {
			out = append(out, a)
		}
	}
	return out
}

// aliasedFixtureDir makes, and removes when the test ends, a directory on a
// disk this machine mounts at a second path (a cloud machine's
// /mnt/workspace-disk) and outside the private /tmp and /var/tmp: they cover
// every path they have, so nothing there has another view inside the sandbox
// to test. Under this checkout (the TestMain guard fails the run if it is ever
// left behind), or else under the user's cache or home directory, when the
// checkout is in /tmp; the test skips when none has a second view. Call it
// before wrapHome, which moves HOME.
func aliasedFixtureDir(t *testing.T) string {
	t.Helper()
	mounts, err := readMounts()
	require.NoError(t, err)
	var private []string
	for _, d := range privateTmpTargets(mounts) {
		private = append(private, d.targets...)
	}
	var candidates []string
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	if cache, err := os.UserCacheDir(); err == nil {
		candidates = append(candidates, cache)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, home)
	}
	// Every other view must be one this user can write through outside the
	// sandbox (a view through root's 0710 /mnt/workspace-disk/docker is not),
	// or the writes the test expects to work there fail for that reason.
	viewable := func(c string) bool {
		aliases := tmpAliases(t, c)
		for _, a := range aliases {
			if unix.Access(a, unix.W_OK) != nil {
				return false
			}
		}
		return len(aliases) > 0
	}
	for _, c := range candidates {
		if under(c, private...) || under(canonicalDir(c), private...) || !viewable(c) {
			continue
		}
		root, err := os.MkdirTemp(c, ".credsandbox-alias-")
		if err != nil {
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
		return root
	}
	t.Skipf("needs a directory outside /tmp and /var/tmp on a disk mounted twice (a cloud machine's /mnt/workspace-disk); tried %v", candidates)
	return ""
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
	own := "credsandbox-own-" + filepath.Base(t.TempDir()) // unique: the name of a directory of this test's own

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

// The daemon gives every command a GOTMPDIR of its own under the machine's
// /tmp (proctree), after Wrap: inside the sandbox it is the private /tmp, or
// every go build would fail to make its work directory.
func TestSandbox_TempVariablesAddedAfterWrapPointIntoThePrivateTmp(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	home := wrapHome(t)
	cmd := wrapped(t, "none", home, home, `echo "GOTMPDIR=$GOTMPDIR TMP=$TMP TMPDIR=$TMPDIR"; mkdir "$GOTMPDIR/go-build1" && echo WORK-DIR-OK`, "TMP=/var/tmp/x")
	cmd.Env = append(cmd.Env, "GOTMPDIR="+filepath.Join(os.TempDir(), "reliant-gotmp-1", "gotmp-1-1"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Contains(t, string(out), "GOTMPDIR=/tmp TMP=/tmp TMPDIR=/tmp\nWORK-DIR-OK")
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
	plant := `mkdir -p '` + scratch + `/planted' && echo x > '` + scratch + `/planted/run.sh' && echo PLANTED-IN-RUN`

	// Before any credentialed command: absent, and sealed, so what a
	// credentials-none command makes there does not outlive it.
	out := runWrapped(t, "none", home, home, plant)
	assert.Contains(t, out, "PLANTED-IN-RUN")
	assert.NoDirExists(t, scratch, "a credentials-none command cannot be the one to make it")

	// The first credentialed command makes it, private.
	runWrapped(t, "inherit", home, home, "true")

	out = runWrapped(t, "none", home, home, `
ls '`+scratch+`' 2>&1; echo "ls-rc=$?"
ls -a '`+filepath.Dir(scratch)+`'
mkdir -p '`+scratch+`/planted' && echo x > '`+scratch+`/planted/run.sh' && echo PLANTED-IN-RUN
cat '`+filepath.Join(home, ".reliant", "daemon.json")+`' 2>/dev/null || true
`)
	fi, err := os.Stat(scratch)
	require.NoError(t, err, "the daemon made it for the credentialed command")
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

// A credentialed step hands a later step a directory under XDG_CACHE_HOME (a
// fetched repository, a pinned script). A credentials-none command running
// beside it, of the same run or another, cannot change it however it races,
// and is told its writes there were its own; the one consumer whose run node
// names its output directory writes it for real.
func TestSandbox_CacheHandOffsAreTamperProofButForTheirNamedConsumer(t *testing.T) {
	testkit.Slow(t, "runs sandboxed commands against a credentialed one for seconds")
	requireSandbox(t)
	home := wrapHome(t)
	xdg := filepath.Join(filepath.Dir(home), "xdg")
	run := filepath.Join(xdg, "pipeline", "run-1")
	out := filepath.Join(run, "out")
	require.NoError(t, os.MkdirAll(run, 0o755))
	script := filepath.Join(run, "script.sh")
	require.NoError(t, os.WriteFile(script, []byte("echo trusted\n"), 0o755))
	env := "XDG_CACHE_HOME=" + xdg

	attack := `end=$((SECONDS+3)); n=0
while [ $SECONDS -lt $end ]; do
  printf 'echo pwned\n' > '` + script + `' 2>/dev/null; mkdir -p '` + out + `' 2>/dev/null; echo forged > '` + out + `/result' 2>/dev/null
  n=$((n+1))
done
echo "LOOPS $n"`
	step := `end=$((SECONDS+3)); n=0
while [ $SECONDS -lt $end ]; do
  got=$(bash '` + script + `'); [ "$got" = trusted ] || echo "TAMPERED: $got"; n=$((n+1))
done
echo "STEPS $n"`
	attacker := wrapped(t, "none", home, home, attack, env)
	credentialed := wrapped(t, "inherit", home, home, step, env)
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
	assert.NotContains(t, string(stepOut), "TAMPERED")
	assert.Contains(t, string(attackOut), "credsandbox: discarded", "the command is told its writes there were its own")
	assert.NoFileExists(t, filepath.Join(out, "result"))
	got, err := os.ReadFile(script)
	require.NoError(t, err)
	assert.Equal(t, "echo trusted\n", string(got))

	// The consumer reads the hand-off and writes the output its node names.
	cmd := wrappedWritable(t, home, home, []string{out},
		`bash '`+script+`' > '`+out+`/result' && echo x > '`+filepath.Join(run, "elsewhere")+`' 2>/dev/null; echo done`, env)
	b, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", b)
	got, err = os.ReadFile(filepath.Join(out, "result"))
	require.NoError(t, err)
	assert.Equal(t, "trusted\n", string(got))
	assert.NoFileExists(t, filepath.Join(run, "elsewhere"), "only the named directory is real")
}

// pr-fix's implementer: an agent's shell, bound credentials: none, started in
// the project root, committing in its per-run clone under XDG_CACHE_HOME,
// which the workflow bound as the shell's writable_checkouts. The commit is
// real; a shared checkout or the daemon's binary in the same binding refuses
// the command and names it.
func TestWrap_ABoundAgentShellCommitsInItsCloneAndNowhereElse(t *testing.T) {
	testkit.Slow(t, "runs git in real user, mount and PID namespaces")
	requireSandbox(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git on PATH")
	}
	home := wrapHome(t)
	xdg := filepath.Join(filepath.Dir(home), "xdg")
	clone := filepath.Join(xdg, "pr-fix", "repo-12-run-1", "repo")
	shared := filepath.Join(managedProjectsDir, "repo")
	gitEnv := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CACHE_HOME=" + xdg, "GIT_CONFIG_NOSYSTEM=1", //nolint:forbidigo // the test's own PATH for git
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}
	setup := exec.Command("bash", "-c", `set -e
git init -q -b main '`+shared+`' && cd '`+shared+`' && echo a > a && git add a && git commit -qm a
git clone -q --shared '`+shared+`' '`+clone+`'`)
	setup.Env = gitEnv
	out, err := setup.CombinedOutput()
	require.NoError(t, err, "%s", out)

	bound := Writable{Paths: []string{clone}, HandOffsOnly: true}
	cmd := exec.Command("bash", "-c", `cd '`+clone+`' && echo fix > fix.go && git add fix.go && git commit -qm fix && echo COMMIT-OK
echo evil > '`+shared+`/a' 2>/dev/null || echo SHARED-DENIED`)
	cmd.Dir, cmd.Env = managedProjectsDir, gitEnv
	require.Empty(t, Wrap(cmd, "none", bound))
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Contains(t, string(out), "COMMIT-OK")
	assert.Contains(t, string(out), "SHARED-DENIED")
	assert.NotContains(t, string(out), "credsandbox: discarded", "nothing it wrote in its clone was thrown away")
	log, err := exec.Command("git", "-C", clone, "log", "--format=%s").Output()
	require.NoError(t, err)
	assert.Equal(t, "fix\na\n", string(log), "the commit survives the command")
	assert.FileExists(t, filepath.Join(clone, "fix.go"))

	for _, bad := range []string{shared, "/daemon-bin", filepath.Join(home, ".gitconfig")} {
		cmd := exec.Command("bash", "-c", "true")
		cmd.Dir, cmd.Env = managedProjectsDir, gitEnv
		refusal := Wrap(cmd, "none", Writable{Paths: []string{clone, bad}, HandOffsOnly: true})
		assert.Contains(t, refusal, "writable_checkouts names "+bad, "a bound agent shell may not name %s", bad)
	}
	// A run node may name the shared checkout.
	cmd = exec.Command("bash", "-c", "true")
	cmd.Dir, cmd.Env = managedProjectsDir, gitEnv
	assert.Empty(t, Wrap(cmd, "none", Writable{Paths: []string{shared}}))
}

// The module cache, the build cache and the cache roots are private through
// their own path; through any other mount of the same disk they are read-only.
func TestSandbox_AnOverlaidCacheIsReadOnlyUnderItsOtherPaths(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	root := aliasedFixtureDir(t) // before wrapHome, which moves HOME
	home := wrapHome(t)
	modcache, beside := filepath.Join(root, "mod"), filepath.Join(root, "beside")
	for _, d := range []string{modcache, beside} {
		require.NoError(t, os.Mkdir(d, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(modcache, "mod.zip"), []byte("trusted"), 0o644))
	aliases := tmpAliases(t, modcache)
	require.NotEmpty(t, aliases, "aliasedFixtureDir chose %s for its second view", root)
	var sh strings.Builder
	sh.WriteString(`echo poisoned > '` + filepath.Join(modcache, "mod.zip") + `' && echo PRIVATE-OK` + "\n")
	for _, a := range aliases {
		sh.WriteString(`echo poisoned > '` + filepath.Join(a, "mod.zip") + `' 2>&1 || echo 'ALIAS-DENIED ` + a + `'` + "\n")
		// The same disk beside the cache, through the same view, is untouched:
		// only the cache's own view is read-only.
		sh.WriteString(`echo ok > '` + filepath.Join(filepath.Dir(a), "beside", "f") + `' && echo 'BESIDE-OK ` + a + `'` + "\n")
	}
	out := runWrapped(t, "none", home, home, sh.String(), "GOMODCACHE="+modcache)
	assert.Contains(t, out, "PRIVATE-OK", "writes through the overlay work")
	for _, a := range aliases {
		assert.Contains(t, out, "ALIAS-DENIED "+a, "and are refused through every other view")
		assert.Contains(t, out, "BESIDE-OK "+a, "while the rest of that view stays writable")
	}
	assert.Regexp(t, deniedByTheKernel, out)
	got, err := os.ReadFile(filepath.Join(modcache, "mod.zip"))
	require.NoError(t, err)
	assert.Equal(t, "trusted", string(got))
	assert.FileExists(t, filepath.Join(beside, "f"))
	// And outside the command, nothing became read-only.
	require.NoError(t, os.WriteFile(filepath.Join(modcache, "after"), []byte("x"), 0o644))
}

// forge's config directory, rebuilt without its credentials file, is
// read-only entry by entry: a rebuilt directory is a bind mount per entry, and
// each must be remounted read-only itself.
func TestSandbox_ForgesConfigIsReadOnlyAndItsCredentialsAbsent(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	home := wrapHome(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("FORGE_HOME", "")
	forge := filepath.Join(home, ".config", "forge")
	require.NoError(t, os.MkdirAll(forge, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(forge, "credentials.json"), []byte("PAT-forge"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(forge, "storage.json"), []byte(`{"trusted":true}`), 0o600))

	out := runWrapped(t, "none", home, home, `
cat '`+filepath.Join(forge, "credentials.json")+`' 2>/dev/null
cat '`+filepath.Join(forge, "storage.json")+`'; echo
echo '{"evil":true}' > '`+filepath.Join(forge, "storage.json")+`' 2>&1 || echo STORAGE-DENIED
echo x > '`+filepath.Join(forge, "new.json")+`' 2>&1 || echo NEW-DENIED
true`)
	assert.NotContains(t, out, "PAT-forge")
	assert.Contains(t, out, `{"trusted":true}`, "forge still reads its config")
	assert.Contains(t, out, "STORAGE-DENIED")
	assert.Contains(t, out, "NEW-DENIED")
	assert.Regexp(t, deniedByTheKernel, out)

	out = runWrapped(t, "inherit", home, home, `echo '{"ok":1}' > '`+filepath.Join(forge, "storage.json")+`' && echo CRED-WROTE`)
	assert.Contains(t, out, "CRED-WROTE")
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

// A PATH directory the daemon's user cannot write is not thereby safe: the
// user's own executable inside it can be overwritten in place, and the user
// can chmod the directory it owns. Both are refused, so a credentials-none
// command cannot poison a program a later credentialed command runs.
func TestSandbox_AProgramInANonWritablePathDirectoryCannotBePoisoned(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	home := wrapHome(t)
	// Under this checkout, not /tmp: the command's /tmp is private.
	root, err := os.MkdirTemp(".", ".credsandbox-pathdir-")
	require.NoError(t, err)
	root, err = filepath.Abs(root)
	require.NoError(t, err)
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	prog := filepath.Join(bin, "tool")
	require.NoError(t, os.WriteFile(prog, []byte("#!/bin/sh\necho trusted\n"), 0o755))
	require.NoError(t, os.Chmod(bin, 0o555))
	t.Cleanup(func() { _ = os.Chmod(bin, 0o755); _ = os.RemoveAll(root) })
	require.Error(t, unix.Access(bin, unix.W_OK), "fixture: the PATH directory must not be writable")

	out := runWrapped(t, "none", home, home, `
echo 'echo poisoned' > '`+prog+`' 2>/dev/null && echo OVERWROTE
chmod u+w '`+bin+`' 2>/dev/null && echo CHMODDED
echo ran`, "PATH="+bin+":/usr/bin:/bin")
	assert.Contains(t, out, "ran")
	assert.NotContains(t, out, "OVERWROTE")
	assert.NotContains(t, out, "CHMODDED")
	got, err := os.ReadFile(prog)
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\necho trusted\n", string(got))
}

// A git credential helper configured as an absolute path that does not exist
// yet, outside home, PATH and the command's checkouts, is sealed: a
// credentials-none command that creates it there would supply every later
// credentialed git's credential.
func TestSandbox_AnAbsentAbsoluteCredentialHelperCannotBePlanted(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	home := wrapHome(t)
	// Under this checkout, not /tmp: the command's /tmp is private.
	root, err := os.MkdirTemp(".", ".credsandbox-helper-")
	require.NoError(t, err)
	root, err = filepath.Abs(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	helper := filepath.Join(root, "helpers", "git-credential-custom")
	require.NoError(t, os.MkdirAll(filepath.Dir(helper), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[credential]\n\thelper = "+helper+"\n"), 0o644))

	out := runWrapped(t, "none", home, home, `
printf '#!/bin/sh\necho password=planted\n' > '`+helper+`' 2>/dev/null && chmod +x '`+helper+`' 2>/dev/null
echo ran`)
	assert.Contains(t, out, "ran")
	_, err = os.Lstat(helper)
	assert.True(t, os.IsNotExist(err), "a planted credential helper outlived the credentials-none command: %v", err)
}

// R15.B1: a trusted path that is a symlink is read-only only at its target, so
// the link itself could be replaced: a helper the git config names, and a
// config file it includes, each a link outside every protected root. The
// command replaces both links; on the host both still name the trusted
// originals, which are unchanged.
func TestSandbox_ALinkedHelperAndIncludedConfigCannotBeReplaced(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	home := wrapHome(t)
	// Under this checkout, not /tmp: the command's /tmp is private.
	root, err := os.MkdirTemp(".", ".credsandbox-links-")
	require.NoError(t, err)
	root, err = filepath.Abs(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	realHelper := filepath.Join(root, "real", "git-credential-custom")
	realInclude := filepath.Join(root, "real", "include.gitconfig")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "real"), 0o755))
	require.NoError(t, os.WriteFile(realHelper, []byte("#!/bin/sh\necho password=trusted\n"), 0o755))
	require.NoError(t, os.WriteFile(realInclude, []byte("[user]\n\tname = trusted\n"), 0o644))
	links := filepath.Join(root, "links")
	require.NoError(t, os.MkdirAll(links, 0o755))
	helper := filepath.Join(links, "helper")
	include := filepath.Join(links, "include")
	require.NoError(t, os.Symlink("../real/git-credential-custom", helper))
	require.NoError(t, os.Symlink(realInclude, include))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[credential]\n\thelper = "+helper+"\n[include]\n\tpath = "+include+"\n"), 0o644))

	out := runWrapped(t, "none", home, home, `
rm -f '`+helper+`' '`+include+`' 2>/dev/null
printf '#!/bin/sh\necho password=planted\n' > '`+helper+`' 2>/dev/null && chmod +x '`+helper+`' 2>/dev/null
printf '[credential]\n\thelper = !planted\n' > '`+include+`' 2>/dev/null
echo ran`)
	assert.Contains(t, out, "ran")
	for link, want := range map[string]string{helper: "../real/git-credential-custom", include: realInclude} {
		got, err := os.Readlink(link)
		assert.NoError(t, err, "%s is no longer a link on the host", link)
		assert.Equal(t, want, got, "the command replaced the trusted link %s", link)
	}
	b, err := os.ReadFile(helper)
	require.NoError(t, err)
	assert.Contains(t, string(b), "password=trusted", "the helper a credentialed git runs changed")
	b, err = os.ReadFile(include)
	require.NoError(t, err)
	assert.Contains(t, string(b), "name = trusted", "the config a credentialed git includes changed")
}

// R16.B1: ~/.config/git/config, which git reads directly, linked to a
// writable file outside every protected root. The command overwrites the
// link's target with a planted credential helper; on the host the target is
// unchanged.
func TestSandbox_ALinkedGitConfigsTargetCannotBeOverwritten(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	home := wrapHome(t)
	// Under this checkout, not /tmp: the command's /tmp is private.
	root, err := os.MkdirTemp(".", ".credsandbox-config-")
	require.NoError(t, err)
	root, err = filepath.Abs(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	target := filepath.Join(root, "git.conf")
	require.NoError(t, os.WriteFile(target, []byte("[user]\n\tname = trusted\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".config", "git"), 0o755))
	config := filepath.Join(home, ".config", "git", "config")
	require.NoError(t, os.Symlink(target, config))

	out := runWrapped(t, "none", home, home, `
printf '[credential]\n\thelper = !planted\n' > '`+target+`' 2>/dev/null
printf '[credential]\n\thelper = !planted\n' > '`+config+`' 2>/dev/null
echo ran`)
	assert.Contains(t, out, "ran")
	got, err := os.Readlink(config)
	require.NoError(t, err, "the trusted config is no longer a link on the host")
	assert.Equal(t, target, got)
	b, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "[user]\n\tname = trusted\n", string(b), "the config a credentialed git reads changed")
}

// R17.B1: a read-only entry that is a link is protected through every mount of
// its TARGET's filesystem, not only through the mounts that show the link.
func TestReadOnlyPaths_ALinksTargetIsProtectedThroughItsOtherMounts(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	target := filepath.Join(dir, "srv", "git.conf")
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte("[user]\n"), 0o644))
	link := filepath.Join(dir, "home", "config")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(target, link))

	mounts := parseMounts(strings.Join([]string{
		`1 1 8:0 / / rw - ext4 /dev/d rw`,
		`2 1 8:0 / /mnt/workspace-disk rw - ext4 /dev/d rw`,
	}, "\n"))
	got := readOnlyPaths([]string{link}, mounts)
	assert.Contains(t, got, link, "the link's own spelling")
	assert.Contains(t, got, filepath.Join("/mnt/workspace-disk", link), "the link's other spelling")
	assert.Contains(t, got, target, "the link's target")
	assert.Contains(t, got, filepath.Join("/mnt/workspace-disk", target), "the target's other spelling")
}

// aliasMountEnv marks the re-executed run of the test below.
const aliasMountEnv = "CREDSANDBOX_TEST_ALIAS_MOUNT"

// R17.B1: ~/.config/git/config and a credential helper, each linked to a file
// in a directory the filesystem also shows at a second mount point (as a cloud
// machine's disk is also /mnt/workspace-disk). A credentials-none command
// writes to both targets through both spellings; neither changes. The second
// mount needs a mount namespace of the test's own, so the test re-executes
// itself under unshare and does the work there.
func TestSandbox_ALinkedTargetIsReadOnlyThroughItsOtherMounts(t *testing.T) {
	testkit.Slow(t, "re-executes the test under unshare and builds real user, mount and PID namespaces")
	if os.Getenv(aliasMountEnv) != "" {
		linkedTargetThroughAnotherMount(t)
		return
	}
	if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
		t.Skipf("this host cannot make the second mount (unshare --user --mount: %v)", err)
	}
	requireSandbox(t)
	cmd := exec.Command("unshare", "--user", "--map-root-user", "--mount", "--propagation", "private",
		selfPath(t), "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), aliasMountEnv+"=1") //nolint:forbidigo // the re-executed test needs this one's environment
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "--- SKIP") {
		t.Skipf("skipped inside the second mount namespace:\n%s", out)
	}
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "--- PASS", "%s", out)
}

// linkedTargetThroughAnotherMount is the re-executed half of the test above,
// as root of a user and mount namespace of its own: it mounts the targets'
// directory a second time, links the trusted config and helper to it, and
// runs the credentials-none command.
func linkedTargetThroughAnotherMount(t *testing.T) {
	requireSandbox(t)
	// Under this checkout, not /tmp: the command's /tmp is private.
	root, err := os.MkdirTemp(".", ".credsandbox-alias-")
	require.NoError(t, err)
	root, err = filepath.Abs(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	src, second := filepath.Join(root, "srv"), filepath.Join(root, "disk")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.MkdirAll(second, 0o755))
	conf, helper := filepath.Join(src, "git.conf"), filepath.Join(src, "git-credential-custom")
	const confBody, helperBody = "[user]\n\tname = trusted\n", "#!/bin/sh\necho password=trusted\n"
	require.NoError(t, os.WriteFile(conf, []byte(confBody), 0o644))
	require.NoError(t, os.WriteFile(helper, []byte(helperBody), 0o755))
	require.NoError(t, unix.Mount(src, second, "", unix.MS_BIND, ""), "binding %s at %s", src, second)
	t.Cleanup(func() { _ = unix.Unmount(second, unix.MNT_DETACH) })

	home := wrapHome(t)
	config := filepath.Join(home, ".config", "git", "config")
	require.NoError(t, os.MkdirAll(filepath.Dir(config), 0o755))
	require.NoError(t, os.Symlink(conf, config))
	bin := filepath.Join(home, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	helperLink := filepath.Join(bin, "git-credential-custom")
	require.NoError(t, os.Symlink(helper, helperLink))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[credential]\n\thelper = "+helperLink+"\n"), 0o644))

	var sh strings.Builder
	for _, p := range []string{conf, helper, filepath.Join(second, "git.conf"), filepath.Join(second, "git-credential-custom")} {
		sh.WriteString("printf 'planted\\n' > '" + p + "' 2>/dev/null && echo \"wrote " + p + "\"\n")
	}
	sh.WriteString("echo ran\n")
	out := runWrapped(t, "none", home, home, sh.String())
	assert.Contains(t, out, "ran")
	assert.NotContains(t, out, "wrote", "a write reached a trusted target")
	for p, want := range map[string]string{conf: confBody, helper: helperBody} {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		assert.Equal(t, want, string(b), "%s changed", p)
	}
}

// R9.B1: the same helper inside a cache hand-off its run node named, which
// the sandbox keeps real and writable over the private cache root, is still
// sealed: the hand-off's real writes persist, the helper does not.
func TestSandbox_AnAbsentCredentialHelperInAHandOffCannotBePlanted(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	home := wrapHome(t)
	xdg := filepath.Join(filepath.Dir(home), "xdg")
	out := filepath.Join(xdg, "pipeline", "out")
	require.NoError(t, os.MkdirAll(out, 0o755))
	helper := filepath.Join(out, "helper")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[credential]\n\thelper = "+helper+"\n"), 0o644))

	script := `
printf '#!/bin/sh\necho password=planted\n' > '` + helper + `' 2>/dev/null && chmod +x '` + helper + `' 2>/dev/null
echo kept > '` + out + `/result'
echo ran`
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = home
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "XDG_CACHE_HOME=" + xdg}
	refusal := Wrap(cmd, "none", Writable{Paths: []string{out}})
	assert.Contains(t, refusal, helper, "a helper the command could plant in its declared output: refused, naming it")
	_, err := os.Lstat(helper)
	assert.True(t, os.IsNotExist(err), "the refused command ran: %v", err)

	// R12.B1: started in the declared output itself, which makes it the
	// command's own: refused all the same, never sealed over its output.
	cmd = exec.Command("bash", "-c", script)
	cmd.Dir = out
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "XDG_CACHE_HOME=" + xdg}
	refusal = Wrap(cmd, "none", Writable{Paths: []string{out}})
	assert.Contains(t, refusal, helper, "a helper in the declared output the command starts in: refused, naming it")
	_, err = os.Lstat(filepath.Join(out, "result"))
	assert.True(t, os.IsNotExist(err), "the refused command ran: %v", err)

	// With the helper outside the hand-off, the command runs and what it
	// writes in the hand-off is real: it persists after it exits.
	elsewhere := filepath.Join(home, "bin", "helper")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[credential]\n\thelper = "+elsewhere+"\n"), 0o644))
	b, err := wrappedWritable(t, home, home, []string{out}, strings.ReplaceAll(script, helper, elsewhere), "XDG_CACHE_HOME="+xdg).CombinedOutput()
	require.NoError(t, err, "%s", b)
	assert.Contains(t, string(b), "ran")
	got, err := os.ReadFile(filepath.Join(out, "result"))
	require.NoError(t, err, "the command's declared output did not persist")
	assert.Equal(t, "kept\n", string(got))
	_, err = os.Lstat(elsewhere)
	assert.True(t, os.IsNotExist(err), "a planted credential helper outlived the credentials-none command: %v", err)
}

// R13.B1: a credential helper named by a config file ~/.gitconfig includes,
// both outside home, PATH and the checkouts, cannot be overwritten, nor the
// included config changed, nor an absent included one created.
func TestSandbox_AnIncludedGitConfigAndItsHelperCannotBeReplaced(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces")
	requireSandbox(t)
	home := wrapHome(t)
	// Under this checkout, not /tmp: the command's /tmp is private.
	root, err := os.MkdirTemp(".", ".credsandbox-include-")
	require.NoError(t, err)
	root, err = filepath.Abs(root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	conf := filepath.Join(root, "config", "credentials.conf")
	helper := filepath.Join(root, "helpers", "auth")
	absentConf := filepath.Join(root, "config", "work.conf")
	for _, d := range []string{filepath.Dir(conf), filepath.Dir(helper)} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	const trustedHelper = "#!/bin/sh\necho trusted\n"
	confBody := "[credential]\n\thelper = " + helper + "\n[includeIf \"gitdir:~/work/\"]\n\tpath = work.conf\n"
	require.NoError(t, os.WriteFile(helper, []byte(trustedHelper), 0o755))
	require.NoError(t, os.WriteFile(conf, []byte(confBody), 0o644))
	// B1: a trailing comment, as git allows; git reads conf itself.
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[include]\n\tpath = "+conf+" # shared credentials\n"), 0o644))

	out := runWrapped(t, "none", home, home, `
printf '#!/bin/sh\necho password=planted\n' > '`+helper+`' 2>/dev/null
printf '[credential]\n\thelper = /planted\n' > '`+conf+`' 2>/dev/null
printf '[credential]\n\thelper = /planted\n' > '`+absentConf+`' 2>/dev/null
echo ran`)
	assert.Contains(t, out, "ran")
	got, err := os.ReadFile(helper)
	require.NoError(t, err)
	assert.Equal(t, trustedHelper, string(got), "the included config's helper was overwritten")
	got, err = os.ReadFile(conf)
	require.NoError(t, err)
	assert.Equal(t, confBody, string(got), "the included config was changed")
	_, err = os.Lstat(absentConf)
	assert.True(t, os.IsNotExist(err), "an absent included config outlived the credentials-none command: %v", err)
}
