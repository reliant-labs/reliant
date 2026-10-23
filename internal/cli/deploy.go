package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
	"github.com/reliant-labs/forge/internal/projectstore"
	"github.com/reliant-labs/forge/internal/secrets"
	"github.com/reliant-labs/forge/internal/statefile"
	"github.com/reliant-labs/forge/kcl"
	"github.com/reliant-labs/forge/pkg/deploystate"
	"github.com/reliant-labs/forge/pkg/release"
)

// deployCmdLong is `forge env deploy`'s help text, hoisted out of the command
// declaration so the constructor reads as a declaration rather than as a
// document with a cobra.Command buried in it.
const deployCmdLong = `Make <environment> run a release. TWO FORMS, and the difference is whether you
name a version.

  forge env deploy prod               # SHIP THIS CHECKOUT: build, push, cut a
                                      # release, plan, confirm, deploy, wait
  forge env deploy prod v1.4.0        # deploy a release that already exists
  forge env deploy prod --from staging  # exactly what staging runs

NO VERSION DOES ALL THE DEPLOYMENT BITS. In order: build the env's artifacts at
the current checkout (images, static artifacts) exactly as ` + "`forge env build`" + `
does, push them, cut a release named ` + "`<YYYYMMDD>.<HHMMSS>-<tree12>`" + ` for this
tree, record the env's declared shape, compute the plan, ask you to confirm it,
then promote, apply and wait.

A RELEASE THAT ALREADY HOLDS THIS CHECKOUT IS DEPLOYED, NOT REBUILT. When a
release — whatever it is named, ` + "`forge env build --release`" + `'s included —
records this checkout's clean tree and this forge version, covers every
artifact the env declares, and every image it pins still resolves in its
registry, nothing is built or cut: forge prints "reusing release <v>" and
deploys it. Otherwise it prints "cutting new release <v> because …" before
building. A dirty checkout, another forge version, a -D option, an artifact the
release lacks, or an image the registry no longer holds each cut a new one. A nested git checkout inside the
project (a parked worktree, a cloned repo) is another repository, not a change
to this one, and does not make the checkout dirty; the decision line names the
ones it ignored.

A DIRTY CUT ON A SHARED ENV ASKS FIRST. When the checkout has uncommitted
changes and the env is hosted or runs on a cluster that is not a local dev
cluster, forge prompts (default no) before building, because the release it
would cut describes no commit. With no terminal it refuses (exit 5) having
built nothing; --yes accepts it. A local env never asks.

--dry-run PREDICTS THIS. It prints the same decision line the real run prints
-- "would reuse release <v>" or "would cut new release <v> because …" -- from
the same function, and --json carries it as release_decision.

A SCOPED DEPLOY NEEDS A RELEASE. --frontends-only and --target ship part of the
env, so they are refused with NO version: a no-version deploy cuts a release
over every artifact, and cutting one that ships only some of them would record
a release that does not describe what is running. Name the version
(` + "`forge env deploy prod v1.7.1 --frontends-only`" + `) or deploy everything
(` + "`forge env deploy prod`" + `). --dry-run and --explain are unaffected: they cut
nothing, so a scoped preview is an honest question.

NAMING A VERSION BUILDS NOTHING. It deploys a release that was already cut — a
redeploy, a move to an older one, or a spec-change deploy (the KCL moved and the
release did not: name the version the env already runs). A version nobody cut is
an error whose fix is ` + "`forge env deploy <env>`" + `, the form that builds it.

NOTHING IS WRITTEN UNTIL YOU CONFIRM. A promotion IS the deploy — the converger
picks it up within minutes — so the plan is printed and approved BEFORE the
write, not after it. On a terminal you are prompted (default no). In CI pass
--yes, which means "I read the plan". With no terminal and no --yes the command
refuses (exit 5) having built, pushed and cut but written NO promotion, so
approving it afterwards needs no rebuild. --plan-only stops after the plan and
is the first stage of a two-stage pipeline.

--yes AND --approve ARE NOT THE SAME APPROVAL. --yes means "I read the plan"
and approves whatever forge computes at the moment that command runs.
--approve <digest> approves the plan you actually READ: if Live moved in
between — another deploy landed, a new bundle was applied, drift appeared —
the digest no longer matches and the deploy is refused (exit 3) instead of
shipping a change set nobody saw. Use --yes for a one-shot deploy; use
--approve whenever the plan and the approval are separate steps.

THE TWO-STAGE PIPELINE, in full:

  PLAN=$(forge env deploy prod --plan-only --json)    # builds, cuts, writes NO promotion
  VERSION=$(jq -r .target.release    <<<"$PLAN")      # the release stage one cut
  DIGEST=$( jq -r .deploy_plan.digest <<<"$PLAN")     # the plan to bind the approval to
  forge env deploy prod "$VERSION" --approve "$DIGEST"

` + "`next_step`" + ` in stage one's document is that final command already assembled,
including ` + "`--acknowledge-destructive <codes>`" + ` when the plan carries stop-class
findings — those codes cannot be written in advance, because a code is not
known until the plan is computed. Paste it rather than rebuilding it.

A nil ` + "`deploy_plan`" + ` means no plan could be computed (a never-built env, or a
control plane that predates bundles) — NOT "no changes". There is then no
digest to approve, so ` + "`next_step`" + ` falls back to the --yes form and says why.

THE VERB IS record + apply + wait, AND THAT IS NOT OPTIONAL. Recording a
binding ships nothing, so a step that only recorded one reported success before
any byte had moved and the release's actual failure surfaced minutes later with
nothing connecting the two. So the health gate is ON by default; --no-wait is
how you opt out, and it says what to run instead.

A HOSTED DEPLOY CAN BE QUEUED, NOT REFUSED. When what the env runs needs
something only a person can provide — billing for its hosted workloads,
managed database or static sites — the control plane still
ACCEPTS the deploy: the release and its bundle are recorded, the promotion is
written, and the env shows "waiting on billing". It goes live by itself the
moment billing is set up; nothing is re-run. forge prints one block — what it
waits on, why, and the URL to act at — and exits 7. --wait instead blocks
(up to --timeout) until it is live. A newer deploy to the env replaces a
queued one.

WHO APPLIES IT IS DECLARED, NOT CHOSEN. An env whose KCL declares
forge.ControlPlane is converged by that control plane — forge records the
promotion and waits on the rollout the server computes. Every other env is
SELF-MANAGED: the same command renders the new binding and applies it from this
machine, and that apply's per-resource rollout wait IS the health gate. Both
reach "the release is live or this command is red"; which machinery got there
is an implementation detail of where the env runs.

` + "`forge env build <env> --release <version>`" + ` builds the env-agnostic images
ONCE, captures their content-addressed digests, and cuts a release. Naming that
version here advances it BY REFERENCE: one entry — env, release, and the
per-image digests frozen at this moment — appended to the env's append-only
promotion ledger. No image is rebuilt, so the exact bytes cut as <version> are
what every env running that release ships, byte-identical. This eliminates the
per-env rebuild that re-cross-compiles (and can drift arch/tag) for every
environment.

WHERE THE LEDGER LIVES is declared by the environment, not chosen by a flag:
an env whose KCL declares forge.ControlPlane records promotions on that control
plane; every other env records them in .forge/promotions/<env>.jsonl.

EVERY DEPLOY IS A NEW ENTRY, NOT AN EDIT. Re-deploying the release an env
already runs appends nothing and still applies and waits; a CI retry is safe.

THERE IS NO ROLLBACK. Recovery is ROLL FORWARD: cut a release with the fix and
deploy it. Binding an env to an OLDER release is still possible — it is an
ordinary deploy — but it cannot undo the newer release: that release's
migrations stay applied and the data it wrote stays written, so the older code
runs against a schema it was never tested on. The plan labels such a move
` + "`direction: BEHIND`" + ` and says so; read it before you write it.

SEE THE CHANGE BEFORE IT IS WRITTEN. --plan computes the ENTIRE change set and
writes nothing: the release the env runs now versus the one it would move to,
every image classified as unchanged / changed / added / removed (with both
digests where they differ), the git commits between the two releases, and —
the fact most worth reading twice — the DIRECTION. A deploy to an older
release is reported as BEHIND rather than left for you to infer from version
numbers. The plan and the real deploy are computed by the SAME function, so the
preview cannot disagree with the write.

EVERY RELEASE DEPLOY IS A COMPARE-AND-SET. The write asserts that the env is
still on the promotion the plan read (` + "`current.promotion_id`" + ` in --json),
and is REFUSED if someone else moved it since — so a hotfix that lands while a
pipeline waits for approval turns the pipeline red instead of being
overwritten. No flag is needed. --expect-current <id> replaces the planned
value with one captured earlier (e.g. when the approval was requested);
` + "`--expect-current unbound`" + ` (or --expect-unbound) asserts the env has never
been promoted. Re-deploying the release the env already runs is a no-op
whatever the expectation says, so a retried success is never a conflict.

Exit codes (release deploys):
  0  deployed and healthy, or already on this release (no-op), or --plan /
     --plan-only, or a confirmation answered "no" (nothing was written)
  1  failed: invalid input, unreadable ledger, release not found, DEGRADED
  2  undetermined — we could not look (the control plane was unreachable)
  3  promotion_conflict / source_moved — the env (or --from's source) moved
     since the plan was read. Stop and look; retrying would overwrite it
  4  rollout_in_flight / environment_pinned — declined, nothing lost. Wait
     and retry, or pass --supersede (recorded) to replace an unfinished rollout
  5  plan_unconfirmed — the plan was printed and nobody approved it: no
     terminal to prompt on and no --yes. NOTHING was promoted. Add --yes
  6  superseded — the env was promoted past the promotion being waited on
  7  queued — the deploy was ACCEPTED and RECORDED and the control plane is
     holding it on a person (today: billing for the hosted workloads or
     database it runs). Nothing failed and nothing is to be re-run: it goes
     live on its own once they act. The output names what it waits on and
     the action URL (--json: .queued). --wait blocks until it is live instead
  8  the wait's budget expired while the rollout was still progressing

5 USED TO MEAN THE TIMEOUT, which is now 8. The confirmation gate took 5
because it is the one outcome that must be impossible to misread as success:
a pipeline that upgrades without adding --yes exits 5, and even a handler
that still reads 5 as "the wait timed out" fails the job rather than passing
it. Pre-1.0, so this is a clean renumber with no alias.

--json carries the same outcome: ` + "`applied`" + `, ` + "`confirmed`" + ` (whether the gate let
the write happen), and a ` + "`refusal`" + ` object naming what was expected and what is
actually there.

Examples:
  forge env build prod --release v1.4.0            # build once, cut the release
  forge env deploy staging v1.4.0 --plan           # what WOULD change (writes nothing)
  forge env deploy staging v1.4.0 --plan --json    # the same, machine-readable
  forge env deploy staging v1.4.0                  # record, apply, wait
  forge env deploy prod --from staging             # the bytes that passed staging
  forge env deploy prod v1.3.0 --plan | grep BEHIND  # catch a backwards move
  forge env deploy prod v1.4.0 --gate e2e.json     # freeze evidence onto the entry
  ID=$(forge env deploy prod v1.4.0 --plan --json | jq -r '.current.promotion_id // "unbound"')
  forge env deploy prod v1.4.0 --expect-current "$ID"  # after approval: exit 3 if prod moved

─────────────────────────────────────────────────────────────────────────────
THE APPLY (both paths, and the whole of a no-version deploy)

Deploy each service to the target declared on its Service.deploy block.

Supported deploy targets (declared in deploy/kcl/<env>/main.k):

  * forge.K8sCluster — Kubernetes deployment via render → kubectl apply
    → wait-rollouts. Forge auto-creates a k3d cluster for dev.
  * forge.Compose    — docker compose pull + up -d.

forge.HostDeploy and forge.BuildOnly are skipped by deploy — those are
owned by forge run / forge env up and forge build respectively.

Safety (declarative context): the kubectl context is read SOLELY from the
env's KCL — forge.K8sCluster.cluster IS the kubectl context name (e.g.
"gke_<project>_<region>_prod"; defaults to k3d-<project> for dev). Every
kubectl call in the apply/wait/prune/secrets path runs
--context <declared> per command, so the deploy applies to EXACTLY the
cluster the env declares — independent of whatever context is currently
active. There is NO CLI override and NO fall-back to the current context:
the binding lives in the env file, full stop, so you can't deploy the
wrong env to the wrong cluster. forge fails fast (even under --dry-run) if
the declared cluster has no matching kubectl context — the only remedy is
to fix your kubeconfig or the KCL forge.K8sCluster.cluster.

Use --explain to print the declared context, whether it exists in your
kubeconfig, and the verdict without applying.

Machine-readable output: --json emits ONE JSON document covering the whole
invocation, with the same exit code text mode produces. It reports the MODE
actually performed (explain / dry_run / apply) so a consumer never
has to infer whether bytes moved; the guard verdict, the target cluster +
namespace (every declared context, for a multi-cluster env); whether the
preflight ran and its findings as structured entries; per-image digest-vs-tag
pinning, so a deploy shipping a MUTABLE reference is visible rather than
implied; the resource identities applied (kind/name — a diffable list, not a
YAML dump); and the per-resource rollout outcome as three distinct states:
ready, failed, and timed_out / not_waited. A timeout is neither a success nor a
failure — it is the absence of an answer — and the document keeps all three
apart. The human output moves to stderr so stdout carries exactly one document.
Works with --explain and --dry-run, which is how a UI previews a deploy before
asking anyone to confirm it.

Deployability preflight: before the first apply (remote/cloud clusters),
forge verifies against the LIVE target that every Secret KEY the rendered
manifests reference is provisioned and every container image: resolves in
its registry. A missing key (CreateContainerConfigError) or image
(ImagePullBackOff) is reported up front — all at once — and the deploy
refuses to apply, instead of surfacing one pod crash at a time mid-rollout.
The preflight is skipped for local dev clusters and runs under --dry-run as
a pure read-only check. Bypass with --skip-preflight.

Use --target <app> (repeatable) to deploy ONLY the named application(s)
instead of the whole env bundle. It filters by app NAME — service,
operator, or frontend: the K8sCluster apply keeps the targeted app's
workload manifests plus all shared resources (Namespace, the shared
ConfigMap/Secret, RBAC), and the External/Compose dispatch + rollout-wait
are scoped to the named apps. A typo'd target errors with the list of
available app names. Targeting an operator (e.g. workspace-controller)
applies just that operator's Deployment + cluster RBAC.

k8s-only deploy: naming only backend apps via --target is itself the
"k8s without touching the frontend" path — a Firebase frontend isn't in
the --target set, so its build+deploy step never runs. To ship the WHOLE
backend bundle while skipping the frontend (without enumerating every
service), pass --skip-frontend: the k8s apply runs as normal and the
Frontend (e.g. Firebase) build+deploy dispatch is skipped.

Examples:
  forge env deploy dev                          # Deploy to dev (local k3d)
  forge env deploy staging --tag v1.2           # Deploy to staging with specific tag
  forge env deploy prod --dry-run               # Preview prod manifests (guard runs)
  forge env deploy prod --explain               # Show the declared-cluster guard verdict
  forge env deploy dev --namespace custom-ns    # Override namespace
  forge env deploy dev --target admin-server    # Deploy only the admin-server app
  forge env deploy prod --target workspace-controller # Deploy only that operator
  forge env deploy prod --skip-frontend         # Deploy backend k8s, skip Firebase`

func newDeployCmd() *cobra.Command {
	// ONE struct and not twenty locals: the flag targets ARE the fields
	// dispatchDeployCmd reads, so binding them directly removes the
	// hand-copied struct literal that used to sit in RunE — the place a
	// newly added flag was silently dropped by forgetting one line.
	var apply deployCmdFlags

	cmd := &cobra.Command{
		Use:   "deploy <environment> [version]",
		Short: "Make an environment run a release: record it, apply it, and wait for health",
		Long:  deployCmdLong,
		// <env> plus an OPTIONAL version. --from can supply the version
		// instead, and no version at all is the spec-change deploy.
		Args: cobra.RangeArgs(1, 2),
		// A release deploy's change set IS the output; a cobra usage dump
		// would bury it under the flag list.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 2 {
				apply.promote.version = args[1]
			}
			return dispatchDeployCmd(cmd.Context(), args[0], apply)
		},
	}

	registerDeployApplyFlags(cmd, &apply)
	registerPromoteFlags(cmd, &apply.promote)

	return cmd
}

// registerDeployApplyFlags declares the APPLY half of `forge env deploy`: the
// flags that govern HOW the manifests land, whichever release they pin.
//
// Split from the command declaration for the same reason registerPromoteFlags
// is: the two halves of this verb have different subjects — which bytes, and
// how they are applied — and a 229-line constructor made that invisible.
func registerDeployApplyFlags(cmd *cobra.Command, f *deployCmdFlags) {
	flags := cmd.Flags()
	flags.StringArrayVarP(&f.renderOptions, "option", "D", nil, "Set a render option the env's KCL declares, as name=value (repeatable). Relayed to KCL verbatim — forge does not interpret the value. List an env's options with 'forge env options <env>'.")
	flags.StringVar(&f.tag, "tag", "", "Override the image tag (priority: --tag > .forge/state/build-<env>.json > git describe --tags --always --dirty)")
	flags.BoolVar(&f.dryRun, "dry-run", false, "Print manifests without applying (env-cluster guard still runs)")
	flags.StringVar(&f.namespace, "namespace", "", "Override namespace from environment config")
	flags.BoolVar(&f.explain, "explain", false, "Print the declared-cluster guard decision (declared/current/verdict) and exit")
	flags.StringVar(&f.targetArch, "target-arch", "", "Override target GOARCH for cross-compilation (default: forge.yaml deploy.target_arch, then amd64)")
	flags.BoolVar(&f.prune, "prune", false, "Delete forge-managed Deployments in the namespace that the current KCL render no longer produces (opt-in)")
	flags.StringArrayVar(&f.targets, "target", nil, "Deploy ONLY the named application(s) (service/operator/frontend name; repeatable). Scopes K8sCluster apply to the app's workload + shared resources, and External/Compose dispatch to the named apps. Empty = deploy the whole env bundle (default).")
	flags.BoolVar(&f.skipFrontend, "skip-frontend", false, "Run the k8s apply but skip the Frontend (e.g. Firebase) build+deploy dispatch. The k8s-only path for the whole backend bundle without enumerating every --target.")
	flags.BoolVar(&f.frontendsOnly, "frontends-only", false, "Deploy ONLY the env's shippable frontend(s) — build + ship to Firebase Hosting or a static-site bucket, skipping the entire k8s apply (Services, Operators, CronJobs, gateways). The inverse of --skip-frontend; the native 'ship just the frontend' path that doesn't touch kubectl. Mutually exclusive with --skip-frontend and --target.")
	flags.BoolVar(&f.skipPreflight, "skip-preflight", false, "Skip the deploy preflight (verify referenced Secret keys + container images exist on the live target BEFORE applying). Default-on for remote/cloud clusters; bypass at your own risk.")
	flags.BoolVar(&f.noDigest, "no-digest", false, "Deploy by the mutable :tag even when the build state captured an immutable image digest. By default forge pins the manifest to <image>@sha256:... so a re-tagged/cached layer can't ship; this escape hatch restores tag-based references.")
	flags.BoolVar(&f.offlineLedger, "offline-ledger", false, "Break-glass for a control plane that is down: deploy the env's cached CURRENT release by its recorded digests (nothing is built, the ledger is not contacted) and queue the promotion record, replayed idempotently on the next successful ledger contact. Refuses a hosted tier and a hub-converged env, and a move to any other release. Prints the cache's age; the cache is refreshed by every successful ledger read or write")
	flags.StringVar(&f.offlineTo, "to", "", "With --offline-ledger: deploy this CACHED release instead of the current one — a release version, a cached promotion id, or `previous` (the release before the current one). The rollback for a bad release that took the control plane down. Refused if the release is not in this machine's offline copy (`forge env status <env>` lists what is)")
	flags.BoolVar(&f.jsonOut, "json", false, "Emit machine-readable JSON describing the whole invocation — mode (explain/dry_run/apply), the declared-cluster guard verdict, the target cluster + namespace, the preflight findings, per-image digest-vs-tag pinning, the resource identities applied, and the per-resource rollout outcome (ready / failed / timed_out / not_waited). Works with --explain and --dry-run, which is how a UI previews a deploy. Same exit codes as text mode; the human output moves to stderr so stdout carries exactly one JSON document.")
	flags.StringVar(&f.rolloutMode, "rollout", "wait", "What to do after the manifests land: 'wait' (wait for every Deployment/Job and FAIL if any does not become ready — the default), 'warn' (wait and report, but exit 0), or 'skip' (apply and return immediately).")
	flags.DurationVar(&f.rolloutTimeout, "rollout-timeout", 0, "Per-resource readiness budget (e.g. 90s, 10m). Applies to EACH Deployment and one-shot Job, not the set. Default 5m.")
	flags.BoolVar(&f.rolloutFailFast, "rollout-fail-fast", false, "Stop at the FIRST resource that fails instead of waiting for the rest. Default reports every failure, which is usually what you want when diagnosing a bad deploy.")
	flags.StringArrayVar(&f.rolloutOrder, "rollout-order", nil, "Wait for these applications FIRST, in this order, before the rest (repeatable). A wait ordering, not an apply ordering — Kubernetes converges concurrently — so it controls what a phased deploy reports first: put the migration or the API server here and its failure surfaces before its dependents time out.")
}

// promoteCmdFlags is the RELEASE half of `forge env deploy`'s flag set: the
// flags that only mean something when a version (or --from) names a release to
// move the env to.
//
// Its own type, beside deployCmdFlags rather than merged into it, because the
// two halves answer different questions and the split is what lets
// refusePromoteFlagsWithoutRelease be a loop over "did anyone set one of
// these?" instead of a hand-maintained list that drifts from the flag
// declarations.
type promoteCmdFlags struct {
	// version is the positional release argument, or "" (then --from may
	// supply it, or there is no release half at all).
	version string

	plan          bool
	note          string
	actor         string
	expectCurrent string
	expectUnbound bool
	supersede     bool
	gates         []string
	run           runOptions

	// The O-13 approval gate. yes is the opt-IN: the gate is on by
	// default, which is the whole of O-13 — see deploy_confirm.go.
	yes      bool
	planOnly bool
	// The SERVER-BINDING half (deploy_plan_gate.go). approve names the
	// exact plan reviewed; acknowledgeDestructive names the stop-class
	// findings accepted, which --yes deliberately does not cover.
	approve                string
	acknowledgeDestructive []string
	// rerecordBundle replaces a release's bundle of record when this
	// checkout renders different objects than the one recorded (see
	// release_bundle_of_record.go). Off, such a render is refused.
	rerecordBundle bool
	// skipHubCheck records a hub-converged env's promotion even when the
	// hub reports its reconciler failing (refuseUnreadyHub).
	skipHubCheck bool
	// liveDiff prints the release bundle's client-side diff against each
	// live cluster with the plan (printPlanLiveDiff).
	liveDiff bool

	// --from / --from-promotion, held FLAT rather than as a nested
	// promoteFromOptions so each is a plain flag target like every field
	// above it. They are folded into the options struct at the call site.
	fromEnv         string
	fromPromotionID string

	// The health gate. noWait is the opt-OUT: the gate is on by default,
	// which is the whole of V3.
	noWait   bool
	timeout  time.Duration
	failFast bool
	// wait blocks THROUGH a queued deploy (held on billing) until it is
	// live or --timeout; without it a queued deploy reports and exits 7.
	wait bool
}

// fromSource is the two --from flags as the options struct the release path
// takes.
func (f promoteCmdFlags) fromSource() promoteFromOptions {
	return promoteFromOptions{Env: f.fromEnv, PromotionID: f.fromPromotionID}
}

// requestedRelease reports whether this invocation has a release half at all.
// --from counts without a version: it supplies one.
func (f promoteCmdFlags) requestedRelease() bool {
	return f.version != "" || f.fromSource().requested()
}

// registerPromoteFlags declares the release half on `forge env deploy`.
func registerPromoteFlags(cmd *cobra.Command, f *promoteCmdFlags) {
	flags := cmd.Flags()
	flags.BoolVar(&f.plan, "plan", false, "Compute and print the full change set WITHOUT writing the binding or applying anything")
	flags.StringVar(&f.note, "note", "", "Why — recorded on the ledger entry (most valuable on a deploy that moves the env BEHIND)")

	// The approval gate (O-13). On by default; --yes is the opt-out of the
	// PROMPT, never of the plan — the plan is always computed and printed.
	flags.BoolVar(&f.yes, "yes", false,
		"Proceed without the interactive confirmation: \"I read the plan\". The paved CI path. The plan is still computed and printed")
	flags.BoolVar(&f.planOnly, "plan-only", false,
		"Build, push and cut as usual, print the plan, and STOP without writing the promotion (exit 0). The first stage of a two-stage pipeline")
	flags.StringVar(&f.approve, "approve", "",
		"Proceed only if the plan is EXACTLY this digest (from an earlier --plan-only). The stronger form of --yes, for a two-stage pipeline where a human reviews stage one's plan: a plan that changed between the stages is refused (exit 3, plan_stale) rather than re-approved blind. The digest also travels on the write, and the server recomputes the plan under the environment's row lock before admitting it")
	flags.StringSliceVar(&f.acknowledgeDestructive, "acknowledge-destructive", nil,
		"Accept the named stop-class finding codes (comma-separated), e.g. stateful_deletion. REQUIRED for every destructive change the plan reports, and --yes does not cover them: --yes is the flag that ends up hard-coded in CI, and one that covered destructive changes would silently pre-approve every future one. The codes are not knowable in advance — run --plan-only to see them")
	flags.StringVar(&f.actor, "actor", "", "Name the automation recording this (e.g. ci); default is the local user")
	flags.BoolVar(&f.skipHubCheck, "skip-hub-check", false,
		"Record a hub-converged env's promotion even when its control plane's hub reports the reconciler failing. Without it, such a deploy is refused before anything is recorded")
	flags.BoolVar(&f.liveDiff, "live-diff", false,
		"Print the release bundle's diff against each live cluster with the plan (client-side kubectl diff; writes nothing; does not show fields the bundle stops setting). What a reviewer reads before --approve")
	flags.BoolVar(&f.rerecordBundle, "rerecord-bundle", false,
		"Replace the release's recorded bundle with this checkout's render. Without it, a render whose objects differ from the bundle recorded for the release is refused: a release's bundle is what a reviewer approved and what deploys")

	// Anti-stomp. Every release deploy compare-and-sets against the plan's
	// read; these replace that value, they do not enable it.
	flags.StringVar(&f.expectCurrent, "expect-current", "",
		"Promotion id the env must still be on (default: the one the plan read); `unbound` = --expect-unbound. Exit 3 if it moved")
	flags.BoolVar(&f.expectUnbound, "expect-unbound", false, "Refuse (exit 3) unless the env has never been promoted")
	cmd.MarkFlagsMutuallyExclusive("expect-current", "expect-unbound")
	flags.BoolVar(&f.supersede, "supersede", false,
		"Deploy even though the current promotion is still rolling out (recorded on the new entry); without it that is exit 4")

	// The health gate, opt-OUT.
	flags.BoolVar(&f.noWait, "no-wait", false,
		"Record and apply, but do NOT wait for health. Gate on it later with 'forge env status <env> --wait'")
	flags.DurationVar(&f.timeout, "timeout", 0, "Whole health-gate budget (default 15m hosted, 5m per resource self-managed)")
	flags.BoolVar(&f.failFast, "fail-fast", false, "Exit 1 on the first DEGRADED observation instead of waiting out --timeout")
	flags.BoolVar(&f.wait, "wait", false,
		"Block until the deploy is LIVE even when the control plane queues it on a person (billing): wait for them to act, up to --timeout. Without it a queued deploy prints what it waits on and the action URL, and exits 7")
	cmd.MarkFlagsMutuallyExclusive("wait", "no-wait")

	// Evidence.
	flags.StringArrayVar(&f.gates, "gate", nil,
		"Pre-deploy evidence frozen onto the entry: a gate JSON file, or name=…,status=passed|failed|skipped|error[,url=…] (repeatable)")

	// Source.
	flags.StringVar(&f.fromEnv, "from", "", "Deploy exactly what this environment is running (same control plane only); the version may be omitted")
	flags.StringVar(&f.fromPromotionID, "from-promotion", "", "With --from: the source promotion captured earlier; refused (exit 3, source_moved) if the source moved")

	// Run identity: --run-id / --run-url / --no-run, defaulted from CI.
	registerRunFlags(flags, &f.run)
}

// deployCmdFlags is the raw flag set `forge env deploy` parses, before it is
// validated and folded into a deployOptions. Kept as its own type so the
// command declaration stays a declaration and the flag SEMANTICS (the mutual
// exclusions, the rollout-policy assembly) live in a function that can be read
// and tested on its own.
type deployCmdFlags struct {
	tag           string
	dryRun        bool
	namespace     string
	explain       bool
	targetArch    string
	prune         bool
	targets       []string
	skipFrontend  bool
	frontendsOnly bool
	skipPreflight bool
	noDigest      bool
	jsonOut       bool
	// offlineLedger deploys the cached current release with the hosted
	// ledger unreachable (offline_ledger.go).
	offlineLedger bool
	// offlineTo names the cached release --offline-ledger deploys: a release
	// version, a cached promotion id, or "previous".
	offlineTo string
	// renderOptions are raw `-D name=value` values pushed into the env's KCL
	// before any render this command does.
	renderOptions []string

	rolloutMode     string
	rolloutTimeout  time.Duration
	rolloutFailFast bool
	rolloutOrder    []string

	promote promoteCmdFlags
}

// dispatchDeployCmd validates the flag combination and routes to the release
// path, the explain path, or the spec-change deploy.
//
// THE FORK IS "DID THE CALLER NAME A RELEASE", and nothing else — not the env's
// shape, not a flag. A version (or a --from that supplies one) means move the
// env to that release: record the promotion, apply it, wait. No version means
// re-apply what the env is already bound to, which is today's spec-change
// deploy, unchanged.
//
// The report is constructed in the no-release path, before the explain branch,
// because --json has to work for --explain and --dry-run too: those are exactly
// what a UI calls to preview a deploy, and a flag that only worked on the real
// thing would make the preview the one case a consumer could not use.
func dispatchDeployCmd(ctx context.Context, envName string, f deployCmdFlags) error {
	if err := bindBuildRenderOptions(buildOptions{env: envName, renderOptions: f.renderOptions}); err != nil {
		return err
	}
	if f.offlineTo != "" && !f.offlineLedger {
		return errors.New("--to belongs to --offline-ledger (the break-glass rollback). Online, name the release: forge env deploy " + envName + " <version>")
	}
	if f.offlineLedger {
		return dispatchOfflineDeploy(ctx, envName, f)
	}
	if f.promote.requestedRelease() {
		return dispatchReleaseDeploy(ctx, envName, f)
	}
	// --explain and --dry-run are READ-ONLY questions about the apply
	// target: "which cluster would this touch", "what would the manifests
	// be". They are answered from the env's current binding and must not
	// build, push or cut anything — a preview that produced a release would
	// be the one command nobody could run safely to find out what would
	// happen. So they keep the apply-only path.
	if f.explain || f.dryRun {
		return dispatchSpecChangeDeploy(ctx, envName, f)
	}
	// A SCOPE FLAG IS REFUSED HERE, before anything is built. A no-version
	// deploy cuts a release over EVERY artifact, so scoping the apply
	// would pay for the whole build to ship one part of it and record a
	// release that does not describe what shipped (deploy_scoped.go).
	if err := refuseScopedDeployWithoutRelease(envName, f); err != nil {
		return err
	}
	// O-15: no version means DO EVERYTHING — build at this checkout, push,
	// cut, plan, confirm, promote, apply, wait.
	return runDeployEverything(ctx, envName, f)
}

// dispatchSpecChangeDeploy is `forge env deploy <env>` with no release named:
// render the env's CURRENT binding and apply it. Unchanged behaviour.
func dispatchSpecChangeDeploy(ctx context.Context, envName string, f deployCmdFlags) error {
	rollout := cluster.RolloutPolicy{
		Mode:     cluster.RolloutMode(f.rolloutMode),
		Timeout:  f.rolloutTimeout,
		FailFast: f.rolloutFailFast,
		Order:    f.rolloutOrder,
	}
	// Validated BEFORE --explain short-circuits: a typo'd --rollout is a typo
	// whether or not the command goes on to do anything, and reporting it only
	// on the real run means discovering it at the worst moment.
	if err := rollout.Validate(); err != nil {
		return err
	}
	report := newDeployReport(envName, f.jsonOut)
	if f.explain {
		return runDeployExplain(ctx, envName, report)
	}
	// A bare `--dry-run` previews a deploy whose real run would build and
	// cut. Say which release it would use, from the real run's own decision.
	if f.dryRun && !f.promote.requestedRelease() {
		previewDeployDecision(ctx, envName, f, report)
	}
	// The hosted capacity preflight is the first thing that authenticates to
	// the control plane, so a missing credential or a missing scope surfaces
	// here — and a --json caller needs the document for exactly that failure,
	// with its error_reason, rather than an empty stdout and a sentence on
	// stderr (refuseDeployReported).
	if _, err := hostedCapacityPreflightForEnv(ctx, projectDirForKCL(), envName, false, progressWriter(f.jsonOut)); err != nil {
		return refuseDeployReported(report, err)
	}
	// --frontends-only is the inverse of --skip-frontend: ship ONLY the env's
	// shippable frontend(s) and nothing else. The two are mutually exclusive —
	// one says "everything but the frontend", the other "the frontend and
	// nothing else"; combining them would deploy nothing.
	if f.frontendsOnly && f.skipFrontend {
		return refuseDeployReported(report, errors.New("--frontends-only and --skip-frontend are mutually exclusive"))
	}
	if f.frontendsOnly && len(f.targets) > 0 {
		return refuseDeployReported(report, errors.New("--frontends-only and --target are mutually exclusive (--frontends-only already scopes to every frontend)"))
	}
	return runDeployReported(ctx, envName, report, deployOptions{
		imageTag:      f.tag,
		dryRun:        f.dryRun,
		namespace:     f.namespace,
		targetArch:    f.targetArch,
		prune:         f.prune,
		targets:       f.targets,
		skipFrontend:  f.skipFrontend,
		frontendsOnly: f.frontendsOnly,
		skipPreflight: f.skipPreflight,
		noDigest:      f.noDigest,
		rollout:       rollout,
		report:        report,
	})
}

// dispatchReleaseDeploy is `forge env deploy <env> vX` / `--from <src>`: record
// the promotion, have it applied, and wait for health.
//
// It assembles the client-side apply's options from the SAME flags the
// no-release path uses, and hands them to the follow-through. That is what
// makes `forge env deploy prod v1.4.0 --target api` and
// `forge env deploy prod --target api` apply the same thing — the release half
// decides WHICH bytes, the apply half decides HOW, and neither reimplements the
// other.
func dispatchReleaseDeploy(ctx context.Context, envName string, f deployCmdFlags) error {
	p := f.promote
	if f.explain {
		// --explain answers "which cluster would this touch", which is a
		// question about the APPLY. Naming a release as well asks forge to
		// move a pointer and then explain instead of doing it, and the
		// only honest reading — explain and write nothing — is what --plan
		// already does, with the change set a release deploy actually
		// wants.
		return errors.New("--explain describes the apply target and writes nothing, so it cannot be combined with a release.\n" +
			"  Preview the release change set with: forge env deploy " + envName + " " + emptyAs(p.version, "<version>") + " --plan\n" +
			"  Or inspect the apply target on its own with: forge env deploy " + envName + " --explain")
	}
	if p.expectUnbound {
		p.expectCurrent = expectUnboundLiteral
	}
	rollout := cluster.RolloutPolicy{
		Mode:     cluster.RolloutMode(f.rolloutMode),
		Timeout:  f.rolloutTimeout,
		FailFast: f.rolloutFailFast,
		Order:    f.rolloutOrder,
	}
	if err := rollout.Validate(); err != nil {
		return err
	}
	// The env's ledger is resolved HERE, once, before anything is computed:
	// it decides both where the promotion is recorded and who applies it,
	// and resolving it up front is what keeps those two answers from being
	// read at different moments from different places.
	projectDir := projectDirForKCL()
	// First of all: a release whose hosted part does not fit the plan is
	// refused before the ledger is even opened, with nothing written.
	capacity, err := hostedCapacityPreflightForEnv(ctx, projectDir, envName, false, progressWriter(f.jsonOut))
	if err != nil {
		return err
	}
	ledger, err := resolveReleaseLedger(ctx, projectDir, envName)
	if err != nil {
		return err
	}
	isSnapshot := false
	if p.version != "" {
		r, err := ledger.Releases.Get(ctx, p.version)
		if err != nil {
			return err
		}
		isSnapshot = r != nil && len(r.SnapshotIntents) > 0
	}
	if isSnapshot && (len(f.targets) > 0 || f.frontendsOnly) {
		return fmt.Errorf("sealed snapshot is a complete desired decision; --target repair cannot promote it")
	}
	if p.version != "" && !isSnapshot {
		// Before the bundle is recorded: a release that cannot run on the
		// platform's nodes must not cause any registry write, --plan-only included.
		if err := checkReleaseHostedPlatforms(ctx, progressWriter(f.jsonOut), projectDir, envName, p.version, ledger.Releases); err != nil {
			return err
		}
	}
	// What a scoped deploy will record as moved (D5), and whether it applies
	// any bundle object at all — resolved before the bundle step, which it
	// sizes, and before the write, so the promotion names it. See
	// deploy_scope_record.go.
	scope, err := resolveDeployScope(ctx, projectDir, envName, f.targets, f.frontendsOnly)
	if err != nil {
		return err
	}
	// The bundle step, sized to what the scope applies, and the §8.6 plan
	// computed from the bundle it records.
	deployPlan, err := releaseBundleAndPlan(ctx, releaseBundleStep{
		ProjectDir: projectDir, Env: envName, Version: p.version, Targets: f.targets,
		Ledger: ledger, Rerecord: p.rerecordBundle, ErrOut: progressWriter(f.jsonOut),
	}, scope)
	if err != nil {
		return err
	}
	if p.liveDiff && p.version != "" && scope.AppliesBundle {
		printPlanLiveDiff(ctx, progressWriter(f.jsonOut), projectDir, envName, p.version, ledger)
	}
	return runPromote(ctx, p.version, envName, promoteOptions{
		Targets:       scope.Targets,
		Ledger:        ledger,
		Capacity:      capacity,
		DryRun:        p.plan,
		JSON:          f.jsonOut,
		ProjectDir:    projectDir,
		Note:          p.note,
		Actor:         p.actor,
		ExpectCurrent: p.expectCurrent,
		Supersede:     p.supersede,
		Gates:         p.gates,
		From:          p.fromSource(),
		Run:           p.run,
		// The O-13 gate. Always stated by the command, so the verb always
		// shows the plan and asks before writing a promotion.
		Confirm: newDeployConfirm(p, ""),
		// The SERVER-BINDING half (O-13): the §8.6 plan this deploy is
		// judged against, plus what the caller approved.
		DeployPlan: deployPlan,
		Approval: deployApproval{
			Digest:               p.approve,
			AcknowledgedFindings: p.acknowledgeDestructive,
		},
		Follow: &promoteFollowOptions{
			NoWait:       p.noWait,
			Wait:         p.wait,
			jsonOut:      f.jsonOut,
			Timeout:      p.timeout,
			FailFast:     p.failFast,
			skipHubCheck: p.skipHubCheck,
			clientDeploy: deployOptions{
				imageTag:      f.tag,
				dryRun:        f.dryRun,
				namespace:     f.namespace,
				targetArch:    f.targetArch,
				prune:         f.prune,
				targets:       f.targets,
				skipFrontend:  f.skipFrontend,
				frontendsOnly: f.frontendsOnly,
				skipPreflight: f.skipPreflight,
				noDigest:      f.noDigest,
				rollout:       rollout,
			},
		},
	})
}

// refusePromoteFlagsWithoutRelease refuses a release-only flag on a deploy that
// names no release.
//
// SILENTLY DOING NOTHING IS THE FAILURE WORTH REFUSING. `forge env deploy prod
// --expect-current abc123` with no version reads as an anti-stomp guard and
// has none: there is no promotion to compare-and-set, so the deploy would apply
// the current binding and report success while the guard the caller asked for
// never ran. Same for --gate (evidence attached to nothing) and --supersede.
//
// The flags NOT listed here are the ones that mean something either way:
// --no-wait/--timeout/--fail-fast tune the apply's own rollout wait, and --json
// and --dry-run are shared by both halves.
func refusePromoteFlagsWithoutRelease(f promoteCmdFlags) error {
	var set []string
	if f.plan {
		set = append(set, "--plan")
	}
	if f.note != "" {
		set = append(set, "--note")
	}
	if f.actor != "" {
		set = append(set, "--actor")
	}
	if f.expectCurrent != "" {
		set = append(set, "--expect-current")
	}
	if f.expectUnbound {
		set = append(set, "--expect-unbound")
	}
	if f.supersede {
		set = append(set, "--supersede")
	}
	if len(f.gates) > 0 {
		set = append(set, "--gate")
	}
	if f.liveDiff {
		set = append(set, "--live-diff")
	}
	if f.rerecordBundle {
		set = append(set, "--rerecord-bundle")
	}
	if len(set) == 0 {
		return nil
	}
	return fmt.Errorf("%s %s a release to move the environment to, and this deploy names none, "+
		"so there is nothing to record %s against.\n"+
		"  Name the release:   forge env deploy <env> <version>\n"+
		"  Or take it from an environment that passed:   forge env deploy <env> --from staging\n"+
		"  A deploy with no version re-applies the env's CURRENT binding (a spec-change deploy) and writes no promotion",
		strings.Join(set, ", "), pluralVerb(len(set), "needs", "need"), pluralVerb(len(set), "it", "them"))
}

// pluralVerb picks between a singular and a plural word. Inline rather than a
// generic helper because the two call sites above are the only ones, and a
// sentence that reads "–-plan needs a release" rather than "--plan need(s)" is
// worth four lines.
func pluralVerb(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// runDeployExplain prints the resolved kubectl-context guard decision
// for an environment without doing anything destructive. Useful when
// debugging why `forge env deploy staging` refuses to apply or what context
// staging is expected to live in.
func runDeployExplain(ctx context.Context, envName string, report *deployReport) error {
	store, err := loadProjectStore()
	if err != nil {
		return err
	}
	cfg := store.Config()

	// THE APPLY PATH FIRST: which machinery ships this env is the
	// question an operator is usually asking, and the guard below answers
	// a narrower one. Said from the same ledger the deploy reads.
	applyPath := ""
	if ledger, lerr := ledgerFor(ctx, projectDirForKCL(), envName); lerr == nil {
		applyPath = deployApplyPath(ledger)
		report.setApplyPath(applyPath)
		if !report.Enabled() {
			fmt.Printf("forge env deploy %s — apply path: %s\n", envName, applyPath)
		}
	}

	// A hosted env's "declared context" is its control plane's endpoint:
	// there is no kubectl context to guard.
	if decl, derr := controlPlaneDeclaration(ctx, envName); derr == nil && decl != nil {
		ep, eerr := cloud.ResolveEndpoint(envName, decl)
		if eerr != nil {
			return eerr
		}
		guard := deployJSONGuard{DeclaredContext: ep.URL, Verdict: deployGuardVerdictAllow, Reason: deployGuardReasonControlPlaneDeclared}
		report.setMode(deployModeExplain)
		report.setGuard(guard)
		report.clearKubeContexts()
		report.setHostedTarget(ep.URL, "")
		if report.Enabled() {
			report.finish(nil, 0)
			return report.emit()
		}
		// A control plane is declared, but that alone does not mean no
		// kubectl context is used: a MIXED env applies its cluster half
		// from here. Say which, rather than "the control plane owns the
		// cluster" for an env it does not.
		verdict := "ALLOW (no kubectl context is used; the control plane applies everything)"
		if strings.HasPrefix(applyPath, "mixed") {
			verdict = "ALLOW for the hosted half; the cluster half is applied FROM THIS MACHINE with the env's declared kubectl contexts"
		}
		fmt.Printf("forge env deploy %s — control plane\n  control plane: %s\n  verdict: %s\n", envName, ep.URL, verdict)
		return nil
	}

	guard := computeDeployGuard(ctx, cfg, envName)
	report.setMode(deployModeExplain)
	report.setGuard(guard)
	// The namespace is part of "which cluster am I about to touch", and a UI
	// previewing a deploy needs it before the user confirms — so --explain
	// resolves it too rather than leaving the target half-populated.
	report.setTarget(guard.DeclaredContext, k8sClusterNamespaceForEnv(ctx, envName))

	if report.Enabled() {
		// An --explain that REFUSES is still a successful explain: it did
		// exactly what it was asked to do. Text mode returns nil here, so
		// the report says ok, and the exit codes match.
		report.finish(nil, 0)
		return report.emit()
	}

	renderDeployGuardText(envName, guard)
	if guard.Verdict == deployGuardVerdictRefuse {
		return nil
	}
	return printDeployExplainHostSkip(cfg, envName)
}

// computeDeployGuard evaluates the declared-context guard and returns the
// verdict AS DATA.
//
// This is the same decision the text explain always made, lifted out of the
// printing so both renderers read one value. Two code paths — one that prints a
// verdict and one that reports it — would be free to disagree, and a UI told
// "allow" by a document while the CLI refuses is the worst available outcome
// for a command whose entire purpose is preventing a wrong-cluster deploy.
//
// The model is declarative: the deploy applies to the context the env's KCL
// declares, and the ONLY failure is that context being absent from the
// kubeconfig. The ambient current-context is read purely to report it.
func computeDeployGuard(ctx context.Context, cfg *config.ProjectConfig, envName string) deployJSONGuard {
	declared := expectedClusterForEnv(ctx, cfg, envName)
	guard := deployJSONGuard{
		DeclaredContext: declared,
		CurrentContext:  strings.TrimSpace(currentKubectlContext(ctx)),
	}

	if declared == "" {
		guard.Verdict = deployGuardVerdictAllow
		guard.Reason = deployGuardReasonNoClusterDeclared
		guard.Fix = fmt.Sprintf("declare `forge.K8sCluster.cluster` in deploy/kcl/%s/main.k to enable the guard", envName)
		return guard
	}

	available, aerr := kubectlContextNames(ctx)
	if aerr != nil {
		guard.Verdict = deployGuardVerdictRefuse
		guard.Reason = deployGuardReasonKubectlUnavailable
		guard.Fix = aerr.Error()
		return guard
	}
	if verr := declaredContextExistsVerdict(envName, declared, available); verr != nil {
		guard.Verdict = deployGuardVerdictRefuse
		guard.Reason = deployGuardReasonDeclaredContextMissing
		guard.AvailableContexts = available
		guard.Fix = "add the context to your kubeconfig, or correct forge.K8sCluster.cluster in the env's KCL"
		return guard
	}
	guard.Verdict = deployGuardVerdictAllow
	guard.Reason = deployGuardReasonContextDeclared
	return guard
}

// renderDeployGuardText prints the human explain report from the same guard
// value the JSON carries.
func renderDeployGuardText(envName string, guard deployJSONGuard) {
	fmt.Printf("forge env deploy %s — declared-cluster guard\n", envName)
	fmt.Printf("  declared context: %s\n", emptyAs(guard.DeclaredContext, "(not declared)"))
	fmt.Printf("  current context:  %s  (purely informational — NEVER used; the deploy always applies to the DECLARED context)\n",
		emptyAs(guard.CurrentContext, "(none — kubectl not configured)"))

	switch guard.Reason {
	case deployGuardReasonNoClusterDeclared:
		fmt.Printf("  hint:             %s\n", guard.Fix)
		fmt.Println("  verdict: ALLOW (no cluster declared — guard skipped, current context used)")
	case deployGuardReasonKubectlUnavailable:
		fmt.Printf("  fix:              %s\n", guard.Fix)
		fmt.Println("  verdict: REFUSE (kubectl not configured)")
	case deployGuardReasonDeclaredContextMissing:
		fmt.Printf("  available:        %s\n", emptyAs(strings.Join(guard.AvailableContexts, ", "), "(none)"))
		fmt.Printf("  fix:              %s\n", guard.Fix)
		fmt.Println("  verdict: REFUSE (declared context not in kubeconfig)")
	default:
		fmt.Println("  verdict: ALLOW (declared context exists; deploy applies there regardless of current)")
	}
}

// printDeployExplainHostSkip is a placeholder for the post-orchestration
// shape: once the KCL-side `deploy: "host"` filter lands (deliverable 4)
// this helper renders the host-mode service list for `forge env deploy <env>
// --explain`. Kept as a stub so the call site keeps compiling while the
// re-wire is in progress.
func printDeployExplainHostSkip(_ *config.ProjectConfig, _ string) error {
	return nil
}

// emptyAs returns alt when s is empty, otherwise s. Cheap helper for
// rendering "not declared" / "(none)" placeholders.
func emptyAs(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// deployOptions bundles the flag values for `forge env deploy`. The
// runDeploy function previously took six discrete parameters; growing
// it to seven tipped revive's argument-limit lint. The struct form
// makes the call site self-documenting and keeps the per-flag default
// (e.g. dryRun=false) co-located with the field declaration.
type deployOptions struct {
	imageTag   string
	dryRun     bool
	namespace  string
	targetArch string
	// prune, when true, deletes forge-managed Deployments in the
	// namespace that the just-applied KCL render no longer produces.
	// Opt-in to start: pruning is destructive (deletes resources the
	// user didn't ask to remove) and surprising behaviour during an
	// in-progress KCL refactor would be costly to roll back. The dev
	// loop benefits most — `forge env deploy dev` after a host-mode
	// refactor leaves stale Deployments behind otherwise.
	prune bool

	// targets, when non-empty, scopes the deploy to the named
	// applications (service / operator / frontend names). Two layers
	// honour it: (1) entities.Services / entities.Operators /
	// entities.Frontends are filtered to the targeted names before
	// buildDeployGroups, so External/Compose dispatch and the
	// rollout-wait / host-skip sets cover only the targeted apps;
	// (2) the K8sCluster apply filters the rendered multi-doc manifest
	// stream EXCLUSIVELY to the manifests whose KCL-declared group ∈ targets
	// — nothing implicit, no shared-base auto-keep (see
	// cluster.SelectManifestsByGroup). Empty means "deploy EVERYTHING in the
	// env bundle" (the full declarative reconcile), the unchanged default.
	targets []string

	// skipFrontend, when true, runs the k8s apply but suppresses the
	// Frontend (e.g. Firebase Hosting) build+deploy dispatch. It's the
	// "k8s-only, leave the frontend alone" escape hatch for the WHOLE
	// backend bundle — naming backend apps via --target already excludes
	// frontends, but that forces enumerating every service; --skip-frontend
	// covers the deploy-everything-but-the-frontend case in one flag.
	skipFrontend bool

	// skipClusterApply, when true, deploys only the groups that run off
	// any cluster (host infra, compose) and touches no cluster: no kubectl
	// context guard, no local image build+push, no Secret projection, no
	// apply. `forge env up` sets it when nothing it is bringing up runs in a
	// cluster (envClusterDemand) — the scaffolded dev env, whose every
	// workload is a host process beside a cluster_target that only places
	// its Namespace and Gateway. `forge env deploy` never sets it: an
	// explicit deploy applies everything the env declares.
	skipClusterApply bool

	// infraConverged, when true, leaves the off-cluster INFRA groups (host
	// infra, compose) alone: `forge env up` converged them in its infra
	// pre-warm moments earlier in the same run. `forge env deploy` never
	// sets it.
	infraConverged bool

	// purpose is why this deploy renders the env. `forge env deploy` leaves
	// the zero value (renderDeclaration): its render claims a local port
	// block only when the env runs on this machine. `forge env up`'s deploy
	// phase passes renderToLaunch so it resolves exactly the ports up's own
	// launch render did.
	purpose renderPurpose

	// frontendsOnly, when true, deploys EXCLUSIVELY the env's Firebase
	// frontend(s): the entire k8s apply (Services, Operators, CronJobs,
	// gateways, routes, helm charts) is dropped and only the frontend
	// build+Firebase-deploy dispatch runs. It is the native inverse of
	// skipFrontend — "ship just the frontend, touch no cluster" — and is
	// what makes a project with backend CronJobs (which otherwise keep
	// the frontendOnly cluster-skip guard from engaging) deployable to
	// Firebase without a kubectl context. Implemented by narrowing the
	// rendered entity set to its Frontends (filterEntitiesByTarget with
	// frontendsOnly=true strips every non-frontend kind), which makes the
	// frontendOnly guard in runDeploy engage so the empty-manifest
	// cluster.Apply is skipped.
	frontendsOnly bool

	// skipPreflight, when true, bypasses the deploy-time deployability
	// preflight (verify every referenced Secret key + container image exists
	// on the LIVE target BEFORE the first apply). The preflight is
	// default-ON for remote/cloud clusters — it turns a deploy-time pod
	// crash (CreateContainerConfigError / ImagePullBackOff, discovered
	// one-at-a-time over a rollout) into one fail-fast error up front. It is
	// naturally skipped for local dev clusters (where images live in the
	// in-cluster registry the checker can't reach the same way) and no-ops
	// when there's nothing to check. Bypass is the escape hatch when you
	// knowingly accept the risk.
	skipPreflight bool

	// preflightOnly stops the deploy right after its deployability
	// preflight: render the env, resolve its live target, check images,
	// Secrets and served kinds — and return, applying nothing. It is how
	// `forge env deploy <env> <version>` runs the preflight BEFORE it
	// records the promotion (see preflightBeforeRecord). Always paired with
	// dryRun, so nothing on the way to the preflight writes either.
	preflightOnly bool

	// frontendFindings, when non-nil, makes the preflight-only pass COLLECT
	// the frontend half's refusals into it instead of returning them. Set
	// only for a pass whose result is a plan nothing will ship from
	// (--plan, --plan-only, --dry-run); a real deploy leaves it nil and is
	// refused as before.
	frontendFindings *[]frontendPreflightFinding

	// noDigest, when true, forces deploy to reference the mutable :tag even
	// when the build state captured an immutable content-addressed digest.
	// By default (false) forge prefers the digest — pinning the manifest to
	// <image>@sha256:... so a re-tagged / node-cached layer can never ship
	// (the structural fix for the mutable-tag/exec-format incident). The
	// escape hatch exists for the rare case a tag reference is wanted (e.g.
	// debugging a registry that mishandles digest pulls). No effect when no
	// digest was captured — the tag is used either way.
	noDigest bool

	// rollout is the post-apply wait policy: whether a Deployment or
	// one-shot Job that never becomes ready FAILS the deploy, how long
	// each gets, and what order they are reported in. The zero value
	// normalizes to wait-and-fail, which is the only safe default for a
	// real environment — see cluster.RolloutPolicy.
	rollout cluster.RolloutPolicy

	// report, when non-nil, accumulates the machine-readable document
	// (--json). It is threaded THROUGH the deploy rather than computed
	// beside it: every method on it is nil-safe, so text mode passes nil and
	// runs the identical code path. That is what makes the JSON a record of
	// what happened instead of a second opinion about it.
	report *deployReport

	// shipped, when non-nil, records each side-effecting stage this deploy
	// reaches — the cluster apply, the control-plane publish, each frontend
	// host — and whether it completed, so a deploy that fails part-way
	// reports what is already live (deploy_ship_log.go). Nil-safe like
	// report; a dry run records nothing.
	shipped *shipLog

	// directApplyAllowed is the env's DECLARED apply path, resolved from
	// Bundle.lifecycle once the render is in hand (DirectApplyAllowed).
	// False means this env is a real one: it is meant to be reconciled
	// from a bundle, and a direct apply to it prints the notice.
	//
	// It carries no behaviour today. It lives on the options so the
	// decision is made ONCE, from the render, rather than re-derived at
	// each apply site — which is what keeps the notice and the eventual
	// refusal from ever disagreeing about the same env.
	directApplyAllowed bool
}

// refuseDeployReported ends a deploy that failed before its body ran. In
// --json mode it still emits the document — ok false, the exit code, the
// error and its structure (error_reason, error_scopes) — because "every
// --json run prints one document" is what lets a consumer tell a refusal it
// can explain from a forge that could not run. The error is returned
// untouched either way.
func refuseDeployReported(report *deployReport, err error) error {
	if !report.Enabled() {
		return err
	}
	report.finish(err, 0)
	if emitErr := report.emit(); emitErr != nil {
		return errors.Join(err, emitErr)
	}
	return err
}

// runDeployReported runs the deploy and, in --json mode, emits the report.
//
// STDOUT IS DIVERTED TO STDERR for the deploy's duration when reporting. The
// deploy body makes 51 fmt.Print calls and drives kubectl with inherited
// stdout; leaving any of that on stdout would interleave prose with the
// document and there would be no JSON to parse. Redirecting rather than
// suppressing is deliberate — a `--json` deploy that fails must still show the
// operator kubectl's own diagnostics, and stderr is where a machine consumer
// expects to find them.
//
// The report is emitted whether the deploy SUCCEEDED OR FAILED, and the
// deploy's own error is returned afterwards untouched. A consumer of a failed
// deploy needs the document most of all — which resource, which state — and
// swallowing it on failure would leave the UI with nothing but an exit code.
func runDeployReported(ctx context.Context, envName string, report *deployReport, opts deployOptions) error {
	if !report.Enabled() {
		return runDeploy(ctx, envName, opts)
	}

	realStdout := os.Stdout
	os.Stdout = os.Stderr
	start := time.Now()
	deployErr := runDeploy(ctx, envName, opts)
	os.Stdout = realStdout

	report.finish(deployErr, time.Since(start))
	// A failure writing the document must not mask the deploy's own verdict:
	// the deploy result is what the caller acted on, so it wins.
	if emitErr := report.emit(); emitErr != nil && deployErr == nil {
		return emitErr
	}
	return deployErr
}

// resolveEnvMainK locates an environment's KCL entrypoint and proves it
// exists, returning the path to <kcl_dir>/<env>/main.k.
//
// The KCL directory is configurable and defaults to deploy/kcl, so the
// default and the "which environments are there?" hint both have to be
// stated in terms of whatever the project actually declared — which is why
// the not-found error names the resolved directory rather than the default.
func resolveEnvMainK(store *projectstore.Store, envName string) (string, error) {
	kclDir := store.K8s().KCLDir
	if kclDir == "" {
		kclDir = "deploy/kcl"
	}
	mainK := filepath.Join(kclDir, envName, "main.k")
	if _, err := os.Stat(mainK); os.IsNotExist(err) {
		return "", fmt.Errorf("environment %q not found: %s does not exist\nAvailable environments can be found under %s/", envName, mainK, kclDir)
	}
	return mainK, nil
}

func runDeploy(ctx context.Context, envName string, opts deployOptions) error { //nolint:funlen // the `forge env deploy` lifecycle in order: resolve, dispatch hosted, render, guard, apply, verify. The sequence is the contract, as in runUp.
	dryRun := opts.dryRun
	namespace := opts.namespace
	targetArchFlag := opts.targetArch
	prune := opts.prune
	targets := opts.targets
	report := opts.report

	recordDeployInvocation(report, opts)

	store, err := loadProjectStore()
	if err != nil {
		return err
	}
	cfg := store.Config()

	if !store.Features().DeployEnabled() {
		return config.DisabledFeatureError(config.FeatureDeploy)
	}

	mainK, err := resolveEnvMainK(store, envName)
	if err != nil {
		return err
	}

	projectDir := projectDirForKCL()

	// Arm the parallel-dev-stack render context BEFORE the first render:
	// push the git facts option("worktree")/option("branch") into KCL, back
	// forge.allocate_port with the lock-guarded block registry, and activate
	// the resolve_port store. `forge env up` arms the IDENTICAL inputs, so up and
	// deploy resolve the SAME ports for a given key — this is the
	// kill-the-up-vs-deploy-port-drift fix. Deploy commits its render, so the
	// restore hook is unused (an applied render's ports are the truth).
	// A deploy of an env that runs nowhere on this machine claims no port
	// block (see renderPurpose), and a --dry-run claims none at all: it
	// previews the stack, it does not bring one up (see blockClaim).
	claim := claimNewBlocks
	if dryRun {
		claim = inspectBlocks
	}
	activateDevStack(ctx, projectDir, envName, opts.purpose, claim)
	// An applying deploy of an env that runs here materializes it; a
	// --dry-run previews it, and must leave the tree exactly as it found it.
	if !dryRun {
		armMaterializer(projectDir)
	}

	if hosted, err := dispatchHostedDeploy(ctx, projectDir, envName, opts); hosted {
		return err
	}

	// imageTag is the env-wide mutable tag bound to KCL's `image_tag` (the
	// per-image fallback for vendored / no-digest images). plainTag is the
	// same mutable tag, never the digest form.
	// imageDigests is the PER-IMAGE name→digest map: each forge-built image
	// resolves to ITS OWN captured digest, so the KCL render pins
	// `<image>@<digest>` per service rather than stamping one env-wide digest
	// onto every image (the multi-image correctness fix). Empty on the
	// no-digest / local-registry path → every image stays on imageTag.
	tagRes, err := resolveDeployTags(ctx, projectDir, envName, opts)
	if err != nil {
		return err
	}
	// The release this env is promoted to is the version its pods report, so a
	// plain redeploy renders the same OTEL_RESOURCE_ATTRIBUTES as the deploy
	// that promoted it instead of dropping service.version and rolling pods.
	ctx = kclrender.WithRelease(ctx, tagRes.boundRelease)
	imageTag := tagRes.imageTag
	plainTag := tagRes.plainTag
	imageDigests := tagRes.imageDigests
	tagSource := tagRes.tagSource
	report.setTags(imageTag, tagSource, tagRes.boundRelease, opts.noDigest)

	namespace = resolveDeployNamespace(ctx, namespace, envName, store.Meta().Name)

	envCfgKV := loadDeployEnvConfigKV(projectDir, envName)
	renderFull := func() (string, error) {
		return cluster.RenderManifests(ctx, mainK, imageTag, namespace, envName, envCfgKV, imageDigests)
	}
	entities, fullEntities, err := renderAndScopeEntities(ctx, projectDir, envName, targets, opts.frontendsOnly, renderFull)
	if err != nil {
		return err
	}

	// A targeted deploy is still a deploy INTO the whole env's topology: a
	// target may be a group of raw manifests that no entity owns (so the
	// scoped entities carry no cluster service at all), and the cluster
	// guards below must still engage for it. The full env answers "does
	// this env deploy to a cluster"; --frontends-only is the one scoping
	// that genuinely removes the cluster from the picture.
	hasK8sServices := kclEntitiesHaveK8sCluster(entities)
	if len(targets) > 0 && !opts.frontendsOnly {
		hasK8sServices = kclEntitiesHaveK8sCluster(fullEntities)
	}
	// WHICH DELIVERY UNITS THE HUB CONVERGES, from the same declaration. This
	// apply leaves those units' objects to the hub: applying them as well
	// would put a second applier over them, which is the one thing an env
	// moving onto Flux unit by unit must never have. Resolved from the FULL
	// env because --target scopes what this run applies and must not change
	// what the environment IS.
	hubSplit, uerr := unitSplitOf(fullEntities)
	if uerr != nil {
		return fmt.Errorf("env %q: %w", envName, uerr)
	}
	skipUnits := hubSplit.Hub
	// Every unit is the hub's and forge owns no cluster-side extra (a chart, a
	// Secret it renders): this run touches no cluster, so it must not demand a
	// kubectl context, a live preflight or a rollout wait for one. What is left
	// is the work outside the bundle: a frontend, compose, host infra.
	if hubSplit.AllHub() && !forgeOwnsClusterExtras(fullEntities) {
		opts.skipClusterApply = true
	}

	// The caller established that nothing this run brings up needs a
	// cluster, so every cluster-shaped step below is out of scope.
	if opts.skipClusterApply {
		hasK8sServices = false
	}
	// --frontends-only touches no cluster, whatever the env declares: no
	// kubectl context, banner, preflight or apply.
	if opts.frontendsOnly {
		hasK8sServices = false
	}

	// WHO APPLIES this env, read from its own declaration. Resolved from
	// the FULL env for the same reason hasK8sServices is: --target scopes
	// what this run applies, not what the environment IS, and a lifecycle
	// that changed with the target flag would describe a different env on
	// every invocation.
	opts.directApplyAllowed = DirectApplyAllowed(fullEntities)

	// Loud-by-default namespace mismatch guard: when KCL env_vars hardcode
	// a project-prefixed `*.svc.cluster.local` reference that disagrees
	// with the namespace we're about to deploy into, fail BEFORE any
	// manifest applies. This is the silent CrashLoop the cp-forge-dev
	// smoke test surfaced — pods land in namespace A, env vars point at
	// services in namespace B, every dial returns `no such host` and no
	// step in the pipeline names the misconfiguration. See the helper for
	// the heuristic that distinguishes legitimate cross-namespace refs
	// from typos.
	if hasK8sServices {
		if err := checkNamespaceReferences(entities, store.Meta().Name, namespace); err != nil {
			return err
		}
	}

	// Reconcile-policy gate. Read HERE, from disk, on every deploy — a
	// pinned environment refuses writes from the very next invocation,
	// with no restart and no re-render. Placed before the banner so a
	// refused deploy says why instead of printing a plan it will not
	// carry out. A dry run is exempt: it writes nothing, and an operator
	// inspecting what WOULD ship during a freeze is exactly the person a
	// freeze should help.
	if !dryRun {
		policyStore := deploystate.NewLocal(projectDir)
		if err := gateDeployOnPolicy(ctx, policyStore, envName, policyStore.PolicyPath(envName)); err != nil {
			return err
		}
	}

	printDeployBanner(store.Meta().Name, envName, imageTag, tagSource, namespace, dryRun, hasK8sServices)

	// Declared external-prerequisite CHECKLIST: print the out-of-band facts
	// this env DEPENDS ON (forge.ExternalSecret / forge.DNSRecord) so the
	// operator sees them every deploy — not just buried in a docstring. The
	// ExternalSecret half is also ENFORCED by the preflight (a missing one
	// BLOCKS); the DNS half can't be authoritatively verified, so the
	// checklist is the only signal for it. Always printed (incl. dry-run /
	// local clusters, where the preflight Secret check is skipped) so the
	// reminder is never lost. No-op when the env declares no prereqs.
	printPrerequisiteChecklist(entities)

	if err := guardDeployCluster(ctx, cfg, envName, hasK8sServices, report); err != nil {
		return err
	}

	start := time.Now()

	if err := prepareDeployCluster(ctx, deployClusterInput{
		cfg: cfg, entities: entities, projectDir: projectDir, envName: envName,
		imageTag: imageTag, targetArchFlag: targetArchFlag,
		dryRun: dryRun, hasK8sServices: hasK8sServices,
	}); err != nil {
		return err
	}

	if !opts.frontendsOnly {
		fmt.Printf("Generating manifests from %s...\n", mainK)
	}

	// Build the deploy groups (bucketed by target type) and propagate the
	// resolved image tag to each. See buildDeployGroupsForEnv.
	//
	// topology is the WHOLE env's groups — the routing model every object's
	// destination cluster is decided by — and groups is what this deploy
	// dispatches. Untargeted they are the same. Targeted, groups is one k8s
	// apply per cluster the selected objects land on (plus the named apps'
	// non-k8s groups), each still scoped by the full topology, so a targeted
	// deploy ships each object exactly where `forge env render --list` says
	// it goes. See deploy_target_scope.go.
	groups, gerr := buildDeployGroupsForEnv(envName, entities, namespace, plainTag, dryRun)
	if gerr != nil {
		return gerr
	}
	// The hosted group (workloads bound to forge.OnHosted, hosted databases,
	// hosted frontends) is PUBLISHED, not applied: it rides the
	// control plane, after the local apply below. Hosting is per workload,
	// so the same env's cluster part deploys in the same run.
	groups, hostedGroups := splitHostedGroups(groups)
	topology := groups
	if len(targets) > 0 && !opts.frontendsOnly && fullEntities != nil {
		if topology, gerr = buildDeployGroupsForEnv(envName, fullEntities, namespace, plainTag, dryRun); gerr != nil {
			return gerr
		}
		topology, _ = splitHostedGroups(topology)
		full, rerr := renderFull()
		if rerr != nil {
			return fmt.Errorf("render %s: %w", mainK, rerr)
		}
		groups = targetedK8sGroups(cluster.SelectManifestsByGroup(full, targets), topology, groups, fullEntities)
	}
	if opts.infraConverged {
		groups = withoutInfraGroups(groups)
	}
	if opts.skipClusterApply {
		groups, topology = withoutClusterGroups(groups), withoutClusterGroups(topology)
		if len(groups) == 0 && len(hostedGroups) == 0 {
			if opts.infraConverged {
				fmt.Println("Infrastructure already converged this run; nothing else to deploy outside a cluster.")
			} else {
				fmt.Println("Nothing to deploy outside a cluster.")
			}
			if hubSplit.AllHub() {
				return shipFrontendsOnly(ctx, opts, deployFrontendInput{
					cfg: cfg, entities: entities, projectDir: projectDir, envName: envName,
					envCfgKV: envCfgKV, targets: targets, dryRun: dryRun, skipFrontend: opts.skipFrontend,
				})
			}
			return nil
		}
	}

	// Env-wide kubectl context for the consumers that don't iterate groups
	// (secrets pre-apply, empty-groups direct apply). Fails fast on
	// a declared cluster with no matching context. See resolveDeployKubectlContext.
	deployContext, err := resolveDeployKubectlContext(ctx, cfg, envName, entities, groups, hasK8sServices)
	if err != nil {
		return err
	}
	// The resolved target, recorded once the context and namespace are both
	// final. declaredClusterContexts is the FULL declared set — this env may
	// span more than one cluster (control-plane's own dev env does), and a
	// confirmation dialog shown only the env-wide context would omit a cluster
	// the deploy is about to write to.
	report.setTarget(deployContext, namespace, declaredClusterContexts(entities, deployContext, groups)...)

	// Env's declared platform deps (forge.HelmChart) rendered into
	// cluster.HelmChartSpec values. See resolveDeployHelmSpecs.
	helmSpecs, err := resolveDeployHelmSpecs(ctx, entities, targets, cluster.ChartOwner(projectNameOf(cfg), envName))
	if err != nil {
		return err
	}

	// Platform dependencies (cert-manager, Envoy Gateway, …) are NOT
	// install-if-missing'd here. They are declarative renderables (KCL
	// forge.HelmChart) applied EXPLICITLY via `forge env deploy <env>
	// --target=<platform-name>` — helm-as-a-RENDERER folds the chart's
	// manifests (and forge-supplied CRDs, CRD-first + Established-gated)
	// into the same apply pipeline. A `--target=<platform>` apply leaves
	// the cluster with the CRDs Established + controllers Deployed, so this
	// app deploy finds them present. The preflight's CRD gate still BLOCKS
	// if a referenced CRD is absent — the fix is to apply the platform dep
	// first, not a hidden per-deploy install.

	// Deployability preflight: BEFORE the first apply (including the dotenv
	// Secret projection below), verify against the LIVE target that every
	// Secret KEY the rendered manifests reference is provisioned and every
	// container image resolves. A missing key (CreateContainerConfigError)
	// or missing image (ImagePullBackOff) otherwise only surfaces as a pod
	// crash mid-rollout, discovered one-at-a-time. This prints ALL the gaps
	// at once and refuses to apply. Default-ON for remote/cloud clusters;
	// the Secret check is skipped for local dev clusters (forge applies the
	// dotenv Secrets itself moments later, so they don't exist yet) and
	// local-registry images are skipped (they live in the in-cluster
	// registry the checker can't reach). Runs under --dry-run too (pure
	// read-only check). --skip-preflight bypasses it.
	if err := gateDeployOnPreflight(ctx, deployPreflightEnvInput{
		entities: entities, groups: groups, topology: topology, topologyEntities: fullEntities,
		mainK: mainK, imageTag: imageTag, namespace: namespace,
		envName: envName, envCfgKV: envCfgKV, deployContext: deployContext,
		targets: targets, imageDigests: imageDigests, report: report,
	}, hasK8sServices, opts.skipPreflight); err != nil {
		return err
	}
	if opts.preflightOnly {
		// THE RENDER GATE. Before a promotion is recorded, every cluster
		// group goes through the apply's own path — render, scope to its
		// cluster, partition into passes, template its charts — as a dry
		// run that writes nothing. A release the render or the router
		// refuses is then refused with the binding unmoved. The ledger used
		// to claim a release that had never rendered, because the first
		// render of a group happened in the apply, after the write.
		//
		// This is not the live preflight and --skip-preflight does not skip
		// it: that flag waives checks against the cluster's current state
		// (Secrets, images), never the question "does this release render".
		if err := renderGateDeployGroups(ctx, deployApplyInput{
			groups: groups, topology: topology, topologyEntities: fullEntities,
			entities: entities, hasK8sServices: hasK8sServices,
			mainK: mainK, imageTag: imageTag, imageDigests: imageDigests,
			namespace: namespace, envName: envName, deployContext: deployContext,
			envCfgKV: envCfgKV, dryRun: true, prune: prune, cfg: cfg,
			targets: targets, helmSpecs: helmSpecs,
			rollout: opts.rollout, report: report, skipUnits: skipUnits,
			directApplyAllowed: opts.directApplyAllowed,
		}); err != nil {
			return err
		}
		// The frontend half's own refusals, which otherwise fire only AFTER
		// the cluster apply above has shipped — see preflightFrontendDeploys.
		return opts.preflightFrontend(ctx, entities, projectDir)
	}

	// Everything from here on changes a live target. Each stage is recorded
	// as it completes (or fails part-way), so a deploy that stops halfway
	// says what already shipped. See deploy_ship_log.go.
	ship := opts.shipLog()
	applyErr := ship.run(applyStageLabels(groups, hasK8sServices && !opts.skipClusterApply, deployContext, namespace), func() error {
		// k8s Secret projection: for a dotenv secret_provider, render the
		// declared cluster secret refs into plaintext Secret manifests and
		// apply them BEFORE the Deployments roll out (so each Deployment's
		// secretKeyRef resolves on first schedule).
		if !opts.skipClusterApply {
			if err := applyK8sSecretsFromProvider(ctx, entities, groups, namespace, deployContext, envName, dryRun); err != nil {
				return err
			}
		}

		// The bootstrap values the secret store needs before it can serve a
		// read (its seal key, its database password) are minted into the
		// bootstrap source once, before ESO is asked to deliver them.
		// Create-if-absent; never an overwrite. See bootstrap_secret.go.
		if !opts.skipClusterApply {
			if err := ensureBootstrapSecrets(ctx, fullEntities, dryRun, os.Stdout); err != nil {
				return err
			}
		}

		// Minted kubeconfig Secrets, BEFORE the workloads that mount them
		// roll out. `env up` mints at the cluster→deploy boundary, which
		// covers dev and e2e; a cloud env is deployed with `env deploy`
		// against clusters that already exist and never runs that phase, so
		// the mint has to happen here too or a cloud consumer's Secret is
		// simply never created.
		//
		// Only the MINTING declarations run here: a k3d in-network mint
		// resolves a docker container address, which is meaningless on the
		// deploy path and stays owned by `env up`.
		if !opts.skipClusterApply {
			if err := mintDeployKubeconfigSecrets(ctx, entities, namespace, dryRun); err != nil {
				return fmt.Errorf("kubeconfig secrets: %w", err)
			}
		}

		// When no K8sCluster groups are present, the rendered set carries
		// only external / compose / host / build-only — nothing to apply
		// via the cluster pipeline. Skip the check above (no namespace)
		// and let dispatchDeployGroups handle the per-provider paths or
		// no-op trivially.
		// A frontend-only env (e.g. just a Firebase Hosting frontend, no
		// services / operators / cronjobs) has nothing for the cluster
		// pipeline to apply. Skip the empty-groups cluster.Apply below so
		// such projects don't need kubectl configured at all — the frontend
		// dispatch further down does the real work.
		return applyDeployGroups(ctx, deployApplyInput{
			groups: groups, topology: topology, topologyEntities: fullEntities,
			entities: entities, hasK8sServices: hasK8sServices,
			mainK: mainK, imageTag: imageTag, imageDigests: imageDigests,
			namespace: namespace, envName: envName, deployContext: deployContext,
			envCfgKV: envCfgKV, dryRun: dryRun, prune: prune, cfg: cfg,
			targets: targets, helmSpecs: helmSpecs,
			rollout: opts.rollout, report: report, skipUnits: skipUnits,
			directApplyAllowed: opts.directApplyAllowed,
		})
	})
	if applyErr != nil {
		return applyErr
	}
	// The hosted part, AFTER the local apply: a hosted workload may
	// reference a cluster workload's URL, never the reverse.
	if len(hostedGroups) > 0 {
		if err := ship.run(hostedStageLabels(hostedGroups), func() error {
			return runHostedDeploy(ctx, envName, entities, hostedGroups, opts)
		}); err != nil {
			return err
		}
	}

	// Under rollout mode skip forge applied the manifests and waited for
	// nothing, so no observation arrived for any resource. Their readiness is
	// genuinely unknown rather than fine, and the document says so explicitly
	// — a consumer must never read silence as health.
	report.markWorkloadsNotWaited()

	// Frontend deploy dispatch — frontends declaring a first-class deploy
	// target are built + shipped after the service groups, under --dry-run
	// too so the plan surfaces before any side effect. The warning and the
	// skip both live in the helper so a caller cannot get one without the
	// other; warnUndeployedFrontends is what stops a declared-but-undeployed
	// frontend failing silently, which was the reported bug.
	if err := dispatchFrontendsOrSkip(ctx, deployFrontendInput{
		cfg: cfg, entities: entities, projectDir: projectDir, envName: envName,
		envCfgKV: envCfgKV, targets: targets, dryRun: dryRun,
		skipFrontend: opts.skipFrontend, shipped: ship,
	}); err != nil {
		return err
	}

	if dryRun {
		return nil
	}

	fmt.Printf("\nDeploy completed in %s.\n", time.Since(start).Truncate(time.Millisecond))
	return nil
}

// shipFrontendsOnly is the whole deploy of an env whose every delivery unit the
// hub converges: the frontends forge ships from this machine are the part of the
// env no bundle carries, so they are the one thing left to do. They ship in the
// deploy that runs AFTER the promotion is recorded; the pre-record pass
// (preflightOnly) only checks that they can.
func shipFrontendsOnly(ctx context.Context, opts deployOptions, in deployFrontendInput) error {
	if opts.preflightOnly {
		return opts.preflightFrontend(ctx, in.entities, in.projectDir)
	}
	in.shipped = opts.shipLog()
	return dispatchFrontendsOrSkip(ctx, in)
}

// resolveDeployNamespace resolves the target namespace by the established
// precedence: an explicit --namespace override, else the env's declared
// forge.K8sCluster.namespace, else the <project>-<env> default.
func resolveDeployNamespace(ctx context.Context, override, envName, projectName string) string {
	if override != "" {
		return override
	}
	if declared := k8sClusterNamespaceForEnv(ctx, envName); declared != "" {
		return declared
	}
	return projectName + "-" + envName
}

// guardDeployCluster runs the kubectl-context guard and records its verdict on
// the report.
//
// Only meaningful when at least one service in the bundle targets K8sCluster.
// External-only / compose-only projects don't touch kubectl, so the guard would
// surface a wrong-context error that has no bearing on what's about to ship —
// hence the hasK8sServices gate rather than an unconditional check.
func guardDeployCluster(
	ctx context.Context,
	cfg *config.ProjectConfig,
	envName string,
	hasK8sServices bool,
	report *deployReport,
) error {
	if !hasK8sServices {
		return nil
	}
	// The guard verdict is computed as DATA first so the report carries it
	// whichever way this goes — including the refusal, where naming the
	// cluster forge declined to touch is the whole value. The enforcement
	// below remains authoritative: this records the decision, it does not
	// make it.
	report.setGuard(computeDeployGuard(ctx, cfg, envName))
	// Runs under --dry-run too: dry-run is for surfacing mistakes (wrong
	// context!) before they ship, not for papering over them. The context is
	// purely declarative (forge.K8sCluster.cluster) — there is no CLI escape
	// hatch, so the guard always runs.
	return verifyKubectlContext(ctx, cfg, envName)
}

// deployFrontendInput carries what dispatchFrontendsOrSkip needs to warn about
// and then ship the env's frontends. Grouped for the same reason as
// deployApplyInput and deployClusterInput: the fields travel together through
// one stage of the deploy pipeline, and naming them at the call site keeps a
// long positional list from being mis-ordered silently — projectDir and
// envName are both strings, so a transposition would compile.
type deployFrontendInput struct {
	cfg          *config.ProjectConfig
	entities     *KCLEntities
	projectDir   string
	envName      string
	envCfgKV     map[string]string
	targets      []string
	dryRun       bool
	skipFrontend bool
	// shipped records each frontend host the dispatch reaches (nil-safe).
	shipped *shipLog
}

// dispatchFrontendsOrSkip ships every frontend declaring a first-class deploy
// target (today: forge.FirebaseHosting / forge.StaticSite), or reports that the
// dispatch was skipped.
//
// Runs under --dry-run too, so the assemble/firebase plan surfaces before any
// side effect. A no-op when no frontend declares a deploy target — the unchanged
// default for k8s/host/none frontends.
//
// --skip-frontend short-circuits it entirely: the k8s apply already ran and the
// user explicitly asked to leave the frontend (and its web/dist rebuild) alone.
// Naming only backend apps via --target also excludes frontends, because the
// target filter empties entities.Frontends; --skip-frontend is the "whole
// backend, no frontend" variant that does not require listing every service.
func dispatchFrontendsOrSkip(ctx context.Context, in deployFrontendInput) error {
	if in.skipFrontend {
		if hasShippableFrontend(in.entities) {
			fmt.Println("\nSkipping frontend deploy (--skip-frontend).")
		}
		return nil
	}

	// BEFORE the dispatch, because the dispatch is a silent no-op for a
	// frontend this env never declared — and that silence is the whole
	// reported bug. Warn rather than error: deploying a frontend out-of-band
	// (Vercel, a separate pipeline) is legitimate, and erroring would break
	// correctly-configured users to fix a reporting gap.
	warnUndeployedFrontends(os.Stdout, in.cfg, in.entities, in.envName, in.targets)
	return dispatchFrontendDeploys(ctx, in.entities, in.projectDir, in.envName, in.envCfgKV, in.dryRun, in.shipped)
}

// recordDeployInvocation stamps the facts that are known from the FLAGS ALONE,
// before anything can fail.
//
// The mode especially: a document that reported it only on success would leave a
// consumer of a FAILED invocation unable to tell whether bytes moved, which is
// the one question the report must always be able to answer. Nil-safe, so text
// mode calls it and nothing happens.
func recordDeployInvocation(report *deployReport, opts deployOptions) {
	if opts.dryRun {
		report.setMode(deployModeDryRun)
	} else {
		report.setMode(deployModeApply)
	}
	report.setScope(opts.targets, opts.skipFrontend, opts.frontendsOnly, opts.prune)
	report.setRolloutPolicy(opts.rollout)
}

// deployTagResolution is the resolved image-reference set runDeploy threads
// through the render + apply pipeline: the (possibly digest-pinned) imageTag,
// the always-plain plainTag (what a provider records as the deployed tag), the
// per-image name→digest map, and a human-readable tagSource for the banner.
type deployTagResolution struct {
	imageTag     string
	plainTag     string
	tagSource    string
	imageDigests map[string]string
	// boundRelease is the release this env is promoted to, when it has a
	// binding — the release whose captured digests are being pinned. Already
	// named in tagSource's prose; carried structurally so the report does not
	// have to parse it back out of a sentence.
	boundRelease string
}

// releaseTagFor is the image tag a deploy that NAMES a release renders with:
// the release version, or "" when the deploy names none, an explicit --tag
// overrides it, or --no-digest asks for the built image directly.
func releaseTagFor(ctx context.Context, flagTag string, noDigest bool) string {
	if flagTag != "" || noDigest {
		return ""
	}
	if named := hostedPinReleaseFrom(ctx); named != "" {
		return releaseVersionTag(named)
	}
	return ""
}

// resolveDeployTags resolves the image tag (three-tier precedence chain) and
// the per-image digest map for a deploy. Split out of runDeploy so the
// precedence logic is testable without stubbing the whole pipeline.
//
// Digest resolution precedence (highest first):
//  1. A bound RELEASE (bound via `forge env deploy <env> <version>`): pins the digests the
//     release captured so every env on the same release deploys byte-identical
//     images. Wins because a deliberate promotion is a stronger signal than the
//     per-env build state.
//  2. The per-env build state: the aggregate + per-service build-<env>.json
//     digests `forge env build --push` wrote.
//  3. The mutable :tag (resolveDeployImageTag) for any image with no captured
//     digest.
//
// FULL BACKWARD COMPAT: with no release binding this is byte-identical to
// resolveDeployImageDigests alone. --no-digest disables both digest paths.
func resolveDeployTags(ctx context.Context, projectDir, envName string, opts deployOptions) (deployTagResolution, error) {
	var res deployTagResolution
	var ref, pt, src string
	if named := releaseTagFor(ctx, opts.imageTag, opts.noDigest); named != "" {
		// A deploy that names a release renders from the release, never from
		// the build this checkout last made: the tag is the release version
		// (a --release build pushes under it only), and the local build state
		// is not read, so neither its tag nor its staleness can change what
		// the release's bundle contains.
		ref, pt, src = named, named, "release "+named
	} else {
		var terr error
		ref, pt, src, terr = resolveDeployImageTag(ctx, projectDir, envName, opts.imageTag, opts.noDigest)
		if terr != nil {
			return deployTagResolution{}, terr
		}
	}
	res.imageTag = ref
	res.plainTag = pt
	res.tagSource = src
	ledger, lerr := ledgerFor(ctx, projectDir, envName)
	if lerr != nil {
		return deployTagResolution{}, lerr
	}
	bindings := ledger.Bindings
	digests, boundRel, derr := resolveDeployDigests(ctx, projectDir, envName, opts.noDigest, bindings, ledger.Releases)
	if derr != nil {
		return deployTagResolution{}, derr
	}
	// Fail closed: a release-bound env whose declared images did not all
	// resolve a digest must not deploy on the mutable tag. That includes a
	// render that cannot be read — skipping the check then would ship the
	// mutable tag on exactly the run that could not prove the pins.
	if boundRel != "" {
		entities, eerr := RenderKCL(ctx, projectDir, envName)
		if eerr != nil {
			return deployTagResolution{}, fmt.Errorf("read env %q's declaration to verify release %s pins every image: %w", envName, boundRel, eerr)
		}
		if perr := checkReleasePinned(entities, digests, boundRel, envName); perr != nil {
			return deployTagResolution{}, perr
		}
	}
	res.imageDigests = digests
	res.boundRelease = boundRel
	if boundRel != "" {
		res.tagSource = fmt.Sprintf("release %s (promoted; %s)", boundRel, bindings.Location())
		fmt.Printf("  Release:     %s  (env %q is promoted to it — pinning its digests from %s)\n", boundRel, envName, bindings.Location())
	}
	return res, nil
}

// deployApplyInput carries everything applyDeployGroups needs to route the
// rendered groups to the cluster (or the empty-groups direct apply).
type deployApplyInput struct {
	groups []deploytarget.ServiceGroup
	// topology / topologyEntities are the WHOLE env's groups and entities,
	// which decide each object's destination cluster (the multi-cluster
	// scope). Equal to groups / entities on an untargeted deploy; nil falls
	// back to them. See deploy_target_scope.go.
	topology         []deploytarget.ServiceGroup
	topologyEntities *KCLEntities
	entities         *KCLEntities
	hasK8sServices   bool
	mainK            string
	imageTag         string
	imageDigests     map[string]string
	namespace        string
	envName          string
	deployContext    string
	envCfgKV         map[string]string
	dryRun           bool
	prune            bool
	cfg              *config.ProjectConfig
	targets          []string
	helmSpecs        []cluster.HelmChartSpec
	rollout          cluster.RolloutPolicy
	// skipUnits are the delivery units the hub converges: the apply leaves
	// them alone (unit_convergence.go, cluster.ApplyOpts.SkipUnits).
	skipUnits []string
	// report, when non-nil, receives the applied manifest stream and the
	// per-resource rollout outcomes. Nil-safe.
	report *deployReport
	// directApplyAllowed is deployOptions.directApplyAllowed, carried in
	// so the one notice prints where the direct apply actually happens.
	directApplyAllowed bool
}

// applyDeployGroups applies the rendered deploy groups. With no groups (and not
// a frontend-only env) it falls back to one direct cluster.Apply against the
// env's main.k — the env's support stream (cluster_target Namespace,
// ConfigMaps, additional manifests) when no workload groups exist. With groups present it dispatches
// each through its provider (the K8sCluster provider wraps cluster.Apply via
// the builder closure). A frontend-only env has nothing for the cluster
// pipeline, so both branches are skipped and the frontend dispatch does the
// real work.
func applyDeployGroups(ctx context.Context, in deployApplyInput) error {
	frontendOnly := len(in.groups) == 0 && !in.hasK8sServices && hasShippableFrontend(in.entities)
	// ONE notice, at the one place a direct cluster apply happens, for an
	// env that declares neither a lifecycle nor an applier. Printed here rather than at the top
	// of runDeploy because a frontend-only or host-only run applies to no
	// cluster, and a notice about retiring direct apply would be noise for
	// a deploy that never touches one.
	if !in.directApplyAllowed && !frontendOnly {
		fmt.Println(lifecycleNoticeLine)
	}
	if len(in.groups) == 0 && !frontendOnly {
		return cluster.Apply(ctx, cluster.ApplyOpts{
			MainK:        in.mainK,
			ImageTag:     in.imageTag,
			ImageDigests: in.imageDigests,
			Namespace:    in.namespace,
			Env:          in.envName,
			Context:      in.deployContext,
			EnvConfigKV:  in.envCfgKV,
			DryRun:       in.dryRun,
			DryRunFramed: true,
			Prune:        in.prune,
			Project:      projectNameOf(in.cfg),
			PruneCRDs:    true,
			Targets:      in.targets,
			HelmCharts:   in.helmSpecs,
			Rollout:      in.rollout,
			SkipUnits:    in.skipUnits,
			OnStream:     in.report.streamObserver(),
			OnRollout:    in.report.rolloutObserver(),
		})
	}
	if len(in.groups) > 0 {
		builder := applyOptsBuilderFromContext(applyOptsContext{
			MainK: in.mainK, ImageTag: in.imageTag, FallbackNamespace: in.namespace, Env: in.envName,
			EnvCfgKV: in.envCfgKV, DryRun: in.dryRun, Prune: in.prune, Project: projectNameOf(in.cfg),
			Targets: in.targets, Groups: in.groups, Entities: in.entities,
			Topology: in.topology, TopologyEntities: in.topologyEntities,
			ImageDigests: in.imageDigests, HelmCharts: in.helmSpecs,
			Rollout:   in.rollout,
			SkipUnits: in.skipUnits,
			OnStream:  in.report.streamObserver(), OnRollout: in.report.rolloutObserver(),
		})
		registry := deploytarget.NewRegistry()
		registry.Register(deploytarget.K8sClusterProvider{ApplyOptsBuilder: builder})
		return dispatchDeployGroupsBeforeClusters(ctx, registry, in.groups,
			ensureDevDatabaseHook(in.cfg, in.topologyOrEntities(), in.envName, in.dryRun))
	}
	return nil
}

// renderGateDeployGroups renders every cluster group a deploy would apply, as a
// dry run. It is applyDeployGroups with nothing to write: the same builder, the
// same shared env render, the same per-cluster scoping and pass partition, so
// "the pre-record gate accepted it" and "the apply can render it" are one
// fact. Nothing is sent to a cluster (every ApplyOpts is DryRun), and the
// manifests the dry run prints go to the caller's captured stdout.
func renderGateDeployGroups(ctx context.Context, in deployApplyInput) error {
	if !in.hasK8sServices || len(in.groups) == 0 {
		return nil
	}
	in.dryRun = true
	for i := range in.groups {
		in.groups[i].DryRun = true
	}
	if err := applyDeployGroups(ctx, in); err != nil {
		return fmt.Errorf("render gate: a cluster group of env %q does not render: %w", in.envName, err)
	}
	return nil
}

// topologyOrEntities is the WHOLE env's entity set when the deploy is
// targeted (the databases a cluster workload dials are a property of the env,
// not of the --target subset), else the deploy's own entities.
func (in deployApplyInput) topologyOrEntities() *KCLEntities {
	if in.topologyEntities != nil {
		return in.topologyEntities
	}
	return in.entities
}

// buildDeployGroupsForEnv builds the deploy groups from the rendered entities
// and propagates the resolved plain image tag to every group. Services bucket
// by deploy target type: K8sCluster groups by (cluster, ns, registry); External
// by deploy_cmd; Compose by compose_file. Host / build-only services are
// skipped (forge run / forge build territory).
//
// The tag propagation uses the PLAIN tag, never the digest form: the cluster
// path consumes ImageTag implicitly via cluster.Apply, and the compose path
// RECORDS it as the deployed tag — a digest there would write
// `image:@sha256:...`, which is not a reference.
func buildDeployGroupsForEnv(envName string, entities *KCLEntities, namespace, plainTag string, dryRun bool) ([]deploytarget.ServiceGroup, error) {
	groups, gerr := buildDeployGroupsWithOpts(envName, entities, namespace, dryRun)
	if gerr != nil {
		return nil, fmt.Errorf("group services for deploy: %w", gerr)
	}
	for i := range groups {
		if groups[i].ImageTag == "" {
			groups[i].ImageTag = plainTag
		}
	}
	return groups, nil
}

// renderAndScopeEntities reads the rendered KCL once and applies the
// application-level scoping filters (--target, --frontends-only) BEFORE
// everything that derives from the entity set (deploy-group bucketing, the
// rollout-wait / host-skip / one-shot-Job sets, the cluster banners).
//
// A render that cannot be read REFUSES the deploy. It used to be logged as a
// note and the deploy carried on with no entities, which is not a degraded
// deploy but a different one: no deploy groups, so no per-cluster scope, no
// helm charts, no minted kubeconfigs, no preflight — just the env's whole
// manifest stream applied to one context. On 2026-10-09 a transient entity-read
// failure during a control-plane prod deploy did exactly that, and every
// object declared for prod's daemon cluster was written to the main cluster.
// The entities are what say where each object goes; a deploy that cannot read
// them has nothing to route by, so it stops before anything is applied.
//
// --target: an empty filter is a no-op (every app). A typo'd target is caught
// here (with the list of available app names) rather than producing a silent
// no-op deploy. --frontends-only: narrow the set to its shippable frontend(s)
// and DROP every other kind, so the frontend-only cluster-skip guard engages
// even for a project that declares backend CronJobs; refuse fast when the env
// declares no shippable frontend rather than silently no-op'ing.
//
// It returns the scoped entities AND the full, unfiltered ones: the full set
// is the env's routing topology, which a targeted deploy still needs to send
// each selected object to the cluster `forge env render --list` attributes it
// to (see deploy_target_scope.go). renderManifests is the env's manifest
// render, consulted only to extend the --target vocabulary with the groups
// the rendered stream carries; nil skips that (entity names only).
func renderAndScopeEntities(ctx context.Context, projectDir, envName string, targets []string, frontendsOnly bool, renderManifests func() (string, error)) (scoped, full *KCLEntities, err error) {
	entities, kerr := RenderKCL(ctx, projectDir, envName)
	if kerr != nil {
		return nil, nil, fmt.Errorf("read env %q's declaration (deploy/kcl/%s): %w\n"+
			"  nothing was deployed: without the rendered entities forge cannot tell which cluster each object belongs on, "+
			"so it applies none of them. Fix the render (`forge env render %s --list` reproduces it) and re-run",
			envName, envName, kerr, envName)
	}
	full = entities
	if len(targets) > 0 && entities != nil {
		if verr := validateDeployTargets(entities, targets); verr != nil && renderManifests != nil {
			// Not an entity name; it may still be a group the rendered
			// stream carries. Only now is the manifest render worth paying.
			manifests, rerr := renderManifests()
			if rerr != nil {
				return nil, nil, fmt.Errorf("%w (and the manifest render that would list the rest failed: %v)", verr, rerr)
			}
			verr = validateTargetsAgainstRender(entities, targets, manifests)
			if verr != nil {
				return nil, nil, verr
			}
		} else if verr != nil {
			return nil, nil, verr
		}
		entities = filterEntitiesByTarget(entities, targets)
	}
	if frontendsOnly {
		if entities == nil || !hasShippableFrontend(entities) {
			return nil, nil, fmt.Errorf("--frontends-only: environment %q declares no shippable frontend to deploy (a forge.FirebaseHosting or forge.StaticSite deploy block)", envName)
		}
		entities = filterEntitiesToFrontendsOnly(entities)
	}
	return entities, full, nil
}

// printDeployBanner prints the pre-deploy summary. Namespace belongs to the
// K8sCluster pipeline, so it is suppressed for external-only / compose-only
// projects to keep the output uncluttered.
func printDeployBanner(projectName, envName, imageTag, tagSource, namespace string, dryRun, hasK8sServices bool) {
	fmt.Printf("Deploying project: %s\n", projectName)
	fmt.Printf("  Environment: %s\n", envName)
	fmt.Printf("  Image tag:   %s  (source: %s)\n", imageTag, tagSource)
	if hasK8sServices {
		fmt.Printf("  Namespace:   %s\n", namespace)
	}
	fmt.Printf("  Dry run:     %v\n", dryRun)
	fmt.Println()
}

// loadDeployEnvConfigKV returns the `-D key=value` config scalars passed to the
// KCL manifest render. On the KCL-native config path there are none: per-env
// app config lives in deploy/kcl/<env>/config.k and flows into each workload's
// env through config_gen.appConfigEnvMap during the render itself — the
// env's main.k reads it directly rather than through top-level `-D` bindings.
// The empty (non-nil) map keeps the render's `-D` plumbing intact while
// carrying no config.
func loadDeployEnvConfigKV(projectDir, envName string) map[string]string {
	return map[string]string{}
}

// deployClusterInput carries the inputs prepareDeployCluster needs to stand up
// the local cluster and push local images before the manifests apply.
type deployClusterInput struct {
	cfg            *config.ProjectConfig
	entities       *KCLEntities
	projectDir     string
	envName        string
	imageTag       string
	targetArchFlag string
	dryRun         bool
	hasK8sServices bool
}

// prepareDeployCluster reconciles the env's declared clusters (or the legacy
// dev-only ensureDevCluster) and, for the dev env, builds + pushes images to
// the local registry. All of this is skipped under --dry-run (renders only) and
// when the env has no
// K8sCluster services (external-only env needs no k3d cluster).
//
// Declarative first: when the env's Bundle declares `clusters = [...]`,
// reconcile each (create-if-absent, no-op if present) — the multi-cluster
// generalization of the dev-only ensure. A declared-cluster env works in ANY
// env name (not just "dev"). When the env declares NO clusters, fall back to
// the legacy dev-only ensureDevCluster so existing single-cluster dev envs are
// byte-identical. The local build+push is independent of the cluster bootstrap
// so a multi-cluster dev env still pushes to its owner cluster's registry.
func prepareDeployCluster(ctx context.Context, in deployClusterInput) error {
	if in.dryRun || !in.hasK8sServices {
		return nil
	}
	if in.entities != nil && len(in.entities.Clusters) > 0 {
		if err := reconcileDeclaredClusters(ctx, in.entities.Clusters, in.projectDir, in.envName); err != nil {
			return err
		}
	} else if in.envName == "dev" {
		if err := ensureDevCluster(ctx); err != nil {
			return err
		}
	}
	// Local image build+push: dev only (the local registry path). Remote
	// envs build/push out-of-band (CI). Only when a pod will actually run
	// the project image: a cluster that holds just the env's support
	// objects, a route to a host process or a vendored image pulls nothing
	// forge builds, and pushing anyway is how the dev env used to fail with
	// "declares no workloads, so there is no image to push".
	if in.envName == "dev" && envRunsProjectImageOnCluster(in.entities) {
		if err := buildAndPushLocal(ctx, in.cfg, in.imageTag, in.targetArchFlag, in.entities); err != nil {
			return err
		}
	}
	return nil
}

// envRunsProjectImageOnCluster reports whether some cluster-bound workload
// runs an image forge builds from this project. Nil entities (the render
// failed) answers true, keeping the build+push rather than guessing it away.
func envRunsProjectImageOnCluster(e *KCLEntities) bool {
	if e == nil {
		return true
	}
	for _, w := range e.WorkloadsOn(RuntimeCluster) {
		if w.GoBuild() != nil {
			return true
		}
	}
	return false
}

// withoutInfraGroups drops the groups prewarmInfra converges (host infra and
// compose) — the providers `forge env up` brings up before anything dials
// them. See deployOptions.infraConverged.
func withoutInfraGroups(groups []deploytarget.ServiceGroup) []deploytarget.ServiceGroup {
	out := make([]deploytarget.ServiceGroup, 0, len(groups))
	for _, g := range groups {
		if g.ProviderID != "host-infra" && g.ProviderID != "compose" {
			out = append(out, g)
		}
	}
	return out
}

// withoutClusterGroups drops every k8s-cluster group, keeping the groups a
// deploy can run with no cluster at all (host infra, compose).
func withoutClusterGroups(groups []deploytarget.ServiceGroup) []deploytarget.ServiceGroup {
	out := make([]deploytarget.ServiceGroup, 0, len(groups))
	for _, g := range groups {
		if g.ProviderID != "k8s-cluster" {
			out = append(out, g)
		}
	}
	return out
}

// resolveDeployKubectlContext resolves the env-wide kubectl context for the
// consumers that don't iterate groups (the secrets pre-apply, the empty-groups
// direct cluster.Apply). It fails fast when a
// declared cluster has no matching kubectl context (with the list of available
// contexts) rather than silently applying to whatever's active.
//
// declaredEnvContext only sees SERVICE-derived groups, so an operator- or
// cronjob-only --target (no service groups) leaves it empty; the apply
// chokepoint HARD-REJECTS an empty context, so we resolve it directly from the
// env (forge.K8sCluster.cluster) — the same source --explain uses. Host-only /
// compose envs declare no cluster and are skipped.
func resolveDeployKubectlContext(ctx context.Context, cfg *config.ProjectConfig, envName string, entities *KCLEntities, groups []deploytarget.ServiceGroup, hasK8sServices bool) (string, error) {
	if hasK8sServices {
		if err := verifyDeclaredContextsExist(ctx, envName, groups); err != nil {
			return "", err
		}
	}
	deployContext := declaredEnvContext(entities, groups)
	if deployContext == "" {
		deployContext = expectedClusterForEnv(ctx, cfg, envName)
	}
	return deployContext, nil
}

// resolveDeployHelmSpecs resolves the env's declared platform deps
// (forge.HelmChart) into cluster.HelmChartSpec values (helm-as-a-RENDERER).
// The uniform exclusive --target rule: EVERY declared chart on a bare `forge
// deploy <env>` (no --target — the full declarative reconcile), or EXACTLY the
// charts whose Name ∈ targets. Returns nil when the env declares no charts (or
// none are selected). The CRD-bundle network fetch only runs for the charts
// actually selected here.
func resolveDeployHelmSpecs(ctx context.Context, entities *KCLEntities, targets []string, owner string) ([]cluster.HelmChartSpec, error) {
	if entities == nil {
		return nil, nil
	}
	selected := selectedHelmChartEntities(entities.HelmCharts, targets)
	if len(selected) == 0 {
		return nil, nil
	}
	// The env's declared clusters are passed so a chart naming a cluster this
	// env does not declare is REFUSED here rather than silently applied to the
	// primary. See validateChartCluster.
	return helmChartSpecsFromEntities(ctx, selected, entities.Clusters, owner)
}

// deployPreflightEnvInput carries the env-derived inputs runDeployPreflightForEnv
// needs; the remaining preflight fields (required secrets, secret supply,
// target arch) are derived from entities inside the helper.
type deployPreflightEnvInput struct {
	entities *KCLEntities
	groups   []deploytarget.ServiceGroup
	// topology / topologyEntities: the WHOLE env's groups and entities, the
	// model a declared Secret's consumers are attributed against. Nil falls
	// back to groups / entities (an untargeted deploy, where they are equal).
	topology         []deploytarget.ServiceGroup
	topologyEntities *KCLEntities
	mainK            string
	imageTag         string
	namespace        string
	envName          string
	envCfgKV         map[string]string
	deployContext    string
	targets          []string
	imageDigests     map[string]string
	// report, when non-nil, receives the structured findings. Nil-safe.
	report *deployReport
}

// gateDeployOnPreflight runs the deployability preflight when it applies, and
// otherwise records WHY it did not.
//
// The skip reason is recorded as precisely as a finding, because "no findings"
// and "nobody looked" are different claims and a consumer that cannot tell them
// apart will present an unchecked deploy as a clean one. The two skips are: no
// cluster to check against, and the explicit --skip-preflight bypass.
func gateDeployOnPreflight(ctx context.Context, in deployPreflightEnvInput, hasK8sServices, skipPreflight bool) error {
	switch {
	case !hasK8sServices:
		in.report.setPreflightStatus(deployPreflightSkippedNoCluster)
	case skipPreflight:
		in.report.setPreflightStatus(deployPreflightSkippedFlag)
	default:
		return runDeployPreflightForEnv(ctx, in)
	}
	return nil
}

// runDeployPreflightForEnv runs the deployability preflight against the live
// target. The preflight arch gate reads the target cluster's declared node arch
// from the env's KCL deploy.Cluster.platform — the explicit per-env SSOT the
// cloud envs set to "amd64". We deliberately do NOT fall back to forge.yaml
// deploy.target_arch here: that field carries an implicit "amd64" default, and
// seeding the gate from a value the author never declared would risk
// false-failing an env that simply hasn't opted into platform pinning yet (the
// WARN-don't-block contract). Empty (no platform declared) leaves the gate inert.
func runDeployPreflightForEnv(ctx context.Context, in deployPreflightEnvInput) error {
	targetArch := kclFirstClusterPlatform(in.entities)
	topology, topologyEntities := in.groups, in.entities
	if len(in.topology) > 0 {
		topology, topologyEntities = in.topology, in.topologyEntities
	}
	return runDeployPreflight(ctx, deployPreflightInput{
		entities:        topologyEntities,
		groups:          topology,
		deployedTo:      clustersDeployedTo(in.groups),
		secretContexts:  declaredClusterContexts(in.entities, in.deployContext, in.groups),
		mainK:           in.mainK,
		imageTag:        in.imageTag,
		namespace:       in.namespace,
		env:             in.envName,
		envCfgKV:        in.envCfgKV,
		deployCtx:       in.deployContext,
		targets:         in.targets,
		targetArch:      targetArch,
		imageDigests:    in.imageDigests,
		requiredSecrets: requiredSecretsForPreflight(in.entities),
		secretSupply:    secretSupplyForPreflight(in.entities),
		// Read from in.entities — the env actually being deployed, the same
		// value mintDeployKubeconfigSecrets is handed further down.
		provisionedByDeploy: provisionedByDeployForPreflight(in.entities),
		storeProbe:          liveStoreProbe(in.envName, topologyEntities),
		report:              in.report,
	})
}

// warnUndeployedFrontends reports each frontend the PROJECT declares that
// this ENV will not ship, and tells the user what to write to fix it.
//
// ── Why this warns at all ────────────────────────────────────────────────
//
// forge is fail-closed nearly everywhere; this is the one place an entire
// workload goes missing without comment. Scaffold a project with a frontend
// and `forge.Frontend` is emitted in exactly ONE env — dev. Staging and prod
// get the frontend's config projection (config.k) but declare no frontend
// workload, so `forge env deploy prod` ships the Go services, says nothing
// about the frontend, and exits 0. The user's app is simply not deployed.
//
// The shape that still produces that silence is a frontend ABSENT from the
// env's rendered bundle: there is nothing to iterate over, so the dispatch
// loop never sees it. A frontend that IS in the env states its runtime (the
// Bundle refuses one that does not), so "on forge.OnHost, ships nothing" is
// a declaration rather than an accident, and is not warned about.
//
// ── Why it warns rather than errors ──────────────────────────────────────
//
// Deploying a frontend out-of-band is legitimate — Vercel, a separate
// pipeline, a static host outside forge. Those users are correctly
// configured and erroring would break them, which is worse than the silence
// this fixes. A frontend named by --target is likewise not a surprise.
//
// The runtimes named in the hint are REFLECTED from the embedded schema
// (kcl.DeployTargetsFor("Frontend")), so this text cannot name a runtime the
// schema doesn't accept, and it picks up a newly added one for free. That is
// also why it asks the Frontend union specifically rather than the whole
// set: OnBucket and OnFirebase are frontend-only, and OnCluster is
// workload-only, so offering the wrong one would be advice that fails to
// compile.
func warnUndeployedFrontends(w io.Writer, cfg *config.ProjectConfig, entities *KCLEntities, envName string, targets []string) {
	if cfg == nil || len(cfg.Frontends) == 0 {
		return
	}
	// --target names the apps to deploy; a frontend not named was excluded
	// on purpose and its absence is not a surprise worth reporting.
	if len(targets) > 0 {
		return
	}
	// dev NEVER deploys a frontend artifact: `forge env up` dev-serves
	// frontends in its own phase (`npm run dev`) and passes skipFrontend to
	// the deploy phase precisely so the prod build path stays out of the dev
	// loop (see up.go). A dev frontend with no deploy target is therefore the
	// CORRECT configuration, and warning about it would train users to
	// ignore the warning in the envs where it means something.
	if envName == "dev" {
		return
	}

	rendered := map[string]*FrontendEntity{}
	if entities != nil {
		for i, f := range entities.Frontends {
			rendered[f.Name] = &entities.Frontends[i]
		}
	}

	var undeployed []string
	for _, fe := range cfg.Frontends {
		if _, inEnv := rendered[fe.Name]; inEnv {
			continue
		}
		undeployed = append(undeployed, fe.Name)
	}
	if len(undeployed) == 0 {
		return
	}

	hint := "declare it as `forge.Frontend {..., runtime = forge.<runtime> { … }}`"
	if avail, err := kcl.DeployTargetsFor("Frontend"); err == nil && len(avail) > 0 {
		hint = fmt.Sprintf("declare it as `forge.Frontend {..., runtime = forge.<%s> { … }}`", strings.Join(avail, " | "))
	}

	fmt.Fprintln(w)
	for _, name := range undeployed {
		fmt.Fprintf(w, "warning: frontend %q is not declared in env %q — it will NOT be deployed\n", name, envName)
		fmt.Fprintf(w, "  %s in deploy/kcl/%s/main.k\n", hint, envName)
		fmt.Fprintln(w, "  see: forge project shapes --kind deploy-target")
		fmt.Fprintln(w, "  (ignore this if the frontend ships out-of-band — Vercel, a separate pipeline, a static host outside forge)")
	}
}

// dispatchFrontendDeploys ships every frontend whose runtime forge publishes
// to FROM THIS MACHINE, dispatching on runtime.type:
//
//	host        the dev server — nothing to ship, skipped
//	build-only  built (env-injected) so its output exists for a sibling's
//	            bundle, never shipped
//	bucket      built, assembled, uploaded to releases/<digest>/ and synced
//	            to live/ (StaticSiteProvider)
//	firebase    built, assembled, `firebase deploy` (FirebaseProvider)
//	hosted      NOT here: published to the control plane by the hosted
//	            group (buildHostedGroup), from a release artifact
//	            `forge env build <env> --push` pushed
//
// envCfgKV (the per-env -D config) is layered UNDER the frontend's KCL
// env_vars so an explicit env_var wins, and is only injected when the env
// var name was actually declared on the frontend (we don't leak the whole
// env config into the JS build).
func dispatchFrontendDeploys(ctx context.Context, entities *KCLEntities, projectDir, envName string, envCfgKV map[string]string, dryRun bool, ship *shipLog) error {
	if entities == nil {
		return nil
	}
	// This environment's frontend runtime config documents, rendered from
	// its OWN KCL. This is what closes the promote loop: forge generates
	// the runtime projection (frontend_config_gen.k) and here is the
	// consumer that turns it into a deployed artifact, so a bundle built
	// once picks up a different config.js in each environment it is
	// promoted to. A project that annotates no frontend config gets an
	// empty map and an unchanged deploy.
	runtimeConfigs, rcErr := renderFrontendRuntimeDocsWith(projectDir, envName, frontendRuntimeOverlays(entities))
	if rcErr != nil {
		return rcErr
	}

	// Materialize any cross-repo frontend source before anything reads a
	// frontend Path: the deploy path installs deps, builds, and assembles
	// the output tree, all of which need a real directory. A project with
	// no `source:` pays nothing here.
	if err := resolveFrontendEntitySources(ctx, projectDir, entities); err != nil {
		return err
	}

	// The release this deploy is bound to (runDeploy's kclrender.WithRelease),
	// handed to every build below as FORGE_RELEASE.
	release := kclrender.Release(ctx)

	var fes []deploytarget.FirebaseFrontend
	var sites []deploytarget.StaticSiteFrontend
	var buildOnly []deploytarget.BuildOnlyFrontend
	var builtDirs []string
	for _, f := range entities.Frontends {
		f.Release = release
		f.Project = entities.Project
		switch f.Runtime.Type {
		case FrontendRuntimeBuildOnly:
			// Built (env-injected) so its output exists on disk before
			// any shipping frontend assembles a bundle that references it.
			if err := checkDeployableFrontendMock(f); err != nil {
				return err
			}
			builtDirs = append(builtDirs, f.Path)
			buildOnly = append(buildOnly, frontendToBuildOnly(f))

		case FrontendRuntimeFirebase:
			if err := checkDeployableFrontendMock(f); err != nil {
				return err
			}
			builtDirs = append(builtDirs, f.Path)
			fb := frontendToFirebase(f)
			fb.RuntimeConfigJS = runtimeConfigs[f.Name]
			fb.SourcemapPublisher = sourcemapPublisherFor(projectDir, envName, entities)
			fes = append(fes, fb)

		case FrontendRuntimeBucket:
			if err := checkDeployableFrontendMock(f); err != nil {
				return err
			}
			builtDirs = append(builtDirs, f.Path)
			ss := frontendToStaticSite(f)
			ss.RuntimeConfigJS = runtimeConfigs[f.Name]
			ss.SourcemapPublisher = sourcemapPublisherFor(projectDir, envName, entities)
			sites = append(sites, ss)
		}
	}

	// Build/deploy-path forge-owned dotenv gate: a committed .env.local /
	// .env* under a frontend forge builds that hard-codes a forge-owned
	// variable (*_MOCK_API / *_API_URL / *_OTEL_ENDPOINT / *_ENVIRONMENT)
	// fights the KCL-injected value — and for MOCK_API is the classic
	// "mock shipped to prod" leak. WARN in `forge lint` (dev); a HARD
	// ERROR here on the deploy path so it can never reach a shipped build.
	if err := gateFrontendEnvFiles(projectDir, builtDirs); err != nil {
		return err
	}

	// Build-only frontends must build FIRST so their output exists before
	// any FirebaseHosting frontend assembles a bundle referencing it.
	if len(buildOnly) > 0 {
		if !dryRun {
			fmt.Printf("\nBuilding %d build-only frontend(s) (forge.BuildOnly)...\n", len(buildOnly))
		} else {
			fmt.Printf("\nBuild-only frontend(s) (forge.BuildOnly): %d\n", len(buildOnly))
		}
		provider := deploytarget.FirebaseProvider{ProjectDir: projectDir}
		if err := provider.BuildOnly(ctx, buildOnly, dryRun); err != nil {
			return err
		}
	}

	if len(fes) == 0 && len(sites) == 0 {
		return nil
	}

	// Dispatch each frontend target through the registry like every
	// other deploy target: build a frontend-bearing group per provider
	// and route it via the provider's Name(). The registry re-registers
	// ProjectDir-configured providers (the K8sClusterProvider
	// ApplyOptsBuilder pattern) so they resolve frontend paths against
	// the project root.
	registry := deploytarget.NewRegistry()
	registry.Register(deploytarget.FirebaseProvider{ProjectDir: projectDir})
	registry.Register(deploytarget.StaticSiteProvider{ProjectDir: projectDir})

	var groups []deploytarget.ServiceGroup
	if len(fes) > 0 {
		fmt.Printf("\nDeploying %d frontend(s) to Firebase Hosting...\n", len(fes))
		groups = append(groups, deploytarget.ServiceGroup{
			Env:        envName,
			ProviderID: deploytarget.FirebaseProvider{}.Name(),
			Frontends:  fes,
			DryRun:     dryRun,
		})
	}
	if len(sites) > 0 {
		fmt.Printf("\nDeploying %d frontend(s) to object storage...\n", len(sites))
		groups = append(groups, deploytarget.ServiceGroup{
			Env:         envName,
			ProviderID:  deploytarget.StaticSiteProvider{}.Name(),
			StaticSites: sites,
			DryRun:      dryRun,
		})
	}
	// One host at a time, so each is recorded as shipped (or not) on its
	// own: a Firebase site that went live stays reported as live when the
	// object-storage upload after it fails.
	if dryRun {
		ship = nil
	}
	for _, g := range groups {
		if err := ship.run([]string{deploytarget.FormatGroupSummary(g)}, func() error {
			return dispatchDeployGroups(ctx, registry, []deploytarget.ServiceGroup{g})
		}); err != nil {
			return err
		}
	}
	return nil
}

// preflightFrontendDeploys runs the frontend dispatch's own refusals for the
// frontends this deploy ships — the mock-API gate, the forge-owned dotenv gate,
// and the host's CLI being installed — WITHOUT building or shipping anything.
//
// WHY IN THE PREFLIGHT. The frontend dispatch runs LAST, after the cluster
// apply and the control-plane publish. A frontend refusal discovered there
// arrives with the promotion recorded and the cluster already on the new
// release: on 2026-10-09 prod's cluster rolled to a release whose Firebase
// half then failed on `exec: "firebase": executable file not found in $PATH`,
// and the ledger called the whole apply FAILED. Each of these checks depends on
// nothing the apply does, so each refuses before the promotion is written.
func preflightFrontendDeploys(ctx context.Context, entities *KCLEntities, projectDir string) error {
	return frontendPreflightError(checkFrontendDeploys(ctx, entities, projectDir))
}

// frontendPreflightFinding is one reason the frontend half of a deploy would
// be refused. It is a finding rather than an error so a PLAN can report it
// beside everything else it computed: the missing `firebase` CLI of a sandbox
// is a fact about the machine, not about the release, and refusing to print
// the plan for it hid the answer to every other question the plan answers.
type frontendPreflightFinding struct {
	// Check names the gate: frontend_sources, frontend_mock_api,
	// frontend_env_files or frontend_firebase_cli.
	Check string `json:"check"`
	// Frontends are the frontends the finding is about.
	Frontends []string `json:"frontends,omitempty"`
	// Detail is the refusal a real deploy would print, fix included.
	Detail string `json:"detail"`
}

// preflightFrontend is the preflight-only pass's frontend step: nothing under
// --skip-frontend; else the findings, handed to the plan when it asked to
// collect them (frontendFindings) and returned as the refusal when it did not.
func (o deployOptions) preflightFrontend(ctx context.Context, entities *KCLEntities, projectDir string) error {
	if o.skipFrontend {
		return nil
	}
	findings := checkFrontendDeploys(ctx, entities, projectDir)
	if o.frontendFindings != nil {
		*o.frontendFindings = append(*o.frontendFindings, findings...)
		return nil
	}
	return frontendPreflightError(findings)
}

// frontendPreflightError is the refusal a real deploy makes of findings: nil
// for none, else every detail, in order.
func frontendPreflightError(findings []frontendPreflightFinding) error {
	if len(findings) == 0 {
		return nil
	}
	details := make([]string, 0, len(findings))
	for _, f := range findings {
		details = append(details, f.Detail)
	}
	return errors.New(strings.Join(details, "\n"))
}

// checkFrontendDeploys is preflightFrontendDeploys' checks, collected instead
// of returned at the first failure — so a plan can name each one, and a real
// deploy prints every gap at once.
func checkFrontendDeploys(ctx context.Context, entities *KCLEntities, projectDir string) []frontendPreflightFinding {
	if entities == nil {
		return nil
	}
	ships := func(f FrontendEntity) bool {
		switch f.Runtime.Type {
		case FrontendRuntimeBuildOnly, FrontendRuntimeFirebase, FrontendRuntimeBucket:
			return true
		}
		return false
	}
	if !slices.ContainsFunc(entities.Frontends, ships) {
		return nil
	}
	var findings []frontendPreflightFinding
	if err := resolveFrontendEntitySources(ctx, projectDir, entities); err != nil {
		// Every later check reads a frontend's resolved path.
		return append(findings, frontendPreflightFinding{Check: "frontend_sources", Detail: err.Error()})
	}
	var dirs, names, firebase []string
	for _, f := range entities.Frontends {
		if !ships(f) {
			continue
		}
		if err := checkDeployableFrontendMock(f); err != nil {
			findings = append(findings, frontendPreflightFinding{Check: "frontend_mock_api", Frontends: []string{f.Name}, Detail: err.Error()})
		}
		dirs = append(dirs, f.Path)
		names = append(names, f.Name)
		if f.Runtime.Type == FrontendRuntimeFirebase {
			firebase = append(firebase, f.Name)
		}
	}
	if err := gateFrontendEnvFiles(projectDir, dirs); err != nil {
		findings = append(findings, frontendPreflightFinding{Check: "frontend_env_files", Frontends: names, Detail: err.Error()})
	}
	if len(firebase) > 0 {
		if _, err := exec.LookPath(deploytarget.FirebaseCLI); err != nil {
			findings = append(findings, frontendPreflightFinding{
				Check: "frontend_firebase_cli", Frontends: firebase,
				Detail: fmt.Sprintf("frontend(s) %s ship to Firebase Hosting, which runs the `%s` CLI, and it is not "+
					"installed here (%v).\n  Install it (npm install -g firebase-tools), or ship everything else with "+
					"--skip-frontend", strings.Join(firebase, ", "), deploytarget.FirebaseCLI, err),
			})
		}
	}
	return findings
}

// hasShippableFrontend reports whether any rendered frontend is bound to a
// runtime forge ships OUT OF BAND from this machine — a bucket or Firebase.
// Used to recognise a frontend-only env (skip the cluster pipeline) and
// gates nothing else. A hosted frontend ships through the control plane, a
// build-only one ships nowhere, and a host one is the dev server.
func hasShippableFrontend(e *KCLEntities) bool {
	if e == nil {
		return false
	}
	for _, f := range e.Frontends {
		if f.Runtime.Ships() {
			return true
		}
	}
	return false
}

// frontendToFirebase maps a rendered FrontendEntity on forge.OnFirebase
// onto the deploytarget.FirebaseFrontend the provider
// consumes. The frontend's env_vars become the build-time env injected
// into the JS build (NEXT_PUBLIC_* / VITE_*); only inline Value entries
// are forwarded — secret/configmap-projected vars have no host build-time
// value to inject.
func frontendToFirebase(f FrontendEntity) deploytarget.FirebaseFrontend {
	fb := f.Runtime.Firebase
	buildEnv := frontendBuildEnv(f)
	return deploytarget.FirebaseFrontend{
		Name:          f.Name,
		Path:          f.Path,
		DevRunner:     f.DevRunner,
		BuildEnv:      buildEnv,
		Project:       f.Project,
		VersionEnvVar: frontendVersionEnvVar(f.Type),
		Sourcemaps:    f.Sourcemaps,
		Spec: deploytarget.FirebaseHostingSpec{
			Project:           fb.Project,
			Site:              fb.Site,
			Target:            fb.Target,
			PublicDir:         f.PublicDir,
			BasePath:          f.BasePath,
			Bundle:            frontendBundleSpecs(f),
			Rewrites:          fb.Rewrites,
			SPAFallback:       fb.SPAFallback,
			AssetDir:          f.AssetDir,
			KeepAssetReleases: fb.KeepAssetReleases,
			CacheControl:      frontendCacheRules(f),
		},
	}
}

// frontendCacheRules is the frontend's Cache-Control rules in the
// provider's shape, order kept (first match wins).
func frontendCacheRules(f FrontendEntity) []deploytarget.CacheRuleSpec {
	rules := make([]deploytarget.CacheRuleSpec, 0, len(f.CacheControl))
	for _, r := range f.CacheControl {
		rules = append(rules, deploytarget.CacheRuleSpec{Pattern: r.Pattern, CacheControl: r.CacheControl})
	}
	return rules
}

// frontendBundleSpecs is the frontend's bundle dirs in the provider's shape.
func frontendBundleSpecs(f FrontendEntity) []deploytarget.BundleDirSpec {
	bundles := make([]deploytarget.BundleDirSpec, 0, len(f.Bundle))
	for _, b := range f.Bundle {
		bundles = append(bundles, deploytarget.BundleDirSpec{Src: b.Src, Dest: b.Dest})
	}
	return bundles
}

// frontendToStaticSite maps a rendered FrontendEntity onto the
// deploytarget.StaticSiteFrontend the static-site build-and-assemble half
// consumes. The build half is IDENTICAL to frontendToFirebase — same path,
// same dev runner, same frontendBuildEnv, the frontend's own public_dir /
// base_path / bundle — because every static runtime shares it. Only the
// bucket placement differs: it comes from forge.OnBucket, and is empty for
// a hosted frontend (the control plane owns its bucket), which is built by
// this same projection and never uploaded from here.
func frontendToStaticSite(f FrontendEntity) deploytarget.StaticSiteFrontend {
	spec := deploytarget.StaticSiteSpec{
		PublicDir:    f.PublicDir,
		BasePath:     f.BasePath,
		Bundle:       frontendBundleSpecs(f),
		CacheControl: frontendCacheRules(f),
	}
	if b := f.Runtime.Bucket; b != nil {
		spec.Bucket = b.Bucket
		spec.KeepReleases = b.KeepReleases
		if b.CDN != nil {
			spec.CDN = &deploytarget.StaticSiteCDNSpec{
				URLMap:               b.CDN.URLMap,
				Invalidate:           b.CDN.Invalidate,
				ExtraInvalidatePaths: b.CDN.ExtraInvalidatePaths,
			}
		}
	}
	return deploytarget.StaticSiteFrontend{
		Name:          f.Name,
		Path:          f.Path,
		DevRunner:     f.DevRunner,
		BuildEnv:      frontendBuildEnv(f),
		Project:       f.Project,
		VersionEnvVar: frontendVersionEnvVar(f.Type),
		Sourcemaps:    f.Sourcemaps,
		Spec:          spec,
	}
}

// frontendToBuildOnly maps a rendered FrontendEntity on forge.BuildOnly
// onto the deploytarget.BuildOnlyFrontend the build-only path consumes.
// Like frontendToFirebase, only inline Value env_vars are forwarded as
// build-time env — secret/configmap-projected vars have no host build-time
// value. PublicDir is the frontend's (the render resolves the type's
// convention when it is undeclared), so the dry-run plan reports the
// emitted directory.
func frontendToBuildOnly(f FrontendEntity) deploytarget.BuildOnlyFrontend {
	return deploytarget.BuildOnlyFrontend{
		Name:      f.Name,
		Path:      f.Path,
		DevRunner: f.DevRunner,
		BuildEnv:  frontendBuildEnv(f),
		PublicDir: f.PublicDir,
	}
}

// frontendBuildEnv computes the build-time env for a frontend forge builds
// (Firebase deploy or build-only bundle): the effective env stream (typed
// `config` folded in, explicit env_vars winning) reduced to inline values,
// then the mock variable FORCE-set to config.mock.
//
// The force is the STRUCTURAL fix for the mock-build leak. The build path
// injects this map via MergeExtraWins (see deploytarget.runInDir →
// RunWithEnv), which makes forge win over the shell. Emitting the mock var
// unconditionally — as "" for the default "off" — means an exported shell
// NEXT_PUBLIC_MOCK_API=true can no longer reach a deployable build: forge's
// empty value overrides it and connect.ts reads "" as the real backend.
// (dispatchFrontendDeploys separately HARD-ERRORS a deployable frontend
// whose config.mock is true/hybrid, so a mock is never even attempted.)
func frontendBuildEnv(f FrontendEntity) map[string]string {
	buildEnv := map[string]string{}
	for _, ev := range f.EffectiveEnvVars() {
		if ev.Value != "" {
			buildEnv[ev.Name] = ev.Value
		}
	}
	maps.Copy(buildEnv, frontendBuildIdentityEnv(f))
	// Force LAST so it wins over any explicit env_vars entry AND (downstream,
	// via MergeExtraWins) any exported shell value. Present-but-empty for the
	// default "off".
	buildEnv[frontendMockEnvVar(f.Type)] = frontendConfigMockValue(f.Config)
	return buildEnv
}

// Build-identity variables forge hands every frontend build it runs. They are
// how a bundle names the code it was built from and the release it ships in —
// the facts its About page, its version.json and its error reports need.
//
// The build cannot recover them itself. A cross-repo source is fetched
// SHALLOW with no tags (gitsource.GitFetcher), so a build pinned to `v1.8.1`
// sees only a detached sha and `git describe` answers with a hash; and the
// release name exists only in forge. reliant-web shipped "version unknown" to
// prod for exactly that reason.
const (
	// FORGE_SOURCE_REPO / _REF are the GitSource as declared: the ref is the
	// pin (a tag, a branch, or a full sha), so a tag-pinned build learns its
	// version from it.
	envForgeSourceRepo = "FORGE_SOURCE_REPO"
	envForgeSourceRef  = "FORGE_SOURCE_REF"
	// FORGE_SOURCE_COMMIT is the sha the ref resolved to when fetched.
	envForgeSourceCommit = "FORGE_SOURCE_COMMIT"
	// FORGE_RELEASE is the release version the deploy is bound to.
	envForgeRelease = "FORGE_RELEASE"
)

// frontendBuildIdentityEnv is the FORGE_SOURCE_* / FORGE_RELEASE block of a
// frontend's build env.
//
// Every variable is ALWAYS present, empty when forge does not know the value —
// the same present-but-empty rule as the mock var. The build env reaches the
// child through MergeExtraWins, so an empty value overrides a stale one in the
// caller's shell (an agent's session runs inside forge; a value left over from
// an enclosing build is precisely the kind that would leak) instead of letting
// it through. Empty means "read your own checkout".
//
// A local source override reports NO source: the code on disk is the
// developer's checkout, not the pin, and naming the pin would be a lie about
// what was built. The build's own `git` reads the override truthfully.
func frontendBuildIdentityEnv(f FrontendEntity) map[string]string {
	env := map[string]string{
		envForgeSourceRepo:   "",
		envForgeSourceRef:    "",
		envForgeSourceCommit: "",
		envForgeRelease:      f.Release,
	}
	if f.Source != nil && f.Source.Repo != "" && !f.SourceOverridden {
		env[envForgeSourceRepo] = f.Source.Repo
		env[envForgeSourceRef] = f.Source.Ref
		env[envForgeSourceCommit] = f.SourceCommit
	}
	return env
}

// checkDeployableFrontendMock hard-errors when a frontend forge is about to
// BUILD for shipment (a Firebase deploy target, or a build-only bundle
// assembled into a deployable site) declares config.mock = true / hybrid.
// A deployable build must never be a mock build — the force in
// frontendBuildEnv already prevents a shell var from turning mock on, and
// this closes the other direction: an explicitly-declared mock on a
// deployable frontend is a configuration error, not something to silently
// ship. mock=off (the default) is a no-op.
func checkDeployableFrontendMock(f FrontendEntity) error {
	if mode := frontendConfigMockValue(f.Config); mode != "" {
		return fmt.Errorf(
			"frontend %q is deployable but its config.mock is %q; a deployable build must not be a mock build — set config.mock = \"off\" (or remove it) before deploying",
			f.Name, mode)
	}
	return nil
}

// gateFrontendEnvFiles runs the forge-owned dotenv rule at ERROR severity
// over the frontend dirs forge is about to build, and fails the deploy on
// any finding. This is the build/deploy arm of the severity split: the
// SAME rule warns (never gates) in `forge lint`. Reuses the forgeconv
// analyzer so dev-lint and the deploy gate can never diverge.
func gateFrontendEnvFiles(projectDir string, feDirs []string) error {
	if len(feDirs) == 0 {
		return nil
	}
	abs := make([]string, 0, len(feDirs))
	for _, d := range feDirs {
		if filepath.IsAbs(d) {
			abs = append(abs, d)
			continue
		}
		abs = append(abs, filepath.Join(projectDir, d))
	}
	res := forgeconv.LintFrontendEnvFiles(projectDir, abs, finding.SeverityError)
	if len(res.Findings) == 0 {
		return nil
	}
	fmt.Print(res.FormatText())
	return fmt.Errorf("a frontend .env* file hard-codes a forge-owned variable; move it to the frontend's KCL config/env_vars and delete the dotenv line (see the findings above)")
}

// validateDeployTargets checks every name passed to --target against
// the set of deployable app names in the rendered KCL (workloads of every
// kind + infra + frontends). A target that matches nothing is almost
// always a typo; erroring here — with the list of available app names —
// is far friendlier than silently deploying nothing (group filter
// empties out) or applying a shared-only manifest bundle. Reuses
// inTargetSet's membership semantics indirectly via a name set.
//
// Every workload kind is a first-class --target subject: RenderWorkloads
// stamps `app.kubernetes.io/name = <workload>` on everything it renders for
// one, so naming it scopes the cluster apply to that workload exactly
// (cluster.SelectManifestsByGroup is group-label-driven, not kind-driven).
//
// manifestGroups extends the set with every GROUP the rendered stream
// attributes an object to (cluster.ManifestGroups over the FULL render) — the
// names `forge env render --list` prints in its APP column. A Bundle's
// `additional_manifests` that carry their own `app.kubernetes.io/name`
// (control-plane dev's `openbao`) form such a group without any entity, and
// `--list` showing a name `--target` refuses is the defect this closes.
// Ungrouped objects (the env-shared Namespace, NetworkPolicies, …) have no
// name to target and stay addressable only by a bare deploy.
func validateDeployTargets(e *KCLEntities, targets []string, manifestGroups ...string) error {
	avail := map[string]struct{}{}
	for _, w := range e.Workloads {
		avail[w.Name] = struct{}{}
	}
	for _, hi := range e.Infra {
		avail[hi.Name] = struct{}{}
	}
	for _, f := range e.Frontends {
		avail[f.Name] = struct{}{}
	}
	// Platform deps (forge.HelmChart) are first-class --target subjects:
	// `forge env deploy <env> --target=<chart>` renders + applies that chart's
	// group. A chart NAME is a valid target the same way a service name is —
	// its rendered manifests carry it as their app.kubernetes.io/name group.
	for _, h := range e.HelmCharts {
		avail[h.Name] = struct{}{}
	}
	var manifestOnly []string
	for _, g := range manifestGroups {
		if _, entity := avail[g]; !entity {
			manifestOnly = append(manifestOnly, g)
		}
	}
	isTarget := func(t string) bool {
		if _, ok := avail[t]; ok {
			return true
		}
		for _, g := range manifestOnly {
			if g == t {
				return true
			}
		}
		return false
	}
	var unknown []string
	for _, t := range targets {
		if !isTarget(t) {
			unknown = append(unknown, t)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	names := make([]string, 0, len(avail))
	for n := range avail {
		names = append(names, n)
	}
	return errUnknownTargets(unknown, names, manifestOnly)
}

// filterEntitiesByTarget returns a shallow copy of e with Workloads, Infra and
// Frontends narrowed to the names in targets (inTargetSet from up.go for the
// membership test). The remaining entity slices — gateways, routes, charts —
// are carried through UNCHANGED: those carry their own group label, and the
// cluster apply's exclusive cluster.SelectManifestsByGroup is what actually
// drops the non-targeted ones from the rendered stream.
func filterEntitiesByTarget(e *KCLEntities, targets []string) *KCLEntities {
	out := *e // shallow copy; slices below are rebuilt, the rest shared
	var ws []WorkloadEntity
	for _, w := range e.Workloads {
		if inTargetSet(targets, w.Name) {
			ws = append(ws, w)
		}
	}
	var infra []HostInfraEntity
	for _, hi := range e.Infra {
		if inTargetSet(targets, hi.Name) {
			infra = append(infra, hi)
		}
	}
	var fes []FrontendEntity
	for _, f := range e.Frontends {
		if inTargetSet(targets, f.Name) {
			fes = append(fes, f)
		}
	}
	out.Workloads = ws
	out.Infra = infra
	out.Frontends = fes
	// A targeted deploy's destinations come from the objects it selects,
	// routed by the WHOLE env's topology (targetedK8sGroups) — not from every
	// cluster the env stamps an object onto, which would apply `--target api`
	// to a cluster that holds only another group's manifests.
	out.ManifestClusters = nil
	return &out
}

// filterEntitiesToFrontendsOnly returns a shallow copy of e narrowed to
// its Frontends — EVERY frontend kept (the Firebase-deploying one PLUS
// any `deploy = None` build-only frontends it bundles), and every other
// entity kind dropped: Workloads, Infra, Databases, Gateways, HTTP/GRPC
// routes, HelmCharts, Clusters, and the secret/manifest prereqs that only
// matter to the k8s apply.
//
// This is the load-bearing half of `--frontends-only`: with everything but
// Frontends stripped, the cluster pipeline is skipped entirely and only the
// frontend dispatch runs.
func filterEntitiesToFrontendsOnly(e *KCLEntities) *KCLEntities {
	out := *e // shallow copy; non-frontend slices are zeroed below
	out.Workloads = nil
	out.Infra = nil
	out.Databases = nil
	out.Gateways = nil
	out.HTTPRoutes = nil
	out.GRPCRoutes = nil
	out.HelmCharts = nil
	out.Clusters = nil
	out.KubeconfigSecrets = nil
	out.RequiredSecrets = nil
	out.RenderedSecrets = nil
	out.ManifestClusters = nil
	// The env-wide cluster declaration and everything that only describes
	// what lands on a cluster. A surviving ClusterTarget alone makes
	// kclEntitiesHaveK8sCluster true, which sends a "frontends only" deploy
	// into the unscoped whole-stream cluster apply.
	out.ClusterTarget = nil
	out.ConnectedClusters = nil
	out.RequiredDNS = nil
	out.ControlPlaneSecrets = nil
	out.ManifestNamespace = ""
	out.ManifestServiceNames = nil
	out.ManifestUnits = nil
	out.ClusterWork = nil
	// Frontends carried through unchanged — the Firebase deploy + any
	// build-only frontends it bundles.
	return &out
}

// kclEntitiesHaveK8sCluster returns true when the env applies anything to a
// cluster forge addresses: a workload or database bound to a cluster, or a
// declared cluster_target (whose support resources forge applies). Used to gate the
// kubectl-context guard, the "Namespace:" banner, and the dev-cluster
// bootstrap so external-only / compose-only projects don't print
// cluster-flavored boilerplate or refuse a deploy because kubectl
// isn't configured.
//
// Returns false when entities is nil (KCL render failed) — the
// conservative behaviour is to fall back to the no-cluster shape and
// let cluster.Apply fail with its own clearer error if a K8s service
// turns out to be in the bundle.
func kclEntitiesHaveK8sCluster(entities *KCLEntities) bool {
	if entities == nil {
		return false
	}
	if len(entities.WorkloadsOn(RuntimeCluster)) > 0 || entities.ClusterTarget.field("cluster") != "" || len(entities.ManifestClusters) > 0 {
		return true
	}
	for _, d := range entities.Databases {
		if !d.Hosted() {
			return true
		}
	}
	return false
}

// resolveDeployImageTag is the precedence chain `forge env deploy <env>`
// runs to pick the image tag for the KCL manifest render. Returns the
// resolved tag and a human-readable description of where it came from
// (printed in the deploy summary so users can debug a surprising
// choice without re-deriving the priority order in their head).
//
// Priority:
//
//  1. flagOverride — `--tag` on the CLI. CI pipelines that pin a release
//     number land here.
//  2. .forge/state/build-<env>.json — what `forge env build --push` last
//     pushed for this env. This is the load-bearing path that closes
//     the build/deploy tag divergence: build records the exact tag it
//     pushed, deploy reads it back, the working-tree state between the
//     two phases stops mattering.
//  3. resolveImageTag — git-derived fallback (`git describe --tags
//     --always --dirty`). The standalone-deploy path: no preceding
//     build, no override — recompute the tag the way build would.
//
// DIGEST PINNING: when the build-state record carries a content-addressed
// Digest (captured by `forge env build --push`), the returned reference is the
// IMMUTABLE digest form `@sha256:...` instead of the mutable :tag — unless
// noDigest is set. The KCL `_image_ref` seam recognises an `@`-prefixed
// image_tag and pins `<image>@sha256:...`, dropping the env tag. A digest
// can't go stale and can't be re-pointed, so the node-cache / re-tag-didn't-
// take failure class is structurally impossible. Fallback is automatic: no
// captured digest (third-party images, non-pushed builds, the local-registry
// e2e path) → the tag is used exactly as before.
//
// Errors:
//
//   - flagOverride bypasses every error path (the user told us
//     exactly what to use).
//   - A present-but-unreadable state file is a hard error — silently
//     falling back would mask a real bug.
//   - A missing state file is fine; the function falls through to git.
//   - A git-derivation failure on the fallback path is wrapped with a
//     "pass --tag to override" hint so users have an escape hatch.
//
// Return values: (imageRef, plainTag, source, err). imageRef is what the
// KCL manifest render pins — the digest form `@sha256:...` when a digest is
// preferred, else the plain tag. plainTag is ALWAYS the mutable tag — what a
// provider records as the tag it deployed, where a digest would not be a
// reference at all. For every tag-only path the two are identical.
func resolveDeployImageTag(ctx context.Context, projectDir, envName, flagOverride string, noDigest bool) (imageRef, plainTag, source string, err error) {
	if flagOverride != "" {
		return flagOverride, flagOverride, "explicit --tag flag", nil
	}
	// Per-env record first, then the env-agnostic "default" written by a
	// plain `forge build` (no --env). This is what makes
	// `forge build --docker && forge env deploy prod` work without forcing a
	// matching --env / --tag on both.
	for _, key := range buildStateLookupEnvs(envName) {
		st, berr := ReadBuildState(projectDir, key)
		if berr != nil {
			return "", "", "", fmt.Errorf("read build state: %w (delete .forge/state/build-%s.json to recompute from git)", berr, key)
		}
		if st == nil {
			continue
		}
		// Stale-image guard: the recorded build may be from a different
		// commit than the one this env is supposed to be running. Deploying
		// it would silently ship code the user already moved past — a
		// real-money footgun on prod. The comparison is anchored to the
		// commit of the RELEASE this deploy names, else of the release the
		// env is bound to, else git HEAD (see resolveFreshnessAnchor).
		// Refuse by default and point at the two escape
		// hatches (rebuild, or --tag to deploy the recorded tag anyway).
		if serr := checkBuildStateFreshness(ctx, projectDir, envName, key, st); serr != nil {
			return "", "", "", serr
		}
		warnIfNonReproducible(st)
		// imageRef is ALWAYS the mutable tag now — never an env-wide digest.
		// Digest pinning moved to resolveDeployImageDigests, which builds a
		// PER-IMAGE name→digest map (control-plane→d…, reliant→12…,
		// workspace-base→89…) from the per-service build states; the KCL
		// `_image_ref` seam then pins each service to ITS image's digest.
		// Returning one digest here was the bug: it stamped a single image's
		// digest onto every service (reliant pinned to control-plane's digest
		// → manifest unknown). The env tag is the per-image fallback (vendored
		// images, no-digest builds) and the tag a provider records.
		src := fmt.Sprintf(".forge/state/build-%s.json (built %s)", key, st.PushedAt)
		return st.Tag, st.Tag, src, nil
	}
	t, terr := resolveImageTag(ctx, envName)
	if terr != nil {
		return "", "", "", fmt.Errorf("failed to determine image tag: %w\nUse --tag to specify one manually", terr)
	}
	return t, t, "git describe --tags --always --dirty", nil
}

// resolveDeployImageDigests builds the PER-IMAGE name→digest map the KCL
// manifest render pins each service's image to. This is the correctness fix
// for the multi-image deploy: instead of one env-wide digest stamped onto
// every service (which pinned reliant + workspace-base to the control-plane
// digest → `manifest unknown`), each image resolves to ITS OWN captured
// digest.
//
// EVERY DIGEST IS RECORDED UNDER TWO KEYS: the BARE image name
// (`control-plane`, `reliant`) and the tag-qualified name (`reliant:e2e`) the
// build actually pushed. The KCL `image_ref` seam looks up whichever one the
// workload's declared image names, and that split is load-bearing:
//
//   - An UNPINNED image (`image = "reliant"`) takes the env tag, so the bare
//     name is unambiguous and is what it looks up.
//   - A TAG-PINNED image (`image = "reliant:e2e"`, which control-plane's e2e
//     env declares) looks up `reliant:e2e`. A digest is only true of the tag
//     it was captured for, so borrowing the bare-name digest could pin bytes
//     that were never pushed under this tag. Before the tag-qualified key
//     existed there was no way to say that, so a tag-pinned image was left on
//     its mutable tag — the spec never changed between rebuilds, the
//     Deployment never rolled out, and the nodes kept the old digest while
//     the deploy reported success.
//
// Multiple services may share one image (reliant-api-server /
// reliant-temporal-worker both run `reliant:e2e`); they all carry the same
// digest, so a later write is a harmless overwrite.
//
// Sources, all best-effort (a missing/unreadable file is skipped, never
// fatal — deploy still works on the tag for any image with no digest):
//
//   - The aggregate `.forge/state/build-<env>.json` (and the `default`
//     fallback) the docker PROJECT build and the external-build deploy-side
//     writer produce — carries the control-plane image's digest.
//   - Every per-service `.forge/state/build-<env>-<service>.json` the
//     external-build dispatcher writes — carries each external image's own
//     digest (reliant, workspace-base).
//
// Returns nil when noDigest is set (the --no-digest escape hatch) or nothing
// captured a digest — the render then leaves every image on its tag,
// byte-identical to the pre-digest behaviour (the local-registry e2e path
// captures no digests, so it is unaffected).
func resolveDeployImageDigests(projectDir, envName string, noDigest bool) (map[string]string, error) {
	if noDigest {
		return nil, nil
	}
	out := map[string]string{}

	// Aggregate build state(s): env-specific, then the env-agnostic default.
	for _, key := range buildStateLookupEnvs(envName) {
		st, err := ReadBuildState(projectDir, key)
		if err != nil {
			// A present-but-unreadable aggregate is already a hard error in
			// resolveDeployImageTag (the tag path runs first), so here we
			// treat it as "no digest from this source" rather than double-
			// reporting — the tag resolver surfaces the real error.
			continue
		}
		if st != nil {
			recordImageDigest(out, st.Image, st.Tag, st.Digest)
		}
	}

	// Per-service external-build states: build-<env>-<service>.json. Glob the
	// state dir for this env's per-service files and read each one. These
	// carry the per-image digests the aggregate can't (the aggregate is a
	// single last-writer-wins file).
	stateDir := filepath.Join(projectDir, statefile.DirRel)
	pattern := filepath.Join(stateDir, "build-"+statefile.SafeSegment(envName)+"-*.json")
	matches, _ := filepath.Glob(pattern)
	for _, path := range matches {
		// Derive the service segment from the filename so we can read the
		// typed per-service state via buildtarget.ReadState (one decode path,
		// SafeSegment-aware). filename: build-<env>-<service>.json
		base := filepath.Base(path)
		prefix := "build-" + statefile.SafeSegment(envName) + "-"
		if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".json") {
			continue
		}
		service := strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".json")
		st, err := buildtarget.ReadState(projectDir, envName, service)
		if err != nil || st == nil {
			continue
		}
		// The filename alone cannot settle which env this file belongs to:
		// both segments may contain "-", so build-dev-k8s-gateway.json reads
		// equally as (dev, k8s-gateway) and (dev-k8s, gateway). Globbing
		// "build-dev-*.json" therefore also matches every env whose name
		// EXTENDS "dev", and when two of them declare the same image the
		// later match overwrites the earlier digest — glob order is
		// alphabetical, so the sibling env usually wins and the deploy ships
		// an image the current env never built.
		//
		// The state names its own env and its own service, either of which
		// settles the split the filename cannot. Checking it here is what
		// keeps a sibling env's stale digest out of this deploy.
		if !buildtarget.StateBelongsTo(st, envName, service) {
			continue
		}
		recordImageDigest(out, st.Image, st.Tag, st.Digest)
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// recordImageDigest writes one build state's digest into the pin map under
// both keys the KCL render may look it up by: the bare image name, and the
// tag-qualified `<image>:<tag>` name the build actually pushed.
//
// Writing both is what lets the render keep the two cases apart without
// knowing anything about build state. A workload declaring `reliant` wants
// "the digest of whatever this env built"; one declaring `reliant:e2e` wants
// "the digest pushed under :e2e specifically", and must get nothing if this
// build pushed some other tag of the same image. One map, two questions, and
// the key is which question was asked.
//
// A record with no image or no digest contributes nothing (a non-pushed build
// has no registry manifest to address). A record with no tag contributes only
// the bare key.
func recordImageDigest(out map[string]string, image, tag, digest string) {
	if image == "" || digest == "" {
		return
	}
	out[image] = digest
	if tag != "" {
		out[image+":"+tag] = digest
	}
}

// overrideTaggedKeys re-points every tag-qualified key of `image` at the
// release's digest, so a release binding wins for a TAG-PINNED workload the
// same way it already wins for an unpinned one.
//
// WHY THIS IS NEEDED, AND WHY IT IS SHAPED THIS WAY. A release ledger records
// a digest per image name and NO TAG (release.Artifact has Digests, URI and
// Platforms; the only Tag in the package is the release's own git tag). That
// is by design — a release names bytes, and which mutable tag those bytes
// were once pushed under is not part of their identity. But a workload
// declaring `reliant:e2e` looks up the tag-qualified key, so a release
// overlay that wrote only the bare key would be silently ignored for exactly
// the images this fix is about: the release would appear to be deployed while
// the tag-pinned workloads shipped whatever the local build state last
// captured.
//
// Since the ledger cannot say which tag it meant, the honest reading is that
// a release is authoritative for the IMAGE: every tag of it that this deploy
// would otherwise resolve now resolves to the release's digest. The keys
// rewritten are only those the build state already put in the map, so this
// pins nothing forge did not already intend to deploy — it redirects them.
// A tag with no build state stays absent and falls through to its mutable
// tag, unchanged.
//
// Recording a per-image tag in the release ledger would let this be exact
// rather than image-wide. It is deliberately NOT done here: it is a ledger
// schema change that every backend (file and hosted) would have to write and
// read, and the image-wide reading is already correct for the promote model,
// where one release's bytes ship to every env.
func overrideTaggedKeys(m map[string]string, image, digest string) {
	prefix := image + ":"
	for key := range m {
		if strings.HasPrefix(key, prefix) {
			m[key] = digest
		}
	}
}

// releaseArtifactURIs is the artifact-name → recorded URI map of a release,
// read for the sole purpose of expanding a LEGACY promotion ledger (see
// expandLegacyLedgerKeys). Empty when the release cannot be read: the
// expansion is best-effort, and the fail-closed check downstream is what
// turns an unexpandable legacy entry into a loud error rather than a silent
// unpin.
func releaseArtifactURIs(ctx context.Context, releases releaseLedger, version string) map[string]string {
	if releases == nil || version == "" {
		return nil
	}
	rel, err := releases.Get(ctx, version)
	if err != nil || rel == nil {
		return nil
	}
	out := map[string]string{}
	for name, art := range rel.Artifacts {
		if art.URI != "" {
			out[name] = art.URI
		}
	}
	return out
}

// checkReleasePinned is the FAIL-CLOSED gate on a release-bound environment.
//
// An env promoted to a release has made a specific claim: these exact bytes
// ship. Every image forge BUILDS for that env must therefore resolve to a
// digest. If one does not, `image_ref`'s last branch silently falls through
// to the mutable env tag — and the tag is usually a `git describe` string
// that was never pushed, so the deploy either ImagePullBackOffs or pulls
// whatever else happens to sit under a matching tag.
//
// That fall-through is not a theoretical hazard. Adopting the registry-bearing
// image model re-keyed every declared reference while the bound ledger still
// held bare names, so all four of a real prod release's images silently
// dropped their digests — and the banner went on announcing "image digests
// pinned", because nothing checked. A render that cannot honour the release
// binding must say so and stop, naming the image and the keys it did have,
// which is the difference between a five-second fix and an outage.
//
// Only images forge builds AND that a pulling runtime uses are checked:
// third-party images (nats, temporal) are pinned by their own author in the
// declaration and appear in no release, and a host/compose workload pulls
// nothing. `--no-digest` is the explicit escape hatch and never reaches here.
//
// A BUILD-ONLY image is pulled only through what references it: a workload's
// or manifest group's `release_inputs` (a controller handing it to the pods it
// creates, a pre-pull DaemonSet). One nothing references — a parent image
// another build is built FROM (ShellBuild.built_images) — renders into no
// object at all, so it has no tag to fall back to and is not checked. Without
// that, declaring a new build-only image made every release cut before it
// unrenderable, although none of those releases' objects could change.
func checkReleasePinned(entities *KCLEntities, digests map[string]string, boundRelease, envName string) error {
	if boundRelease == "" || entities == nil {
		return nil
	}
	var unpinned []string
	for _, d := range releasePulledDestinations(entities) {
		if _, ok := digests[d.repository]; ok {
			continue
		}
		unpinned = append(unpinned, fmt.Sprintf("  %s (workload %q)", d.repository, d.workload))
	}
	if len(unpinned) == 0 {
		return nil
	}
	keys := make([]string, 0, len(digests))
	for k := range digests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	known := "  (none)"
	if len(keys) > 0 {
		known = "  " + strings.Join(keys, "\n  ")
	}
	return fmt.Errorf(
		"environment %q is promoted to release %s, but these images it builds resolved no digest:\n%s\n\n"+
			"The release ledger and this deploy's build state carry digests under:\n%s\n\n"+
			"Deploying would silently fall back to the mutable tag, which is not what the release names — so forge stopped.\n"+
			"Fix: re-cut and re-promote the release so its artifacts are keyed by the repositories the KCL declares\n"+
			"  forge env build %s --release <version> --push && forge env deploy %s <version>\n"+
			"Or deploy without the release's pins, deliberately: forge env deploy %s --no-digest",
		envName, boundRelease, strings.Join(unpinned, "\n"), known, envName, envName, envName)
}

// releasePulledDestinations is declaredImageDestinations minus the images no
// rendered object can pull: a repository every declaring workload builds only
// (forge.BuildOnly) and no release_inputs names. See checkReleasePinned.
func releasePulledDestinations(e *KCLEntities) []imageDestination {
	referenced := map[string]bool{}
	for _, w := range e.Workloads {
		for _, ref := range w.ReleaseInputs {
			referenced[imageRepository(ref)] = true
		}
	}
	for _, m := range e.ReleaseManifestConsumers {
		for _, ref := range m.ReleaseInputs {
			referenced[imageRepository(ref)] = true
		}
	}
	buildOnly := map[string]bool{}
	for _, w := range e.Workloads {
		if w.Image == "" {
			continue
		}
		repo := imageRepository(w.Image)
		switch w.Runtime.Type {
		case RuntimeBuildOnly:
			if _, seen := buildOnly[repo]; !seen {
				buildOnly[repo] = true
			}
		case RuntimeCluster, RuntimeHosted:
			buildOnly[repo] = false
		}
	}
	var out []imageDestination
	for _, d := range declaredImageDestinations(e) {
		if buildOnly[d.repository] && !referenced[d.repository] {
			continue
		}
		out = append(out, d)
	}
	return out
}

// expandLegacyLedgerKeys re-keys a promotion's digest map onto the FULL image
// references the KCL actually declares.
//
// A release cut today keys every OCI artifact by its repository, registry host
// included (`us-central1-docker.pkg.dev/acme/prod/api`), because that is what
// the workload declares and therefore what `image_ref` looks up. Releases cut
// BEFORE a workload's image carried its registry are keyed by the bare name
// (`api`), with the repository recorded separately in Artifact.URI.
//
// Reading a legacy entry is not optional: prod is bound to one. Its bare key
// matches no declared image any more, so every lookup misses and — before the
// fail-closed check below — each image silently fell through to its mutable
// env tag while the banner still announced "image digests pinned". That is
// the exact silent-unpin this function and checkReleasePinned exist to stop.
//
// The bare key is DROPPED once it has been expanded, rather than kept as a
// parallel lookup path. Forge is pre-1.0 and one key per artifact is the
// invariant worth holding: a surviving bare key would be a second, older
// spelling of the same fact that nothing consults (no post-#322 KCL looks one
// up) and that would quietly answer a future lookup with a digest from a
// DIFFERENT registry — the collapse the repository-qualified key exists to
// prevent. A bare key with no known URI is kept as-is, so an artifact forge
// cannot place is still visible to the fail-closed check instead of vanishing.
func expandLegacyLedgerKeys(resolved, uris map[string]string) map[string]string {
	out := make(map[string]string, len(resolved))
	for name, digest := range resolved {
		uri, known := uris[name]
		// Already a full reference (it names a registry host, so it carries a
		// path separator), or no URI to place it under: keep it verbatim.
		if _, maps := release.SourcemapsFrontend(name); maps || strings.Contains(name, "/") || !known || uri == "" {
			out[name] = digest
			continue
		}
		out[strings.TrimSuffix(uri, "/")+"/"+name] = digest
	}
	return out
}

// resolveDeployDigests is the per-image digest resolver with the release layer
// folded in. It returns the image-name → digest map the KCL render pins each
// service to, plus the bound release version (empty when the env has no
// binding) so the caller can surface it in the deploy banner.
//
// Precedence (highest first):
//
//  1. A bound RELEASE (bound via `forge env deploy <env> <version>`). The release's
//     resolved digests OVERRIDE the per-env build state per image: a deliberate
//     promotion is the strongest signal, and pinning the release's digests is
//     what makes every env on the same release deploy byte-identical images
//     (build once, promote). This is the new layer.
//  2. The per-env build state (today's flow): resolveDeployImageDigests reads
//     the aggregate + per-service build-<env>.json digests `forge env build --push`
//     wrote.
//  3. The mutable :tag (resolveDeployImageTag, the caller's separate step) for
//     any image still carrying no digest.
//
// FULL BACKWARD COMPAT: an env with NO release binding gets exactly
// resolveDeployImageDigests' result and a "" release — byte-identical to before
// the release layer existed. The current cloud + e2e flow binds no release, so
// it is unaffected. noDigest disables both digest paths (the tag-only escape
// hatch) AND the release lookup.
// bindings is the binding ledger to consult. projectDir remains for the
// build-state half, which IS a file concept (.forge/state/build-<env>.json);
// the release half no longer knows where — or whether — bindings are files.
func resolveDeployDigests(ctx context.Context, projectDir, envName string, noDigest bool, bindings bindingStore, releases releaseLedger) (digests map[string]string, boundRelease string, err error) {
	base, err := resolveDeployImageDigests(projectDir, envName, noDigest)
	if err != nil {
		return nil, "", err
	}
	if noDigest {
		return base, "", nil
	}
	binding, bound, berr := bindings.Current(ctx, envName)
	if berr != nil {
		return nil, "", fmt.Errorf("read the promotion ledger for %q (%s): %w", envName, bindings.Location(), berr)
	}
	// A release NAMED by the caller wins over the one the env is currently
	// promoted to. `forge env deploy <env> <version>` records the bundle BEFORE
	// it moves the binding, so without this the bundle of a release that moves
	// images was rendered from the PREVIOUS release's pins: the plan said
	// "3 images changed" while the bundle it planned carried the old digests,
	// and applying it would have bound the ledger to a release whose code never
	// ran. Reading the release's own pins here is the same layering a bound
	// promotion gets, applied to the version the deploy is about to bind.
	named := hostedPinReleaseFrom(ctx)
	if named != "" {
		rel, rerr := releases.Get(ctx, named)
		if rerr != nil {
			return nil, "", fmt.Errorf("read release %q (%s) to pin its images: %w", named, releases.Location(), rerr)
		}
		if rel == nil {
			return nil, "", fmt.Errorf("release %q is not in %s, so there are no pins to render its bundle from", named, releases.Location())
		}
		binding = release.Promotion{Release: named, Resolved: releasePinsByRepository(*rel)}
		bound = true
	}
	if !bound {
		return base, "", nil
	}
	// Layer the release's resolved digests over the per-env build state. A
	// per-image override (not a wholesale replace) keeps any image present in
	// the build state but absent from the release still resolvable — though in
	// practice a release is authoritative for everything it pins.
	if base == nil {
		base = map[string]string{}
	}
	for image, digest := range expandLegacyLedgerKeys(binding.Resolved, releaseArtifactURIs(ctx, releases, binding.Release)) {
		// Name every image whose freshly-built digest the release is about to
		// discard. The release winning is correct — a promotion is a
		// deliberate "these exact bytes ship" — but doing it SILENTLY is how a
		// successful `forge env build --push` becomes a deploy that ships the old
		// image and still reports a clean rollout. The operator sees a green
		// deploy and an unchanged app, with nothing connecting the two.
		if built, ok := base[image]; ok && built != digest {
			fmt.Printf("  Note: %s was just built as %s, but release %s pins %s — deploying the RELEASE.\n"+
				"        To ship the build instead: forge env build %s --release <version> --push && forge env deploy %s <version>\n"+
				"        Or deploy the built image directly: forge env deploy %s --no-digest --tag <tag>\n",
				image, shortDigest(built), binding.Release, shortDigest(digest), envName, envName, envName)
		}
		base[image] = digest
		overrideTaggedKeys(base, image, digest)
		// Only for a NAMED release: its render binds image_tag to the
		// release version (releaseTagFor), so that is the key a
		// hand-composed ref looks up. See releaseVersionTag.
		if named != "" {
			base[image+":"+releaseVersionTag(named)] = digest
		}
	}
	return base, binding.Release, nil
}

// releaseVersionTag is the tag a release's images are pushed under, and so the
// image_tag every render of that release binds: the release version. ONE rule,
// read by the build that pushes (releaseImageTag), the render that names the
// release (releaseTagFor), and the pins that render resolves
// (resolveDeployDigests).
//
// A NAMED RELEASE IS PINNED UNDER IT, from the release alone. The digest a
// release records for a repository is the digest of `<repo>:<version>`, so a
// hand-composed `forge.image_ref_for(<repo>, image_tag)` must resolve to
// `<repo>:<version>@<digest>` in every render of the release. The cut's render
// got that key from the build state its own build had just written; a
// redeploy from another checkout, or after a later build, had no such key and
// rendered `<repo>@<digest>` — so the release's bundle of record could not be
// reproduced from its own commit and forge (2026-10-19, control-plane prod's
// otel gateway).
func releaseVersionTag(version string) string { return version }

// releasePinsByRepository is a release's shared digests keyed the way a
// promotion records them (the artifact's repository, registry host included),
// so a named release layers over the build state exactly like a bound one.
func releasePinsByRepository(rel release.Release) map[string]string {
	return rel.SharedDigests()
}

// shortDigest trims a canonical `sha256:<64 hex>` to a human-comparable head.
// Two digests differ in their first few bytes in practice, and a full-length
// pair on one line is unreadable — which defeats the point of printing them.
func shortDigest(d string) string {
	const shown = len("sha256:") + 12
	if len(d) <= shown {
		return d
	}
	return d[:shown]
}

// buildStateLookupEnvs is the ordered fallback of build-state keys to try
// for a deploy env: the env itself, then the "default" record a plain
// `forge build` writes. "default" is not retried for itself.
func buildStateLookupEnvs(envName string) []string {
	if envName == "" || envName == "default" {
		return []string{"default"}
	}
	return []string{envName, "default"}
}

// freshnessAnchor is the commit a recorded build is measured against by
// checkBuildStateFreshness, plus enough provenance to say so out loud. The
// anchor is NOT always HEAD — see resolveFreshnessAnchor.
type freshnessAnchor struct {
	// Commit is the full sha the build's recorded commit must match.
	Commit string
	// Release is the version label when the anchor came from a release
	// ledger, empty when the anchor is HEAD.
	Release string
}

// describe renders the anchor for an error message. Naming WHAT was compared
// against — not just the sha — is the difference between an operator fixing
// the build and an operator guessing which of two plausible commits forge
// meant.
func (a freshnessAnchor) describe() string {
	if a.Release != "" {
		return fmt.Sprintf("release %q was cut from %s", a.Release, shortSHA(a.Commit))
	}
	return fmt.Sprintf("HEAD is %s", shortSHA(a.Commit))
}

// remedy is the rebuild instruction that matches the anchor: a release-bound
// env must rebuild the RELEASE (so the ledger's digests and the images agree
// again), where an unbound env just rebuilds from HEAD.
func (a freshnessAnchor) remedy(envName string) string {
	if a.Release != "" {
		return fmt.Sprintf("rebuild the release (forge env build %s --release %s --push), then re-promote and deploy", envName, a.Release)
	}
	return "rebuild from HEAD (forge build --docker ...), then deploy"
}

// resolveFreshnessAnchor picks the commit a recorded build must match to be
// considered current for this env, and reports whether staleness can be
// proven at all.
//
// THE ANCHOR IS THE RELEASE'S COMMIT WHEN THE ENV IS BOUND TO A RELEASE.
// This is the correctness fix for a false refusal that made every
// release-ledger deploy require `--tag`: a release records `git.commit` — the
// commit its images were ACTUALLY built from — and then CUTTING the ledger
// adds one more commit on top of it. HEAD is therefore legitimately ahead of
// the images by the ledger commit itself, and comparing against HEAD refused a
// deploy that was exactly right. The release record is the honest anchor: the
// question "is this image stale?" means "was it built from something other
// than what this release claims", not "was it built from the newest commit in
// my working tree".
//
// When the env is bound to a release, that release is the ONLY valid anchor —
// if it can't be read (missing/corrupt ledger, no recorded commit), this
// reports enforce=false rather than silently falling back to HEAD, because
// falling back is precisely the false refusal above.
//
// Unbound envs keep the original HEAD comparison, which fires only when:
//
//   - HEAD is resolvable, and
//   - there are no uncommitted changes to TRACKED files. Such a tree has no
//     single HEAD the build can be measured against, so the comparison would
//     be meaningless (warnIfNonReproducible covers the dirty-build
//     reproducibility angle separately). Untracked files (editor dirs, build
//     artifacts, gitignored caches) are deliberately ignored — they don't
//     change which commit HEAD points at, and a stray `.idea/` directory must
//     not silently disable a real-money stale-deploy guard.
//
// A release-anchored comparison needs no git at all: it is ledger-vs-ledger,
// so it stays correct in a dirty tree, in CI, and on a detached checkout.
//
// A RELEASE THE DEPLOY NAMES OUTRANKS THE BINDING. `forge env deploy <env>
// <version>` preflights the release before it records the promotion
// (preflightBeforeRecord), so while this runs the binding still names the
// release the env runs NOW — the one being replaced. Anchoring on it measured
// every new release's build against the old release's commit and refused
// every forward deploy. The named release is the one that ships, so its
// commit is the anchor, the same layering resolveDeployDigests gives its
// pins. Such a deploy never consults the binding here, and when the named
// release cannot anchor it stands down rather than falling back to the
// binding (the release being replaced) or to HEAD (the false refusal above).
// A deploy that names no release re-applies the bound one and is measured
// against it, as before.
//
// AN UNREADABLE LEDGER IS AN ERROR, NEVER "no anchor". There are two reasons
// this function can decline to enforce, and they are not the same fact:
//
//   - "There is nothing to compare against" — the env is unbound and the tree
//     is dirty, or a release records no commit. Nothing is wrong; the guard
//     has no subject, so it stands down. That is enforce=false, nil error.
//   - "The subject could not be read" — the ledger would not open, its
//     promotion log does not decode, the project has no name to key it by.
//     The guard's own input is missing, and the only thing that must not
//     follow is a deploy proceeding as though it had been checked.
//
// Collapsing them is what the defect did: a corrupt promotions line, a ledger
// whose permissions changed, a missing forge.yaml name — each one silently
// disarmed the stale-image guard on the real-money path, and the deploy said
// nothing about it. That is strictly worse than the false refusal the release
// anchor exists to remove, because a refusal is read and acted on where this
// is invisible.
func resolveFreshnessAnchor(ctx context.Context, projectDir, envName string) (freshnessAnchor, bool, error) {
	ledger, err := ledgerFor(ctx, projectDir, envName)
	if err != nil {
		return freshnessAnchor{}, false, fmt.Errorf(
			"cannot check whether this build is stale for env %q: its release ledger could not be opened: %w", envName, err)
	}
	if named := hostedPinReleaseFrom(ctx); named != "" {
		return releaseFreshnessAnchor(ctx, ledger, envName, named)
	}
	binding, bound, err := ledger.Bindings.Current(ctx, envName)
	if err != nil {
		return freshnessAnchor{}, false, fmt.Errorf(
			"cannot check whether this build is stale for env %q: reading its promotions from %s failed: %w",
			envName, ledger.Bindings.Location(), err)
	}
	if bound && binding.Release != "" {
		return releaseFreshnessAnchor(ctx, ledger, envName, binding.Release)
	}
	head, clean, ok := gitHEADAndClean(ctx, projectDir)
	if !ok || !clean {
		return freshnessAnchor{}, false, nil
	}
	return freshnessAnchor{Commit: head}, true, nil
}

// releaseFreshnessAnchor is the anchor a release supplies: the commit its
// images were recorded as built from.
func releaseFreshnessAnchor(ctx context.Context, ledger envLedger, envName, version string) (freshnessAnchor, bool, error) {
	rel, err := ledger.Releases.Get(ctx, version)
	if err != nil {
		return freshnessAnchor{}, false, fmt.Errorf(
			"cannot check whether this build is stale for env %q: reading release %s from %s failed: %w",
			envName, version, ledger.Releases.Location(), err)
	}
	// A release this ledger does not hold, or one carrying no commit, is an
	// honest stand-down rather than a failure: the read SUCCEEDED and the
	// answer is that there is no anchor. Falling back to HEAD here is
	// precisely the false refusal resolveFreshnessAnchor exists to remove.
	if rel == nil || rel.Git.Commit == "" {
		return freshnessAnchor{}, false, nil
	}
	return freshnessAnchor{Commit: rel.Git.Commit, Release: version}, true, nil
}

// checkBuildStateFreshness refuses to deploy a build whose recorded source
// commit does not match the commit this env is supposed to be running, so
// `forge env deploy` never silently ships a stale image after a fresh
// commit/push (fr-02d44d2b03).
//
// What "supposed to be running" means is resolveFreshnessAnchor's job: the
// commit of the release the deploy names, else of the release the env is
// bound to, otherwise git HEAD.
//
// The check fires ONLY when both hold, to keep it a precise footgun-guard
// rather than a nag:
//
//   - The build recorded a source commit (st.Commit non-empty). Older state
//     files predating commit-stamping skip the check.
//   - An anchor is resolvable (see resolveFreshnessAnchor). "There is nothing
//     to compare against" resolves to allow; "the ledger could not be read"
//     is returned as an error, because a guard whose input is missing must
//     not report a pass.
//
// Escape hatch: pass `--tag <tag>` to deploy a specific tag directly — that
// path bypasses build-state (and therefore this check) entirely.
func checkBuildStateFreshness(ctx context.Context, projectDir, envName, stateKey string, st *BuildState) error {
	if st == nil || st.Commit == "" {
		return nil
	}
	anchor, enforce, err := resolveFreshnessAnchor(ctx, projectDir, envName)
	if err != nil {
		return err
	}
	if !enforce || anchor.Commit == st.Commit {
		return nil
	}
	if buildStateIsForeignToEnv(ctx, projectDir, envName, stateKey, st) {
		return nil
	}
	return fmt.Errorf(
		"refusing to deploy stale image: tag %q was built from %s, but %s.\n"+
			"  The recorded build does not match the commit this env should be running — deploying it would ship the wrong code.\n"+
			"  Fix: %s; or pass --tag %s to deploy the recorded image anyway",
		st.Tag, shortSHA(st.Commit), anchor.describe(), anchor.remedy(envName), st.Tag)
}

// buildStateIsForeignToEnv reports whether this build state describes a build
// that has nothing to do with the env being deployed — in which case its
// commit says nothing about that env's freshness and must not refuse it.
//
// WHAT THIS FIXES. The guard compares st.Commit against the env's anchor with
// no regard for whether the record is even ABOUT this env. The `default`
// record a plain `forge build` writes is the common case: it is the shared
// fallback for every env (buildStateLookupEnvs), so one local dev build
// poisons every future release deploy until someone deletes the file.
//
// Measured: the v1.5.18 control-plane release was refused three times by an
// Aug-25 `.forge/state/build-default.json` recording a `forge build` against
// a localhost:5051 dev registry. All four images deployed from release-ledger
// digests in a prod GAR that record had no part in, and its digest appeared
// nowhere in the release. The only ways forward were hand-deleting local
// state or passing --tag, which disarms the guard wholesale.
//
// The registry is the discriminator, and it is the honest one: a record whose
// registry is not the registry this deploy pushes/pulls from cannot be the
// build that ships here, whatever its commit says. That covers the dev-
// registry case above without weakening the guard for a real stale build,
// which by construction carries the env's own registry.
//
// NOT sufficient: "the bound release pins this image". The K8s path renders a
// digest for a pinned image and ignores the tag, but plainTag still reaches
// every provider via buildDeployGroupsForEnv, so a stale tag can genuinely
// ship there. TestResolveDeployImageTag_ReleaseCommitStill
// RefusesOlderImage pins exactly that, and it is right to.
//
// Two conditions, and BOTH are required:
//
//   - The record is the env-AGNOSTIC `default`. A build-<env>.json names this
//     env, so its commit is evidence about this env and is measured as before.
//     `default` is the shared fallback every env reads, so it is the only one
//     whose commit can be about somewhere else entirely.
//   - The record is of a DIRTY tree. A release is cut from a clean checkout
//     (the release scripts refuse otherwise), so a dirty build is by
//     construction not the artifact a release-bound env deploys — it is
//     someone's local iteration. A CLEAN `default` build could genuinely be
//     the thing that ships, so it keeps refusing.
//
// "Release-bound" includes a deploy that NAMES a release: it ships that
// release, and its preflight runs before the binding is written — an env's
// first release deploy has no binding yet (see resolveFreshnessAnchor).
//
// Best-effort by construction: it returns false unless it can positively
// justify standing down, so it never widens a refusal — only withdraws one
// that rests on a record which cannot be what ships.
func buildStateIsForeignToEnv(ctx context.Context, projectDir, envName, stateKey string, st *BuildState) bool {
	if st == nil || stateKey != "default" || !st.Dirty {
		return false
	}
	if hostedPinReleaseFrom(ctx) != "" {
		return true
	}
	bindings, err := bindingStoreFor(ctx, projectDir, envName)
	if err != nil {
		return false
	}
	_, bound, err := bindings.Current(ctx, envName)
	return err == nil && bound
}

// gitHEADAndClean returns the current HEAD commit, whether the working
// tree has no uncommitted TRACKED changes, and whether git was usable at
// all, evaluated in dir (the project root). ok=false means git failed
// (not a repo, no git binary) — callers treat that as "can't prove
// anything" and fall through rather than erroring. dir scopes the check
// to the project so the result doesn't depend on the process CWD (and so
// tests can run against a throwaway repo).
//
// "clean" uses --untracked-files=no on purpose: untracked files don't
// move HEAD, so a stray editor/artifact directory must not flip the
// staleness guard off. Genuine uncommitted edits to tracked files DO
// count as not-clean (the build's recorded commit can't represent them).
func gitHEADAndClean(ctx context.Context, dir string) (head string, clean, ok bool) {
	rev := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	rev.Dir = dir
	out, err := rev.Output()
	if err != nil {
		return "", false, false
	}
	head = strings.TrimSpace(string(out))
	stat := exec.CommandContext(ctx, "git", "status", "--porcelain", "--untracked-files=no")
	stat.Dir = dir
	st, serr := stat.Output()
	if serr != nil {
		return head, false, false
	}
	clean = strings.TrimSpace(string(st)) == ""
	return head, clean, true
}

// warnIfNonReproducible prints a heads-up when the build being deployed
// came from a dirty working tree or an untagged commit — the single
// "discipline" nudge toward tag-then-build, with no enforcement.
func warnIfNonReproducible(st *BuildState) {
	switch {
	case st.Dirty:
		fmt.Printf("  Warning: deploying %q built from a DIRTY working tree (commit %s) — not reproducible.\n", st.Tag, shortSHA(st.Commit))
	case st.GitTag == "":
		fmt.Printf("  Warning: deploying %q built from an UNTAGGED commit (%s) — tag the release for a reproducible version.\n", st.Tag, shortSHA(st.Commit))
	}
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	if sha == "" {
		return "unknown"
	}
	return sha
}

func gitShortSHA(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func ensureDevCluster(ctx context.Context) error {
	fmt.Println("Checking k3d cluster...")
	listCmd := exec.CommandContext(ctx, "k3d", "cluster", "list", "-o", "json")
	scrubSubprocessLogEnv(listCmd)
	out, err := listCmd.Output()
	if err != nil {
		return fmt.Errorf("k3d not available: %w\nInstall k3d: https://k3d.io", err)
	}

	// If no clusters exist or our cluster isn't found, the user needs to create one.
	if len(out) == 0 || string(out) == "[]" || string(out) == "[]\n" {
		fmt.Println("No k3d clusters found. Creating dev cluster...")
		args, cleanup, err := devClusterCreateArgs()
		if err != nil {
			return err
		}
		defer cleanup()
		// This legacy bootstrap keeps k3d's own kubeconfig write (and its
		// switch to the new context, which the single-cluster dev deploy
		// relies on), so the whole create holds the kubeconfig lock
		// (kubeconfig_lock.go).
		if err := withKubeconfigLock(func() error {
			createCmd := exec.CommandContext(ctx, "k3d", args...)
			createCmd.Stdout = os.Stdout
			createCmd.Stderr = os.Stderr
			return createCmd.Run()
		}); err != nil {
			return fmt.Errorf("failed to create k3d cluster: %w", err)
		}
	} else {
		fmt.Println("  k3d cluster found.")
		fmt.Println("  Tip: if in-cluster image pulls fail with `localhost:5050`,")
		fmt.Println("       the cluster pre-dates forge's auto-mirror config. See")
		fmt.Println("       the deploy skill ('Pre-existing k3d cluster mirror fix').")
	}
	return nil
}

// devClusterCreateArgs builds the `k3d cluster create` argv for
// ensureDevCluster's bootstrap, and returns a cleanup for the temp
// registries.yaml the no-config path writes.
//
// Both branches end at addClusterStorageArgsFn. That is the point of this
// function existing: this bootstrap used to assemble its own exec.Cmd and was
// therefore the ONE forge cluster-creation path that did not mount the kubelet
// image-GC drop-in, so the clusters it made grew containerd images without
// bound. It is also the path a project with no deploy/k3d.yaml takes on its
// first deploy, which is why the omission cost as much disk as it did.
func devClusterCreateArgs() (args []string, cleanup func(), err error) {
	cleanup = func() {}
	k3dConfig := filepath.Join("deploy", "k3d.yaml")
	if _, statErr := os.Stat(k3dConfig); statErr == nil {
		merged, cleanupCfg, mergeErr := mergeK3dConfig(k3dConfig, k3dConfigOverlay{PublicIssuer: true})
		if mergeErr != nil {
			return nil, cleanup, mergeErr
		}
		cleanup = cleanupCfg
		args = []string{"cluster", "create", "--config", merged.path}
	} else {
		// Fallback create path (no project-level deploy/k3d.yaml).
		// Write a temp registries.yaml that mirrors the canonical
		// `localhost:5050 → registry.localhost:5000` mapping, so
		// in-cluster pulls succeed for images pushed to the
		// host-visible `localhost:5050`. Without this, `docker push
		// localhost:5050/<image>` lands in the registry but pods
		// ImagePullBackOff because `localhost:5050` doesn't resolve
		// from inside the node container. The project-templated
		// `deploy/k3d.yaml` carries the same mirrors inline via the
		// k3d Simple config's `registries.config` block — see
		// internal/templates/deploy/k3d.yaml.tmpl.
		regsPath, regsErr := writeFallbackRegistriesYAML()
		if regsErr != nil {
			return nil, cleanup, fmt.Errorf("write fallback registries.yaml: %w", regsErr)
		}
		cleanup = func() { _ = os.Remove(regsPath) }
		args = []string{"cluster", "create", "dev",
			"--registry-create", "dev-registry:0.0.0.0:5050",
			"--registry-config", regsPath,
			"--servers", "1",
			"--no-lb",
		}
		args = append(args, k3sIssuerFlags()...)
	}
	args, err = addClusterStorageArgsFn(args)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return args, cleanup, nil
}

// fallbackRegistriesYAML is the canonical containerd mirror config
// written when forge creates a k3d cluster without a project-level
// `deploy/k3d.yaml` to drive it. Kept as a top-level const so the
// content is reviewable in one place.
const fallbackRegistriesYAML = `mirrors:
  "registry.localhost:5000":
    endpoint:
      - http://registry.localhost:5000
  "registry.localhost:5050":
    endpoint:
      - http://registry.localhost:5000
  "localhost:5050":
    endpoint:
      - http://registry.localhost:5000
`

// writeFallbackRegistriesYAML writes the canonical mirror config to a
// temp file and returns the path. Caller is responsible for removing
// it after `k3d cluster create` returns.
func writeFallbackRegistriesYAML() (string, error) {
	f, err := os.CreateTemp("", "forge-k3d-registries-*.yaml")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(fallbackRegistriesYAML); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func buildAndPushLocal(ctx context.Context, cfg *config.ProjectConfig, tag, targetArchFlag string, entities *KCLEntities) error {
	// Where the project image goes is the reference the workload declaring it
	// wrote — the same one the dev cluster pulls. Nothing here composes a
	// destination out of an env field, because there is no env field.
	repository := declaredProjectRepositoryForEnv(ctx, "dev", cfg.Name)
	if repository == "" {
		// The env's real declaration, so the runbook names the actual gap
		// rather than claiming the env declares no workloads at all.
		return noPushableImagesError("forge env deploy dev", "dev", entities)
	}

	// Build and push the single project image from root Dockerfile.
	dockerfile := "Dockerfile"
	if _, err := os.Stat(dockerfile); os.IsNotExist(err) {
		fmt.Printf("  Skipping %s (no Dockerfile)\n", cfg.Name)
		return nil
	}

	imageRef := repository + ":" + tag

	// Skip the rebuild if the image is already present at the tag (e.g.
	// the user just ran `forge env build --push` against the same registry).
	// `docker manifest inspect` is cheap (HEAD against the registry) and
	// avoids an O(minutes) docker build + push on the hot path.
	if imageExistsInRegistry(ctx, imageRef) {
		fmt.Printf("  %s already present — skipping rebuild.\n", imageRef)
		return nil
	}

	fmt.Printf("  Building and pushing %s...\n", imageRef)

	// Resolve cross-compile target: --target-arch flag > forge.yaml
	// deploy.target_arch > "amd64" (k8s host default). When the resolved
	// target equals the host arch, no --platform flag is emitted.
	crossArch := resolveDeployArch(cfg.Deploy.TargetArch, targetArchFlag)

	buildArgs := []string{"build"}
	if crossArch != "" {
		buildArgs = append(buildArgs, "--platform=linux/"+crossArch)
		fmt.Printf("  [build] cross-compiling for linux/%s (host: %s/%s)\n",
			crossArch, runtime.GOOS, runtime.GOARCH)
	}
	buildArgs = append(buildArgs, "-t", imageRef)
	// Apply docker.build_contexts from forge.yaml so sibling-checkout
	// replace directives resolve in the deploy-time rebuild too. Shares
	// the build.go helper so the path-resolution + scheme passthrough
	// semantics stay in lockstep across `forge build --docker` and
	// `forge env deploy`.
	if err := resolveNamedBuildContexts(ctx, projectDirForKCL(), cfg.Docker.BuildContexts); err != nil {
		return err
	}
	buildArgs = appendBuildContexts(buildArgs, cfg, "")
	buildArgs = append(buildArgs, "-f", dockerfile, ".")
	if err := prepareDockerBuildStorage(ctx, projectDirForKCL()); err != nil {
		return err
	}
	buildCmd := exec.CommandContext(ctx, "docker", buildArgs...)
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("docker build for %s failed: %w", cfg.Name, err)
	}

	if err := dockerPush(ctx, imageRef); err != nil {
		return fmt.Errorf("%s: %w", cfg.Name, err)
	}

	return nil
}

// resolveDeployArch picks the target GOARCH for a deploy build. The
// dispatch order is: explicit --target-arch override, then forge.yaml's
// deploy.target_arch, then the "amd64" default (which reflects the
// empirical reality that most k8s nodes are amd64). Returns the empty
// string when the resolved target equals runtime.GOARCH — the empty
// return signals callers that no --platform flag is needed.
//
// Unlike resolveBuildArch in build.go, this function always falls back
// to amd64 (i.e. deploy is treated as the docker-context case in
// build.go). `forge env deploy` always builds an image destined for a
// cluster node, so the "no cross-compile, use host arch" outcome
// happens only when host == target.
func resolveDeployArch(cfgArch, flagArch string) string {
	target := flagArch
	if target == "" {
		target = cfgArch
	}
	if target == "" {
		target = "amd64"
	}
	if target == runtime.GOARCH {
		return ""
	}
	return target
}

// imageExistsInRegistry returns true when `docker manifest inspect` can
// resolve the given image:tag, i.e. it's already in the registry. Used
// by buildAndPushLocal to short-circuit the redundant deploy-time build
// when `forge env build --push` has already pushed the same tag. Any error
// (manifest absent, registry unreachable, manifest API disabled) yields
// false so we fall through to the normal build+push path.
//
// For local/HTTP registries we use --insecure so the check works against
// the dev k3d registry (localhost:5051), which doesn't speak TLS.
func imageExistsInRegistry(ctx context.Context, ref string) bool {
	args := []string{"manifest", "inspect"}
	if isInsecureRegistry(ref) {
		args = append(args, "--insecure")
	}
	args = append(args, ref)
	cmd := exec.CommandContext(ctx, "docker", args...)
	return cmd.Run() == nil
}

// isInsecureRegistry reports whether the image ref points at a registry
// that should be treated as HTTP. We treat localhost / 127.0.0.1 /
// registry.localhost as insecure — these are the dev-cluster k3d
// registries forge sets up. Anything else (ghcr.io, gcr.io, AR…) is
// HTTPS by default.
func isInsecureRegistry(ref string) bool {
	host, _, _ := strings.Cut(ref, "/")
	hostOnly, _, _ := strings.Cut(host, ":")
	switch hostOnly {
	case "localhost", "127.0.0.1", "registry.localhost":
		return true
	}
	return false
}

// deployPreflightInput bundles what runDeployPreflight needs to render the
// env's manifests and run the deployability checks against the live target.
type deployPreflightInput struct {
	mainK     string
	imageTag  string
	namespace string
	env       string
	envCfgKV  map[string]string
	deployCtx string
	targets   []string
	// report, when non-nil, receives the structured preflight findings.
	// Nil-safe, so text mode threads nil through this identical path.
	report *deployReport
	// imageDigests is the per-image name→digest map (image NAME →
	// "sha256:..."). Threaded into the manifest render so the preflight
	// checks the SAME `<image>@<digest>` refs the apply will ship — a
	// preflight that pinned every image to one env-wide digest would pass
	// or fail on the wrong refs. Empty on the no-digest path.
	imageDigests map[string]string
	// targetArch is the DECLARED node architecture of the target cluster
	// (GOARCH form, from the env's KCL deploy.Cluster.platform). Threads into
	// PreflightOpts.TargetArch to drive the arch gate. EMPTY when the env
	// hasn't declared a platform — the gate is inert (WARN-don't-block), so
	// envs that predate the platform field (incl. local e2e) aren't
	// false-failed.
	targetArch string
	// requiredSecrets are the env's DECLARED external Secret prerequisites
	// (forge.ExternalSecret) — out-of-band Secrets the deploy depends on but
	// forge does NOT create. Threaded into PreflightOpts.RequiredSecrets so a
	// declared-required-but-absent Secret/key BLOCKS the deploy (and a
	// value_group byte mismatch is caught). Empty => no declared prereqs.
	requiredSecrets []cluster.RequiredSecret
	// secretContexts are the kubectl contexts of every cluster this env
	// deploys to. Each declared prerequisite is checked only on the clusters
	// that consume it (scopeRequiredSecretsToClusters); this full set is the
	// fallback for one nothing attributes. Checking only the primary would let
	// a workload on a secondary reach a rollout that dies with
	// CreateContainerConfigError; checking every Secret everywhere refused
	// deploys for Secrets a cluster never uses.
	secretContexts []string
	// deployedTo are the clusters this deploy WRITES (its k8s groups). A
	// declared Secret is checked only where it is both consumed and written:
	// a targeted deploy of the daemon cluster must not be refused for the
	// hub's IdP credentials. Empty = no narrowing.
	deployedTo []string
	// entities and groups feed the per-Secret cluster attribution: the
	// deploy's own router decides which cluster each rendered object lands on.
	// They are the WHOLE env's (the routing topology), not a --target subset.
	entities *KCLEntities
	groups   []deploytarget.ServiceGroup
	// secretSupply is the env's bundle-internal Secret SUPPLY for the
	// render-time back-propagation gate: the Secrets the bundle PROVIDES via a
	// forge.KubeconfigSecret mint, a forge.ExternalSecret promise, or a
	// rendered secret_provider Secret. Threaded into PreflightOpts.SecretSupply
	// so a workload mounting a Secret NOTHING declares fails at render time
	// (the silent FailedMount fix). Rendered-stream Secrets are collected from
	// the manifests directly, so this carries only the entity-derived supply.
	secretSupply []cluster.SecretSupply
	// provisionedByDeploy are the Secrets this deploy MINTS after the
	// preflight (the minted forge.KubeconfigSecret declarations). Threaded
	// into PreflightOpts.ProvisionedByDeploy so the live existence check does
	// not block the deploy on a Secret that same deploy creates. See
	// provisionedByDeployForPreflight.
	provisionedByDeploy []cluster.ProvisionedSecret
	// storeProbe answers the store-source preflight (CRDs, operator owner,
	// store readiness, store names). Nil disables it: tests inject a fake, the
	// deploy builds the live one in runDeployPreflightForEnv.
	storeProbe cluster.StoreProbe
}

// requiredSecretsForPreflight projects the env's declared external Secret
// prerequisites (forge.ExternalSecret) onto the cluster-package
// RequiredSecret shape the preflight consumes. Keeps the cluster package
// decoupled from the cli entity types. nil entities / no declarations =>
// nil (the preflight check stays inert).
func requiredSecretsForPreflight(entities *KCLEntities) []cluster.RequiredSecret {
	if entities == nil || len(entities.RequiredSecrets) == 0 {
		return nil
	}
	out := make([]cluster.RequiredSecret, 0, len(entities.RequiredSecrets))
	for _, s := range entities.RequiredSecrets {
		// ESO creates an Orphan store-backed Secret AFTER this deploy; demanding
		// it up front would block the deploy that brings its deliverer. A Merge
		// one only updates a Secret that exists, so it keeps the check.
		if s.Source != nil && s.Source.CreationPolicy != "Merge" {
			continue
		}
		out = append(out, cluster.RequiredSecret{
			Name:       s.Name,
			Namespace:  s.Namespace,
			Keys:       s.Keys,
			ValueGroup: s.ValueGroup,
		})
	}
	return out
}

// secretSupplyForPreflight projects the env's bundle-internal Secret SUPPLY
// onto the cluster-package SecretSupply shape the render-time back-propagation
// gate consumes — keeping the cluster package decoupled from the cli entity
// types (the same pattern requiredSecretsForPreflight uses). It enumerates the
// Secrets the bundle PROVIDES that aren't rendered as a `kind: Secret`
// document (those the gate collects from the manifest stream itself):
//
//   - forge.KubeconfigSecret mints (entities.KubeconfigSecrets) — forge mints
//     each fresh every up and applies it as a k8s Secret. The control-plane bug
//     that motivated this gate was a mounted Secret with NO matching
//     KubeconfigSecret; declaring one is the supply that satisfies the demand.
//   - forge.ExternalSecret promises (entities.RequiredSecrets) — the author's
//     explicit out-of-band promise the Secret exists. Counts as SATISFIED here;
//     the LIVE preflight separately verifies it's actually provisioned.
//   - declared rendered Secrets (Bundle.rendered_secrets, plus a
//     RenderedSecrets provider's SecretProvider.Secrets) — forge renders +
//     applies these CLI-side BEFORE the Deployments, so they never appear in
//     the manifest stream; without this entry a plain manifest mounting one
//     reads as an undeclared mount.
//   - dotenv secret_provider Secrets (Type=="dotenv") — forge renders these
//     CLI-side from the declared cluster refs and applies them BEFORE the
//     Deployments roll out (see applyK8sSecretsFromProvider), so the mount
//     resolves on first schedule. They are supply even though they never appear
//     in the rendered manifest stream: a dotenv provider carries no
//     SecretProvider.Secrets, so without this the scaffold's own dev bundle
//     reports every sensitive config field as an undeclared mount.
//
// nil entities / no supply => nil (only rendered-stream Secrets then count).
// provisionedByDeployForPreflight projects the Secrets THIS deploy creates
// after the preflight onto the cluster-package shape the live existence check
// consults — keeping the cluster package decoupled from the cli entity types
// (the same pattern requiredSecretsForPreflight uses).
//
// Today that is exactly the minted forge.KubeconfigSecret declarations, read
// from deployMintedKubeconfigSecrets — the SAME filter the mint phase iterates,
// so the gate cannot drift from what is actually provisioned. Without this the
// preflight blocks on its own output: the Secret is absent precisely because
// the deploy being refused is the one that mints it.
//
// A declaration forge does NOT mint (no service_account — a copied kubeconfig)
// is absent from this set and still BLOCKS when missing, which is correct: the
// deploy will not create it.
//
// nil entities / no minted declarations => nil (the exemption is inert).
func provisionedByDeployForPreflight(entities *KCLEntities) []cluster.ProvisionedSecret {
	minted := deployMintedKubeconfigSecrets(entities)
	var delivered []ControlPlaneSecretEntity
	if entities != nil {
		delivered = entities.ControlPlaneSecrets
	}
	byStore := storeProvisionedByDeploy(entities)
	if len(minted)+len(delivered)+len(byStore) == 0 {
		return nil
	}
	out := make([]cluster.ProvisionedSecret, 0, len(minted)+len(delivered)+len(byStore))
	out = append(out, byStore...)
	for _, k := range minted {
		out = append(out, cluster.ProvisionedSecret{
			Name:      k.Name,
			Namespace: k.Namespace,
			By:        cluster.SupplyKubeconfigSecret,
		})
	}
	// A Secret the control plane delivers AFTER this deploy cannot be a
	// precondition of it. Its mount is optional, so the pod starts without it,
	// and the live reference check must not read the absence as a missing
	// dependency: that would refuse every first deploy, on a Secret whose
	// deliverer is waiting for this deploy to record it.
	for _, c := range delivered {
		out = append(out, cluster.ProvisionedSecret{
			Name:      c.Name,
			Namespace: c.Namespace,
			By:        cluster.SupplyControlPlaneSecret,
		})
	}
	return out
}

func secretSupplyForPreflight(entities *KCLEntities) []cluster.SecretSupply {
	if entities == nil {
		return nil
	}
	var out []cluster.SecretSupply
	// A Secret the control plane delivers is the bundle's promise that the
	// Secret will exist, exactly as an ExternalSecret is. It is supply for the
	// render-time gate, so a workload mounting one is not "undeclared", and it
	// is deliberately NOT a RequiredSecret, so the live check never demands it
	// before the control plane has had a chance to write it.
	for _, c := range entities.ControlPlaneSecrets {
		out = append(out, cluster.SecretSupply{
			Name:      c.Name,
			Namespace: c.Namespace,
			Kind:      cluster.SupplyControlPlaneSecret,
		})
	}
	for _, k := range entities.KubeconfigSecrets {
		out = append(out, cluster.SecretSupply{
			Name:      k.Name,
			Namespace: k.Namespace,
			Kind:      cluster.SupplyKubeconfigSecret,
		})
	}
	for _, s := range entities.RequiredSecrets {
		out = append(out, cluster.SecretSupply{
			Name:      s.Name,
			Namespace: s.Namespace,
			Kind:      cluster.SupplyExternalSecret,
		})
	}
	for _, s := range declaredSecretEntities(entities) {
		out = append(out, cluster.SecretSupply{
			Name:      s.Name,
			Namespace: s.Namespace,
			Kind:      cluster.SupplyRenderedManifest,
		})
	}
	if entities.SecretProvider != nil {
		if entities.SecretProvider.Type == "file" {
			// The refs forge resolves + renders into Secrets at deploy time are
			// exactly the cluster-service refs; dedupe by Secret name.
			seen := map[string]bool{}
			for _, r := range secretRefsForK8sServices(entities) {
				if r.SecretName == "" || seen[r.SecretName] {
					continue
				}
				seen[r.SecretName] = true
				out = append(out, cluster.SecretSupply{
					Name: r.SecretName,
					Kind: cluster.SupplyGenerated,
				})
			}
		}
	}
	return out
}

// runDeployPreflight renders the env's manifest bundle and runs the
// cluster.Preflight deployability checks (referenced Secret keys present in
// the live cluster + container images present in the registry) BEFORE the
// first apply. Returns a grouped, actionable error when anything is missing
// — nothing is applied. A no-op when the bundle has nothing to check.
//
// The Secret check is gated to REMOTE clusters: on a local dev cluster
// forge applies the dotenv-projected Secrets itself moments after this
// runs, so they don't exist yet and checking them would false-fail the
// inner loop. Local-registry images are skipped via cluster.LocalImageRef
// for the same don't-break-dev-loop reason. Image checks DO still run on a
// local cluster for remote-registry images (e.g. ghcr.io refs in a local
// test), since those are reachable and a miss is real.
// declaredClusterContexts lists the kubectl contexts an env deploys to: the
// resolved deploy target, every cluster its KCL declares, and every cluster a
// k8s apply group targets. A single-cluster env yields exactly the target, so
// the multi-cluster handling is inert for the common case.
//
// The groups are the part that must not be skipped. e.Clusters lists only the
// clusters forge CREATES (k3d); a cloud env's second cluster is declared only
// by where its workloads route (a ClusterTarget / K8sCluster.cluster), so it
// appears in no entity list — only as a group the dispatch applies to.
// control-plane's prod applies PriorityClasses to prod-daemon-v2 that way, and
// the report's all_kube_contexts listed prod alone: a confirmation dialog would
// have shown one cluster for a deploy that writes to two.
func declaredClusterContexts(e *KCLEntities, deployCtx string, groups []deploytarget.ServiceGroup) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(c string) {
		c = strings.TrimSpace(c)
		if c == "" {
			return
		}
		if _, dup := seen[c]; dup {
			return
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	add(deployCtx)
	if e != nil {
		for _, c := range e.Clusters {
			add(c.Context)
		}
	}
	for _, g := range groups {
		if g.ProviderID == "k8s-cluster" {
			add(g.Cluster)
		}
	}
	return out
}

func runDeployPreflight(ctx context.Context, in deployPreflightInput) error {
	// Render the same manifest stream the apply will consume. Applying the
	// --target filter keeps the preflight scoped to exactly the apps about
	// to be applied (a targeted single-app deploy shouldn't fail on another
	// app's missing image).
	manifests, err := cluster.RenderManifests(ctx, in.mainK, in.imageTag, in.namespace, in.env, in.envCfgKV, in.imageDigests)
	if err != nil {
		return fmt.Errorf("preflight: render manifests: %w", err)
	}
	// Attribute each declared prerequisite to the clusters that consume it
	// BEFORE the --target filter: a targeted deploy still checks every
	// declared Secret, and the filtered stream no longer holds the consumers
	// that place it.
	requiredSecrets, unattributed := scopeRequiredSecretsToClusters(
		in.requiredSecrets, manifests, in.groups, in.entities, in.namespace)
	// Then narrow to the clusters this deploy actually writes. Attribution
	// answers "who consumes it"; this answers "is that cluster ours to break
	// right now" — a deploy that never touches the hub is not blocked by a
	// Secret only the hub reads.
	requiredSecrets = restrictSecretsToDeployedClusters(requiredSecrets, in.deployedTo)
	unattributed = restrictSecretsToDeployedClusters(unattributed, in.deployedTo)
	if len(in.targets) > 0 {
		// Same exclusive --target filter the apply uses (keep iff the
		// manifest's KCL-declared group ∈ targets) so the preflight checks
		// EXACTLY the manifests about to be applied.
		manifests = cluster.SelectManifestsByGroup(manifests, in.targets)
	}

	opts := cluster.PreflightOpts{
		Manifests: manifests,
		Namespace: in.namespace,
		Images:    cluster.RegistryImageChecker{},
		ImageArch: cluster.DockerImageArchChecker{},
		// One recheck for a miss or an inconclusive lookup, a moment apart:
		// a registry having a bad minute answers wrongly in bursts.
		ImageRecheckDelay: 2 * time.Second,
		TargetArch:        in.targetArch,
		SkipImageRef:      cluster.LocalImageRef,
		// Render-time secret back-propagation supply. Set UNCONDITIONALLY (not
		// gated behind the remote-cluster block below): the back-propagation
		// gate is a pure, no-cluster check that runs on every deploy — incl.
		// local dev clusters and --dry-run — so a workload mounting a Secret
		// nothing declares fails fast at render time rather than rotting on
		// FailedMount. Rendered-stream Secrets are collected from the manifests;
		// this carries the KubeconfigSecret / ExternalSecret / rendered-provider
		// supply.
		SecretSupply: in.secretSupply,
		// The Secrets this deploy mints a few phases after the preflight. They
		// are exempt from the LIVE existence check only — demanding them
		// up-front blocks the deploy on its own output. See
		// PreflightOpts.ProvisionedByDeploy.
		ProvisionedByDeploy: in.provisionedByDeploy,
	}
	// Store-backed Secrets (forge.StoreSource): the operator, its CRDs, the
	// store's readiness and whether it holds the names the ExternalSecrets
	// read. Whole-env topology, narrowed by --target to what this deploy
	// installs; each expectation names its own cluster, so it runs for a local
	// cluster too (a dev OpenBao is a store like any other).
	if in.storeProbe != nil {
		if in.entities != nil {
			opts.StoreExpect = storeExpectations(in.entities, in.targets, storeOwner(in.entities.Project, in.env))
		}
		if len(opts.StoreExpect) > 0 {
			opts.StoreProbe = in.storeProbe
		}
	}
	// Secret + ConfigMap checks only against a REMOTE cluster (see
	// docstring). An empty or local context leaves opts.Secrets /
	// opts.ConfigMaps nil → cluster.Preflight skips those checks entirely.
	// Both are gated identically: on a local dev cluster forge applies the
	// projected Secret AND ConfigMap moments after this runs, so neither
	// exists yet and checking would false-fail the inner loop.
	if in.deployCtx != "" && !isLocalCluster(in.deployCtx) {
		opts.Context = in.deployCtx
		opts.Secrets = cluster.KubectlSecretGetter{}
		opts.ConfigMaps = cluster.KubectlConfigMapGetter{}
		// Each referenced Secret / ConfigMap is checked on the cluster its
		// referencing object is applied to, by the deploy's own router. Nil
		// for a single-cluster env, where everything lands on deployCtx.
		opts.DocumentContexts = preflightDocumentContexts(in.groups, in.entities)
		// CRD / served-kind gate: verify the cluster serves every NON-CORE kind
		// the bundle renders (e.g. GRPCRoute needs the Gateway API channel)
		// BEFORE the apply. Without it a missing CRD fails `no matches for kind`
		// mid-rollout, AFTER other resources already applied. Gated to remote
		// clusters like the Secret check — the local dev cluster's CRDs are
		// reconciled by the same up/deploy flow.
		opts.ServedKinds = cluster.KubectlServedKinds{}
		// A StatefulSet whose declared volume cannot be converged (a shrink, a
		// changed class, a class that cannot expand) fails here, naming it,
		// instead of as an immutable-field rejection mid-apply or a wedged
		// Flux tree. Same decision the resize Job makes.
		opts.VolumeResize = cluster.KubectlVolumeResize{}
		// Declared external Secret prerequisites (forge.ExternalSecret): each
		// in its OWN declared namespace, verified against the live target. A
		// declared-required-but-absent Secret/key BLOCKS — the converse of
		// "render green then ACME hangs silently". The byte-match group compare
		// needs the live values, hence the value getter. Gated to remote
		// clusters like the Secret check (a local dev cluster's out-of-band
		// Secrets are provisioned by the same up flow).
		opts.SecretValues = cluster.KubectlSecretValueGetter{}
		// Verify images using the CLUSTER's pull credentials (the bundle's
		// imagePullSecrets), not just the local docker daemon: when the local
		// daemon lacks creds for a private registry, an auth-denied lookup is a
		// FALSE block for an image the cluster can pull. The resolver reads the
		// pull Secrets' .dockerconfigjson from the target cluster so an
		// auth-denied lookup is retried from the cluster's perspective for a
		// TRUE verdict. Gated to remote clusters (same as the Secret check) —
		// local k3d images are skipped via LocalImageRef anyway.
		opts.PullCreds = cluster.KubectlPullCredsResolver{}
		// An image this cluster is already RUNNING is not "missing",
		// whatever the registry answers this minute: a would-be block on it
		// becomes a warning naming both facts. Listed only when an image
		// would otherwise block.
		opts.RunningImages = cluster.KubectlRunningImages{}
	}

	// DECLARED external Secret prerequisites are checked on every cluster that
	// consumes them, local ones included. The remote-only gate above exists because forge
	// applies its own projected Secrets moments later, so checking those
	// locally would false-fail the inner loop — but a forge.ExternalSecret is
	// by definition one forge does NOT create, so that reasoning never applied
	// to it. Skipping it locally is why a dev deploy could reach a rollout and
	// die on a Secret that was missing, or present but missing a KEY, in the
	// secondary cluster.
	if len(requiredSecrets) > 0 && len(in.secretContexts) > 0 {
		opts.RequiredSecrets = requiredSecrets
		opts.RequiredSecretContexts = in.secretContexts
		if opts.Secrets == nil {
			opts.Secrets = cluster.KubectlSecretGetter{}
		}
	}
	// PreflightReport returns the SAME error Preflight would (it is the one
	// implementation; Preflight is a projection of it), plus the structured
	// findings the error string would otherwise be the only record of. The
	// result is recorded whether or not it blocks, so a clean preflight is
	// distinguishable from one that never ran.
	result, err := cluster.PreflightReport(ctx, opts)
	in.report.setPreflightResult(result)
	if note := unattributedSecretsNote(unattributed); err != nil && note != "" && len(result.MissingRequiredSecretKeys) > 0 {
		return fmt.Errorf("%w\n%s", err, note)
	}
	return err
}

// expectedClusterForEnv returns the expected kubectl context name for
// an environment. Resolution priority:
//  1. The rendered KCL's declared cluster_target.cluster for env <envName>,
//     else its first K8sCluster.cluster (see k8sClusterFieldFromEntities)
//  2. For dev: k3d-<project-name>
//  3. Empty string — no expectation declared (skip the guard)
//
// Reads the rendered KCL via RenderKCL using a background context so
// the lookup remains usable from the explain path where we don't carry
// a request context. Failures fall through to the dev default / empty
// — the env-cluster guard is a recommendation, not a hard dependency.
func expectedClusterForEnv(ctx context.Context, cfg *config.ProjectConfig, envName string) string {
	if clusterName := firstK8sClusterField(ctx, envName, "cluster"); clusterName != "" {
		return clusterName
	}
	if envName == "dev" && cfg != nil {
		// Dev's default is the k3d cluster forge env deploy dev creates.
		return "k3d-" + cfg.Name
	}
	return ""
}

// firstK8sClusterField reads the rendered KCL for env and returns the
// requested field ("cluster" / "namespace" / "domain" / "platform")
// from the env's declared cluster_target, falling back to the first
// Cluster-bound workload's runtime. Returns "" when KCL can't be
// rendered or the field is declared nowhere.
func firstK8sClusterField(ctx context.Context, envName, field string) string {
	if envName == "" {
		return ""
	}
	entities, err := RenderKCL(ctx, projectDirForKCL(), envName)
	if err != nil || entities == nil {
		return ""
	}
	return k8sClusterFieldFromEntities(entities, field)
}

// k8sClusterFieldFromEntities is firstK8sClusterField's resolution over an
// ALREADY-RENDERED entity set. Split out so a caller that has just rendered
// the env (`forge env render`) reads the field off what it has instead of
// paying for a second full KCL render to ask one question — and so both
// callers resolve it by the same rule.
func k8sClusterFieldFromEntities(entities *KCLEntities, field string) string {
	if entities == nil {
		return ""
	}
	// The Bundle's declared env-wide target is the answer whenever it
	// states the field. Walking workloads for it is only a fallback for a
	// contract with no cluster_target: the first Cluster-bound workload can
	// be one pinned to ANOTHER cluster, which is how an env's whole render
	// once moved into that workload's namespace.
	if v := entities.ClusterTarget.field(field); v != "" {
		return v
	}
	for _, w := range entities.WorkloadsOn(RuntimeCluster) {
		c := w.Runtime.Cluster
		var v string
		switch field {
		case "cluster":
			v = c.Cluster
		case "namespace":
			v = c.Namespace
		case "domain":
			v = c.Domain
		case "platform":
			v = c.Platform
		}
		if v != "" {
			return v
		}
	}
	// Fallback: an env with no cluster_target and no Cluster-bound workload
	// (only additional manifests) still names a namespace in its stream's
	// objects. The cluster (kubectl context) is not a field on any k8s
	// object, so only "namespace" has a manifest fallback; the apply
	// chokepoint refuses an empty context.
	if field == "namespace" && entities.ManifestNamespace != "" {
		return entities.ManifestNamespace
	}
	return ""
}

// k8sClusterNamespaceForEnv reads the rendered KCL and returns the
// first K8sCluster.namespace declared for env. Returns "" when no
// cluster-shaped service is declared or the field is unset.
func k8sClusterNamespaceForEnv(ctx context.Context, envName string) string {
	return firstK8sClusterField(ctx, envName, "namespace")
}

// declaredImageDestinationsForEnv renders env and returns every repository its
// workloads declare for an image forge builds. Nil when the env declares none
// or cannot be rendered.
func declaredImageDestinationsForEnv(ctx context.Context, envName string) []imageDestination {
	if envName == "" {
		return nil
	}
	entities, err := RenderKCL(ctx, projectDirForKCL(), envName)
	if err != nil {
		return nil
	}
	return declaredImageDestinations(entities)
}

// declaredProjectRepositoryForEnv is the repository env's workloads declare for
// the artifact named `image` — the project image, usually. "" when no workload
// in that env declares it.
func declaredProjectRepositoryForEnv(ctx context.Context, envName, image string) string {
	for _, d := range declaredImageDestinationsForEnv(ctx, envName) {
		if repositoryName(d.repository) == image {
			return d.repository
		}
	}
	return ""
}

// verifyKubectlContext is the DECLARATIVE env-cluster guard. The env's
// KCL declares its target cluster (`forge.K8sCluster.cluster`), and that
// name IS the kubectl context the deploy applies to (threaded per-command
// via --context). So the guard no longer cares what context is currently
// ACTIVE — it deliberately does NOT refuse on a current-vs-expected
// mismatch (that would block a valid deploy: we apply to the declared
// cluster regardless of the active context). Instead it fails fast when
// the declared cluster has no matching kubectl context, listing the
// available contexts — the check that makes a wrong-cluster deploy
// impossible while never depending on the globally-switched active
// context (the cross-cluster contamination incident).
//
// There is no CLI override: the declared cluster is the only source. An
// env with no declared cluster skips the guard (host-only / compose) —
// those envs run no kubectl writes, so there's nothing to guard.
func verifyKubectlContext(ctx context.Context, cfg *config.ProjectConfig, envName string) error {
	expected := expectedClusterForEnv(ctx, cfg, envName)
	if expected == "" {
		// No expectation declared for this env. Print a one-liner
		// reminder so users know they can lock it down, but don't
		// block the deploy (backwards-compatible default).
		fmt.Printf("Note: no forge.K8sCluster.cluster declared in deploy/kcl/%s/main.k — declared-cluster guard skipped.\n", envName)
		return nil
	}

	available, err := kubectlContextNames(ctx)
	if err != nil {
		return err
	}
	if err := declaredContextExistsVerdict(envName, expected, available); err != nil {
		return err
	}
	fmt.Printf("kubectl context: %s (declared by env %s; applied per-command via the declared cluster)\n", expected, envName)
	return nil
}

// kubectlContextNames returns the set of context names declared in the
// active kubeconfig (`kubectl config get-contexts -o name`). Returns an
// error when kubectl isn't installed / configured — the caller turns
// that into a clear deploy-time failure rather than silently applying
// to whatever's active.
func kubectlContextNames(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, "kubectl", "config", "get-contexts", "-o", "name").Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl config get-contexts: %w (is kubectl installed and configured?)", err)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			names = append(names, s)
		}
	}
	return names, nil
}

// declaredContextExistsVerdict is the pure core of the declarative
// fail-fast guard: the declared cluster name IS the kubectl context the
// deploy will apply to, so if it isn't present in the kubeconfig we
// refuse with a clear error listing the available contexts. Lifted out so
// unit tests exercise the missing-context path without shelling to
// kubectl. An empty declared value (env declares no cluster) is a no-op.
func declaredContextExistsVerdict(envName, declared string, available []string) error {
	if declared == "" {
		return nil
	}
	for _, c := range available {
		if c == declared {
			return nil
		}
	}
	// A `k3d-` context is a LOCAL cluster forge creates, so the first fix is
	// to create it — not to fetch credentials for a cloud cluster that does
	// not exist.
	createIt := ""
	if strings.HasPrefix(declared, "k3d-") {
		createIt = fmt.Sprintf("  - create the local cluster: `forge cluster up` (it creates %q from deploy/k3d.yaml)\n", declared)
	}
	return fmt.Errorf(
		"env %q declares cluster %q but no such kubectl context exists.\n"+
			"  available contexts: %s\n"+
			"\n"+
			"refusing to deploy (the declared cluster is the kubectl context — this is what makes wrong-cluster deploys impossible). Fix with one of:\n"+
			"%s"+
			"  - add the context to your kubeconfig (e.g. `gcloud container clusters get-credentials ...`)\n"+
			"  - correct the cluster the env's KCL declares (a ClusterTarget, OnCluster runtime or forge.Manifests group) to match an existing context",
		envName, declared, emptyAs(strings.Join(available, ", "), "(none)"), createIt)
}

// verifyDeclaredContextsExist is the post-build, MULTI-CLUSTER completion
// of the declarative guard: every K8sCluster group declares its target
// cluster (group.Cluster, from KCL `forge.K8sCluster.cluster`), and that
// name IS the kubectl context we'll apply that group to. If any declared
// context isn't present in the kubeconfig we refuse the deploy listing the
// available contexts — instead of silently landing on whatever context is
// currently active. (The single-cluster case is already caught earlier by
// verifyKubectlContext, before the image build; this covers a rare env
// whose groups span multiple clusters.)
//
// There is no CLI override. Groups without a declared cluster (host-only
// / compose, dev env with blank cluster) are skipped — those run no
// kubectl writes, so there's nothing to guard.
func verifyDeclaredContextsExist(ctx context.Context, envName string, groups []deploytarget.ServiceGroup) error {
	// Collect the distinct declared clusters across the K8sCluster
	// groups (a multi-cluster env applies each group to its own).
	declared := map[string]struct{}{}
	for _, g := range groups {
		if g.ProviderID == "k8s-cluster" && g.Cluster != "" {
			declared[g.Cluster] = struct{}{}
		}
	}
	if len(declared) == 0 {
		return nil
	}
	available, err := kubectlContextNames(ctx)
	if err != nil {
		return err
	}
	// Deterministic order so the error names the same cluster every run.
	var declaredList []string
	for c := range declared {
		declaredList = append(declaredList, c)
	}
	sort.Strings(declaredList)
	for _, c := range declaredList {
		if verr := declaredContextExistsVerdict(envName, c, available); verr != nil {
			return verr
		}
	}
	return nil
}

// applyK8sSecretsFromProvider renders + applies plaintext k8s Secret
// manifests from a dotenv secret_provider, BEFORE the Deployments roll
// out so each Deployment's secretKeyRef resolves on first schedule.
//
// Sequence:
//  1. Build the provider and fail-fast validate that every declared
//     cluster secret ref resolves (no-op for external/none providers —
//     forge can't see those values, they're provisioned out-of-band).
//  2. For a dotenv provider ONLY: guard that every targeted k8s cluster
//     is a recognized LOCAL dev cluster. dotenv renders PLAINTEXT
//     Secrets; shipping those into a remote/prod cluster is a footgun,
//     so we refuse and point the user at forge.ExternalSecrets {}.
//  3. Render the Secret manifests and apply them via the same
//     cluster.KubectlApply path the Deployments use — once per cluster and
//     namespace whose workloads declare them (placeProviderSecretRefs), so
//     a multi-cluster env's every consumer finds its Secret.
//
// external/none providers produce no manifests (RenderK8sSecrets returns
// nil), so this is a no-op for them beyond the validation gate.
func applyK8sSecretsFromProvider(ctx context.Context, entities *KCLEntities, groups []deploytarget.ServiceGroup, namespace, kubeContext, envName string, dryRun bool) error {
	return applyK8sSecretsFromProviderTo(ctx, entities, groups, namespace, kubeContext, envName, dryRun, secretTarget{sink: applySecretsWithKubectl})
}

// secretPlacement is one (cluster, namespace) worth of rendered Secrets,
// handed to a [secretSink] once everything about it has been validated.
type secretPlacement struct {
	cluster, namespace string
	mans               []map[string]any
	stream             string
	// declared is true for Bundle.rendered_secrets / RenderedSecrets, false
	// for a provider's secret_ref projection; it only selects the wording.
	declared bool
}

// secretSink is what happens to a validated placement. The deploy's own sink
// applies it; the Flux path's collects them, so it can sync every Secret —
// including the ones in the render — once per cluster.
type secretSink func(ctx context.Context, p secretPlacement) error

// secretTarget is where validated placements go. Plaintext Secrets are
// refused for a remote cluster; there is no sanctioned transport for them.
type secretTarget struct {
	sink secretSink
}

// applySecretsWithKubectl is the direct-deploy sink.
func applySecretsWithKubectl(ctx context.Context, p secretPlacement) error {
	// Never a write to whatever context happens to be current: a placement
	// with no cluster has nowhere declared to land.
	if strings.TrimSpace(p.cluster) == "" {
		return fmt.Errorf("projected Secret(s) for namespace %q have no kubectl context to apply to: "+
			"declare the consuming workload's cluster (a forge.ClusterTarget) in the env's KCL", p.namespace)
	}
	what, wrap := "secret manifest(s)", "apply k8s secrets to %s: %w"
	if p.declared {
		what, wrap = "rendered Secret(s)", "apply rendered secrets to %s: %w"
	}
	// The Namespace object itself lives in the MAIN manifest stream applied
	// AFTER this, so on a fresh cluster it doesn't exist yet. Ensure it
	// first (idempotent; the later full apply re-applies it with labels).
	if err := cluster.EnsureNamespace(ctx, p.cluster, p.namespace); err != nil {
		return fmt.Errorf("ensure namespace %q in %q before secrets: %w", p.namespace, p.cluster, err)
	}
	fmt.Printf("Applying %d %s into %s/%s...\n", len(p.mans), what, p.cluster, p.namespace)
	if err := cluster.KubectlApply(ctx, p.cluster, p.stream); err != nil {
		return fmt.Errorf(wrap, p.cluster, err)
	}
	return nil
}

func applyK8sSecretsFromProviderTo(ctx context.Context, entities *KCLEntities, groups []deploytarget.ServiceGroup, namespace, kubeContext, envName string, dryRun bool, target secretTarget) error {
	// Declared Secrets (Bundle.rendered_secrets, and a RenderedSecrets
	// provider's list) go first and through ONE path, whatever the
	// provider: a dev env whose services read FileSecrets can still
	// declare the Secrets its plain-manifest workloads mount.
	if err := applyDeclaredSecretsTo(ctx, entities, groups, namespace, envName, dryRun, target); err != nil {
		return err
	}
	// A RenderedSecrets provider has nothing further to project: its
	// Secrets ARE its declarations, applied above.
	if entities != nil && entities.SecretProvider != nil && entities.SecretProvider.Type == "rendered" {
		return nil
	}

	prov, err := secretProviderFromEntities(entities, projectDirForKCL())
	if err != nil {
		return fmt.Errorf("secret provider: %w", err)
	}
	noteSecretLayering(prov, os.Stderr)
	// Fail-fast: declared cluster refs must resolve (no-op for
	// external/none).
	dotenvPath := ""
	if entities != nil && entities.SecretProvider != nil {
		dotenvPath = entities.SecretProvider.Path
	}
	if err := secrets.ValidateDeclaredRefs(prov, secretRefsForK8sServices(entities), dotenvPath); err != nil {
		return err
	}
	// Only the value-resolving provider renders Secrets; external/none
	// deliberately resolve nothing.
	if prov.Kind() != "file" {
		return nil
	}

	// GUARD: these providers render PLAINTEXT Secrets — local clusters
	// only. Reject if any k8s-cluster group targets a non-local cluster.
	// This is the Go half of the dev/e2e gate the KCL check declares;
	// both exist so a hand-built entity (no KCL check) still can't ship a
	// developer's local secret into a real cluster.
	for _, g := range groups {
		if g.ProviderID != "k8s-cluster" {
			continue
		}
		if !isLocalCluster(g.Cluster) {
			return fmt.Errorf(
				"secret_provider %q renders plaintext Secrets and is for LOCAL clusters only; target cluster %q is not local. "+
					"Use secret_provider = forge.ExternalSecrets {} (Secrets provisioned out-of-band) for remote clusters",
				prov.Kind(), g.Cluster)
		}
	}

	// One render + apply per destination: every (cluster, namespace) whose
	// workloads declare a ref receives the Secret, carrying the keys THOSE
	// workloads declare. See placeProviderSecretRefs.
	for _, p := range placeProviderSecretRefs(entities, groups, namespace, kubeContext) {
		mans := secrets.RenderK8sSecrets(prov, p.refs, p.namespace)
		if len(mans) == 0 {
			continue
		}
		// Marshal the []map[string]any into the `---`-separated YAML document
		// stream cluster.KubectlApply consumes (identical shape to
		// RenderManifests' output that the Deployment apply uses).
		stream, merr := marshalManifestStream(mans)
		if merr != nil {
			return fmt.Errorf("render k8s secrets: %w", merr)
		}
		if dryRun {
			fmt.Printf("\n--- Generated Secret Manifests for %s/%s (dry-run) ---\n", p.cluster, p.namespace)
			fmt.Println(cluster.RedactSecretValues(stream))
			fmt.Println("--- End Secret Manifests ---")
			continue
		}
		if err := target.sink(ctx, secretPlacement{cluster: p.cluster, namespace: p.namespace, mans: mans, stream: stream}); err != nil {
			return err
		}
	}
	return nil
}

// placedProviderRefs is the set of provider-projected secret refs bound for
// ONE (cluster, namespace): the unit a single render + apply handles.
type placedProviderRefs struct {
	cluster, namespace string
	refs               []secrets.SecretRef
}

// placeProviderSecretRefs decides where a value-resolving provider's
// projected Secrets land: in EVERY k8s group (cluster, namespace) whose
// workloads declare a ref to them, carrying exactly the refs those workloads
// declare.
//
// WHY PER CONSUMER. A multi-cluster env runs workloads that read the same
// Secret in more than one cluster. Applying the projection once, to the env's
// primary context, left every other cluster's consumer stuck on
// CreateContainerConfigError — and the only way around it was to call the
// Secret out-of-band (a forge.ExternalSecret the live preflight then blocks
// on) and create it with a script, whose values then drifted from the ones
// forge projected into the primary cluster.
//
// Same trust boundary as the RenderedSecrets provider's inference
// (placeDeclaredSecrets): a value lands only in a cluster that runs one of
// its consumers, and only the keys that consumer declares.
//
// A cluster workload no group claims keeps the historical destination, the
// env's resolved context and namespace, so a single-cluster env renders and
// applies exactly what it did before. The result is grouped and sorted so the
// apply order — and the dry-run output — is stable.
func placeProviderSecretRefs(entities *KCLEntities, groups []deploytarget.ServiceGroup, fallbackNamespace, fallbackContext string) []placedProviderRefs {
	if entities == nil {
		return nil
	}
	type place struct{ cluster, namespace string }
	at := map[string]place{}
	for _, g := range groups {
		if g.ProviderID != "k8s-cluster" {
			continue
		}
		ns := g.Namespace
		if ns == "" {
			ns = fallbackNamespace
		}
		cl := g.Cluster
		if cl == "" {
			cl = fallbackContext
		}
		for _, s := range g.Services {
			at[s.Name] = place{cl, ns}
		}
	}
	byPlace := map[place][]secrets.SecretRef{}
	for i := range entities.Workloads {
		w := &entities.Workloads[i]
		if !w.OnRuntime(RuntimeCluster) {
			continue
		}
		refs := secretRefsForService(w)
		if len(refs) == 0 {
			continue
		}
		p, ok := at[w.Name]
		if !ok {
			p = place{fallbackContext, fallbackNamespace}
		}
		byPlace[p] = append(byPlace[p], refs...)
	}
	out := make([]placedProviderRefs, 0, len(byPlace))
	for p, refs := range byPlace {
		out = append(out, placedProviderRefs{cluster: p.cluster, namespace: p.namespace, refs: refs})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].cluster != out[j].cluster {
			return out[i].cluster < out[j].cluster
		}
		return out[i].namespace < out[j].namespace
	})
	return out
}

// placedSecrets is the set of declared Secrets bound for ONE
// (cluster, namespace): the unit a single render + apply handles.
type placedSecrets struct {
	cluster, namespace string
	secrets            []secrets.DeclaredSecret
}

// placeDeclaredSecrets decides where every declared Secret lands, from two
// sources that share one rule:
//
//   - Bundle.rendered_secrets: always EXPLICIT — the entry's own
//     cluster/namespace (KCL resolves the cluster_target default). This is
//     the only way to place a Secret whose consumer is a plain manifest,
//     because nothing about a raw Deployment's secretKeyRef tells forge
//     which cluster it runs in.
//   - a RenderedSecrets provider's list: explicit when the entry names a
//     cluster; otherwise INFERRED — once per k8s group whose services
//     reference it by secret_ref, in that group's namespace. Inference is
//     the provider's original trust boundary (a Secret never lands in a
//     cluster none of its consumers run in), kept for entries that rely
//     on it.
//
// An entry with no namespace falls back to fallbackNamespace, the env's
// resolved deploy namespace. The result is grouped and sorted so the apply
// order — and the dry-run output — is stable.
func placeDeclaredSecrets(entities *KCLEntities, groups []deploytarget.ServiceGroup, fallbackNamespace string) []placedSecrets {
	if entities == nil {
		return nil
	}
	byPlace := map[[2]string][]secrets.DeclaredSecret{}
	place := func(cluster, namespace string, s RenderedSecretEntity) {
		if namespace == "" {
			namespace = fallbackNamespace
		}
		k := [2]string{cluster, namespace}
		byPlace[k] = append(byPlace[k], declaredSecret(s))
	}

	for _, s := range entities.RenderedSecrets {
		place(s.Cluster, s.Namespace, s)
	}
	if entities.SecretProvider != nil && entities.SecretProvider.Type == "rendered" {
		for _, s := range entities.SecretProvider.Secrets {
			if s.Cluster != "" {
				place(s.Cluster, s.Namespace, s)
				continue
			}
			for _, g := range groups {
				if g.ProviderID != "k8s-cluster" {
					continue
				}
				if _, ok := referencedSecretNamesForGroup(entities, g)[s.Name]; ok {
					place(g.Cluster, g.Namespace, s)
				}
			}
		}
	}

	out := make([]placedSecrets, 0, len(byPlace))
	for k, list := range byPlace {
		out = append(out, placedSecrets{cluster: k[0], namespace: k[1], secrets: list})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].cluster != out[j].cluster {
			return out[i].cluster < out[j].cluster
		}
		return out[i].namespace < out[j].namespace
	})
	return out
}

// applyDeclaredSecrets renders + applies every declared Secret at the place
// placeDeclaredSecrets assigned it, BEFORE the Deployments roll out, so a
// secretKeyRef resolves on first schedule.
//
// Sourcing: `from="file"` keys resolve from the env's secret store
// (secrets/<env>.yaml, gitignored — the value never enters KCL output);
// `from="literal"` keys are inlined but ONLY in dev/e2e (the Go guard in
// secrets.RenderDeclaredSecrets mirrors the KCL check). Local clusters only:
// these are PLAINTEXT Secrets, so a non-local placement is refused before
// anything is applied anywhere.
func applyDeclaredSecrets(ctx context.Context, entities *KCLEntities, groups []deploytarget.ServiceGroup, namespace, envName string, dryRun bool) error {
	return applyDeclaredSecretsTo(ctx, entities, groups, namespace, envName, dryRun, secretTarget{sink: applySecretsWithKubectl})
}

func applyDeclaredSecretsTo(ctx context.Context, entities *KCLEntities, groups []deploytarget.ServiceGroup, namespace, envName string, dryRun bool, target secretTarget) error {
	placed := placeDeclaredSecrets(entities, groups, namespace)
	if len(placed) == 0 {
		return nil
	}
	for _, p := range placed {
		// GUARD: PLAINTEXT Secrets — local clusters only. Checked for
		// every placement up front, so a bad one refuses the whole set
		// rather than leaving half of it applied.
		if !isLocalCluster(p.cluster) {
			return fmt.Errorf(
				"rendered Secret(s) %s would land in cluster %q, which is not local: forge renders these as plaintext "+
					"and applies them to LOCAL clusters only. Declare the Secret as a forge.ExternalSecret in "+
					"required_secrets (provisioned out-of-band) for a remote cluster",
				declaredSecretNamesList(p.secrets), p.cluster)
		}
	}

	// Value source for `from="file"` keys: the env's secret store.
	store, err := renderedSecretsValueSource(envName, entities)
	if err != nil {
		return fmt.Errorf("rendered secrets value source: %w", err)
	}

	for _, p := range placed {
		mans, rerr := secrets.RenderDeclaredSecrets(p.secrets, store, envName, p.namespace)
		if rerr != nil {
			return rerr
		}
		if len(mans) == 0 {
			continue
		}
		stream, merr := marshalManifestStream(mans)
		if merr != nil {
			return fmt.Errorf("render rendered secrets: %w", merr)
		}
		if dryRun {
			fmt.Printf("\n--- Rendered Secret Manifests for %s/%s (dry-run) ---\n", p.cluster, p.namespace)
			fmt.Println(cluster.RedactSecretValues(stream))
			fmt.Println("--- End Rendered Secret Manifests ---")
			continue
		}
		if err := target.sink(ctx, secretPlacement{cluster: p.cluster, namespace: p.namespace, mans: mans, stream: stream, declared: true}); err != nil {
			return err
		}
	}
	return nil
}

// declaredSecret maps one cli RenderedSecretEntity to the secrets-package
// DeclaredSecret shape (keeping the secrets package decoupled from cli).
func declaredSecret(s RenderedSecretEntity) secrets.DeclaredSecret {
	keys := make(map[string]secrets.DeclaredSecretKey, len(s.Keys))
	for k, src := range s.Keys {
		keys[k] = secrets.DeclaredSecretKey{From: src.From, Key: src.Key, Value: src.Value}
	}
	return secrets.DeclaredSecret{Name: s.Name, Keys: keys}
}

// declaredSecretNamesList renders Secret names for an error message.
func declaredSecretNamesList(list []secrets.DeclaredSecret) string {
	names := make([]string, 0, len(list))
	for _, d := range list {
		names = append(names, fmt.Sprintf("%q", d.Name))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// declaredSecretEntities is every declared Secret the env renders itself —
// Bundle.rendered_secrets plus a RenderedSecrets provider's list — for the
// consumers that ask "what does this env declare?" rather than "where does
// it land?": the preflight supply set and `forge secret list/ensure`.
func declaredSecretEntities(entities *KCLEntities) []RenderedSecretEntity {
	if entities == nil {
		return nil
	}
	out := append([]RenderedSecretEntity(nil), entities.RenderedSecrets...)
	if entities.SecretProvider != nil && entities.SecretProvider.Type == "rendered" {
		out = append(out, entities.SecretProvider.Secrets...)
	}
	return out
}

// referencedSecretNamesForGroup returns the set of Secret names the
// group's workloads reference via their env-var secretRefs. This is the
// scoping key: a declared Secret lands in a cluster ONLY when one of that
// cluster's workloads names it. The group carries workload names; the
// entities carry each workload's spec.env.
func referencedSecretNamesForGroup(entities *KCLEntities, g deploytarget.ServiceGroup) map[string]struct{} {
	inGroup := map[string]struct{}{}
	for _, rs := range g.Services {
		inGroup[rs.Name] = struct{}{}
	}
	names := map[string]struct{}{}
	for i := range entities.Workloads {
		s := &entities.Workloads[i]
		if _, ok := inGroup[s.Name]; !ok {
			continue
		}
		for _, ref := range secretRefsForService(s) {
			if ref.SecretName != "" {
				names[ref.SecretName] = struct{}{}
			}
		}
	}
	return names
}

// marshalManifestStream serialises a list of manifest maps into the
// `---`-separated multi-doc YAML stream `kubectl apply -f -` consumes —
// the same shape cluster.extractManifests produces for the Deployment
// apply, so KubectlApply handles both identically.
func marshalManifestStream(mans []map[string]any) (string, error) {
	var sb strings.Builder
	for i, m := range mans {
		if i > 0 {
			sb.WriteString("---\n")
		}
		b, err := yaml.Marshal(m)
		if err != nil {
			return "", fmt.Errorf("marshal manifest %d: %w", i, err)
		}
		sb.Write(b)
	}
	return sb.String(), nil
}

// isLocalCluster reports whether a cluster name / kubectl context is
// clearly a local dev cluster — the only place plaintext dotenv Secrets
// are safe to project. Recognizes the k3d / kind context prefixes and
// the docker-desktop / minikube / rancher-desktop / colima / orbstack
// local-runtime markers. An empty name is treated as non-local so a
// missing cluster declaration can't silently bypass the guard.
func isLocalCluster(name string) bool {
	if name == "" {
		return false
	}
	n := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(n, "k3d-") || strings.HasPrefix(n, "kind-") {
		return true
	}
	for _, marker := range []string{"docker-desktop", "minikube", "rancher-desktop", "colima", "orbstack"} {
		if strings.Contains(n, marker) {
			return true
		}
	}
	return false
}

// projectNameOf is the forge project name, "" for a nil config.
func projectNameOf(cfg *config.ProjectConfig) string {
	if cfg == nil {
		return ""
	}
	return cfg.Name
}
