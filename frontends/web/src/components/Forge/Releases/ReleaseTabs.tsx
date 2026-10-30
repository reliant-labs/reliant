// Copyright (c) 2025 Reliant Labs

/**
 * The release detail page's tabs:
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

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ChevronDown, ExternalLink } from "lucide-react";

import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Mono from "@/components/forge-ui/mono";
import Status, { type StatusTone } from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useObservabilityOrg } from "@/hooks/observability-queries";
import { useEscapeLayer } from "@/hooks/useEscapeLayer";
import { usePromotionBundle, usePromotionRollout, useReleaseComparison } from "@/hooks/release-queries";
import type {
  ArtifactChange,
  ChangeKind,
  ForgeAppliedSlot,
  ObjectDelta,
  ReleaseArtifact,
  ReleaseComparison as Comparison,
  ReleaseCommit,
  ReleaseGate,
  ReleasePromotion,
  ReleaseRecord,
  RolloutPhase,
  WorkloadRollout,
} from "@/services/forge/releases";
import { DEFAULT_HEALTH_WINDOW, type HealthWindow } from "@/services/controlPlane/observability/deliveryTypes";
import { cn } from "@/lib/utils";

import { consolePath, withSearch } from "../Console/paths";
import { ReleaseComparison } from "../Observability/ReleaseComparison";
import { Ago, Facts, INK_BUTTON, Note, Panel } from "./releaseParts";
import { shortVersion, type ReleaseEnv } from "./releaseModel";

/**
 * A flush forge-ui DataTable inside a Panel, with a test id on its frame. Rows
 * are spread into plain objects because DataTable's rows are records.
 */
function Table<T extends object>({
  label,
  columns,
  rows,
  rowKey,
  testId,
}: {
  label: string;
  columns: DataTableColumn<T & Record<string, unknown>>[];
  rows: T[];
  rowKey: (row: T) => string;
  testId?: string;
}) {
  return (
    <div data-testid={testId}>
      <DataTable
        flush
        ariaLabel={label}
        columns={columns}
        data={rows.map((row) => ({ ...row }) as T & Record<string, unknown>)}
        getRowKey={(row) => rowKey(row)}
      />
    </div>
  );
}

/** A cell whose text truncates in a column that takes the remaining width. */
const GROW = "w-full max-w-0";
const truncated = (text: ReactNode, title?: string) => (
  <div className="truncate" title={title}>
    {text}
  </div>
);

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
          <button type="button" onClick={onRetry} className={`font-medium ${INK_BUTTON}`}>
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
    <Table<ReleaseCommit>
      label="Commits"
      testId="release-commits"
      rows={commits}
      rowKey={(commit) => commit.sha}
      columns={[
        { key: "sha", header: "Commit", width: "6rem", render: (_, commit) => <Mono value={commit.sha} /> },
        {
          key: "subject",
          header: "Subject",
          className: GROW,
          render: (_, commit) => truncated(commit.subject || <span className="text-ink-muted">No subject recorded</span>, commit.subject),
        },
        { key: "author", header: "Author", className: "text-ink-muted" },
        { key: "time", header: "When", className: "text-ink-muted", render: (_, commit) => <Ago at={commit.time} /> },
      ]}
    />
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
  const picker = base ? (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-surface px-3 py-2 text-xs text-ink-muted" data-testid="release-compare-with">
      <span>Compared with</span>
      <BaseMenu value={base} options={baseOptions} onChange={onBaseChange} />
      <span>{baseReason}</span>
    </div>
  ) : (
    <div className="rounded-lg border border-border bg-surface px-3 py-2 text-xs text-ink-muted" data-testid="release-compare-with">
      The first release recorded for {project}: there is nothing earlier to compare it with.
    </div>
  );

  const noteBlock = (
    <NotePanel testId="release-deploy-note">
      <div className="mb-1 text-xs text-ink-muted">Deploy note{note?.by ? ` · ${note.by}` : ""}</div>
      <div className="text-sm text-ink">{note?.text || <span className="text-ink-muted">No deploy note.</span>}</div>
    </NotePanel>
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

/** The deploy note: demoted to an unfilled, bordered panel. */
function NotePanel({ testId, children }: { testId?: string; children: ReactNode }) {
  return (
    <section data-testid={testId} className="rounded-lg border border-border bg-transparent p-4">
      {children}
    </section>
  );
}

/**
 * The release Changes compares with: a button naming it, opening the earlier
 * releases (the mockup's note-panel control). The DROPDOWN_STANDARDS
 * outside-click pattern; Escape closes it.
 */
function BaseMenu({ value, options, onChange }: { value: string; options: ReleaseRecord[]; onChange: (version: string) => void }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement | null>(null);
  const escapeLayer = useEscapeLayer(open, () => setOpen(false));
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: MouseEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onPointerDown);
    return () => document.removeEventListener("mousedown", onPointerDown);
  }, [open]);
  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Compare with ${value}`}
        onClick={() => setOpen((prev) => !prev)}
        className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-surface px-2 text-xs font-medium text-ink transition-colors hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
        data-testid="release-base"
      >
        {shortVersion(value)}
        <ChevronDown className="h-3 w-3 text-ink-muted" aria-hidden="true" />
      </button>
      {open && (
        <div
          role="menu"
          {...escapeLayer}
          className="absolute left-0 top-8 z-50 max-h-80 w-96 max-w-[80vw] overflow-y-auto rounded-lg border border-border bg-surface p-1 shadow-md"
        >
          {options.map((option) => (
            <button
              key={option.version}
              type="button"
              role="menuitemradio"
              aria-checked={option.version === value}
              onClick={() => {
                setOpen(false);
                onChange(option.version);
              }}
              className={cn(
                "flex w-full items-baseline gap-2 rounded-md px-2 py-1.5 text-left text-sm text-ink transition-colors hover:bg-ink/5",
                option.version === value && "font-medium"
              )}
            >
              <span className="shrink-0">{shortVersion(option.version)}</span>
              <span className="truncate text-xs text-ink-muted">{option.subject}</span>
            </button>
          ))}
        </div>
      )}
    </div>
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
          <Table<ArtifactChange>
            label="Changed artifacts"
            rows={artifacts}
            rowKey={(artifact) => artifact.name}
            columns={[
              { key: "name", header: "Artifact", width: "11rem", className: "font-medium" },
              {
                key: "change",
                header: "Digest",
                className: GROW,
                render: (_, artifact) => (
                  <span className="flex min-w-0 items-center gap-1.5">
                    {artifact.from ? <Mono value={artifact.from} /> : <span className="text-xs text-ink-muted">new</span>}
                    <span aria-hidden="true" className="text-ink-muted">
                      →
                    </span>
                    <span className="sr-only">to</span>
                    {artifact.to ? <Mono value={artifact.to} /> : <span className="text-xs text-ink-muted">removed</span>}
                  </span>
                ),
              },
            ]}
          />
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
              <Table<ObjectDelta>
                label="Changed objects"
                rows={objects.deltas}
                rowKey={(delta) => `${delta.cluster}/${delta.kind}/${delta.namespace}/${delta.name}`}
                columns={[
                  {
                    key: "change",
                    header: "Change",
                    width: "7rem",
                    render: (_, delta) => <Status tone={CHANGE_STATUS[delta.change].tone} label={CHANGE_STATUS[delta.change].label} />,
                  },
                  {
                    key: "name",
                    header: "Object",
                    render: (_, delta) => (
                      <>
                        <span className="text-ink-muted">{delta.kind}</span> <span className="font-medium">{delta.name}</span>
                      </>
                    ),
                  },
                  {
                    key: "summary",
                    header: "What changed",
                    className: `${GROW} text-ink-muted`,
                    render: (_, delta) => truncated(delta.summary || "—", delta.summary),
                  },
                ]}
              />
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
  const forgeApplied = data.forgeApplied;
  return (
    <Panel
      title={
        <span className="inline-flex items-center gap-3">
          Rollout in {env.name} <PhaseStatus phase={data.phase} />
        </span>
      }
      meta={
        `${converged} of ${data.workloads.length} workloads converged` +
        (forgeApplied.length > 0 ? ` · ${forgeApplied.length} applied by forge` : "")
      }
      flush
      testId="release-rollout"
    >
      {data.reason && <p className="px-4 pb-2 text-xs text-ink-muted">{data.reason}</p>}
      {promotion.targets.length > 0 && (
        <p className="px-4 pb-2 text-xs text-ink-muted">A scoped deploy: only {promotion.targets.join(", ")} moved.</p>
      )}
      {workloads.length === 0 ? (
        forgeApplied.length === 0 && <p className="px-4 pb-4 text-sm text-ink-muted">This promotion pinned no workloads.</p>
      ) : (
        <Table<WorkloadRollout>
          label="Workloads"
          rows={workloads}
          rowKey={(workload) => workload.name}
          columns={[
            {
              key: "name",
              header: "Workload",
              className: "font-medium",
              render: (_, workload) => (
                <>
                  {workload.name}
                  {data.unpinned.some((w) => w.name === workload.name) && (
                    <span className="ml-2 text-xs font-normal text-ink-muted">not in this release</span>
                  )}
                </>
              ),
            },
            {
              key: "phase",
              header: "Status",
              render: (_, workload) => (
                <span title={workload.lastError || workload.reason || undefined}>
                  <PhaseStatus phase={workload.phase} />
                </span>
              ),
            },
            { key: "replicas", header: "Replicas", numeric: true, render: (_, w) => `${w.updatedReplicas}/${w.desiredReplicas}` },
            {
              key: "observedDigest",
              header: "Serving",
              render: (_, w) => (w.observedDigest ? <Mono value={w.observedDigest} /> : <span className="text-ink-muted">—</span>),
            },
            { key: "convergedAt", header: "Converged", className: "text-ink-muted", render: (_, w) => <Ago at={w.convergedAt} /> },
          ]}
        />
      )}
      {forgeApplied.length > 0 && <ForgeAppliedGroup slots={forgeApplied} />}
    </Panel>
  );
}

/**
 * The slots forge applies itself, apart from the judged workloads: the control
 * plane does not observe them, so each row says so in its own reason instead
 * of a phase badge (their wire phase is UNSPECIFIED, which is not "Cannot be
 * observed").
 */
function ForgeAppliedGroup({ slots }: { slots: ForgeAppliedSlot[] }) {
  return (
    <section aria-labelledby="release-rollout-forge-applied-label" data-testid="release-rollout-forge-applied">
      <p id="release-rollout-forge-applied-label" className="px-4 pb-2 pt-3 text-xs text-ink-muted">
        Applied by forge
      </p>
      <Table<ForgeAppliedSlot>
        label="Applied by forge"
        rows={slots}
        rowKey={(slot) => slot.name}
        columns={[
          { key: "name", header: "Workload", className: "font-medium", render: (_, slot) => slot.name },
          {
            key: "reason",
            header: "Status",
            className: `${GROW} text-ink-muted`,
            render: (_, slot) => truncated(slot.reason || "Applied by forge (not observed by the control plane)", slot.reason || undefined),
          },
          {
            key: "pinnedDigest",
            header: "Pinned",
            render: (_, slot) => (slot.pinnedDigest ? <Mono value={slot.pinnedDigest} /> : <span className="text-ink-muted">—</span>),
          },
        ]}
      />
    </section>
  );
}

// ── Checks ──────────────────────────────────────────────────────────────────

const GATE_TONE: Record<ReleaseGate["status"], StatusTone> = { passed: "success", failed: "danger", error: "danger", skipped: "neutral" };

interface CheckRow {
  key: string;
  env: ReleaseEnv;
  promotion: ReleasePromotion;
  gate: ReleaseGate;
}

export function ChecksTab({ entries }: { entries: { env: ReleaseEnv; promotion: ReleasePromotion }[] }) {
  const rows: CheckRow[] = entries.flatMap(({ env, promotion }) =>
    promotion.gates.map((gate, index) => ({ key: `${promotion.id}-${gate.name}-${index}`, env, promotion, gate }))
  );
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
      <Table<CheckRow>
        label="Checks"
        rows={rows}
        rowKey={(row) => row.key}
        columns={[
          { key: "name", header: "Check", className: "font-medium", render: (_, { gate }) => gate.name },
          {
            key: "result",
            header: "Result",
            render: (_, { gate }) => <Status tone={GATE_TONE[gate.status]} label={gate.rawStatus ? `${gate.status} (${gate.rawStatus})` : gate.status} />,
          },
          { key: "summary", header: "Summary", className: `${GROW} text-ink-muted`, render: (_, { gate }) => truncated(gate.summary || "—", gate.summary) },
          { key: "env", header: "Environment", render: (_, { env }) => <Tag>{env.name}</Tag> },
          {
            key: "recorded",
            header: "Recorded",
            className: "text-ink-muted",
            render: (_, { gate }) => (
              <>
                <span title={gate.source === "claimed" ? "Claimed by the promoter when they promoted" : "Recorded after the promotion"}>
                  {gate.source === "claimed" ? "at promote" : "after"}
                </span>
                {gate.finishedAt && " · "}
                <Ago at={gate.finishedAt} />
              </>
            ),
          },
          {
            key: "report",
            header: "Report",
            srHeader: true,
            width: "2.5rem",
            className: "px-2",
            render: (_, { gate }) =>
              gate.url ? (
                <a
                  href={gate.url}
                  target="_blank"
                  rel="noreferrer"
                  aria-label={`Open the ${gate.name} report`}
                  className="no-color inline-flex h-7 w-7 items-center justify-center rounded-md text-ink-muted hover:bg-ink/5 hover:text-ink"
                >
                  <ExternalLink className="h-3.5 w-3.5" aria-hidden="true" />
                </a>
              ) : null,
          },
        ]}
      />
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
          <Table<ReleaseArtifact>
            label="Artifacts"
            rows={release.artifacts}
            rowKey={(artifact) => `${artifact.name}${artifact.variant}`}
            columns={[
              {
                key: "name",
                header: "Name",
                className: "font-medium",
                render: (_, artifact) => (
                  <>
                    {artifact.name}
                    {artifact.variant && <span className="ml-1 text-ink-muted">[{artifact.variant}]</span>}
                  </>
                ),
              },
              { key: "kind", header: "Kind", className: "text-ink-muted", render: (_, a) => a.kind || "—" },
              {
                key: "digest",
                header: "Digest",
                render: (_, a) => (a.digest ? <Mono value={a.digest} /> : a.version || <span className="text-ink-muted">—</span>),
              },
              { key: "platforms", header: "Platforms", className: "text-ink-muted", render: (_, a) => a.platforms.join(", ") || "—" },
            ]}
          />
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
  const [width, setWidth] = useState<HealthWindow>(DEFAULT_HEALTH_WINDOW);
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
        envName={env.name}
        width={width}
        navigate={(next) => {
          if ("cmpWindow" in next) setWidth(next.cmpWindow ?? DEFAULT_HEALTH_WINDOW);
        }}
        issueHref={(issue) => withSearch(consolePath(project, { kind: "obs-view", view: "errors" }), { env: env.name, issue })}
      />
    </div>
  );
}
