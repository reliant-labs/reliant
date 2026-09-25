// Copyright (c) 2025 Reliant Labs

/**
 * Forge deploy data layer.
 *
 * This module owns the shape of forge's deploy document, the classifications a
 * reviewer must not misread, and the derivation of the CONFIRMATION TOKEN that
 * authorises the only write on this whole surface that reaches a live cluster.
 * No React, no styling; presentation lives in components/Forge/Deploy.
 *
 * Promote writes one pointer into a file that git can restore. Deploy applies
 * manifests to Kubernetes, and control-plane's prod env declares
 * gke_reliant-labs-475814_us-central1_prod — recoverable from nowhere. Five
 * things here follow from that, and each one is a way this screen could
 * otherwise authorise something nobody looked at.
 *
 * 1. THE TOKEN CARRIES THE CLUSTER, not just the release. Forge applies to the
 *    context the environment's KCL DECLARES and never to the ambient kubectl
 *    current-context, so the declared context is the only thing that decides
 *    where bytes land — and it comes from a file anyone can edit between a
 *    preview and a confirmation. deployTokenFor is the single producer and it
 *    takes a plan document, so the cluster in the token is by construction the
 *    cluster that was on screen.
 *
 * 2. ALL_KUBE_CONTEXTS, NOT KUBE_CONTEXT. A multi-cluster env is not
 *    hypothetical: control-plane's own dev env declares two
 *    (k3d-control-plane and k3d-cp-daemon), each group applying to its own
 *    declared context. `kube_context` names the env-wide one, which for such an
 *    env is one of several — so a dialog built on that single field would show a
 *    cluster list missing a cluster the deploy is about to write to. That is
 *    precisely the accident a confirmation dialog exists to prevent, so
 *    targetContexts returns the whole set.
 *
 * 3. CURRENT_CONTEXT IS NOT A TARGET. It is in the document because an operator
 *    invariably wants to see it, and forge never reads it. Anything that
 *    presents it as where the deploy lands has inverted the guard.
 *
 * 4. FOUR ROLLOUT STATES, AND TWO OF THEM ARE THE ABSENCE OF AN ANSWER.
 *    timed_out and not_waited are neither success nor failure: a Deployment that
 *    did not become ready inside its budget may be mid-pull on a cold node or
 *    may be crash-looping, and forge does not know which. Forge's own decoder
 *    REFUSES an unrecognised state rather than defaulting it, because a default
 *    of `ready` is the green-deploy-over-a-broken-environment failure that made
 *    its RolloutPolicy exist. rolloutStateOf falls to `unknown` for the same
 *    reason.
 *
 * 5. THE JOB LIFECYCLE IS A SEPARATE QUESTION FROM THE DEPLOY'S VERDICT, and
 *    conflating them is the mistake this module is shaped to prevent. Whether
 *    the invocation reached a determinate outcome is jobDispositionOf; whether
 *    the deploy WORKED is deployCertainty over forge's report. A client must
 *    read both, and `unknown` on either axis means an operator has to go and
 *    look at the environment.
 *
 * 6. A HOSTED DEPLOY HAS NO KUBE CONTEXT, AND THE TOKEN DISCIPLINE STILL HOLDS.
 *    An env whose plan says `target.destination: "hosted"` deploys through a
 *    control plane: bytes land wherever that control plane runs them, so the
 *    thing that decides "where" is the ENDPOINT, and the thing the deploy
 *    writes into is the control plane's ENVIRONMENT. Forge reports the endpoint
 *    as `guard.declared_context` for hosted, so the token keeps its one
 *    required field and its one producer: the operator confirms the endpoint
 *    they saw, and the daemon's stale-context check refuses a deploy whose KCL
 *    now names a different control plane. Nothing here may show kube-context
 *    language for a hosted plan — there is no cluster the operator could go
 *    and check, and naming one would send them to the wrong place.
 *
 * There is deliberately NO representation anywhere in this module of forge's
 * --skip-preflight or --no-digest. Both are overrides for a human at a terminal
 * who has weighed the consequence; a field for either would be set once as a
 * workaround and then stay set forever.
 */

import type { Certainty } from "./topology";
import { destinationOf, endpointHost, type EnvDestination } from "./topology";

// ── Mode ────────────────────────────────────────────────────────────────────

/**
 * What an invocation DID. The answer to "did bytes move?", which must never be
 * inferred from the absence of a field.
 */
export type DeployMode = "unknown" | "explain" | "dry_run" | "apply" | "rollback";

const MODES: readonly DeployMode[] = ["unknown", "explain", "dry_run", "apply", "rollback"];

/**
 * deployModeOf reads the mode, falling back to `unknown`.
 *
 * Never to one of the read-only modes: a newer forge's mode decoded as
 * `dry_run` would tell a reader nothing was applied when something was.
 */
export function deployModeOf(value: string | undefined): DeployMode {
  if (!value) return "unknown";
  const lowered = value.toLowerCase() as DeployMode;
  return MODES.includes(lowered) ? lowered : "unknown";
}

/** True when this mode MUTATES the target. Asked once, here. */
export function deployModeWrites(mode: DeployMode): boolean {
  return mode === "apply" || mode === "rollback";
}

// ── The declared-context guard ──────────────────────────────────────────────

/** Whether forge will deploy at all. */
export type DeployGuardVerdict = "unknown" | "allow" | "refuse";

/**
 * guardVerdictOf reads the verdict, falling back to `unknown`.
 *
 * Never to `allow`. An unevaluated or unrecognised guard must not read as
 * permission — this is the check that makes a wrong-cluster deploy impossible.
 */
export function guardVerdictOf(value: string | undefined): DeployGuardVerdict {
  const lowered = (value ?? "").toLowerCase();
  if (lowered === "allow") return "allow";
  if (lowered === "refuse") return "refuse";
  return "unknown";
}

// ── Image pinning ───────────────────────────────────────────────────────────

/** How ONE image reference is bound to bytes. */
export type DeployPinning = "unknown" | "digest" | "tag";

/**
 * pinningOf reads an image's pinning, falling back to `unknown`.
 *
 * Never to `digest`. Reporting an unexamined reference as digest-pinned claims
 * a safety property forge never verified, and digest pinning is exactly what
 * stops a re-tagged layer shipping in place of the bytes that were built.
 */
export function pinningOf(value: string | undefined): DeployPinning {
  const lowered = (value ?? "").toLowerCase();
  if (lowered === "digest") return "digest";
  if (lowered === "tag") return "tag";
  return "unknown";
}

// ── Preflight ───────────────────────────────────────────────────────────────

/**
 * Whether the deployability preflight ran, and when it did not, why.
 *
 * "No findings" and "never looked" are completely different claims, and a UI
 * that cannot tell them apart presents an unchecked deploy as a clean one. Any
 * `skipped_*` value means NOTHING WAS CHECKED.
 */
export type DeployPreflightStatus =
  | "unknown"
  | "ran"
  | "skipped_flag"
  | "skipped_rollback"
  | "skipped_no_cluster";

const PREFLIGHT_STATUSES: readonly DeployPreflightStatus[] = [
  "unknown",
  "ran",
  "skipped_flag",
  "skipped_rollback",
  "skipped_no_cluster",
];

/** preflightStatusOf reads the status, falling back to `unknown` — never `ran`. */
export function preflightStatusOf(value: string | undefined): DeployPreflightStatus {
  if (!value) return "unknown";
  const lowered = value.toLowerCase() as DeployPreflightStatus;
  return PREFLIGHT_STATUSES.includes(lowered) ? lowered : "unknown";
}

/** True when the preflight actually examined the live target. */
export function preflightRan(status: DeployPreflightStatus): boolean {
  return status === "ran";
}

// ── Rollout ─────────────────────────────────────────────────────────────────

/**
 * What forge was asked to do after the manifests landed. Read this BEFORE the
 * results: under `skip`, every not_waited is expected rather than a problem.
 */
export type DeployRolloutMode = "unknown" | "wait" | "warn" | "skip";

const ROLLOUT_MODES: readonly DeployRolloutMode[] = ["unknown", "wait", "warn", "skip"];

/** rolloutModeOf reads the mode, falling back to `unknown` — never `wait`. */
export function rolloutModeOf(value: string | undefined): DeployRolloutMode {
  if (!value) return "unknown";
  const lowered = value.toLowerCase() as DeployRolloutMode;
  return ROLLOUT_MODES.includes(lowered) ? lowered : "unknown";
}

/**
 * ONE resource's readiness outcome. Four values, and only two of them are
 * answers.
 */
export type DeployRolloutState = "unknown" | "ready" | "failed" | "timed_out" | "not_waited";

const ROLLOUT_STATES: readonly DeployRolloutState[] = [
  "unknown",
  "ready",
  "failed",
  "timed_out",
  "not_waited",
];

/**
 * rolloutStateOf reads a resource's state, falling back to `unknown`.
 *
 * THE FALLBACK DIRECTION IS THE POINT. Forge's own decoder refuses a state it
 * does not recognise; this one cannot refuse a render, so it lands on `unknown`
 * — never on `ready`, which would paint a stuck rollout green.
 */
export function rolloutStateOf(value: string | undefined): DeployRolloutState {
  if (!value) return "unknown";
  const lowered = value.toLowerCase() as DeployRolloutState;
  return ROLLOUT_STATES.includes(lowered) ? lowered : "unknown";
}

/**
 * rolloutCertainty places a rollout state in the three-level vocabulary the
 * rest of this feature runs on (see components/Forge/stateVocabulary.ts).
 *
 * ready and failed are CERTAIN and differ by hue. timed_out, not_waited and
 * unknown are the same third thing: nothing was established. They keep separate
 * states — and separate icons and copy — because an operator's next move
 * differs, but none of them may borrow either certain treatment.
 */
export function rolloutCertainty(state: DeployRolloutState): Certainty {
  switch (state) {
    case "ready":
      return "known-good";
    case "failed":
      return "known-bad";
    default:
      return "unknown";
  }
}

// ── The job lifecycle ───────────────────────────────────────────────────────

/**
 * Whether the detached invocation reached a determinate outcome. NOT the
 * deploy's verdict — see deployCertainty for that.
 *
 * `unknown` covers a killed job, a job whose process died, a job that produced
 * no parseable report, and a handle the daemon no longer recognises. The
 * daemon's registry is in memory, so a daemon that restarted mid-apply has
 * genuinely lost the outcome of a deploy that may well have landed. MANIFESTS
 * MAY OR MAY NOT HAVE REACHED THE CLUSTER, and that is the only honest thing to
 * say about it.
 *
 * `failed` is reserved for the one case where nothing can have shipped: the
 * invocation never got off the ground, e.g. a pinned forge too old for the
 * command.
 */
export type DeployJobDisposition = "running" | "completed" | "failed" | "unknown";

/** True when the job's outcome is genuinely not established either way. */
export function jobIsIndeterminate(disposition: DeployJobDisposition): boolean {
  return disposition === "unknown";
}

// ── The plan / report document ──────────────────────────────────────────────
//
// Typed against forge's deploy `--json` contract (internal/cli/deploy_json.go),
// which is explicitly ADDITIVE: fields are added, never renamed or repurposed.
// Every optional field is optional here and nothing is assumed non-null — a
// document from a newer or older forge must render, not throw.

export interface ForgeDeployGuard {
  /**
   * THE CLUSTER THIS DEPLOYS TO. Empty means the env declares no cluster, which
   * is a legitimate shape (host-only / compose) and not an error.
   */
  declared_context?: string;
  /**
   * The ambient kubectl current-context. INFORMATIONAL ONLY — forge never reads
   * it and never falls back to it. Never present this as the target.
   */
  current_context?: string;
  verdict?: string;
  reason?: string;
  /** On a refusal, forge's own statement of what would make the deploy possible. */
  fix?: string;
  /** The kubeconfig's context list, when it is the evidence for a refusal. */
  available_contexts?: string[];
}

export interface ForgeDeployTarget {
  /**
   * Where this deploy lands — the topology vocabulary (hosted | cluster | …).
   * Absent from a forge that predates hosted deploys, whose plans are
   * cluster-shaped by evidence (they carry kube contexts).
   */
  destination?: string;
  /** Hosted only: the control plane's normalized base URL. */
  endpoint?: string;
  /** Hosted only: the control plane's id for this env; empty until the first deploy ensures it. */
  environment_id?: string;
  /** The env-wide declared context. For a multi-cluster env this is ONE of several. */
  kube_context?: string;
  namespace?: string;
  /** EVERY declared cluster this invocation addresses. Prefer this — see targetContexts. */
  all_kube_contexts?: string[];
}

export interface ForgeDeployImage {
  reference?: string;
  repository?: string;
  pinning?: string;
}

export interface ForgeDeployImages {
  images?: ForgeDeployImage[];
  digest_count?: number;
  tag_count?: number;
  /** Whether --no-digest was passed. There is no UI that can set this. */
  no_digest_requested?: boolean;
}

export interface ForgeDeployFinding {
  /** The gate that fired, e.g. "missing_secret_key". A plain string: the set grows. */
  check?: string;
  subject?: string;
  keys?: string[];
  detail?: string;
  /**
   * Whether this finding REFUSES the deploy. Read this, never the check name —
   * an unrecognised check still reports its consequence correctly.
   */
  blocking?: boolean;
}

export interface ForgeDeployPreflight {
  status?: string;
  findings?: ForgeDeployFinding[];
  blocking?: number;
}

export interface ForgeDeployResource {
  api_version?: string;
  kind?: string;
  name?: string;
}

export interface ForgeDeployRolloutResult {
  kind?: string;
  name?: string;
  state?: string;
  detail?: string;
}

export interface ForgeDeployRollout {
  mode?: string;
  /** The PER-RESOURCE readiness budget, not the budget for the set. */
  timeout_seconds?: number;
  results?: ForgeDeployRolloutResult[];
  ready?: number;
  failed?: number;
  timed_out?: number;
  not_waited?: number;
}

export interface ForgeDeployReport {
  env?: string;
  /** Read this FIRST: the authoritative answer to whether anything was written. */
  mode?: string;
  guard?: ForgeDeployGuard;
  target?: ForgeDeployTarget;
  image_tag?: string;
  tag_source?: string;
  /** The bound release whose digests get pinned. Empty when the env has no binding. */
  release?: string;
  preflight?: ForgeDeployPreflight;
  images?: ForgeDeployImages;
  resources?: ForgeDeployResource[];
  rollout?: ForgeDeployRollout;
  targets?: string[];
  skip_frontend?: boolean;
  frontends_only?: boolean;
  prune?: boolean;
  duration_ms?: number;
  /** Whether THIS INVOCATION succeeded. Not the same question as guard.verdict. */
  ok?: boolean;
  exit_code?: number;
  error?: string;
}

// ── Where this lands ────────────────────────────────────────────────────────

/** The plan's destination. Unknown/absent is `unknown` — see isHostedPlan for what that implies here. */
export function planDestination(report: ForgeDeployReport | null | undefined): EnvDestination {
  return destinationOf(report?.target);
}

/**
 * True only when forge SAID this plan is hosted. Never inferred from an
 * absent kube context: a cluster env with no declared context is a blocker,
 * not a hosted deploy.
 */
export function isHostedPlan(report: ForgeDeployReport | null | undefined): boolean {
  return planDestination(report) === "hosted";
}

/** The control plane a hosted plan deploys through: target.endpoint, else the guard's declared endpoint. */
export function hostedEndpoint(report: ForgeDeployReport | null | undefined): string {
  const endpoint = (report?.target?.endpoint ?? "").trim();
  if (endpoint !== "") return endpoint;
  return (report?.guard?.declared_context ?? "").trim();
}

/**
 * targetContexts returns EVERY declared cluster this deploy addresses, sorted
 * and de-duplicated.
 *
 * `all_kube_contexts` is preferred and `kube_context` is folded in rather than
 * used as a fallback only, because the two arrive by different routes in forge
 * and an env-wide context that failed to make it into the set is still a cluster
 * this deploy writes to. Under-reporting the blast radius is the one error this
 * function must not make.
 *
 * Returns an empty array for an env that declares no cluster. That is a real
 * shape, and it is also the shape from which no deploy can be authorised — see
 * deployTokenFor.
 */
export function targetContexts(report: ForgeDeployReport | null | undefined): string[] {
  // A hosted plan addresses no kube context; its guard's declared_context is
  // an endpoint, and listing it here would render it as a cluster.
  if (isHostedPlan(report)) return [];
  const seen = new Set<string>();
  const add = (value: string | undefined) => {
    const trimmed = (value ?? "").trim();
    if (trimmed !== "") seen.add(trimmed);
  };

  for (const context of report?.target?.all_kube_contexts ?? []) add(context);
  add(report?.target?.kube_context);
  // The guard resolved the declared context first, and a refusal still reports
  // WHICH cluster it refused to touch.
  add(report?.guard?.declared_context);

  return [...seen].sort();
}

/** True when this deploy writes to more than one cluster. */
export function isMultiCluster(report: ForgeDeployReport | null | undefined): boolean {
  return targetContexts(report).length > 1;
}

// ── Blockers ────────────────────────────────────────────────────────────────

/**
 * A reason this deploy must not be offered for confirmation.
 *
 * These are not warnings to render beside an enabled button. Each one means the
 * deploy either cannot run or will fail, and the confirm step is ABSENT while
 * any of them holds — the same structural guard promote uses, for a write whose
 * consequences are considerably worse.
 */
export type DeployBlocker =
  /** Forge's own declared-cluster guard says no. It will not deploy. */
  | { kind: "guard-refused"; detail: string; fix: string }
  /**
   * The preflight found something that REFUSES the deploy: a referenced Secret
   * key or container image that does not exist on the live target. Applying
   * anyway fails, so offering to proceed would be offering a known failure.
   */
  | { kind: "preflight-blocking"; findings: ForgeDeployFinding[] }
  /**
   * The environment declares no cluster, so there is no declared context to
   * authorise against — and the token has no "unset" spelling by design.
   */
  | { kind: "no-declared-cluster" }
  /**
   * Hosted, but the plan names no control-plane endpoint to authorise
   * against. The hosted twin of no-declared-cluster.
   */
  | { kind: "no-declared-endpoint" }
  /** The document is not a read-only preview, so it cannot authorise anything. */
  | { kind: "not-a-preview"; mode: DeployMode };

/**
 * blockingFindings returns the preflight findings that refuse the deploy.
 *
 * `blocking` is read per finding rather than taken from the section's count, so
 * the findings that are SHOWN are exactly the ones that justify the stop.
 */
export function blockingFindings(report: ForgeDeployReport | null | undefined): ForgeDeployFinding[] {
  return (report?.preflight?.findings ?? []).filter((finding) => finding.blocking === true);
}

/**
 * deployBlockers lists every reason this plan cannot be confirmed.
 *
 * All of them, not the first: an operator fixing one wants to know about the
 * other, and a one-at-a-time reveal turns a single conversation into three.
 */
export function deployBlockers(report: ForgeDeployReport | null | undefined): DeployBlocker[] {
  const blockers: DeployBlocker[] = [];
  if (!report) return [{ kind: "not-a-preview", mode: "unknown" }];

  const mode = deployModeOf(report.mode);
  if (mode !== "dry_run") blockers.push({ kind: "not-a-preview", mode });

  if (guardVerdictOf(report.guard?.verdict) === "refuse") {
    blockers.push({
      kind: "guard-refused",
      detail: report.guard?.reason ?? "",
      fix: report.guard?.fix ?? "",
    });
  }

  if (isHostedPlan(report)) {
    if ((report.guard?.declared_context ?? "").trim() === "") blockers.push({ kind: "no-declared-endpoint" });
  } else if (targetContexts(report).length === 0) {
    blockers.push({ kind: "no-declared-cluster" });
  }

  const findings = blockingFindings(report);
  if (findings.length > 0) blockers.push({ kind: "preflight-blocking", findings });

  return blockers;
}

// ── The confirmation token ──────────────────────────────────────────────────

/**
 * The claim about current state that authorises writing to a cluster.
 *
 * expectedDeclaredContext is REQUIRED by the type, with no spelling that means
 * "I did not populate it" — mirroring the server, which rejects an empty one as
 * InvalidArgument. There is no legitimate deploy whose target cluster the
 * operator did not see.
 *
 * The release half is the same three-way shape promote uses, and exactly one
 * side is representable: "I saw v1.3.0" or "I saw no binding". The third
 * possibility — neither — cannot be expressed, which is what stops an
 * unpopulated request authorising whatever the environment happens to be bound
 * to now.
 */
export type DeployConfirmationToken = {
  /**
   * THE TARGET THE OPERATOR SAW NAMED. From the plan's guard, never a default.
   * A kube context for a cluster deploy; the control-plane endpoint for a
   * hosted one (forge reports it in the same field, and the daemon re-checks
   * it the same way).
   */
  expectedDeclaredContext: string;
  /**
   * Present only for a hosted plan: what the confirmation SAYS, never sent.
   * The daemon's request has no field for either and needs none — the
   * endpoint in expectedDeclaredContext is the re-checked half.
   */
  hosted?: { environmentId: string };
} & (
  | { expectedCurrentRelease: string; expectUnbound?: false }
  | { expectUnbound: true; expectedCurrentRelease?: undefined }
);

/**
 * deployTokenFor derives the token FROM THE PLAN THE USER SAW.
 *
 * THIS FUNCTION IS THE SAFETY PROPERTY, and its single argument is the reason.
 * The token must describe the cluster and release the reviewer actually read off
 * the screen, so it is computed from the plan document itself rather than
 * assembled from component state, props threaded through a dialog, or a
 * remembered env name. It is the ONLY producer of a token in this feature: the
 * start path takes a plan and calls this, so "the token matches what was
 * rendered" holds by construction rather than by discipline.
 *
 * Returns null whenever the plan cannot support a claim, and every branch that
 * does so is a case where both available guesses are dangerous:
 *
 *   not a dry_run preview — nothing was previewed, so nothing was reviewed.
 *   guard refused         — forge will not deploy; a token would be a request
 *                           to try anyway.
 *   no declared context   — there is nothing to name, and the server rightly
 *                           has no way to say "unspecified".
 *
 * A null here must make the confirm step ABSENT, not disabled.
 *
 * Note what this deliberately does NOT gate on: blocking preflight findings.
 * Those make the deploy a known failure and are handled as blockers, which the
 * component layer checks alongside this. Folding them in here would conflate
 * "this claim cannot be made" with "this claim is fine but the deploy will
 * fail", and the copy an operator needs is different for each.
 */
export function deployTokenFor(
  report: ForgeDeployReport | null | undefined
): DeployConfirmationToken | null {
  if (!report) return null;

  // Only a read-only preview can authorise a write. An apply report describes a
  // deploy that already happened.
  if (deployModeOf(report.mode) !== "dry_run") return null;

  // Forge's own guard said no. There is no token that overrides it.
  if (guardVerdictOf(report.guard?.verdict) === "refuse") return null;

  const declaredContext = (report.guard?.declared_context ?? "").trim();
  if (declaredContext === "") return null;

  const hosted = isHostedPlan(report)
    ? { hosted: { environmentId: (report.target?.environment_id ?? "").trim() } }
    : {};

  // An empty release is forge's own spelling of "this environment has no
  // binding" (the field is omitted when unbound), and it is the case the
  // expect_unbound half of the token exists for.
  const release = (report.release ?? "").trim();
  if (release === "") return { expectedDeclaredContext: declaredContext, expectUnbound: true, ...hosted };

  return { expectedDeclaredContext: declaredContext, expectedCurrentRelease: release, ...hosted };
}

/**
 * describeDeployToken renders the claim in the words the confirm step shows.
 *
 * The user is told what they are asserting, not just what they are doing — that
 * assertion is what the server re-checks, and a refusal only makes sense to
 * someone who was shown the claim it refers to. The cluster comes first because
 * it is the consequential half.
 */
export function describeDeployToken(token: DeployConfirmationToken): string {
  const release = token.expectUnbound
    ? "this environment has no release binding"
    : `this environment is bound to ${token.expectedCurrentRelease}`;
  if (token.hosted) {
    // The verb says whether this touches something live: an ensured env is
    // UPDATED (its id is named, so the claim pins which one), an un-ensured one
    // is CREATED.
    const action = token.hosted.environmentId
      ? `this updates the live environment ${token.hosted.environmentId}`
      : "this creates a new environment";
    return `${action} on the control plane at ${token.expectedDeclaredContext}, and ${release}`;
  }
  return `this deploys to ${token.expectedDeclaredContext}, and ${release}`;
}

/**
 * confirmPhrase is what the operator must TYPE to enable the deploy: the
 * cluster name, or — hosted — the control plane's host. The host rather than
 * the full URL because it is the part a human reads and recognises; the full
 * endpoint still travels in the token and is what the daemon re-checks.
 */
export function confirmPhrase(token: DeployConfirmationToken): string {
  return token.hosted ? endpointHost(token.expectedDeclaredContext) : token.expectedDeclaredContext;
}

// ── Refusal ─────────────────────────────────────────────────────────────────

/**
 * Why a deploy was not STARTED. In every case NOTHING WAS APPLIED.
 *
 * Four reasons, kept distinct because each calls for a different action:
 * re-plan against the cluster that is declared now, re-plan against the release
 * that is bound now, fix the kubeconfig, or watch the deploy that is already
 * running instead of racing it.
 */
export type DeployRefusalReason =
  | "stale-declared-context"
  | "stale-current-release"
  | "guard-refused"
  | "already-running"
  | "unknown";

/**
 * The state the guard found, when it refused.
 *
 * A plain shape rather than the generated proto message, because this travels as
 * a connect error DETAIL and the UI only ever reads it. The field names are in
 * the UI's own idiom so no component has to import the generated enum to ask the
 * one question it cares about.
 */
export interface DeployRefusal {
  /** Branch on this, never on `detail`. */
  reason: DeployRefusalReason;
  /** One-sentence human phrasing from the server. */
  detail: string;
  /** The cluster claim, and what the KCL declares NOW. */
  expectedDeclaredContext: string;
  actualDeclaredContext: string;
  /** The release claim, and the binding actually found. */
  expectedCurrentRelease: string;
  expectedUnbound: boolean;
  actualCurrentRelease: string;
  actualBound: boolean;
  /** Forge's own guard decision, when IT is the refusal. */
  guardVerdict: string;
  guardReason: string;
  /** Forge's statement of what would make the deploy possible. */
  guardFix: string;
  /** The in-flight deploy's handle, on an already-running refusal. */
  runningHandle: string;
}

/**
 * describeActualBinding renders what the guard found, for the refusal notice.
 *
 * `actualBound === false` gets its own sentence rather than an empty release,
 * because "no binding" and "bound to something blank" are different findings.
 */
export function describeActualBinding(refusal: DeployRefusal): string {
  if (!refusal.actualBound) return "no release binding at all";
  if (!refusal.actualCurrentRelease) return "a binding with no release recorded";
  return refusal.actualCurrentRelease;
}

// ── The deploy's verdict ────────────────────────────────────────────────────

/** Rollout tallies, counted from the results actually rendered. */
export interface RolloutTally {
  ready: number;
  failed: number;
  timedOut: number;
  notWaited: number;
  unknown: number;
  total: number;
}

/**
 * rolloutTally counts the per-resource states.
 *
 * Counted from `results` when there are any, because those are the entries the
 * screen renders and a tally that disagreed with the list would be a bug nobody
 * could see. The document's own flat counts are the fallback for a report that
 * carried tallies but no list.
 */
export function rolloutTally(report: ForgeDeployReport | null | undefined): RolloutTally {
  const results = report?.rollout?.results ?? [];
  if (results.length === 0) {
    const ready = report?.rollout?.ready ?? 0;
    const failed = report?.rollout?.failed ?? 0;
    const timedOut = report?.rollout?.timed_out ?? 0;
    const notWaited = report?.rollout?.not_waited ?? 0;
    return {
      ready,
      failed,
      timedOut,
      notWaited,
      unknown: 0,
      total: ready + failed + timedOut + notWaited,
    };
  }

  const tally: RolloutTally = {
    ready: 0,
    failed: 0,
    timedOut: 0,
    notWaited: 0,
    unknown: 0,
    total: results.length,
  };
  for (const result of results) {
    switch (rolloutStateOf(result.state)) {
      case "ready":
        tally.ready += 1;
        break;
      case "failed":
        tally.failed += 1;
        break;
      case "timed_out":
        tally.timedOut += 1;
        break;
      case "not_waited":
        tally.notWaited += 1;
        break;
      default:
        tally.unknown += 1;
    }
  }
  return tally;
}

/**
 * deployCertainty is what a FINISHED forge report establishes about the
 * environment, in the three-level vocabulary this feature runs on.
 *
 * The order is the argument:
 *
 *   known-bad   forge said it failed, or a resource genuinely failed. A real
 *               answer, and it is checked first — a failure alongside an unknown
 *               is still a failure.
 *   unknown     anything was not established: a resource timed out, a resource
 *               was never waited on (every resource, under rollout mode skip),
 *               a state this build does not recognise, or a report with no
 *               resources to speak for. NOT success.
 *   known-good  every resource forge waited on became ready, and forge exited 0.
 *
 * A `not_waited` under skip is legitimate rather than alarming — forge was asked
 * not to look — but it is still not evidence the environment converged, so it
 * lands in `unknown` with copy that says which of the two it is.
 */
export function deployCertainty(report: ForgeDeployReport | null | undefined): Certainty {
  if (!report) return "unknown";

  const tally = rolloutTally(report);
  if (report.ok === false || tally.failed > 0) return "known-bad";
  if (tally.timedOut > 0 || tally.notWaited > 0 || tally.unknown > 0) return "unknown";

  // Nothing failed and nothing is outstanding. A report that waited on nothing
  // at all has still established nothing about the environment.
  if (tally.ready === 0) return "unknown";
  return report.ok === true ? "known-good" : "unknown";
}

/**
 * describeDeployCertainty is the sentence that goes with the verdict.
 *
 * The `unknown` text is the load-bearing one: it has to stop a reader treating
 * an unconverged environment as a clean deploy, and it has to say what to do —
 * go and look — because nothing in the report can answer it.
 */
export function describeDeployCertainty(
  certainty: Certainty,
  report: ForgeDeployReport | null | undefined
): string {
  const tally = rolloutTally(report);
  const mode = rolloutModeOf(report?.rollout?.mode);

  switch (certainty) {
    case "known-good":
      return `Forge waited for all ${tally.ready} resource${tally.ready === 1 ? "" : "s"} and each one became ready.`;
    case "known-bad":
      return (
        report?.error ||
        `Forge reported this deploy as failed${tally.failed > 0 ? `, with ${tally.failed} resource${tally.failed === 1 ? "" : "s"} failing` : ""}.`
      );
    default:
      if (mode === "skip") {
        return (
          "Manifests were applied and forge was asked not to wait, so nothing was observed converging. " +
          "This is neither healthy nor broken — verify the environment to find out what is running."
        );
      }
      if (tally.timedOut > 0) {
        return (
          `${tally.timedOut} resource${tally.timedOut === 1 ? "" : "s"} did not report ready inside the budget. ` +
          "That is not a failure and not a success: they may still be converging, or may be stuck. Verify the environment."
        );
      }
      return (
        "This deploy's outcome was not established. It is neither healthy nor broken — " +
        "verify the environment to find out what is running."
      );
  }
}
