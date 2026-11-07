// Tests for `forge project rescaffold` (rescaffold.go).
//
// The defect these pin: every scaffolded workflow said "`forge generate`
// re-creates it only if you delete it first", and that was false. The
// scaffold-once birth ledger (.forge/scaffolded.json) records every file forge
// has written once, so a deletion reads as ownership and generate leaves the
// file gone — while printing "exists — leaving it untouched" about a file that
// is not there. Worse, some scaffold files (e2e.yml, pre-commit.yml,
// .pre-commit-config.yaml) are written only by `forge project new`, so no
// command re-emitted them at all: houndersclub copied them out of a throwaway
// scaffold.
//
// The generate pipeline is stood in for by its CI step, which is the emitter
// that writes .github/workflows — the same function, over the same mapper
// (generator.CIWorkflows). The env config modules that step's hosted-env
// render needs come from the REAL `--steps env-config` preset: the fixture is
// the bare `forge project new` scaffold, before its bootstrap generate, so its
// hosted envs do not render until rescaffold writes them. Everything else a
// rescaffold writes comes from renderers that run in-process, so the whole
// file is pure filesystem work.
//
// Not t.Parallel: the birth ledger's in-memory cache is process-global (see
// newLedgerProject).
package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// scaffoldForRescaffold runs the `forge project new` scaffold lane into a temp
// dir and returns it with its config loaded the way every command loads it.
func scaffoldForRescaffold(t *testing.T, shape func(g *generator.ProjectGenerator)) (string, *config.ProjectConfig) {
	t.Helper()
	dir := t.TempDir()
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)
	g := generator.NewProjectGenerator("demo", dir, "github.com/example/demo")
	if shape != nil {
		shape(g)
	}
	captureStdout(t, func() {
		if err := g.Generate(); err != nil {
			t.Fatalf("scaffold: %v", err)
		}
	})
	return dir, loadAdvisoryConfig(t, dir)
}

// ciStepOnly stands in for the generate pipeline (see the file comment).
func ciStepOnly(root string, cfg *config.ProjectConfig) rescaffoldGenerate {
	return rescaffoldGenerate{
		envConfig: func() error { return runGeneratePipelineFlags(root, pipelineFlags{Steps: envConfigStepPreset}) },
		pipeline:  func() error { return generateCIWorkflows(root, cfg, nil, false) },
	}
}

func readRescaffoldFile(t *testing.T, root, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return b
}

func removeRescaffoldFile(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, rel)); err != nil {
		t.Fatalf("delete %s: %v", rel, err)
	}
}

func rescaffoldFileExists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, rel))
	return err == nil
}

// A deleted workflow stays deleted under `forge generate` — that is the
// ledger doing its job — but generate must SAY so truthfully and name the one
// command that brings it back, and that command must reproduce the scaffold's
// bytes exactly.
func TestRescaffold_DeletedWorkflowComesBackByteIdentical(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root, cfg := scaffoldForRescaffold(t, nil)
	const rel = ".github/workflows/ci.yml"
	born := readRescaffoldFile(t, root, rel)
	removeRescaffoldFile(t, root, rel)

	// Under -v, because the per-path line is routine: it is identical on
	// every run. The EVENT — the moment the file went missing — is
	// reported at default verbosity by reportMissingScaffolds, which names
	// the same command. What must never happen, at any verbosity, is the
	// line claiming the file EXISTS; that is the bug this test was written
	// for and it is asserted below.
	defer setGenerateVerbosity(true)()
	out := captureStdout(t, func() {
		if err := generateCIWorkflows(root, cfg, nil, false); err != nil {
			t.Fatalf("generateCIWorkflows: %v", err)
		}
	})
	if rescaffoldFileExists(root, rel) {
		t.Fatal("generate re-created a scaffold the user deleted — the birth ledger must keep deletions sticky")
	}
	if strings.Contains(out, rel+" exists") {
		t.Errorf("generate reports %s as existing, but it was deleted:\n%s", rel, out)
	}
	if !strings.Contains(out, "project rescaffold "+rel) {
		t.Errorf("generate does not name the command that re-creates %s:\n%s", rel, out)
	}

	var w bytes.Buffer
	if err := rescaffoldPaths(&w, root, cfg, []string{rel}, ciStepOnly(root, cfg)); err != nil {
		t.Fatalf("rescaffold: %v\n%s", err, w.String())
	}
	if !rescaffoldFileExists(root, rel) {
		t.Fatalf("rescaffold did not re-create %s:\n%s", rel, w.String())
	}
	if got := readRescaffoldFile(t, root, rel); !bytes.Equal(got, born) {
		t.Errorf("rescaffolded %s differs from what the scaffold wrote\n--- born ---\n%s\n--- rescaffolded ---\n%s", rel, born, got)
	}
	if !checksums.ScaffoldRecorded(root, rel) {
		t.Errorf("%s is back on disk but not in the birth ledger — a later deletion would be undone by generate", rel)
	}
}

// e2e.yml, pre-commit.yml and .pre-commit-config.yaml are the files houndersclub
// had to copy from a throwaway scaffold: nothing but `forge project new` ever
// wrote them. The devcontainer and bootstrap script are the same class.
func TestRescaffold_ReemitsFilesOnlyProjectNewWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	// A service scaffold carries the generated e2e/ suite, which is what
	// makes e2e.yml part of the project (see generator.CIWorkflows).
	root, cfg := scaffoldForRescaffold(t, func(g *generator.ProjectGenerator) { g.ServiceName = "item" })
	paths := []string{
		".github/workflows/e2e.yml",
		".github/workflows/pre-commit.yml",
		".pre-commit-config.yaml",
		".devcontainer/devcontainer.json",
		"scripts/bootstrap.sh",
	}
	born := map[string][]byte{}
	for _, rel := range paths {
		born[rel] = readRescaffoldFile(t, root, rel)
		removeRescaffoldFile(t, root, rel)
	}

	var w bytes.Buffer
	if err := rescaffoldPaths(&w, root, cfg, paths, ciStepOnly(root, cfg)); err != nil {
		t.Fatalf("rescaffold: %v\n%s", err, w.String())
	}
	for _, rel := range paths {
		if !rescaffoldFileExists(root, rel) {
			t.Errorf("%s was not re-created:\n%s", rel, w.String())
			continue
		}
		if got := readRescaffoldFile(t, root, rel); !bytes.Equal(got, born[rel]) {
			t.Errorf("rescaffolded %s differs from what the scaffold wrote\n--- born ---\n%s\n--- rescaffolded ---\n%s", rel, born[rel], got)
		}
	}
	// Windows has no executable mode bit to restore (os.Stat reports 0666 for
	// every writable file), so the bit is only checked where it exists.
	if info, err := os.Stat(filepath.Join(root, "scripts/bootstrap.sh")); err == nil && runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Errorf("scripts/bootstrap.sh came back without its executable bit (mode %v)", info.Mode().Perm())
	}
}

// A generate run that reached the end with failed steps kept the rest of what
// it wrote, so rescaffold still re-creates every path — the pipeline's and the
// renderers' — and then fails with the failed steps, rather than stopping
// between the two passes or exiting 0.
func TestRescaffold_FinishesThePathsWhenGenerateStepsFailed(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root, cfg := scaffoldForRescaffold(t, nil)
	paths := []string{".github/workflows/ci.yml", "scripts/bootstrap.sh"}
	for _, rel := range paths {
		removeRescaffoldFile(t, root, rel)
	}

	gen := ciStepOnly(root, cfg)
	ciStep := gen.pipeline
	gen.pipeline = func() error {
		if err := ciStep(); err != nil {
			return err
		}
		return &factory.StepsFailedError{Steps: []string{"ORM generation"}}
	}
	var w bytes.Buffer
	err := rescaffoldPaths(&w, root, cfg, paths, gen)
	var stepsFailed *factory.StepsFailedError
	if !errors.As(err, &stepsFailed) {
		t.Fatalf("rescaffold must fail with the generate run's failed steps, got %v\n%s", err, w.String())
	}
	for _, rel := range paths {
		if !rescaffoldFileExists(root, rel) {
			t.Errorf("%s was not re-created:\n%s", rel, w.String())
		}
	}
}

// generator.CIWorkflows decides which workflows a project HAS. A project with
// no e2e suite has no e2e.yml, and rescaffold must not smuggle one in from a
// scaffold-time render — it must say why there is nothing to re-create.
func TestRescaffold_WorkflowTheProjectDoesNotHave(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root, cfg := scaffoldForRescaffold(t, nil)
	const rel = ".github/workflows/e2e.yml"
	if rescaffoldFileExists(root, rel) {
		t.Fatalf("precondition: the default scaffold has no e2e suite, so no %s", rel)
	}

	var w bytes.Buffer
	err := rescaffoldPaths(&w, root, cfg, []string{rel}, ciStepOnly(root, cfg))
	if err == nil {
		t.Fatalf("rescaffold of a workflow this project does not have succeeded:\n%s", w.String())
	}
	if rescaffoldFileExists(root, rel) {
		t.Errorf("rescaffold wrote %s for a project with no e2e suite", rel)
	}
	if !strings.Contains(err.Error(), "e2e") {
		t.Errorf("the refusal must say what the workflow needs, got: %v", err)
	}
}

// Rescaffold restores ABSENT files only. Overwriting a present one would
// discard the user's edits to a file forge promised never to touch.
func TestRescaffold_RefusesAPresentFile(t *testing.T) {
	root, cfg := scaffoldForRescaffold(t, nil)
	const rel = ".pre-commit-config.yaml"
	edited := []byte("# mine\n")
	if err := os.WriteFile(filepath.Join(root, rel), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	err := rescaffoldPaths(&w, root, cfg, []string{rel}, ciStepOnly(root, cfg))
	if err == nil {
		t.Fatalf("rescaffold over a present file succeeded:\n%s", w.String())
	}
	if got := readRescaffoldFile(t, root, rel); !bytes.Equal(got, edited) {
		t.Errorf("rescaffold rewrote a present file the user owns:\n%s", got)
	}
	// The pre-commit config is not upgrade-managed, so pointing at
	// `upgrade --force` would send the user into upgrade's refusal.
	if strings.Contains(err.Error(), "upgrade --force") {
		t.Errorf("remedy names `upgrade --force` for a path upgrade does not manage: %v", err)
	}

	// Taskfile.yml IS upgrade-managed: there the diff-first adopt is the
	// better remedy and must be the one named.
	err = rescaffoldPaths(&w, root, cfg, []string{"Taskfile.yml"}, ciStepOnly(root, cfg))
	if err == nil || !strings.Contains(err.Error(), "upgrade --force Taskfile.yml") {
		t.Errorf("remedy for an upgrade-managed present file should name `upgrade --force`, got: %v", err)
	}
}

// A deleted service proto took the service with it; the refusal names the
// verb that adds a service rather than a generic "not scaffolded".
func TestRescaffold_ServiceProtoNamesScaffoldService(t *testing.T) {
	root, cfg := scaffoldForRescaffold(t, func(g *generator.ProjectGenerator) { g.ServiceName = "item" })
	const rel = "proto/services/item/v1/item.proto"
	removeRescaffoldFile(t, root, rel)
	var w bytes.Buffer
	err := rescaffoldPaths(&w, root, cfg, []string{rel}, ciStepOnly(root, cfg))
	if err == nil || !strings.Contains(err.Error(), "scaffold service item") {
		t.Fatalf("want a refusal naming `scaffold service item`, got: %v", err)
	}
}

// The scaffold writes the hosted pipeline (release.yml + the forge-deploy
// action) for the hosted staging and prod it just wrote. Those envs import
// config modules only `forge generate` writes, so before a generate has
// completed — the state a failed bootstrap generate leaves — they do not
// render, and rescaffold took them for envs that are not hosted: "this
// project has no release.yml ... declares none". It must write the modules
// and look again.
func TestRescaffold_HostedPipelineComesBackBeforeTheFirstGenerate(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root, cfg := scaffoldForRescaffold(t, nil)
	if rescaffoldFileExists(root, "deploy/kcl/config_gen.k") {
		t.Fatal("precondition: the bare scaffold has no env config modules yet")
	}
	paths := []string{".github/workflows/release.yml", generator.ForgeDeployActionPath}
	born := map[string][]byte{}
	for _, rel := range paths {
		born[rel] = readRescaffoldFile(t, root, rel)
		removeRescaffoldFile(t, root, rel)
	}

	var w bytes.Buffer
	if err := rescaffoldPaths(&w, root, cfg, paths, ciStepOnly(root, cfg)); err != nil {
		t.Fatalf("rescaffold: %v\n%s", err, w.String())
	}
	for _, rel := range paths {
		if got := readRescaffoldFile(t, root, rel); !bytes.Equal(got, born[rel]) {
			t.Errorf("rescaffolded %s differs from what the scaffold wrote\n--- born ---\n%s\n--- rescaffolded ---\n%s", rel, born[rel], got)
		}
	}
	if rescaffoldFileExists(root, ".github/workflows/deploy.yml") {
		t.Error("rescaffold wrote a deploy.yml: every env of this scaffold is hosted")
	}
}

// The pipeline's CI step must render the envs as the PIPELINE leaves them.
// Rescaffold asks the CI mapper its question before the pipeline runs, and
// the answer is memoized per project; handed on to the CI step, it wrote CI
// for a tree the pipeline had since changed — on a bare scaffold, a
// build-images.yml for cluster envs and a deploy.yml beside release.yml.
//
// The stand-in is the slice of the real pipeline that matters, in its real
// order: the env config modules, then the CI step. No envConfig hook, so
// the only thing that can make the CI step see the modules is a fresh render.
func TestRescaffold_PipelineCIStepRendersTheTreeThePipelineLeaves(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root, cfg := scaffoldForRescaffold(t, nil)
	const rel = ".github/workflows/build-images.yml"
	born := readRescaffoldFile(t, root, rel)
	removeRescaffoldFile(t, root, rel)

	gen := rescaffoldGenerate{pipeline: func() error {
		cs, err := generator.LoadChecksums(root)
		if err != nil {
			return err
		}
		if err := generatePerEnvDeployConfig(root, cfg, cs, nil); err != nil {
			return err
		}
		return generateCIWorkflows(root, cfg, nil, false)
	}}
	var w bytes.Buffer
	if err := rescaffoldPaths(&w, root, cfg, []string{rel}, gen); err != nil {
		t.Fatalf("rescaffold: %v\n%s", err, w.String())
	}
	if got := readRescaffoldFile(t, root, rel); !bytes.Equal(got, born) {
		t.Errorf("rescaffolded %s was rendered for a topology the pipeline had changed\n--- born ---\n%s\n--- rescaffolded ---\n%s", rel, born, got)
	}
	if rescaffoldFileExists(root, ".github/workflows/deploy.yml") {
		t.Error("the pipeline wrote a deploy.yml for hosted envs: its CI step was handed the pre-pipeline render")
	}
}

// An env that does not render is one forge cannot classify. When whether
// the project has a file hinges on it, the refusal must say so and name the
// way to see why — not claim the project declares no hosted env.
func TestRescaffold_UnrenderableEnvIsNotReportedAsUnhosted(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds and generates a full project; runs in task test")
	}
	root, cfg := scaffoldForRescaffold(t, nil)
	// A project generate HAS run on, so the env config step cannot be
	// what is missing: the envs themselves are broken.
	if err := runGeneratePipelineFlags(root, pipelineFlags{Steps: envConfigStepPreset}); err != nil {
		t.Fatalf("env-config preset: %v", err)
	}
	for _, env := range []string{"staging", "prod"} {
		mainK := "deploy/kcl/" + env + "/main.k"
		src := readRescaffoldFile(t, root, mainK)
		if err := os.WriteFile(filepath.Join(root, mainK), append(src, "\n_broken = undefined_name_mid_edit\n"...), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const rel = ".github/workflows/release.yml"
	removeRescaffoldFile(t, root, rel)

	var w bytes.Buffer
	err := rescaffoldPaths(&w, root, cfg, []string{rel}, ciStepOnly(root, cfg))
	if err == nil {
		t.Fatalf("rescaffold of %s succeeded with no env rendering:\n%s", rel, w.String())
	}
	if rescaffoldFileExists(root, rel) {
		t.Errorf("rescaffold wrote %s without knowing whether the project has it", rel)
	}
	msg := err.Error()
	if strings.Contains(msg, "declares none") {
		t.Errorf("the refusal reports envs that did not render as envs that are not hosted: %v", err)
	}
	for _, want := range []string{"prod, staging", "env render prod"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal should contain %q, got: %v", want, err)
		}
	}
}

// "Can forge re-emit this file?" must be YES for every file the scaffold
// writes — not for a hand-kept list of the ones that were measured missing.
// Each file is deleted alone and rescaffolded, and must come back
// byte-identical.
func TestRescaffold_EveryScaffoldedFileIsReemittable(t *testing.T) {
	if testing.Short() {
		t.Skip("one scaffold render per file; the targeted rescaffold tests above run in -short")
	}
	// Not reproducible by construction, and not a scaffold's content:
	//   .forge/            forge's own state, including the ledger itself
	//   .reliant/project.json  stamps created_at at birth
	//   proto/services/<svc>/v1/<svc>.proto  IS the service — deleting it
	//                      deletes the service; `forge scaffold service`
	//                      re-creates one (TestRescaffold_ServiceProtoNamesScaffoldService)
	skip := func(rel string) bool {
		if _, ok := serviceProtoOf(rel); ok {
			return true
		}
		return strings.HasPrefix(rel, ".forge/") || rel == ".reliant/project.json"
	}
	for name, shape := range map[string]func(*generator.ProjectGenerator){
		"default service": nil,
		"service with frontend": func(g *generator.ProjectGenerator) {
			g.ServiceName = "item"
			g.FrontendName = "web"
		},
		"cli":     func(g *generator.ProjectGenerator) { g.Kind = config.ProjectKindCLI },
		"library": func(g *generator.ProjectGenerator) { g.Kind = config.ProjectKindLibrary },
	} {
		t.Run(name, func(t *testing.T) {
			// Each file's rescaffold asks the CI mapper which envs are hosted
			// and which frontends exist, and that is a KCL evaluation of every
			// env — hundreds of byte-identical evaluations of an unchanged
			// deploy/kcl, 85% of this test's CPU. See memoizeKCLRenders: a
			// file that is itself under deploy/kcl changes the digest and
			// re-evaluates.
			memoizeKCLRenders(t)
			root, cfg := scaffoldForRescaffold(t, shape)
			var files []string
			_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || !d.Type().IsRegular() {
					return err
				}
				rel, _ := filepath.Rel(root, p)
				if rel = filepath.ToSlash(rel); !skip(rel) {
					files = append(files, rel)
				}
				return nil
			})
			sort.Strings(files)
			for _, rel := range files {
				full := filepath.Join(root, rel)
				born := readRescaffoldFile(t, root, rel)
				info, _ := os.Stat(full)
				removeRescaffoldFile(t, root, rel)
				var w bytes.Buffer
				err := rescaffoldPaths(&w, root, cfg, []string{rel}, ciStepOnly(root, cfg))
				got, _ := os.ReadFile(full)
				switch {
				case err != nil:
					t.Errorf("%s: forge cannot re-emit it: %v", rel, err)
				case !bytes.Equal(got, born):
					t.Errorf("%s: rescaffolded bytes differ from the scaffold's", rel)
				}
				// Put the birth bytes back whatever happened, so one gap
				// cannot cascade into the files checked after it.
				_ = os.WriteFile(full, born, info.Mode().Perm())
			}
		})
	}
}

// A batch whose only generate-needing paths are CI files is rendered by the
// CI step alone, never the whole pipeline. The pipeline runs buf, the
// frontend TypeScript plugins and a compile — none of which a workflow is
// rendered from — so rescaffolding .github/workflows/ci.yml failed until the
// TS plugin directory's node_modules had been installed. A batch that also
// names a file only the pipeline writes still runs the pipeline.
func TestRescaffold_CIOnlyBatchRunsTheCIStepNotThePipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("scaffolds a full project; runs in task test")
	}
	root, cfg := scaffoldForRescaffold(t, nil)
	const ci = ".github/workflows/ci.yml"
	born := readRescaffoldFile(t, root, ci)
	removeRescaffoldFile(t, root, ci)

	ciRan, pipelineRan := false, false
	gen := rescaffoldGenerate{
		ci:       func() error { ciRan = true; return generateCIWorkflows(root, cfg, nil, false) },
		pipeline: func() error { pipelineRan = true; return nil },
	}
	var w bytes.Buffer
	if err := rescaffoldPaths(&w, root, cfg, []string{ci}, gen); err != nil {
		t.Fatalf("rescaffold: %v\n%s", err, w.String())
	}
	if !ciRan || pipelineRan {
		t.Fatalf("a CI-only batch ran ci=%v pipeline=%v; want the CI step alone", ciRan, pipelineRan)
	}
	if got := readRescaffoldFile(t, root, ci); !bytes.Equal(got, born) {
		t.Errorf("rescaffolded %s differs from what the scaffold wrote\n--- born ---\n%s\n--- rescaffolded ---\n%s", ci, born, got)
	}

	// The P8 batch: the Taskfile (the upgrade renderer writes it, no
	// generate at all) beside the CI file. Still the CI step alone, and
	// both come back.
	const taskfile = "Taskfile.yml"
	bornTaskfile := readRescaffoldFile(t, root, taskfile)
	removeRescaffoldFile(t, root, ci)
	removeRescaffoldFile(t, root, taskfile)
	ciRan, pipelineRan = false, false
	if err := rescaffoldPaths(&w, root, cfg, []string{ci, taskfile}, gen); err != nil {
		t.Fatalf("rescaffold: %v\n%s", err, w.String())
	}
	if !ciRan || pipelineRan {
		t.Errorf("CI file + Taskfile ran ci=%v pipeline=%v; want the CI step alone", ciRan, pipelineRan)
	}
	if got := readRescaffoldFile(t, root, taskfile); !bytes.Equal(got, bornTaskfile) {
		t.Errorf("rescaffolded %s differs from what the scaffold wrote", taskfile)
	}
}
