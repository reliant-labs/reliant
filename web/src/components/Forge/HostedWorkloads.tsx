// Copyright (c) 2025 Reliant Labs

/**
 * A hosted environment's workloads, one line each: its name, its health, its
 * URL — and, only while it is not serving, the control plane's last error.
 *
 * ONE RENDERER FOR BOTH SOURCES. The control plane's GetStatus answer is
 * converted into forge's hosted-workload shape (services/forge/cloudEnvs.ts),
 * so a workload reads identically whether the browser asked the control plane
 * directly or forge asked it on the daemon's behalf.
 *
 * THE THREE QUESTIONS, IN READING ORDER. A hosted workload is read for: is it
 * healthy, what URL does it serve, and is it running what was published. Each
 * is one glance:
 *
 *   healthy  a verdict chip in the console's certainty vocabulary
 *            (stateVocabulary), with an icon, so it survives greyscale and
 *            colour-vision deficiency
 *   URL      the workload's name AND its link; a link is never the only label
 *            a workload has, because the name is what the reader knows
 *   version  forge's drift call, as its own chip
 */

import { AlertTriangle, CheckCircle2, CircleDashed, ExternalLink, Loader2, Play, Square } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { Tooltip } from "@/components/ui/Tooltip";
import { cn } from "@/lib/utils";
import {
  hostedVerdictOf,
  shortDigest,
  verdictCertainty,
  verdictExplanation,
  verdictLabel,
  workloadLastError,
  workloadLink,
  type ForgeHostedWorkload,
  type HostedVerdict,
} from "@/services/forge/topology";

import { useScaleDeployment } from "@/hooks/forge-queries";
import { cloudErrorDetail } from "@/services/forge/cloudEnvs";

import { runStateOf } from "./runStateVocabulary";
import { CERTAINTY_STYLES } from "./stateVocabulary";

/**
 * One line per hosted workload: its name, its health, and its URL (a link
 * unless `inert`); forge's version-mismatch call; and — only while it is not
 * serving — the control plane's last error.
 */
export function HostedWorkloadList({
  envName,
  workloads,
  inert,
  runControls,
}: {
  envName: string;
  workloads: ForgeHostedWorkload[];
  inert?: boolean;
  /** Show per-workload Stop / Start (control-plane-placed environments only). */
  runControls?: boolean;
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
              {runControls && <RunStateChip workload={workload} />}
              {workload.drifted === true && <DriftChip workload={workload} />}
              {link ? (
                <WorkloadUrl envName={envName} name={name} link={link} inert={inert} />
              ) : (
                <span className="text-2xs text-muted-foreground">no public URL</span>
              )}
              {runControls && workload.deployment_id && <RunToggle workload={workload} envName={envName} />}
            </span>
            {lastError && (
              <span
                // Foreground words, destructive icon: destructive text at 11px
                // measured 3.97:1 on the dark card.
                className="flex min-w-0 items-start gap-1 pl-[6.5rem] text-2xs text-foreground"
                data-testid={`hosted-error-${envName}-${name}`}
              >
                <AlertTriangle className="mt-px h-3 w-3 shrink-0 text-destructive-ink" aria-hidden="true" />
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
 * topology cells use, so there is no second colour
 * language). Converged is the only known-good; converging is NOT — a workload
 * that has not held its shape past the stability window has not yet earned it,
 * so it keeps the dashed, unfilled `unknown` treatment.
 *
 * The TEXT is always `text-foreground`; the hue lives on the icon, the fill
 * and the border. Hued text at 11px over its own 15% tint measured 2.8:1
 * (success, light) and 3.5:1 (destructive, dark) — below AA — while the
 * certainty still reads from three independent axes without it.
 */
export function VerdictChip({
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

/** The owner's stop, a billing suspension, or a start/stop in flight — said in words, not as a health verdict. */
function RunStateChip({ workload }: { workload: ForgeHostedWorkload }) {
  const view = runStateOf(workload);
  if (view.kind === "unknown" || view.kind === "running") return null;
  const style = CERTAINTY_STYLES[view.kind === "billing" ? "known-bad" : "unknown"];
  return (
    <Tooltip content={view.detail}>
      <span
        className={cn(
          "inline-flex shrink-0 items-center gap-1 rounded px-1.5 text-2xs font-medium leading-5 text-foreground",
          style.container
        )}
        data-run-state={view.kind}
      >
        {view.label}
      </span>
    </Tooltip>
  );
}

function RunToggle({ workload, envName }: { workload: ForgeHostedWorkload; envName: string }) {
  const scale = useScaleDeployment();
  const stopped = workload.declared_run_state === "suspended";
  const name = workload.name ?? "workload";
  return (
    <>
      <button
        type="button"
        disabled={scale.isPending}
        onClick={() =>
          scale.mutate({ deploymentId: workload.deployment_id ?? "", state: stopped ? "running" : "suspended" })
        }
        className={cn(
          "inline-flex shrink-0 items-center gap-1 rounded border border-border px-1.5 text-2xs leading-5 text-foreground",
          "hover:bg-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
        )}
        data-testid={`hosted-run-toggle-${name}`}
        aria-label={`${stopped ? "Start" : "Stop"} ${name}`}
      >
        {stopped ? <Play className="h-3 w-3" aria-hidden="true" /> : <Square className="h-3 w-3" aria-hidden="true" />}
        {stopped ? "Start" : "Stop"}
      </button>
      {scale.isError && (
        <span
          role="alert"
          className="basis-full pl-[6.5rem] text-2xs text-foreground"
          data-testid={`hosted-run-error-${envName}-${name}`}
        >
          {cloudErrorDetail(scale.error)}
        </span>
      )}
    </>
  );
}
