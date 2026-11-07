// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reliant bug e433fa60: a credentials-none command could write what a later,
// credentialed one executes or reads. These run a real sandbox (user, mount and
// PID namespaces, overlays) over a cloud machine's layout and try every write.

// probeScript tries to write each path it is given and prints one line per
// path: WROTE, or DENIED and the shell's error.
const probeScript = `
w() { # w <kind> <path>
  case "$1" in
    file) out=$( { printf 'planted\n' >> "$2"; } 2>&1 ) ;;
    new)  out=$( { printf 'planted\n' > "$2"; } 2>&1 ) ;;
    dir)  out=$( touch "$2/planted" 2>&1 ) ;;
    mv)   printf 'planted\n' > "$TMPDIR/swap.$$" 2>/dev/null; out=$( mv -f "$TMPDIR/swap.$$" "$2" 2>&1 ) ;;
  esac
  if [ $? -eq 0 ]; then echo "$1 WROTE $2"; else echo "$1 DENIED $2: $out"; fi
}
`

type probe struct{ kind, path string }

func script(probes []probe) string {
	var b strings.Builder
	b.WriteString(probeScript)
	for _, p := range probes {
		b.WriteString("w " + p.kind + " '" + p.path + "'\n")
	}
	return b.String()
}

// deniedByTheKernel is EROFS or EPERM/EACCES as the shell words them, never a
// missing directory or a typo in the test.
var deniedByTheKernel = regexp.MustCompile(`Read-only file system|Operation not permitted|Permission denied|Device or resource busy`)

func runIn(t *testing.T, plan *Plan, dir string, env []string, sh string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", sh)
	cmd.Dir = dir
	cmd.Env = env
	if plan != nil {
		require.Empty(t, Apply(cmd, selfPath(t), *plan))
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return string(out)
}

func TestSandbox_ALaterCredentialedRunsInputsAreReadOnly(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces and overlays for each command")
	requireSandbox(t)
	m := newMachine(t)
	gocache := filepath.Join(m.root, "cache", "go-build")
	gobin := filepath.Join(m.home, "go", "bin")
	for _, d := range []string{gocache, gobin, filepath.Join(m.home, ".config", "git"), filepath.Join(m.home, ".config", "gh")} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(m.home, ".config", "gh", "hosts.yml"), []byte("oauth_token: PAT-gh"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(m.home, ".gitconfig"), []byte("[user]\n\tname = x\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(gocache, "ab-d"), []byte("trusted"), 0o644))
	tmp := t.TempDir()
	env := []string{"PATH=" + gobin + ":/usr/bin:/bin", "HOME=" + m.home, "TMPDIR=" + tmp, "GOCACHE=" + gocache}

	plan := buildPlan(Plan{Hide: []string{filepath.Join(m.home, ".config", "gh")}}, planEnv{
		home: m.home, cwd: m.multi, env: env, executable: filepath.Join(m.bin, "reliant"),
		projectsDir: m.projects, daemonBinDir: m.bin,
	})
	protected := []probe{
		{"file", filepath.Join(m.bin, "reliant")},
		{"mv", filepath.Join(m.bin, "reliant")},
		{"dir", m.bin},
		{"file", filepath.Join(m.home, ".gitconfig")},
		{"mv", filepath.Join(m.home, ".gitconfig")},
		{"dir", filepath.Join(m.home, ".config", "git")},
		{"file", filepath.Join(m.a, "README")},
		{"dir", m.a},
		{"new", filepath.Join(m.a, ".git", "hooks", "pre-commit")},
		{"file", filepath.Join(m.a, ".git", "config")},
		{"dir", filepath.Join(m.b, ".git")},
		{"dir", filepath.Join(m.single, ".git", "hooks")},
		{"dir", filepath.Join(m.multi, ".reliant", "workflows")},
		{"file", filepath.Join(m.multi, "reliant.md")},
	}
	out := runIn(t, &plan, m.multi, env, script(protected)+`
cat "$HOME/.config/gh/hosts.yml" 2>/dev/null
echo own > "`+filepath.Join(m.multi, "notes.txt")+`" && echo OWN-CWD-OK
t=$(mktemp -d) && echo x > "$t/f" && echo OWN-TMP-OK
echo poisoned > "$GOCACHE/ab-d" && cat "$GOCACHE/ab-d" && echo new > "$GOCACHE/cd-d" && echo CACHE-PRIVATE-WRITE-OK
echo evil > "`+gobin+`/git" && echo PATH-PRIVATE-WRITE-OK
echo '{"mcpServers":{}}' > "$HOME/.reliant/mcp.json" && echo SEALED-IN-RUN
`)
	for _, p := range protected {
		line := lineFor(out, p.path, p.kind)
		assert.True(t, strings.HasPrefix(line, p.kind+" DENIED "+p.path+":"), "%s %s must be refused:\n%s", p.kind, p.path, out)
		assert.Regexp(t, deniedByTheKernel, line)
	}
	assert.NotContains(t, out, "PAT-gh", "gh's config stays hidden")
	for _, ok := range []string{"OWN-CWD-OK", "OWN-TMP-OK", "CACHE-PRIVATE-WRITE-OK", "PATH-PRIVATE-WRITE-OK", "SEALED-IN-RUN"} {
		assert.Contains(t, out, ok)
	}
	assert.Contains(t, out, "poisoned", "the command sees its own write to the cache")

	// Nothing it wrote to the caches or PATH outlived it.
	got, err := os.ReadFile(filepath.Join(gocache, "ab-d"))
	require.NoError(t, err)
	assert.Equal(t, "trusted", string(got), "the shared build cache is untouched")
	assert.NoFileExists(t, filepath.Join(gocache, "cd-d"))
	assert.NoFileExists(t, filepath.Join(gobin, "git"))
	assert.NoFileExists(t, filepath.Join(m.home, ".reliant", "mcp.json"), "an absent trusted file is sealed")
	assert.FileExists(t, filepath.Join(m.multi, "notes.txt"), "its working directory's writes are real")
	got, err = os.ReadFile(filepath.Join(m.bin, "reliant"))
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\n", string(got))

	// The same writes with credentials: every one lands.
	out = runIn(t, nil, m.multi, env, script(protected))
	for _, p := range protected {
		assert.Equal(t, p.kind+" WROTE "+p.path, lineFor(out, p.path, p.kind), "a credentialed command writes %s:\n%s", p.path, out)
	}
}

func TestSandbox_AnAbsentTrustedFileCannotBeCreated(t *testing.T) {
	testkit.Slow(t, "builds real user, mount and PID namespaces for each command")
	requireSandbox(t)
	m := newMachine(t)
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + m.home}
	plan := buildPlan(Plan{}, planEnv{home: m.home, cwd: m.multi, env: env, projectsDir: m.projects})

	out := runIn(t, &plan, m.multi, env, `
echo '[core] hooksPath = /tmp/evil' > "$HOME/.gitconfig" && echo CREATED-IN-RUN
mkdir -p "$HOME/.config/go" && echo 'GOFLAGS=-toolexec=/tmp/evil' > "$HOME/.config/go/env" && echo GOENV-IN-RUN
`)
	assert.Contains(t, out, "CREATED-IN-RUN", "the command may create it for itself")
	assert.NoFileExists(t, filepath.Join(m.home, ".gitconfig"), "but not for a later run")
	assert.NoFileExists(t, filepath.Join(m.home, ".config", "go", "env"))
}

func TestSandbox_AWorktreeCommitsThroughItsReadOnlyMainCheckout(t *testing.T) {
	testkit.Slow(t, "runs git in real namespaces")
	requireSandbox(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	m := newMachine(t)
	main := filepath.Join(m.projects, "repo")
	wt := filepath.Join(m.home, ".reliant", "worktrees", "repo", "feature-2")
	gitEnv := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + m.home, "GIT_CONFIG_NOSYSTEM=1", //nolint:forbidigo // the test's own PATH for git
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}
	runIn(t, nil, m.root, gitEnv, `set -e
git init -q -b main "`+main+`" && cd "`+main+`" && echo a > a && git add a && git commit -qm a
git worktree add -q -b feature "`+wt+`"`)

	plan := buildPlan(Plan{}, planEnv{home: m.home, cwd: wt, env: gitEnv, projectsDir: m.projects})
	out := runIn(t, &plan, wt, gitEnv, `
echo b > b && git add b && git commit -qm b && echo COMMIT-OK
echo evil > "`+main+`/a" 2>&1 || echo MAIN-TREE-DENIED
git config core.hooksPath /tmp/evil 2>&1 || echo CONFIG-DENIED
echo 'exit 1' > "`+main+`/.git/hooks/pre-commit" 2>&1 || echo HOOKS-DENIED
`)
	for _, want := range []string{"COMMIT-OK", "MAIN-TREE-DENIED", "CONFIG-DENIED", "HOOKS-DENIED"} {
		assert.Contains(t, out, want, out)
	}
	log, err := exec.Command("git", "-C", main, "log", "--format=%s", "feature").Output()
	require.NoError(t, err)
	assert.Equal(t, "b\na\n", string(log), "the commit is in the shared repository")
}

func TestSandbox_WithoutOverlaysCachesAreReadOnlyAndPrivate(t *testing.T) {
	testkit.Slow(t, "builds real namespaces")
	requireSandbox(t)
	cache := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cache, "entry"), []byte("trusted"), 0o644))
	saved := overlayResult
	overlayResult = errors.New("refused by the test")
	t.Cleanup(func() { overlayResult = saved })
	plan := Plan{Overlay: []Overlay{{Path: cache, Env: "GOCACHE", Cache: true}}}

	out := runIn(t, &plan, t.TempDir(), []string{"PATH=/usr/bin:/bin", "GOCACHE=" + cache},
		`echo "GOCACHE=$GOCACHE"; echo x > "`+cache+`/entry" 2>&1 || echo SHARED-DENIED; echo y > "$GOCACHE/new" && echo PRIVATE-OK`)
	assert.Contains(t, out, "GOCACHE=/tmp/.credsandbox-gocache", "a private, empty cache in the command's own /tmp")
	assert.Contains(t, out, "SHARED-DENIED")
	assert.Contains(t, out, "PRIVATE-OK")
	got, err := os.ReadFile(filepath.Join(cache, "entry"))
	require.NoError(t, err)
	assert.Equal(t, "trusted", string(got))
}

// lineFor is the probe's line for one kind of write to path.
func lineFor(out, path, kind string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, kind+" WROTE "+path) || strings.HasPrefix(l, kind+" DENIED "+path+":") {
			return l
		}
	}
	return "MISSING " + kind + " " + path
}
