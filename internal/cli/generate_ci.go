package cli

import (
	"fmt"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// writeCIScaffold writes a generated CI workflow as a scaffold ("yours"):
// write-once when absent, user-owned from birth, NO forge:hash marker,
// and never re-emitted while the file exists. CI workflows are the
// canonical hand-edited policy file (add jobs, secrets, custom steps),
// so certifying them Tier-1 mis-flagged every sanctioned edit as
// `user_edited_gen_files` drift and pushed users toward `forge project disown`.
// The derived jobs (frontend lint, KCL-env matrix, verify-generated) are
// a convenience starting point, not a correctness requirement — a stale
// workflow still runs, unlike buf.yaml whose derived dep gates the build
// — so write-once is the right lifecycle. Write-once covers deletion too:
// a repo that manages its own CI removes these and they stay removed. To
// re-scaffold one, `forge project rescaffold <path>`.
func writeCIScaffold(root, relPath string, content []byte) (bool, error) {
	written, err := generator.WriteScaffoldIfMissing(root, relPath, content)
	if err != nil {
		return false, fmt.Errorf("write %s: %w", relPath, err)
	}
	if written {
		fmt.Printf("  ✅ Generated %s\n", relPath)
	} else if line, routine := scaffoldSkipLine(root, relPath); routine {
		routinef("%s\n", line)
	} else {
		fmt.Println(line)
	}
	return written, nil
}

// generateCIWorkflows writes the project's CI files as write-once scaffolds
// (see writeCIScaffold): the user owns them after the first write and forge
// never stomps edits. Under ci.provider github those are the GitHub Actions
// workflows and dependabot config; under ci.provider reliant, the Reliant
// workflows under .reliant/workflows/.
//
// Which files exist and what each renders with come from
// generator.CIWorkflows — the SAME mapping `forge project new` uses — so
// re-scaffolding a deleted workflow reproduces what the scaffold wrote.
// This caller contributes one input only it can answer: the frontends,
// discovered from the KCL topology (see generate_ci_frontends.go).
//
// Switching ci.provider writes the new provider's files and leaves the old
// ones alone: they are the user's. So the first write of the Reliant
// workflows prints what to do next (generator.ReliantCIGuidance): activate
// them, require the check, retire the GitHub workflows.
func generateCIWorkflows(root string, cfg *config.ProjectConfig, _ *generator.FileChecksums, _ bool) error {
	in := generator.CIInputs{}
	if cfg.CI.EffectiveProvider() == config.CIProviderGitHub {
		// Only the GitHub workflows vary with the KCL topology; reading
		// it renders every env.
		in = ciInputs(root, cfg)
	}
	var reliantWritten []string
	for _, f := range generator.CIWorkflowsFor(root, cfg, in) {
		// A same-named workflow the project wrote itself: never adopt it
		// silently (generator.ForeignCIWorkflow).
		if generator.ForeignCIWorkflow(root, f.Dest) {
			fmt.Println(generator.ForeignCIWorkflowNotice(f.Dest))
			continue
		}
		content, err := f.Render()
		if err != nil {
			return fmt.Errorf("render %s: %w", f.Dest, err)
		}
		written, err := writeCIScaffold(root, f.Dest, content)
		if err != nil {
			return err
		}
		if written && generator.CIProviderOfPath(f.Dest) == config.CIProviderReliant {
			reliantWritten = append(reliantWritten, f.Dest)
		}
	}
	if len(reliantWritten) > 0 {
		fmt.Print(generator.ReliantCIGuidance(cfg, Name(), reliantWritten))
	}
	return nil
}

// ciInputs gathers what only a live project's KCL can answer: the frontends,
// and which envs are hosted (release.yml + forge-deploy are scaffolded for
// those, and deploy.yml leaves them to it).
func ciInputs(root string, cfg *config.ProjectConfig) generator.CIInputs {
	topo := discoverCIHostedEnvs(root)
	return generator.CIInputs{Frontends: ciFrontends(root, cfg), HostedEnvs: topo.Hosted, MixedEnvs: topo.Mixed}
}

// ciFrontends is the frontend list CI drives a Node toolchain for:
// the KCL-declared frontends that have a directory in THIS repository.
//
// CI workflow paths are committed to git and run on a bare checkout of
// the repository, so a frontend with no directory here (a cross-repo pin)
// contributes no CI step — the workflow would cd into a path that does not
// exist on the runner.
func ciFrontends(root string, cfg *config.ProjectConfig) []templates.FrontendCIConfig {
	var out []templates.FrontendCIConfig
	for _, fe := range discoverCIFrontends(root, cfg) {
		if p, ok := fe.Dir("."); ok {
			out = append(out, templates.FrontendCIConfig{Name: fe.Name, Path: p,
				PackageManager: frontenddeps.PackageManagerOrScaffold(filepath.Join(root, p))})
		}
	}
	return out
}
