package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// shippedSBOMPlaybook is the SBOM playbook as the binary ships it.
func shippedSBOMPlaybook(t *testing.T) migrationMeta {
	t.Helper()
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	for _, m := range metas {
		if m.ID == sbomPlaybookID {
			return m
		}
	}
	t.Fatalf("no shipped playbook %s: a topic playbook with a detection must be listed", sbomPlaybookID)
	return migrationMeta{}
}

// The playbook is a topic playbook one level under its release, and it is
// listed as one: its own ID, the release as its version, its codemod as its
// automation. A sub-skill without a detection (a section of its release's
// playbook) is not.
func TestV1110SBOMPlaybook_IsListedWithItsAutomation(t *testing.T) {
	m := shippedSBOMPlaybook(t)
	if m.Version != "v1.11.0" || m.SkillPath != "migrations/"+sbomPlaybookID {
		t.Errorf("meta = %+v", m)
	}
	if len(m.Automation) != 1 || m.Automation[0] != "fix:build-images-vuln-scan" {
		t.Errorf("automation = %v, want [fix:build-images-vuln-scan]", m.Automation)
	}
	metas, _ := loadMigrationMetas()
	for _, other := range metas {
		if other.ID == "v1.0.0/connect-errors" || other.ID == "v1.0.0/composition-graph" {
			t.Errorf("%s declares no detection and is a section of v1.0.0's playbook, not a playbook of its own", other.ID)
		}
	}
}

// The shipped detection matches a build-images.yml that still runs either
// action, and nothing else.
func TestV1110SBOMPlaybook_Detection(t *testing.T) {
	m := shippedSBOMPlaybook(t)
	for name, tc := range map[string]struct {
		body     string // build-images.yml; "" writes none
		baseline string
		want     bool
	}{
		"no build-images.yml":        {"", "v1.9.0", false},
		"the anchore SBOM action":    {"      - uses: anchore/sbom-action@v0\n", "v1.9.0", true},
		"the Trivy action":           {"        uses: aquasecurity/trivy-action@v0.35.0\n", "v1.9.0", true},
		"forge vuln scan":            {"        run: forge vuln scan --image \"$IMAGE_REF\"\n", "v1.9.0", false},
		"at v1.10.0, the old file":   {"        uses: aquasecurity/trivy-action@v0.35.0\n", "v1.10.0", true},
		"already at v1.11.0":         {"        uses: aquasecurity/trivy-action@v0.35.0\n", "v1.11.0", false},
		"an unknown baseline (none)": {"        uses: aquasecurity/trivy-action@v0.35.0\n", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.body != "" {
				writeBuildImages(t, dir, []byte(tc.body))
			}
			if got := migrationApplies(m, tc.baseline, dir); got != tc.want {
				t.Errorf("applies = %v, want %v", got, tc.want)
			}
		})
	}
}

// `forge project upgrade list` offers the playbook to a project whose
// build-images.yml still runs Trivy: one on v1.9.0, and one that already
// moved to v1.10.0, which was tagged before the playbook and its codemod.
func TestV1110SBOMPlaybook_UpgradeListSurfacesIt(t *testing.T) {
	for _, pin := range []string{"v1.9.0", "v1.10.0"} {
		t.Run(pin, func(t *testing.T) {
			dir := newTestProjectWithVersion(t, pin)
			writeBuildImages(t, dir, []byte("        uses: aquasecurity/trivy-action@v0.35.0\n"))
			var pending []pendingMigration
			var err error
			withCwd(t, dir, func() { pending, err = computePendingMigrations() })
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := writePendingMigrationsHuman(&out, pending, len(pending)); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{sbomPlaybookID, "skill load migrations/" + sbomPlaybookID, "upgrade apply " + sbomPlaybookID} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("upgrade list does not show %q:\n%s", want, out.String())
				}
			}
		})
	}
}

// `forge project upgrade --to v1.11.0` from a v1.10.0 pin crosses both
// playbooks v1.10.0 was tagged without, and each applies where the project
// still has the old shape. An upgrade from v1.10.0 to v1.10.0 crosses neither.
func TestV1110Playbooks_OfferedToAV1100Project(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{sbomPlaybookID, "v1.11.0/pgo-default-on"}
	state := migrationsState{Applied: map[string]string{}}
	always := func(string) bool { return true }
	evals := evaluatePlaybookMetas(metas, "v1.10.0", "v1.11.0", state, always)
	for _, id := range ids {
		var found bool
		for _, ev := range evals {
			if ev.Meta.ID == id {
				found = true
				if !ev.applies {
					t.Errorf("%s does not apply from v1.10.0 to v1.11.0: %s", id, ev.reason)
				}
			}
		}
		if !found {
			t.Errorf("%s is not evaluated from v1.10.0 to v1.11.0: %+v", id, evals)
		}
	}
	for _, ev := range evaluatePlaybookMetas(metas, "v1.11.0", "v1.11.0", state, always) {
		for _, id := range ids {
			if ev.Meta.ID == id {
				t.Errorf("%s is evaluated for a project already at v1.11.0", id)
			}
		}
	}
}

func writeBuildImages(t *testing.T, dir string, body []byte) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(buildImagesWorkflowPath))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildImagesFixture is a service project with a staging and a prod env,
// pinned to pin, whose CI topology is stated (no KCL render): neither env is
// hosted.
func buildImagesFixture(t *testing.T, pin string) string {
	t.Helper()
	dir := newTestProjectWithVersion(t, pin)
	for _, env := range []string{"staging", "prod"} {
		p := filepath.Join(dir, "deploy", "kcl", env, "main.k")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("output = {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prevIn, prevUn := buildImagesCIInputs, buildImagesUnrendered
	buildImagesCIInputs = func(string, *config.ProjectConfig) generator.CIInputs { return generator.CIInputs{} }
	buildImagesUnrendered = func(string) []string { return nil }
	t.Cleanup(func() { buildImagesCIInputs, buildImagesUnrendered = prevIn, prevUn })
	return dir
}

// buildImagesRow is the fixture's build-images row, rendered for pin.
func buildImagesRow(t *testing.T, dir, pin string) (generator.CIWorkflowFile, templates.BuildImagesWorkflowData, []byte) {
	t.Helper()
	cfg, err := config.LoadProjectDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ForgeVersion = pin
	for _, f := range generator.CIWorkflowsFor(dir, cfg, generator.CIInputs{}) {
		if f.Dest == buildImagesWorkflowPath {
			body, err := f.Render()
			if err != nil {
				t.Fatal(err)
			}
			return f, f.Data.(templates.BuildImagesWorkflowData), body
		}
	}
	t.Fatalf("the fixture renders no %s", buildImagesWorkflowPath)
	return generator.CIWorkflowFile{}, templates.BuildImagesWorkflowData{}, nil
}

func runBuildImagesCodemod(t *testing.T, dir string, write bool) codemod.Report {
	t.Helper()
	c, ok := codemod.Lookup("build-images-vuln-scan")
	if !ok {
		t.Fatal("build-images-vuln-scan is not registered")
	}
	rep, err := c.Run(dir, codemod.Options{Write: write})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func readBuildImages(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(buildImagesWorkflowPath)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An unedited build-images.yml, as v1.9.0 or this forge's pre-v1.10.0 form
// scaffolded it, is re-rendered as forge writes it at the v1.10.0 pin; a dry
// run writes nothing, and a second run finds nothing to do.
func TestBuildImagesVulnScan_RewritesAnUneditedScaffold(t *testing.T) {
	for name, old := range map[string]func(t *testing.T, dir string) []byte{
		"the v1.9.0 template": func(t *testing.T, dir string) []byte {
			_, data, _ := buildImagesRow(t, dir, "v1.10.0")
			b, err := templates.RenderFromFS(buildImagesV190, "upgrade_renders", "build-images.yml.v1.9.0.tmpl", data)
			if err != nil {
				t.Fatal(err)
			}
			return b
		},
		"this forge's pre-v1.10.0 form": func(t *testing.T, dir string) []byte {
			_, _, b := buildImagesRow(t, dir, "v1.9.0")
			return b
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := buildImagesFixture(t, "v1.10.0")
			before := old(t, dir)
			if !buildImagesHasPreVulnSteps(before) {
				t.Fatalf("the old render runs neither action:\n%s", before)
			}
			writeBuildImages(t, dir, before)
			_, _, want := buildImagesRow(t, dir, "v1.10.0")

			if rep := runBuildImagesCodemod(t, dir, false); len(rep.Rewrites) != 1 || len(rep.Next) != 0 {
				t.Fatalf("dry run = %+v, want one rewrite", rep)
			}
			if !bytes.Equal(readBuildImages(t, dir), before) {
				t.Fatal("the dry run wrote the file")
			}
			if rep := runBuildImagesCodemod(t, dir, true); len(rep.Rewrites) != 1 {
				t.Fatalf("run = %+v, want one rewrite", rep)
			}
			got := readBuildImages(t, dir)
			if !bytes.Equal(got, want) {
				t.Fatalf("rewritten file is not forge's v1.10.0 render:\n%s", got)
			}
			if !bytes.Contains(got, []byte("forge vuln scan --image")) || buildImagesHasPreVulnSteps(got) {
				t.Errorf("rewritten file still runs an old action, or no forge vuln scan:\n%s", got)
			}
			if rep := runBuildImagesCodemod(t, dir, true); rep.Changed() || len(rep.Next) != 0 {
				t.Errorf("second run = %+v, want nothing", rep)
			}
		})
	}
}

// A file someone edited is left byte for byte, and the report says what to
// replace; so is any file while the pin still predates forge vuln.
func TestBuildImagesVulnScan_LeavesAnEditedFileOrAnOldPinAlone(t *testing.T) {
	t.Run("edited", func(t *testing.T) {
		dir := buildImagesFixture(t, "v1.10.0")
		_, _, old := buildImagesRow(t, dir, "v1.9.0")
		edited := append(append([]byte(nil), old...), []byte("# our own step\n")...)
		writeBuildImages(t, dir, edited)
		rep := runBuildImagesCodemod(t, dir, true)
		if rep.Changed() || !bytes.Equal(readBuildImages(t, dir), edited) {
			t.Fatalf("an edited file was rewritten: %+v", rep)
		}
		next := strings.Join(rep.Next, "\n")
		for _, want := range []string{"left alone", "Generate SBOM (SPDX JSON)", "forge vuln scan", "upgrade --force " + buildImagesWorkflowPath} {
			if !strings.Contains(next, want) {
				t.Errorf("next steps do not mention %q:\n%s", want, next)
			}
		}
	})
	t.Run("pin before v1.10.0", func(t *testing.T) {
		dir := buildImagesFixture(t, "v1.9.0")
		_, _, old := buildImagesRow(t, dir, "v1.9.0")
		writeBuildImages(t, dir, old)
		rep := runBuildImagesCodemod(t, dir, true)
		if rep.Changed() || !bytes.Equal(readBuildImages(t, dir), old) {
			t.Fatalf("rewritten at a v1.9.0 pin, whose CI forge has no forge vuln: %+v", rep)
		}
		if next := strings.Join(rep.Next, "\n"); !strings.Contains(next, "upgrade --to "+sbomPlaybookRelease) {
			t.Errorf("next steps do not say to move the pin:\n%s", next)
		}
	})
	t.Run("no workflow", func(t *testing.T) {
		dir := buildImagesFixture(t, "v1.10.0")
		if rep := runBuildImagesCodemod(t, dir, true); rep.Changed() || len(rep.Next) != 0 {
			t.Errorf("a project with no build-images.yml got %+v", rep)
		}
	})
}

// convergeBuildImagesFixture is a v1.9.0 project with a staging and a prod
// env (a stated render: neither is hosted) and the build-images.yml v1.9.0
// scaffolded for it.
func convergeBuildImagesFixture(t *testing.T) (dir string, want []byte) {
	t.Helper()
	dir = buildImagesFixture(t, "v1.9.0")
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"output":{}}`))
	_, _, old := buildImagesRow(t, dir, "v1.9.0")
	writeBuildImages(t, dir, old)
	_, _, want = buildImagesRow(t, dir, "v1.10.0")
	return dir, want
}

// `forge project upgrade --to v1.11.0` moves the pin and then runs the
// playbook's automation, which re-renders the unedited workflow; the playbook
// then no longer applies.
func TestUpgradeConverge_V1110MovesAnUneditedBuildImagesToForgeVulnScan(t *testing.T) {
	testkit.Slow(t, "runs the whole upgrade converge")
	if runtime.GOOS == "windows" {
		t.Skip("shell fake npm")
	}
	dir, _ := convergeBuildImagesFixture(t)
	_, _, want := buildImagesRow(t, dir, sbomPlaybookRelease)
	fakeUpgradeNPM(t)
	rec := &recordedSteps{}
	rep, err := runConverge(t, dir, upgradeOptions{to: sbomPlaybookRelease, goRun: noGo, steps: rec.steps(),
		metas: []migrationMeta{shippedSBOMPlaybook(t)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readBuildImages(t, dir); !bytes.Equal(got, want) {
		t.Fatalf("build-images.yml is not forge's %s render after the upgrade:\n%s", sbomPlaybookRelease, got)
	}
	var found bool
	for _, p := range rep.Playbooks {
		if p.ID == sbomPlaybookID {
			found = true
			if !p.Applies || !p.Resolved {
				t.Errorf("playbook = %+v, want applies and resolved", p)
			}
		}
	}
	if !found {
		t.Errorf("the upgrade did not run %s: %+v", sbomPlaybookID, rep.Playbooks)
	}
}

// `--force .github/workflows/build-images.yml` in the same run adopts the
// TARGET pin's render, not the one the old pin gets: the scaffold-once rows
// render after the pin the run is moving to.
func TestUpgradeConverge_ForceAdoptsTheTargetPinsBuildImages(t *testing.T) {
	testkit.Slow(t, "runs the whole upgrade converge")
	if runtime.GOOS == "windows" {
		t.Skip("shell fake npm")
	}
	dir, want := convergeBuildImagesFixture(t)
	fakeUpgradeNPM(t)
	rec := &recordedSteps{}
	if _, err := runConverge(t, dir, upgradeOptions{to: "v1.10.0", goRun: noGo, steps: rec.steps(),
		force: true, forcePaths: []string{buildImagesWorkflowPath}, skip: map[string]bool{"playbooks": true}}); err != nil {
		t.Fatal(err)
	}
	if got := readBuildImages(t, dir); !bytes.Equal(got, want) {
		t.Fatalf("--force adopted something other than the v1.10.0 render:\n%s", got)
	}
}
