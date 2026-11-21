package cli

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// `forge fix build-images-vuln-scan`: the mechanical half of the v1.10.0
// playbook migrations/v1.10.0/sbom-vulnerabilities.
//
// From v1.10.0 every release cut records an SBOM per artifact and forge scans
// it with its own pinned grype, so the scaffolded build-images.yml renders the
// `forge vuln db pull` + `forge vuln scan` steps (and the grype database
// cache) for a project pinned there, in place of the anchore SBOM action and
// the Trivy step an older pin keeps (BuildImagesWorkflowData.ForgeAtLeast).
// But build-images.yml is a scaffold-once file: forge writes it at `forge
// project new` and never again, so moving forge_version past v1.10.0 changes
// nothing in it by itself. This codemod is what moves it.
//
// It rewrites only a file that is still forge's, which it proves by bytes:
// the file must be exactly what forge renders for this project from the
// template a pre-v1.10.0 pin got — the v1.6.0–v1.9.0 template (kept below,
// verbatim) or this forge's own pre-v1.10.0 form. CI workflows carry no birth
// hash in the scaffold ledger (checksums.RecordScaffold records none for
// them), so the render is the only evidence there is. Such a file is replaced
// by what `forge generate` writes for the project today. A file someone
// edited is left alone, and the report lists the steps to replace by hand.
//
// It lives here rather than in internal/codemod because the render needs
// every env's KCL (which env the image is built for, whether it is hosted),
// and only this package reads it (ciInputs).

// buildImagesWorkflowPath is the scaffolded workflow the codemod rewrites.
const buildImagesWorkflowPath = ".github/workflows/build-images.yml"

// buildImagesVulnRelease is the first release whose build-images.yml runs
// `forge vuln scan`: the version the template's ForgeAtLeast guard names.
const buildImagesVulnRelease = "v1.10.0"

// preVulnBuildImagesPin is a pin below buildImagesVulnRelease: rendering the
// current template with it yields the form an older pin gets.
const preVulnBuildImagesPin = "v1.9.0"

// buildImagesV190 is build-images.yml.tmpl exactly as forge v1.6.0 through
// v1.9.0 shipped it, the template every project in the upgrade window was
// scaffolded from. It is rendered with the project's data to recognise an
// unedited file, never written.
//
//go:embed upgrade_renders/build-images.yml.v1.9.0.tmpl
var buildImagesV190 embed.FS

// buildImagesCIInputs is ciInputs, as a var so a test states the topology
// without rendering KCL.
var buildImagesCIInputs = ciInputs

// buildImagesUnrendered names the envs whose render failed; a var for the
// same reason.
var buildImagesUnrendered = func(projectDir string) []string { return discoverCIHostedEnvs(projectDir).Unrendered }

func init() {
	codemod.Register(codemod.Codemod{
		Name:    "build-images-vuln-scan",
		Version: buildImagesVulnRelease,
		Short:   "Move the scaffolded build-images.yml from the anchore SBOM action and Trivy to forge vuln scan",
		Long: `Re-render .github/workflows/build-images.yml as forge writes it for a project
pinned to v1.10.0 or later: no anchore/sbom-action steps (every release cut
records an SBOM per artifact itself), and the Trivy step replaced by a cached
grype vulnerability database, ` + "`forge vuln db pull`" + ` and ` + "`forge vuln scan`" + `.

It rewrites the file only while it is still forge's: byte for byte what an
older forge scaffolded for this project. A file you edited is left alone, and
the run lists what to replace by hand. forge.yaml must already pin v1.10.0 or
later, since CI runs the forge it pins; ` + "`forge project upgrade --to v1.10.0`" + `
moves the pin and then runs this. Re-running it changes nothing.

The rest of the story (vuln_gate, forge vuln exempt, vuln.attach_sbom) is the
migration skill: ` + "`forge skill load migrations/v1.10.0/sbom-vulnerabilities`" + `.`,
		Run: runBuildImagesVulnScan,
	})
}

// buildImagesHasPreVulnSteps reports whether a build-images.yml still uses
// either step forge vuln replaced. The playbook's detection asks the same.
func buildImagesHasPreVulnSteps(content []byte) bool {
	return bytes.Contains(content, []byte("anchore/sbom-action")) || bytes.Contains(content, []byte("aquasecurity/trivy-action"))
}

func runBuildImagesVulnScan(dir string, opts codemod.Options) (codemod.Report, error) {
	var rep codemod.Report
	rel := buildImagesWorkflowPath
	path := filepath.Join(dir, filepath.FromSlash(rel))
	existing, err := os.ReadFile(path) //nolint:gosec // the project's own workflow
	if errors.Is(err, fs.ErrNotExist) {
		return rep, nil
	}
	if err != nil {
		return rep, err
	}
	if !buildImagesHasPreVulnSteps(existing) {
		return rep, nil
	}
	handEdit := func(why string) codemod.Report {
		rep.Next = append(rep.Next, rel+" "+why+", so it was left alone. Replace its pre-v1.10.0 steps by hand (migrations/v1.10.0/sbom-vulnerabilities, \"An edited build-images.yml\"):",
			"delete the `Generate SBOM (SPDX JSON)`, `Upload SBOM artifact` and `Attach SBOM to the image (cosign attest)` steps (anchore/sbom-action): every release cut now records an SBOM per artifact",
			"replace the `Run Trivy vulnerability scanner` step (aquasecurity/trivy-action) with the steps forge writes: restore the grype database cache, `forge vuln db pull`, `forge vuln scan --image \"$IMAGE_REF\"`, save the cache",
			fmt.Sprintf("or take forge's whole file over yours: `%s project upgrade --check` shows the diff, `%s project upgrade --force %s` writes it", Name(), Name(), rel))
		return rep
	}

	cfg, err := config.LoadProjectDir(dir)
	if err != nil {
		rep.Next = append(rep.Next, fmt.Sprintf("forge.yaml did not load (%v): fix it, then re-run `%s fix build-images-vuln-scan`", err, Name()))
		return rep, nil
	}
	if !(templates.BuildImagesWorkflowData{ForgeVersion: cfg.ForgeVersion}).ForgeAtLeast(buildImagesVulnRelease) {
		rep.Next = append(rep.Next, fmt.Sprintf("forge.yaml pins forge %q, which has no `forge vuln` (it ships in %s) and is what CI installs: `%s project upgrade --to %s` (or later) moves the pin and then runs this",
			cfg.ForgeVersion, buildImagesVulnRelease, Name(), buildImagesVulnRelease))
		return rep, nil
	}
	if unrendered := buildImagesUnrendered(dir); len(unrendered) > 0 {
		rep.Next = append(rep.Next, fmt.Sprintf("what %s should hold depends on every env's render, and %v did not render: `%s env render %s` says why; fix it and re-run `%s fix build-images-vuln-scan`",
			rel, unrendered, Name(), unrendered[0], Name()))
		return rep, nil
	}
	var row *generator.CIWorkflowFile
	for _, f := range generator.CIWorkflowsFor(dir, cfg, buildImagesCIInputs(dir, cfg)) {
		if f.Dest == rel {
			row = &f
			break
		}
	}
	data, ok := buildImagesData(row)
	if !ok {
		return handEdit("is not a workflow forge renders for this project (no deploy env to build for, ci.workflows switches it off, or ci.provider is not github)"), nil
	}
	want, err := row.Render()
	if err != nil {
		return rep, fmt.Errorf("render %s: %w", rel, err)
	}
	if !buildImagesIsUnedited(existing, *row, data) {
		return handEdit("differs from what forge scaffolded for this project"), nil
	}
	if opts.Write {
		if err := atomicfile.WriteFile(path, want, 0o644); err != nil {
			return rep, err
		}
		checksums.MarkWrittenThisRun(rel)
	}
	rep.Rewrites = append(rep.Rewrites, fmt.Sprintf("%s: anchore/sbom-action and trivy-action → `forge vuln scan` with a cached grype database (forge's render for forge_version %s; the file was unedited)", rel, cfg.ForgeVersion))
	return rep, nil
}

// buildImagesData is the template data of the build-images row, when there
// is one.
func buildImagesData(row *generator.CIWorkflowFile) (templates.BuildImagesWorkflowData, bool) {
	if row == nil {
		return templates.BuildImagesWorkflowData{}, false
	}
	data, ok := row.Data.(templates.BuildImagesWorkflowData)
	return data, ok
}

// buildImagesIsUnedited reports whether existing is, newline style and
// trailing blank lines aside, what a pre-v1.10.0 pin got scaffolded from
// either template: v1.6.0–v1.9.0's, or this forge's own older-pin form.
func buildImagesIsUnedited(existing []byte, row generator.CIWorkflowFile, data templates.BuildImagesWorkflowData) bool {
	got := checksums.BodyHash(existing)
	if old, err := templates.RenderFromFS(buildImagesV190, "upgrade_renders", "build-images.yml.v1.9.0.tmpl", data); err == nil && checksums.BodyHash(old) == got {
		return true
	}
	data.ForgeVersion = preVulnBuildImagesPin
	row.Data = data
	old, err := row.Render()
	return err == nil && checksums.BodyHash(old) == got
}
