// Copyright (c) 2025 Reliant Labs

/**
 * THE APPROVABLE PLAN — what a deploy would actually ship, and the digest that
 * binds an approval to it.
 *
 * WHY THIS FILE IS SEPARATE FROM deploy.ts, WHICH ALSO DESCRIBES A "PLAN".
 * They are two different documents and conflating them is the bug this whole
 * change exists to fix:
 *
 *   deploy.ts's ForgeDeployReport  is the INSTANT preview (`--dry-run`). It
 *     renders the environment and checks the guard, builds nothing, and
 *     describes the binding the environment is running RIGHT NOW. Its images
 *     and digests are the CURRENT ones.
 *   this file's DeployPlan         is the APPROVABLE plan (`--plan-only`). It
 *     is the product of a real build: images pushed, a release cut, and the
 *     change set computed against what is live. Its digest is what a human
 *     approves.
 *
 * A versionless deploy builds new images and cuts a new release, so the
 * preview's digests are NOT what a click would ship. Putting them next to a
 * deploy button is the misleading thing that was removed. Only this document
 * can answer "what does this deploy ship", so only this one may be shown
 * beside an approval.
 *
 * NOTHING HERE RE-DERIVES A VERDICT. The classes, codes and sections are
 * forge's, decoded as the closed sets forge declares them to be, and an
 * unrecognised value is treated as the MORE severe reading rather than
 * discarded — see findingClassOf.
 */

/**
 * How much a finding gates a deploy. Forge's closed set.
 *
 * - `info` — a change worth seeing. A general approval covers it.
 * - `warn` — read before approving: a missing secret, drift this deploy will
 *   overwrite, a section that could not be computed. Approval covers it.
 * - `stop` — a change a general approval must NOT cover. It needs its own
 *   acknowledgement, by code, every time.
 */
export type FindingClass = "info" | "warn" | "stop";

/** Least to most severe. The order findings are grouped in. */
export const FINDING_CLASSES: readonly FindingClass[] = ["info", "warn", "stop"];

/**
 * findingClassOf decodes a class, and AN UNRECOGNISED ONE BECOMES `stop`.
 *
 * That direction is the entire safety property. A newer forge adding a class
 * more severe than anything here would, under a permissive default, have its
 * findings silently covered by an ordinary approval — which is exactly the
 * "approved something nobody classified" failure this surface is built to
 * prevent. Treating the unknown as stop-class means it gets an explicit
 * acknowledgement instead: noisy at worst, never unsafe.
 */
export function findingClassOf(value: string | undefined): FindingClass {
  switch (value) {
    case "info":
      return "info";
    case "warn":
      return "warn";
    case "stop":
      return "stop";
    default:
      return "stop";
  }
}

/** One thing this deploy changes. */
export interface DeployPlanFinding {
  /**
   * The finding's code, e.g. "stateful_deletion". For a stop-class finding
   * this is what an acknowledgement NAMES, which is why it cannot be
   * pre-approved: the code is not knowable until the plan is computed.
   */
  code?: string;
  class?: string;
  /** Which part of the plan this belongs to: objects, images, config, … */
  section?: string;
  /** What it is about — a workload, an object, a secret key. */
  subject?: string;
  detail?: string;
}

/** What the plan was computed against: the state that is live now. */
export interface DeployPlanBasis {
  current_promotion_id?: string;
  applied_bundle_id?: string;
  applied_config_digest?: string;
  /**
   * The live state does not match what was last deployed. A deploy will
   * overwrite whatever caused that.
   */
  drift_observed?: boolean;
}

/**
 * The §8.6 plan: one reviewed deploy.
 *
 * `digest` is the load-bearing field. It is computed over the plan's substance
 * — environment, bundle, release, live basis, and every finding's code, class,
 * section and subject — so a plan that differs in any of those is a different
 * digest. Approving a digest therefore approves a specific change set, and
 * forge refuses the deploy if what it recomputes is not that one.
 */
export interface DeployPlan {
  digest?: string;
  environment_id?: string;
  bundle_id?: string;
  release_version?: string;
  live_basis?: DeployPlanBasis;
  findings?: DeployPlanFinding[];
  /** The rendered configuration is byte-identical to what is deployed. */
  config_identical?: boolean;
  computed_at?: string;
}

/**
 * The document `--plan-only` emits: the plan, plus what the stage produced.
 *
 * `target.release` is the AUTO version this stage cut — the release a deploy
 * must name to ship these artifacts rather than building a second set.
 * `applied` is false and `confirmed` is false, always: this stage writes no
 * promotion, which is what makes it safe to run before anyone has approved
 * anything.
 */
export interface DeployPlanReport {
  env?: string;
  ok?: boolean;
  exit_code?: number;
  confirmed?: boolean;
  applied?: boolean;
  target?: { release?: string };
  /** Forge's own statement of the exact command that would approve this plan. */
  next_step?: string;
  deploy_plan?: DeployPlan;
}

// ── Reading a plan ──────────────────────────────────────────────────────────

/** The plan inside a plan-only document, or null when there is none. */
export function planOf(report: DeployPlanReport | null | undefined): DeployPlan | null {
  return report?.deploy_plan ?? null;
}

/**
 * The digest a deploy would approve, or null.
 *
 * NULL IS NOT "NO CHANGES" AND MUST NEVER BE TREATED AS APPROVABLE. It means
 * no plan could be computed — a never-built environment, or a control plane
 * that cannot produce one — so there is nothing to approve and no claim to
 * make. A deploy offered against a null digest would be an unapproved deploy
 * wearing an approval's clothes.
 */
export function approvableDigest(report: DeployPlanReport | null | undefined): string | null {
  const digest = planOf(report)?.digest?.trim();
  return digest ? digest : null;
}

/**
 * The release this plan was computed for — the version a deploy must name.
 *
 * Read from `target.release` rather than the plan's own `release_version`
 * because `target.release` is what forge says this stage CUT, and that is the
 * thing a deploy has to name positionally. Falls back to the plan's copy when
 * the target is absent; null when neither says.
 */
export function approvableRelease(report: DeployPlanReport | null | undefined): string | null {
  const target = report?.target?.release?.trim();
  if (target) return target;
  const fromPlan = planOf(report)?.release_version?.trim();
  return fromPlan ? fromPlan : null;
}

/** Findings of one class, in document order. */
export function findingsOfClass(
  plan: DeployPlan | null | undefined,
  wanted: FindingClass
): DeployPlanFinding[] {
  return (plan?.findings ?? []).filter((finding) => findingClassOf(finding.class) === wanted);
}

/**
 * The stop-class findings, which need acknowledgement by code.
 *
 * These describe a deploy that SUCCEEDS and is still unrecoverable — storage
 * deleted, an address reissued. No re-deploy undoes them, which is why a
 * general approval is not allowed to cover them.
 */
export function stopFindings(plan: DeployPlan | null | undefined): DeployPlanFinding[] {
  return findingsOfClass(plan, "stop");
}

/**
 * The distinct codes that must be acknowledged before this plan can deploy.
 *
 * BY CODE, DEDUPLICATED, because forge's flag takes codes and two findings
 * sharing a code are one decision. A finding with no code is skipped here and
 * surfaced by unacknowledgeableStopFindings instead — it cannot be named, so
 * it cannot be acknowledged, and silently dropping it would let a plan deploy
 * with an unaddressed stop finding.
 */
export function requiredAcknowledgements(plan: DeployPlan | null | undefined): string[] {
  const codes = new Set<string>();
  for (const finding of stopFindings(plan)) {
    const code = finding.code?.trim();
    if (code) codes.add(code);
  }
  return [...codes].sort();
}

/**
 * Stop-class findings carrying NO code.
 *
 * Forge should never emit one, and if it does the plan is unapprovable rather
 * than approvable-by-default: an acknowledgement names a code, so a nameless
 * stop finding has no way to be accepted. Surfaced so the UI can say that
 * plainly instead of offering a button that forge will refuse.
 */
export function unacknowledgeableStopFindings(
  plan: DeployPlan | null | undefined
): DeployPlanFinding[] {
  return stopFindings(plan).filter((finding) => !finding.code?.trim());
}

/**
 * Whether this plan may be deployed, given what the operator has ticked.
 *
 * Three conditions, and all are necessary:
 *   - there is a digest to approve, and a release to ship it as;
 *   - every stop-class code is acknowledged;
 *   - no stop-class finding is unacknowledgeable.
 */
export function planIsApprovable(
  report: DeployPlanReport | null | undefined,
  acknowledged: ReadonlySet<string>
): boolean {
  if (!approvableDigest(report) || !approvableRelease(report)) return false;
  const plan = planOf(report);
  if (unacknowledgeableStopFindings(plan).length > 0) return false;
  return requiredAcknowledgements(plan).every((code) => acknowledged.has(code));
}

/**
 * What a deploy needs in order to be bound to the plan that was read.
 *
 * It lives HERE, beside the plan it is derived from, rather than next to the
 * button that produces it: the API layer needs this type, and a UI component is
 * the wrong thing for it to depend on.
 *
 * Every field comes from the rendered plan document — see approvalFor, the only
 * producer. There is deliberately no spelling of this type that means "approve
 * whatever is current".
 */
export interface PlanApproval {
  /** The digest of the plan the operator read. */
  approveDigest: string;
  /** The release that plan was computed for, deployed by name. */
  releaseVersion: string;
  /** The irreversible changes accepted, by code. */
  acknowledgedFindings: string[];
}

/**
 * approvalFor derives the approval FROM THE PLAN THAT WAS RENDERED.
 *
 * THIS FUNCTION IS THE SAFETY PROPERTY, and its single plan argument is the
 * reason. The approval must describe the change set the reviewer actually read,
 * so it is computed from the plan document itself rather than assembled from
 * component state or threaded through props. It is the only producer, so "the
 * digest sent is the digest shown" holds by construction.
 *
 * Returns null when the plan cannot support an approval — no digest, no
 * release, or an irreversible change that cannot be named. A null must make the
 * deploy control ABSENT, not disabled.
 */
export function approvalFor(
  report: DeployPlanReport | null | undefined,
  acknowledged: ReadonlySet<string>
): PlanApproval | null {
  if (!planIsApprovable(report, acknowledged)) return null;
  const digest = approvableDigest(report);
  const release = approvableRelease(report);
  if (!digest || !release) return null;
  return {
    approveDigest: digest,
    releaseVersion: release,
    acknowledgedFindings: requiredAcknowledgements(planOf(report)),
  };
}

// ── What changed, when a plan goes stale ────────────────────────────────────

/**
 * The difference between the plan that was approved and the one forge
 * recomputed.
 *
 * This exists because "the plan changed, approve again" is not an actionable
 * sentence. Live moved under the operator — someone else deployed, a new
 * bundle landed, drift appeared — and the only safe next step is to show WHAT
 * moved and ask again. Re-sending the same approval, or re-approving without
 * reading, is the accident approving by digest exists to prevent.
 */
export interface PlanChange {
  /** Findings in the new plan that were not in the approved one. */
  added: DeployPlanFinding[];
  /** Findings that were in the approved plan and are now gone. */
  removed: DeployPlanFinding[];
  /** The release changed between the two plans. */
  releaseChanged?: { from: string; to: string };
  /** The bundle changed between the two plans. */
  bundleChanged?: { from: string; to: string };
  /** Drift appeared or cleared. */
  driftChanged?: { from: boolean; to: boolean };
  /** New stop-class codes the previous approval did not cover. */
  newStopCodes: string[];
}

/** A finding's identity for comparison: the fields the digest itself covers. */
function findingKey(finding: DeployPlanFinding): string {
  return [
    finding.code ?? "",
    findingClassOf(finding.class),
    finding.section ?? "",
    finding.subject ?? "",
  ].join("\u0000");
}

/**
 * diffPlans reports what changed from the approved plan to the recomputed one.
 *
 * Compared on the fields the DIGEST covers — code, class, section, subject —
 * and deliberately not on `detail`, which is prose derived from those fields.
 * A detail-only difference cannot change the digest, so reporting it as a
 * change would show the operator a diff that does not explain why their
 * approval was refused.
 */
export function diffPlans(
  approved: DeployPlan | null | undefined,
  current: DeployPlan | null | undefined
): PlanChange {
  const approvedFindings = approved?.findings ?? [];
  const currentFindings = current?.findings ?? [];

  const approvedKeys = new Set(approvedFindings.map(findingKey));
  const currentKeys = new Set(currentFindings.map(findingKey));

  const added = currentFindings.filter((finding) => !approvedKeys.has(findingKey(finding)));
  const removed = approvedFindings.filter((finding) => !currentKeys.has(findingKey(finding)));

  const change: PlanChange = {
    added,
    removed,
    newStopCodes: [
      ...new Set(
        added
          .filter((finding) => findingClassOf(finding.class) === "stop")
          .map((finding) => finding.code?.trim())
          .filter((code): code is string => !!code)
      ),
    ].sort(),
  };

  const fromRelease = approved?.release_version?.trim() ?? "";
  const toRelease = current?.release_version?.trim() ?? "";
  if (fromRelease !== toRelease) {
    change.releaseChanged = { from: fromRelease, to: toRelease };
  }

  const fromBundle = approved?.bundle_id?.trim() ?? "";
  const toBundle = current?.bundle_id?.trim() ?? "";
  if (fromBundle !== toBundle) {
    change.bundleChanged = { from: fromBundle, to: toBundle };
  }

  const fromDrift = approved?.live_basis?.drift_observed === true;
  const toDrift = current?.live_basis?.drift_observed === true;
  if (fromDrift !== toDrift) {
    change.driftChanged = { from: fromDrift, to: toDrift };
  }

  return change;
}

/** Whether a diff found anything at all worth showing. */
export function planChanged(change: PlanChange): boolean {
  return (
    change.added.length > 0 ||
    change.removed.length > 0 ||
    !!change.releaseChanged ||
    !!change.bundleChanged ||
    !!change.driftChanged
  );
}
