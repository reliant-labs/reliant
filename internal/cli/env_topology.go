package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/config"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/topology"
)

// `forge env status` — the whole release/environment picture in ONE read.
//
// WHY A WHOLE-PROJECT COMMAND EXISTS AT ALL. Every other release verb is
// scoped to one env or one version, because every other release verb ACTS on
// one env or one version. This one answers a question no single-env command
// can: "how do the environments relate to each other" — prod is 15 releases
// and ten weeks ahead of staging, and it carries an image staging does not
// have at all. That is a comparison ACROSS envs, and a comparison across envs
// assembled by a client from N single-env calls is a client that has
// re-implemented forge's data model. The rule this project runs on is that the
// UI never computes anything the CLI cannot, so the CLI computes it: one call,
// one document, the entire screen.
//
// VERIFICATION IS OPT-IN, AND THAT IS THE LOAD-BEARING DESIGN DECISION.
// Reading the live cluster for every env needs credentials for every env,
// which almost nobody has locally, and costs a round-trip per workload even
// when they do. So the default reads the LEDGER only: fast, offline, and
// correct about what was DECLARED. `--verify` additionally reads each env's
// cluster through the same path `forge env status` uses.
//
// The consequence for the output contract is the part that must not be gotten
// wrong: in the default mode every image's state is "not_verified", which is
// UNKNOWN — not OK. A consumer painting the ledger-only response must render
// those cells as unknown and fill them in when a --verify pass returns. A
// state model where "not checked" and "checked and matching" share a value
// produces a green screen over an environment nobody looked at, which is the
// exact failure the five-state model in env_verify.go exists to prevent.

// The report's types and its assembly live in forge/pkg/topology, so a
// control plane serving the same picture from its own records runs the same
// code and emits the same document. This file is the CLI's GATHERER: it reads
// a checkout (deploy/kcl, the machine ledger, each env's declared control
// plane, optionally each env's cluster) into a topology.Input and renders the
// topology.Report that comes back.

// topologyStateFor maps a verification verdict into the topology state space.
// Exhaustive by construction: an imageState this switch does not know falls to
// not_verified rather than to a green value.
func topologyStateFor(s imageState) topology.ImageState {
	switch s {
	case imageMatch:
		return topology.Match
	case imageDrift:
		return topology.Drift
	case imageMissing:
		return topology.Missing
	case imageUntagged:
		return topology.Untagged
	case imageUnreachable:
		return topology.Unreachable
	default:
		return topology.NotVerified
	}
}

// envTopologyOptions carries the flags and the injected seams into the run
// function. The three seams are the same ones env verify injects, for the
// same reason: a test of this command's assembly should be able to state the
// ledger, the cluster and the target directly rather than staging a project
// and a live cluster to imply them.
type envTopologyOptions struct {
	// JSON switches the RENDERING only. Every value and the exit code are
	// computed before either renderer runs, so the two modes cannot
	// disagree about what was found.
	JSON bool
	// Verify opts in to reading clusters. Off by default — see the file
	// header for why that is load-bearing rather than a performance tweak.
	Verify  bool
	Timeout time.Duration
	// ProjectDir is the checkout the ledgers and the declared-env scan
	// are read from. The command passes the discovered root; an empty
	// value falls back to projectDirForKCL() so a caller that only cares
	// about the other seams need not resolve it.
	ProjectDir string
	Lister     clusterImageLister
	Resolver   envTargetResolver
	// Ledgers resolves each env's ledger. Nil uses ledgerFor — the env's
	// declared backend. Per ENV, not per report: one project can keep
	// prod's promotions on a control plane and dev's in its own files.
	Ledgers func(ctx context.Context, env string) (envLedger, error)
	// Destinations resolves where each DECLARED env runs (the console
	// contract). Nil renders the env's KCL and, for a hosted env, reads its
	// status from the control plane.
	Destinations func(ctx context.Context, projectDir, env string) envDestination
	// Rollouts reads a hosted env's rollout for a promotion (rollout_phase).
	// Nil uses the env's declared control plane, as `env rollout` does.
	Rollouts func(ctx context.Context, env, promotionID string) (wireRollout, error)
}

// runEnvTopology assembles the report and renders it.
func runEnvTopology(ctx context.Context, envArgs []string, opts envTopologyOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultEnvStatusReleaseTimeout
	}
	if opts.Resolver == nil {
		opts.Resolver = kclTargetResolver{}
	}
	if opts.Lister == nil {
		opts.Lister = kubectlImageLister{}
	}

	projectDir := opts.ProjectDir
	if projectDir == "" {
		projectDir = projectDirForKCL()
	}
	if opts.Ledgers == nil {
		opts.Ledgers = func(ctx context.Context, env string) (envLedger, error) {
			return ledgerFor(ctx, projectDir, env)
		}
	}
	if opts.Destinations == nil {
		opts.Destinations = func(ctx context.Context, projectDir, env string) envDestination {
			e, err := RenderKCL(ctx, projectDir, env)
			if err != nil {
				return envDestination{Note: "could not render this environment to resolve where it runs: " + err.Error()}
			}
			return resolveEnvDestination(ctx, env, e, readHostedStatusFromDeclaration)
		}
	}

	declared := declaredEnvNames(projectDir)
	envs := envArgs
	if len(envs) == 0 {
		envs = declared
	}
	isDeclared := map[string]bool{}
	for _, name := range declared {
		isDeclared[name] = true
	}

	releases := readReleaseLedgers(projectDir)
	report, failure := buildEnvTopology(ctx, projectDir, envs, isDeclared, releases, opts)

	if opts.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return fmt.Errorf("write topology report: %w", err)
		}
		// The report is already on stdout; returning the same sentinel
		// gives cobra the exit code text mode would have produced.
		return failure
	}

	renderEnvTopologyText(report)
	return failure
}

// buildEnvTopology gathers every env and hands the facts to
// topology.Assemble, which decides the verdict ONCE, before either renderer
// runs. That is what makes "--json exits identically to text mode" a
// structural property rather than two switches someone must keep in
// agreement.
func buildEnvTopology(
	ctx context.Context,
	projectDir string,
	envs []string,
	isDeclared map[string]bool,
	releases []release.Release,
	opts envTopologyOptions,
) (topology.Report, error) {
	in := topology.Input{
		Ledger:      machineLedgerLocation(projectDir),
		GeneratedAt: time.Now(),
		Releases:    releases,
		Verified:    opts.Verify,
	}
	if cfg, err := config.LoadProjectDir(projectDir); err == nil && cfg != nil {
		in.Project = cfg.Name
	}

	names := append([]string(nil), envs...)
	sort.Strings(names)
	names = dedupe(names)

	// Every env is independent, so they are gathered in parallel. That
	// matters even without --verify: resolving where an env runs renders
	// its KCL, and doing four of those in series is four times the wait
	// for a screen whose whole promise is that it paints at once.
	in.Envs = make([]topology.EnvInput, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			in.Envs[i] = gatherTopologyEnv(ctx, projectDir, name, isDeclared[name], releases, opts)
		}(i, name)
	}
	wg.Wait()

	report := topology.Assemble(in)
	var failure error
	if code := report.ExitCode(); code != 0 {
		failure = exitCodeError{code: code, msg: report.Detail}
	}
	return report, failure
}

// gatherTopologyEnv reads one environment's facts from the checkout and its
// ledger. It decides nothing topology.Assemble decides: a failed read is
// recorded as a fact, and becomes the row's note there.
func gatherTopologyEnv(
	ctx context.Context,
	projectDir, envName string,
	declared bool,
	releases []release.Release,
	opts envTopologyOptions,
) topology.EnvInput {
	in := topology.EnvInput{Name: envName, Declared: declared}
	if declared {
		in.Lifecycle = declaredLifecycle(ctx, opts.Resolver, projectDir, envName)
	}
	var dest envDestination
	if !declared {
		in.Note = fmt.Sprintf("not declared in this checkout (%s does not exist) — it may be declared on another branch",
			filepath.Join(projectDir, "deploy", "kcl", envName, "main.k"))
	} else if opts.Destinations != nil {
		// Resolved FIRST and independently of the ledger: where an env runs
		// is a fact of its declaration, and a ledger that cannot be read
		// must not also blank the destination badge.
		dest = opts.Destinations(ctx, projectDir, envName)
		in.Destination, in.Endpoint, in.EnvironmentID = dest.Destination, dest.Endpoint, dest.EnvironmentID
		in.ControlPlaneKind = dest.ControlPlaneKind
		in.Verdict, in.Workloads, in.Placements = dest.Verdict, dest.Workloads, dest.Placements
		in.Note = dest.Note
	}

	ledger, err := opts.Ledgers(ctx, envName)
	if err != nil {
		in.LedgerErr = err
		return in
	}
	in.Hosted = ledger.Hosted
	in.Ledger = ledger.Bindings.Location()
	in.Releases = releases
	in.ReleasesLocation = ledger.Releases.Location()
	if ledger.Hosted {
		// A hosted env's releases live on its control plane, not in this
		// checkout, so provenance and lag are measured against THAT set.
		// A failed listing degrades to "provenance unknown", not a blank row.
		if hosted, lerr := ledger.Releases.List(ctx); lerr == nil {
			in.Releases = hosted
		}
	}

	binding, bound, err := ledger.Bindings.Current(ctx, envName)
	if err != nil {
		in.BindingErr = err
		return in
	}
	if !bound {
		return in
	}
	in.Binding = &binding
	if applied := applyOf(projectDir, envName, binding); applied.Failed {
		in.NotApplied, in.ApplyError = true, applied.Summary
	}
	if ledger.Hosted {
		read := opts.Rollouts
		if read == nil {
			read = readDeclaredRollout
		}
		// One read per hosted env, the same GetRollout `env rollout`
		// makes. A failure leaves the phase absent — a dashboard row
		// without a phase is honest; a row that blanked the whole screen
		// because one control plane was slow is not.
		if r, rerr := read(ctx, envName, binding.ID); rerr == nil {
			in.RolloutPhase = rolloutPhaseName(r.Phase)
		} else {
			in.RolloutErr = rerr
		}
	}

	// Where the env runs is resolved declaratively from its KCL. An env
	// not declared in this checkout has no KCL to read, so skip it rather
	// than paying for a render that is certain to fail.
	if declared && in.Destination != destinationHosted {
		target := opts.Resolver.Resolve(ctx, projectDir, envName)
		in.KubeContext = target.KubeContext
		in.Namespace = target.Namespace
	}

	if opts.Verify && len(binding.Resolved) > 0 {
		in.Observed = verifyTopologyEnv(ctx, envName, declared, in.KubeContext, in.Namespace, binding.Resolved, opts)
	}
	return in
}

// verifyTopologyEnv reads one env's cluster and returns a real verdict per
// declared image.
//
// It delegates to verifyEnvImages rather than re-deriving the comparison: the
// five-state model has subtleties (a digest that matches where pinned while
// some workload still runs a mutable tag is UNTAGGED, not MATCH) and a second
// implementation would eventually disagree with `forge env status <env>` about
// the same environment, which is worse than having no second view at all.
func verifyTopologyEnv(
	ctx context.Context,
	envName string,
	declared bool,
	kubeContext, namespace string,
	images map[string]string,
	opts envTopologyOptions,
) map[string]topology.Observation {
	var results []imageVerification
	if kubeContext == "" || namespace == "" {
		// Cannot even address the cluster. UNREACHABLE, not drift:
		// nothing has been learned about what this env is running.
		reason := fmt.Errorf("no forge.K8sCluster.cluster/namespace resolved for %s (a host-only or compose env has no cluster to verify)", envName)
		if !declared {
			reason = fmt.Errorf("environment %s is not declared in this checkout, so there is no KCL to resolve a cluster from", envName)
		}
		results = unreachableVerifications(images, reason)
	} else {
		readCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
		running, lerr := opts.Lister.ListWorkloadImages(readCtx, kubeContext, namespace)
		if lerr != nil {
			results = unreachableVerifications(images, lerr)
		} else {
			results = verifyEnvImages(running, images)
		}
	}

	// An image the verifier did not report on gets no entry, so it stays
	// not_verified.
	out := make(map[string]topology.Observation, len(results))
	for _, r := range results {
		out[r.Image] = topology.Observation{State: topologyStateFor(r.State), Running: r.Running, Detail: r.Detail}
	}
	return out
}

// declaredEnvNames lists the environments declared under deploy/kcl/ — one
// directory per env, each carrying a main.k. This is the same rule
// `forge env list` applies, so the two commands can never disagree about which
// environments a project has.
//
// A missing or unreadable deploy/kcl/ yields an empty list rather than an
// error: a project that declares no environments has an empty topology, which
// is a legitimate (if uninteresting) answer, and failing here would make the
// command unusable in exactly the project where someone is trying to find out
// why they have no environments.
func declaredEnvNames(projectDir string) []string {
	base := filepath.Join(projectDir, "deploy", "kcl")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var envs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(base, e.Name(), "main.k")); statErr == nil {
			envs = append(envs, e.Name())
		}
	}
	sort.Strings(envs)
	return envs
}

// readReleaseLedgers loads this project's releases from the MACHINE ledger,
// NEWEST FIRST.
//
// It reads the non-hosted store because that is where a project's own cut
// releases are recorded; an env whose ledger is a control plane contributes
// its releases through its own ledger, which the per-env loop below reads
// (buildEnvTopology's ledger.Releases.List).
//
// An unreadable ledger is EMPTY, not fatal. This command's job is to show
// the shape of a project's releases; refusing to show any of them because
// the store could not be opened trades a complete answer for no answer.
func readReleaseLedgers(projectDir string) []release.Release {
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return nil
	}
	all, err := store.Releases()
	if err != nil {
		return nil
	}
	topology.SortReleasesNewestFirst(all)
	return all
}

// renderEnvTopologyText prints the human report: a header, one block per
// environment, and the cross-environment image matrix.
func renderEnvTopologyText(report topology.Report) {
	name := report.Project
	if name == "" {
		name = "project"
	}
	fmt.Printf("Release topology for %s\n", name)
	fmt.Printf("  ledger    %s (per-env override: an env declaring forge.ControlPlane records on its control plane)\n", report.Ledger)
	if report.LatestRelease != "" {
		fmt.Printf("  latest    %s (cut %s, %d release(s) on record)\n",
			report.LatestRelease, report.LatestReleaseCreatedAt, len(report.Releases))
	} else {
		fmt.Printf("  latest    (no releases cut in this checkout)\n")
	}
	if !report.Verified {
		// Stated up front, not as a footnote. Every state below is
		// "not verified", and a reader who skims the table without
		// this line has read a claim about clusters that was never made.
		fmt.Printf("  clusters  NOT READ — every image state below is the LEDGER's declaration, not what is running. Add --verify to reconcile.\n")
	}
	fmt.Println()

	if len(report.Environments) == 0 {
		fmt.Println("No environments found. Declare one under deploy/kcl/<env>/main.k, or name one explicitly:")
		fmt.Println("  forge env status staging")
		return
	}

	for _, env := range report.Environments {
		fmt.Printf("%s\n", env.Env)
		if env.Hosted {
			fmt.Printf("  ledger    %s (hosted)\n", env.Ledger)
		}
		switch {
		case !env.Bound:
			fmt.Printf("  release   (unbound)\n")
		default:
			line := fmt.Sprintf("  release   %s", env.Release)
			if env.Lag != nil {
				if env.Lag.Current {
					line += "  [current]"
				} else {
					behind := fmt.Sprintf("%d release(s) behind %s", env.Lag.ReleasesBehind, env.Lag.LatestRelease)
					if env.Lag.Behind != "" {
						behind += ", " + env.Lag.Behind + " older"
					}
					line += "  [" + behind + "]"
				}
			}
			fmt.Println(line)
			if env.NotApplied {
				// Directly under the release it qualifies: a reader who
				// stops at "release" must not walk away believing it runs.
				fmt.Printf("  NOT APPLIED  bound to %s, but its apply FAILED: %s\n", env.Release, oneLine(env.ApplyError, 200))
			}
			// promoted_at is the single most misread field in the
			// ledger, so the label says what it means instead of
			// leaving the reader to assume it is a deploy time.
			fmt.Printf("  promoted  %s (promote time, NOT deploy time)\n", env.PromotedAt)
			if env.ReleaseKnown && env.Git != nil && env.Git.Commit != "" {
				provenance := fmt.Sprintf("  cut from  %s", env.Git.Commit)
				if env.Git.Tag != "" {
					provenance += fmt.Sprintf(" (tag %s)", env.Git.Tag)
				}
				if env.Git.Dirty {
					provenance += "  [DIRTY TREE — these bytes match no reviewable commit]"
				}
				fmt.Println(provenance)
			}
		}
		if env.KubeContext != "" || env.Namespace != "" {
			fmt.Printf("  cluster   %s (namespace %s)\n", env.KubeContext, env.Namespace)
		}
		if env.Note != "" {
			fmt.Printf("  note      %s\n", env.Note)
		}
		for _, img := range env.Images {
			fmt.Printf("    %-12s %-18s %s\n", img.State, img.Image, img.Digest)
			if img.Running != "" && img.State != topology.Match {
				fmt.Printf("      running %s\n", img.Running)
			}
		}
		fmt.Println()
	}

	renderTopologyMatrix(report)

	if report.Verified {
		fmt.Printf("\n%d match, %d drifted, %d missing, %d untagged, %d unreachable\n",
			report.Tally.Match, report.Tally.Drift, report.Tally.Missing,
			report.Tally.Untagged, report.Tally.Unreachable)
	}
	if report.Detail != "" {
		fmt.Printf("\n%s\n", report.Detail)
	}
}

// renderTopologyMatrix prints the image × environment grid.
//
// This is the part that cannot be assembled from per-env output: a blank cell
// means the image is in one environment's release and NOT in another's, and
// that is only visible when every environment is laid against the same column
// set.
func renderTopologyMatrix(report topology.Report) {
	if len(report.Images) == 0 || len(report.Environments) == 0 {
		return
	}
	fmt.Println("Images by environment (· = not in this environment's release)")

	width := 5
	for _, img := range report.Images {
		if len(img) > width {
			width = len(img)
		}
	}

	fmt.Printf("  %-*s", width, "image")
	for _, env := range report.Environments {
		fmt.Printf("  %-14s", env.Env)
	}
	fmt.Println()

	for _, image := range report.Images {
		fmt.Printf("  %-*s", width, image)
		for _, env := range report.Environments {
			cell := "·"
			for _, img := range env.Images {
				if img.Image == image {
					// The short digest is the identity a
					// reader compares across the row: two
					// envs on the same bytes show the same
					// twelve characters.
					cell = matrixDigest(img.Digest)
					break
				}
			}
			fmt.Printf("  %-14s", cell)
		}
		fmt.Println()
	}
}

// matrixDigest abbreviates a digest for a matrix cell.
//
// Distinct from shortDigest (deploy.go) because the two are read differently:
// shortDigest keeps the "sha256:" prefix for a line a reader copies out of,
// while a matrix cell is scanned ACROSS a row to see which environments share
// bytes, and a prefix repeated in every cell is nine characters of noise in a
// column that has to stay narrow. The full digest is in the per-environment
// blocks above and in the JSON, so nothing is lost.
func matrixDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	if d == "" {
		return "?"
	}
	return d
}
