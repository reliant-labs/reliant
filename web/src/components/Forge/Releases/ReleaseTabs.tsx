// Copyright (c) 2025 Reliant Labs

/**
 * The release detail page's tabs (PROPOSAL §4.4):
 *
 *   Changes    compared with the release before it (changeable): the commits
 *              between them, the images whose digest moved, the Kubernetes
 *              objects added / modified / removed, and the deploy note.
 *              DeployService.CompareReleases, which reads only what the ledger
 *              recorded — so a release cut before commit ranges or bundles were
 *              recorded says so instead of showing an empty "no changes".
 *   Rollout    per-workload convergence of its promotion (GetRollout).
 *   Checks     the gates on its promotions: claimed at promote time, and
 *              recorded after.
 *   Artifacts  every artifact with its digest, and the bundle applied.
 *   Health     telemetry before and after its promotion (the release
 *              comparison the Observability area already draws).
 */

import { useMemo, useState, type ReactNode } from "react";
import { ExternalLink } from "lucide-react";

import Mono from "@/components/forge-ui/mono";
import Status, { type StatusTone } from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useObservabilityOrg } from "@/hooks/observability-queries";
import { usePromotionBundle, usePromotionRollout, useReleaseComparison } from "@/hooks/release-queries";
import type {
  ChangeKind,
  ReleaseComparison as Comparison,
  ReleaseCommit,
  ReleaseGate,
  ReleasePromotion,
  ReleaseRecord,
  RolloutPhase,
  WorkloadRollout,
} from "@/services/forge/releases";
import { cn } from "@/lib/utils";

import { DEFAULT_COMPARE_WINDOW, type CompareWindow } from "../Observability/deliveryScope";
import { ReleaseComparison } from "../Observability/ReleaseComparison";
import { Ago, Facts, Note, Panel } from "./releaseParts";
import { shortVersion, type ReleaseEnv } from "./releaseModel";

const TH = "h-9 whitespace-nowrap px-4 text-left text-xs font-medium text-ink-muted";
const TD = "h-10 whitespace-nowrap px-4 text-sm text-ink";
const ROW = "border-t border-border/70 first:border-t-0";

function Table({ label, head, children, testId }: { label: string; head?: ReactNode; children: ReactNode; testId?: string }) {
  return (
    <div className="overflow-x-auto">
      <table className="min-w-full border-collapse" aria-label={label} data-testid={testId}>
        {head && (
          <thead>
            <tr className="border-b border-border">{head}</tr>
          </thead>
        )}
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

function PanelSkeleton({ rows = 3, testId }: { rows?: number; testId?: string }) {
  return (
    <Panel testId={testId}>
      <span className="sr-only">Loading…</span>
      <div aria-hidden="true" className="space-y-3">
        <div className="h-3 w-32 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
        {Array.from({ length: rows }).map((_, i) => (
          <div key={i} className="h-3 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
        ))}
      </div>
    </Panel>
  );
}

function ErrorNote({ what, error, onRetry, testId }: { what: string; error: unknown; onRetry?: () => void; testId?: string }) {
  return (
    <Note tone="danger" testId={testId}>
      {what} could not be read: {(error as Error | null)?.message || "the control plane did not answer"}.
      {onRetry && (
        <>
          {" "}
          <button type="button" onClick={onRetry} className="font-medium text-ink underline-offset-2 hover:underline">
            Try again
          </button>
        </>
      )}
    </Note>
  );
}

/** "In [prod] [dev]": which environment an environment-bound tab reads. */
export function EnvPicker({ envs, value, onChange }: { envs: ReleaseEnv[]; value: string | null; onChange: (env: string) => void }) {
  if (envs.length < 2) return null;
  return (
    <div role="group" aria-label="Environment" className="inline-flex items-center gap-2 text-xs text-ink-muted" data-testid="release-env-picker">
      In
      <span className="inline-flex gap-0.5 rounded-md border border-border bg-surface p-0.5">
        {envs.map((env) => (
          <button
            key={env.name}
            type="button"
            aria-pressed={env.name === value}
            onClick={() => onChange(env.name)}
            className={cn(
              "inline-flex h-6 items-center rounded px-2.5 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent",
              env.name === value ? "bg-ink/8 font-medium text-ink" : "text-ink-muted hover:text-ink"
            )}
          >
            {env.name}
          </button>
        ))}
      </span>
    </div>
  );
}

// ── Changes ─────────────────────────────────────────────────────────────────

const CHANGE_STATUS: Record<ChangeKind, { label: string; tone: StatusTone }> = {
  added: { label: "Added", tone: "success" },
  modified: { label: "Modified", tone: "warning" },
  removed: { label: "Removed", tone: "danger" },
  unknown: { label: "Changed", tone: "neutral" },
};

function CommitRows({ commits }: { commits: ReleaseCommit[] }) {
  return (
    <Table label="Commits" testId="release-commits">
      {commits.map((commit) => (
        <tr key={commit.sha} className={ROW}>
          <td className={cn(TD, "w-24")}>
            <Mono value={commit.sha} />
          </td>
          <td className={cn(TD, "w-full max-w-0")}>
            <div className="truncate" title={commit.subject}>
              {commit.subject || <span className="text-ink-muted">No subject recorded</span>}
            </div>
          </td>
          <td className={cn(TD, "text-ink-muted")}>{commit.author}</td>
          <td className={cn(TD, "text-ink-muted")}>
            <Ago at={commit.time} />
          </td>
        </tr>
      ))}
    </Table>
  );
}

function TruncatedNote({ shown, total }: { shown: number; total: number }) {
  return (
    <div className="mx-4 mb-2">
      <Note tone="warning" testId="release-commits-truncated">
        Range truncated (a release in it was cut from a shallow clone, or its range was capped): showing {shown} of{" "}
        {total > 0 ? total : "an unknown number of"} commits.
      </Note>
    </div>
  );
}

export interface ChangesTabProps {
  project: string;
  release: ReleaseRecord;
  /** The release compared with; null when there is none (the first release). */
  base: string | null;
  /** What `base` is, in words ("the release prod ran before"). */
  baseReason: string;
  baseOptions: ReleaseRecord[];
  onBaseChange: (version: string) => void;
  /** The environment whose bundles the object comparison reads. */
  env: ReleaseEnv | null;
  note: { text: string; by: string } | null;
}

export function ChangesTab({ project, release, base, baseReason, baseOptions, onBaseChange, env, note }: ChangesTabProps) {
  const comparison = useReleaseComparison(project, base, release.version, env?.id ?? null);
  const picker = (
    <div className="flex flex-wrap items-center gap-2 text-sm text-ink-muted" data-testid="release-compare-with">
      {base ? (
        <>
          <span>Compared with</span>
          <label className="sr-only" htmlFor="release-base">
            Release to compare with
          </label>
          <select
            id="release-base"
            value={base}
            onChange={(event) => onBaseChange(event.target.value)}
            className="h-7 max-w-72 rounded-md border border-border bg-surface px-2 text-sm font-medium text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent"
            data-testid="release-base"
          >
            {baseOptions.map((option) => (
              <option key={option.version} value={option.version}>
                {shortVersion(option.version)}
                {option.subject ? ` — ${option.subject.slice(0, 60)}` : ""}
              </option>
            ))}
          </select>
          <span>{baseReason}</span>
        </>
      ) : (
        <span>The first release recorded for {project}: there is nothing earlier to compare it with.</span>
      )}
    </div>
  );

  const noteBlock = (
    <Card transparent testId="release-deploy-note">
      <div className="mb-1 text-xs text-ink-muted">Deploy note{note?.by ? ` · ${note.by}` : ""}</div>
      <div className="text-sm text-ink">{note?.text || <span className="text-ink-muted">No deploy note.</span>}</div>
    </Card>
  );

  if (!base) {
    const own = release.commits;
    return (
      <div className="space-y-4" data-testid="release-changes" data-state="first">
        {picker}
        {own && own.commits.length > 0 && (
          <Panel title="Commits" meta={own.total || own.commits.length} flush testId="release-commits-panel">
            {own.truncated && <TruncatedNote shown={own.commits.length} total={own.total} />}
            <CommitRows commits={own.commits} />
          </Panel>
        )}
        {noteBlock}
      </div>
    );
  }

  return (
    <div className="space-y-4" data-testid="release-changes" data-state={comparison.isLoading ? "loading" : comparison.error ? "error" : "ready"}>
      {picker}
      {comparison.isLoading ? (
        <>
          <PanelSkeleton rows={3} testId="release-changes-loading" />
          <PanelSkeleton rows={2} />
        </>
      ) : comparison.error || !comparison.data ? (
        <ErrorNote what="The comparison" error={comparison.error} onRetry={() => void comparison.refetch()} testId="release-changes-error" />
      ) : (
        <ComparisonPanels comparison={comparison.data} base={base} env={env} />
      )}
      {noteBlock}
    </div>
  );
}

function Card({ transparent, testId, children }: { transparent?: boolean; testId?: string; children: ReactNode }) {
  return (
    <section data-testid={testId} className={cn("rounded-lg border border-border p-4", transparent ? "bg-transparent" : "bg-surface")}>
      {children}
    </section>
  );
}

function ComparisonPanels({ comparison, base, env }: { comparison: Comparison; base: string; env: ReleaseEnv | null }) {
  const { commits, artifacts, objects } = comparison;
  const count = (kind: ChangeKind) => objects.deltas.filter((delta) => delta.change === kind).length;
  return (
    <>
      <Panel
        title={commits.reverts ? "Commits this takes away" : "Commits"}
        meta={commits.state === "computed" || commits.state === "by-cut-time" ? commits.total || commits.commits.length : undefined}
        flush
        testId="release-commits-panel"
      >
        {commits.state === "unrecorded" || commits.state === "unknown" ? (
          <p className="px-4 pb-4 text-sm text-ink-muted" data-testid="release-commits-unrecorded">
            {commits.state === "unrecorded"
              ? `The commits between ${shortVersion(base)} and this release are unknown: nothing cut between them recorded its commits (a forge older than commit ranges cut them).`
              : "This control plane did not say which commits are between the two releases."}
          </p>
        ) : commits.state === "same" ? (
          <p className="px-4 pb-4 text-sm text-ink-muted" data-testid="release-commits-same">
            These are the same release.
          </p>
        ) : (
          <>
            {commits.reverts && (
              <p className="px-4 pb-2 text-xs text-ink-muted">This release is older than {shortVersion(base)}: moving to it removes these commits.</p>
            )}
            {commits.state === "by-cut-time" && (
              <p className="px-4 pb-2 text-xs text-ink-muted" data-testid="release-commits-by-cut-time">
                Approximate: no recorded range joins the two releases, so these are the commits recorded by the {commits.releases.length}{" "}
                {commits.releases.length === 1 ? "release" : "releases"} cut between them.
              </p>
            )}
            {commits.truncated && <TruncatedNote shown={commits.commits.length} total={commits.total} />}
            {commits.commits.length > 0 ? (
              <CommitRows commits={commits.commits} />
            ) : (
              <p className="px-4 pb-4 text-sm text-ink-muted">No commits between them.</p>
            )}
          </>
        )}
      </Panel>

      <Panel
        title="Images"
        meta={`${artifacts.length} changed · ${comparison.unchangedArtifacts} unchanged`}
        flush
        testId="release-images-panel"
      >
        {artifacts.length === 0 ? (
          <p className="px-4 pb-4 text-sm text-ink-muted">No artifact changed: every image and asset is byte-identical.</p>
        ) : (
          <Table label="Changed artifacts">
            {artifacts.map((artifact) => (
              <tr key={artifact.name} className={ROW}>
                <td className={cn(TD, "w-44 font-medium")}>{artifact.name}</td>
                <td className={cn(TD, "w-full max-w-0")}>
                  <span className="flex min-w-0 items-center gap-1.5">
                    {artifact.from ? <Mono value={artifact.from} /> : <span className="text-xs text-ink-muted">new</span>}
                    <span aria-hidden="true" className="text-ink-muted">
                      →
                    </span>
                    <span className="sr-only">to</span>
                    {artifact.to ? <Mono value={artifact.to} /> : <span className="text-xs text-ink-muted">removed</span>}
                  </span>
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Panel>

      <Panel
        title={env ? `Kubernetes objects in ${env.name}` : "Kubernetes objects"}
        meta={objects.state === "compared" ? `${count("added")} added · ${count("modified")} modified · ${count("removed")} removed` : undefined}
        flush
        testId="release-objects-panel"
      >
        {objects.state === "compared" ? (
          <>
            {objects.summariesMissing && (
              <p className="px-4 pb-2 text-xs text-ink-muted">One bundle was rendered by a forge that wrote no per-object summaries, so some objects are named without saying what changed.</p>
            )}
            {objects.deltas.length === 0 ? (
              <p className="px-4 pb-4 text-sm text-ink-muted">No object changed in {env?.name ?? "this environment"}{objects.unchanged ? ` (${objects.unchanged} unchanged)` : ""}.</p>
            ) : (
              <Table label="Changed objects">
                {objects.deltas.map((delta) => (
                  <tr key={`${delta.cluster}/${delta.kind}/${delta.namespace}/${delta.name}`} className={ROW}>
                    <td className={cn(TD, "w-28")}>
                      <Status tone={CHANGE_STATUS[delta.change].tone} label={CHANGE_STATUS[delta.change].label} />
                    </td>
                    <td className={TD}>
                      <span className="text-ink-muted">{delta.kind}</span> <span className="font-medium">{delta.name}</span>
                    </td>
                    <td className={cn(TD, "w-full max-w-0")}>
                      <div className="truncate text-ink-muted" title={delta.summary}>
                        {delta.summary || "—"}
                      </div>
                    </td>
                  </tr>
                ))}
              </Table>
            )}
          </>
        ) : (
          <p className="px-4 pb-4 text-sm text-ink-muted" data-testid="release-objects-unavailable">
            {objects.state === "no-environment"
              ? "Objects are compared in one environment's render, and this release has not been deployed to a long-lived environment yet."
              : objects.state === "bundle-missing"
                ? objects.detail || "One of the two releases has no recorded bundle in this environment, so its objects cannot be compared."
                : "This control plane did not compare the releases' objects."}
          </p>
        )}
      </Panel>
    </>
  );
}

// ── Rollout ─────────────────────────────────────────────────────────────────

const PHASE: Record<RolloutPhase, { label: string; tone: StatusTone; pulse?: boolean }> = {
  pending: { label: "Pending", tone: "neutral", pulse: true },
  progressing: { label: "Rolling out", tone: "neutral", pulse: true },
  stabilizing: { label: "Stabilizing", tone: "neutral", pulse: true },
  succeeded: { label: "Converged", tone: "success" },
  degraded: { label: "Degraded", tone: "danger" },
  superseded: { label: "Superseded", tone: "neutral" },
  unknown: { label: "Cannot be observed", tone: "warning" },
  held: { label: "Queued", tone: "warning" },
};

function PhaseStatus({ phase }: { phase: RolloutPhase }) {
  const p = PHASE[phase];
  return <Status tone={p.tone} label={p.label} pulse={p.pulse} />;
}

export function RolloutTab({ env, promotion }: { env: ReleaseEnv | null; promotion: ReleasePromotion | null }) {
  const rollout = usePromotionRollout(env?.id ?? null, promotion?.id ?? null);
  if (!env || !promotion) {
    return <Note testId="release-rollout-none">This release has not been promoted to a long-lived environment, so there is no rollout to show.</Note>;
  }
  if (rollout.isLoading) return <PanelSkeleton rows={4} testId="release-rollout-loading" />;
  if (rollout.error) return <ErrorNote what={`The rollout in ${env.name}`} error={rollout.error} onRetry={() => void rollout.refetch()} />;
  const data = rollout.data;
  if (!data) return <Note testId="release-rollout-none">The control plane has no rollout record for this promotion.</Note>;
  const workloads: WorkloadRollout[] = [...data.workloads, ...data.unpinned];
  const converged = data.workloads.filter((w) => w.phase === "succeeded").length;
  return (
    <Panel
      title={
        <span className="inline-flex items-center gap-3">
          Rollout in {env.name} <PhaseStatus phase={data.phase} />
        </span>
      }
      meta={`${converged} of ${data.workloads.length} workloads converged`}
      flush
      testId="release-rollout"
    >
      {data.reason && <p className="px-4 pb-2 text-xs text-ink-muted">{data.reason}</p>}
      {promotion.targets.length > 0 && (
        <p className="px-4 pb-2 text-xs text-ink-muted">A scoped deploy: only {promotion.targets.join(", ")} moved.</p>
      )}
      {workloads.length === 0 ? (
        <p className="px-4 pb-4 text-sm text-ink-muted">This promotion pinned no workloads.</p>
      ) : (
        <Table
          label="Workloads"
          head={
            <>
              <th className={TH}>Workload</th>
              <th className={TH}>Status</th>
              <th className={cn(TH, "text-right")}>Replicas</th>
              <th className={TH}>Serving</th>
              <th className={TH}>Converged</th>
            </>
          }
        >
          {workloads.map((workload) => (
            <tr key={workload.name} className={ROW}>
              <td className={cn(TD, "font-medium")}>
                {workload.name}
                {data.unpinned.includes(workload) && <span className="ml-2 text-xs font-normal text-ink-muted">not in this release</span>}
              </td>
              <td className={TD}>
                <span title={workload.lastError || undefined}>
                  <PhaseStatus phase={workload.phase} />
                </span>
              </td>
              <td className={cn(TD, "text-right tabular-nums")}>
                {workload.updatedReplicas}/{workload.desiredReplicas}
              </td>
              <td className={TD}>{workload.observedDigest ? <Mono value={workload.observedDigest} /> : <span className="text-ink-muted">—</span>}</td>
              <td className={cn(TD, "text-ink-muted")}>
                <Ago at={workload.convergedAt} />
              </td>
            </tr>
          ))}
        </Table>
      )}
    </Panel>
  );
}

// ── Checks ──────────────────────────────────────────────────────────────────

const GATE_TONE: Record<ReleaseGate["status"], StatusTone> = { passed: "success", failed: "danger", error: "danger", skipped: "neutral" };

export function ChecksTab({ entries }: { entries: { env: ReleaseEnv; promotion: ReleasePromotion }[] }) {
  const rows = entries.flatMap(({ env, promotion }) => promotion.gates.map((gate) => ({ env, promotion, gate })));
  if (rows.length === 0) {
    return (
      <Note testId="release-checks-none">
        No checks were recorded on this release&apos;s promotions. A check appears here when CI or a reviewer records a gate on a promotion — at
        promote time, or afterwards.
      </Note>
    );
  }
  return (
    <Panel title="Checks" meta={`${rows.length} recorded`} flush testId="release-checks">
      <Table
        label="Checks"
        head={
          <>
            <th className={TH}>Check</th>
            <th className={TH}>Result</th>
            <th className={TH}>Summary</th>
            <th className={TH}>Environment</th>
            <th className={TH}>Recorded</th>
            <th className={cn(TH, "w-10")}>
              <span className="sr-only">Report</span>
            </th>
          </>
        }
      >
        {rows.map(({ env, promotion, gate }, index) => (
          <tr key={`${promotion.id}-${gate.name}-${index}`} className={ROW}>
            <td className={cn(TD, "font-medium")}>{gate.name}</td>
            <td className={TD}>
              <Status tone={GATE_TONE[gate.status]} label={gate.rawStatus ? `${gate.status} (${gate.rawStatus})` : gate.status} />
            </td>
            <td className={cn(TD, "w-full max-w-0")}>
              <div className="truncate text-ink-muted" title={gate.summary}>
                {gate.summary || "—"}
              </div>
            </td>
            <td className={TD}>
              <Tag>{env.name}</Tag>
            </td>
            <td className={cn(TD, "text-ink-muted")}>
              <span title={gate.source === "claimed" ? "Claimed by the promoter when they promoted" : "Recorded after the promotion"}>
                {gate.source === "claimed" ? "at promote" : "after"}
              </span>
              {gate.finishedAt && " · "}
              <Ago at={gate.finishedAt} />
            </td>
            <td className="h-10 px-2">
              {gate.url && (
                <a
                  href={gate.url}
                  target="_blank"
                  rel="noreferrer"
                  aria-label={`Open the ${gate.name} report`}
                  className="inline-flex h-7 w-7 items-center justify-center rounded-md text-ink-muted hover:bg-ink/5 hover:text-ink"
                >
                  <ExternalLink className="h-3.5 w-3.5" aria-hidden="true" />
                </a>
              )}
            </td>
          </tr>
        ))}
      </Table>
    </Panel>
  );
}

// ── Artifacts ───────────────────────────────────────────────────────────────

export function ArtifactsTab({ release, env, promotion }: { release: ReleaseRecord; env: ReleaseEnv | null; promotion: ReleasePromotion | null }) {
  const bundle = usePromotionBundle(env?.id ?? null, promotion?.bundleId ?? "", promotion?.bundleDigest ?? "");
  return (
    <div className="space-y-4" data-testid="release-artifacts">
      <Panel title="Artifacts" meta={release.artifacts.length} flush>
        {release.artifacts.length === 0 ? (
          <p className="px-4 pb-4 text-sm text-ink-muted">This release recorded no artifacts.</p>
        ) : (
          <Table
            label="Artifacts"
            head={
              <>
                <th className={TH}>Name</th>
                <th className={TH}>Kind</th>
                <th className={TH}>Digest</th>
                <th className={TH}>Platforms</th>
              </>
            }
          >
            {release.artifacts.map((artifact) => (
              <tr key={`${artifact.name}${artifact.variant}`} className={ROW}>
                <td className={cn(TD, "font-medium")}>
                  {artifact.name}
                  {artifact.variant && <span className="ml-1 text-ink-muted">[{artifact.variant}]</span>}
                </td>
                <td className={cn(TD, "text-ink-muted")}>{artifact.kind || "—"}</td>
                <td className={TD}>{artifact.digest ? <Mono value={artifact.digest} /> : artifact.version || <span className="text-ink-muted">—</span>}</td>
                <td className={cn(TD, "text-ink-muted")}>{artifact.platforms.join(", ") || "—"}</td>
              </tr>
            ))}
          </Table>
        )}
      </Panel>
      <Panel title={env ? `Bundle applied in ${env.name}` : "Bundle"} testId="release-bundle">
        {!env || !promotion ? (
          <p className="text-sm text-ink-muted">A bundle is one environment&apos;s render of the release; this release has not been promoted to a long-lived one.</p>
        ) : bundle.isLoading ? (
          <div className="h-16 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-label="Loading" />
        ) : bundle.error || !bundle.data ? (
          <Facts items={[["Digest", promotion.bundleDigest ? <Mono value={promotion.bundleDigest} /> : <span className="text-ink-muted">not recorded</span>]]} />
        ) : (
          <Facts
            items={[
              ["Digest", <Mono key="d" value={bundle.data.digest} />],
              ["Reference", <Mono key="r" value={bundle.data.reference} length={48} />],
              ["Config", bundle.data.configDigest ? <Mono key="c" value={bundle.data.configDigest} /> : "—"],
              ["Snapshot", bundle.data.snapshotDigest ? <Mono key="s" value={bundle.data.snapshotDigest} /> : "—"],
              ["Recorded", <Ago key="a" at={bundle.data.createdAt} />],
            ]}
          />
        )}
      </Panel>
    </div>
  );
}

// ── Health ──────────────────────────────────────────────────────────────────

export function HealthTab({ project, env, promotion }: { project: string; env: ReleaseEnv | null; promotion: ReleasePromotion | null }) {
  const org = useObservabilityOrg();
  const [width, setWidth] = useState<CompareWindow>(DEFAULT_COMPARE_WINDOW);
  const scope = useMemo(() => (org.orgId ? { orgId: org.orgId, projectId: project, envIds: env ? [env.id] : [], filters: [] } : null), [org.orgId, project, env]);
  if (!env || !promotion?.createdAt) {
    return <Note testId="release-health-none">Health compares telemetry before and after a promotion, and this release has not been promoted to a long-lived environment.</Note>;
  }
  if (org.isLoading) return <PanelSkeleton rows={4} testId="release-health-loading" />;
  if (!scope) return <Note tone="warning">Telemetry is not set up for this organization yet, so there is no before and after to compare.</Note>;
  return (
    <div data-testid="release-health">
      <ReleaseComparison
        scope={scope}
        subject={env.name}
        target={{ envId: env.id, promotedAtMs: Date.parse(promotion.createdAt), promotionId: promotion.id }}
        width={width}
        navigate={(next) => {
          if ("cmpWindow" in next) setWidth((next.cmpWindow as CompareWindow | undefined) ?? DEFAULT_COMPARE_WINDOW);
        }}
      />
    </div>
  );
}
