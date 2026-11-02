package cli

// The RELEASE half of `forge env status <env>`: is this environment actually
// RUNNING the release its binding claims?
//
// WHY THIS IS A SEPARATE QUESTION FROM RUNTIME HEALTH. A promotion writes a
// binding — env → release, with the per-image digests frozen at promote time
// — and `promoted_at` is stamped when the env is PROMOTED, not when it is
// deployed. Cutting a release is not shipping it, and a binding claiming
// v1.5.13 proves only that someone ran a promote. Confirming prod had moved
// used to mean reading live digests by hand, one
// `kubectl get deploy -o jsonpath` per workload. This is that check.
//
// It is the retired `env verify`, unchanged in substance: the same five
// outcomes, the same injected seams, and the same exit codes. Only the
// command surface moved — status is the one read view now, and this is the
// half of it that compares a declaration against reality.
//
// FIVE OUTCOMES, NOT TWO:
//
//	MATCH        the cluster runs the digest the binding declares.
//	DRIFT        the cluster runs a DIFFERENT digest. Both are reported.
//	MISSING      declared by the binding, running nowhere.
//	UNTAGGED     the workload runs by mutable tag, so there is no digest to
//	             compare. Nothing is proven either way.
//	UNREACHABLE  the cluster could not be read. Says NOTHING about the env.
//
// Exit 2 is separate from 1 on purpose. A VPN drop or an expired credential
// is not evidence that a release is wrong, and a gate that reports drift and
// a network failure with the same code gets switched off the first week it is
// wrong about one of them.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/release"
)

// defaultEnvStatusReleaseTimeout bounds the cluster read. Generous enough for a cold
// cloud API server behind a fresh credential exchange, short enough that an
// unreachable cluster fails a CI job rather than hanging it.
const defaultEnvStatusReleaseTimeout = 60 * time.Second

// envStatusDocument is the `--json` output contract of `forge env status <env>`.
//
// It carries exactly what the text report carries, in the same five states,
// and the envelope's `ok` is false in exactly the cases where text mode exits
// non-zero — the house convention (see internal/cli/lint/lint_json.go's
// header) is that the two modes never disagree about the verdict, only about
// the rendering.
//
// THE RELEASE FIELDS ARE FLAT, NOT NESTED, and that is load-bearing rather
// than a style choice: `forge gate record --from` RECOGNISES this document by
// the presence of top-level `bound` and `images` (gate_doc.go's "verify"
// recipe). Moving them under a `release` key would make every recorded
// deploy-verification gate fall through to the generic ok/exit_code reading,
// silently losing the unbound-env-is-SKIPPED distinction — a green check in
// the evidence trail attesting to images nobody checked.
//
// Extensions are ADDITIVE: new fields may be added, but no field is renamed or
// repurposed, so a consumer reading `state` and `tally` keeps working.
type envStatusDocument struct {
	// jsonEnvelope is the F0 head every env verb carries: ok, exit_code and
	// error, stamped from the SAME error the text path returns so the two
	// cannot disagree about the verdict.
	jsonEnvelope
	// Env is the environment name as given on the command line.
	Env string `json:"env"`
	// Lifecycle is the env's declared Bundle.lifecycle ("local",
	// "ephemeral", or "" when unset), as `env shape --json` reports it.
	Lifecycle string `json:"lifecycle"`
	// Bound is false for an env that has never been promoted. That is NOT a
	// failure — it has declared nothing, so there is nothing to be wrong
	// about — and it exits 0 with OK true. It is a separate field rather
	// than an empty Release so a consumer can tell "never promoted" from a
	// binding that somehow carries a blank version.
	Bound bool `json:"bound"`
	// Release is the version label the binding names. Empty when unbound.
	Release string `json:"release,omitempty"`
	// PromotedAt is RFC3339 wall-clock for when the env was PROMOTED — NOT
	// when it was deployed. Promotion writes a pointer; deployment moves
	// bytes, and the gap between the two is the entire thing this command
	// exists to measure. A consumer treating this as a deploy time has
	// reintroduced the bug: it would read as "shipped" for an env that was
	// promoted and then never deployed at all.
	PromotedAt string `json:"promoted_at,omitempty"`
	// KubeContext and Namespace are where the cluster was read, resolved
	// declaratively from the env's KCL. Empty when the target could not be
	// resolved, which is itself an UNREACHABLE condition.
	KubeContext string `json:"kube_context,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	// Source is where a HOSTED env's verdicts came from — "control-plane
	// observer" — because forge cannot read a hosted cluster. Absent for a
	// cluster env, whose source is the cluster itself (kube_context).
	Source string `json:"source,omitempty"`
	// Images is one verdict per DECLARED image. Always non-nil so consumers
	// see `[]` rather than `null`.
	Images []imageVerification `json:"images"`
	// Tally counts the five states. Unreachable stays its own bucket.
	Tally envStatusReleaseTally `json:"tally"`
	// Queued is set when the bound release is ACCEPTED and RECORDED but the
	// control plane holds it on a person (billing): what it waits on and
	// the action URL. exit_code is then 7, and drift in `images` is the
	// expected state of an env whose release has not been applied yet.
	Queued *deployQueuedJSON `json:"queued,omitempty"`
	// Detail carries the one-line human reason for a non-OK result, or the
	// explanation of an unbound env. Empty on a clean verify.
	Detail string `json:"detail,omitempty"`
	// Runtime is the RUNTIME half of the same read: host services and
	// frontends with their ports and log files, the compose infra, and the
	// app/telemetry health checks. Nested (unlike the release fields,
	// which are flat for gate record's sake) because it is a different
	// question with its own vocabulary, and because `gate record` must
	// NOT recognise a status document by anything in here.
	//
	// Nil when the runtime half did not run.
	Runtime *upServicesReport `json:"runtime,omitempty"`
	// Ledger says whether the declaration came from the newest copy of the
	// ledger. Present only for a file ledger (a control plane is the ledger
	// and has no copy to be behind). `behind` / `diverged` make the whole
	// verdict undetermined: the cluster was compared against a release
	// that may not be the one this env is bound to.
	Ledger *ledgerFreshnessReport `json:"ledger,omitempty"`
	// Records is the deploy-source-of-truth half: the bound release's
	// PROVENANCE, the reconciler's CONVERGENCE of it, and the LOCAL SESSIONS running
	// it (doc §6.3, §7.4). Nested for the same reason Runtime is — a
	// different question with its own vocabulary — and ADDITIVE: nothing
	// above it is renamed or repurposed, so `forge gate record --from`
	// keeps recognising this document by top-level `bound` and `images`,
	// and reliant's daemon keeps parsing the fields it already reads.
	//
	// It NEVER affects the verdict. An unreadable records store sets
	// Detail inside here and leaves ok/exit_code alone; see
	// env_status_records.go's header.
	//
	// Nil when the records were not read.
	Records *envStatusRecords `json:"records,omitempty"`
	// OfflineCache lists the releases this machine could deploy with the
	// control plane down (`--offline-ledger`). Only for an env whose ledger is
	// a control plane. Never affects the verdict.
	OfflineCache *offlineCacheReport `json:"offline_cache,omitempty"`
	// Identity is a hosted env's managed identity (forge.ManagedIdentity) as
	// its control plane reports it. Absent when the env declares none or is
	// not hosted. Never affects the verdict.
	Identity *envIdentityReport `json:"identity,omitempty"`
	// Apply says whether the bound promotion was APPLIED, as the ledger
	// records it: `release` is what the env is bound to, and a failed apply
	// means it does not run it (apply.last_applied_release is what likely
	// does). A consumer that finishes or verifies a release checks
	// apply.status == "passed", never `release` alone. Absent when unbound.
	Apply *envApplyReport `json:"apply,omitempty"`
}

// envTarget is WHERE an environment runs: the kubectl context and the
// namespace. Either may be empty, which means the env's KCL declares no
// cluster — a host-only or compose env, which has nothing to verify.
type envTarget struct {
	KubeContext string
	Namespace   string
}

// envTargetResolver answers "where does this env run".
//
// A seam rather than a direct call because resolving it for real renders the
// env's KCL, which needs a whole project on disk. A unit test asserting the
// DRIFT verdict should not have to construct one — but it must still exercise
// the same code path production does, so the production command injects the
// real resolver rather than leaving this nil and branching on it.
type envTargetResolver interface {
	Resolve(ctx context.Context, projectDir, envName string) envTarget
}

// kclTargetResolver is the production resolver.
//
// The kubectl context is DECLARATIVE: forge.K8sCluster.cluster in the env's
// KCL IS the context name. It is never the ambient current-context — a command
// whose entire output is a claim about a named environment must not silently
// read whichever cluster someone last switched to, or the claim is about an
// unknown cluster.
type kclTargetResolver struct{}

func (kclTargetResolver) Resolve(ctx context.Context, projectDir, envName string) envTarget {
	// A load failure is not fatal: expectedClusterForEnv reads the config
	// only for its dev-env fallback, and a broken forge.yaml has already
	// failed whatever command loaded it first.
	cfg, _ := config.LoadProjectDir(projectDir)
	return envTarget{
		KubeContext: expectedClusterForEnv(ctx, cfg, envName),
		Namespace:   k8sClusterNamespaceForEnv(ctx, envName),
	}
}

// envLifecycleResolver is the optional seam that reads an env's DECLARED
// Bundle.lifecycle. Separate from envTargetResolver so existing resolvers
// need not implement it; one that does not reports "" (unset).
type envLifecycleResolver interface {
	Lifecycle(ctx context.Context, projectDir, envName string) string
}

func (kclTargetResolver) Lifecycle(ctx context.Context, projectDir, envName string) string {
	entities, err := RenderKCL(ctx, projectDir, envName)
	if err != nil || entities == nil {
		return ""
	}
	return entities.Lifecycle
}

func declaredLifecycle(ctx context.Context, r envTargetResolver, projectDir, envName string) string {
	if lr, ok := r.(envLifecycleResolver); ok {
		return lr.Lifecycle(ctx, projectDir, envName)
	}
	return ""
}

// envStatusOptions carries the flags and the injected seams into the run
// function.
type envStatusOptions struct {
	Timeout time.Duration
	// JSON switches the RENDERING only. Every verdict, every state and
	// every exit code is computed before either renderer runs, so the two
	// modes cannot disagree about what was found.
	JSON bool
	// Lister reads the cluster.
	Lister clusterImageLister
	// Resolver says where the env runs.
	Resolver envTargetResolver
	// Bindings is the ledger holding what the env is SUPPOSED to run. The
	// third injected seam, for the same reason as the other two: this
	// command's whole job is comparing a declaration against reality, and a
	// test of that comparison should be able to state the declaration
	// directly instead of staging a file on disk to imply it.
	Bindings bindingStore
	// Signal and Verbose scope and expand the RUNTIME half: one signal
	// only (app, metrics, traces, logs, profiles), and evidence for every
	// check rather than only the failures. They live here so the one
	// options struct carries the whole command's flags — the two halves
	// are one read, and splitting their options would make the caller
	// decide which struct a flag belongs to.
	Signal  string
	Verbose bool
	// Runtime is the already-collected runtime half, folded into this
	// document so `--json` emits exactly ONE document. Two documents on
	// stdout produce a stream no `jq` invocation can read, and the failure
	// looks like malformed JSON rather than like two halves sharing an
	// output.
	Runtime *upServicesReport
	// Records reads the env's provenance / apply / session records. Nil
	// uses readEnvRecords, which selects the store from the same
	// declaration that selects the ledger. The fourth injected seam, for
	// the reason the other three exist: a test of the RENDERING should be
	// able to state the records without a ledger home or a control plane.
	Records envRecordsReader
	// HostedRollout reads a HOSTED env's rollout for a promotion. Nil uses
	// the env's declared control plane (readDeclaredRollout). Setting it
	// also selects the hosted path, so a test can state the observer's
	// answer without a control plane.
	HostedRollout func(ctx context.Context, env, promotionID string) (wireRollout, error)
	// HostedIdentity reads a HOSTED env's managed identity. Nil reads it from
	// the same control plane the rollout is read from — unless HostedRollout
	// is set, when a test that states no identity gets none.
	HostedIdentity func(ctx context.Context, env string) (identityStatusDocument, error)
}

// runEnvStatusRelease resolves the binding, reads the cluster, prints a per-image
// report, and returns an error carrying the right exit code.
func runEnvStatusRelease(ctx context.Context, envName string, opts envStatusOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultEnvStatusReleaseTimeout
	}
	if opts.Lister == nil {
		opts.Lister = kubectlImageLister{}
	}
	if opts.Resolver == nil {
		opts.Resolver = kclTargetResolver{}
	}

	projectDir := projectDirForKCL()
	if opts.Bindings == nil {
		store, err := bindingStoreFor(ctx, projectDir, envName)
		if err != nil {
			return err
		}
		opts.Bindings = store
	}
	binding, bound, err := opts.Bindings.Current(ctx, envName)
	if err != nil {
		return fmt.Errorf("read the promotion ledger for %s (%s): %w", envName, opts.Bindings.Location(), err)
	}
	// Read BEFORE any verdict, including the unbound one: "never promoted"
	// in a checkout that has not pulled the first promotion is the stale
	// case too.
	ledger := ledgerFreshnessOf(ctx, opts.Bindings, envName)
	staleErr := staleLedgerError(envName, ledger)

	// Read BEFORE the unbound branch, because an UNBOUND env still has
	// records worth showing — a local stack is running right now, or an
	// apply of an unreleased bundle was abandoned — and those are exactly
	// the facts that explain why nothing is bound yet. Folding the failure
	// into the document (collectEnvRecords) is what keeps this read off the
	// verdict path.
	records := collectEnvRecords(ctx, opts.Records, projectDir, envName, time.Now().UTC())
	offline := collectOfflineCache(opts.Bindings, envName)

	if !bound {
		return reportUnboundEnv(declaredLifecycle(ctx, opts.Resolver, projectDir, envName), envName, opts.JSON, opts.Runtime, ledger, staleErr, records, offline)
	}
	if len(binding.Resolved) == 0 && binding.Snapshot == nil {
		// A binding with no resolved digests is a defective binding: it
		// names a release but froze no artifacts, so it cannot be checked
		// against anything. Reporting "0 drifted" would read as success.
		return fmt.Errorf("environment %s is bound to release %s but the binding resolved NO image digests — "+
			"there is nothing to verify against.\n"+
			"  Re-deploy it with: forge env deploy %s %s",
			envName, binding.Release, binding.Release, envName)
	}

	applied := envApplyOf(ctx, opts.Bindings, projectDir, envName, binding)
	if !opts.JSON {
		fmt.Printf("Verifying environment %s against release %s (%d image(s))\n", envName, binding.Release, len(binding.Resolved))
		if !binding.PromotedAt.IsZero() {
			// Printed with the caveat attached. The timestamp is the single
			// most misread field in the ledger — it looks like a deploy time
			// and is not — so the report states what it actually means rather
			// than leaving the reader to assume.
			fmt.Printf("  promoted %s (promote time, NOT deploy time — that gap is what this command checks)\n", formatLedgerTime(binding.PromotedAt))
		}
		if line := latestApplyLine(projectDir, envName, binding.ID, time.Now()); line != "" {
			fmt.Println(line)
		} else if line := recordedApplyGateLine(binding); line != "" {
			fmt.Println(line)
		}
		// Before any per-image line: a reader who stops at "release" must
		// not walk away believing the env runs it.
		if line := applied.currentLine(binding.Release); line != "" {
			fmt.Println(line)
		}
		if ledger != nil {
			// Said up front, before any per-image line: a reader who
			// sees DRIFT first and this last has already started
			// chasing the wrong problem.
			fmt.Printf("  ledger   %s (%s)\n", strings.ToUpper(ledger.State.String()), ledger.Detail)
		}
	}

	var (
		results                []imageVerification
		kubeContext, namespace string
		source                 string
		queued                 *deployQueuedError
		identity               *envIdentityReport
	)
	if binding.Snapshot != nil {
		source = "snapshot consumer-slot observation"
		results = snapshotStatusImages(ctx, *binding.Snapshot, opts)
	} else if _, hosted := opts.Bindings.(*hostedStore); hosted || opts.HostedRollout != nil {
		// A HOSTED env: forge cannot read its cluster, so the control
		// plane's observer is the witness (env_verify_hosted.go). The
		// verdict below and the exit codes are the cluster path's own.
		source = hostedObserverSource
		if !opts.JSON {
			fmt.Printf("  source   %s (forge cannot read a hosted env's cluster)\n", source)
		}
		results, queued, identity = verifyHostedWithIdentity(ctx, envName, binding, opts)
	} else {
		results, kubeContext, namespace = verifyCluster(ctx, projectDir, envName, binding.Resolved, opts)
	}
	tally := tallyEnvStatusRelease(results)
	failure := envStatusReleaseVerdict(envName, binding.Release, staleErr, tally)
	if queued != nil && staleErr == nil {
		// QUEUED OUTRANKS DRIFT. The declared release is accepted and
		// waiting on a person, so "the env does not run it yet" is the
		// expected state, not a defect: exit 7 with what it waits on and
		// where to act, instead of exit 1 blaming the release.
		failure = queued
	}

	if opts.JSON {
		report := envStatusDocument{
			Env:         envName,
			Lifecycle:   declaredLifecycle(ctx, opts.Resolver, projectDir, envName),
			Bound:       true,
			Release:     binding.Release,
			PromotedAt:  formatLedgerTime(binding.PromotedAt),
			KubeContext: kubeContext,
			Namespace:   namespace,
			Source:      source,
			Images:      results,
			Tally:       tally,
			Runtime:     opts.Runtime,
			Ledger:      ledger,
			Records:     &records,

			OfflineCache: offline,
			Identity:     identity,
			Apply:        &applied,
		}
		if queued != nil {
			report.Queued = queued.document()
		}
		if failure != nil {
			report.Detail = failure.Error()
		}
		if report.Images == nil {
			report.Images = []imageVerification{}
		}
		report.stamp(failure)
		if err := writeEnvStatusReleaseJSON(report); err != nil {
			return err
		}
		// The report is already on stdout; returning the same sentinel
		// gives cobra the exit code text mode would have produced.
		return failure
	}

	fmt.Println()

	printEnvStatusReleaseImages(results)

	fmt.Printf("\n%d match, %d drifted, %d missing, %d untagged, %d unreachable\n",
		tally.Match, tally.Drift, tally.Missing, tally.Untagged, tally.Unreachable)
	if identity != nil {
		fmt.Println(identity.line())
	}

	// BEFORE the failure return, not after. A DRIFT whose cause is an apply
	// that never finished is a different problem from one whose cause is a
	// bad release, and printing the records only on success would withhold
	// that distinction in precisely the case a reader needs it.
	writeEnvStatusRecords(os.Stdout, records)
	writeOfflineCache(os.Stdout, envName, offline)

	if failure != nil {
		return failure
	}

	if tally.Untagged > 0 {
		fmt.Printf("\nNote: %d image(s) run by mutable tag and could not be digest-checked. Deploy without --no-digest to pin them.\n", tally.Untagged)
	}
	return nil
}

// verifyHosted reads the env's CURRENT promotion's rollout from its control
// plane and maps it onto the five states. A read failure is every pinned image
// UNREACHABLE (exit 2), never a guess.
//
// A QUEUED promotion (phase HELD: accepted, recorded, waiting on a person) is
// returned as such beside the rows. Its workloads still run the PREVIOUS
// release or nothing, so the per-image rows read as drift — which is true and
// is not the verdict: the env is where it should be while it waits.
func verifyHosted(ctx context.Context, envName string, binding release.Promotion, opts envStatusOptions) ([]imageVerification, *deployQueuedError) {
	results, queued, _ := verifyHostedWithIdentity(ctx, envName, binding, opts)
	return results, queued
}

// verifyHostedWithIdentity is verifyHosted plus the env's managed identity,
// read from the same control plane (resolved once). The identity is reported,
// never judged: it does not change the release verdict.
func verifyHostedWithIdentity(ctx context.Context, envName string, binding release.Promotion, opts envStatusOptions) ([]imageVerification, *deployQueuedError, *envIdentityReport) {
	readCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	readRolloutFn, readIdentityFn := opts.HostedRollout, opts.HostedIdentity
	if readRolloutFn == nil {
		target, err := resolveDeclaredWaitTarget(readCtx, envName)
		if err != nil {
			return unreachableVerifications(binding.Resolved, err), nil, nil
		}
		readRolloutFn = func(ctx context.Context, _, promotionID string) (wireRollout, error) {
			return readRollout(ctx, target.Client, target.EnvironmentID, promotionID)
		}
		if readIdentityFn == nil {
			read := identityReader(target.Client, target.EnvironmentID)
			readIdentityFn = func(ctx context.Context, _ string) (identityStatusDocument, error) { return read(ctx) }
		}
	}
	rollout, err := readRolloutFn(readCtx, envName, binding.ID)
	if err != nil {
		return unreachableVerifications(binding.Resolved, err), nil, nil
	}
	var queued *deployQueuedError
	if rollout.Phase == wireRolloutPhaseHeld {
		queued = &deployQueuedError{Env: envName, Release: binding.Release, PromotionID: binding.ID, Holds: rollout.Holds}
	}
	var identity *envIdentityReport
	if readIdentityFn != nil {
		w := &identityWatch{read: func(ctx context.Context) (identityStatusDocument, error) { return readIdentityFn(ctx, envName) }}
		identity = w.observe(readCtx)
	}
	return verifyHostedRollout(rollout), queued, identity
}

// readDeclaredRollout is the production hosted read: the env's declared
// control plane, through the same target resolution and GetRollout call
// `forge env status --wait` uses (F3), so verify and wait cannot disagree about what
// the observer saw.
func readDeclaredRollout(ctx context.Context, env, promotionID string) (wireRollout, error) {
	target, err := resolveDeclaredWaitTarget(ctx, env)
	if err != nil {
		return wireRollout{}, err
	}
	return readRollout(ctx, target.Client, target.EnvironmentID, promotionID)
}

// envStatusReleaseVerdict is the ONE decision, made before either renderer runs, so
// "--json exits identically to text mode" is structural.
func envStatusReleaseVerdict(envName, release string, staleErr error, tally envStatusReleaseTally) error {
	switch {
	case staleErr != nil:
		// FIRST, above drift. A drift against a declaration that is not
		// the ledger is not evidence the release is wrong — after a
		// release it is the expected result of reading the old one — and
		// a match against it is not evidence the release shipped. The
		// per-image findings are still printed; the verdict is "could not
		// determine".
		return staleErr
	case tally.Drift > 0 || tally.Missing > 0:
		return exitCodeError{code: 1, msg: fmt.Sprintf(
			"environment %s does not match its binding: %d image(s) drifted, %d missing (declared release %s)",
			envName, tally.Drift, tally.Missing, release)}
	case tally.Unreachable > 0:
		return exitCodeError{code: 2, msg: fmt.Sprintf(
			"environment %s could not be checked: %d image(s) unreachable (cluster, context, credentials, or a stale observation), %d matched, 0 drifted",
			envName, tally.Unreachable, tally.Match)}
	}
	return nil
}

// verifyCluster is the kubectl path for an env forge can reach: unchanged.
func verifyCluster(ctx context.Context, projectDir, envName string, resolved map[string]string, opts envStatusOptions) (results []imageVerification, kubeContext, namespace string) {
	target := opts.Resolver.Resolve(ctx, projectDir, envName)
	kubeContext, namespace = target.KubeContext, target.Namespace

	switch {
	case kubeContext == "" || namespace == "":
		// Cannot even address the cluster. This is UNREACHABLE, not drift:
		// nothing has been learned about what the env is running.
		//
		// The two reasons a target does not resolve need DIFFERENT
		// messages, and conflating them sends the reader to the wrong
		// place. An env bound in the ledger but absent from this checkout
		// (a release cut on a branch that has the env, verified from one
		// that does not) is not a KCL problem at all — telling that reader
		// to add a field to deploy/kcl/<env>/main.k names a file they will
		// not find, which is how a diagnostic costs more time than it saves.
		mainK := filepath.Join(projectDir, "deploy", "kcl", envName, "main.k")
		if _, statErr := os.Stat(mainK); statErr != nil {
			results = unreachableVerifications(resolved, fmt.Errorf(
				"environment %s is bound in the ledger but not declared in this checkout (%s does not exist) — "+
					"verify from a checkout that declares it", envName, mainK))
			break
		}
		missing := "forge.K8sCluster.cluster"
		if namespace == "" && kubeContext != "" {
			missing = "forge.K8sCluster.namespace"
		} else if namespace == "" {
			missing = "forge.K8sCluster.cluster and .namespace"
		}
		results = unreachableVerifications(resolved, fmt.Errorf(
			"could not determine where %s runs — no %s declared in %s (a host-only or compose env has no cluster to verify)",
			envName, missing, mainK))
	default:
		if !opts.JSON {
			fmt.Printf("  cluster  %s (namespace %s)\n", kubeContext, namespace)
		}
		readCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
		running, lerr := opts.Lister.ListWorkloadImages(readCtx, kubeContext, namespace)
		if lerr != nil {
			results = unreachableVerifications(resolved, lerr)
		} else {
			results = verifyEnvImages(running, resolved)
		}
	}
	return results, kubeContext, namespace
}

// reportUnboundEnv is verify's answer for an env with no binding.
//
// NO BINDING IS NOT A FAILURE. An env that has never been promoted has
// declared nothing, so there is nothing to be wrong about. Exiting 1 here
// would make the command red for a perfectly healthy env that simply does
// not use releases, and a permanently-red gate is a deleted gate. Say plainly
// what the state is and exit 0 — unless this checkout's copy of the ledger is
// stale (staleErr), in which case "never promoted" is not known either.
func reportUnboundEnv(lifecycle, envName string, jsonOut bool, runtime *upServicesReport, ledger *ledgerFreshnessReport, staleErr error, records envStatusRecords, offline *offlineCacheReport) error {
	const unboundDetail = "no release binding — the environment has never been promoted, so nothing is declared and there is nothing to verify"
	if jsonOut {
		// Still a complete, valid report. `bound: false` is the
		// machine-readable marker; `ok` stays true because this is a
		// healthy state, not a failure. Images is non-nil so a consumer
		// ranging over it sees `[]`, not `null`.
		report := envStatusDocument{
			Env:       envName,
			Lifecycle: lifecycle,
			Bound:     false,
			Images:    []imageVerification{},
			Detail:    unboundDetail,
			Runtime:   runtime,
			Ledger:    ledger,
			Records:   &records,

			OfflineCache: offline,
		}
		if staleErr != nil {
			report.Detail = staleErr.Error()
		}
		report.stamp(staleErr)
		if err := writeEnvStatusReleaseJSON(report); err != nil {
			return err
		}
		return staleErr
	}
	if staleErr != nil {
		return staleErr
	}
	fmt.Printf("Environment %s has no release binding — nothing is declared, so there is nothing to verify.\n", envName)
	fmt.Printf("  Bind one with: forge env deploy %s <version>\n", envName)
	// An unbound env can still be running a local stack, which is often
	// the whole story: "nothing is promoted AND two worktrees are up" is
	// the normal state of a dev env, and reading it as "nothing is
	// happening" is what the records block prevents.
	writeEnvStatusRecords(os.Stdout, records)
	writeOfflineCache(os.Stdout, envName, offline)
	return nil
}

// ledgerFreshnessOf asks the binding store whether its copy of env's ledger
// is the newest one, or returns nil for a store that has no copy to be
// behind (a control plane).
func ledgerFreshnessOf(ctx context.Context, store bindingStore, env string) *ledgerFreshnessReport {
	checker, ok := store.(ledgerFreshnessChecker)
	if !ok {
		return nil
	}
	report := checker.LedgerFreshness(ctx, env)
	return &report
}

// staleLedgerError is the verdict when the declaration came from a stale copy
// of the ledger: exit 2, "could not determine", with the fix. nil when the
// ledger is current, ahead, unknown, or hosted.
func staleLedgerError(env string, ledger *ledgerFreshnessReport) error {
	if ledger == nil || !ledger.State.stale() {
		return nil
	}
	fix := "git pull (or rebase onto " + ledger.Ref + ") and re-run"
	if ledger.State == ledgerDiverged {
		fix = "reconcile the two promotion logs (each holds entries the other lacks), then re-run"
	}
	return exitCodeError{code: exitUndetermined, msg: fmt.Sprintf(
		"cannot verify %s: this checkout's promotion ledger is %s %s — %s.\n  Fix: %s",
		env, ledger.State, ledger.Ref, ledger.Detail, fix)}
}

// printEnvStatusReleaseImages renders the per-image block of the text report — the
// human-readable twin of the Images array --json emits. It is the whole of
// what text mode says about individual images, so it carries no verdict and
// returns nothing: runEnvStatusRelease decides pass/fail once, before either renderer
// runs, and this only describes what was found.
func printEnvStatusReleaseImages(results []imageVerification) {
	for _, r := range results {
		fmt.Printf("  %-12s %s\n", r.State, r.Image)
		// Drift prints both digests on their own labelled lines. This is
		// the case the command exists for, and the reader needs to copy
		// both values out; burying them in a prose sentence makes that
		// harder for no gain.
		if r.State == imageDrift {
			fmt.Printf("      declared  %s\n", r.Declared)
			fmt.Printf("      running   %s\n", r.Running)
		} else if r.Running != "" && r.State != imageMatch {
			fmt.Printf("      running   %s\n", r.Running)
		}
		if len(r.Workloads) > 0 && r.State != imageMatch {
			fmt.Printf("      workloads %s\n", joinWorkloads(r.Workloads))
		}
		if r.Detail != "" {
			fmt.Printf("      %s\n", r.Detail)
		}
	}
}

// writeEnvStatusReleaseJSON emits the report to stdout, indented, per the house
// convention (flag `--json`, indented json.Encoder to stdout).
func writeEnvStatusReleaseJSON(report envStatusDocument) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// joinWorkloads renders the workload list, capping it so an env with fifty
// replicas of a shared image does not bury the digests the reader came for.
func joinWorkloads(names []string) string {
	const max = 4
	if len(names) <= max {
		return joinComma(names)
	}
	return fmt.Sprintf("%s (+%d more)", joinComma(names[:max]), len(names)-max)
}

func joinComma(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
