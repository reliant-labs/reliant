// Copyright (c) 2025 Reliant Labs

/**
 * WHAT IS RUNNING — or, for an environment the platform does not place, what
 * the last render said SHOULD run.
 *
 * Two sources, and the distinction between them is load-bearing:
 *
 *   PLACED (persistent, preview)  the platform runs the workloads, so
 *                                 GetStatus is an OBSERVATION: verdicts,
 *                                 digests, drift, URLs. A database read on the
 *                                 control plane, with no cluster hop and no
 *                                 daemon.
 *   NOT PLACED (self_managed)     forge applies it to the user's own cluster.
 *                                 The control plane has no observer there, so
 *                                 the honest answer is the DECLARED shape —
 *                                 what the last recorded render said the env
 *                                 contains — and the section says that is what
 *                                 it is showing.
 *
 * Collapsing the two would be the worst available outcome: a declared workload
 * rendered in the same cells as an observed one claims a convergence nobody
 * measured. "Unknown is not in_sync" is the same rule the drift model follows.
 *
 * A local env runs on a developer machine and is not deployed anywhere, so
 * there is nothing to report here at all — the Preview tab's dev stack is
 * where that lives.
 */

import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import { isPlacedKind, type LiveEnv } from "@/services/forge/live";

import { HostedWorkloadList } from "../HostedWorkloads";

const TH = "px-3 py-2 text-left text-2xs font-medium uppercase tracking-wide text-muted-foreground";

export function LiveWorkloads({
  env,
  status,
  isLoading,
  error,
}: {
  env: LiveEnv;
  status: CloudEnvStatus | undefined;
  isLoading: boolean;
  error: Error | null;
}) {
  if (env.kind === "local") {
    return (
      <p data-testid="live-workloads-local" className="text-sm text-muted-foreground">
        A local environment runs on a developer machine via{" "}
        <code className="font-mono text-foreground">forge env up</code> — nothing is deployed
        anywhere.
      </p>
    );
  }

  // ── The platform places it: show what it observes. ──
  if (isPlacedKind(env.kind)) {
    if (isLoading && !status) {
      return (
        <p data-testid="live-workloads-loading" className="text-sm text-muted-foreground">
          Loading what&apos;s running…
        </p>
      );
    }
    if (error && !status) {
      return (
        <p data-testid="live-workloads-error" className="text-sm text-muted-foreground">
          Couldn&apos;t load what&apos;s running in {env.name}. Try again.{" "}
          <span className="font-mono text-2xs">{error.message}</span>
        </p>
      );
    }
    const workloads = status?.workloads ?? [];
    if (workloads.length === 0) {
      return (
        <p data-testid="live-workloads-empty" className="text-sm text-muted-foreground">
          Nothing is deployed here yet.
        </p>
      );
    }
    return (
      <div data-testid="live-workloads-observed">
        <HostedWorkloadList workloads={workloads} envName={env.name} runControls />
      </div>
    );
  }

  // ── forge applies it: show what was DECLARED, labelled as such. ──
  const declared = env.declaredShape?.workloads ?? [];
  if (declared.length === 0) {
    return (
      <p data-testid="live-workloads-undeclared" className="text-sm text-muted-foreground">
        No workloads have been recorded for this environment yet.
      </p>
    );
  }

  return (
    <div className="space-y-2" data-testid="live-workloads-declared">
      <div className="overflow-hidden rounded-lg border border-border bg-card">
        <table className="w-full border-collapse text-xs">
          <caption className="sr-only">
            The workloads {env.name}&apos;s last recorded render declares.
          </caption>
          <thead>
            <tr className="border-b border-border">
              <th scope="col" className={TH}>
                Workload
              </th>
              <th scope="col" className={TH}>
                Runtime
              </th>
              <th scope="col" className={TH}>
                Cluster
              </th>
            </tr>
          </thead>
          <tbody>
            {declared.map((workload) => (
              <tr key={workload.name} className="border-b border-border/60 last:border-0">
                <th scope="row" className="px-3 py-2 text-left font-mono font-normal text-foreground">
                  {workload.name}
                </th>
                <td className="px-3 py-2 text-muted-foreground">{workload.runtime || "—"}</td>
                <td className="px-3 py-2 font-mono text-muted-foreground">{workload.cluster || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {/* Said explicitly, because the alternative is a table of workloads a
          reader would reasonably take for a live observation. */}
      <p className="text-xs text-muted-foreground">
        Declared by the last recorded render — not an observation of your cluster.
      </p>
    </div>
  );
}
