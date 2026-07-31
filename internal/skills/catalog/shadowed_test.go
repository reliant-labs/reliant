package catalog

import (
	"os"
	"path/filepath"
	"testing"

	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
	"github.com/stretchr/testify/require"
)

func writeShadowSkill(t *testing.T, dir, name, body string) {
	t.Helper()
	skillPath := filepath.Join(dir, name, "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o755))
	require.NoError(t, os.WriteFile(skillPath,
		[]byte("---\nname: "+name+"\ndescription: How this project does it\n---\n"+body), 0o644))
}

// shadowFor selects the collision between a specific pair of producers, and
// fails loudly when the set is empty — a project on a machine with forge
// installed collides on several keys at once, so a bare count would assert
// against whichever entry happened to sort first.
func shadowFor(t *testing.T, snapshot Snapshot, key string, loser skillscore.Scope) ShadowedSkill {
	t.Helper()
	require.NotEmpty(t, snapshot.Shadowed, "discovery reported no collisions at all; nothing below is being checked")
	for _, s := range snapshot.Shadowed {
		if s.Key == key && s.LoserScope == loser {
			return s
		}
	}
	t.Fatalf("no collision reported for %q shadowing scope %q; reported: %+v", key, loser, snapshot.Shadowed)
	return ShadowedSkill{}
}

// A project can hold two copies of one skill from two producers, with different
// bytes, and the catalog silently kept one. Verified on a real scaffold:
// `forge generate` renders forge's own skills into .claude/skills, which
// discovery picks up at ScopeClaude (priority 7) while the forge-embedded
// originals are ScopeForge (priority 10) — so the generated render wins, and it
// carries a banner telling the reader that `forge skill load` prints the
// authoritative copy "when the two differ". Nothing anywhere said the two
// existed, let alone whether they differed.
//
// Note what is NOT being changed: ScopeClaude outranking ScopeForge is correct
// — a project's own skill should beat a framework default. The defect is that
// discovery observed the duplicate and reported nothing.
func TestDiscoverReportsShadowedSkill(t *testing.T) {
	project := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	writeShadowSkill(t, filepath.Join(project, ".reliant", "skills"), "service-layer", "The authored copy.")
	writeShadowSkill(t, filepath.Join(project, ".claude", "skills"), "service-layer", "A rendered copy of a different vintage.")

	snapshot := Discover(DiscoverInput{ProjectPath: project, LoadFullDefinitions: true})

	got := shadowFor(t, snapshot, "service-layer", skillscore.ScopeClaude)
	require.Equal(t, skillscore.ScopeProject, got.WinnerScope, "the higher-priority copy must be the one kept")
	require.True(t, got.BytesDiffer,
		"the two copies have different bodies and the report says they match — a reader cannot tell whether the dropped copy mattered")

	// The surviving definition is still the winner: this reports the collision,
	// it does not re-rank the scopes.
	require.Equal(t, skillscore.ScopeProject, snapshot.ByName["service-layer"].Scope)
}

// Identical copies are still reported — two producers is the fact — but the
// report must not claim a difference it did not observe.
func TestShadowedReportDoesNotInventDifferences(t *testing.T) {
	project := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	const body = "Identical in both copies."
	writeShadowSkill(t, filepath.Join(project, ".reliant", "skills"), "db", body)
	writeShadowSkill(t, filepath.Join(project, ".claude", "skills"), "db", body)

	snapshot := Discover(DiscoverInput{ProjectPath: project, LoadFullDefinitions: true})

	got := shadowFor(t, snapshot, "db", skillscore.ScopeClaude)
	require.False(t, got.BytesDiffer, "the two copies are byte-identical and the report says they differ")
}

// A skill with one producer is not reported — otherwise the signal above is
// noise every project emits and nobody reads.
func TestNoShadowReportForASkillWithOneProducer(t *testing.T) {
	project := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	// A name no framework ships, so the only producer is this project.
	const only = "acme-invoice-rounding"
	writeShadowSkill(t, filepath.Join(project, ".reliant", "skills"), only, "Only copy.")

	snapshot := Discover(DiscoverInput{ProjectPath: project, LoadFullDefinitions: true})

	// Fail loudly if nothing was discovered: an empty catalog would satisfy the
	// assertion below without proving anything.
	require.Contains(t, snapshot.ByName, only)
	for _, s := range snapshot.Shadowed {
		require.NotEqual(t, only, s.Key, "a skill with a single producer was reported as shadowed: %+v", s)
	}
}
