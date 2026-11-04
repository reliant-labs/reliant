// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	req "github.com/stretchr/testify/require"
)

func paths(cs []Credential) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Path
	}
	return out
}

func TestProtectedCredentials_NamesTheStoresTheReviewNamed(t *testing.T) {
	cs := protectedCredentials("/home/u", "/home/u/.config/forge/credentials.json", "/home/u/.reliant-data")
	got := paths(cs)

	for _, want := range []string{
		"/home/u/.reliant/daemon.json",
		"/home/u/.reliant/forge-token-cache.json",
		"/home/u/.config/forge/credentials.json",
		"/home/u/.config/forge/credentials.json.tmp",
		"/home/u/.config/forge/credentials.json.lock",
		"/home/u/.git-credentials",
		"/home/u/.ssh",
		"/home/u/.config/gh",
		"/home/u/.kube/config",
		"/home/u/.aws",
		"/home/u/.docker/config.json",
		"/home/u/.config/configstore",
		"/home/u/.reliant-data",
	} {
		assert.Contains(t, got, want)
	}
	for _, c := range cs {
		assert.True(t, filepath.IsAbs(c.Path), c.Path)
		assert.Equal(t, filepath.Clean(c.Path), c.Path)
	}
	assert.NotContains(t, got, "/home/u/.config/forge", "the forge directory also holds files that are not credentials")
	assert.NotContains(t, got, "/home/u/.reliant", "nor does ~/.reliant: it holds worktrees and releases")
}

func TestProtectedCredentials_HonoursFORGE_HOME(t *testing.T) {
	cs := protectedCredentials("/home/u", "/srv/forge/credentials.json", "")
	assert.Contains(t, paths(cs), "/srv/forge/credentials.json")
}

func TestProtectedCredentials_DropsEmptyAndRelative(t *testing.T) {
	assert.Empty(t, protectedCredentials("", "", ""))
	assert.Empty(t, protectedCredentials("", "relative/credentials.json", "also/relative"))
}

func TestIsReliantCredentialFile_ResolvesLinksAndIdentity(t *testing.T) {
	home := t.TempDir()
	cred := filepath.Join(home, ".reliant", "daemon.json")
	req.NoError(t, os.MkdirAll(filepath.Dir(cred), 0o755))
	req.NoError(t, os.WriteFile(cred, []byte("PAT"), 0o600))
	protected := []string{cred}

	assert.True(t, isCredentialFile(cred, protected), "the file")
	assert.True(t, isCredentialFile(filepath.Join(home, ".reliant", ".", "daemon.json"), protected), "an unclean spelling")
	assert.True(t, isCredentialFile(filepath.Join(home, "x", "..", ".reliant", "daemon.json"), protected), "a dot-dot spelling")

	link := filepath.Join(home, "innocent.txt")
	req.NoError(t, os.Symlink(cred, link))
	assert.True(t, isCredentialFile(link, protected), "a symlink to it")

	dirLink := filepath.Join(home, "dot-reliant")
	req.NoError(t, os.Symlink(filepath.Dir(cred), dirLink))
	assert.True(t, isCredentialFile(filepath.Join(dirLink, "daemon.json"), protected), "through a symlinked parent")

	hard := filepath.Join(home, "hard.txt")
	req.NoError(t, os.Link(cred, hard))
	assert.True(t, isCredentialFile(hard, protected), "a hard link to it: the same file under another name")

	other := filepath.Join(home, ".reliant", "reliant.md")
	req.NoError(t, os.WriteFile(other, []byte("memory"), 0o600))
	assert.False(t, isCredentialFile(other, protected), "a neighbour")
	assert.False(t, isCredentialFile(filepath.Join(home, "missing"), protected))
	assert.False(t, isCredentialFile("", protected))
}

func TestIsReliantCredentialFile_ACreateIsJudgedByItsParent(t *testing.T) {
	home := t.TempDir()
	cred := filepath.Join(home, ".reliant", "daemon.json")
	req.NoError(t, os.MkdirAll(filepath.Dir(cred), 0o755))
	link := filepath.Join(home, "alias")
	req.NoError(t, os.Symlink(filepath.Dir(cred), link))

	assert.True(t, isCredentialFile(filepath.Join(link, "daemon.json"), []string{cred}), "a write that would create it through a link")
}

func TestDenyCredentialFile_IsThePolicyEveryResolveGoesThrough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FORGE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	cred := filepath.Join(home, ".reliant", "daemon.json")
	req.NoError(t, os.MkdirAll(filepath.Dir(cred), 0o755))
	req.NoError(t, os.WriteFile(cred, []byte("PAT"), 0o600))

	err := DenyCredentialFile(cred)
	req.Error(t, err)
	assert.True(t, errors.Is(err, ErrCredentialFile))
	assert.NoError(t, DenyCredentialFile(filepath.Join(home, "project", "a.go")))

	_, err = ResolveFile(context.Background(), cred)
	assert.ErrorIs(t, err, ErrCredentialFile, "an unconfined caller is refused too")
	_, err = ResolveDir(context.Background(), cred)
	assert.ErrorIs(t, err, ErrCredentialFile)

	ok := filepath.Join(home, "project")
	req.NoError(t, os.MkdirAll(ok, 0o755))
	got, err := ResolveDir(context.Background(), ok)
	req.NoError(t, err)
	assert.Equal(t, ok, got)
}
