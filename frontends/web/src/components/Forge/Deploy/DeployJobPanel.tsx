// Copyright (c) 2025 Reliant Labs

/**
 * THE JOB. Two axes, rendered as two axes, because collapsing them is the one
 * mistake this panel exists to prevent.
 *
 *   THE INVOCATION — running / completed / failed / unknown. Whether forge
 *   reached a determinate outcome.
 *   THE DEPLOY'S VERDICT — read from forge's report, in the three-level certainty
 *   vocabulary. Whether the environment actually converged.
 *
 * `completed` on the first axis says NOTHING about the second: a deploy whose
 * rollout failed, timed out, or was never waited on arrives as completed, with
 * the truth in the report. So the job's own badge deliberately carries no success
 * hue, and the hue on this screen comes from the verdict.
 *
 * THREE STATES THAT ARE NOT SUCCESS AND NOT FAILURE, and all three are on this
 * panel:
 *
 *   RUNNING. The outcome does not exist yet. No green, no red, and no summary of
 *   a rollout that has not finished.
 *
 *   UNKNOWN. A killed job, a job whose process died, a job that produced no
 *   parseable report, or a handle the daemon no longer recognises — the registry
 *   is in memory, so a daemon that restarted mid-apply has genuinely lost the
 *   outcome. MANIFESTS MAY OR MAY NOT HAVE REACHED THE CLUSTER, said in those
 *   words, with verifying the environment as the offered move. Not a retry:
 *   retrying against a cluster that may already be converging is the worst
 *   available action.
 *
 *   COMPLETED WITH AN INDETERMINATE ROLLOUT. timed_out and not_waited resources,
 *   which forge reports faithfully and which this panel must not average away.
 *
 * `failed` is the ONLY disposition that licenses a retry, and it is the one case
 * where nothing can have shipped: the invocation never got off the ground.
 */

import { RefreshCw } from "lucide-react";

import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/Button";
import {
  deployCertainty,
  describeDeployCertainty,
  jobIsIndeterminate,
  type DeployJobDisposition,
  type ForgeDeployReport,
} from "@/services/forge/deploy";
import type { ForgeOutcome } from "@/services/forge/topology";
import { CERTAINTY_LABELS, CERTAINTY_STYLES } from "../stateVocabulary";

import { ForgeMalformed, ForgeUnreachable, ForgeUnsupported, NotForgeProject } from "../ForgeStates";
import { JOB_DISPOSITION_STYLES } from "./deployVocabulary";
import { DeployPlanView, RolloutPanel } from "./DeployPlanView";

export interface DeployJobPanelProps {
  handle: string;
  env: string;
  jobStatus: DeployJobDisposition;
  /** The server's sentence for a non-completed terminal state. */
  jobStatusDetail?: string;
  startedAt?: string;
  finishedAt?: string;
  /** Forge's report, once one exists. Null while running, and null for a job that died. */
  report?: ForgeOutcome<ForgeDeployReport> | null;
  /** Verify the environment — the correct move for an unknown outcome. */
  onVerify?: () => void;
  isVerifying?: boolean;
  onClose: () => void;
  projectName?: string;
}

export function DeployJobPanel({
  handle,
  env,
  jobStatus,
  jobStatusDetail,
  startedAt,
  finishedAt,
  report,
  onVerify,
  isVerifying,
  onClose,
  projectName,
}: DeployJobPanelProps) {
  const jobStyle = JOB_DISPOSITION_STYLES[jobStatus];
  const JobIcon = jobStyle.icon;
  const running = jobStatus === "running";
  const indeterminate = jobIsIndeterminate(jobStatus);

  return (
    <div className="space-y-4" data-testid="deploy-job" data-job-status={jobStatus}>
      {/* THE INVOCATION's own state. Note that `completed` gets no success hue —
          finishing is not succeeding. */}
      <section
        className={cn("space-y-1.5 rounded-lg px-4 py-3", jobStyle.container)}
        data-testid="deploy-job-status"
      >
        <div className={cn("flex items-center gap-2", jobStyle.foreground)}>
          <JobIcon
            className={cn("h-4 w-4 shrink-0", running && "animate-spin")}
            aria-hidden="true"
          />
          <h3 className="text-sm font-medium" data-testid="deploy-job-heading">
            {jobStyle.label}
          </h3>
        </div>
        <p className="text-xs text-muted-foreground" data-testid="deploy-job-blurb">
          {jobStyle.blurb}
        </p>
        {/* The server's own sentence, when it sent one. Branching is on
            jobStatus; this is displayed, never parsed. */}
        {jobStatusDetail && (
          <p className="text-2xs text-muted-foreground" data-testid="deploy-job-detail">
            {jobStatusDetail}
          </p>
        )}
        <p className="font-mono text-2xs text-muted-foreground" data-testid="deploy-job-handle">
          {env} · {handle}
          {startedAt && ` · started ${formatStamp(startedAt)}`}
          {finishedAt && ` · finished ${formatStamp(finishedAt)}`}
        </p>
      </section>

      {/* THE UNKNOWN OUTCOME, as its own panel with its own action. This is the
          state that must not be collapsed, so it says the thing that is true and
          offers the only thing that resolves it. */}
      {indeterminate && (
        <section
          data-testid="deploy-job-unknown"
          className="space-y-2 rounded-lg border border-dashed border-border px-4 py-3"
        >
          <p className="text-xs text-foreground">
            Manifests may or may not have reached the cluster. Nothing here can tell you which —
            forge&apos;s outcome was lost, not negative.
          </p>
          <p className="text-2xs text-muted-foreground">
            Do not start another deploy to find out: a retry against a cluster that may already be
            converging applies a second manifest stream on top of the first. Read the environment
            instead.
          </p>
          {onVerify && (
            <Button
              variant="secondary"
              size="xs"
              onClick={onVerify}
              loading={isVerifying}
              disabled={isVerifying}
              leftIcon={<RefreshCw className="h-3 w-3" />}
              data-testid="deploy-job-verify"
            >
              {isVerifying ? "Reading cluster…" : `Verify ${env} against its cluster`}
            </Button>
          )}
        </section>
      )}

      {/* While running there is no verdict to render, and inventing an
          intermediate one would be inventing an answer. */}
      {running && (
        <p
          data-testid="deploy-job-running-note"
          className="rounded-lg border border-dashed border-border px-4 py-3 text-xs text-muted-foreground"
        >
          Forge is applying and watching for readiness. Its report — including which resources
          became ready and which did not — appears here when it finishes.
        </p>
      )}

      {report && <ReportSection outcome={report} projectName={projectName} />}

      <div className="flex justify-end">
        <Button variant="secondary" size="sm" onClick={onClose}>
          {running ? "Close — the deploy keeps running" : "Done"}
        </Button>
      </div>
    </div>
  );
}

/**
 * Forge's report, once it exists: the VERDICT first, then the detail.
 *
 * The verdict is deployCertainty over the whole document — not `ok` alone, which
 * cannot distinguish "every resource became ready" from "forge exited zero and
 * waited for nothing". The rollout section is hoisted above the rest because
 * after a deploy it is the part that answers "did it work", and the plan view is
 * told not to repeat it.
 */
function ReportSection({
  outcome,
  projectName,
}: {
  outcome: ForgeOutcome<ForgeDeployReport>;
  projectName?: string;
}) {
  // The job produced something unreadable. That says nothing about the cluster,
  // so it must not read as either outcome — these shared states say exactly what
  // went wrong and claim nothing else.
  if (outcome.kind === "not-forge-project") return <NotForgeProject projectName={projectName} />;
  if (outcome.kind === "unsupported") return <ForgeUnsupported meta={outcome.meta} />;
  if (outcome.kind === "unreachable") return <ForgeUnreachable meta={outcome.meta} />;
  if (outcome.kind === "malformed") return <ForgeMalformed meta={outcome.meta} />;

  const report = outcome.report;
  const certainty = deployCertainty(report);
  const style = CERTAINTY_STYLES[certainty];

  return (
    <div className="space-y-4">
      <section
        className={cn("space-y-1 rounded-lg px-4 py-3", style.container)}
        data-testid="deploy-verdict"
        data-certainty={certainty}
      >
        <h3 className={cn("text-sm font-medium", style.foreground)} data-testid="deploy-verdict-label">
          {CERTAINTY_LABELS[certainty]}
        </h3>
        <p className="text-xs text-muted-foreground" data-testid="deploy-verdict-blurb">
          {describeDeployCertainty(certainty, report)}
        </p>
      </section>

      {/* The rollout, hoisted: after a deploy it is the section that answers the
          question. */}
      <RolloutPanel plan={report} />

      <DeployPlanView plan={report} showRollout={false} />
    </div>
  );
}

/** Renders an RFC3339 stamp locally, falling back to the raw string. */
function formatStamp(value: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return parsed.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
