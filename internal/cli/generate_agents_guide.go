package cli

import (
	"fmt"
	"os"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// agentsGuideStep keeps AGENTS.md's forge guide in step with the pinned
// forge. `forge project new` writes the guide once; without this step it
// then described the forge that scaffolded the project, release after
// release, while `forge project memory` (what Reliant injects) moved on.
// The guide above `## Project Notes` is forge's and is re-rendered; the
// notes are the project's and are kept byte for byte. A guide forge cannot
// prove it wrote is reported, never rewritten (generator.RefreshAgentsGuide).
const agentsGuideStep = "AGENTS.md guide (above Project Notes)"

// gateAgentsGuide: `harness: none` means forge writes no agent file at all,
// so an AGENTS.md there is the project's own whatever it says.
func gateAgentsGuide(ctx *pipelineContext) bool {
	return ctx.Cfg != nil && generator.Harness(ctx.Cfg.Harness) != generator.HarnessNone
}

func stepAgentsGuide(ctx *pipelineContext) error {
	// Like a frontend's package.json, AGENTS.md is a file the user owns that
	// generate edits in place: marking it a target keeps a disown of it from
	// being retired as obsolete (generate_retire_disowns.go).
	checksums.MarkTier1Target(generator.AgentsGuidePath)
	res, err := generator.RefreshAgentsGuide(ctx.ProjectDir, Name(), ctx.Checksums.IsDisowned(generator.AgentsGuidePath),
		func(cli string) ([]byte, []byte, error) { return renderAgentsGuides(ctx.ProjectDir, ctx.Cfg.Name, cli) })
	if err != nil {
		return ctx.warnOrFail("AGENTS.md guide", err)
	}
	switch res.Outcome {
	case generator.GuideRefreshed:
		fmt.Printf("  ✓ %s: the forge guide is now %s's (your Project Notes are unchanged)\n", generator.AgentsGuidePath, buildinfo.Version())
	case generator.GuideDrifted:
		fmt.Fprint(os.Stderr, agentsGuideDriftNotice(res.Reason))
	}
	return nil
}

// isAgentsGuideRelPath reports whether a project-relative path is AGENTS.md:
// unmarked, but its guide is edited by `forge generate` (stepAgentsGuide), so
// `forge project disown` accepts it — the way to keep a hand-edited guide
// and stop generate reporting it.
func isAgentsGuideRelPath(rel string) bool {
	return normalizeProjectRelPath(rel) == generator.AgentsGuidePath
}

// agentsGuideDriftNotice tells the user the guide is stale, why forge left
// it, and the two ways out: adopt the current guide (forge keeps it current
// from then on) or keep theirs on purpose.
func agentsGuideDriftNotice(reason string) string {
	p := generator.AgentsGuidePath
	return fmt.Sprintf("  ⚠️  %s: forge did not update its guide to this forge's, because %s.\n"+
		"     `%s project memory` prints the current guide. Replace everything above `## Project Notes` in %s with its part above that heading\n"+
		"     (your notes below it stay), and `%s generate` keeps the guide current from then on.\n"+
		"     To keep your guide as it is and silence this: `%s project disown %s --reason \"<why>\"`.\n",
		p, reason, Name(), p, Name(), Name(), p)
}

// renderAgentsGuides renders the two guides RefreshAgentsGuide compares
// against, with the CLI name the file already uses: the guide of the project
// as it is (RenderProjectMemory's), and the one `forge project new` writes
// for a project of this kind.
func renderAgentsGuides(root, name, cli string) (current, scaffold []byte, err error) {
	d, err := projectMemoryData(root, name)
	if err != nil {
		return nil, nil, err
	}
	d.CLI = cli
	if current, err = templates.ProjectTemplates().Render("reliant.md.tmpl", d); err != nil {
		return nil, nil, err
	}
	birth := templates.ProjectMemoryData{Name: name, CLI: cli, Site: d.Site, Container: d.Container}
	if scaffold, err = templates.ProjectTemplates().Render("reliant.md.tmpl", birth); err != nil {
		return nil, nil, err
	}
	return current, scaffold, nil
}
