// Copyright (c) 2025 Reliant Labs
package repo

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/require"
)

// remote.origin.url read from config text the way git reads it.
func TestRemoteURLFromConfig(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ config, want string }{
		"plain": {
			"[core]\n\tbare = false\n[remote \"origin\"]\n\turl = git@github.com:acme/app.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n",
			"git@github.com:acme/app.git"},
		"no origin": {"[remote \"upstream\"]\n\turl = https://example.com/u.git\n", ""},
		"another remote's url is not origin's": {
			"[remote \"origin\"]\n\turl = https://example.com/o.git\n[remote \"upstream\"]\n\turl = https://example.com/u.git\n",
			"https://example.com/o.git"},
		"the last url wins":                    {"[remote \"origin\"]\n\turl = https://a/1.git\n\turl = https://a/2.git\n", "https://a/2.git"},
		"key and section are case-insensitive": {"[REMOTE \"origin\"]\n\tURL = https://a/x.git\n", "https://a/x.git"},
		"the subsection is case-sensitive":     {"[remote \"Origin\"]\n\turl = https://a/x.git\n", ""},
		"legacy dotted section":                {"[remote.origin]\n\turl = https://a/legacy.git\n", "https://a/legacy.git"},
		"trailing comment":                     {"[remote \"origin\"]\n\turl = https://a/x.git # mirror\n", "https://a/x.git"},
		"quoted, with a hash inside":           {"[remote \"origin\"]\n\turl = \"https://a/x.git#frag\" ; note\n", "https://a/x.git#frag"},
		"escaped quote and backslash":          {"[remote \"origin\"]\n\turl = /srv/a\\\\b\\\"c\n", "/srv/a\\b\"c"},
		"continued line":                       {"[remote \"origin\"]\n\turl = https://a/\\\nx.git\n", "https://a/x.git"},
		"no spaces around =":                   {"[remote \"origin\"]\nurl=https://a/x.git\n", "https://a/x.git"},
		"CRLF":                                 {"[remote \"origin\"]\r\n\turl = https://a/x.git\r\n", "https://a/x.git"},
		"commented-out section line":           {"# [remote \"origin\"]\n\turl = https://a/x.git\n", ""},
		"key on the header line":               {"[remote \"origin\"] url = https://a/x.git\n", "https://a/x.git"},
		"no trailing newline":                  {"[remote \"origin\"]\n\turl = https://a/x.git", "https://a/x.git"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := remoteURLFromConfig(bufio.NewReader(strings.NewReader(tc.config)), "origin")
			require.Equal(t, tc.want, got)
		})
	}
}

// git itself is the oracle: on real repositories, a main checkout, a linked
// worktree of it and a submodule-style relative gitdir, Discover's remotes are
// exactly `git config --get remote.origin.url` (credentials stripped).
func TestDiscoverRemotesMatchGit(t *testing.T) {
	testkit.Slow(t, "runs git to build real repositories and to read their remotes")
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	root := t.TempDir()
	remotes := map[string]string{
		"api":  "git@github.com:acme/api.git",
		"web":  `https://example.com/acme/web "quoted".git`,
		"docs": "https://user:secret@example.com/acme/docs.git",
	}
	for name, url := range remotes {
		dir := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		run(dir, "init", "-q")
		run(dir, "remote", "add", "origin", url)
	}
	// A linked worktree of api, outside the scanned tree's api checkout.
	run(filepath.Join(root, "api"), "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "x")
	run(filepath.Join(root, "api"), "worktree", "add", "-q", filepath.Join(root, "wt", "api-wt"))
	// No remote at all.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "local"), 0o755))
	run(filepath.Join(root, "local"), "init", "-q")

	d, err := Discover(context.Background(), root, Options{})
	require.NoError(t, err)
	require.False(t, d.Truncated)
	got := map[string]string{}
	for _, f := range d.Repos {
		got[f.RelativePath] = f.RemoteURL
	}
	want := map[string]string{}
	for _, rel := range []string{"api", "web", "docs", "local"} {
		cmd := exec.Command("git", "config", "--get", "remote.origin.url")
		cmd.Dir = filepath.Join(root, rel)
		out, _ := cmd.Output() // exits 1 when there is no remote
		want[rel] = strings.TrimSpace(string(out))
	}
	want["docs"] = "https://example.com/acme/docs.git" // credentials are stripped
	require.Equal(t, want, got, "the linked worktree collapses into api")

	// A linked worktree scanned on its own reads its main checkout's remote.
	wt, err := Discover(context.Background(), filepath.Join(root, "wt", "api-wt"), Options{})
	require.NoError(t, err)
	require.Len(t, wt.Repos, 1)
	require.Equal(t, remotes["api"], wt.Repos[0].RemoteURL)
}
