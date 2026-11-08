// Copyright (c) 2026 Reliant Labs

package credsandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// machine is a cloud machine's layout in a temp directory: a home with a
// Reliant worktree, a daemon binary directory, and a projects directory with a
// single-repo project and a multi-repo root (not a checkout) holding two.
type machine struct {
	root, home, projects, bin, multi, a, b, single, wt string
}

func newMachine(t *testing.T) machine {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	m := machine{
		root:     root,
		home:     filepath.Join(root, "home"),
		projects: filepath.Join(root, "home", "projects"),
		bin:      filepath.Join(root, "daemon-bin"),
	}
	m.multi = filepath.Join(m.projects, "labs")
	m.a = filepath.Join(m.multi, "a")
	m.b = filepath.Join(m.multi, "b")
	m.single = filepath.Join(m.projects, "solo")
	m.wt = filepath.Join(m.home, ".reliant", "worktrees", "labs", "feature-1")

	mkdir := func(p string) { require.NoError(t, os.MkdirAll(p, 0o755)) }
	write := func(p, s string) {
		mkdir(filepath.Dir(p))
		require.NoError(t, os.WriteFile(p, []byte(s), 0o644))
	}
	for _, repo := range []string{m.a, m.b, m.single} {
		mkdir(filepath.Join(repo, ".git", "hooks"))
		write(filepath.Join(repo, ".git", "config"), "[core]\n")
		write(filepath.Join(repo, "README"), "shared")
	}
	mkdir(filepath.Join(m.multi, ".reliant", "workflows"))
	write(filepath.Join(m.multi, "reliant.md"), "memory")
	write(filepath.Join(m.bin, "reliant"), "#!/bin/sh\n")

	// The Reliant worktree holds a linked worktree of a.
	gitdir := filepath.Join(m.a, ".git", "worktrees", "feature-1")
	write(filepath.Join(gitdir, "commondir"), "../..\n")
	write(filepath.Join(m.wt, "a", ".git"), "gitdir: "+gitdir+"\n")
	write(filepath.Join(m.wt, "a", "README"), "mine")
	return m
}

func (m machine) plan(cwd string, env []string, optIn ...string) Plan {
	return buildPlan(Plan{}, planEnv{
		home: m.home, cwd: cwd, env: env, executable: filepath.Join(m.bin, "reliant"),
		projectsDir: m.projects, daemonBinDir: m.bin, optIn: optIn,
	})
}

func TestPlan_SharedCheckoutsAreReadOnlyFromTheProjectRoot(t *testing.T) {
	m := newMachine(t)
	p := m.plan(m.multi, nil)

	for _, want := range []string{m.a, m.b, m.single, m.bin, filepath.Join(m.bin, "reliant"),
		filepath.Join(m.multi, ".reliant"), filepath.Join(m.multi, "reliant.md"),
		filepath.Join(m.home, ".gitconfig"), filepath.Join(m.home, ".config", "git"),
		filepath.Join(m.home, ".reliant", "mcp.json")} {
		assert.Contains(t, p.ReadOnly, want)
	}
	assert.NotContains(t, p.ReadOnly, m.multi, "a project root that is not a checkout stays the command's to write")
	assert.Empty(t, p.Writable)
	assert.Contains(t, p.Seal, filepath.Join(m.home, ".gitconfig"))
}

func TestPlan_TheCheckoutHoldingTheWorkingDirectoryIsTheCommands(t *testing.T) {
	m := newMachine(t)
	p := m.plan(filepath.Join(m.a, "sub", "dir"), nil)

	assert.NotContains(t, p.ReadOnly, m.a)
	assert.Contains(t, p.ReadOnly, m.b)
	assert.Contains(t, p.ReadOnly, m.single)
}

func TestPlan_AWorktreeWritesItsSharedGitDirectoryButNotItsConfigOrHooks(t *testing.T) {
	m := newMachine(t)
	p := m.plan(filepath.Join(m.wt, "a"), nil)

	assert.Contains(t, p.ReadOnly, m.a, "the main checkout behind the worktree is shared")
	assert.Contains(t, p.Writable, filepath.Join(m.a, ".git"), "git in the worktree writes objects and refs there")
	assert.Contains(t, p.ReadOnly, filepath.Join(m.a, ".git", "config"))
	assert.Contains(t, p.ReadOnly, filepath.Join(m.a, ".git", "hooks"))
	for _, ro := range p.ReadOnly {
		assert.False(t, under(ro, m.wt), "nothing of the command's own worktree is read-only: %s", ro)
	}
}

func TestPlan_OptInUnfreezesOnlySharedCheckouts(t *testing.T) {
	m := newMachine(t)

	p := m.plan(m.multi, nil, m.b)
	assert.NotContains(t, p.ReadOnly, m.b, "an opted-in checkout is the command's own")
	assert.Contains(t, p.ReadOnly, m.a)

	p = m.plan(m.multi, nil, filepath.Join(m.a, "gen"))
	assert.Contains(t, p.ReadOnly, m.a)
	assert.Contains(t, p.Writable, filepath.Join(m.a, "gen"), "a directory inside a shared checkout is a carve-out")

	p = m.plan(m.multi, nil, m.bin, filepath.Join(m.home, ".gitconfig"))
	assert.Contains(t, p.ReadOnly, m.bin, "an opt-in cannot unfreeze the daemon's binary")
	assert.Contains(t, p.ReadOnly, filepath.Join(m.home, ".gitconfig"))
	assert.Empty(t, p.Writable)
	assert.Equal(t, []string{m.bin, filepath.Join(m.home, ".gitconfig")}, p.Rejected, "and the command is refused, naming them")
}

func TestPlan_CredentialHelpersAreReadOnly(t *testing.T) {
	m := newMachine(t)
	require.NoError(t, os.WriteFile(filepath.Join(m.home, ".gitconfig"), []byte(
		"[credential \"https://github.com\"]\n\thelper = !'/opt/helper/reliant' 'auth' 'git-credential'\n"+
			"[credential]\n\thelper = store\n\thelper = \"!/usr/local/bin/gh auth git-credential\"\n"), 0o644))
	p := m.plan(m.multi, []string{`FORGE_CREDENTIAL_HELPER=["/opt/forge-helper","auth"]`})

	assert.Contains(t, p.ReadOnly, "/opt/helper/reliant")
	assert.Contains(t, p.ReadOnly, "/usr/local/bin/gh")
	assert.Contains(t, p.ReadOnly, "/opt/forge-helper")
}

// R2.B9: a helper that is not a path is the program a credentialed git finds
// on PATH: a `!` helper's first word, any other helper as git-credential-<name>.
// Every such candidate on the command's PATH is read-only where it exists, and
// cannot be created where it does not, unless that directory is private to the
// command (then nothing it makes there outlives it).
func TestPlan_CredentialHelpersFoundOnPathAreProtected(t *testing.T) {
	m := newMachine(t)
	tools := filepath.Join(m.a, ".tools", "bin") // the command's own: not overlaid
	gobin := filepath.Join(m.home, "go", "bin")  // private to the command
	for _, d := range []string{tools, gobin} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(tools, "gh"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(m.home, ".gitconfig"), []byte(
		"[credential]\n\thelper = store\n\thelper = \"!gh auth git-credential\"\n\thelper = ./relative-helper\n"), 0o644))
	env := []string{"HOME=" + m.home, "PATH=" + tools + ":" + gobin + ":/usr/bin:/bin"}

	p := m.plan(m.a, env)
	assert.Contains(t, p.ReadOnly, filepath.Join(tools, "gh"), "a `!` helper found on PATH, in the command's own tools")
	assert.Contains(t, p.Seal, filepath.Join(tools, "git-credential-store"), "git-credential-<name> cannot be planted there")
	assert.Contains(t, p.Seal, filepath.Join(m.home, ".bashrc"), "(the home's own seals are unchanged)")
	assert.NotContains(t, p.Seal, filepath.Join(gobin, "git-credential-store"), "a private PATH directory needs no seal")
	assert.NotContains(t, p.Seal, "/usr/bin/git-credential-store", "nor the system's")
	var overlaid []string
	for _, o := range p.Overlay {
		overlaid = append(overlaid, o.Path)
	}
	assert.Contains(t, overlaid, gobin)
}

func TestPlan_CachesAndPathDirectoriesArePrivate(t *testing.T) {
	m := newMachine(t)
	gobin := filepath.Join(m.home, "go", "bin")
	env := []string{
		"GOCACHE=/var/cache/w/go-build", "GOMODCACHE=/var/cache/w/go-mod", "XDG_CACHE_HOME=/var/cache/w/xdg",
		"PATH=" + filepath.Join(m.a, ".tools", "bin") + ":" + gobin + ":" + m.bin + ":/usr/local/bin:/usr/bin:/bin",
	}

	p := m.plan(m.a, env)
	byPath := map[string]Overlay{}
	for _, o := range p.Overlay {
		byPath[o.Path] = o
	}
	assert.Equal(t, "GOCACHE", byPath["/var/cache/w/go-build"].Env)
	assert.Equal(t, "GOMODCACHE", byPath["/var/cache/w/go-mod"].Env)
	assert.True(t, byPath["/var/cache/w/xdg/reliant/forge-pinned"].Cache, "the pinned forge a later `reliant forge` runs")
	assert.Contains(t, byPath, "/usr/local/bin")
	assert.True(t, byPath[gobin].Sealed, "a PATH directory under home that is missing is sealed")
	assert.NotContains(t, byPath, "/usr/bin", "the system's own")
	assert.NotContains(t, byPath, filepath.Join(m.a, ".tools", "bin"), "the command's own checkout's tools")
	assert.NotContains(t, byPath, m.bin, "read-only already")
}

func TestPlan_GoDefaultsFollowHomeAndGoEnv(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".config", "go"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".config", "go", "env"), []byte("GOMODCACHE=/srv/mod\n"), 0o644))

	got := map[string]string{}
	for _, o := range cacheOverlays(home, nil) {
		if o.Env != "" {
			got[o.Env] = o.Path
		}
	}
	assert.Equal(t, filepath.Join(home, ".cache", "go-build"), got["GOCACHE"])
	assert.Equal(t, "/srv/mod", got["GOMODCACHE"], "go env -w")
	assert.Equal(t, filepath.Join(home, ".cache", "golangci-lint"), got["GOLANGCI_LINT_CACHE"])
}

// The same rule for a run node's writable_checkouts and a workflow's binding
// of an agent shell's (writableFor), but the agent's may name only hand-offs:
// its commands are a model's.
func TestPlan_ABoundAgentShellMayNameOnlyHandOffs(t *testing.T) {
	m := newMachine(t)
	xdg := filepath.Join(m.root, "xdg")
	clone := filepath.Join(xdg, "pr-fix", "a-12-run", "a")
	env := []string{"XDG_CACHE_HOME=" + xdg, "HOME=" + m.home}
	agent := func(optIn ...string) Plan {
		return buildPlan(Plan{}, planEnv{
			home: m.home, cwd: m.multi, env: env, executable: filepath.Join(m.bin, "reliant"),
			projectsDir: m.projects, daemonBinDir: m.bin, optIn: optIn, handOffsOnly: true,
		})
	}

	p := agent(clone)
	assert.Empty(t, p.Rejected)
	assert.Contains(t, p.Passthrough, clone, "its scratch clone is written for real")

	for _, bad := range []string{m.a, filepath.Join(m.a, "gen"), m.bin, filepath.Join(m.bin, "reliant"),
		filepath.Join(m.home, ".gitconfig"), filepath.Join(xdg, "go-build")} {
		p = agent(bad)
		assert.Equal(t, []string{bad}, p.Rejected, "%s", bad)
		if !under(bad, xdg) {
			assert.Contains(t, p.ReadOnly, firstReadOnlyAbove(p.ReadOnly, bad), "%s stays read-only", bad)
		}
	}

	// A run node may name the shared checkout; the agent's binding may not.
	runNode := m.plan(m.multi, env, m.a)
	assert.Empty(t, runNode.Rejected)
	assert.NotContains(t, runNode.ReadOnly, m.a)
}

// firstReadOnlyAbove is the read-only path at or above p, or p.
func firstReadOnlyAbove(ro []string, p string) string {
	for _, r := range ro {
		if under(p, r) {
			return r
		}
	}
	return p
}

func TestPlan_TheDaemonBinaryDirectoryIsReadOnly(t *testing.T) {
	m := newMachine(t)
	p := buildPlan(Plan{}, planEnv{home: m.home, cwd: m.multi, projectsDir: m.projects, daemonBinDir: "/daemon-bin"})
	assert.Contains(t, p.ReadOnly, "/daemon-bin", "the directory and everything in it")
	assert.Equal(t, "/daemon-bin", daemonBinDir, "a cloud machine's daemon binary directory")
}

// The cache roots hold the hand-offs between a workflow's steps: private to a
// credentials-none command, but for what stays real in them.
func TestPlan_CacheRootsArePrivateButForWhatTheCommandOwns(t *testing.T) {
	m := newMachine(t)
	xdg := filepath.Join(m.root, "xdg")
	gate := filepath.Join(xdg, "cp-merge", "gate")
	env := []string{"XDG_CACHE_HOME=" + xdg, "HOME=" + m.home, "GOCACHE=" + filepath.Join(xdg, "go-build")}
	handOff := filepath.Join(xdg, "pr-regen", "stage-1")

	p := m.plan(gate, env, handOff, filepath.Join(xdg, "go-build", "x"), xdg)
	roots := map[string]Overlay{}
	for _, o := range p.Overlay {
		roots[o.Path] = o
	}
	assert.True(t, roots[xdg].Notice, "XDG_CACHE_HOME")
	assert.True(t, roots[filepath.Join(m.home, ".cache")].Notice, "~/.cache")
	assert.Contains(t, p.Passthrough, gate, "its working directory stays real")
	assert.Contains(t, p.Passthrough, handOff, "a hand-off its run node named stays real")
	assert.Contains(t, p.Passthrough, filepath.Join(xdg, "forge", "slots"), "the heavy-slot locks every command queues on")
	assert.NotContains(t, p.Passthrough, filepath.Join(xdg, "go-build", "x"), "an opt-in cannot make a tool cache real")
	assert.NotContains(t, p.Passthrough, xdg, "nor a whole cache root")

	// Started in its home, a command still does not own ~/.cache.
	p = m.plan(m.home, []string{"HOME=" + m.home})
	var paths []string
	for _, o := range p.Overlay {
		paths = append(paths, o.Path)
	}
	assert.Contains(t, paths, filepath.Join(m.home, ".cache"))
}

func TestDefaultPlan_ForgesConfigIsReadOnlyAndItsCredentialsHidden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("FORGE_HOME", "")
	p := DefaultPlan()
	forge := filepath.Join(home, ".config", "forge")
	assert.Contains(t, p.ReadOnly, forge, "storage.json decides what a later forge deletes")
	assert.Contains(t, p.Seal, forge)
	assert.Contains(t, p.Hide, filepath.Join(forge, "credentials.json"))
	assert.Contains(t, p.Hide, filepath.Join(home, ".reliant", "forge-token-cache.json"), "the token cache is hidden, not read-only")
}

// R6.B2: Go's env file and git's config are protected where the go command
// and git find them, the same path goEnvFile reads.
func TestPlan_GoAndGitConfigUnderXDGConfigHomeAreProtected(t *testing.T) {
	m := newMachine(t)
	xdg := filepath.Join(m.root, "elsewhere", "config")
	p := m.plan(m.multi, []string{"XDG_CONFIG_HOME=" + xdg})
	for _, want := range []string{filepath.Join(xdg, "go", "env"), filepath.Join(xdg, "git")} {
		assert.Contains(t, p.ReadOnly, want)
		assert.Contains(t, p.Seal, want)
	}
	assert.Equal(t, filepath.Join(xdg, "go", "env"), goEnvPath(m.home, []string{"XDG_CONFIG_HOME=" + xdg}))
	assert.Equal(t, "/x/env", goEnvPath(m.home, []string{"XDG_CONFIG_HOME=" + xdg, "GOENV=/x/env"}), "GOENV wins")
	assert.Equal(t, "", goEnvPath(m.home, []string{"GOENV=off"}))
	assert.Equal(t, filepath.Join(m.home, ".config", "go", "env"), goEnvPath(m.home, nil))
}

func TestFirstWord(t *testing.T) {
	assert.Equal(t, "/daemon-bin/reliant", firstWord(`'/daemon-bin/reliant' 'auth'`))
	assert.Equal(t, "/usr/bin/gh", firstWord(`/usr/bin/gh auth git-credential`))
	assert.Equal(t, "store", firstWord("store"))
	assert.Equal(t, "", firstWord(`'unterminated`))
}

// R9.B1: an absent absolute credential helper inside a cache root is sealed
// when a hand-off its run node named makes that directory real, and when it
// is in a carve-out of a read-only checkout; a private one needs no seal.
func TestPlan_AnAbsentHelperInAHandOffIsSealed(t *testing.T) {
	m := newMachine(t)
	xdg := filepath.Join(m.root, "xdg")
	out := filepath.Join(xdg, "pipeline", "out")
	private := filepath.Join(xdg, "pipeline", "private", "helper")
	inHandOff := filepath.Join(out, "helper")
	inCarveOut := filepath.Join(m.a, "tools", "helper")
	require.NoError(t, os.MkdirAll(m.home, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(m.home, ".gitconfig"), []byte(
		"[credential]\n\thelper = "+inHandOff+"\n\thelper = "+private+"\n\thelper = "+inCarveOut+"\n"), 0o644))
	env := []string{"XDG_CACHE_HOME=" + xdg, "HOME=" + m.home}

	p := m.plan(m.multi, env, out, filepath.Join(m.a, "tools"))
	assert.Contains(t, p.Passthrough, out)
	assert.Contains(t, p.Seal, inHandOff, "the hand-off is real and writable: the helper must not be plantable there")
	assert.Contains(t, p.Seal, inCarveOut, "a writable carve-out of a read-only checkout is real too")
	assert.NotContains(t, p.Seal, private, "a private directory's writes do not outlive the command")
}
