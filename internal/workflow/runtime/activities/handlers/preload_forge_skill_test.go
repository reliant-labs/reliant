// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	cfgpkg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/skills/catalog"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
)

func forgeStartHereEntry() cfgpkg.StoredSkill {
	return cfgpkg.StoredSkill{
		Name: "forge", SkillPath: forgeStartHereSkill, Scope: string(skillscore.ScopeForge),
		Body: "# Forge — start here\nrun `reliant forge scaffold`\n",
	}
}

func TestWithForgeStartHere(t *testing.T) {
	generalAgent := cfgpkg.StoredSkill{Name: "general-agent", SkillPath: "general-agent", Scope: "builtin", Body: "GA"}
	forgeProject := []cfgpkg.StoredSkill{generalAgent, forgeStartHereEntry()}

	for _, tc := range []struct {
		name      string
		requested []string
		catalog   []cfgpkg.StoredSkill
		want      []string
		wantAdded []string
	}{
		{
			name:      "forge project: added alongside the node's own skills, after them",
			requested: []string{"general-agent"},
			catalog:   forgeProject,
			want:      []string{"general-agent", forgeStartHereSkill},
			wantAdded: []string{forgeStartHereSkill},
		},
		{
			name:      "not a forge project: nothing added",
			requested: []string{"general-agent"},
			catalog:   []cfgpkg.StoredSkill{generalAgent},
			want:      []string{"general-agent"},
		},
		{
			name:    "node preloads nothing (a title, a classifier): nothing added",
			catalog: forgeProject,
			want:    nil,
		},
		{
			name:      "already requested, in any case: not added twice",
			requested: []string{"Forge/Forge", "db"},
			catalog:   forgeProject,
			want:      []string{"Forge/Forge", "db"},
		},
		{
			name:      "an on-disk render shadows the forge entry: never preloaded",
			requested: []string{"general-agent"},
			catalog: []cfgpkg.StoredSkill{generalAgent, func() cfgpkg.StoredSkill {
				s := forgeStartHereEntry()
				s.Scope = "claude"
				return s
			}()},
			want: []string{"general-agent"},
		},
		{
			name:      "only a nested repo is a forge project: root rule not met",
			requested: []string{"general-agent"},
			catalog: []cfgpkg.StoredSkill{generalAgent, func() cfgpkg.StoredSkill {
				s := forgeStartHereEntry()
				s.Source = "api"
				return s
			}()},
			want: []string{"general-agent"},
		},
		{
			name:      "an empty body is not a skill",
			requested: []string{"general-agent"},
			catalog: []cfgpkg.StoredSkill{generalAgent, func() cfgpkg.StoredSkill {
				s := forgeStartHereEntry()
				s.Body = "  "
				return s
			}()},
			want: []string{"general-agent"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, added := withForgeStartHere(tc.requested, tc.catalog)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.wantAdded, added)
		})
	}
}

// Reliant's catalog has TWO skills named "forge": the synthesized namespace
// map at `forge` and forge's start-here at `forge/forge`. The seed deduped by
// name, so a node that preloaded `forge` (forge-one-shot does) silently lost
// the start-here as a "duplicate".
func TestBuildSeededSkillMessages_SameNameDifferentSkillsBothArrive(t *testing.T) {
	cfg := &cfgpkg.Config{Skills: []cfgpkg.StoredSkill{
		{Name: "forge", SkillPath: "forge", Scope: string(skillscore.ScopeForge), Body: "# Forge skills\nMAP BODY\n"},
		forgeStartHereEntry(),
	}}

	msgs, injected, _, missing := buildSeededSkillMessages(cfg, []string{"forge", forgeStartHereSkill})
	require.Empty(t, missing)
	require.Equal(t, []string{"forge", "forge"}, injected)
	text := msgs[0].Content().Text
	require.Contains(t, text, "MAP BODY")
	require.Contains(t, text, "Forge — start here")
	require.Contains(t, text, `<skill name="forge" path="forge/forge">`)
}

// The end-to-end property the harness promises: in a forge project, an agent
// preloading general-agent ALSO receives forge's start-here skill, and the
// body it receives is byte-for-byte what `<binary> forge skill load forge`
// prints — rendered from the forge linked into this binary, not from any copy
// on disk — undelivered bytes included: it must arrive whole, not windowed.
//
// The catalog here is built by the same discovery the daemon runs
// (catalog.DiscoverAll with full definitions, as buildSkillsIndex), and the CLI is the real forge
// command tree mounted the way reliant mounts it (`reliant forge`).
func TestForgeStartHerePreload_IsWhatTheCLIPrints(t *testing.T) {
	dir, stored := discoverForgeProjectCatalog(t)

	requested, added := withForgeStartHere([]string{"general-agent"}, stored)
	require.Equal(t, []string{forgeStartHereSkill}, added, "a forge project root must get the start-here skill")

	msgs, injected, oversized, missing := buildSeededSkillMessages(&cfgpkg.Config{Skills: stored}, requested)
	require.Empty(t, missing)
	require.Equal(t, []string{"general-agent", "forge"}, injected)
	require.Empty(t, oversized, "the start-here skill must arrive whole; a windowed copy hides most of it")
	seed := msgs[0].Content().Text

	// What `reliant forge skill load forge` prints, run in the project.
	cli := runEmbeddedForge(t, dir, "skill", "load", "forge")
	require.Contains(t, cli, "# Forge — start here")
	require.Contains(t, seed, cli, "the preloaded body must be exactly what `reliant forge skill load forge` prints")

	// Commands are spelled for the mount that serves them, so an agent never
	// runs a `forge` from PATH that is a different build (or absent).
	for _, bare := range []string{"\nforge project new", "`forge project new", "\nforge scaffold"} {
		require.NotContains(t, seed, bare, "a bare `forge` command leaked into the preload")
	}
	require.True(t, strings.Contains(seed, " forge project new"), "the start-here's commands are missing entirely")

	// And it is byte-stable turn over turn, so the prompt cache keeps it.
	again, _, _, _ := buildSeededSkillMessages(&cfgpkg.Config{Skills: stored}, requested)
	require.Equal(t, seed, again[0].Content().Text)
}

// Every skill forge ships whose rendered body fits the delivery budget must
// arrive whole, whether preloaded or loaded by hand. forge guards each body's
// size where the content lives (TestShippedSkillsFitDeliveryBudget), but it
// cannot see the sub-skill and related-skill lists reliant appends, which grow
// with the catalog rather than the skill. This test runs against the forge
// linked into this binary, so a pin bump that adds skills is checked by the
// exact sizes it ships.
func TestForgeSkills_ABodyThatFitsArrivesWhole(t *testing.T) {
	_, stored := discoverForgeProjectCatalog(t)

	checked := 0
	for _, s := range stored {
		if s.Scope != string(skillscore.ScopeForge) || strings.TrimSpace(s.Body) == "" {
			continue
		}
		if _, bodyAloneTruncated := tools.DeliverSkillContent(s.SkillPath, s.Body); bodyAloneTruncated {
			continue // over budget on its own: a publishing defect forge's guard owns
		}
		_, body, ok := tools.LoadSkillForInjection(stored, s.SkillPath)
		require.True(t, ok, "%s must resolve", s.SkillPath)
		_, truncated := tools.DeliverSkillContent(s.SkillPath, body)
		require.False(t, truncated, "%s: the body fits the budget, so the navigation reliant appends must not window it", s.SkillPath)
		checked++
	}
	require.Greater(t, checked, 10, "the linked forge must contribute its shipped skills to the catalog")
}

// discoverForgeProjectCatalog builds the skill catalog for a fresh forge
// project with the same discovery the daemon runs (catalog.DiscoverAll with
// full definitions, as buildSkillsIndex), from the forge linked into this
// binary. It returns the project dir and the catalog.
func discoverForgeProjectCatalog(t *testing.T) (string, []cfgpkg.StoredSkill) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // no ~/.forge or ~/.reliant skills
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: roofers\n"), 0o644))

	snap := catalog.DiscoverAll(catalog.DiscoverInput{ProjectPath: dir, LoadFullDefinitions: true})
	stored := make([]cfgpkg.StoredSkill, 0, len(snap.Definitions))
	for _, d := range snap.Definitions {
		stored = append(stored, cfgpkg.StoredSkill{
			SkillPath: d.SkillPath, Name: d.Name, Description: d.Description, Scope: string(d.Scope),
			Body: d.Body, HasChildren: d.HasChildren, Source: d.Source,
		})
	}
	return dir, stored
}

// runEmbeddedForge runs forge's real command tree mounted under a parent the
// way cmd/reliant mounts it, from inside dir, and returns stdout.
func runEmbeddedForge(t *testing.T, dir string, args ...string) string {
	t.Helper()
	t.Chdir(dir)
	parent := &cobra.Command{Use: "reliant", SilenceUsage: true}
	parent.AddCommand(forgecli.NewRootCmd())
	var stdout, stderr bytes.Buffer
	parent.SetOut(&stdout)
	parent.SetErr(&stderr)
	parent.SetArgs(append([]string{"forge"}, args...))
	require.NoError(t, parent.Execute(), "forge %v: %s", args, stderr.String())
	return stdout.String()
}
