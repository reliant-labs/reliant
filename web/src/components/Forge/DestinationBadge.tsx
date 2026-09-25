// Copyright (c) 2025 Reliant Labs

/**
 * Where an environment deploys, as one badge — plus, for a hosted env, the
 * facts that replace a kube context: the control plane's host, each
 * workload's URL, and the control plane's verdict.
 *
 * ONE COMPONENT FOR EVERY SCREEN that lists environments (the topology
 * matrix, the environments list, the env tab strip) so the vocabulary cannot
 * drift between them. It is built on forge-ui's `badge.tsx`, used as
 * installed, so the badge inherits the `.forge-ui` token bridge like every
 * other forge primitive on these screens.
 *
 * UNKNOWN IS ITS OWN BADGE. A forge too old to report `destination`, or a
 * value this build does not recognise, renders "Unknown" with a sentence
 * saying so — never "Cluster". Defaulting to cluster would put kube-context
 * language in front of an env that may have none, and would claim a fact no
 * one reported.
 *
 * Hosted is the one destination with a hue (`info`): it is the one whose
 * operational story differs — no cluster to go and look at — and the reader
 * scanning a matrix needs to find those rows first. Every other known
 * destination is neutral, because "compose" is not better or worse than
 * "cluster" and colour would imply it was.
 *
 * ── THE THREE QUESTIONS, IN READING ORDER ───────────────────────────────────
 *
 * A hosted env is read for: where does it run, is it healthy, what URL does it
 * serve. So the facts render in that order and each is one glance:
 *
 *   where    the destination badge and the control plane's host
 *   healthy  ONE health chip for the env (forge's roll-up), and one per
 *            workload — in the console's certainty vocabulary, with an icon,
 *            so it survives greyscale and colour-vision deficiency
 *   URL      each workload's name AND its link; a link is never the only
 *            label a workload has, because the name is what the reader knows
 *
 * `compact` (the topology matrix) shows the env chip and a one-line summary
 * instead of every workload, so one env stays one row — the matrix's whole
 * premise. The per-workload list lives on the Environments and Status screens.
 */

import { AlertTriangle, CheckCircle2, CircleDashed, ExternalLink, Loader2 } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import { Tooltip } from "@/components/ui/Tooltip";
import { cn } from "@/lib/utils";
import {
  destinationExplanation,
  destinationLabel,
  destinationOf,
  endpointHost,
  envHostedVerdict,
  hostedVerdictOf,
  hostedWorkloadsOf,
  shortDigest,
  verdictCertainty,
  verdictExplanation,
  verdictLabel,
  workloadLastError,
  workloadLink,
  type EnvDestination,
  type ForgeHostedWorkload,
  type ForgeTopologyEnv,
  type HostedVerdict,
} from "@/services/forge/topology";

import { CERTAINTY_STYLES } from "./stateVocabulary";

export function DestinationBadge({
  env,
  className,
}: {
  env: Pick<ForgeTopologyEnv, "env" | "destination">;
  className?: string;
}) {
  const destination = destinationOf(env);
  const explanation = destinationExplanation(destination);
  return (
    <Tooltip content={explanation}>
      <span
        data-testid={`destination-${env.env}`}
        data-destination={destination}
        className={cn("inline-flex whitespace-nowrap font-sans", className)}
      >
        {destination === "unknown" ? (
          // Unknown is flagged, not hidden — in the console's "not known"
          // treatment (dashed, unfilled: stateVocabulary), not as a warning.
          // Forge not saying is an absence of a fact, not a problem; and the
          // forge-ui warning badge's amber-on-amber measured 1.9:1 in light.
          <span
            className={cn(
              "inline-flex items-center rounded-full px-2 py-0.5 text-2xs font-medium text-foreground",
              CERTAINTY_STYLES.unknown.container
            )}
          >
            {destinationLabel(destination)}
          </span>
        ) : (
          <Badge label={destinationLabel(destination)} variant={badgeVariant(destination)} size="sm" />
        )}
      </span>
    </Tooltip>
  );
}

function badgeVariant(destination: EnvDestination): "info" | "neutral" {
  return destination === "hosted" ? "info" : "neutral";
}

/**
 * The hosted env's "where": control-plane host, the env's health, and its
 * workloads. Rendered where a cluster env shows its kube context.
 *
 * An env that was never deployed says so in words — "Not deployed yet" — rather
 * than showing an empty id, a blank list, or a health it does not have.
 */
export function HostedFacts({
  env,
  compact,
  inert,
}: {
  env: ForgeTopologyEnv;
  /** One line per env: the health chip and a workload summary (topology matrix). */
  compact?: boolean;
  /** Render URLs as text, not links — for use inside another control. */
  inert?: boolean;
}) {
  const host = endpointHost(env.endpoint);
  const workloads = hostedWorkloadsOf(env);
  const environmentId = (env.environment_id ?? "").trim();
  const deployed = environmentId !== "";

  return (
    <div className="flex min-w-0 flex-col gap-1" data-testid={`hosted-facts-${env.env}`}>
      <div className="flex min-w-0 items-center gap-2">
        {deployed ? (
          <VerdictChip verdict={envHostedVerdict(env)} testId={`hosted-verdict-${env.env}`} />
        ) : (
          <NotDeployedChip testId={`hosted-verdict-${env.env}`} />
        )}
        <span
          className="truncate font-mono text-xs text-muted-foreground"
          title={
            env.endpoint
              ? deployed
                ? `${env.endpoint} · environment ${environmentId}`
                : env.endpoint
              : undefined
          }
          data-testid={`hosted-endpoint-${env.env}`}
        >
          {host || "no control plane named"}
        </span>
      </div>

      {!deployed ? (
        <span
          className={cn("text-2xs text-muted-foreground", compact && "sr-only")}
          data-testid={`hosted-env-id-${env.env}`}
        >
          Not created on the control plane yet — the first deploy creates it.
        </span>
      ) : compact ? (
        <HostedWorkloadSummary env={env} workloads={workloads} inert={inert} />
      ) : (
        <>
          {workloads.length > 0 ? (
            <HostedWorkloadList envName={env.env} workloads={workloads} inert={inert} />
          ) : (
            <span className="text-2xs text-muted-foreground">
              The control plane reported no workloads.
            </span>
          )}
          {/* Support/CLI detail, not a thing to read at a glance: last, quiet. */}
          <span className="text-2xs text-muted-foreground" data-testid={`hosted-env-id-${env.env}`}>
            Environment ID <span className="font-mono">{environmentId}</span>
          </span>
        </>
      )}
    </div>
  );
}

/**
 * The matrix's one-line answer to "what does it serve": the first workload's
 * URL, plus a count of the rest and of any that are not healthy. The full
 * list is one click away on the Environments screen.
 */
function HostedWorkloadSummary({
  env,
  workloads,
  inert,
}: {
  env: ForgeTopologyEnv;
  workloads: ForgeHostedWorkload[];
  inert?: boolean;
}) {
  if (workloads.length === 0) {
    return <span className="text-2xs text-muted-foreground">No workloads reported</span>;
  }
  const withLink = workloads.find((w) => workloadLink(w) !== "") ?? null;
  const others = workloads.length - (withLink ? 1 : 0);
  const unhealthy = workloads.filter(
    (w) => verdictCertainty(hostedVerdictOf(w.verdict)) === "known-bad"
  ).length;
  return (
    <span className="flex min-w-0 items-center gap-1.5 text-xs">
      {withLink && (
        <WorkloadUrl
          envName={env.env}
          name={withLink.name || "workload"}
          link={workloadLink(withLink)}
          inert={inert}
        />
      )}
      {others > 0 && (
        <span className="shrink-0 text-2xs text-muted-foreground">
          {withLink ? `+${others} more` : `${others} workload${others === 1 ? "" : "s"}`}
        </span>
      )}
      {unhealthy > 0 && (
        // Hue on the icon, foreground on the words — destructive text at this
        // size measured 3.97:1 on the dark card.
        <span className="inline-flex shrink-0 items-center gap-1 text-2xs font-medium text-foreground">
          <AlertTriangle className="h-3 w-3 text-destructive" aria-hidden="true" />
          {unhealthy} not healthy
        </span>
      )}
    </span>
  );
}

/**
 * One line per hosted workload: its name, its health, and its URL (a link
 * unless `inert`); forge's version-mismatch call; and — only while it is not
 * serving — the control plane's last error. Shared by the Environments card
 * and the env status panel, which receive the same forge object under
 * different keys (`workloads` / `hosted_workloads`).
 */
export function HostedWorkloadList({
  envName,
  workloads,
  inert,
}: {
  envName: string;
  workloads: ForgeHostedWorkload[];
  inert?: boolean;
}) {
  return (
    <ul className="flex min-w-0 flex-col gap-1.5" aria-label={`Hosted workloads in ${envName}`}>
      {workloads.map((workload, index) => {
        const link = workloadLink(workload);
        const verdict = hostedVerdictOf(workload.verdict);
        const name = workload.name || `workload ${index + 1}`;
        const lastError = workloadLastError(workload);
        return (
          <li key={`${name}-${index}`} className="flex min-w-0 flex-col gap-0.5">
            <span
              className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-xs"
              data-testid={`hosted-workload-${envName}-${name}`}
            >
              {/* The name is an identifier and the label the reader knows —
                  shown even when there is a URL, which is a different fact. */}
              <span className="w-24 shrink-0 truncate font-mono text-foreground" title={name}>
                {name}
              </span>
              <VerdictChip verdict={verdict} reason={workload.verdict_reason} />
              {workload.drifted === true && <DriftChip workload={workload} />}
              {link ? (
                <WorkloadUrl envName={envName} name={name} link={link} inert={inert} />
              ) : (
                <span className="text-2xs text-muted-foreground">no public URL</span>
              )}
            </span>
            {lastError && (
              <span
                // Foreground words, destructive icon: destructive text at 11px
                // measured 3.97:1 on the dark card.
                className="flex min-w-0 items-start gap-1 pl-[6.5rem] text-2xs text-foreground"
                data-testid={`hosted-error-${envName}-${name}`}
              >
                <AlertTriangle className="mt-px h-3 w-3 shrink-0 text-destructive" aria-hidden="true" />
                <span className="sr-only">Last error: </span>
                <span className="line-clamp-2 break-words" title={lastError}>
                  {lastError}
                </span>
              </span>
            )}
          </li>
        );
      })}
    </ul>
  );
}

function WorkloadUrl({
  envName,
  name,
  link,
  inert,
}: {
  envName: string;
  name: string;
  link: string;
  inert?: boolean;
}) {
  const display = link.replace(/^https?:\/\//, "");
  if (inert) {
    return (
      <span
        className="min-w-0 truncate font-mono text-muted-foreground"
        data-testid={`hosted-url-${envName}-${name}`}
      >
        {display}
      </span>
    );
  }
  return (
    <a
      href={link}
      target="_blank"
      rel="noreferrer noopener"
      className={cn(
        "inline-flex min-w-0 items-center gap-1 rounded-sm font-mono text-foreground underline decoration-border underline-offset-2",
        "hover:text-primary hover:decoration-primary",
        "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      )}
      data-testid={`hosted-url-${envName}-${name}`}
    >
      <span className="truncate">{display}</span>
      <ExternalLink className="h-3 w-3 shrink-0" aria-hidden="true" />
      <span className="sr-only">(opens in a new tab)</span>
    </a>
  );
}

/** The icon per verdict: the same certainty glyph family the topology cells use. */
const VERDICT_ICON: Record<HostedVerdict, LucideIcon> = {
  converged: CheckCircle2,
  converging: Loader2,
  diverged: AlertTriangle,
  degraded: AlertTriangle,
  unknown: CircleDashed,
};

/**
 * The verdict in the console's three-level certainty vocabulary
 * (stateVocabulary's CERTAINTY_STYLES — the same fill/border treatment the
 * topology cells and CertaintyLegend use, so there is no second colour
 * language). Converged is the only known-good; converging is NOT — a workload
 * that has not held its shape past the stability window has not yet earned it,
 * so it keeps the dashed, unfilled `unknown` treatment.
 *
 * The TEXT is always `text-foreground`; the hue lives on the icon, the fill
 * and the border. Hued text at 11px over its own 15% tint measured 2.8:1
 * (success, light) and 3.5:1 (destructive, dark) — below AA — while the
 * certainty still reads from three independent axes without it.
 */
function VerdictChip({
  verdict,
  reason,
  testId,
}: {
  verdict: HostedVerdict;
  reason?: string;
  testId?: string;
}) {
  const certainty = verdictCertainty(verdict);
  const style = CERTAINTY_STYLES[certainty];
  const label = verdictLabel(verdict);
  const Icon = VERDICT_ICON[verdict];
  const detail = reason ? `${verdictExplanation(verdict)} Control plane: ${reason}.` : verdictExplanation(verdict);
  return (
    <Tooltip content={detail}>
      <span
        className={cn(
          "inline-flex shrink-0 items-center gap-1 rounded px-1.5 text-2xs font-medium leading-5 text-foreground",
          style.container
        )}
        data-verdict={verdict}
        data-certainty={certainty}
        data-testid={testId}
      >
        <Icon className={cn("h-3 w-3 shrink-0", style.foreground)} aria-hidden="true" />
        {label}
      </span>
    </Tooltip>
  );
}

/** A hosted env with no control-plane id yet: nothing has been deployed, so there is no health. */
function NotDeployedChip({ testId }: { testId?: string }) {
  const style = CERTAINTY_STYLES.unknown;
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center gap-1 rounded px-1.5 text-2xs font-medium leading-5 text-foreground",
        style.container
      )}
      data-verdict="not-deployed"
      data-certainty="unknown"
      data-testid={testId}
    >
      <CircleDashed className={cn("h-3 w-3 shrink-0", style.foreground)} aria-hidden="true" />
      Not deployed yet
    </span>
  );
}

/**
 * Forge's drift call — the digest running is not the one published — as a
 * known-bad chip. Worded as what it IS ("wrong version") rather than as a
 * second "Drifted", which is already a verdict above.
 */
function DriftChip({ workload }: { workload: ForgeHostedWorkload }) {
  const style = CERTAINTY_STYLES["known-bad"];
  const detail = `Running ${shortDigest(workload.observed_digest) || "an unknown digest"}, but the published release asks for ${
    shortDigest(workload.desired_digest) || "an unknown digest"
  }.`;
  return (
    <Tooltip content={detail}>
      <span
        className={cn(
          "inline-flex shrink-0 items-center gap-1 rounded px-1.5 text-2xs font-medium leading-5 text-foreground",
          style.container
        )}
        data-drifted="true"
      >
        <AlertTriangle className={cn("h-3 w-3 shrink-0", style.foreground)} aria-hidden="true" />
        Wrong version
      </span>
    </Tooltip>
  );
}
