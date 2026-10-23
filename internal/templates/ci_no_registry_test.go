package templates

import (
	"regexp"
	"strings"
	"testing"
)

// A registry is DECLARED as part of a workload's (or frontend's) `image` in
// the project's KCL, and nowhere else — an environment does not have one. A
// scaffolded workflow therefore never names a registry: it logs in with
// `forge registry login <env>`, builds with `forge env build <env> --push`, and
// reads the pushed ref back with `forge registry ref <env>` — each of which
// reads the images' own references. A registry host in a workflow, a REGISTRY
// env/var, or a --push carrying a value is a second source of truth that
// drifts from the one the deploy pulls from.
var workflowRegistryLeaks = []struct {
	what string
	re   *regexp.Regexp
}{
	{"a registry host literal", regexp.MustCompile(`(?i)\bghcr\.io\b|\bpkg\.dev\b|\bamazonaws\.com\b|\bdocker\.io\b|\blocalhost:50\d\d\b|\bregistry\.localhost\b`)},
	{"REGISTRY as an env/vars key or reference", regexp.MustCompile(`\b[A-Z_]*REGISTRY[A-Z_]*\s*:|\$\{?[A-Z_]*REGISTRY[A-Z_]*\b|(?:env|vars|secrets)\.[A-Z_]*REGISTRY`)},
	{"--push followed by a value", regexp.MustCompile(`--push[= ]+(?:["'$<]|[A-Za-z0-9-]+[.:/])`)},
	{"docker/login-action (it takes the registry as an input)", regexp.MustCompile(`docker/login-action`)},
	{"docker/metadata-action (it takes the image repository as an input)", regexp.MustCompile(`docker/metadata-action`)},
}

func TestScaffoldedWorkflowsNameNoRegistry(t *testing.T) {
	for name, body := range renderedWorkflows(t) {
		for i, line := range strings.Split(string(body), "\n") {
			code := line
			// A comment may explain what the declaration is for (e.g. "the
			// registry each image names"); only a registry
			// VALUE in the workflow is a leak. Comments still may not carry a
			// host literal: an example host in a comment is copied into code.
			if idx := strings.Index(line, "#"); idx >= 0 && strings.TrimSpace(line[:idx]) == "" {
				code = ""
			}
			for _, leak := range workflowRegistryLeaks {
				target := code
				if leak.what == "a registry host literal" {
					target = line
				}
				if m := leak.re.FindString(target); m != "" {
					t.Errorf("%s:%d names %s (%q) — the registry is declared in the env's KCL, never in a workflow:\n    %s",
						name, i+1, leak.what, m, strings.TrimSpace(line))
				}
			}
		}
	}
}

// TestScaffoldedWorkflowsGoThroughForgeForTheRegistry: the three places a
// workflow needs the registry are forge commands that read the declaration.
func TestScaffoldedWorkflowsGoThroughForgeForTheRegistry(t *testing.T) {
	w := renderedWorkflows(t)
	for _, tc := range []struct{ name, want string }{
		{"build-images", `forge registry login "$FORGE_ENV" --username`},
		{"build-images", `forge env build "$FORGE_ENV" --target demo --push --tag "$tag"`},
		{"build-images", `forge registry ref "$FORGE_ENV" --github-output`},
		{"build-images", `image-ref: ${{ needs.build-push.outputs.ref }}`},
		{"build-images", `run: cosign sign --yes "$IMAGE_REF"`},
		{"deploy", `forge registry login "${{ matrix.env }}" --username`},
		{"deploy", `run: forge env build "${{ matrix.env }}" --push` + "\n"},
		// The hosted job's login carries NO credential: forge resolves the
		// control-plane one. The env it needs is the control-plane token,
		// which the job already has for the ledger.
		{"build-images hosted", `run: forge registry login "staging"` + "\n"},
		{"build-images hosted", `FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}`},
	} {
		if !strings.Contains(string(w[tc.name]), tc.want) {
			t.Errorf("%s.yml lacks %q", tc.name, tc.want)
		}
	}
}

// TestScaffoldedWorkflows_HostedRegistryTakesNoCredential is ADR-0003 F3's
// scaffold half: ONE TOKEN means a hosted workflow carries no registry
// credential at all.
//
// A `--username`/`--password-stdin` aimed at the platform registry is the
// defect this pins, and it is not a cosmetic one. forge REFUSES those flags
// for our host, so a scaffolded job that passed them would fail on its first
// run; and before the refusal existed, a job that happened to work taught the
// author that our registry has a password of its own — so the next pipeline
// they wrote carried a second secret to rotate.
func TestScaffoldedWorkflows_HostedRegistryTakesNoCredential(t *testing.T) {
	w := renderedWorkflows(t)
	for _, name := range []string{"build-images hosted", "release", "release mixed"} {
		body := string(w[name])
		if body == "" {
			t.Fatalf("%s rendered nothing", name)
		}
		for _, line := range strings.Split(body, "\n") {
			if !strings.Contains(line, "forge registry login") {
				continue
			}
			// A comment may SHOW the foreign-registry form — release.yml
			// explains it for an env that also pushes somewhere of its own —
			// and that is documentation, not a step.
			if idx := strings.Index(line, "#"); idx >= 0 && strings.TrimSpace(line[:idx]) == "" {
				continue
			}
			for _, flag := range []string{"--username", "--password-stdin", "--password-env"} {
				if strings.Contains(line, flag) {
					t.Errorf("%s logs in to the platform registry with %s — forge refuses that flag for our host, "+
						"which takes the control-plane credential:\n    %s", name, flag, strings.TrimSpace(line))
				}
			}
		}
	}
}

// TestScaffoldedWorkflows_HostedBuildHasNoLoginStep: the build job does not
// log in at ALL, because the push does it. A login step would be a second
// place to be wrong about how the registry is reached.
func TestScaffoldedWorkflows_HostedBuildHasNoLoginStep(t *testing.T) {
	for _, name := range []string{"release", "release mixed"} {
		body := string(renderedWorkflows(t)[name])
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "run:") && strings.Contains(line, "forge registry login") {
				t.Errorf("%s has a registry login STEP; `forge env build --release` authenticates its own push "+
					"(ADR-0003 F3):\n    %s", name, strings.TrimSpace(line))
			}
		}
	}
}
