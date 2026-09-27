// Copyright (c) 2025 Reliant Labs

/**
 * WHAT IS RUNNING IN THIS ENVIRONMENT — from whichever source can see it.
 *
 *   Reliant cloud   the control plane's GetStatus: one row per deployment,
 *                   with its verdict, drift call, observed digest and URL.
 *                   A database read of what the reconcile worker observed —
 *                   no daemon, no cluster hop.
 *   cluster / mixed forge's cluster inventory (`forge env status`), which
 *                   reads the cluster the env's KCL names. Needs the daemon.
 *   local           nothing is DEPLOYED: `forge env up` runs the working
 *                   tree. The processes it launched are the Dev stack
 *                   section, which says so; this section does not pretend.
 *
 * Pure props — the page owns the queries — so each branch is testable with
 * object literals.
 */

import { Card } from "@/components/ui";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import type { ForgeEnvSummary } from "@/services/forge/environments";
import type { ForgeEnvStatusReport } from "@/services/forge/status";
import type { ForgeOutcome } from "@/services/forge/topology";

import { WorkloadInventory } from "../Environments/WorkloadInventory";
import { HostedWorkloadList } from "../HostedWorkloads";
import { formatTimestamp } from "../Overview/EnvironmentTable";

export type WorkloadsSource =
  | { kind: "cloud"; status: CloudEnvStatus | undefined; isLoading: boolean; error: Error | null }
  | {
      kind: "daemon";
      outcome: ForgeOutcome<ForgeEnvStatusReport> | undefined;
      isLoading: boolean;
      error: Error | null;
      daemonOffline: boolean;
    }
  | { kind: "local" };

export function WorkloadsSection({
  summary,
  source,
  projectName,
}: {
  summary: ForgeEnvSummary;
  source: WorkloadsSource;
  projectName?: string;
}) {
  if (source.kind === "local") {
    return (
      <p data-testid="workloads-local" className="text-sm text-muted-foreground">
        Nothing is deployed for a local environment —{" "}
        <code className="font-mono text-foreground">forge env up</code> runs the working tree on this
        machine. What it launched is under Dev stack.
      </p>
    );
  }

  if (source.kind === "daemon") {
    if (source.daemonOffline) {
      return (
        <p data-testid="workloads-daemon-offline" className="text-sm text-muted-foreground">
          Forge reads this environment&apos;s cluster from your daemon, which is offline — so what is
          running there is not known right now.
        </p>
      );
    }
    return (
      <WorkloadInventory
        outcome={source.outcome}
        isLoading={source.isLoading}
        error={source.error}
        env={summary.name}
        projectName={projectName}
      />
    );
  }

  // Reliant cloud, straight from the control plane.
  if (source.isLoading && !source.status) {
    return (
      <p data-testid="workloads-cloud-loading" className="text-sm text-muted-foreground">
        Asking the control plane what <span className="font-mono">{summary.name}</span> runs…
      </p>
    );
  }
  if (source.error && !source.status) {
    return (
      <p data-testid="workloads-cloud-error" className="text-sm text-muted-foreground">
        The control plane could not be asked what this environment runs, so it is not known right now
        — this is not a statement that nothing is running.{" "}
        <span className="font-mono text-2xs">{source.error.message}</span>
      </p>
    );
  }

  const workloads = source.status?.workloads ?? [];
  return (
    <div className="space-y-2" data-testid="workloads-cloud">
      <p className="text-xs text-muted-foreground">
        As observed by the control plane
        {source.status?.observedAt ? ` at ${formatTimestamp(source.status.observedAt)}` : ""} — no
        daemon involved.
      </p>
      <Card variant="default" size="sm" hover={false} className="border-border">
        {workloads.length > 0 ? (
          <HostedWorkloadList envName={summary.name} workloads={workloads} />
        ) : (
          <p className="text-xs text-muted-foreground" data-testid="workloads-cloud-empty">
            Nothing has been deployed here yet. The control plane reports no workloads — that is not a
            statement that they are healthy.
          </p>
        )}
      </Card>
    </div>
  );
}
