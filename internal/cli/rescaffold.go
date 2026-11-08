// `forge project rescaffold <path>...` — have forge write a scaffold-once file
// again.
//
// A scaffold-once ("yours") file is written exactly once and then belongs to
// the user. The birth ledger (.forge/scaffolded/, committed) is what makes
// that true across deletion: an absent file with a birth record was deleted on
// purpose, and `forge generate` leaves it deleted in every clone. That is a
// feature — a repo that runs its own CI deletes forge's workflows and they
// stay gone — and it is not negotiable here.
//
// What was false is the way back. Every scaffolded workflow said "`forge
// generate` re-creates it only if you delete it first", and generate did no
// such thing; it even printed "exists — leaving it untouched" about a file
// that was gone. The real reset was a hand edit of the ledger, and for the
// files only `forge project new` writes (the pre-commit config and workflow,
// the devcontainer, ...) there was no renderer left to run at all.
//
// This command is the one supported reset. For each ABSENT path it drops the
// birth record and writes the file the way forge would scaffold it today, then
// records the birth again — so the file is the user's once more, and a later
// deletion sticks as before. A PRESENT path is refused: overwriting a file the
// user owns is `forge project upgrade --force <path>`, which shows the diff
// first.
//
// Which renderer answers a path, in order:
//
//  1. The CI mapper (generator.CIWorkflows) for .github/workflows/* and
//     dependabot.yml — THE decision about which workflows a project has.
//     Rendered through `forge generate`'s own CI step, so a rescaffolded
//     workflow is what generate would have written — that step alone, not
//     the whole pipeline, when CI files are all the batch needs generated.
//  2. The rest of the `forge generate` pipeline, for scaffolds it births
//     (the command tree, internal/app, frontend pages, config.k, ...).
//  3. The upgrade renderer, for files `forge project upgrade` manages
//     (Taskfile.yml, Dockerfile, .golangci.yml, ...) and the scaffold-once
//     advisory rows — rendered against the live project.
//  4. The `forge project new` scaffold itself, rendered for this project's
//     forge.yaml into a staging directory (generator.ScaffoldProjectInto) —
//     the only renderer for the DX set, and the one that makes "every file
//     forge scaffolds can be re-emitted" true without a list to maintain.
package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

func newRescaffoldCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rescaffold <path>...",
		Short: "Re-create scaffold-once files you deleted, as forge would scaffold them today",
		Long: `Re-create one or more scaffold-once ("yours") files.

forge writes a scaffold-once file exactly once. Deleting it is an act of
ownership that .forge/scaffolded/ records, so ` + "`forge generate`" + ` leaves it deleted —
in this checkout and every clone. To get forge's version back, name it here:

  forge project rescaffold .github/workflows/ci.yml

For each path, rescaffold drops its birth record, writes the file the way forge
scaffolds it for this project today, and records the birth again: the file is
yours once more, and deleting it again sticks.

Every file forge scaffolds can be re-created this way — CI workflows (through
the same mapper ` + "`forge generate`" + ` uses, so only workflows this project has),
.pre-commit-config.yaml, the devcontainer, the command tree, internal/app, the
frontend pages, and the rest.

A path that still exists is refused: rescaffold never overwrites your bytes. To
replace a file you have edited with the current template, use
` + "`forge project upgrade --force <path>`" + `, which shows the diff first.

Examples:
  forge project rescaffold .github/workflows/e2e.yml .pre-commit-config.yaml
  forge project rescaffold internal/handlers/item/handlers_crud_test.go`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath, err := findProjectConfigFile()
			if err != nil {
				return err
			}
			store, err := loadProjectStoreFrom(configPath)
			if err != nil {
				return err
			}
			root := filepath.Dir(configPath)
			// The lock spans the deletions as well as the regenerate: a
			// concurrent run must not see the paths removed but not yet
			// re-created.
			return withGenerateLock(root, func() error {
				return rescaffoldPaths(cmd.OutOrStdout(), root, store.Config(), args, rescaffoldGenerate{
					envConfig: func() error { return runGeneratePipelineFlags(root, pipelineFlags{Steps: envConfigStepPreset}) },
					ci:        func() error { return generateCIWorkflows(root, store.Config(), nil, false) },
					pipeline:  func() error { return runGeneratePipelineFlags(root, pipelineFlags{}) },
				})
			})
		},
	}
}

// rescaffoldCmd is the command line that re-creates the named paths, for
// notices that point at it.
func rescaffoldCmd(paths ...string) string {
	return fmt.Sprintf("%s project rescaffold %s", Name(), strings.Join(paths, " "))
}

// rescaffoldGenerate is the part of `forge generate` a rescaffold drives,
// injected so tests can stand in the steps that own the paths they exercise.
type rescaffoldGenerate struct {
	// envConfig writes the config modules every env's KCL imports
	// (`forge generate --steps env-config`). Run only when an env does not
	// render as rescaffold asks which CI files the project has — see
	// refuseCIPathsTheProjectLacks. nil skips it.
	envConfig func() error
	// ci writes the CI workflows the mapper emits for this project — the
	// pipeline's "CI workflows" step alone. A batch whose every path that
	// needs a generate is a CI file runs this instead of the pipeline: a
	// workflow is rendered from the project's declarations, not from what
	// codegen writes, so it needs no buf, no frontend toolchain and no
	// compile (a full generate failed until `npm ci` in a TS plugin
	// directory, for a rescaffold of ci.yml). nil falls back to pipeline.
	ci func() error
	// pipeline runs the `forge generate` pipeline. It is run once, after
	// every path's birth record is dropped, and only when a path needs it.
	pipeline func() error
}

// rescaffoldPaths re-creates each absent scaffold-once path under root.
func rescaffoldPaths(w io.Writer, root string, cfg *config.ProjectConfig, rawPaths []string, gen rescaffoldGenerate) error {
	paths, err := normalizeRescaffoldPaths(root, rawPaths)
	if err != nil {
		return err
	}
	if cfg != nil && cfg.CI.Provider != "" && cfg.CI.Provider != "github" {
		for _, p := range paths {
			if generator.IsCIMapperPath(p) {
				return rescaffoldErr(paths, fmt.Sprintf("%s is a GitHub Actions file, and this project's ci.provider is %q", p, cfg.CI.Provider),
					"forge scaffolds CI only for ci.provider: github")
			}
		}
	}

	// Refuse up front, before anything is written: a batch that is half
	// applied and half refused is harder to reason about than one that did
	// nothing.
	var present []string
	for _, p := range paths {
		if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(p))); statErr == nil {
			present = append(present, p)
		}
	}
	if len(present) > 0 {
		return rescaffoldErr(paths,
			fmt.Sprintf("%s still %s — rescaffold only re-creates files that are absent, so it never overwrites yours",
				strings.Join(present, ", "), plural(len(present), "exists", "exist")),
			presentFileRemedy(root, cfg, present))
	}

	// A service proto is not a scaffold of the project — it IS the service.
	// Deleting it removed the service, so there is nothing left to derive
	// its re-creation from; adding a service back is its own verb.
	for _, p := range paths {
		if svc, ok := serviceProtoOf(p); ok {
			return rescaffoldErr(paths,
				fmt.Sprintf("%s declares the %s service — deleting it removed the service, so there is no project shape to re-derive it from", p, svc),
				fmt.Sprintf("re-create the service with `%s scaffold service %s`", Name(), svc))
		}
	}

	// CI paths answer to the mapper, and a workflow the mapper does not
	// emit for this project is not re-created from any other renderer.
	if slices.ContainsFunc(paths, generator.IsCIMapperPath) {
		if err := refuseCIPathsTheProjectLacks(root, cfg, paths, gen.envConfig); err != nil {
			return err
		}
	}

	// Drop the birth records — the ONE thing standing between a deleted
	// scaffold and every writer that would scaffold it.
	for _, p := range paths {
		checksums.ForgetScaffold(root, p)
	}

	// Pass 1: the generate pipeline. It owns the CI workflows and every
	// scaffold whose content depends on the project's current state.
	needsPipeline, onlyCI := false, true
	for _, p := range paths {
		if !rescaffoldRenderableByUpgrade(root, cfg, p) {
			needsPipeline = true
			onlyCI = onlyCI && generator.IsCIMapperPath(p)
		}
	}
	// A run that reached the end with failed steps still wrote the rest,
	// and its writes stand: finish re-creating the paths, then fail with it.
	var stepsFailed *factory.StepsFailedError
	switch {
	case needsPipeline && onlyCI && gen.ci != nil:
		if err := gen.ci(); err != nil {
			return fmt.Errorf("render the CI workflows (re-scaffolding %s): %w", strings.Join(paths, ", "), err)
		}
	case needsPipeline && gen.pipeline != nil:
		if err := gen.pipeline(); err != nil && !errors.As(err, &stepsFailed) {
			return fmt.Errorf("forge generate (re-scaffolding %s): %w", strings.Join(paths, ", "), err)
		}
	}

	// Pass 2: anything the pipeline did not write comes from the upgrade
	// renderer or the staging render of `forge project new`.
	var remaining []string
	for _, p := range paths {
		if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(p))); statErr != nil {
			remaining = append(remaining, p)
		}
	}
	var unrenderable []string
	if len(remaining) > 0 {
		staging, stageErr := stagedProjectScaffold(root, cfg)
		if staging != "" {
			defer func() { _ = os.RemoveAll(staging) }()
		}
		for _, p := range remaining {
			written, werr := rescaffoldFromRenderers(root, cfg, staging, p)
			if werr != nil {
				return werr
			}
			if !written {
				unrenderable = append(unrenderable, p)
			}
		}
		if len(unrenderable) > 0 && stageErr != nil {
			return fmt.Errorf("render the project scaffold for %s: %w", strings.Join(unrenderable, ", "), stageErr)
		}
	}

	var done []string
	for _, p := range paths {
		if slices.Contains(unrenderable, p) {
			continue
		}
		done = append(done, p)
		// Whatever wrote it, a scaffold is a scaffold-once birth again —
		// so deleting it later sticks, as before. A forge-generated file
		// is not a scaffold and gets no birth record.
		if content, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(p))); rerr == nil && !generator.IsForgeGenerated(content) {
			checksums.RecordScaffold(root, p)
		} else {
			checksums.ForgetScaffold(root, p)
		}
	}
	for _, p := range done {
		fmt.Fprintf(w, "  ✅ Re-scaffolded %s (yours to edit; delete it again and it stays deleted)\n", p)
	}
	if len(unrenderable) > 0 {
		sort.Strings(unrenderable)
		return rescaffoldErr(unrenderable,
			fmt.Sprintf("forge does not scaffold %s for this project", strings.Join(unrenderable, ", ")),
			"check the path: rescaffold re-creates files forge writes for this project's forge.yaml (kind, features, frontends, services)")
	}
	if stepsFailed != nil {
		return fmt.Errorf("forge generate (re-scaffolding %s): %w", strings.Join(paths, ", "), stepsFailed)
	}
	return nil
}

// refuseCIPathsTheProjectLacks refuses every CI-mapper path in paths that
// the mapper does not emit for this project — before anything of the
// user's is written.
//
// Which CI files a project has is read off a RENDER of every env: a hosted
// env gets release.yml and the forge-deploy action (generate_ci_hosted.go).
// An env imports config modules only `forge generate` writes, so on a
// project generate has not completed on — a fresh scaffold whose bootstrap
// generate failed — its hosted envs do not render, and reading that as "not
// hosted" refused the very release.yml the scaffold wrote. The full pipeline
// orders its CI step after those modules for the same reason; here, when an
// env does not render, envConfig writes them and the question is asked
// again. An env that still does not render is named as the reason, never
// reported as an env that is not hosted.
func refuseCIPathsTheProjectLacks(root string, cfg *config.ProjectConfig, paths []string, envConfig func() error) error {
	// The question is about the tree as it is now, whatever an earlier
	// asker in this process memoized — and the answer is for this check
	// alone. Left memoized, it was handed to the pipeline's CI step, which
	// must render the envs as the PIPELINE leaves them: on a project whose
	// env config modules the pipeline was about to write, that step wrote
	// CI for hosted envs it still took for cluster envs (a deploy.yml beside
	// release.yml, a build-images.yml for the wrong topology).
	forgetCIDiscovery(root)
	defer forgetCIDiscovery(root)
	if len(discoverCIHostedEnvs(root).Unrendered) > 0 && envConfig != nil {
		if err := envConfig(); err != nil {
			return fmt.Errorf("write the env config modules so every env renders (forge generate --steps %s): %w", envConfigStepPreset, err)
		}
		forgetCIDiscovery(root)
	}
	unrendered := discoverCIHostedEnvs(root).Unrendered
	inputs := ciInputs(root, cfg)
	for _, p := range paths {
		if !generator.IsCIMapperPath(p) {
			continue
		}
		_, ok, err := generator.CIWorkflowFileFor(root, cfg, inputs, p)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		// Would the project have it if the envs that did not render were
		// hosted? Then its absence is forge not knowing, not the project
		// lacking it.
		if len(unrendered) > 0 {
			ifHosted := inputs
			ifHosted.HostedEnvs = append(slices.Clone(inputs.HostedEnvs), unrendered...)
			if _, wouldHave, _ := generator.CIWorkflowFileFor(root, cfg, ifHosted, p); wouldHave {
				return rescaffoldErr(paths,
					fmt.Sprintf("forge cannot tell whether this project has %s: that depends on which envs are hosted, which forge reads off a render of each env, and %s did not render",
						p, strings.Join(unrendered, ", ")),
					fmt.Sprintf("run `%s env render %s` to see why, fix it, then re-run rescaffold", Name(), unrendered[0]))
			}
		}
		return rescaffoldErr(paths, fmt.Sprintf("this project has no %s: %s", p, generator.CIWorkflowAbsenceReason(p)),
			"change forge.yaml or the project so it has one, then re-run rescaffold")
	}
	return nil
}

// rescaffoldFromRenderers writes p from the upgrade renderer, the advisory
// renderer, or the staged project scaffold — the first that owns it.
func rescaffoldFromRenderers(root string, cfg *config.ProjectConfig, staging, p string) (bool, error) {
	if content, tier, ok, err := generator.RenderUpgradeManagedFile(root, cfg, p); err != nil {
		return false, err
	} else if ok && tier == generator.Tier2 {
		return true, generator.WriteRescaffoldedFile(root, p, content, 0, "")
	}
	if content, ok, err := generator.RenderAdvisoryFile(root, cfg, p); err != nil {
		return false, err
	} else if ok {
		return true, generator.WriteRescaffoldedFile(root, p, content, 0, "")
	}
	if staging == "" {
		return false, nil
	}
	staged := filepath.Join(staging, filepath.FromSlash(p))
	info, err := os.Stat(staged)
	if err != nil || !info.Mode().IsRegular() {
		return false, nil
	}
	content, err := os.ReadFile(staged)
	if err != nil {
		return false, err
	}
	// A file carrying forge's generated banner that the pipeline pass did
	// not write is one only the scaffold emits (a CLI project's
	// cmd/<bin>/main.go): the staged bytes ARE forge's render of it. It is
	// written, but not recorded as a scaffold-once birth — it is not one.
	return true, generator.WriteRescaffoldedFile(root, p, content, info.Mode(), generator.StagedScaffoldBirthHash(staging, p))
}

// rescaffoldRenderableByUpgrade reports whether p can be written without the
// generate pipeline: an upgrade-managed Tier-2 file or an advisory row.
// Everything else goes through the pipeline first, which is cheap to skip for
// the common Taskfile/Dockerfile case and correct for everything else.
func rescaffoldRenderableByUpgrade(root string, cfg *config.ProjectConfig, p string) bool {
	if generator.IsCIMapperPath(p) {
		return false
	}
	if _, tier, ok, err := generator.RenderUpgradeManagedFile(root, cfg, p); err == nil && ok && tier == generator.Tier2 {
		return true
	}
	if _, ok, err := generator.RenderAdvisoryFile(root, cfg, p); err == nil && ok {
		return true
	}
	return false
}

// stagedProjectScaffold renders `forge project new`'s output for this project
// into a fresh temp dir. The caller removes it.
func stagedProjectScaffold(root string, cfg *config.ProjectConfig) (string, error) {
	staging, err := os.MkdirTemp("", "forge-rescaffold-*")
	if err != nil {
		return "", err
	}
	// The scaffold announces itself on stdout; a rescaffold reports only
	// what it re-created.
	var sink bytes.Buffer
	defer atomicfile.MarkScratch(staging)()
	err = withStdout(&sink, func() error {
		return generator.ScaffoldProjectInto(root, staging, cfg)
	})
	return staging, err
}

// normalizeRescaffoldPaths turns user-typed paths into sorted, deduplicated
// project-relative slash paths, refusing any that leave the project.
func normalizeRescaffoldPaths(root string, raw []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, r := range raw {
		p := strings.TrimSpace(r)
		if p == "" {
			continue
		}
		if filepath.IsAbs(p) {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil, rescaffoldErr(raw, fmt.Sprintf("%s is not inside the project at %s", r, root), "pass a path relative to the project root")
			}
			p = rel
		}
		p = filepath.ToSlash(filepath.Clean(p))
		if p == "." || p == ".." || strings.HasPrefix(p, "../") {
			return nil, rescaffoldErr(raw, fmt.Sprintf("%s is not inside the project at %s", r, root), "pass a path relative to the project root")
		}
		if strings.HasPrefix(p, ".forge/") {
			return nil, rescaffoldErr(raw, fmt.Sprintf("%s is forge's own state, not a scaffold", r), "rescaffold re-creates files forge scaffolds into the project")
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, rescaffoldErr(raw, "no paths given", "name the files to re-create, e.g. .github/workflows/ci.yml")
	}
	sort.Strings(out)
	return out, nil
}

// presentFileRemedy names how to replace files the user still has.
//
// `forge project upgrade --force <path>` shows the diff before adopting, so
// it is the better tool — but only for paths upgrade manages. Suggesting it
// for anything else (a CI workflow, the pre-commit config) sends the user
// straight into upgrade's "not upgrade-managed" refusal; for those the
// remedy is delete-then-rescaffold.
func presentFileRemedy(root string, cfg *config.ProjectConfig, present []string) string {
	var upgradable, other []string
	for _, p := range present {
		if rescaffoldRenderableByUpgrade(root, cfg, p) {
			upgradable = append(upgradable, p)
		} else {
			other = append(other, p)
		}
	}
	var parts []string
	if len(upgradable) > 0 {
		parts = append(parts, fmt.Sprintf("to replace %s with the current template, run `%s project upgrade --force %s` (it shows the diff first)",
			strings.Join(upgradable, ", "), Name(), strings.Join(upgradable, " ")))
	}
	if len(other) > 0 {
		parts = append(parts, fmt.Sprintf("to get forge's version of %s, delete it and run %#q (keep a copy of anything of yours first)",
			strings.Join(other, ", "), rescaffoldCmd(other...)))
	}
	return strings.Join(parts, "; ")
}

// serviceProtoOf reports whether p is a scaffolded service proto
// (proto/services/<svc>/v1/<svc>.proto) and names the service.
func serviceProtoOf(p string) (string, bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 5 || parts[0] != "proto" || parts[1] != "services" || parts[3] != "v1" {
		return "", false
	}
	if parts[4] != parts[2]+".proto" {
		return "", false
	}
	return parts[2], true
}

func rescaffoldErr(paths []string, what, fix string) error {
	return cliutil.UserErr(rescaffoldCmd(paths...), what, "", fix)
}

// withStdout runs f with os.Stdout redirected into w.
func withStdout(w io.Writer, f func() error) error {
	orig := os.Stdout
	r, pw, err := os.Pipe()
	if err != nil {
		return f()
	}
	os.Stdout = pw
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(w, r)
		close(done)
	}()
	ferr := f()
	_ = pw.Close()
	<-done
	os.Stdout = orig
	return ferr
}
