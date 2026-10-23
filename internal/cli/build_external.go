// build_external.go is the SINGLE dispatcher for KCL Services whose
// effective build is a ShellBuild (`build = forge.ShellBuild { cmd, cwd,
// env }`) — the one shell escape hatch. Mirrors the deploytarget/External
// provider on the build side — same `sh -c` shape, same ${X} substitution,
// same fail-when-cwd-missing contract. See internal/buildtarget for the
// runner + spec types.
//
// The dispatcher's responsibilities are deliberately narrow:
//
//   - Iterate KCL services whose effective build is a ShellBuild
//     (EffectiveBuildCmd != "").
//   - Build a Spec from KCL fields + the build-loop's resolved tag /
//     registry / target arch.
//   - Run Spec through buildtarget.Runner.Build.
//   - Persist per-service state when the build succeeded so a
//     subsequent `forge env deploy <env>` can pin the same tag.
//
// Build-side ownership boundary: the user's BuildCmd owns BOTH the
// build AND the push (the user composes
// `docker build … && docker push …` into one string). Forge does NOT
// run docker push afterwards. This matches the External (deploy)
// provider's "user owns the CLI" contract — one mental model across
// both escape hatches.
package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// externalImageDigestResolver resolves the content-addressed manifest digest
// (+ platforms) of a pushed image ref by querying the REGISTRY ONLY
// (imageRepoDigest → `docker buildx imagetools inspect`). This is load-bearing
// for the external / ShellBuild / remote-built path: there the user's
// build_cmd owns build AND push and the image may have been built on a remote
// builder (so the local docker daemon never holds it) OR the local daemon may
// hold a STALE image carrying the same `:tag` from an earlier, never-pushed
// build. A local-daemon RepoDigest read in that situation captures a digest
// that does NOT match what was pushed, and deploy would then pin
// `<image>@<stale-digest>` — shipping the wrong image or one the registry
// doesn't have (ImagePullBackOff, surfacing downstream as a deploy exit 1).
// There is therefore deliberately NO local-cache fallback and NO fall-through
// to a previously captured build-state digest: a registry miss records NO
// digest and deploy falls back to the mutable tag.
//
// It's a package var purely so tests can substitute a deterministic fake
// without shelling out to docker; production code never reassigns it.
var externalImageDigestResolver = imageRepoDigest

// kclHasExternalBuildService reports whether the KCL entity set
// contains any service whose effective build is a ShellBuild. Used by
// runBuild to decide whether to invoke the external-build dispatcher at
// all, and by `--target external` validation to fail loudly when no
// shell-build services are declared.
func kclHasExternalBuildService(e *KCLEntities) bool {
	if e == nil {
		return false
	}
	for _, s := range e.Workloads {
		if s.EffectiveBuildCmd() != "" {
			return true
		}
	}
	return false
}

// kclHasServiceNamed reports whether the rendered env declares a service with
// this exact name.
//
// This is what lets `forge build <env> -t <name>` reach a service that exists
// only in KCL — every sibling-repo / ShellBuild workload. Before it, -t resolved
// against frontends and the project binary alone, so those services were
// unreachable from the build lane even though the flag's help advertised
// "a specific service/frontend name".
func kclHasServiceNamed(e *KCLEntities, name string) bool {
	if e == nil || name == "" {
		return false
	}
	for _, s := range e.Workloads {
		if s.Name == name {
			return true
		}
	}
	return false
}

// externalBuildServices returns the subset of KCL services whose
// effective build is a ShellBuild (EffectiveBuildCmd non-empty).
// Convenience wrapper so the dispatcher loop stays one-liner; also used
// by the `--target external` filter.
//
// Returns nil (not an empty slice) when no shell-build services are
// declared so the parallel/sequential dispatch loops can use the
// idiomatic `len(s) > 0` guard.
func externalBuildServices(e *KCLEntities) []WorkloadEntity {
	if e == nil {
		return nil
	}
	var out []WorkloadEntity
	for _, s := range e.Workloads {
		if s.EffectiveBuildCmd() != "" {
			out = append(out, s)
		}
	}
	return out
}

// buildExternalServices runs every KCL service with a non-empty
// BuildCmd through buildtarget.Runner. Returns one buildResult per
// service so the build-loop summary surfaces them alongside the Go
// binary / docker / frontend results.
//
// Concurrency: distinct artifacts run in parallel when opts.parallel is true,
// sequentially otherwise. Consumers of an identical artifact share ONE command,
// digest lookup and pre-build source capture, then receive per-service state.
// A unit built FROM another (ShellBuild.built_images) waits for it and runs
// with the pushed `<repository>@sha256:<digest>` in its environment — see
// build_external_graph.go; units with no edge between them stay parallel.
//
// Skipped builds (build_cwd missing on disk) are recorded as a
// successful buildResult with kind "external-skip" so the summary
// shows them but they don't trip the failed/succeeded fan-out.
//
// State-file write: per the Phase 2 brief, every successful build
// writes .forge/state/build-<env>-<service>.json. The file is the
// single source of truth a subsequent `forge env deploy <env>` reads to
// pin the image tag — eliminating the build/deploy tag divergence
// the External (deploy) provider already closes for the deploy side.
func buildExternalServices(ctx context.Context, services []WorkloadEntity, opts buildOptions, tag, projectDir string) []buildResult {
	if len(services) == 0 {
		return nil
	}

	// Materialize shared source pointers serially before workers read them.
	for _, svc := range services {
		if err := resolveShellBuildSource(ctx, projectDir, svc.Build.Shell); err != nil {
			return externalBuildFailures(services, err)
		}
	}
	graph, aliases, err := externalBuildSchedule(services, opts, tag, projectDir)
	if err != nil {
		return externalBuildFailures(services, err)
	}
	var base *release.Snapshot
	if opts.composition != nil {
		base = opts.composition.BaseSnapshot
	}
	services = graph.order
	resultCh := make(chan buildResult, len(services))

	// Each unit's outcome, published when its dispatch returns, so a unit
	// built FROM it (built_images) can wait for the exact image it pushed.
	var mu sync.Mutex
	built := map[string]buildResult{}
	done := map[string]chan struct{}{}
	for _, svc := range services {
		done[svc.Name] = make(chan struct{})
	}

	// builtImageEnv waits for every unit svc is built FROM and returns the
	// env overlay naming each one's pushed image. A dependency that failed,
	// was skipped, or pushed nothing forge could resolve to a digest fails
	// svc: building on a mutable tag would make the image's parent
	// unattributable, which is the drift built_images exists to remove.
	builtImageEnv := func(svc WorkloadEntity) (map[string]string, error) {
		env := map[string]string{}
		for _, edge := range graph.edges[svc.Name] {
			if edge.Owner == "" {
				ref, _ := compositionCarriedImage(base, edge.Workload)
				fmt.Printf("[build] %s: %s = %s (carried from the base release's %s)\n", svc.Name, edge.Env, ref, edge.Workload)
				env[edge.Env] = ref
				continue
			}
			fmt.Printf("[build] %s: waiting for %s, which it is built FROM (built_images %s)\n", svc.Name, edge.Workload, edge.Env)
			<-done[edge.Owner]
			mu.Lock()
			dep := built[edge.Owner]
			mu.Unlock()
			switch {
			case dep.err != nil:
				return nil, fmt.Errorf("built FROM %s (built_images %s), whose build failed: %w", edge.Workload, edge.Env, dep.err)
			case dep.kind == "external-skip":
				return nil, fmt.Errorf("built FROM %s (built_images %s), whose build was skipped", edge.Workload, edge.Env)
			case dep.digest == "" || dep.repository == "":
				return nil, fmt.Errorf("built FROM %s (built_images %s), but forge could not resolve the digest of the image it pushed; "+
					"a build FROM it needs an immutable reference, never a tag", edge.Workload, edge.Env)
			}
			ref := dep.repository + "@" + dep.digest
			fmt.Printf("[build] %s: %s = %s (built by %s)\n", svc.Name, edge.Env, ref, edge.Workload)
			env[edge.Env] = ref
		}
		return env, nil
	}

	dispatch := func(svc WorkloadEntity) {
		res := buildExternalUnit(ctx, svc, opts, tag, projectDir, aliases, builtImageEnv)
		mu.Lock()
		built[svc.Name] = res
		mu.Unlock()
		close(done[svc.Name])
		resultCh <- res
	}

	if opts.parallel {
		var wg sync.WaitGroup
		for _, svc := range services {
			wg.Add(1)
			go func(s WorkloadEntity) {
				defer wg.Done()
				dispatch(s)
			}(svc)
		}
		wg.Wait()
	} else {
		// graph.order puts every dependency first, so no wait blocks.
		for _, svc := range services {
			dispatch(svc)
		}
	}
	close(resultCh)
	results := make([]buildResult, 0, len(services))
	for r := range resultCh {
		results = append(results, r)
		name := strings.TrimSuffix(r.name, " (external)")
		for _, alias := range aliases[name] {
			clone := r
			clone.name = alias.Name + " (external)"
			clone.image = alias.Image
			clone.workload = alias.Name
			results = append(results, clone)
		}
	}
	return results
}

// buildExternalUnit runs one ShellBuild unit and records its state. env is
// consulted first for the images the unit is built FROM; it blocks until those
// builds finish.
func buildExternalUnit(ctx context.Context, svc WorkloadEntity, opts buildOptions, tag, projectDir string, aliases map[string][]WorkloadEntity, builtImageEnv func(WorkloadEntity) (map[string]string, error)) buildResult {
	// Per-service tag — see externalBuildTag for the precedence and for
	// why a release build overrides it with the release version.
	svcTag := externalBuildTag(svc, tag, opts)
	spec := buildtarget.Spec{
		Service:    svc.Name,
		Image:      svc.Image,
		Tag:        svcTag,
		ProjectDir: projectDir,
		// The single shell hatch: EffectiveBuildCmd/Cwd/Env all read
		// off the service's effective ShellBuild (build = forge.
		// ShellBuild { cmd, cwd, env }). One source, one contract.
		// The cmd is run verbatim — the render already resolved
		// everything forge contributes.
		BuildCmd:       svc.EffectiveBuildCmd(),
		BuildCwd:       svc.EffectiveBuildCwd(),
		BuildEnv:       svc.EffectiveBuildEnv(),
		RequireReceipt: opts.composition != nil,
	}
	builtEnv, err := builtImageEnv(svc)
	if err != nil {
		return buildResult{name: svc.Name + " (external)", kind: "external", err: err}
	}
	if len(builtEnv) > 0 {
		// A fresh map: the declared env is shared with every consumer of
		// the workload entity and must never be written through.
		merged := make(map[string]string, len(spec.BuildEnv)+len(builtEnv))
		for k, v := range spec.BuildEnv {
			merged[k] = v
		}
		for k, v := range builtEnv {
			merged[k] = v
		}
		spec.BuildEnv = merged
	}
	cwd, err := buildtarget.ResolveCwd(spec)
	if err == nil {
		err = checkBuildStorageFn(cwd)
	}
	if err != nil {
		return buildResult{name: svc.Name + " (external)", kind: "external", err: err}
	}
	if builder := spec.BuildEnv["BUILDX_BUILDER"]; builder != "" {
		registerDockerBuilderStorage(ctx, builder)
	}
	// The checkout the command is about to run in, captured BEFORE it
	// runs: what the command was handed, not whatever it leaves behind
	// (build outputs a sibling's .gitignore misses would read as dirty).
	if opts.composition != nil && svc.Build.InputsFile != "" {
		_, expected, err := buildtarget.ReadInputs(resolveProjectPath(projectDir, svc.Build.InputsFile))
		if err != nil {
			return buildResult{name: svc.Name + " (external)", kind: "external", err: err}
		}
		spec.ExpectedInputDigest = expected
	}
	source := externalSourceCapture(ctx, svc.Name, cwd, projectDir)
	fmt.Printf("[build] %s: ShellBuild (tag %s)%s\n", svc.Name, svcTag, describeShellSource(source))
	for _, alias := range aliases[svc.Name] {
		fmt.Printf("[build] %s: sharing ShellBuild artifact with %s\n", alias.Name, svc.Name)
	}
	res := externalShellBuild(ctx, spec)
	if res.Err == nil && spec.ExpectedInputDigest != "" {
		_, after, err := buildtarget.ReadInputs(resolveProjectPath(projectDir, svc.Build.InputsFile))
		if err != nil {
			res.Err = err
		} else if after != spec.ExpectedInputDigest {
			res.Err = fmt.Errorf("declared build inputs changed during execution")
		}
	}

	// Skip-with-warn: the runner returns Skipped=true when the
	// build_cwd is missing on disk. Surface a clear "skipped: X"
	// log line so the user sees why their build finished early
	// without a failure cluttering the summary.
	if res.Skipped {
		fmt.Printf("[build] %s: skipped (%s)\n", svc.Name, res.SkipMsg)
		return buildResult{name: svc.Name + " (external)", kind: "external-skip", duration: res.Duration}
	}

	// Real failure — return as-is so the build summary's failed
	// list catches it and the outer runBuild returns non-zero.
	if res.Err != nil {
		return buildResult{name: svc.Name + " (external)", kind: "external", duration: res.Duration, err: res.Err}
	}

	// Capture the pushed image's content-addressed digest so deploy can
	// pin `<image>@sha256:...` (immutable, node-cache-proof) instead of the
	// mutable env tag — closing the external-build half of the digest gap:
	// the user's build_cmd owns build AND push, so forge resolves the
	// digest AFTER the command by querying the registry for the exact ref
	// it pushed. Best-effort, same contract as
	// the docker PROJECT path: any lookup failure (local-only ref with no
	// registry manifest — the e2e workspace-base/reliant case — or an
	// unreachable registry) records no digest and deploy falls back to the
	// tag exactly as before. NEVER fails the build.
	pushedRef := externalPushedRef(svc, svcTag, opts.pushPlan.pushBase)
	digest, platforms := "", []string(nil)
	if d, p, derr := externalImageDigestResolver(ctx, pushedRef); derr == nil {
		digest, platforms = d, p
		fmt.Printf("[build] %s: pushed digest %s\n", svc.Name, digest)
	} else {
		fmt.Printf("[build]   Note: could not capture image digest for %s (%v); deploy will use the tag\n", pushedRef, derr)
	}

	// Success path: persist the per-service state file so
	// `forge env deploy <env>` reads the exact tag forge build just
	// pushed. Non-fatal: a failed state write logs a warning but
	// the build itself stays successful (a future deploy will
	// fall back to git-derived tag resolution).
	state := buildtarget.State{
		Service: svc.Name,
		// The RESOLVED repository — the address the cmd pushed and the
		// deploy pulls, which for a bare hosted image is not what the
		// workload declared. See externalPushedRepository.
		Image:     externalPushedRepository(svc, opts.pushPlan.pushBase),
		Tag:       svcTag,
		PushedAt:  nowRFC3339(),
		Digest:    digest,
		Platforms: platforms,
		Source:    source,
	}
	for _, consumer := range append([]WorkloadEntity{svc}, aliases[svc.Name]...) {
		state.Service = consumer.Name
		if werr := buildtarget.WriteState(projectDir, opts.env, state); werr != nil {
			fmt.Printf("[build] %s: warning: failed to write build-state file: %v\n", consumer.Name, werr)
		} else {
			fmt.Printf("[build] %s: wrote build state: %s\n", consumer.Name, buildtarget.StatePath(projectDir, opts.env, consumer.Name))
		}
	}

	// Also write the deploy-side build-<env>.json so a subsequent
	// `forge env deploy <env>` reuses this exact tag without --tag. The
	// per-service file above is consumed only by forge project audit/doctor;
	// deploy's tag-resolution reads THIS single-per-env file
	// (build_state.go::buildStatePath, deploy.go:892). Closing this
	// gap is the external-build half of fr-e6dbce2a01 (build-state
	// was only written on --push, leaving external builds with no
	// deploy-readable tag). Non-fatal — deploy falls back to git
	// describe on a missing file. Last successful service wins; this
	// matches the single-file-per-env shape the --push path uses.
	deployState := BuildState{
		Image: externalPushedRepository(svc, opts.pushPlan.pushBase),
		Tag:   svcTag,
		// The user's build_cmd owns build AND push; we record the
		// registry coordinates but can't prove a push happened, so
		// Pushed stays false (the deploy-side tag read doesn't gate
		// on it — see BuildState.Pushed).
		PushedAt: nowRFC3339(),
		// Carry the digest into the deploy-readable aggregate too — this
		// is the file resolveDeployImageTag actually reads, so without it
		// the per-service capture above would never reach deploy and the
		// reliant/workspace-base images would still pin the mutable tag.
		Digest:    digest,
		Platforms: platforms,
	}
	if werr := WriteBuildState(projectDir, opts.env, deployState); werr != nil {
		fmt.Printf("[build] %s: warning: failed to write deploy build-state file: %v\n", svc.Name, werr)
	} else {
		fmt.Printf("[build] %s: wrote deploy build state: %s\n", svc.Name, buildStatePath(projectDir, opts.env))
	}
	return buildResult{
		name:     svc.Name + " (external)",
		kind:     "external",
		duration: res.Duration,
		err:      nil,
		receipt:  res.Receipt, digest: digest, platforms: platforms, repository: externalPushedRepository(svc, opts.pushPlan.pushBase), image: svc.Image, workload: svc.Name,
	}
}

func externalBuildUnits(services []WorkloadEntity, opts buildOptions, tag, projectDir string) ([]WorkloadEntity, map[string][]WorkloadEntity, error) {
	// Preserve composition's explicit unit validation, then apply the same
	// artifact identity to composed and ordinary builds. A unit name alone
	// cannot authorize different platforms/cwds to write one immutable tag.
	if opts.composition != nil {
		if _, err := compositionBuildUnits(&KCLEntities{Workloads: services}); err != nil {
			return nil, nil, err
		}
	}
	artifacts, err := externalBuildArtifacts(services, opts, tag, projectDir)
	if err != nil {
		return nil, nil, err
	}
	aliases := map[string][]WorkloadEntity{}
	var units []WorkloadEntity
	for _, artifact := range artifacts {
		owner := artifact.services[0]
		units = append(units, owner)
		aliases[owner.Name] = artifact.services[1:]
	}
	return units, aliases, nil
}

func externalBuildFailures(services []WorkloadEntity, err error) []buildResult {
	results := make([]buildResult, 0, len(services))
	for _, svc := range services {
		results = append(results, buildResult{name: svc.Name + " (external)", kind: "external", err: err})
	}
	return results
}

// externalBuildTag is one ShellBuild workload's tag: the shared precedence
// (buildTagFor) over the workload's own pin and the build-wide tag.
//
// The command owns its own push, so this IS the tag written to the registry
// and the one its state records — it was bound as the `image_tag` KCL input
// for the render that produced the command. That is why a release version
// beats even the pin: every ordinary answer is a SHARED tag (prod's
// `stable`, e2e's `e2e`, a pinned `dev-per-daemon`), and handing one to a cut
// moves it the moment that one image finishes. See releaseImageTag.
func externalBuildTag(svc WorkloadEntity, buildTag string, opts buildOptions) string {
	pin, _ := svc.PinnedBuildTag()
	return buildTagFor(opts, pin, buildTag)
}

// externalPushedRef reconstructs the image reference the ShellBuild's command
// pushed, so the post-build digest lookup queries the same manifest and the
// state records the address the deploy will pull.
//
// It is externalPushedRepository plus the tag — one derivation, so the ref
// asked about and the repository recorded cannot name different places.
func externalPushedRef(svc WorkloadEntity, tag, pushBase string) string {
	return externalPushedRepository(svc, pushBase) + ":" + tag
}

// externalPushedRepository is the repository a ShellBuild's command pushed to.
//
// For every runtime whose registry the AUTHOR chooses, that is the declared
// reference's own repository: the host is part of what they wrote, and an
// image naming no host (a local build tagging `<image>:<tag>`, e.g. the e2e
// workspace-base / reliant images) is a local-only ref that resolves no
// registry digest — the best-effort lookup returns empty and deploy stays on
// the tag.
//
// forge.OnHosted is the one runtime where the author does NOT choose it
// (ADR-0003 F1): the control plane admits exactly one registry subtree, so a
// BARE image is the correct, default declaration and forge composes
// `<push base>/<name>` onto it. That resolution happened in the render, which
// is what the ShellBuild's `cmd` was composed from and therefore what it
// pushed — so reading the unresolved `svc.Image` here described a push that
// did not happen. The registry query ran against a hostless name and missed
// every time, and the state recorded the bare name with an empty digest,
// leaving a release cut nothing to pin. Resolving through the same
// resolveHostedImageBase the docker/frontend paths use is what keeps all
// three naming one address.
func externalPushedRepository(svc WorkloadEntity, pushBase string) string {
	repo := imageRepository(svc.Image)
	if svc.Runtime.Type == RuntimeHosted {
		repo = resolveHostedImageBase(pushBase, repo)
	}
	return repo
}
