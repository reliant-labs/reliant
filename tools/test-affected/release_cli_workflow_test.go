package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The signing contract of the standalone CLI (release.yml cli-publish): who signs,
// what the certificate and the predicate say, and what the asset is called. A
// verifier (forge's selfupdate, a user with gh or cosign) pins these, so they are
// held equal to dev-docs/cli-release-artifacts.md, where verifiers read them.
const (
	cliSignerIdentity = `^https://github\.com/reliant-labs/reliant/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`
	cliSignerIssuer   = "https://token.actions.githubusercontent.com"
	cliPredicateType  = "https://github.com/reliant-labs/reliant/attestations/release-assets/v1"
	cliSignatureAsset = "reliant-release.sigstore.json"
)

type cliWorkflow struct {
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		Permissions map[string]string `yaml:"permissions"`
		Env         map[string]string `yaml:"env"`
	} `yaml:"jobs"`
}

func loadCLIWorkflow(t *testing.T) *cliWorkflow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf cliWorkflow
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatalf("parse release.yml: %v", err)
	}
	return &wf
}

// TestReleaseCLIJobs evaluates the job and step conditions: the archives are
// packaged from this run's backend, or taken from the prebuild a tag reuses, never
// both; they are signed and published only from a tag that is not a dry run, after
// Finalize; and nothing is packaged without every backend binary.
func TestReleaseCLIJobs(t *testing.T) {
	wf := loadReleaseWorkflow(t)
	archives, publish := wf.job(t, "cli-archives"), wf.job(t, "cli-publish")
	for _, c := range []struct {
		name                                     string
		prebuilt, backend, finalize, ref, dryRun string
		wantArchives, wantPublish                bool
	}{
		{"a tag builds, packages, signs and publishes", "", "success", "success", "tag", "", true, true},
		{"a tag reusing a prebuild takes its archives and publishes them", "4242", "skipped", "success", "tag", "", true, true},
		{"a failed backend leg packages nothing", "", "failure", "success", "tag", "", false, false},
		{"a prebuild dispatch packages but publishes nothing", "", "success", "success", "branch", "true", true, false},
		{"a dry-run dispatch on a tag publishes nothing", "", "success", "success", "tag", "true", true, false},
		{"a failed Finalize (no Release) publishes nothing", "", "success", "failure", "tag", "", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			results := map[string]string{"prepare": "success", "build-backend": c.backend}
			ctx := map[string]any{
				"needs.prepare.result":               "success",
				"needs.prepare.outputs.prebuilt_run": c.prebuilt,
				"needs.build-backend.result":         c.backend,
				"github.ref_type":                    c.ref,
				"github.event.inputs.dry_run":        c.dryRun,
				"needs.finalize.result":              c.finalize,
			}
			gotArchives := evalJobIf(t, archives, results, ctx)
			if gotArchives != c.wantArchives {
				t.Fatalf("cli-archives runs=%v, want %v", gotArchives, c.wantArchives)
			}
			results["cli-archives"], results["finalize"] = "skipped", c.finalize
			if gotArchives {
				results["cli-archives"] = "success"
				checkCLIArchiveSteps(t, archives, ctx, c.prebuilt != "")
			}
			ctx["needs.cli-archives.result"] = results["cli-archives"]
			if got := evalJobIf(t, publish, results, ctx); got != c.wantPublish {
				t.Errorf("cli-publish runs=%v, want %v", got, c.wantPublish)
			}
		})
	}
}

// checkCLIArchiveSteps: the job packages this run's backend artifacts or downloads
// the prebuild run's cli-archives, never both, and verifies and uploads either way.
func checkCLIArchiveSteps(t *testing.T, j *releaseJob, ctx map[string]any, prebuilt bool) {
	t.Helper()
	var packaged, downloaded, verified, uploaded bool
	for _, s := range j.Steps {
		if !evalStepIf(t, s.If, ctx) {
			continue
		}
		switch {
		case strings.Contains(s.Run, "scripts/release-cli.sh package backend cli-dist"):
			packaged = true
		case strings.HasPrefix(s.Uses, "actions/download-artifact@") && s.With["run-id"] == "${{ needs.prepare.outputs.prebuilt_run }}":
			downloaded = s.With["name"] == "cli-archives"
		case strings.TrimSpace(s.Run) == "bash scripts/release-cli.sh verify cli-dist":
			verified = true
		case strings.HasPrefix(s.Uses, "actions/upload-artifact@") && s.With["name"] == "cli-archives":
			uploaded = true
		}
	}
	if packaged == prebuilt || downloaded != prebuilt || !verified || !uploaded {
		t.Errorf("cli-archives with prebuilt=%v: packaged=%v downloaded-prebuild=%v verified=%v uploaded=%v",
			prebuilt, packaged, downloaded, verified, uploaded)
	}
}

// TestReleaseCLISBOMJob holds the CLI SBOMs to warn-first: cli-sbom never fails
// or gates the release (continue-on-error, no job needs it), describes whatever
// cli-archives packaged, dispatches included, and attaches to the GitHub Release
// only after cli-publish stored the signed set there.
func TestReleaseCLISBOMJob(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var coe struct {
		Jobs map[string]struct {
			ContinueOnError bool `yaml:"continue-on-error"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &coe); err != nil {
		t.Fatalf("parse release.yml: %v", err)
	}
	if !coe.Jobs["cli-sbom"].ContinueOnError {
		t.Error("cli-sbom is not continue-on-error: an SBOM it cannot write would fail the release")
	}
	wf := loadReleaseWorkflow(t)
	for id, j := range wf.Jobs {
		for _, n := range j.needs() {
			if n == "cli-sbom" {
				t.Errorf("job %s needs cli-sbom; nothing may wait on the SBOMs", id)
			}
		}
	}
	sbom := wf.job(t, "cli-sbom")
	ctx := map[string]any{"needs.cli-archives.result": "success"}
	if !evalJobIf(t, sbom, map[string]string{"prepare": "success", "cli-archives": "success", "cli-publish": "skipped"}, ctx) {
		t.Error("cli-sbom does not run when cli-archives packaged and cli-publish was skipped (a dry run)")
	}
	ctx["needs.cli-archives.result"] = "failure"
	if evalJobIf(t, sbom, map[string]string{"prepare": "success", "cli-archives": "failure", "cli-publish": "skipped"}, ctx) {
		t.Error("cli-sbom runs without the CLI archives")
	}
	var described, attached int
	for i, s := range sbom.Steps {
		switch {
		case strings.Contains(s.Run, "scripts/release-cli.sh sbom "):
			described = i
		case strings.Contains(s.Run, "scripts/release-cli.sh store-sbom-github "):
			attached = i
			if !strings.Contains(s.If, "needs.cli-publish.result == 'success'") {
				t.Errorf("the SBOMs are attached when %q; only after cli-publish stored the signed set", s.If)
			}
		}
	}
	if described == 0 || attached <= described {
		t.Errorf("cli-sbom must describe the archives, then attach the SBOMs: steps %d, %d", described, attached)
	}
}

// TestReleaseCLISigning holds cli-publish to the signing contract: it is the one job
// with an OIDC token, it attests the archives, checksums.txt and the manifest with
// the reliant predicate, checks the bundle against the pinned identity before
// anything is published, and the identity, predicate type and asset name are the
// ones dev-docs/cli-release-artifacts.md tells verifiers to pin.
func TestReleaseCLISigning(t *testing.T) {
	perms := loadCLIWorkflow(t)
	if v, ok := perms.Permissions["id-token"]; ok {
		t.Errorf("release.yml grants id-token: %s to every job; only cli-publish may have it", v)
	}
	for id, j := range perms.Jobs {
		if id == "cli-publish" {
			continue
		}
		if _, ok := j.Permissions["id-token"]; ok {
			t.Errorf("job %s has id-token; only cli-publish signs", id)
		}
	}
	pub := perms.Jobs["cli-publish"]
	for k, want := range map[string]string{"contents": "write", "id-token": "write", "attestations": "write"} {
		if pub.Permissions[k] != want {
			t.Errorf("cli-publish permissions[%s] = %q, want %q", k, pub.Permissions[k], want)
		}
	}
	if len(pub.Permissions) != 3 {
		t.Errorf("cli-publish permissions = %v, want exactly contents, id-token and attestations", pub.Permissions)
	}
	for k, want := range map[string]string{
		"SIGNER_IDENTITY": cliSignerIdentity, "SIGNER_ISSUER": cliSignerIssuer, "PREDICATE_TYPE": cliPredicateType, "SIGNATURE_ASSET": cliSignatureAsset,
	} {
		if pub.Env[k] != want {
			t.Errorf("cli-publish env %s = %q, want %q", k, pub.Env[k], want)
		}
	}

	wf := loadReleaseWorkflow(t)
	var attest, verify, recovered, storeGH, storeR2, root int
	for i, s := range wf.job(t, "cli-publish").Steps {
		switch {
		case strings.HasPrefix(s.Uses, "actions/attest@"):
			attest = i
			subjects := s.With["subject-path"]
			for _, want := range []string{"cli-dist/reliant_*.tar.gz", "cli-dist/checksums.txt", "cli-dist/daemon-channels.json"} {
				if !strings.Contains(subjects.(string), want) {
					t.Errorf("actions/attest does not sign %s: subject-path %q", want, subjects)
				}
			}
			if s.With["predicate-type"] != "${{ env.PREDICATE_TYPE }}" {
				t.Errorf("actions/attest predicate-type = %v", s.With["predicate-type"])
			}
			if s.If != "steps.release.outputs.signed == 'false'" {
				t.Errorf("actions/attest runs when %q; it must sign only when no signed set was recovered", s.If)
			}
		case strings.Contains(s.Run, "gh attestation verify"):
			verify = i
			for _, want := range []string{`--cert-identity-regex "$SIGNER_IDENTITY"`, `--cert-oidc-issuer "$SIGNER_ISSUER"`, `--predicate-type "$PREDICATE_TYPE"`, ".release == $release", ".commit == $commit"} {
				if !strings.Contains(s.Run, want) {
					t.Errorf("the signature check does not pin %s", want)
				}
			}
		case strings.Contains(s.Run, "scripts/release-cli.sh recover "):
			recovered = i
		case strings.Contains(s.Run, "scripts/release-cli.sh store-github "):
			storeGH = i
		case strings.Contains(s.Run, "scripts/release-cli.sh store-r2 "):
			storeR2 = i
		case strings.Contains(s.Run, "scripts/release-cli.sh publish-root "):
			root = i
		}
	}
	// Recover before signing; the set durable on the Release, then on R2, before
	// the root manifest changes. Each store runs on a recovered set too.
	order := []int{recovered, attest, verify, storeGH, storeR2, root}
	for k := 1; k < len(order); k++ {
		if order[k-1] == 0 || order[k] <= order[k-1] {
			t.Errorf("cli-publish must recover, sign, check, store on the Release, store on R2, then publish the root manifest: steps %v", order)
			break
		}
	}
	for _, i := range []int{verify, storeGH, storeR2, root} {
		if s := wf.job(t, "cli-publish").Steps[i]; s.If != "" {
			t.Errorf("step %q is conditional (%s); a recovered set must be checked and stored too", s.Name, s.If)
		}
	}

	vp := wf.job(t, "verify-published")
	if !strings.Contains(strings.Join(vp.needs(), " "), "cli-publish") {
		t.Errorf("verify-published needs %v, want cli-publish too", vp.needs())
	}
	var checked bool
	for _, s := range vp.Steps {
		checked = checked || strings.Contains(s.Run, `scripts/release-cli.sh verify-published "$VERSION" "$CHANNEL"`)
	}
	if !checked {
		t.Error("verify-published does not check the CLI assets and the channel manifest")
	}

	doc, err := os.ReadFile(filepath.Join(repoRoot, "dev-docs", "cli-release-artifacts.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{cliSignerIdentity, cliSignerIssuer, cliPredicateType, cliSignatureAsset} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("dev-docs/cli-release-artifacts.md does not state %q, which verifiers pin", want)
		}
	}
}
