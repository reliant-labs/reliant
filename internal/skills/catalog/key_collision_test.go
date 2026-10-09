package catalog

import (
	"os"
	"path/filepath"
	"testing"

	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
	"github.com/stretchr/testify/require"
)

func writeClaudeSkill(t *testing.T, base, rel, body string) {
	t.Helper()
	dir := filepath.Join(base, ".claude", "skills", filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	name := filepath.Base(rel)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: "+name+"\ndescription: d\n---\n"+body), 0o644))
}

func skillPaths(snap Snapshot) []string {
	var out []string
	for _, d := range snap.Definitions {
		out = append(out, d.SkillPath)
	}
	return out
}

// A nested repo literally named "forge" must not shadow the forge/ namespace.
func TestKeyCollision_RepoNamedForge(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	ensureForgeYaml(t, root)
	repo := filepath.Join(root, "forge")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	writeClaudeSkill(t, repo, "proto", "repo proto body")

	snap := DiscoverAll(DiscoverInput{ProjectPath: root, RepoSources: []string{"forge"}, LoadFullDefinitions: true})

	fw, ok := snap.ByName[SkillKey{Path: "forge/proto"}]
	require.True(t, ok, "framework forge/proto must survive; paths=%v", skillPaths(snap))
	require.Equal(t, skillscore.ScopeForge, fw.Scope)
	nested, ok := snap.ByName[SkillKey{Source: "forge", Path: "proto"}]
	require.True(t, ok, "nested repo proto must survive")
	require.Equal(t, "forge", nested.Source)

	paths := skillPaths(snap)
	require.Equal(t, fw.SkillPath, paths[skillscore.ResolveSkillPathIndex(paths, "forge/proto")])
	i := skillscore.ResolveSkillPathIndex(paths, "proto")
	require.GreaterOrEqual(t, i, 0)
	require.Equal(t, "proto", paths[i])
	for _, s := range snap.Shadowed {
		require.NotEqual(t, "proto", s.Key.Path, "distinct sources must not be reported as shadowing: %+v", s)
	}
}

// A repo named "api" with skill x vs a root skill whose path is api/x.
func TestKeyCollision_RepoNamedLikeRootNamespace(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	repo := filepath.Join(root, "api")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	writeClaudeSkill(t, repo, "x", "nested x")
	writeClaudeSkill(t, root, "api/x", "root api/x")

	snap := DiscoverAll(DiscoverInput{ProjectPath: root, RepoSources: []string{"api"}, LoadFullDefinitions: true})

	rootDef, ok := snap.ByName[SkillKey{Path: "api/x"}]
	require.True(t, ok)
	require.Contains(t, rootDef.Body, "root api/x")
	nested, ok := snap.ByName[SkillKey{Source: "api", Path: "x"}]
	require.True(t, ok)
	require.Contains(t, nested.Body, "nested x")
	for _, s := range snap.Shadowed {
		require.NotEqual(t, "x", s.Key.Path)
	}
}

// Real same-source/same-path duplicates still dedupe by scope priority and are
// reported with a readable key.
func TestKeyCollision_SameSourceStillDedupes(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	repo := filepath.Join(root, "api")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	writeClaudeSkill(t, repo, "dup", "claude copy")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".reliant", "skills", "dup"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".reliant", "skills", "dup", "SKILL.md"),
		[]byte("---\nname: dup\ndescription: d\n---\nreliant copy"), 0o644))

	snap := DiscoverAll(DiscoverInput{ProjectPath: root, RepoSources: []string{"api"}, LoadFullDefinitions: true})

	key := SkillKey{Source: "api", Path: "dup"}
	winner := snap.ByName[key]
	require.Equal(t, skillscore.ScopeProject, winner.Scope)
	require.Contains(t, winner.Body, "reliant copy")
	var n int
	for _, s := range snap.Shadowed {
		if s.Key == key {
			n++
			require.Equal(t, "api:dup", s.Key.String())
		}
	}
	require.Equal(t, 1, n)
	require.Equal(t, "(root):forge/proto", SkillKey{Path: "forge/proto"}.String())
}
