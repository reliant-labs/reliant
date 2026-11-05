package cli

// Writing the bundle: `forge env build <env>` is the only producer (doc §4.4).
//
// WHAT A BUNDLE IS FOR. A release says "these exact image bytes"; it is
// env-agnostic. A bundle says "these exact MANIFEST bytes, for this env, from
// this source" — and that is the half nothing recorded before. The same commit
// renders differently under a different forge, a different config_gen.k, a
// moved sibling checkout, or a dirty tree, so git is not an answer to "what
// was shipped" and neither is the release. The bundle is the render's OUTPUT,
// and the commit is one of its inputs.
//
// WHERE IT GOES, and why that is two answers rather than a flag (doc §4.3):
//
//   - An env whose ledger is a control plane PUSHES to
//     <image_push_base>/bundle.v1/<env>, beside the images it was built with,
//     and records it with RecordBundle — which carries the BYTES, so the
//     server records a shape it verified rather than one forge described.
//   - Every other env — a file-ledger env, and every LOCAL env even when its
//     control plane is reachable — writes into the machine ledger's own OCI
//     layout. A local env's bundle is not pushed because it is not shipped.
//
// The rule is the ledger's, never a flag's: an env's bundle is recorded
// wherever that env's history already lives, so "where is this env's bundle"
// cannot have two answers for one env.
//
// A BUILD THAT PUSHES NOTHING STILL WRITES ONE, to the local layout. That is
// deliberate: the bundle is how a later deploy applies the same bytes it
// recorded, and a build with no push has still produced a render worth
// pinning. What it has NOT produced is pushed images, which is the release's
// problem and is refused there.
//
// FAILING TO RECORD IS NOT FAILING TO BUILD, with one exception. `forge env
// build` is the primitive a CI build job runs and a developer runs offline,
// so an unreachable control plane warns and carries on — the same asymmetry
// recordEnvBuildDeclaration follows, for the same reason. A control plane that
// ANSWERS and says no is different: that is a fact about the env, and a green
// build over it would leave every reader on a bundle that does not exist.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
)

// bundleWriteOutcome is what writing one env's bundle did, so the caller can
// report it without re-deriving any of it.
type bundleWriteOutcome struct {
	Env string
	// Digest is the bundle's identity: its OCI manifest digest.
	Digest string
	// Reference is where the bytes are — a registry reference, or
	// `oci-layout:<dir>@<digest>` for the machine ledger.
	Reference string
	// Pushed reports whether the bytes went to a registry, as opposed to
	// the local OCI layout.
	Pushed bool
	// Recorded reports whether the bundle reached a ledger. False with a
	// nil error is the undeliverable case: the bundle exists, nothing
	// recorded it, and the next command that reaches the control plane
	// will.
	Recorded bool
	// Created is false when the ledger already held this digest for this
	// env — an idempotent retry (F-3), which is what makes a re-push after
	// a lost RecordBundle response free.
	Created bool
	// Objects is how many rendered objects the bundle's shape describes,
	// for the one-line report.
	Objects int
	// Skipped reports that no bundle was written at all, because the env
	// could not be projected. Distinct from "written but not recorded":
	// there is nothing to record and nothing to fetch, and a reader must
	// not show a digest for it.
	Skipped bool
}

// writeBundlesFn is the bundle step as the BUILD calls it.
//
// A seam so a test can prove the build REACHES it. The producer tests below
// call writeEnvBundles directly, which proves the bundle is correct but says
// nothing about whether any command writes one — and "forge env build quietly
// stopped writing bundles" is a regression no producer test can catch.
var writeBundlesFn = writeEnvBundles

// writeEnvBundles writes one bundle per env named, from the pins this build
// resolved.
//
// SEVERAL ENVS FROM ONE CUT is what --bundle-envs is for. A release is
// env-agnostic and a bundle is not, so a pipeline that cuts one release and
// ships it to three envs would otherwise have to re-render each one later,
// at deploy time, from a checkout that may have moved. Rendering them all
// here binds every env's manifests to the same release and the same source.
func writeEnvBundles(ctx context.Context, projectDir string, envs []string, in bundleBuildInputs) ([]bundleWriteOutcome, error) {
	out := make([]bundleWriteOutcome, 0, len(envs))
	for _, env := range envs {
		got, err := writeEnvBundle(ctx, projectDir, env, in)
		if err != nil {
			return out, fmt.Errorf("write env %s's bundle: %w", env, err)
		}
		out = append(out, got)
	}
	return out, nil
}

// bundleBuildInputs is what a bundle needs that only the BUILD knows: which
// release it pins (if any), the digests it resolved, and the CI run that
// produced it.
//
// The provenance and the shape are NOT here: both are projected from the
// render, by projectEnvShapeFn, inside writeEnvBundle. That is the §4.2
// invariant — the shape is computed by the same pass that writes the
// manifests, so the two cannot disagree — and accepting a caller's shape here
// would be exactly the "description beside the bytes" the whole design
// removes.
type bundleBuildInputs struct {
	// Release is the version pinned, "" for an unreleased bundle (a dev
	// build, or a Preview deploy) pinned to the build's own digests.
	Release string
	// Pins is what the render resolved: images by artifact key, and source
	// pins for source-built components.
	Pins release.BundlePins
	// Run is the CI run that built it, or the zero Run when a human did.
	Run release.Run
	// Pushed reports whether this build pushed its images. A build that
	// pushed nothing writes its bundle to the local layout only, whatever
	// the env's ledger is: a registry reference to bytes nobody pushed
	// would be a record of a deploy that cannot be performed.
	Pushed bool
	// Rerecord replaces a release's bundle of record even when this render's
	// objects differ from it. Off — the default — such a render is refused,
	// and a render whose objects match reuses the recorded bundle verbatim.
	Rerecord bool
	// Now is the bundle's creation time for an UNRELEASED bundle only. The
	// caller's, because bundle.Build never reads a clock.
	//
	// A RELEASED bundle does not use it: its creation time is the RELEASE's,
	// read from the env's ledger (bundleCreatedAt), so the digest is a
	// function of the render and the release and nothing that varies between
	// runs. Using a wall clock here made every plan-only run of one render a
	// new digest, a new registry push and a new ledger row, and left "the
	// bundle that was planned" a different object from "the bundle that was
	// promoted".
	Now time.Time
	// OutputDir is the build output directory the metrics schema artifact is
	// read from. "" means bin, the build's default.
	OutputDir string
	// StrictObservability makes a dashboard the project owns that does not
	// validate fail the write. The build and release-cut path sets it; a deploy
	// that builds nothing only warns, so a broken working-tree file cannot
	// block deploying a release that was cut before it was broken.
	StrictObservability bool
	// errOut is where warnings go. nil means stderr.
	errOut io.Writer
}

// writeEnvBundle renders one env, builds its bundle, puts the bytes where that
// env's ledger says they go, and records it.
func writeEnvBundle(ctx context.Context, projectDir, env string, in bundleBuildInputs) (bundleWriteOutcome, error) {
	if in.Now.IsZero() {
		return bundleWriteOutcome{}, fmt.Errorf("%w: a bundle's creation time is the caller's", release.ErrInvalid)
	}
	createdAt, err := bundleCreatedAt(ctx, projectDir, env, in)
	if err != nil {
		return bundleWriteOutcome{}, err
	}

	// The SAME projection `forge env shape` prints and F-DECL records. One
	// function, one render: a shape a user read, a shape the control plane
	// stored and a shape a bundle sealed describe the same bytes or the
	// bundle is a record of a deploy that never happened.
	//
	// A PROJECTION FAILURE IS LOUD BUT NOT FATAL, the same asymmetry
	// recordEnvDeclaration follows and for a reason that is stronger here.
	// The caller asked to BUILD, and an env with no renderable
	// deploy/kcl/<env>/ is a legitimate `forge env build` target — a test
	// project, an env named only on the command line, a project whose KCL
	// this forge cannot render for an unrelated reason. Refusing the build
	// over a record nobody asked for would break a command that has always
	// worked.
	//
	// It is also RECOVERABLE in a way a declaration is not: `forge env
	// deploy` resolves the bundle for (env, release, tree) and renders one
	// on demand when none exists, so a build that could not write one costs
	// a render later rather than losing the deploy. What is NOT tolerated is
	// a bundle that was written and then mis-recorded — see
	// recordWrittenBundle.
	doc, err := projectEnvShapeFn(withHostedPinRelease(ctx, in.Release), in.errWriter(), env)
	if err != nil {
		fmt.Fprintf(in.errWriter(),
			"[bundle] Warning: env %s's bundle was not written: %v\n"+
				"[bundle]   The build continues; `forge env deploy %s` renders one on demand, and "+
				"`forge env shape %s` reproduces this.\n", env, err, env, env)
		return bundleWriteOutcome{Env: env, Skipped: true}, nil
	}

	// A HOSTED BUNDLE IS NEVER SEALED OVER PLACEHOLDER DIGESTS. The hosted
	// records are planned from the bound release's pins; with no release the
	// plan falls back to obviously-fake placeholders (fine for a shape a human
	// reads), and sealing those would ship a Workload nothing can pull.
	if doc.hostedPlaceholder {
		fmt.Fprintf(in.errWriter(),
			"[bundle] Warning: env %s's bundle was not written: its hosted workloads are not pinned by a release.\n"+
				"[bundle]   Cut and promote one (forge env build %s --release <version> --push), then deploy.\n", env, env)
		return bundleWriteOutcome{Env: env, Skipped: true}, nil
	}

	observability, err := bundleObservabilityFiles(projectDir, in, doc.entities)
	if err != nil {
		return bundleWriteOutcome{}, err
	}

	ledger, err := bundleLedgerFor(ctx, projectDir, env)
	if err != nil {
		// #404's rule, applied to the bundle: a build never needs a
		// reachable control plane. Opening a hosted env's ledger needs a
		// credential and a server, and when neither answers there is
		// nowhere to say where the bytes go — so nothing is written, the
		// same outcome as a projection failure, and `forge env deploy`
		// renders one on demand. A control plane that ANSWERS and refuses
		// is a fact about the env and still fails the build.
		if declarationUndeliverable(err) {
			fmt.Fprintf(in.errWriter(),
				"[bundle] Warning: env %s's bundle was not written: %v\n"+
					"[bundle]   The build continues; the next build or deploy that reaches the control plane writes it.\n",
				env, err)
			return bundleWriteOutcome{Env: env, Skipped: true}, nil
		}
		return bundleWriteOutcome{}, err
	}

	// WHAT THE RECORDER DECODES, asked before the document is built: the
	// document is content-addressed, so it is written once, for the backend
	// that will decode it. A control plane strict-decodes it and refuses a
	// field it predates (forge#850's commit_subject did exactly that to every
	// control plane built before it), so a hosted bundle carries only the
	// optional parts its control plane names (release.BundleReader).
	reader, err := bundleReaderFor(ctx, projectDir, env, ledger)
	if err != nil {
		return bundleWriteOutcome{}, err
	}
	// The service catalog is a layer artifact with the same rule: a control
	// plane whose layer validator predates it refuses the WHOLE bundle.
	var withheld bool
	observability, withheld = withholdServiceCatalog(observability, reader)
	if withheld {
		noteServiceCatalogWithheld(in.errWriter(), env)
	}

	placements, err := bundlePlacementDeclarations(doc.entities)
	if err != nil {
		if in.Release == "" && !in.Pushed {
			// Compile-only: an unsupported mixed shape costs the local bundle.
			fmt.Fprintf(in.errWriter(), "[bundle] Warning: env %s's bundle was not written: %v\n", env, err)
			return bundleWriteOutcome{Env: env, Skipped: true}, nil
		}
		return bundleWriteOutcome{}, err
	}
	// A compile-only build publishes nothing, so a recorder that cannot
	// decode mixed prerequisites costs it this local bundle, not the build.
	// Publishing builds were refused before any effect (preflightPlacementBuild).
	if placements != nil && in.Release == "" && !in.Pushed && !reader.Supports(release.BundleFeaturePlacementPrerequisites) {
		fmt.Fprintf(in.errWriter(), "[bundle] Warning: env %s's bundle was not written: its recording control plane does not decode %s.\n",
			env, release.BundleFeaturePlacementPrerequisites)
		return bundleWriteOutcome{Env: env, Skipped: true}, nil
	}
	built, err := bundle.Build(ctx, bundle.BuildInput{
		Placements: placements,
		Project:    doc.Project,
		Env:        env,
		Release:    in.Release,
		Pins:       in.Pins,
		Provenance: doc.Provenance,
		Reader:     reader,
		Shape:      bundleShapeInputOf(doc),
		CreatedAt:  createdAt,
		Units:      doc.units,
		Reconciled: doc.reconciled(),
		// The units the hub converges, for an env that splits them.
		ReconciledUnits: doc.reconciledUnits(),
		Observability:   observability,
	})
	if err != nil {
		return bundleWriteOutcome{}, err
	}

	// ONE BUNDLE PER (ENV, RELEASE). See release_bundle_of_record.go.
	if in.Release != "" && !in.Rerecord {
		prior, found, ferr := findReleaseBundle(ctx, projectDir, env, in.Release, ledger, in)
		switch {
		case ferr != nil:
			fmt.Fprintf(in.errWriter(),
				"[bundle] Note: release %s's recorded bundle for env %s could not be looked up (%v); this render is written unchecked.\n",
				in.Release, env, ferr)
		case found:
			rendered := built.Doc.Shape.Objects
			if prior.fromLedgerRow {
				rendered = doc.Shape.Objects
			}
			if changes := diffBundleObjects(prior.Objects, rendered); len(changes) > 0 {
				return bundleWriteOutcome{}, errReleaseBundleDiffers(env, in.Release, prior, changes)
			}
			// The same objects: reuse the release's bundle, its digest
			// and its record, rather than write a second one that differs
			// only in who rendered it.
			reused := placedBundle{bundleWriteOutcome: bundleWriteOutcome{
				Env: env, Digest: prior.Digest, Reference: prior.Reference,
				Pushed: prior.Pushed, Recorded: true, Objects: len(prior.Objects),
			}, repository: prior.blobs.Repository}
			if len(prior.blobs.Manifest) == 0 {
				return reused.bundleWriteOutcome, nil
			}
			// A hosted bundle is re-sent as ITS OWN bytes: RecordBundle is
			// idempotent on (env, digest), and the server derives the
			// record from what it verifies, so the record is the
			// release's bundle whichever forge sends it.
			return recordWrittenBundle(ctx, projectDir, env, ledger,
				// Shape carries the env's kind, which the control plane
				// needs to ensure the env; the rest of the record is the
				// server's decode of the bytes.
				release.BundleRecord{Env: env, Release: in.Release, Digest: prior.Digest, Reference: prior.Reference, Shape: doc.Shape},
				prior.blobs, reused, in)
		}
	}

	placed, err := placeBundleBytes(ctx, projectDir, env, built, ledger, in)
	if err != nil {
		return bundleWriteOutcome{}, err
	}
	placed.Objects = len(doc.Shape.Objects)

	// A hosted bundle record must name the repository that holds the exact
	// manifest/config bytes the server verifies. If a platform-registry push
	// was unavailable, do not send a local-layout record to the hosted ledger:
	// it cannot be resolved by the control plane and it has no admissible
	// repository to validate.
	if ledger.Hosted && placed.repository == "" {
		if ledger.hubUnits() && in.Pushed {
			return placed.bundleWriteOutcome, errHubBundleNotPushed(env, "it was not pushed to the hosted registry")
		}
		fmt.Fprintf(in.errWriter(),
			"[bundle] Warning: env %s's bundle %s was written locally but not recorded because it was not pushed to the hosted registry.\n"+
				"[bundle]   The build continues; re-run after platform registry access is restored.\n",
			env, shortDigest(placed.Digest))
		return placed.bundleWriteOutcome, nil
	}

	record := release.BundleRecord{
		Env: env, Release: in.Release,
		Digest: built.Digest, Reference: placed.Reference,
		ConfigDigest: built.Doc.ConfigDigest,
		Shape:        doc.Shape, Objects: built.Doc.Objects, Provenance: doc.Provenance,
		Run: in.Run, CreatedAt: createdAt,
	}
	blobs := bundleBlobs{
		Repository: placed.repository,
		Manifest:   built.Manifest,
		Config:     built.ConfigBlob(),
	}
	out, err := recordWrittenBundle(ctx, projectDir, env, ledger, record, blobs, placed, in)
	if err == nil && in.Release != "" && out.Recorded && placed.repository != "" {
		tagReleaseBundle(ctx, projectDir, env, placed.repository, built.Manifest, in.Release, in)
	}
	return out, err
}

// bundleCreatedAt is the creation time sealed into a bundle: the RELEASE's, so
// the same render of the same release is the same bytes on every run. An
// unreleased bundle has no release to borrow a time from and keeps the caller's
// clock; that is the only case where two runs may differ, and it is also the
// case nobody promotes or plans against.
func bundleCreatedAt(ctx context.Context, projectDir, env string, in bundleBuildInputs) (time.Time, error) {
	if in.Release == "" {
		return in.Now, nil
	}
	ledger, err := ledgerFor(ctx, projectDir, env)
	if err != nil {
		if declarationUndeliverable(err) {
			return in.Now, nil
		}
		return time.Time{}, fmt.Errorf("resolve release %s's timestamp: %w", in.Release, err)
	}
	rel, err := ledger.Releases.Get(ctx, in.Release)
	if err != nil {
		return time.Time{}, fmt.Errorf("read release %s to date its bundle: %w", in.Release, err)
	}
	if rel == nil || rel.CreatedAt.IsZero() {
		return in.Now, nil
	}
	return rel.CreatedAt.UTC(), nil
}

// bundleShapeInputOf re-states the declaration half of the projection for
// bundle.Build, which runs the OBJECT half itself from the stream it packages.
//
// Build deliberately does not accept a finished shape: it projects the objects
// from the same parse it writes the manifest layer from, which is what makes
// "the shape describes these manifests" true by construction rather than by a
// caller's discipline. So the declaration fields are passed through and the
// objects are not.
func bundleShapeInputOf(doc envShapeDoc) bundle.ShapeInput {
	return bundle.ShapeInput{
		Kind:          doc.Shape.Kind,
		Workloads:     doc.Shape.Workloads,
		Secrets:       doc.Shape.Secrets,
		Domains:       doc.Shape.Domains,
		Clusters:      doc.Shape.Clusters,
		Manifests:     doc.manifests,
		SyncedSecrets: doc.syncedSecrets,
		Images:        doc.images,

		ControlPlaneSecrets: doc.Shape.ControlPlaneSecrets,
		Alerts:              doc.Shape.Alerts,
		AlertRoutes:         doc.Shape.AlertRoutes,
		ObservabilityPolicy: doc.Shape.ObservabilityPolicy,
		FrontendTelemetry:   doc.Shape.FrontendTelemetry,
		DeliveryPolicy:      doc.Shape.DeliveryPolicy,
		DataAccess:          doc.Shape.DataAccess,
		ReleaseHealth:       doc.Shape.ReleaseHealth,
		UptimeChecks:        doc.Shape.UptimeChecks,
		Identity:            doc.Shape.Identity,
		Databases:           doc.Shape.Databases,
		Applier:             doc.Shape.Applier,
		Units:               doc.Shape.Units,
		Store: release.StoreProjection{
			StoreSecrets: doc.Shape.StoreSecrets, GeneratedSecrets: doc.Shape.GeneratedSecrets,
			OpenBao: doc.Shape.OpenBao, ExternalSecretsOperators: doc.Shape.ExternalSecretsOperators,
		},
	}
}

// NO CHART GOES IN THE BUNDLE, because nothing could consume one there.
//
// A bundle used to carry `helm template` output for the env's declared
// forge.HelmChart platform deps, in a SECOND OCI layer. That layer was
// unreachable by construction: a Flux OCIRepository selects exactly ONE layer
// by media type, so whatever rode in a second one was recorded as shipped and
// never applied — the worst available outcome, because the record would say
// the cert-manager install went out while no cluster ever received it.
//
// They are not folded into the manifest layer instead, for two reasons that
// survive forge keeping its own cluster apply:
//
//   - A chart is a PLATFORM DEPENDENCY, by its own declaration. The real ones
//     are substrate every other object rests on — the CNPG operator that owns
//     each Postgres Cluster, Envoy Gateway with the Gateway API CRDs every
//     HTTPRoute needs, cert-manager. They are installed deliberately, at a
//     moment someone chose, not swept along by whichever app deploy happened
//     to run.
//   - The bundle's consumer PRUNES what its path no longer carries. Anything
//     the bundle owns can be removed by a render that merely failed to select
//     it, and for an operator or a CRD that means orphaning every custom
//     resource defined by it. A platform dependency must not be deletable by
//     omission.
//
// So platform infra stays on the path that already installs it, where it is
// applied by a person naming it:
//
//	forge env deploy <env> --target <chart>
//
// which renders and applies that chart against the cluster
// (resolveDeployHelmSpecs -> cluster.Apply), scoped to the one chart, with
// nothing pruning from it. That path is unchanged by this.
//
// `forge env shape` already left charts out, for its own reason — a chart's
// objects are a dependency's, not this project's declaration — so the shape,
// which is what every reader indexes, is unaffected either way.

// bundlePushTarget opens the registry a bundle is pushed to.
//
// A var so a test can push into an in-process oras target instead of standing
// up a registry and a credential helper. The thing under test here is the
// ORDER and the PLACEMENT — render, build, push to the env's bundle
// repository, record the reference that push produced — and none of that is a
// property of the network. internal/bundle's own tests cover Push against a
// real oras store, including the blobs-before-manifest rule.
//
// It is given the project and env, not just the reference, because the
// credential is the ENV's: its declared control plane, resolved in memory
// (bundle_registry_auth.go). The docker credential store is not consulted for
// the platform registry.
var bundlePushTarget = func(ctx context.Context, projectDir, env, reference string) (bundle.Pusher, error) {
	return openBundleRepository(ctx, projectDir, env, reference)
}

// pushBundleTo opens the repository and pushes, returning the digest.
func pushBundleTo(ctx context.Context, projectDir, env, repo string, built bundle.Bundle) (string, error) {
	target, err := bundlePushTarget(ctx, projectDir, env, repo)
	if err != nil {
		return "", err
	}
	return bundle.Push(ctx, target, built)
}

// placedBundle is where a bundle's bytes ended up.
type placedBundle struct {
	bundleWriteOutcome
	// repository is the push destination, for RecordBundle's own
	// `repository` field. Empty for a local layout, which the hosted path
	// never sees.
	repository string
}

// placeBundleBytes pushes or writes the bundle, by the env's ledger.
//
// THE DECISION IS THE LEDGER'S, with one override: a build that pushed no
// images never pushes its bundle either. Recording a registry reference for
// bytes that are not in that registry is F-4's shape — resolvable,
// recordable, unfetchable — manufactured by the writer rather than by a GC
// bug.
func placeBundleBytes(ctx context.Context, projectDir, env string, built bundle.Bundle, ledger envLedger, in bundleBuildInputs) (placedBundle, error) {
	if ledger.Hosted && in.Pushed {
		return pushHostedBundle(ctx, projectDir, env, built, ledger, in)
	}
	return writeBundleLocally(ctx, projectDir, env, built)
}

// pushHostedBundle pushes a hosted env's bundle to its platform registry
// subtree under the env's control-plane identity, and decides what a failure
// means: a registry that REFUSED the identity fails the build; one that could
// not be reached degrades to a local bundle, except where the env's hub
// deploys only recorded bundles and a local one is worth nothing.
func pushHostedBundle(ctx context.Context, projectDir, env string, built bundle.Bundle, ledger envLedger, in bundleBuildInputs) (placedBundle, error) {
	base := bundlePushBaseFor(ctx, projectDir, env)
	if base == "" {
		// No base to push under. NOT an error: the images were
		// pushed to their own declared references and the build
		// stands. The bundle goes to the local layout, which is
		// where it stays — nothing later will learn an address the
		// declaration does not state.
		if ledger.hubUnits() {
			return placedBundle{}, errHubBundleNotPushed(env, "it declares no platform registry subtree (forge could not learn the organization its credential acts for)")
		}
		fmt.Fprintf(in.errWriter(),
			"[bundle] Note: env %s declares no platform registry subtree, so its bundle was written locally.\n"+
				"[bundle]   A hosted bundle's address is composed from the env's control-plane declaration\n"+
				"[bundle]   (registry host + the org your credential acts for); authenticate to push one.\n", env)
		return writeBundleLocally(ctx, projectDir, env, built)
	}
	repo := bundle.Repository(base, env)
	digest, err := pushBundleTo(ctx, projectDir, env, repo, built)
	if err != nil {
		// A REFUSAL IS NOT A DEGRADATION. The registry read the identity
		// and said no, which is a fact about credentials that a green
		// build would bury; the unreachable-registry case below is the
		// one a build tolerates. See bundleRegistryDeniedError.
		var denied *bundleRegistryDeniedError
		if errors.As(err, &denied) {
			return placedBundle{}, denied
		}
		// A hub-converged env is deployed FROM its recorded bundle, so
		// there is no "next build renders one on demand" to defer to:
		// without the bundle the release is not deployable at all.
		if ledger.hubUnits() {
			return placedBundle{}, errHubBundleNotPushed(env, fmt.Sprintf("the push to %s failed: %v", repo, err))
		}
		// A FAILED PUSH DOES NOT FAIL THE BUILD, for the same reason a
		// failed record does not: the images went to their own declared
		// references and the release is cut, so the build's product
		// stands. The bundle is a RECORD, and a missing one is
		// recoverable — `forge env deploy` renders one on demand.
		//
		// Making it fatal would mean a build newly depended on the
		// bundle subtree being reachable and writable, which is a
		// different fact from the image registry being reachable: a
		// registry can admit images and reject an artifact type, and a
		// project can push images somewhere the control plane does not
		// name as its base. Found exactly that way — two tests that
		// push images to a stub and had no route to the declared
		// registry started failing the whole build.
		fmt.Fprintf(in.errWriter(),
			"[bundle] Warning: env %s's bundle could not be pushed to %s (%v).\n"+
				"[bundle]   It was written to this machine's ledger instead; the build continues, and the next\n"+
				"[bundle]   build or deploy that can reach the registry pushes it (a bundle is content-addressed,\n"+
				"[bundle]   so re-pushing identical bytes is free).\n", env, repo, err)
		return writeBundleLocally(ctx, projectDir, env, built)
	}
	return placedBundle{
		bundleWriteOutcome: bundleWriteOutcome{
			Env: env, Digest: digest, Reference: repo + "@" + digest, Pushed: true,
		},
		repository: repo,
	}, nil
}

// writeBundleLocally writes the bundle into the machine ledger's OCI layout.
//
// The layout sits BESIDE the records that index it (Store.OCIDir), so a
// bundle's two halves — the row and the blob — cannot end up on different
// machines or in different lifetimes.
func writeBundleLocally(ctx context.Context, projectDir, env string, built bundle.Bundle) (placedBundle, error) {
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return placedBundle{}, err
	}
	layout, err := bundle.NewLocalLayout(store.OCIDir())
	if err != nil {
		return placedBundle{}, err
	}
	digest, err := layout.Write(ctx, built)
	if err != nil {
		return placedBundle{}, err
	}
	return placedBundle{bundleWriteOutcome: bundleWriteOutcome{
		Env: env, Digest: digest, Reference: layout.Reference(digest),
	}}, nil
}

// recordWrittenBundle indexes the bundle in the env's ledger.
//
// An UNDELIVERABLE record warns and returns; a REFUSED one fails the build.
// The split is declarationUndeliverable's, reused rather than re-derived: no
// credential, a transport failure, Unavailable and DeadlineExceeded mean the
// request never got an answer, and the bundle is content-addressed so a retry
// is free (F-3). Any other answer is the control plane having read the request
// and declined it, which is a fact about this env that a green build must not
// hide.
//
// F-15 rides the same path. A control plane that predates bundles answers
// Unimplemented on RecordBundle, which is not a refusal of anything — it is a
// server that cannot hold the record at all — so it warns, says what to do,
// and the build continues.
func recordWrittenBundle(ctx context.Context, projectDir, env string, ledger envLedger,
	record release.BundleRecord, blobs bundleBlobs, placed placedBundle, in bundleBuildInputs,
) (bundleWriteOutcome, error) {
	recorder, err := bundleRecorderForEnv(ctx, projectDir, env, ledger)
	if err != nil {
		// Opening the records half can fail the same way the record call
		// can — no credential, no transport — and it is classified the same
		// way, by the one classifier the declaration and RecordBundle use.
		if declarationUndeliverable(err) && ledger.hubUnits() && in.Pushed {
			return placed.bundleWriteOutcome, errHubBundleNotRecorded(env, placed.Digest, err)
		}
		if declarationUndeliverable(err) {
			fmt.Fprintf(in.errWriter(),
				"[bundle] Warning: env %s's bundle %s was written but not recorded: %v\n"+
					"[bundle]   The build continues; recording is idempotent on (env, digest), so the next build or deploy records it.\n",
				env, shortDigest(placed.Digest), err)
			return placed.bundleWriteOutcome, nil
		}
		return placed.bundleWriteOutcome, err
	}
	stored, created, err := recorder.RecordBundle(ctx, record, blobs)
	switch {
	case err == nil:
		out := placed.bundleWriteOutcome
		out.Recorded, out.Created = true, created
		if stored.Digest != "" && stored.Digest != record.Digest {
			// The server recorded a different digest than the bytes
			// forge pushed. That can only mean the two disagree about
			// what was sent, and every reader downstream would believe
			// the record.
			return out, fmt.Errorf(
				"the control plane recorded bundle %s for env %s, but forge pushed %s — "+
					"the record and the artifact do not describe the same bytes",
				stored.Digest, env, record.Digest)
		}
		return out, nil
	case errors.Is(err, errControlPlanePredatesBundles):
		fmt.Fprintf(in.errWriter(),
			"[bundle] %s predates bundles; upgrade it.\n"+
				"[bundle]   Env %s's bundle %s was written but not recorded; the build continues on the pre-bundle path.\n",
			errControlPlanePredatesBundles.Error(), env, shortDigest(placed.Digest))
		return placed.bundleWriteOutcome, nil
	case declarationUndeliverable(err) && ledger.hubUnits() && in.Pushed:
		return placed.bundleWriteOutcome, errHubBundleNotRecorded(env, placed.Digest, err)
	case declarationUndeliverable(err):
		fmt.Fprintf(in.errWriter(),
			"[bundle] Warning: env %s's bundle %s was written but not recorded: %v\n"+
				"[bundle]   The build continues; recording is idempotent on (env, digest), so the next build or deploy records it.\n",
			env, shortDigest(placed.Digest), err)
		return placed.bundleWriteOutcome, nil
	default:
		return placed.bundleWriteOutcome, fmt.Errorf("record env %s's bundle in %s: %w", env, ledger.Releases.Location(), err)
	}
}

// errHubBundleNotPushed and errHubBundleNotRecorded are the refusals for a
// hub-converged env, whose control plane applies the bundle it has RECORDED and
// nothing else. A release with no recorded bundle is not deployable there, so
// reporting the build green would leave the next deploy to discover it.
func errHubBundleNotPushed(env, why string) error {
	return fmt.Errorf("env %s's bundle was not pushed, so the release is not deployable: %s.\n"+
		"  env %s is converged by its control plane's hub from the bundle it records; with no recorded bundle there is nothing to apply.\n"+
		"  fix: resolve the cause above, then re-run `forge env build %s --release <version> --push` (a bundle is content-addressed, so a retry is free)",
		env, why, env, env)
}

func errHubBundleNotRecorded(env, digest string, cause error) error {
	return fmt.Errorf("env %s's bundle %s was pushed but the control plane did not record it, so the release is not deployable: %w\n"+
		"  env %s is converged by its control plane's hub from the bundle it records.\n"+
		"  fix: re-run `forge env build %s --release <version> --push` once the control plane is reachable (recording is idempotent on (env, digest))",
		env, shortDigest(digest), cause, env, env)
}

// bundleRecorderFor is the bundle half of an env's ledger: the control plane
// when the env declares one, this machine's ledger otherwise.
//
// It reads the SAME envLedger the release and the promotion came from, rather
// than re-resolving the declaration. A records store chosen by a different
// rule than the ledger it records into could put one env's bundles and its
// promotions in two different places.
// bundleLedgerFor and bundleRecorderForEnv are the env's ledger and its
// bundle half, as vars.
//
// Seams rather than mocked interfaces, because what a test needs to STATE here
// is which BACKEND the env selected — the one decision that routes the bytes —
// and that decision is made by rendering the env's KCL and resolving a
// credential. A test of "a hosted env pushes and records" would otherwise have
// to stand up a control plane to assert a placement rule.
var (
	bundleLedgerFor      = ledgerFor
	bundleRecorderForEnv = bundleRecorderFor
	bundlePushBaseFor    = declaredBundlePushBase
)

// declaredBundlePushBase is the platform registry subtree this env's bundle is
// pushed to, composed from the env's own control-plane DECLARATION
// (F-REGISTRY-DEFAULT: `<registry host>/<organization>/<project>`).
//
// It reads the declaration rather than a cache a previous command populated,
// which removes a state that used to exist: there is no longer a "base not
// known yet". The address is a function of the declaration, so an env that
// declares a control plane with an organization HAS one on its very first
// build, and an env that does not will never acquire one by being built again.
// The note placeBundleBytes prints says that, instead of promising a later
// build will learn it.
//
// A render failure yields "" rather than an error, and the bundle goes to the
// local layout. The caller has already rendered this env successfully to
// project its shape, so a failure here is unrelated to the bundle — and a
// build that refused over an address it only needs in order to PUSH would be
// failing the build for a record.
//
// A var (above) so a test can state the base without standing up a KCL tree:
// the thing under test in those cases is the PLACEMENT — pushed to the env's
// bundle repository versus written locally — not how an address is composed,
// which internal/hostedimage owns and tests.
func declaredBundlePushBase(ctx context.Context, projectDir, env string) string {
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return ""
	}
	// An org that cannot be learned composes no base, and placeBundleBytes then
	// says the bundle was written locally and why.
	_ = requireOrg(ctx, entities)
	return platformPushBase(entities)
}

// bundleReaderFor is what the env's bundle recorder decodes: nil (every
// feature) for a recorder this forge reads itself, or what a control plane
// says its decoder accepts.
func bundleReaderFor(ctx context.Context, projectDir, env string, ledger envLedger) (*release.BundleReader, error) {
	if !ledger.Hosted {
		return nil, nil
	}
	recorder, err := bundleRecorderForEnv(ctx, projectDir, env, ledger)
	if err != nil {
		return nil, err
	}
	reporter, ok := recorder.(bundleReaderReporter)
	if !ok {
		return nil, nil
	}
	return reporter.BundleReader(ctx)
}

func bundleRecorderFor(ctx context.Context, projectDir, env string, ledger envLedger) (bundleRecorder, error) {
	if !ledger.Hosted {
		return recordStoreFor(projectDir)
	}
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return nil, fmt.Errorf("render deploy/kcl/%s to reach its control plane: %w", env, err)
	}
	client, _, err := envDeclarationClient(env, entities)
	if err != nil {
		return nil, err
	}
	return hostedRecordStoreFor(client, hostedEnvRefFor(env, entities).Project), nil
}

// printBundleWrites is the one-line-per-env report.
func printBundleWrites(out io.Writer, written []bundleWriteOutcome) {
	for _, b := range written {
		if b.Skipped {
			fmt.Fprintf(out, "[bundle] env %s: no bundle written (the env could not be projected)\n", b.Env)
			continue
		}
		where := "locally"
		if b.Pushed {
			where = "pushed"
		}
		state := "recorded"
		switch {
		case !b.Recorded:
			state = "NOT recorded"
		case !b.Created:
			state = "already recorded"
		}
		fmt.Fprintf(out, "[bundle] env %s: %s (%d object(s)), %s, %s\n",
			b.Env, shortDigest(b.Digest), b.Objects, where, state)
		fmt.Fprintf(out, "[bundle]   %s\n", b.Reference)
	}
}

// parseBundleEnvs reads --bundle-envs, defaulting to the env being built.
//
// An empty entry is dropped rather than refused: `--bundle-envs prod,` is a
// trailing comma, not a request to bundle an env with no name.
func parseBundleEnvs(flag, env string) []string {
	if strings.TrimSpace(flag) == "" {
		return []string{env}
	}
	var out []string
	seen := map[string]bool{}
	for _, name := range strings.Split(flag, ",") {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return []string{env}
	}
	return out
}

// errWriter is in.errOut, or stderr.
func (in bundleBuildInputs) errWriter() io.Writer {
	if in.errOut != nil {
		return in.errOut
	}
	return progressWriter(true)
}

type hostedPinReleaseKey struct{}

// withHostedPinRelease says which release a deploy NAMES — the one it is about
// to record, which outranks the env's CURRENT promotion wherever the deploy
// reads "its release" before the promotion is written: the bundle's hosted
// records, the image pins (resolveDeployDigests), and the stale-image guard's
// anchor (resolveFreshnessAnchor). Without it they follow the current
// promotion, which on a first deploy does not exist yet, and on every later
// one is the release being replaced.
func withHostedPinRelease(ctx context.Context, version string) context.Context {
	if version == "" {
		return ctx
	}
	return context.WithValue(kclrender.WithRelease(ctx, version), hostedPinReleaseKey{}, version)
}

func hostedPinReleaseFrom(ctx context.Context) string {
	v, _ := ctx.Value(hostedPinReleaseKey{}).(string)
	return v
}

// ensureHostedReleaseBundle writes and records the env's bundle for a named
// release when none is recorded, so a deploy of that release has a bundle (and
// so a plan digest) to promote against.
func ensureHostedReleaseBundle(ctx context.Context, projectDir, env, version string, ledger envLedger, rerecord bool, errOut io.Writer) error {
	if !ledger.Hosted || version == "" {
		return nil
	}
	if r, err := ledger.Releases.Get(ctx, version); err != nil {
		return err
	} else if r != nil && len(r.SnapshotIntents) > 0 {
		if rerecord {
			return fmt.Errorf("sealed snapshot cannot be re-recorded from the current checkout")
		}
		return nil
	}
	// THE MACHINE LEDGER IS NOT EVIDENCE ABOUT A HOSTED ENV. A bundle row
	// there says "this machine rendered one", usually at cut time under a
	// different declaration (a direct-apply KCL, before the env declared its
	// control plane), so its digest is not the one the control plane holds.
	// Treating it as "already recorded" skipped the record, and the plan then
	// looked up a digest the control plane had never seen: "no recorded
	// bundle ... no deploy plan could be computed", on exactly the first
	// hosted deploy of a release that has a local bundle. Writing is
	// idempotent — a bundle is content-addressed, a re-push of existing
	// blobs is a no-op and a re-record of the same digest returns the row —
	// so the cost of not trusting the local row is one render.
	return writeReleaseBundle(ctx, projectDir, env, version, ledger, rerecord, errOut)
}

// writeReleaseBundle renders env's bundle pinned to the digests and sources
// release version RECORDS — not to whatever this machine last built — and
// writes and records it where env's ledger says. Idempotent: a release whose
// bundle of record matches this render reuses that bundle.
//
// Its callers are the two deploys that build nothing: a named version
// (through ensureHostedReleaseBundle) and a no-version deploy that reuses an
// existing release (deploy_all.go). Both must seal the bundle over the
// release's pins, because the build state on this machine may belong to a
// different build entirely.
func writeReleaseBundle(ctx context.Context, projectDir, env, version string, ledger envLedger, rerecord bool, errOut io.Writer) error {
	rel, err := ledger.Releases.Get(ctx, version)
	if err != nil || rel == nil {
		return nil
	}
	pins := release.BundlePins{Images: rel.SharedDigests()}
	if sources := rel.Sources(); len(sources) > 0 {
		pins.Sources = sources
	}
	out, err := writeBundlesFn(ctx, projectDir, []string{env}, bundleBuildInputs{
		Release: version, Pins: pins, Pushed: true, Rerecord: rerecord,
		Now: time.Now().UTC().Truncate(time.Second), errOut: errOut,
	})
	if err != nil {
		// A render that differs from the release's recorded bundle is
		// a REFUSAL, not a warning: deploying past it would apply bytes
		// nobody reviewed under a release that names other bytes.
		var differs *releaseBundleDiffersError
		var denied *bundleRegistryDeniedError
		if errors.As(err, &differs) || errors.As(err, &denied) || ledger.hubUnits() {
			return err
		}
		fmt.Fprintf(errOut, "[bundle] Warning: env %s's bundle for %s could not be written: %v\n", env, version, err)
		return nil
	}
	printBundleWrites(errOut, out)
	noteRecordedBundles(version, out)
	if ledger.hubUnits() {
		for _, o := range out {
			if o.Skipped || !o.Recorded {
				return fmt.Errorf("env %s's bundle for release %s was not recorded, so the control plane has nothing to apply.\n"+
					"  fix: resolve the [bundle] warning above and re-run `forge env deploy %s %s`", env, version, env, version)
			}
		}
	}
	return nil
}
