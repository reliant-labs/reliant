package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/generator"
)

// `forge project new --in-place --kind app` — adopt an existing app in any
// language onto forge, as it is.
//
// It writes the adopt skill's Tier 1 files, filled with what it reads off
// the app (new_app_detect.go): forge.yaml, deploy/kcl/kcl.mod,
// deploy/kcl/workloads.k (one workload per Dockerfile or compose service it
// builds, plus a pre-rollout migration job) and the dev / staging / prod
// envs (dev runs the compose file, or each workload as a host process;
// staging and prod run on Reliant hosting with a managed Postgres). Nothing
// else: no go.mod, internal/, Taskfile, CI workflow or memory file (a
// harness's agent files only when --harness asks), and none of the app's
// files change, except forge's block appended to .gitignore. Like --kind
// site, `app` is a scaffold choice only: afterwards the project's shape is
// read off its tree (workloads.k and no go.mod is an adopted app).

// projectKindApp is the `--kind app` value.
const projectKindApp = "app"

// newAppDryRun is --dry-run for the `project new` run in progress, and
// newHarnessExplicit whether --harness was passed (an adopted app gets
// agent files only when asked).
var (
	newAppDryRun       bool
	newHarnessExplicit bool
)

// newAppInput carries what runNewApp needs from the `project new` flags.
type newAppInput struct {
	projectName string
	projectPath string
	inPlace     bool
	force       bool
	harness     string
	claimable   bool
	dryRun      bool
}

func runNewApp(in newAppInput) error {
	if !in.inPlace {
		return cliutil.UserErr("forge project new --kind app",
			"--kind app adopts the app already in a directory, so it needs --in-place",
			"",
			"run it inside the app: cd <app> && "+Name()+" project new --in-place --kind app [--name <name>] (--dry-run to see the files first)")
	}
	targetPath, name, err := resolveNewTargetPath(in.projectName, in.projectPath, true, in.force)
	if err != nil {
		return err
	}
	if !hasAppToAdopt(targetPath) {
		hint := "run it in the app's directory (the one with its Dockerfile)"
		if _, ok := siteFrontendAt(targetPath); ok {
			hint = "a Vite or Next.js app with no server is a static site: " + Name() + " project new --in-place --kind site"
		}
		return cliutil.UserErr("forge project new --kind app",
			fmt.Sprintf("%s has no Dockerfile (at its root or in a top-level directory) and no compose service with a build, so there is no image to deploy. Nothing was written", targetPath),
			"",
			hint+"; or add a Dockerfile that builds the app on its own, then run this again")
	}
	det, err := detectApp(targetPath, name, in.claimable)
	if err != nil {
		return cliutil.WrapUserErr("forge project new --kind app", "could not read the app in "+targetPath, "",
			"fix what the error names, or write the files by hand from "+Name()+" skill load adopt", err)
	}
	var harness generator.Harness
	if newHarnessExplicit && in.harness != "" {
		harness, err = generator.ParseHarness(in.harness)
		if err != nil {
			return err
		}
	}

	if in.dryRun {
		files, err := generator.RenderAppScaffold(det.spec, harness)
		if err != nil {
			return err
		}
		fmt.Printf("Dry run: adopting %s as forge project %q would write the files below. Nothing was written.\n\n", targetPath, name)
		printAppDetection(det)
		for _, f := range files {
			fmt.Printf("\n----- %s -----\n%s", f.Path, f.Content)
		}
		fmt.Printf("\n----- .gitignore (appended in a \"# --- forge ---\" block, only the lines it lacks) -----\n%s", generator.AppGitignore)
		fmt.Println()
		printAppNextSteps(det, false)
		return nil
	}

	staging, err := os.MkdirTemp("", "forge-app-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging) //nolint:errcheck // best-effort cleanup of a temp dir
	if err := generator.WriteAppScaffold(staging, det.spec, harness); err != nil {
		return err
	}
	merged, err := mergeScaffoldInto(staging, targetPath, in.force)
	if err != nil {
		return err
	}
	fmt.Printf("✅ App '%s' adopted at %s\n", name, targetPath)
	fmt.Println("   wrote forge.yaml, deploy/kcl/kcl.mod (+ its empty kcl.mod.lock), deploy/kcl/workloads.k, deploy/kcl/{dev,staging,prod}/main.k — no Go, none of the app's files changed")
	fmt.Println()
	printAppDetection(det)
	for _, line := range inPlaceMergeSummary(merged) {
		fmt.Println(line)
	}
	printAppNextSteps(det, true)
	return nil
}

// printAppDetection prints what --kind app found, assumed and left out.
func printAppDetection(det *appDetection) {
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Println(title)
		for _, l := range lines {
			fmt.Println("  - " + l)
		}
	}
	section("Detected:", det.detected)
	dev := "each workload as a host process (its own dev command)"
	if det.spec.ComposeFile != "" {
		dev = det.spec.ComposeFile + ", through docker compose, as before"
	}
	section("Where it runs:", []string{
		"dev: " + dev,
		"staging and prod: Reliant hosting (forge.OnHosted), built once and promoted by digest",
	})
	section("Assumed (each is a `# check:` line in deploy/kcl; review them):", det.assumed)
	section("Left out:", det.skipped)
}

// printAppNextSteps prints appNextSteps.
func printAppNextSteps(det *appDetection, written bool) {
	fmt.Println()
	for _, line := range appNextSteps(det, written) {
		fmt.Println(line)
	}
}

// appNextSteps is what to do with an app --kind app just adopted (or
// would): review, prove the image builds, run it, ship it. Every forge
// command in it resolves in the command tree (TestAppNextStepsArePasteable).
func appNextSteps(det *appDetection, written bool) []string {
	cli := Name()
	lines := []string{"Next steps:"}
	step := func(cmd, why string) { lines = append(lines, "  "+cmd, "      ↳ "+why) }
	if !written {
		step(cli+" project new --in-place --kind app", "write these files")
	}
	lines = append(lines, "  review deploy/kcl/workloads.k and deploy/kcl/<env>/main.k: every `# check:` line is a guess")
	if len(det.dockerignoreMissing) > 0 {
		lines = append(lines, fmt.Sprintf("  add %s to .dockerignore: forge's files stay out of the image, so a KCL edit keeps its digest and dev secrets never ship",
			strings.Join(det.dockerignoreMissing, ", ")))
	}
	lines = append(lines, "  docker build .   (a deploy builds the Dockerfile on its own: it must not COPY anything built on your machine)")
	if det.spec.ComposeFile == "" {
		for _, c := range det.installs {
			lines = append(lines, "  "+c+"   (dev runs the app on the host)")
		}
	}
	step(cli+" env render dev", "what dev runs (deploy/kcl/kcl.mod.lock was written for you: commit it with the rest)")
	step(cli+" env up dev", "run it; logs in .forge/logs/dev/")
	step(cli+" env render staging", "the hosted spec")
	for _, s := range det.secrets {
		if det.spec.Claimable {
			step(cli+" secret set --env staging "+s, "the value on stdin (prod, claimable, takes none until it is claimed)")
			continue
		}
		step(cli+" secret set --env staging "+s, "the value on stdin; again with --env prod")
	}
	step(cli+" env deploy staging --yes", "build, push, cut a release, roll it out (billing on the org; exit 7 prints the link)")
	step(cli+" obs check staging --wait --json", "is the release good? INCONCLUSIVE until the app exports traces (adopt Tier 2)")
	if det.spec.Claimable {
		step(cli+" env deploy prod --claimable", "publish with no account: a live URL and a claim link for your user")
	} else {
		step(cli+" env deploy prod <release>", "the same bytes, promoted by digest")
	}
	step(cli+" skill load adopt", "what each file says, and the next tiers: telemetry, gated promotion, CI")
	return lines
}
