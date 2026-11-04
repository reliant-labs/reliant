package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func TestRepoWorktreeBasesTargetOnly(t *testing.T) {
	repos := []*core.Repo{{ID: "cp", RelativePath: "control-plane"}, {ID: "f", RelativePath: "forge"}, {ID: "r", RelativePath: "reliant"}}
	bases, target, err := repoWorktreeBases(repos, "control-plane", "feature/only-in-cp", nil)
	require.NoError(t, err)
	require.Equal(t, "cp", target)
	require.Equal(t, map[string]string{"cp": "feature/only-in-cp"}, bases, "siblings must keep their own defaults")
	bases, _, err = repoWorktreeBases(repos, "control-plane", "feature/only-in-cp", map[string]string{"forge": "develop"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"cp": "feature/only-in-cp", "f": "develop"}, bases)
}

func TestRepoWorktreeBasesCannotEscapeChatProject(t *testing.T) {
	repos := []*core.Repo{{ID: "cp", RelativePath: "control-plane"}}
	for _, path := range []string{"/tmp/elsewhere", "../other", "control-plane/../forge", "not-registered"} {
		_, _, err := repoWorktreeBases(repos, path, "feature", nil)
		require.Error(t, err, path)
		_, _, err = repoWorktreeBases(repos, "", "", map[string]string{path: "main"})
		require.Error(t, err, path)
	}
}

func TestFetchRequiresExplicitTargetAndExpectedSHA(t *testing.T) {
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	require.NoError(t, validateWorktreeFetch("cp", "feature/cp", sha))
	for _, args := range [][3]string{{"", "feature", sha}, {"cp", "feature", ""}, {"cp", "--upload-pack=evil", sha}, {"cp", "main; touch /tmp/owned", sha}} {
		require.Error(t, validateWorktreeFetch(args[0], args[1], args[2]))
	}
}
