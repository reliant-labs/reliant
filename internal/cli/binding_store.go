package cli

// The env→release promotion ledger, behind a seam.
//
// WHY A SEAM AT ALL. Promotion state answers "which release does prod run",
// and a file inside the repo that PRODUCED the artifact is circular by
// construction: the commit recording "prod runs v1.5.13" cannot be in
// v1.5.13, because it is written after v1.5.13 was cut. So the ledger must be
// able to live somewhere that is not the artifact's own source tree, which
// means the callers must not know where a ledger is.
//
// THE LOCAL BACKEND IS THE DEFAULT, FOREVER. Not a stepping stone. forge with
// no account, on a plane, must stay fully functional: `forge env deploy <env>
// <version>` and `forge env deploy` are core verbs, and a core verb that
// degrades without a login is a product that lied about being local-first.
// What changed is WHERE that backend writes: a machine-scoped, locked store
// under $FORGE_LEDGER_HOME (internal/ledgerfile) rather than
// .forge/promotions inside the checkout. That move closes three defects at
// once — the circularity above, the branch-dependence that made two worktrees
// see two histories, and the append race the old backend documented rather
// than fixed.
//
// ONE MODEL, TWO STORES. Both read and write forge/pkg/release types, both
// serialize them as the same canonical JSON, and both apply release.Decide,
// so "is this a no-op retry" and "what does the env run now" have one answer
// whichever store holds the ledger. See pkg/release's package doc.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

// bindingStore is the promotion ledger as its CONSUMERS need it: what one env
// runs now, append one promotion, and say where the answer came from.
//
// Declared here at the consumer rather than exported from an implementation
// package, per the package-boundary rule the repo follows for
// clusterImageLister and envTargetResolver in env_verify.go.
//
// THERE IS NO "SET". The ledger is append-only: an environment's current
// binding is its most recent entry, and there is no pointer beside the
// history that could disagree with it. Moving an env back to an older release
// is a NEW promote entry, never an edit of an old one.
//
// NO projectDir ANYWHERE IN THIS INTERFACE. A project directory is a FILE
// concept; neither the hosted backend nor the machine ledger has one. The
// backing is bound ONCE at construction and the methods speak only in domain
// terms.
type bindingStore interface {
	// Current returns env's most recent promotion, and whether it has one.
	// "Never promoted" is a normal state, so it is a bool rather than an
	// error — distinct from a ledger that could not be read at all.
	Current(ctx context.Context, env string) (release.Promotion, bool, error)

	// Append records p (which must name p.Env, p.Release and p.Kind) under
	// release.Decide's rules and returns the entry the ledger now holds:
	// the newly appended one, or — for a retry of the current state — the
	// EXISTING entry, unchanged. The backend stamps ID and PromotedAt.
	//
	// guard is the compare-and-set the write asserts (see promote_cas.go),
	// checked by the backend AFTER the idempotent no-op and against the
	// history it holds at the moment of the write. A write the guard
	// refuses is a *promoteRefusedError and appends nothing. The zero
	// guard asserts nothing.
	Append(ctx context.Context, p release.Promotion, guard appendGuard) (release.Promotion, error)

	// Location names where promotions are recorded, for human-facing
	// output — a directory for the machine ledger, the endpoint URL for a
	// hosted one. Opaque: callers must only PRINT it.
	Location() string
}

// releaseLedger is the release half of the same backend: cut a release, read
// one back, list them. Separate from bindingStore rather than widening it,
// because the two have different consumers (build/cut writes releases;
// promote/deploy/verify read promotions) and a six-method interface would be
// a concrete type wearing an interface.
type releaseLedger interface {
	// Cut records r. created is false when an identical release already
	// held the version (a retry); a DIFFERENT artifact set under the same
	// version is release.ErrReleaseConflict.
	Cut(ctx context.Context, r release.Release) (created bool, err error)
	// Get returns the release, or (nil, nil) when the version was never cut.
	Get(ctx context.Context, version string) (*release.Release, error)
	// List returns releases NEWEST FIRST.
	List(ctx context.Context) ([]release.Release, error)
	Location() string
}

// ─── The new record seams ────────────────────────────────────────────────────
//
// Three thin capabilities for the records the bundle/apply/session half of
// the design adds. Each is one or two methods, declared HERE at the consumer
// and implemented by whichever store the env selected — the same shape as
// bindingStore, for the same reason: the command layer must not know whether
// an apply was recorded in a file or over an RPC.
//
// They are separate interfaces rather than fields on a widened bindingStore
// because their consumers are disjoint: `forge env build` records bundles,
// `forge env deploy` records applies, `forge env up` reports sessions. A
// store that cannot do one of these simply does not implement it, and the
// caller type-asserts — the pattern ledgerFreshnessChecker and
// bindingHistoryReader already use.

// bundleBlobs is a bundle's OWN BYTES, plus where they were pushed.
//
// Declared HERE, at the consumer, rather than in pkg/release beside
// BundleRecord — the boundary rule this package follows for bindingStore and
// clusterImageLister. A BundleRecord is a DOMAIN record, readable from either
// store and shown to a user; these are transport bytes, meaningful only to
// the two backends and the one command that produces them. Putting them on
// the domain type would also invite a reader to expect a record read back
// from a ledger to carry them, which it never does.
type bundleBlobs struct {
	// Repository is where the bytes were pushed — the reference the
	// control plane checks against the registry subtree it admits, or an
	// OCI-layout reference for the machine ledger.
	Repository string
	// Manifest is the OCI image manifest, the bytes the digest is over.
	// Config is the bundle document blob the manifest's config descriptor
	// names.
	Manifest, Config []byte
	ManifestsLayer   []byte
}

// bundleRecorder records a built bundle and reads one back. A bundle IS its
// content, so recording is idempotent on (env, digest) and a retry after a
// failed record is free.
//
// IT CARRIES THE BYTES, NOT A DESCRIPTION OF THEM, and that asymmetry is the
// whole contract on the hosted side. RecordBundle on a control plane checks
// digest = sha256(manifest), checks the manifest's config descriptor names
// sha256(config), then decodes that blob strictly and takes the shape,
// provenance, config digest and release FROM THE VERIFIED DOCUMENT (doc
// §6.3). A seam that passed only the BundleRecord could not express that: a
// client able to STATE a shape can state one that disagrees with the bytes it
// pushed, and every reader downstream would then trust the description over
// the artifact.
//
// So both are passed, and each backend uses the half it can verify. The
// machine backend indexes the record and ignores the blobs, which it already
// holds in an OCI layout it owns — there is no second party for it to lie to.
// The hosted backend sends the blobs and lets the server derive the record.
type bundleRecorder interface {
	RecordBundle(ctx context.Context, b release.BundleRecord, blobs bundleBlobs) (record release.BundleRecord, created bool, err error)
	Bundle(ctx context.Context, id string) (*release.BundleRecord, error)
}

// THERE IS NO applyRecorder SEAM, because nothing ever called one.
//
// F3 declared it — BeginApply before the bytes moved, FinishApply after — and
// F4 asked to widen it with a compare-and-set. Neither half ever acquired a
// production caller: the apply path (cluster.Apply, through the deploy
// dispatch) reports its outcome by SUCCEEDING OR FAILING, and no verb in this
// package brackets it with a record. So the seam, the two methods satisfying
// it, and the hosted client's write half were an interface with no consumer on
// either side.
//
// It is deleted on that ground alone — dead code, removed — and NOT on the
// stronger claim that forge does not apply. Forge DOES apply today, and this
// deletion does not change when it stops.
//
// Where that is heading, so the next reader is not misled either way: every
// real env is moving to build -> OCI bundle -> version store -> Flux applies
// to the target cluster, control-plane included, and forge's direct apply then
// narrows to dev and ephemeral clusters. That removal is a LATER step, after
// control-plane is on the Flux path; nothing here does it, and the client
// apply is untouched by this branch.
//
// If a verb ever does want to bracket an apply with a durable record, a seam
// for it should be designed against that verb's needs rather than restored
// from this one.
//
// The READ side survives and is used: a convergence record is an observation
// the control plane's own observer writes, which forge reads through
// GetLiveView (hosted_apply.go decodes it). internal/ledgerfile keeps the
// reader for a machine ledger's existing apply lines, which `forge ledger
// show` and `forge ledger export` render as history.

// sessionReporter records that a local stack is running here.
//
// Presence only (owner decision O-8): a session is an observation, never a
// promotion, never a deploy target, and it feeds no policy or billing. The
// single method is a report, and repeated reports for one
// (env, host, worktree) replace rather than accumulate.
type sessionReporter interface {
	ReportSession(ctx context.Context, s release.LocalSession) error
}

// envLedger is one environment's backend, both halves. A concrete struct —
// accept interfaces, return structs — so a caller that needs only one half
// takes only that field.
type envLedger struct {
	Bindings bindingStore
	Releases releaseLedger
	// Hosted reports whether this env's ledger is a control plane, and so
	// whether a promotion to it is converged server-side.
	Hosted bool
	// Mixed reports that a HOSTED env also declares work its control plane
	// does not run: a cluster or compose workload in a unit the hub does not
	// converge, host infra, a cluster database, a shipped frontend
	// (forgeAppliesLocally). Such an env needs BOTH halves of the
	// follow-through: forge applies the part it owns, the control plane
	// converges the rest.
	//
	// Meaningless unless Hosted, which is why it is not spelled
	// "AppliesLocally": a self-managed env always applies from this
	// machine, so a field naming that fact would have to be set on every
	// ledger — and the one time it was forgotten, the deploy would record a
	// binding and apply nothing. Mixed is the fact nothing else implies,
	// and the zero value is right for both other shapes.
	Mixed bool
	// Units is who applies each delivery unit of the env (unit_convergence.go):
	// the units the control plane's hub converges, and the units forge still
	// applies directly. An env moves onto the hub one unit at a time, so this
	// is a per-unit answer; the old env-level "hub converged" boolean could
	// only say yes or no for the whole env. Meaningful only for a hosted
	// ledger. Mixed already accounts for it: it is false when the hub converges
	// everything forge would otherwise apply.
	Units unitSplit
	// HostedTiers are the tiers a HOSTED env binds to its control plane
	// (hostedTiersOf): what that control plane converges, and so the only
	// thing a server-side rollout of this env can ever contain.
	//
	// It is resolved with Mixed, from the same render, because together they
	// separate two shapes Mixed alone conflates. A Mixed env WITH hosted
	// tiers is converged in two halves. A Mixed env with NONE — control-plane
	// prod: a forge.ControlPlane ledger over sixteen cluster workloads and a
	// Firebase frontend — is converged by nothing but this machine's apply;
	// its control plane records the promotion and has no rollout to report
	// (ledgerOnly). Treating the second as the first is what made a deploy
	// that had already shipped refuse with "nothing to converge".
	HostedTiers []hostedTier
	// OverriddenEndpoint is the control plane the env DECLARES when
	// FORGE_LEDGER=machine kept this run on the machine ledger instead
	// (ledger_override.go). Empty otherwise. Carried so a surface that
	// reports where records go can say what was overridden, rather than
	// letting the override read as "this env declares nothing".
	OverriddenEndpoint string
	// Offline is set when `--offline-ledger` replaced a hosted ledger with
	// its cached copy (offline_ledger.go). Nil otherwise.
	Offline *offlineBindings
}

// ledgerOnly reports a hosted-ledger env whose control plane converges NONE
// of it: Mixed (forge applies its workloads from here), no hosted tier, and no
// unit handed to the hub. Its control plane is its release ledger and nothing
// else, so the local apply is the whole deploy, there is no server-side rollout
// to wait on, and an empty one is the declaration's doing — never a missing
// publish.
func (l envLedger) ledgerOnly() bool {
	return l.Mixed && len(l.HostedTiers) == 0 && !l.Units.AnyHub()
}

// appliesLocally reports whether any part of the env is applied FROM THIS
// MACHINE. A self-managed env always is: nothing watches a jsonl file, so if
// this command does not apply the binding it just wrote, nothing ever will. A
// hosted env is only when it is Mixed.
func (l envLedger) appliesLocally() bool { return !l.Hosted || l.Mixed }

// hubUnits reports whether the hub applies at least one unit of the env: the
// bundle must be recorded for it, its reconciler must be able to apply, and the
// deploy waits on what it reports. It is the per-unit replacement for the old
// env-level "hub converged" question, true for an env the hub converges whole
// and for one that has moved only some units.
func (l envLedger) hubUnits() bool { return l.Hosted && l.Units.AnyHub() }

// forgePlans reports an env whose deploy plan only the machine applying it can
// compute: forge applies bundle objects itself and the control plane has no
// hosted tier to diff, so the control plane's Live is missing what forge
// applied and its plan would call it all new, every release. That is the
// ledger-only env, and an env that has moved only SOME units to the hub: the
// hub sees its own units, forge sees the rest, and only forge's apply gates
// record a baseline for both. An env whose every unit the hub converges is
// planned by the control plane even if forge still ships a frontend, because
// the frontend is outside the bundle and the bundle is all the hub's.
func (l envLedger) forgePlans() bool {
	return l.Mixed && len(l.HostedTiers) == 0 && (!l.Units.AnyHub() || len(l.Units.Forge) > 0)
}

// ─── Selection ───────────────────────────────────────────────────────────────

// ledgerFor returns the ledger an environment uses. This is the SINGLE place
// the backend is chosen, and the choice is DECLARATIVE: an env whose KCL
// declares `forge.ControlPlane` uses that control plane's ledger; every other
// env uses this machine's ledger. No flag, no context — the same checkout
// resolves the same backend on every machine. The one override is
// FORGE_LEDGER=machine (ledger_override.go), which keeps a hermetic run off a
// declared control plane and says so.
//
// A render FAILURE is an error, never a fallback to the machine ledger. For a
// hosted env that fallback would silently answer "never promoted", and a
// deploy would then ship mutable tags instead of the promoted digests — the
// exact failure the ledger exists to prevent.
// It is also where the UNIMPORTED-CHECKOUT refusal fires. Selection is the
// one place every ledger read and write passes through, so checking here
// means no command can reach a ledger that is missing history the checkout
// still holds — rather than each verb having to remember to ask. See
// ledger_unimported.go for why that refusal exists at all.
func ledgerFor(ctx context.Context, projectDir, env string) (envLedger, error) {
	return ledgerForRendered(ctx, projectDir, env, nil)
}

// ledgerForRendered is ledgerFor for a caller that has already rendered env's
// declaration (RenderKCL) in this command's render context: the selection
// reads those entities instead of evaluating the env's KCL a second time —
// for a large env, seconds of CPU per command. nil renders, as ledgerFor does.
func ledgerForRendered(ctx context.Context, projectDir, env string, entities *KCLEntities) (envLedger, error) {
	l, err := selectLedgerRendered(ctx, projectDir, env, entities)
	if err != nil {
		return envLedger{}, err
	}
	// --offline-ledger is the one thing that bypasses the unimported-
	// checkout refusal: the cached ledger cannot be asked what it has
	// imported, and an operator in an outage has chosen to deploy without
	// consulting it. Nothing else — no env var, no other flag — does.
	if l.Offline != nil {
		return l, nil
	}
	if err := checkLedgerImported(ctx, projectDir, env, l); err != nil {
		return envLedger{}, err
	}
	return l, nil
}

// selectLedger is the selection alone, without the import check — the seam
// `forge ledger import` itself needs, because the import must be able to
// open the very ledger the refusal is about in order to fill it.
func selectLedger(ctx context.Context, projectDir, env string) (envLedger, error) {
	return selectLedgerRendered(ctx, projectDir, env, nil)
}

// selectLedgerRendered is selectLedger reading entities, when the caller has
// them, instead of rendering env's declaration again.
func selectLedgerRendered(ctx context.Context, projectDir, env string, entities *KCLEntities) (envLedger, error) {
	// Validated before anything else, so a malformed FORGE_LEDGER fails on
	// the first command that reaches any ledger — not only once an env
	// that declares a control plane is rendered.
	if _, err := ledgerSelectionFromEnv(); err != nil {
		return envLedger{}, err
	}
	mainK := filepath.Join(projectDir, "deploy", "kcl", env, "main.k")
	if _, err := os.Stat(mainK); err != nil {
		// No KCL for this env in this checkout — nothing can declare a
		// control plane, so the answer is this machine's ledger. This
		// is how a test project, and an env named only on the command
		// line, keep working.
		if offlineLedgerRequested(ctx) {
			return envLedger{}, fmt.Errorf("--offline-ledger: this checkout has no deploy/kcl/%s/main.k, so env %q declares no control plane to be offline from", env, env)
		}
		return machineLedger(projectDir)
	}
	if entities == nil {
		var err error
		if entities, err = RenderKCL(ctx, projectDir, env); err != nil {
			return envLedger{}, fmt.Errorf("choose the release ledger for env %q: render deploy/kcl/%s: %w", env, env, err)
		}
	}
	if offlineLedgerRequested(ctx) {
		return offlineLedgerForEntities(env, entities, projectDir)
	}
	return ledgerForEntities(env, entities, projectDir)
}

// offlineLedgerForEntities is --offline-ledger's selection: the env's cached
// ledger, for an env whose ledger is a control plane and whose deploy this
// machine alone applies. Nothing that could reach the control plane runs — not
// the credential, not the client.
func offlineLedgerForEntities(env string, entities *KCLEntities, projectDir string) (envLedger, error) {
	decl := declarationFromEntities(entities)
	if decl == nil {
		return envLedger{}, fmt.Errorf("--offline-ledger: env %q keeps its ledger on this machine, not on a control plane, so there is no hosted ledger to be offline from", env)
	}
	if sel, err := ledgerSelectionFromEnv(); err != nil {
		return envLedger{}, err
	} else if sel == ledgerSelectMachine {
		return envLedger{}, fmt.Errorf("--offline-ledger and %s=%s contradict each other: one deploys from the cached hosted ledger, the other from this machine's own", ledgerSelectionEnv, ledgerSelectMachine)
	}
	ep, err := cloud.ResolveEndpoint(env, decl)
	if err != nil {
		return envLedger{}, err
	}
	units, err := unitSplitOf(entities)
	if err != nil {
		return envLedger{}, err
	}
	base := envLedger{Units: units, HostedTiers: hostedTiersOf(entities)}
	base.Mixed = forgeAppliesLocally(entities, units)
	if !base.ledgerOnly() {
		return envLedger{}, offlineShapeError(env, ep.URL, base.Units, base.HostedTiers)
	}
	return openOfflineLedger(projectDir, env, ep.URL, base)
}

// ledgerForEntities is the render-free half of ledgerFor, split out so the
// selection rule is testable from a literal entity.
//
// AN ENV THAT DECLARES A CONTROL PLANE KEEPS ITS LEDGER THERE, WHATEVER ITS
// KIND — including LOCAL. That is the one change from the previous rule,
// which sent a LOCAL control-plane env back to the checkout on the reasoning
// that "the platform runs nothing of it, so there is no hosted release to
// bind". The reasoning was about PLACEMENT, and it was applied to RECORDING.
// A LOCAL env still has presence to report and sessions to show, Live must
// render them without a daemon, and splitting one project's records across
// two stores by kind made "where is this env's history" a question with two
// answers. Declaration decides the store; kind decides what gets placed.
func ledgerForEntities(env string, entities *KCLEntities, projectDir string) (envLedger, error) {
	decl := declarationFromEntities(entities)
	if decl == nil {
		return machineLedger(projectDir)
	}
	selection, err := ledgerSelectionFromEnv()
	if err != nil {
		return envLedger{}, err
	}
	if selection == ledgerSelectMachine {
		// BEFORE the endpoint, the credential and the client: under the
		// override nothing that could reach the declared control plane
		// runs — not even a credential helper.
		l, err := machineLedger(projectDir)
		if err != nil {
			return envLedger{}, err
		}
		l.OverriddenEndpoint = strings.TrimRight(strings.TrimSpace(decl.Endpoint), "/")
		noteLedgerOverride(env, l.OverriddenEndpoint, l.Bindings.Location())
		return l, nil
	}
	ep, err := cloud.ResolveEndpoint(env, decl)
	if err != nil {
		return envLedger{}, err
	}
	cred, err := cloud.ResolveCredential("", ep)
	if err != nil {
		return envLedger{}, fmt.Errorf("env %q keeps its release ledger on the control plane at %s: %w", env, ep.URL, err)
	}
	ref := hostedEnvRefFor(env, entities)
	l := hostedLedger(cloud.NewClient(ep, cred), ep.URL, ref.Project, ref.Kind)
	// A MIXED env: its ledger is this control plane, and it ALSO declares
	// workloads the control plane does not run. Both facts come from the
	// same render, so they are resolved together here rather than being
	// re-derived later from a second render that could disagree.
	units, err := unitSplitOf(entities)
	if err != nil {
		return envLedger{}, fmt.Errorf("env %q: %w", env, err)
	}
	l.Units = units
	l.Mixed = forgeAppliesLocally(entities, units)
	l.HostedTiers = hostedTiersOf(entities)
	return attachHostedMirror(l, projectDir), nil
}

// bindingStoreFor is ledgerFor for the callers that need only the promotion
// half.
func bindingStoreFor(ctx context.Context, projectDir, env string) (bindingStore, error) {
	l, err := ledgerFor(ctx, projectDir, env)
	if err != nil {
		return nil, err
	}
	return l.Bindings, nil
}

// machineLedger is a SELF-MANAGED env's ledger: Hosted false, so it applies
// from this machine by definition (envLedger.appliesLocally).
//
// The project id is derived from forge.yaml's name plus the canonical origin
// URL, so every worktree of one project shares one ledger and a fork with
// the same name does not.
func machineLedger(projectDir string) (envLedger, error) {
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return envLedger{}, err
	}
	return envLedger{
		Bindings: machineBindingStore{store: store},
		Releases: machineReleaseLedger{store: store},
	}, nil
}

// ledgerHome resolves the machine ledger's root. A seam, like
// hostedProjectName above it, so a test can point the whole CLI at a
// t.TempDir() WITHOUT t.Setenv — which Go forbids in a parallel test and
// which would leak between tests that share a process.
var ledgerHome = ledgerfile.Home

// openMachineLedger resolves the home and project id and opens the store.
func openMachineLedger(projectDir string) (*ledgerfile.Store, error) {
	home, err := ledgerHome()
	if err != nil {
		return nil, err
	}
	return openMachineLedgerIn(home, projectDir)
}

// ledgerProjectName is the project a directory belongs to, for keying its
// ledger. A seam beside ledgerHome, for the same reason: a test must be able
// to name the project WITHOUT writing a forge.yaml into the checkout.
//
// That is not a convenience. Writing a file into a project directory changes
// what the project IS — it makes a git tree dirty, which silently disables
// the build-staleness guard — so a helper that created one to satisfy the
// ledger would quietly change the behaviour of every test that also cares
// about git state. Found exactly that way.
var ledgerProjectName = func(projectDir string) string {
	cfg, err := config.LoadProjectDir(projectDir)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Name
}

// openMachineLedgerIn is openMachineLedger with the home supplied — the seam
// a test drives from a t.TempDir() without t.Setenv, which cannot be used in
// a parallel test.
func openMachineLedgerIn(home, projectDir string) (*ledgerfile.Store, error) {
	name := ledgerProjectName(projectDir)
	if name == "" {
		// A project with no forge.yaml name cannot be keyed, and
		// falling back to the directory name would re-introduce exactly
		// the per-worktree split the machine ledger exists to remove:
		// two worktrees of one project have two directory names.
		return nil, fmt.Errorf("this project has no name in forge.yaml, so its release ledger cannot be keyed.\n" +
			"  The ledger is shared by every worktree of a project, which needs a stable project identity.\n" +
			"  Add `name: <project>` to forge.yaml")
	}
	id, err := ledgerfile.ProjectID(name, originURL(projectDir))
	if err != nil {
		return nil, err
	}
	return ledgerfile.Open(home, id)
}

// machineLedgerLocation is the machine ledger's directory for a project, for
// human-facing output. A project whose ledger cannot be resolved reports the
// reason instead of an empty string, so a report never shows a blank where a
// path belongs.
func machineLedgerLocation(projectDir string) string {
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return "(unavailable: " + err.Error() + ")"
	}
	return store.Dir()
}

// originURL is the project's origin remote, or "" when it has none.
//
// A project with no remote is keyed by name alone, which is correct rather
// than degraded: a never-pushed project has no identity beyond its name on
// this machine. ledgerfile.ProjectID canonicalizes whatever this returns, so
// the raw URL is fine here.
//
// Read with git rather than through pkg/release, whose equivalent is
// deliberately unexported (it is one step of provenance capture, not a
// utility). Duplicating three lines at the consumer is the boundary rule the
// repo follows — WET over DRY beats exporting an implementation detail.
func originURL(projectDir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), ledgerGitReadTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	cmd.Dir = projectDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ─── The machine ledger: promotions ──────────────────────────────────────────

// machineBindingStore adapts internal/ledgerfile to the bindingStore seam.
//
// It is a thin adapter on purpose. The LOCKING and the record rules live in
// ledgerfile, where they are testable without a project or a render; the
// translation of "a promote with a CAS guard" into "a decision function" is
// what belongs at this boundary, because appendGuard and the refusal type it
// produces are the command layer's vocabulary, not the store's.
type machineBindingStore struct {
	store *ledgerfile.Store
}

// Current is env's newest promotion.
func (s machineBindingStore) Current(_ context.Context, env string) (release.Promotion, bool, error) {
	return s.store.CurrentPromotion(env)
}

// Append records the promotion under release.Decide and the guard, with the
// store's lock held across the read, the decision and the write.
//
// THAT LOCK IS THE DIFFERENCE from the retired in-checkout backend, which
// documented this exact race and declined to close it: two writers could
// both decide from the same history and both append. Here the CAS is checked
// against the history on disk at the instant of the write, so a stale plan
// is refused and two concurrent promotes of one move produce one line.
func (s machineBindingStore) Append(_ context.Context, p release.Promotion, guard appendGuard) (release.Promotion, error) {
	if guard.ResolveVersionFromSource {
		// A file ledger has no server to resolve a release under a lock,
		// so it cannot honour this and must not pretend to by writing
		// the caller's preview. `--from` is refused earlier for a
		// file-ledger env (promote_from.go's same-control-plane guard);
		// this is the backstop that keeps that the only way in.
		return release.Promotion{}, fmt.Errorf(
			"%s records promotions in %s, which cannot resolve a release from a source promotion: promote by version",
			p.Env, s.Location())
	}
	return s.store.AppendPromotion(p, func(history []release.Promotion, req release.Promotion) (*release.Promotion, error) {
		return admitPromotion(history, req, guard)
	})
}

// Location is the ledger directory.
func (s machineBindingStore) Location() string { return s.store.Dir() }

// HistoryPage serves the machine ledger's history, with the SAME semantics
// the server applies: newest first, strictly before the cursor, optionally
// one release. The log is small, so the page is cut in memory.
func (s machineBindingStore) HistoryPage(_ context.Context, env string, q historyQuery) (historyPage, error) {
	limit, err := q.limit()
	if err != nil {
		return historyPage{}, err
	}
	all, err := s.store.Promotions(env)
	if err != nil {
		return historyPage{}, err
	}
	newestFirst := make([]release.Promotion, len(all))
	for i := range all {
		newestFirst[len(all)-1-i] = all[i]
	}
	start := 0
	if q.Before != "" {
		start = -1
		for i, p := range newestFirst {
			if p.ID == q.Before {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return historyPage{}, fmt.Errorf("%w: --before %q names no promotion of %s", errHistoryQueryInvalid, q.Before, env)
		}
	}
	var page historyPage
	for i := start; i < len(newestFirst); i++ {
		p := newestFirst[i]
		if q.Release != "" && p.Release != q.Release {
			continue
		}
		if len(page.Promotions) == limit {
			// There is at least one more matching entry, so this page
			// is not the last: the cursor is the last entry SERVED.
			page.Next = page.Promotions[len(page.Promotions)-1].ID
			break
		}
		page.Promotions = append(page.Promotions, p)
	}
	return page, nil
}

// ─── The machine ledger: releases ────────────────────────────────────────────

// machineReleaseLedger adapts internal/ledgerfile to the releaseLedger seam.
type machineReleaseLedger struct {
	store *ledgerfile.Store
}

func (l machineReleaseLedger) Cut(_ context.Context, r release.Release) (bool, error) {
	return l.store.CutRelease(r)
}

func (l machineReleaseLedger) Get(_ context.Context, version string) (*release.Release, error) {
	return l.store.Release(version)
}

// List returns releases NEWEST FIRST. The store returns them in the order
// they were cut and the ordering is applied HERE, through the one comparator
// both backends share, so "latest" cannot mean two things.
func (l machineReleaseLedger) List(_ context.Context) ([]release.Release, error) {
	all, err := l.store.Releases()
	if err != nil {
		return nil, err
	}
	sortReleasesNewestFirst(all)
	return all, nil
}

func (l machineReleaseLedger) Location() string { return l.store.Dir() }

// ─── The machine ledger: the new records ─────────────────────────────────────

// machineRecordStore implements bundleRecorder and sessionReporter against
// the machine ledger. It writes no apply record, because no caller ever asked
// it to — see the note on the absent applyRecorder seam above. The underlying
// ledgerfile.Store still reads existing apply lines for `forge ledger show`.
//
// A separate type from machineBindingStore rather than more methods on it:
// the two have disjoint consumers, and a store that is handed to `forge env
// up` to report a session has no business also exposing Append.
type machineRecordStore struct {
	store *ledgerfile.Store
}

// recordStoreFor returns the records half of a project's machine ledger.
//
// This is the entry point F6a (bundles) and F6b (applies, sessions) call. It
// takes a project directory rather than an env, because the machine ledger is
// keyed by PROJECT: every env of one project records into the same store, and
// the per-env split is a file inside it.
//
// There is no hosted twin yet. When one exists the selection belongs beside
// ledgerForEntities, reading the same declaration — a records store chosen by
// a different rule than the ledger it records into would be able to put an
// env's bundles and its promotions in two different places.
func recordStoreFor(projectDir string) (machineRecordStore, error) {
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return machineRecordStore{}, err
	}
	return machineRecordStore{store: store}, nil
}

// RecordBundle indexes the record and IGNORES the blobs.
//
// That is not a shortcut, and it is the one place the two backends
// legitimately differ. The machine ledger's blobs live in an OCI layout it
// owns (Store.OCIDir), written by the same command, in the same pass, before
// this call — so there is no second party that could be told a shape
// disagreeing with the bytes. The hosted backend has one, which is why it
// sends the blobs and lets the server derive the record from them instead.
//
// The blobs are still a PARAMETER here rather than a hosted-only method,
// because the caller must not have to know which backend it holds. A
// command that had to branch would eventually record a bundle on one store
// and not the other.
func (s machineRecordStore) RecordBundle(_ context.Context, b release.BundleRecord, _ bundleBlobs) (release.BundleRecord, bool, error) {
	return s.store.RecordBundle(b)
}

func (s machineRecordStore) Bundle(_ context.Context, id string) (*release.BundleRecord, error) {
	return s.store.Bundle(id)
}

func (s machineRecordStore) ReportSession(_ context.Context, sess release.LocalSession) error {
	return s.store.ReportSession(sess)
}

// Compile-time proof that the machine backend satisfies every seam it claims.
// The assertions are what keep a method rename from silently dropping a
// store out of a capability its consumers type-assert for.
var (
	_ bindingStore         = machineBindingStore{}
	_ bindingHistoryReader = machineBindingStore{}
	_ releaseLedger        = machineReleaseLedger{}
	_ bundleRecorder       = machineRecordStore{}
	_ sessionReporter      = machineRecordStore{}
)
