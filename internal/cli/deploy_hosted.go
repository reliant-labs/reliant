package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostedDeployClient builds the control-plane client a hosted deploy talks
// through. A var so the CLI e2e test can point it at an httptest server while
// every other step runs for real.
var hostedDeployClient = func(ep cloud.Endpoint, cred cloud.Credential) deploytarget.HostedCaller {
	return cloud.NewClient(ep, cred)
}

// hostedPollInterval is the readiness poll cadence; tests shorten it.
var hostedPollInterval = 3 * time.Second

// dispatchHostedDeploy handles the envs whose deploy is decided before any
// cluster-shaped step runs, and reports whether it did:
//
//   - an env with NOTHING applied from this machine but hosted items (every
//     workload bound to forge.OnHosted, plus hosted databases / static
//     sites) is published by runHostedDeploy alone: no tag resolution, no
//     kubectl, no cluster;
//   - a LOCAL control-plane env (control_plane is only its secret store,
//     nothing hosted) is refused for a direct `forge env deploy`: the
//     platform never deploys to it. `forge env up` (renderToLaunch)
//     continues down the ordinary path.
//
// A MIXED env (hosted items AND a cluster / compose / infra part) returns
// false: runDeploy applies the local part first and then publishes the
// hosted group (publishHostedGroups), in the same deploy. Hosting is per
// workload, never an env mode.
func dispatchHostedDeploy(ctx context.Context, projectDir, envName string, opts deployOptions) (hosted bool, err error) {
	entities, rerr := RenderKCL(ctx, projectDir, envName)
	if rerr != nil || entities == nil {
		return false, nil
	}
	if !entities.HasHosted() {
		if entities.ControlPlane != nil && opts.purpose != renderToLaunch && !envAppliesLocally(entities) {
			return true, refuseLocalEnvDeploy(envName)
		}
		return false, nil
	}
	if envAppliesLocally(entities) {
		return false, nil
	}
	if opts.frontendsOnly {
		return true, fmt.Errorf("--frontends-only is not supported on hosted env %q", envName)
	}
	groups, err := buildDeployGroups(envName, entities, "")
	if err != nil {
		return true, err
	}
	_, hostedGroups := splitHostedGroups(groups)
	return true, opts.shipLog().run(hostedStageLabels(hostedGroups), func() error {
		return runHostedDeploy(ctx, envName, entities, hostedGroups, opts)
	})
}

// deployDeclarationFor is the declaration a hosted deploy carries on its
// EnsureEnvironment: the env's rendered shape and that render's provenance.
//
// IT RIDES THE ENSURE THE DEPLOY ALREADY PERFORMS, rather than being a write
// of its own. The hosted order (see internal/deploytarget/hosted.go) exists
// so nothing is written until everything is known to be admissible, and a
// deploy forge is about to refuse — unbound, off its shape band, an image
// outside the registry the platform admits — must leave the control plane
// untouched. A declaration sent ahead of the plan would be the one write that
// escaped that rule, and three existing tests pin it.
//
// A --dry-run carries none: it previews a deploy, and a preview that wrote to
// the control plane would be the one command that cannot be run safely to
// find out what would happen.
//
// A projection failure is NOT fatal. The deploy's own render has already
// succeeded by this point, so the only way this fails is the purity check —
// and refusing to deploy a project whose KCL writes a file would break a
// working deploy over a declaration it did not ask for. The warning says what
// was not recorded and why.
func deployDeclarationFor(ctx context.Context, envName string, opts deployOptions) (*release.Shape, *release.Provenance) {
	if opts.dryRun {
		return nil, nil
	}
	doc, err := projectEnvShapeFn(ctx, os.Stderr, envName)
	if err != nil {
		fmt.Printf("  Warning: env %s's declaration was not recorded (%v).\n"+
			"           The deploy continues; `forge env shape %s` reproduces this.\n", envName, err, envName)
		return nil, nil
	}
	return &doc.Shape, &doc.Provenance
}

// envAppliesLocally reports whether any part of the env is applied FROM THIS
// MACHINE: a Cluster- or Compose-bound workload, host infra, a cluster
// database, a declared cluster_target (support resources), or a frontend
// shipped or built by forge from here (OnBucket, OnFirebase, BuildOnly).
func envAppliesLocally(e *KCLEntities) bool {
	if len(e.WorkloadsOn(RuntimeCluster)) > 0 || len(e.WorkloadsOn(RuntimeCompose)) > 0 || len(e.Infra) > 0 {
		return true
	}
	if e.ClusterTarget.field("cluster") != "" || len(e.ManifestClusters) > 0 {
		return true
	}
	for _, d := range e.Databases {
		if !d.Hosted() {
			return true
		}
	}
	for _, f := range e.Frontends {
		if f.Runtime.Ships() || f.Runtime.Type == FrontendRuntimeBuildOnly {
			return true
		}
	}
	return false
}

// runHostedDeploy is `forge env deploy <env>` for the HOSTED part of an env:
// it publishes groups (the one hosted group buildDeployGroups built) to the
// env's control plane.
//
// IT TOUCHES NO CLUSTER. There is no ClusterProvider.Ensure, no kubectl
// context guard, no local image build, no preflight against a kubeconfig and
// no manifest apply: the control plane owns the cluster, and forge's job ends
// at publishing admissible specs and confirming they converged.
//
// The order, and why it is this order, is documented on the provider
// (internal/deploytarget/hosted.go). This function only resolves the inputs:
// the endpoint and credential from the env's own declaration, and the release
// binding from the env's ledger — which, for an env with hosted items, is that
// same control plane.
func runHostedDeploy(ctx context.Context, envName string, entities *KCLEntities, groups []deploytarget.ServiceGroup, opts deployOptions) error {
	report := opts.report
	if !opts.dryRun {
		if err := refuseUnsyncableHostedSecretsFor(ctx, envName, entities); err != nil {
			return err
		}
	}
	if len(opts.targets) > 0 {
		// A partial publish would leave the platform running a mix of two
		// releases under one binding — the state the release model exists
		// to rule out.
		return fmt.Errorf("--target is not supported for the hosted workloads of env %q: a hosted deploy publishes them all at the bound release", envName)
	}
	decl := declarationFromEntities(entities)
	ep, err := cloud.ResolveEndpoint(envName, decl)
	if err != nil {
		return err
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return fmt.Errorf("env %q deploys to the control plane at %s: %w", envName, ep.URL, err)
	}
	client := hostedDeployClient(ep, cred)
	// The push base every hosted address below hangs off is composed from the
	// org this credential acts for. Learned once, here, so a deploy that cannot
	// know it fails before it publishes anything rather than composing a base
	// with a hole in it.
	if err := requireOrg(ctx, entities); err != nil {
		return err
	}
	ref := hostedEnvRefFor(envName, entities)

	// The guard, stated for a hosted destination: the declared "context" is
	// the endpoint. A consumer that keys its confirmation on where the bytes
	// land (the reliant console refuses a deploy whose declared context
	// changed between preview and confirm) gets the same guarantee. A MIXED
	// env keeps the cluster guard its local apply recorded.
	if !envAppliesLocally(entities) {
		report.setGuard(deployJSONGuard{
			DeclaredContext: ep.URL,
			Verdict:         deployGuardVerdictAllow,
			Reason:          deployGuardReasonControlPlaneDeclared,
		})
		report.clearKubeContexts()
	}
	envID := ""
	if id, lerr := deploytarget.LookupHostedEnvironment(ctx, client, ref.Project, envName); lerr == nil {
		envID = id
	} else if !errors.Is(lerr, deploytarget.ErrHostedEnvironmentNotFound) {
		return lerr
	}
	report.setHostedTarget(ep.URL, envID)

	ledger := hostedLedger(client, ep.URL, ref.Project, ref.Kind)
	pins, perr := hostedPinsFromLedger(ctx, ledger, envName)
	if perr != nil {
		return fmt.Errorf("%w (%s)", perr, ep.URL)
	}
	release, promotionID, digests, registries := pins.release, pins.promotionID, pins.digests, pins.registries
	if !envAppliesLocally(entities) {
		report.setTags("", "release "+emptyAs(release, "(none)")+" (promoted; "+ep.URL+")", release, false)
	}

	fmt.Printf("Publishing env %s's hosted workloads to the control plane at %s\n", envName, ep.URL)
	if release != "" {
		fmt.Printf("  Release:     %s  (pinning its digests)\n", release)
	}
	fmt.Printf("  Dry run:     %v\n\n", opts.dryRun)

	if len(groups) == 0 {
		fmt.Println("Nothing to publish — the env binds nothing to the control plane.")
		return nil
	}
	shape, declaredBy := deployDeclarationFor(ctx, envName, opts)
	for i := range groups {
		groups[i].DryRun = opts.dryRun
		groups[i].Hosted = &deploytarget.HostedTarget{Endpoint: ep.URL, Project: ref.Project, Release: release,
			PromotionID: promotionID, Digests: digests, Registries: registries,
			PushBase: platformPushBase(entities),
			Shape:    shape, DeclaredBy: declaredBy}
	}

	registry := &deploytarget.Registry{}
	registry.Register(deploytarget.HostedProvider{
		RecordBundle: func(ctx context.Context, _ string) (string, error) {
			return recordHostedBundle(ctx, envName, release, digests, opts)
		},
		Client:        client,
		Rollout:       opts.rollout,
		PollInterval:  hostedPollInterval,
		OnRollout:     report.rolloutObserver(),
		OnEnvironment: func(id string) { report.setHostedTarget(ep.URL, id) },
	})
	start := time.Now()
	// A failed hosted publish is NOT reverted. The ledger still names the
	// release the operator promoted; recovery is roll forward.
	if err := dispatchDeployGroups(ctx, registry, groups); err != nil {
		return err
	}
	if !opts.dryRun {
		fmt.Printf("\nPublish completed in %s.\n", time.Since(start).Truncate(time.Millisecond))
	}
	return nil
}

// releaseRegistries is a release's OCI artifact → registry map: the URI each
// image was pushed to, which the hosted pin uses to locate a backend this
// project built. A nil release (never cut) or an artifact with no URI (built
// without --push) contributes nothing, and the pin refuses such a backend with
// the fix.
func releaseRegistries(rel *release.Release) map[string]string {
	if rel == nil {
		return nil
	}
	out := map[string]string{}
	for name, art := range rel.Artifacts {
		// The artifact's name IS its repository, host included, so the
		// registry is read off the key rather than a parallel URI field that
		// could contradict it. An entry naming no host was never pushed.
		if art.Kind == release.KindOCI && registryHost(name) != "" {
			out[name] = name
		}
	}
	return out
}

// recordHostedBundle builds, pushes and records the hosted env's bundle at the
// bound release. It is the hosted twin of what `forge env build --bundle-envs`
// does for every other env — the SAME writeEnvBundle — and it is the whole of
// what a hosted deploy ships: the control plane's hub Flux applies the
// promoted bundle, so no per-deployment config artifact is published.
//
// A bundle that was not written is an ERROR here, unlike at build time: a
// deploy that recorded nothing would report success for a release the
// platform has no bundle to apply.
func recordHostedBundle(ctx context.Context, envName, bound string, digests map[string]string, opts deployOptions) (string, error) {
	if opts.dryRun {
		return "", nil
	}
	pins := release.BundlePins{Images: map[string]string{}}
	for artifact, digest := range digests {
		pins.Images[artifact] = digest
	}
	out, err := writeBundlesFn(ctx, projectDirForKCL(), []string{envName}, bundleBuildInputs{
		Release: bound,
		Pins:    pins,
		Pushed:  true,
		Now:     time.Now().UTC().Truncate(time.Second),
	})
	if err != nil {
		return "", err
	}
	noteRecordedBundles(bound, out)
	digest := ""
	for _, o := range out {
		if o.Skipped || !o.Recorded {
			return "", fmt.Errorf("hosted env %q: its bundle was not recorded, so the control plane has nothing to apply.\n"+
				"  fix: forge env build %s --release <version> --push && forge env deploy %s <version>", envName, envName, envName)
		}
		fmt.Printf("  bundle %s recorded (%s)\n", shortDigest(o.Digest), o.Reference)
		digest = o.Digest
	}
	return digest, nil
}

// hostedPins is what a promotion froze for a hosted env, in the exact form the
// hosted plan looks it up: the release, the promotion that froze it, each
// artifact's digest, and each artifact's registry.
type hostedPins struct {
	release, promotionID string
	digests, registries  map[string]string
}

// hostedPinsFromLedger reads an env's CURRENT promotion and its release. It is
// the ONE place those are read for a hosted env: the deploy's plan and the
// bundle the deploy ships take their pins from here, so the key the plan looks
// a digest up by and the key the bundle pins under cannot differ. (They did:
// resolveDeployDigests expands ledger keys to repository form for the KCL
// render, and a bundle built from that map found no pin for any workload.)
//
// An unbound env yields zero pins and no error — "nothing promoted" is a
// state, not a failure.
func hostedPinsFromLedger(ctx context.Context, ledger envLedger, envName string) (hostedPins, error) {
	binding, bound, err := ledger.Bindings.Current(ctx, envName)
	if err != nil {
		return hostedPins{}, fmt.Errorf("read the promotion ledger for %q: %w", envName, err)
	}
	if !bound {
		return hostedPins{}, nil
	}
	rel, err := ledger.Releases.Get(ctx, binding.Release)
	if err != nil {
		return hostedPins{}, fmt.Errorf("read release %s: %w", binding.Release, err)
	}
	pins := hostedPins{
		release: binding.Release, promotionID: binding.ID,
		digests: binding.Resolved, registries: releaseRegistries(rel),
	}
	return pins, nil
}

// refuseUnsyncableHostedSecretsFor finds the Secret values a pure-hosted env's
// render needs forge to supply and refuses when it has no way to. Best effort
// on the render: a projection failure is reported by the deploy proper.
func refuseUnsyncableHostedSecretsFor(ctx context.Context, envName string, entities *KCLEntities) error {
	doc, err := projectEnvShapeFn(ctx, io.Discard, envName)
	if err != nil {
		return nil
	}
	refs, err := envSyncedSecretRefs(entities, nil, envName, doc.manifests)
	if err != nil {
		return nil
	}
	return refuseUnsyncableHostedSecrets(envName, refs)
}
