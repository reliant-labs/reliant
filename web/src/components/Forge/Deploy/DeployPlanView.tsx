// Copyright (c) 2025 Reliant Labs

/**
 * The deploy PREVIEW: everything a reviewer needs before authorising manifests
 * onto a live cluster.
 *
 * PURE PROPS, like PromotePlanView and TopologyView — it takes a document and
 * renders it, so the visual contract can be tested by handing it a plan rather
 * than by standing up a query client, a transport, and under no circumstances a
 * cluster.
 *
 * Four things are given structural prominence, because each is a way a reviewer
 * can misread this screen into approving something else:
 *
 *   WHICH CLUSTER, first and largest. Named, with the namespace, and with EVERY
 *   declared context listed when there is more than one. `current_context` is
 *   rendered only as an explicitly-labelled aside — forge deploys to the
 *   DECLARED context and never consults the ambient one, so presenting the
 *   ambient one as the target would invert the entire guard.
 *
 *   BLOCKING PREFLIGHT FINDINGS, as a stop rather than a warning. The preflight
 *   checks that referenced Secret keys and container images actually exist on
 *   the live target, so a blocking finding means this deploy WILL fail. It gets a
 *   destructive panel above the detail, and the flow does not offer a confirm
 *   while one stands.
 *
 *   THE PINNING SPLIT. digest_count against tag_count. A tag-pinned image cannot
 *   be proven — whatever the tag points at when the kubelet pulls is what runs —
 *   and this is a real, current condition rather than a hypothetical: prod's
 *   internal-console ships `:latest` today. It is a caveat, not a failure, and it
 *   is rendered as one.
 *
 *   THE ROLLOUT MODE, above the rollout results, because it changes what an
 *   absent answer MEANS. Under `skip` every resource is legitimately not_waited,
 *   and a reader who does not know that will read a screen of dashes as a
 *   problem — while a reader who assumes `wait` will read them as fine.
 */

import { Boxes, Layers, Package, ShieldCheck, Target } from "lucide-react";

import { cn } from "@/lib/utils";
import { Tooltip } from "@/components/ui/Tooltip";
import {
  blockingFindings,
  guardVerdictOf,
  isMultiCluster,
  pinningOf,
  preflightRan,
  preflightStatusOf,
  rolloutModeOf,
  targetContexts,
  type ForgeDeployFinding,
  type ForgeDeployReport,
} from "@/services/forge/deploy";

import { PREFLIGHT_STATUS_LABELS, ROLLOUT_MODE_LABELS, TAG_PINNING_ICON } from "./deployVocabulary";
import { RolloutResults } from "./RolloutResults";

export interface DeployPlanViewProps {
  plan: ForgeDeployReport;
  /**
   * Set false when the container renders the rollout section itself — the
   * finished-deploy panel does, to give the verdict top billing. A preview has no
   * rollout results to show anyway (nothing was applied), so this defaults on
   * only to keep the mode statement visible.
   */
  showRollout?: boolean;
}

export function DeployPlanView({ plan, showRollout = true }: DeployPlanViewProps) {
  return (
    <div className="space-y-4" data-testid="deploy-plan">
      <TargetPanel plan={plan} />
      <PreflightPanel plan={plan} />
      <ImagesPanel plan={plan} />
      <ResourcesPanel plan={plan} />
      {showRollout && <RolloutPanel plan={plan} />}
    </div>
  );
}

/**
 * WHERE THIS LANDS. The most consequential element on the screen, so it is
 * first, largest, and built from targetContexts rather than from any single
 * field.
 *
 * The multi-cluster case is not a footnote: control-plane's dev env declares two
 * clusters, and an earlier version of the backend reported only the env-wide one
 * — which would have let this panel omit a cluster the deploy was about to write
 * to. Every declared context is listed whenever there is more than one, with the
 * count stated in words so a reader cannot skim past it.
 */
export function TargetPanel({ plan }: { plan: ForgeDeployReport }) {
  const contexts = targetContexts(plan);
  const multi = isMultiCluster(plan);
  const namespace = plan.target?.namespace ?? "";
  const currentContext = (plan.guard?.current_context ?? "").trim();
  const verdict = guardVerdictOf(plan.guard?.verdict);

  return (
    <section
      data-testid="deploy-target"
      data-multi-cluster={multi ? "true" : "false"}
      data-context-count={contexts.length}
      className={cn(
        "space-y-2 rounded-lg px-4 py-3",
        verdict === "refuse"
          ? "border border-solid border-destructive/50 bg-destructive/10"
          : "border border-solid border-warning/50 bg-warning/10"
      )}
    >
      <div className="flex items-center gap-2 text-foreground">
        <Target className="h-4 w-4 shrink-0" aria-hidden="true" />
        <h3 className="text-sm font-medium" data-testid="deploy-target-heading">
          {multi
            ? `This deploys to ${contexts.length} clusters`
            : contexts.length === 1
              ? "This deploys to a live cluster"
              : "This environment declares no cluster"}
        </h3>
      </div>

      {contexts.length === 0 ? (
        // A real shape — host-only, compose, frontend-only — and also the shape
        // from which no deploy can be authorised, since the confirmation token
        // has no spelling for "no cluster".
        <p data-testid="deploy-target-none" className="text-xs text-muted-foreground">
          No <span className="font-mono">forge.K8sCluster.cluster</span> is declared for this
          environment, so there is no cluster for a deploy to be authorised against.
        </p>
      ) : (
        <ul className="space-y-1" data-testid="deploy-target-contexts">
          {contexts.map((context) => (
            <li
              key={context}
              data-testid={`deploy-target-context-${context}`}
              className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5"
            >
              <span className="font-mono text-sm font-medium text-foreground">{context}</span>
              {namespace && (
                <span className="font-mono text-xs text-muted-foreground">
                  namespace {namespace}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}

      {multi && (
        <p data-testid="deploy-multi-cluster-note" className="text-2xs text-muted-foreground">
          This environment declares more than one cluster. Every context listed above will be
          written to.
        </p>
      )}

      {/* THE AMBIENT CONTEXT, and it is labelled as not-the-target. Forge never
          reads it. It is here only because an operator always wants to see it,
          and omitting it invites them to go and check kubectl themselves — where
          they would find a context that has nothing to do with this deploy. */}
      {currentContext && (
        <Tooltip content="Your kubectl current-context. Forge NEVER reads it and never falls back to it: a deploy goes to the context this environment declares. It is shown only so you can see the difference.">
          <p data-testid="deploy-current-context" className="text-2xs text-muted-foreground">
            Not the target: your kubectl current-context is{" "}
            <span className="font-mono">{currentContext}</span>.
          </p>
        </Tooltip>
      )}

      {/* Forge's own refusal, when the guard is what stops this. */}
      {verdict === "refuse" && (
        <div
          data-testid="deploy-guard-refused"
          className="space-y-1 rounded-md border border-solid border-destructive/50 bg-destructive/10 px-3 py-2"
        >
          <p className="text-xs font-medium text-destructive">
            Forge will not deploy this environment.
          </p>
          {plan.guard?.reason && (
            <p className="text-2xs text-muted-foreground">Reason: {plan.guard.reason}</p>
          )}
          {plan.guard?.fix && (
            <p className="text-2xs text-foreground" data-testid="deploy-guard-fix">
              {plan.guard.fix}
            </p>
          )}
          {(plan.guard?.available_contexts?.length ?? 0) > 0 && (
            <p className="text-2xs text-muted-foreground">
              Your kubeconfig has: {plan.guard?.available_contexts?.join(", ")}
            </p>
          )}
        </div>
      )}

      {/* What ships: the release whose digests are pinned, or a statement that
          there is no binding — which is the case the token's expect_unbound half
          exists for. */}
      <p className="text-2xs text-muted-foreground" data-testid="deploy-release">
        {plan.release
          ? `Shipping release ${plan.release}'s pinned digests.`
          : "This environment has no release binding, so it deploys by resolved tag."}
        {plan.image_tag && ` Tag ${plan.image_tag}${plan.tag_source ? ` (from ${plan.tag_source})` : ""}.`}
      </p>
    </section>
  );
}

/**
 * THE PREFLIGHT. Two failure modes to separate, and they look nothing alike.
 *
 * A BLOCKING finding means the deploy will fail — a referenced Secret key or
 * container image that is not on the live target — and it gets a destructive
 * panel that says so. The confirm step is absent while one stands, which is
 * enforced in DeployFlow rather than here; this panel's job is to make the reason
 * impossible to miss.
 *
 * A SKIPPED status means nothing was checked, which is a different and quieter
 * danger: it renders as "nothing was checked" in those words, never as an absence
 * of findings.
 */
export function PreflightPanel({ plan }: { plan: ForgeDeployReport }) {
  const status = preflightStatusOf(plan.preflight?.status);
  const findings = plan.preflight?.findings ?? [];
  const blocking = blockingFindings(plan);
  const advisory = findings.filter((finding) => finding.blocking !== true);

  return (
    <section
      className="space-y-2 rounded-lg border border-border px-4 py-3"
      data-testid="deploy-preflight"
      data-preflight-status={status}
      data-blocking-count={blocking.length}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <ShieldCheck className="h-3.5 w-3.5" aria-hidden="true" />
          Preflight
        </h3>
        <span
          data-testid="deploy-preflight-status"
          className={cn(
            "text-2xs",
            preflightRan(status) ? "text-muted-foreground" : "text-warning"
          )}
        >
          {PREFLIGHT_STATUS_LABELS[status]}
        </span>
      </div>

      {blocking.length > 0 && (
        // THE STOP. Not a warning beside an enabled button: these findings mean
        // the apply fails.
        <div
          data-testid="deploy-preflight-blocking"
          className="space-y-2 rounded-md border border-solid border-destructive/50 bg-destructive/10 px-3 py-2"
        >
          <p className="text-xs font-medium text-destructive">
            {blocking.length} blocking finding{blocking.length === 1 ? "" : "s"} — this deploy would
            fail.
          </p>
          <p className="text-2xs text-muted-foreground">
            Forge checked the live target and something this deploy references is not there. These
            are not advisory: fix them and re-plan.
          </p>
          <ul className="space-y-1">
            {blocking.map((finding, index) => (
              <FindingRow key={findingKey(finding, index)} finding={finding} blocking />
            ))}
          </ul>
        </div>
      )}

      {advisory.length > 0 && (
        <ul className="space-y-1" data-testid="deploy-preflight-advisory">
          {advisory.map((finding, index) => (
            <FindingRow key={findingKey(finding, index)} finding={finding} />
          ))}
        </ul>
      )}

      {findings.length === 0 && (
        <p className="text-xs text-muted-foreground">
          {preflightRan(status)
            ? "Forge checked the live target and found nothing wrong."
            : // "No findings" would be a claim about the cluster nobody made.
              "No checks were performed, so nothing is known about whether this deploy's Secrets and images exist on the target."}
        </p>
      )}
    </section>
  );
}

/**
 * One finding.
 *
 * `blocking` is read off the finding rather than inferred from the check name,
 * so a check this build has never heard of still reports its consequence
 * correctly — the set of checks grows, and forge deliberately types `check` as a
 * plain string for that reason.
 */
function FindingRow({ finding, blocking }: { finding: ForgeDeployFinding; blocking?: boolean }) {
  return (
    <li
      data-testid={`deploy-finding-${finding.check ?? "unnamed"}`}
      data-blocking={blocking ? "true" : "false"}
      className={cn(
        "space-y-0.5 rounded px-2 py-1 text-2xs",
        blocking ? "bg-destructive/10 text-destructive" : "text-muted-foreground"
      )}
    >
      <span className="font-mono">{finding.check || "check"}</span>
      {finding.subject && <span className="pl-1.5 font-mono">{finding.subject}</span>}
      {(finding.keys?.length ?? 0) > 0 && (
        <span className="pl-1.5 font-mono">keys: {finding.keys?.join(", ")}</span>
      )}
      {finding.detail && <p className="text-muted-foreground">{finding.detail}</p>}
    </li>
  );
}

/** A stable-enough key: findings have no id, and the index disambiguates repeats. */
function findingKey(finding: ForgeDeployFinding, index: number): string {
  return `${finding.check ?? "check"}:${finding.subject ?? ""}:${index}`;
}

/**
 * IMAGES, as the pinning split.
 *
 * The counts come first because they are the whole question: a deploy where
 * tag_count is zero ships bytes that can be proven, and one where it is not does
 * not. A tag-pinned image is a CAVEAT — it is legitimate, forge ships it, and
 * prod runs one today — so it takes the warning hue rather than destructive, and
 * the copy says what is actually unknown about it rather than calling it broken.
 */
export function ImagesPanel({ plan }: { plan: ForgeDeployReport }) {
  const images = plan.images?.images ?? [];
  const digestCount = plan.images?.digest_count ?? 0;
  const tagCount = plan.images?.tag_count ?? 0;

  return (
    <section
      className="space-y-2 rounded-lg border border-border px-4 py-3"
      data-testid="deploy-images"
      data-digest-count={digestCount}
      data-tag-count={tagCount}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Package className="h-3.5 w-3.5" aria-hidden="true" />
          Images
        </h3>
        <div className="flex flex-wrap items-center gap-1.5">
          <span
            data-testid="deploy-digest-count"
            className="inline-flex items-center gap-1 rounded-full border border-solid border-success/40 bg-success/15 px-2 py-0.5 text-2xs text-success"
          >
            {digestCount} digest-pinned
          </span>
          {tagCount > 0 && (
            <span
              data-testid="deploy-tag-count"
              className="inline-flex items-center gap-1 rounded-full border border-solid border-warning/40 bg-warning/15 px-2 py-0.5 text-2xs text-warning"
            >
              <TAG_PINNING_ICON className="h-3 w-3" aria-hidden="true" />
              {tagCount} by mutable tag
            </span>
          )}
        </div>
      </div>

      {tagCount > 0 && (
        <p data-testid="deploy-tag-caveat" className="text-2xs text-warning">
          {tagCount === 1 ? "One image is" : `${tagCount} images are`} referenced by a mutable tag.
          Whatever that tag points at when the kubelet pulls is what runs, so the bytes this deploy
          ships cannot be proven to be the bytes that were built.
        </p>
      )}

      {images.length === 0 ? (
        <p className="text-xs text-muted-foreground">This plan renders no container images.</p>
      ) : (
        <ul className="space-y-1">
          {images.map((image) => {
            const pinning = pinningOf(image.pinning);
            return (
              <li
                key={image.reference ?? image.repository}
                data-testid={`deploy-image-${image.repository ?? image.reference}`}
                data-pinning={pinning}
                className="flex flex-wrap items-center gap-x-2 gap-y-0.5"
              >
                <span
                  className={cn(
                    "w-16 text-2xs font-medium",
                    pinning === "digest" ? "text-success" : "text-warning"
                  )}
                >
                  {pinning === "digest" ? "digest" : pinning === "tag" ? "tag" : "unknown"}
                </span>
                <span className="truncate font-mono text-2xs text-foreground">
                  {image.reference}
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

/**
 * The resource identities the stream carries.
 *
 * Under a preview these are what WOULD be applied; the mode says which, and
 * there is deliberately no per-resource "applied" flag that could disagree with
 * it. Identity only — a UI wants "these 34 objects", not the YAML.
 */
export function ResourcesPanel({ plan }: { plan: ForgeDeployReport }) {
  const resources = plan.resources ?? [];

  return (
    <section
      className="space-y-2 rounded-lg border border-border px-4 py-3"
      data-testid="deploy-resources"
    >
      <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <Layers className="h-3.5 w-3.5" aria-hidden="true" />
        {resources.length} resource{resources.length === 1 ? "" : "s"}
      </h3>
      {resources.length > 0 && (
        <ul className="grid gap-x-4 gap-y-0.5 sm:grid-cols-2">
          {resources.map((resource) => (
            <li
              key={`${resource.kind}/${resource.name}`}
              className="truncate font-mono text-2xs text-muted-foreground"
            >
              {resource.kind}/{resource.name}
            </li>
          ))}
        </ul>
      )}
      {(plan.targets?.length ?? 0) > 0 && (
        <p className="text-2xs text-muted-foreground">
          Narrowed to: {plan.targets?.join(", ")}
        </p>
      )}
    </section>
  );
}

/**
 * The rollout section: the MODE first, then whatever results exist.
 *
 * The mode is above the results because it changes what they mean. Under `skip`
 * every not_waited is expected, and a reader who does not know that reads a
 * screen of dashes as a problem — while a reader who assumes `wait` reads the
 * same dashes as fine. Neither is what the document says.
 */
export function RolloutPanel({ plan }: { plan: ForgeDeployReport }) {
  const mode = rolloutModeOf(plan.rollout?.mode);
  const timeout = plan.rollout?.timeout_seconds;

  return (
    <section
      className="space-y-2 rounded-lg border border-border px-4 py-3"
      data-testid="deploy-rollout"
      data-rollout-mode={mode}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Boxes className="h-3.5 w-3.5" aria-hidden="true" />
          Rollout
        </h3>
        {typeof timeout === "number" && timeout > 0 && (
          <Tooltip content="The readiness budget PER RESOURCE, not for the whole set.">
            <span className="text-2xs text-muted-foreground">{timeout}s per resource</span>
          </Tooltip>
        )}
      </div>

      <p
        data-testid="deploy-rollout-mode"
        className={cn("text-2xs", mode === "skip" ? "text-warning" : "text-muted-foreground")}
      >
        {ROLLOUT_MODE_LABELS[mode]}
      </p>

      <RolloutResults plan={plan} />
    </section>
  );
}
