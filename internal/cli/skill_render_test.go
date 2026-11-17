// Copyright (c) 2025 Reliant Labs
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// TestSkillLoadPrintsRenderSkillContent pins the property harness consumers
// rely on: `skill load` prints exactly RenderSkillContentAt's bytes, so a
// harness injecting a skill through cli.RenderSkill hands its agent what that
// agent would read by running the command itself.
func TestSkillLoadPrintsRenderSkillContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from ~/.forge/skills
	cmdutil.ResetCmdRoute()
	t.Cleanup(cmdutil.ResetCmdRoute)
	t.Cleanup(func() { _ = cmdutil.SetProjectDir("") })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: demo\n"), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}

	for _, arg := range []string{"forge", "forge/forge", "db"} {
		t.Run(arg, func(t *testing.T) {
			var out bytes.Buffer
			root := newTestRootCmd(t)
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"-C", dir, "skill", "load", arg})
			if err := root.Execute(); err != nil {
				t.Fatalf("skill load %s: %v\n%s", arg, err, out.String())
			}

			// Name() reads the route the command just recorded, which is
			// the name the command rewrote to.
			want, err := RenderSkillContentAt(dir, arg, SkillAudienceAll, Name())
			if err != nil {
				t.Fatalf("RenderSkillContentAt: %v", err)
			}
			if out.String() != string(want) {
				t.Errorf("`skill load %s` and RenderSkillContentAt disagree (%d vs %d bytes)", arg, out.Len(), len(want))
			}
		})
	}
}

// TestRenderSkillContentAtRewritesCommands pins the rewrite itself: under a
// `reliant forge` mount every bare `forge <cmd>` reference must carry the
// mount prefix, or the agent is told to run a command it does not have.
func TestRenderSkillContentAtRewritesCommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	raw, err := RenderSkillContentAt("", "forge", SkillAudienceAll, "forge")
	if err != nil {
		t.Fatalf("render as forge: %v", err)
	}
	if !bytes.Contains(raw, []byte("forge project new")) {
		t.Fatal("the start-here skill no longer mentions `forge project new`; pick another command to pin")
	}

	mounted, err := RenderSkillContentAt("", "forge", SkillAudienceAll, "reliant forge")
	if err != nil {
		t.Fatalf("render as reliant forge: %v", err)
	}
	all := strings.Count(string(mounted), "forge project new")
	prefixed := strings.Count(string(mounted), "reliant forge project new")
	if all == 0 || all != prefixed {
		t.Errorf("%d of %d `forge project new` references carry the `reliant ` mount prefix", prefixed, all)
	}

	// "" and "forge" both mean "no rewrite".
	unnamed, err := RenderSkillContentAt("", "forge", SkillAudienceAll, "")
	if err != nil {
		t.Fatalf("render with empty name: %v", err)
	}
	if !bytes.Equal(unnamed, raw) {
		t.Error("an empty cliName rewrote the body; it must leave it unchanged")
	}
}

// TestRenderSkillContentAtFiltersAudienceBeforeRewriting pins the order: the
// audience filter strips @forge-only blocks, and the rewrite applies to what
// survives.
func TestRenderSkillContentAtFiltersAudienceBeforeRewriting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	skillDir := filepath.Join(root, ".forge", "skills", "mixed")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: mixed\ndescription: d\nemit: both\n---\n\nRun forge lint.\n" +
		"<!-- @forge-only:start -->\nRun forge generate.\n<!-- @forge-only:end -->\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	general, err := RenderSkillContentAt(root, "mixed", SkillAudienceGeneral, "reliant forge")
	if err != nil {
		t.Fatalf("render general: %v", err)
	}
	if bytes.Contains(general, []byte("generate")) {
		t.Errorf("general audience kept the @forge-only block:\n%s", general)
	}
	if !bytes.Contains(general, []byte("Run reliant forge lint.")) {
		t.Errorf("general audience lost the rewrite:\n%s", general)
	}

	full, err := RenderSkillContentAt(root, "mixed", SkillAudienceForge, "reliant forge")
	if err != nil {
		t.Fatalf("render forge: %v", err)
	}
	if !bytes.Contains(full, []byte("Run reliant forge generate.")) {
		t.Errorf("forge audience dropped or failed to rewrite the @forge-only block:\n%s", full)
	}
}
