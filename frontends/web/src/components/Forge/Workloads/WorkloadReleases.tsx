// Copyright (c) 2025 Reliant Labs

/**
 * The workload History tab's Releases panel: the environment's promotions
 * that moved this workload, newest first — who, when, the checks' verdict,
 * rollbacks marked — each with this workload's image digest and what changed
 * in its objects since the release it replaced (CompareReleases, filtered to
 * the workload). The full promotion record stays with each release; every row
 * links there.
 */

import Mono from "@/components/forge-ui/mono";
import Tag from "@/components/forge-ui/tag";
import Status from "@/components/forge-ui/status";
import { usePromotionBundle, usePromotionRollout, useWorkloadReleaseComparison } from "@/hooks/release-queries";

import { PromotionItem } from "../Releases/PromotionItem";
import { Panel } from "../Releases/releaseParts";
import { shortVersion } from "../Releases/releaseModel";
import { workloadChange, workloadDigestOf, type WorkloadChange, type WorkloadReleaseRow } from "./workloadModel";

/** How many promotions the panel lists, each with one comparison read. */
export const RELEASES_SHOWN = 10;
/** How many changed objects one row names before it counts the rest. */
const CHANGE_LINES = 4;

export interface WorkloadReleasesProps {
  project: string;
  env: { id: string; name: string };
  workload: string;
  rows: WorkloadReleaseRow[];
  loading: boolean;
  failed: boolean;
  releaseHref: (version: string) => string;
}

export function WorkloadReleases({ project, env, workload, rows, loading, failed, releaseHref }: WorkloadReleasesProps) {
  const shown = rows.slice(0, RELEASES_SHOWN);
  return (
    <Panel title="Releases" meta={`Promotions in ${env.name} that moved ${workload}, newest first`} testId="workload-history-releases">
      {loading ? (
        <p className="text-xs text-ink-muted" aria-busy="true">
          Loading {env.name}&apos;s promotions…
        </p>
      ) : failed ? (
        <p className="text-xs text-ink-muted">{env.name}&apos;s promotions could not be read right now.</p>
      ) : shown.length === 0 ? (
        <p className="text-xs text-ink-muted">No promotion in the loaded history moved {workload} in {env.name}.</p>
      ) : (
        <>
          <ol className="divide-y divide-border" aria-label={`Releases of ${workload} in ${env.name}`}>
            {shown.map((row) => (
              <ReleaseRow key={row.id} project={project} env={env} workload={workload} row={row} releaseHref={releaseHref} />
            ))}
          </ol>
          {rows.length > shown.length && (
            <p className="pt-3 text-xs text-ink-muted" data-testid="workload-releases-more">
              The {shown.length} newest of {rows.length} loaded promotions. Each release keeps its full promotion record.
            </p>
          )}
        </>
      )}
    </Panel>
  );
}

function ReleaseRow({
  project,
  env,
  workload,
  row,
  releaseHref,
}: {
  project: string;
  env: { id: string; name: string };
  workload: string;
  row: WorkloadReleaseRow;
  releaseHref: (version: string) => string;
}) {
  const comparison = useWorkloadReleaseComparison(project, row.previousVersion, row.version, env.id, workload);
  const change = workloadChange(row, comparison.data, workload);
  const bundle = usePromotionBundle(env.id, row.bundleId, row.bundleDigest);
  const rollout = usePromotionRollout(env.id, row.id);
  const observed = rollout.data?.workloads.find((item) => item.name === workload);
  const images = bundle.data?.workloadImages?.[workload];
  const pinned = images?.[workload] ?? (images ? Object.entries(images).sort(([a], [b]) => a.localeCompare(b))[0]?.[1] : undefined);
  const digest = pinned || observed?.pinnedDigest || workloadDigestOf(row, change, workload);
  const verdict = observed?.verdict ?? "unknown";
  return (
    <PromotionItem
      testId="workload-release-row"
      version={row.version}
      href={releaseHref(row.version)}
      rollback={row.rollback}
      marks={
        <>
          {row.rollback && <Tag tone="warning">rollback</Tag>}
          {row.scoped && <Tag>scoped</Tag>}
        </>
      }
      by={row.by}
      at={row.at}
      checks={row.checks}
      health={<Status tone={verdict === "converged" ? "success" : verdict === "degraded" || verdict === "diverged" ? "danger" : "neutral"} label={`health: ${verdict}`} />}
      digest={<>image {digest ? <Mono value={digest} /> : "not recorded"}</>}
    >
      <ChangeLine change={change} loading={comparison.isLoading} failed={!!comparison.error} previous={row.previousVersion} version={row.version} workload={workload} />
    </PromotionItem>
  );
}

function ChangeLine({
  change,
  loading,
  failed,
  previous,
  version,
  workload,
}: {
  change: WorkloadChange | null;
  loading: boolean;
  failed: boolean;
  previous: string | null;
  version: string;
  workload: string;
}) {
  const muted = (text: string) => (
    <p className="text-ink-muted" data-testid="workload-release-change">
      {text}
    </p>
  );
  if (!change) {
    if (failed) return muted("What changed could not be read right now.");
    return loading && previous ? muted(`Comparing with ${shortVersion(previous)}…`) : null;
  }
  switch (change.kind) {
    case "first":
      return muted(`No earlier promotion of ${workload} is loaded to compare.`);
    case "same-release":
      return muted(`${shortVersion(version)} again: the release did not change.`);
    case "not-compared":
      return muted(change.detail || `What changed from ${previous ? shortVersion(previous) : "the release before"} was not recorded.`);
    case "unchanged":
      return muted(`Nothing in ${workload} changed from ${previous ? shortVersion(previous) : "the release before"}.`);
    case "changed": {
      const extra = change.lines.length - CHANGE_LINES;
      return (
        <ul className="space-y-0.5 text-ink" data-testid="workload-release-change" aria-label={`What changed in ${workload}`}>
          {change.lines.slice(0, CHANGE_LINES).map((line) => (
            <li key={line} className="break-words">
              {line}
            </li>
          ))}
          {extra > 0 && <li className="text-ink-muted">and {extra} more</li>}
        </ul>
      );
    }
  }
}
