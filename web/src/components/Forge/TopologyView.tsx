// Copyright (c) 2025 Reliant Labs

/**
 * The topology screen's presentation layer.
 *
 * PURE PROPS BY DESIGN. It takes an outcome and callbacks and owns no data
 * fetching, mirroring the split the repo already uses between
 * services/controlPlane/environments.ts and Settings/cloud/machines.tsx.
 * That is what lets the visual contract be tested by handing it a report object
 * rather than by standing up a query client and a transport.
 *
 * Layout: an env-per-row matrix with an image per column, because the questions
 * an operator brings to this screen are per-environment ("what is prod running,
 * and is it behind?") while images are the dimension being compared across them.
 * The env column is sticky so the row identity survives horizontal scrolling on a
 * project with many images.
 *
 * It is a REAL table: every fact an env carries — release, lag, promote time,
 * where it runs (a cluster, or for a hosted env its control plane) — gets its own `<td>` under its own `<th scope="col">`, and the
 * actions sit in a right-aligned column of their own. One row is one line.
 * (It previously stacked all of those into the row header's flex column, which
 * produced ~200px rows next to image cells on a single baseline.)
 */

import { cn } from "@/lib/utils";
import type { ForgeOutcome, ForgeTopologyReport } from "@/services/forge/topology";
import { environments, imageNames } from "@/services/forge/topology";

import { CertaintyLegend } from "./CertaintyLegend";
import { EnvRow } from "./EnvRow";
import {
  ForgeMalformed,
  ForgeUnreachable,
  ForgeUnsupported,
  NotForgeProject,
} from "./ForgeStates";

export interface TopologyViewProps {
  outcome: ForgeOutcome<ForgeTopologyReport> | undefined;
  isLoading: boolean;
  /** A transport/daemon failure — genuinely an error, unlike every outcome above. */
  error?: Error | null;
  onVerify: (env: string) => void;
  /** The env whose verify is in flight, so one row shows pending, not the table. */
  verifyingEnv?: string | null;
  /** Per-env notice when a verify returned something other than a report. */
  verifyNotices?: Record<string, string>;
  projectName?: string;
  /**
   * Enables the per-row promote entry point. Absent means no promote is offered
   * — a promote cannot be planned without knowing which project to plan against.
   */
  projectId?: string | null;
  /** Navigate to this env's status screen. Absent = rows are not links. */
  onOpenEnv?: (env: string) => void;
}

/**
 * Every column header, so the row of headers is one consistent band. Uppercase
 * + tracking is the standard table-header treatment in this app; the image
 * headers override `normal-case` because an image name is an identifier and
 * case-folding one is a lie about what it is called.
 */
const HEADER_CELL =
  "whitespace-nowrap px-3 py-2 text-left text-xs font-medium uppercase tracking-wide text-muted-foreground";

export function TopologyView({
  outcome,
  isLoading,
  error,
  onVerify,
  verifyingEnv,
  verifyNotices,
  projectName,
  projectId,
  onOpenEnv,
}: TopologyViewProps) {
  if (isLoading && !outcome) {
    return (
      <div
        data-testid="forge-topology-loading"
        className="mx-auto max-w-lg rounded-lg border border-dashed border-border bg-card px-6 py-12 text-center text-sm text-muted-foreground"
      >
        Reading the release ledger…
      </div>
    );
  }

  if (error && !outcome) {
    return (
      <div
        data-testid="forge-topology-error"
        className="mx-auto max-w-lg rounded-lg border border-destructive/40 bg-destructive/10 px-6 py-12 text-center text-sm text-destructive"
      >
        Could not reach your daemon to read the forge project: {error.message}
      </div>
    );
  }

  if (!outcome) return null;

  // Each of these is a successful RPC with a different meaning. See ForgeStates.
  if (outcome.kind === "not-forge-project") return <NotForgeProject projectName={projectName} />;
  if (outcome.kind === "unsupported") return <ForgeUnsupported meta={outcome.meta} />;
  if (outcome.kind === "unreachable") return <ForgeUnreachable meta={outcome.meta} />;
  if (outcome.kind === "malformed") return <ForgeMalformed meta={outcome.meta} />;

  const report = outcome.report;
  const envs = environments(report);
  const images = imageNames(report);

  return (
    <div className="space-y-5" data-testid="forge-topology">
      <header className="space-y-1">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          {/* The heading is prose, so it is NOT monospace. The project name
              inside it is an identifier, so that span is. */}
          <h1 className="text-base font-semibold text-foreground">
            Release topology
            <span className="ml-2 font-mono text-sm font-normal text-muted-foreground">
              {report.project || projectName || ""}
            </span>
          </h1>
          {report.latest_release && (
            <span className="text-xs text-muted-foreground">
              latest release{" "}
              <span className="font-mono text-foreground">{report.latest_release}</span>
            </span>
          )}
          {typeof report.releases?.length === "number" && (
            <span className="text-xs text-muted-foreground">
              {report.releases.length} release{report.releases.length === 1 ? "" : "s"} in this
              checkout
            </span>
          )}
        </div>
        {/* The promote/deploy caveat, stated once at the top as well as per row.
            It is the single most misread fact on this screen. */}
        <p className="text-xs text-muted-foreground">
          Timestamps below are <span className="text-foreground">promote</span> times, not deploy
          times — promotion writes a pointer, deployment moves bytes. Verify an environment to learn
          what its cluster is actually running.
        </p>
      </header>

      <CertaintyLegend report={report} />

      {envs.length === 0 ? (
        <div
          data-testid="forge-topology-no-envs"
          className="rounded-lg border border-dashed border-border bg-card px-6 py-10 text-center text-sm text-muted-foreground"
        >
          This forge project declares no environments yet.
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border border-border bg-card">
          <table className="w-full border-collapse text-sm">
            <caption className="sr-only">
              Environments by image. A middot means the image is not in that environment&apos;s
              release.
            </caption>
            <thead>
              <tr className="border-b border-border bg-card">
                {/* Sticky like the row header beneath it, and opaque for the
                    same reason: the image columns scroll under it. */}
                <th scope="col" className={cn(HEADER_CELL, "sticky left-0 z-20 bg-card")}>
                  Environment
                </th>
                <th scope="col" className={HEADER_CELL}>
                  Release
                </th>
                <th scope="col" className={HEADER_CELL}>
                  Status
                </th>
                {/* "Promoted", never "deployed" — promotion writes a pointer,
                    deployment moves bytes. The column header is one of the three
                    places that caveat survives; see EnvRow. */}
                <th scope="col" className={HEADER_CELL}>
                  Promoted
                </th>
                {/* "Runs on", not "Cluster": a hosted env has no cluster to
                    name, and its cell shows the control plane instead. */}
                <th scope="col" className={HEADER_CELL}>
                  Runs on
                </th>
                {images.map((image) => (
                  <th key={image} scope="col" className={HEADER_CELL}>
                    <span className="font-mono normal-case">{image}</span>
                  </th>
                ))}
                <th scope="col" className={cn(HEADER_CELL, "text-right")}>
                  Actions
                </th>
              </tr>
            </thead>
            <tbody>
              {envs.map((env) => (
                <EnvRow
                  key={env.env}
                  env={env}
                  images={images}
                  onVerify={onVerify}
                  isVerifying={verifyingEnv === env.env}
                  verifyNotice={verifyNotices?.[env.env] ?? null}
                  projectId={projectId}
                  // The project's latest release is the promote target. Null
                  // when the report names none, which withdraws the entry point
                  // rather than letting a row invent a target.
                  promoteRelease={report.latest_release ?? null}
                  onOpenEnv={onOpenEnv}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      <p className="text-xs text-muted-foreground">
        <span aria-hidden="true">·</span> means the image is not in that environment&apos;s release —
        it is not a missing or unchecked image.
      </p>
    </div>
  );
}
