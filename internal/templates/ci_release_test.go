package templates

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The hosted release pipeline, as rendered (ADR docs/adr/env-verbs.md, task V6).
//
// Asserted on the WORKFLOW TEXT — the only thing a user actually gets — and
// parsed as YAML, so a template that renders text GitHub cannot load fails
// here rather than on a user's first tag.

func releaseFixture() ReleaseWorkflowData {
	return ReleaseWorkflowData{
		ProjectName: "demo",
		BuildEnv:    "staging",
		Stages:      ReleaseStages([]DeployEnv{{Name: "staging", Auto: true}, {Name: "preprod"}, {Name: "prod", Protection: true}}, nil),
	}
}

func renderCIText(t *testing.T, tmpl string, data any) string {
	t.Helper()
	out, err := CITemplates("github").Render(tmpl, data)
	if err != nil {
		t.Fatalf("render %s: %v", tmpl, err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("%s does not parse as YAML: %v\n%s", tmpl, err, out)
	}
	return string(out)
}

// One job per hosted env, in promotion order, each with its GitHub
// Environment and its per-env concurrency group — and chained: staging needs
// the build, every later stage needs the stage before.
func TestRelease_OneJobPerHostedEnvInPromotionOrder(t *testing.T) {
	out := renderCIText(t, "release.yml.tmpl", releaseFixture())
	var doc struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]struct {
			Needs       any            `yaml:"needs"`
			Environment string         `yaml:"environment"`
			Concurrency map[string]any `yaml:"concurrency"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.On["push"]; !ok {
		t.Error("release.yml must run on a v* tag push")
	}
	if _, ok := doc.On["workflow_dispatch"]; !ok {
		t.Error("release.yml must be re-drivable by hand")
	}
	wantNeeds := map[string]string{"staging": "build-release", "preprod": "staging", "prod": "preprod"}
	for env, need := range wantNeeds {
		job, ok := doc.Jobs[env]
		if !ok {
			t.Fatalf("no job for hosted env %q:\n%s", env, out)
		}
		if job.Needs != need {
			t.Errorf("%s needs %v, want %s", env, job.Needs, need)
		}
		if job.Environment != env {
			t.Errorf("%s environment = %q", env, job.Environment)
		}
		if job.Concurrency["group"] != "deploy-"+env || job.Concurrency["cancel-in-progress"] != false {
			t.Errorf("%s concurrency = %v, want group deploy-%s, no cancel", env, job.Concurrency, env)
		}
	}
	if doc.Jobs["build-release"].Needs != "checks" {
		t.Errorf("build-release must need checks, got %v", doc.Jobs["build-release"].Needs)
	}
}

// The first stage deploys by VERSION; every later stage deploys --from the
// stage before, pinned to that stage's promotion, with an expect-current the
// PREVIOUS job captured before this one's approval wait.
func TestRelease_LaterStagesDeployFromThePreviousWithCAS(t *testing.T) {
	out := renderCIText(t, "release.yml.tmpl", releaseFixture())
	stagingBlock := between(t, out, "\n  staging:", "\n  preprod:")
	if !strings.Contains(stagingBlock, "version: ${{ env.VERSION }}") || strings.Contains(stagingBlock, "from:") {
		t.Errorf("the first stage deploys by version, not --from:\n%s", stagingBlock)
	}
	// staging captures preprod's current BEFORE preprod's approval wait,
	// through the ONE read verb (`env status --history`, V4).
	if !strings.Contains(stagingBlock, "forge env status preprod --history --limit 1 --json") {
		t.Errorf("staging must capture preprod's current promotion:\n%s", stagingBlock)
	}
	prodBlock := out[strings.Index(out, "\n  prod:"):]
	for _, want := range []string{
		"from: preprod",
		"from-promotion: ${{ needs.preprod.outputs.promotion-id }}",
		"expect-current: ${{ needs.preprod.outputs.next-current }}",
	} {
		if !strings.Contains(prodBlock, want) {
			t.Errorf("the prod job must pass %q:\n%s", want, prodBlock)
		}
	}
	if strings.Contains(prodBlock, "id: capture") {
		t.Error("the LAST stage has no next stage to capture for")
	}
}

// Everything through forge: no curl, no environment ids, no DEPLOY_TOKEN, no
// nonexistent verbs — in the workflow, the action, and the build workflow
// the deleted cut-release job used to live in.
func TestRelease_NoCurlNoIdsNoDeployToken(t *testing.T) {
	texts := map[string]string{
		"release.yml":  renderCIText(t, "release.yml.tmpl", releaseFixture()),
		"forge-deploy": renderCIText(t, "forge-deploy-action.yml.tmpl", releaseFixture()),
		"build-images": renderCIText(t, "build-images.yml.tmpl", BuildImagesWorkflowData{ProjectName: "demo", BuildEnv: "staging", VulnDocker: true}),
	}
	for name, text := range texts {
		for _, banned := range []string{"curl ", "DEPLOY_TOKEN", "DEPLOY_ENVIRONMENT_ID", "forge reconcile", "cut-release"} {
			if strings.Contains(text, banned) {
				t.Errorf("%s contains %q", name, banned)
			}
		}
	}
	if !strings.Contains(texts["release.yml"], "FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}") {
		t.Error("release.yml must read the one token name forge reads")
	}
}

// The suite runs through the Taskfile — the one definition of the tests —
// never a second spelling of `go test` that can drift from it; and the build
// and the release cut are ONE command (`env build <env> --release`, V2), whose
// re-run over the same artifacts is a no-op.
func TestRelease_ChecksThroughTaskAndBuildCutsTheRelease(t *testing.T) {
	out := renderCIText(t, "release.yml.tmpl", releaseFixture())
	checks := between(t, out, "\n  checks:", "\n  build-release:")
	if !strings.Contains(checks, "task test -- -json ./...") {
		t.Errorf("the checks job must run the suite through `task test`:\n%s", checks)
	}
	if strings.Contains(checks, "go test ") {
		t.Errorf("the checks job re-spells `go test`; the Taskfile is the one definition:\n%s", checks)
	}
	if !strings.Contains(out, `forge env build staging --release "$VERSION" --gate-json build.json`) {
		t.Error("build-release must build+push+cut against the first hosted env in one command")
	}
	// --release IMPLIES --push, so spelling it again would suggest the
	// implication is not real.
	if strings.Contains(out, "--release \"$VERSION\" --push") {
		t.Error("--release already implies --push; passing both re-introduces the old two-flag spelling")
	}
}

// A MIXED env (hosted plus a cluster part) gets the kubeconfig, because
// `forge env deploy` applies that half from CI. It needs NO flag to say so:
// the verb reads the env's own declaration (envLedger.Mixed), which is why
// the old `deploy: auto|true` input is gone. A purely hosted stage gets no
// kubeconfig.
func TestRelease_MixedStageGetsTheKubeconfigAndNoDeployFlag(t *testing.T) {
	data := ReleaseWorkflowData{
		ProjectName: "demo",
		BuildEnv:    "staging",
		Stages: ReleaseStages([]DeployEnv{{Name: "staging"}, {Name: "prod", Protection: true}},
			map[string]bool{"prod": true}),
	}
	out := renderCIText(t, "release.yml.tmpl", data)
	staging := between(t, out, "\n  staging:", "\n  prod:")
	prod := out[strings.Index(out, "\n  prod:"):]
	if !strings.Contains(prod, "secrets.KUBECONFIG") {
		t.Errorf("the mixed prod stage must set a kubeconfig:\n%s", prod)
	}
	if strings.Contains(staging, "KUBECONFIG") {
		t.Errorf("a hosted-only stage needs no kubeconfig:\n%s", staging)
	}
	// No stage passes a deploy: input, and the action declares none — who
	// applies is DECLARED in the env, not chosen per pipeline.
	action := renderCIText(t, "forge-deploy-action.yml.tmpl", releaseFixture())
	for name, text := range map[string]string{"release.yml": out, "the action": action} {
		if strings.Contains(text, `deploy: "true"`) || strings.Contains(text, "deploy: auto") {
			t.Errorf("%s still carries a deploy: input; `forge env deploy` decides from the env's declaration", name)
		}
	}
}

// The composite action is ONE deploy command, then evidence, then the
// summary. There is no promote step, no converges-promotions probe and no
// separate wait: `forge env deploy` records, applies and waits itself (V3).
func TestForgeDeployAction_StepsInOrder(t *testing.T) {
	out := renderCIText(t, "forge-deploy-action.yml.tmpl", releaseFixture())
	var doc struct {
		Runs struct {
			Using string `yaml:"using"`
			Steps []struct {
				ID  string `yaml:"id"`
				If  string `yaml:"if"`
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"runs"`
		Outputs map[string]any `yaml:"outputs"`
	}
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Runs.Using != "composite" {
		t.Fatalf("runs.using = %q", doc.Runs.Using)
	}
	order := []string{`forge "${args[@]}" > deploy.json`, "forge gate record", "forge ci summarize"}
	idx := 0
	for _, s := range doc.Runs.Steps {
		if idx < len(order) && strings.Contains(s.Run, order[idx]) {
			idx++
		}
	}
	if idx != len(order) {
		t.Fatalf("steps out of order or missing (matched %d of %v):\n%s", idx, order, out)
	}
	// The three absorbed steps are GONE, not merely unused. A surviving
	// promote/rollout/wait line is a command that now exits non-zero.
	for _, gone := range []string{"env promote", "env rollout", "env wait", "converges_promotions", "rollout.json", "promote.json"} {
		if strings.Contains(out, gone) {
			t.Errorf("the action still references %q; record+apply+wait is one verb now:\n%s", gone, out)
		}
	}
	for _, s := range doc.Runs.Steps {
		if strings.Contains(s.Run, "forge gate record") || strings.Contains(s.Run, "forge ci summarize") {
			if !strings.HasPrefix(s.If, "always()") {
				t.Errorf("evidence and summary must run always(), got if=%q", s.If)
			}
		}
	}
	if _, ok := doc.Outputs["promotion-id"]; !ok {
		t.Error("the action must output promotion-id for the next stage's --from-promotion")
	}
	// The gate attaches to the promotion the deploy WROTE, never to
	// "whatever is current" — a hotfix landing mid-rollout must not receive
	// this run's evidence.
	if !strings.Contains(out, ".recorded.id") {
		t.Error("the promotion id must come from the deploy document's recorded.id")
	}
}

func between(t *testing.T, s, from, to string) string {
	t.Helper()
	i := strings.Index(s, from)
	j := strings.Index(s, to)
	if i < 0 || j < 0 || j < i {
		t.Fatalf("markers %q..%q not found in order", from, to)
	}
	return s[i:j]
}
