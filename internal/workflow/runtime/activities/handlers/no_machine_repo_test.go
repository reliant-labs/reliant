// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestGitHubRepoFromRemote(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/acme/widgets.git":                       "acme/widgets",
		"https://github.com/acme/widgets":                           "acme/widgets",
		"https://github.com/acme/widgets/":                          "acme/widgets",
		"http://www.github.com/Acme/My.Repo.git":                    "Acme/My.Repo",
		"https://x-access-token:ghs_secret@github.com/acme/app.git": "acme/app",
		"git@github.com:acme/widgets.git":                           "acme/widgets",
		"git@github.com:acme/.github":                               "acme/.github",
		"ssh://git@github.com/acme/widgets.git":                     "acme/widgets",
		"ssh://git@github.com:22/acme/widgets":                      "acme/widgets",
		"git://github.com/acme/widgets.git":                         "acme/widgets",
		"  https://github.com/acme/widgets.git\n":                   "acme/widgets",
	} {
		owner, name, ok := githubRepoFromRemote(remote)
		if assert.True(t, ok, remote) {
			assert.Equal(t, want, owner+"/"+name, remote)
		}
	}
	for _, remote := range []string{
		"", "/home/me/src/app", "file:///srv/git/app.git",
		"https://gitlab.com/acme/widgets.git",
		"https://github.example.com/acme/widgets.git", // Enterprise: the integration does not reach it
		"git@bitbucket.org:acme/widgets.git",
		"https://github.com/acme", "https://github.com/acme/widgets/tree/main",
		"https://github.com/../widgets", "https://github.com/acme/..",
	} {
		_, _, ok := githubRepoFromRemote(remote)
		assert.False(t, ok, remote)
	}
}

// The project's own remote comes first; nested repos follow with where they
// sit, and a remote listed twice (the root repo's row) appears once.
func TestProjectGitHubRepos(t *testing.T) {
	root := "git@github.com:acme/platform.git"
	forge := "https://github.com/acme/forge.git"
	local := "/srv/git/scratch"
	got := projectGitHubRepos(&db.Project{RemoteURL: &root}, []*core.Repo{
		{RelativePath: "", RemoteURL: &root},
		{RelativePath: "forge", RemoteURL: &forge},
		{RelativePath: "scratch", RemoteURL: &local},
		{RelativePath: "none"},
	})
	assert.Equal(t, []githubRepo{{Owner: "acme", Name: "platform"}, {Owner: "acme", Name: "forge", Dir: "forge"}}, got)
	assert.Empty(t, projectGitHubRepos(&db.Project{}, nil))
}

func TestNoMachineRepoNote(t *testing.T) {
	assert.Empty(t, noMachineRepoNote(nil, true), "a project with no GitHub remote adds nothing")

	one := noMachineRepoNote([]githubRepo{{Owner: "acme", Name: "widgets"}}, true)
	assert.Contains(t, one, "github.com/acme/widgets")
	for _, name := range []string{"github__repo_get", "github__repo_get_tree", "github__repo_get_content", "github__code_search"} {
		assert.Contains(t, one, name)
	}
	assert.Contains(t, one, "repo:acme/widgets")
	assert.Contains(t, one, "load_tool")

	many := noMachineRepoNote([]githubRepo{{Owner: "acme", Name: "platform"}, {Owner: "acme", Name: "forge", Dir: "forge"}}, true)
	assert.Contains(t, many, "github.com/acme/platform, github.com/acme/forge (forge/)")

	unreadable := noMachineRepoNote([]githubRepo{{Owner: "acme", Name: "widgets"}}, false)
	assert.Contains(t, unreadable, "github.com/acme/widgets")
	assert.Contains(t, unreadable, "No GitHub tools are available")
	assert.NotContains(t, unreadable, "github__", "never name tools the run does not have")
}

func (f *integrationFixture) setProjectRemote(t *testing.T, remote string) {
	t.Helper()
	_, err := f.h.DB().Exec(`UPDATE projects SET remote_url = $1 WHERE id = $2`, remote, f.project.ID)
	require.NoError(t, err)
}

func (f *integrationFixture) systemPrompt() string { return strings.Join(f.driver.prompts, "\n") }

// A no-machine chat in a project on GitHub is told where the code is and how
// to read it — when the owner can read GitHub.
func TestCallLLM_NoMachineNoteNamesTheProjectRepositoryAndHowToReadIt(t *testing.T) {
	f := setupIntegrationFixture(t, true, &ownerConnections{usable: map[string]bool{"github": true}})
	f.setProjectRemote(t, "git@github.com:acme/widgets.git")

	f.offeredTools(t, []string{"tag:web"}, []string{"*"})

	prompt := f.systemPrompt()
	assert.Contains(t, prompt, noMachineSystemNote)
	assert.Contains(t, prompt, "github.com/acme/widgets")
	assert.Contains(t, prompt, "github__repo_get_content")
	assert.Contains(t, prompt, "repo:acme/widgets")
}

// Without GitHub the note still says where the code is, and does not name
// tools the run cannot use.
func TestCallLLM_NoMachineNoteWithoutGitHubSaysTheCodeCannotBeRead(t *testing.T) {
	f := setupIntegrationFixture(t, true, &ownerConnections{usable: map[string]bool{}})
	f.setProjectRemote(t, "https://github.com/acme/widgets.git")

	f.offeredTools(t, []string{"tag:web"}, []string{"*"})

	prompt := f.systemPrompt()
	assert.Contains(t, prompt, "github.com/acme/widgets")
	assert.Contains(t, prompt, "No GitHub tools are available")
	assert.NotContains(t, prompt, "github__repo_get_content")
}

// A chat on a machine has the checkout; it gets no repository note.
func TestCallLLM_ChatOnAMachineGetsNoRepositoryNote(t *testing.T) {
	f := setupIntegrationFixture(t, false, &ownerConnections{usable: map[string]bool{"github": true}})
	f.setProjectRemote(t, "git@github.com:acme/widgets.git")

	f.offeredTools(t, []string{"tag:web"}, []string{"*"})

	assert.NotContains(t, f.systemPrompt(), "github.com/acme/widgets")
}
