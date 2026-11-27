// Copyright (c) 2026 Reliant Labs

/**
 * ML › Overview (02-views §2): the first screen of the morning. Is anything
 * burning (Needs attention: the top of Recommendations by money), where is
 * the money going (GPU-hours, productive share, idle cost), what is running
 * (active runs with phase, progress, goodput) and serving (SLO, latency,
 * $/1M tokens), and what changed (a merged timeline of every domain's
 * notable events).
 *
 * Every panel reads its own rpc, so one domain without data never blanks the
 * page; every row opens its entity.
 */

import { useMemo, type ReactNode } from "react";
import type { UseQueryResult } from "@tanstack/react-query";

import { MlSubjectKind, type MlRun } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { MlOutcome } from "@/services/controlPlane/ml/types";

import { MlFrame, MlRead } from "../MlParts";
import {
  Chip,
  EmptyNote,
  eventKindOf,
  findingCategoryOf,
  fmtAgo,
  fmtCompact,
  fmtGpuHours,
  fmtMs,
  fmtPct,
  fmtStep,
  fmtDuration,
  fmtUsd,
  Grid,
  Heatmap,
  KpiStrip,
  MlChart,
  MlLink,
  MlTable,
  mlHref,
  num,
  Panel,
  phaseKindOf,
  runBadgeOf,
  runStateOf,
  severityOf,
  Sparkline,
  StatusDot,
  SubjectLink,
  tsMs,
  useMl,
  useMlEvents,
  useMlSeries,
  verdictOf,
  type HeatmapRow,
} from "../kit";
import { activeRuns, findingCounts, needsAttention, productiveShare, recentChanges, servicesBreachingSlo, wasteUsd } from "./overviewModel";

const ORG = { kind: MlSubjectKind.ORG, id: "", label: "org" } as const;

/** An outcome's data, or undefined while loading or without data. */
function dataOf<T>(q: UseQueryResult<MlOutcome<T>, unknown>): T | undefined {
  return q.data?.kind === "ok" ? q.data.data : undefined;
}

export function MlOverviewView(_props: { sub: readonly string[] }) {
  const fleet = useMl("getMlFleet", {});
  const findings = useMl("listMlFindings", {});
  const runs = useMl("listMlRuns", {});
  const services = useMl("listMlInferenceServices", {});
  const queues = useMl("getMlQueues", {});
  const now = Date.now();

  const f = dataOf(fleet);
  const fd = dataOf(findings);
  const live = useMemo(() => activeRuns(dataOf(runs)?.runs ?? []), [runs]);
  const svc = dataOf(services)?.services ?? [];
  const counts = findingCounts(fd?.findings ?? []);
  const s = f?.summary;
  const impact = (fd?.findings ?? []).reduce((sum, x) => sum + (x.impact && Number.isFinite(x.impact.usdPerDay) ? x.impact.usdPerDay : 0), 0);

  return (
    <MlFrame view="overview">
      <KpiStrip
        testId="ml-overview-kpis"
        items={[
          {
            label: "GPUs allocated",
            value: s ? `${fmtCompact(s.gpusAllocated)} / ${fmtCompact(s.gpusTotal)}` : "—",
            sub: s ? `${fmtPct(s.busyRatio)} busy · ${s.gpusIdleAllocated} idle-allocated · ${s.gpusFaulty} faulty` : undefined,
            href: mlHref("fleet"),
            hero: true,
          },
          {
            label: "Training goodput (24h)",
            value: s ? fmtPct(s.trainingGoodput) : "—",
            sub: s?.cost24h ? `${fmtPct(productiveShare(s.cost24h))} of all GPU-h productive` : undefined,
            tone: s && s.trainingGoodput < 0.85 ? "warning" : undefined,
            href: mlHref("runs"),
          },
          {
            label: "GPU spend (24h)",
            value: s?.cost24h ? fmtUsd(s.cost24h.usd) : "—",
            sub: s?.cost24h ? `${fmtGpuHours(s.cost24h.gpuHours)} · waste ${fmtUsd(wasteUsd(s.cost24h))}` : undefined,
          },
          {
            label: "Idle-allocated",
            value: s ? `${fmtUsd(s.idleUsdPerDay)}/day` : "—",
            sub: s ? `${s.gpusIdleAllocated} GPUs doing nothing` : undefined,
            tone: s && s.gpusIdleAllocated > 0 ? "warning" : undefined,
            href: mlHref("fleet"),
          },
          {
            label: "Open findings",
            value: fd ? `${counts.critical + counts.warning + counts.info}` : "—",
            sub: fd ? `${counts.critical} critical · ${fmtUsd(impact)}/day at stake` : undefined,
            tone: counts.critical > 0 ? "danger" : undefined,
            href: mlHref("recommendations"),
          },
          {
            label: "Waiting for GPUs",
            value: dataOf(queues)?.summary ? `${dataOf(queues)!.summary!.pending}` : "—",
            sub: dataOf(queues)?.summary ? `wait p95 ${fmtDuration(dataOf(queues)!.summary!.waitP95Seconds)}` : undefined,
            href: mlHref("queues"),
          },
        ]}
      />

      <Grid cols={2}>
        <Panel title="Needs attention" note="The top of Recommendations, by money at stake" testId="ml-overview-attention" flush actions={<MlLink href={mlHref("recommendations")}>All findings</MlLink>}>
          <MlRead query={findings} what="recommendations">
            {(d) => {
              const top = needsAttention(d.findings);
              if (top.length === 0) return <div className="px-4"><EmptyNote>Nothing open. </EmptyNote></div>;
              return (
                <MlTable
                  testId="ml-overview-attention-table"
                  rows={top}
                  rowKey={(x) => x.id}
                  rowHref={(x) => mlHref("recommendations", [], { sel: x.id })}
                  columns={[
                    { key: "sev", header: "", width: "1.5rem", render: (x) => <StatusDot tone={severityOf(x.severity).tone} label={severityOf(x.severity).label} hideLabel /> },
                    {
                      key: "title",
                      header: "Finding",
                      className: "max-w-0 w-full",
                      render: (x) => (
                        <div className="min-w-0 truncate">
                          <span className="text-ink">{x.title}</span>
                          <span className="ml-2 text-meta text-ink-muted">{findingCategoryOf(x.category).label}</span>
                        </div>
                      ),
                    },
                    { key: "impact", header: "Impact", numeric: true, render: (x) => <span className="text-ink">{x.impact?.text || "—"}</span> },
                  ]}
                />
              );
            }}
          </MlRead>
        </Panel>

        <Panel title="Fleet utilisation" note="Busy share by pool, last 24h" testId="ml-overview-heatmap" actions={<MlLink href={mlHref("fleet")}>Fleet</MlLink>}>
          <FleetHeatmap />
        </Panel>
      </Grid>

      <Panel title="Active training" note="Running, queued and degraded runs" testId="ml-overview-runs" flush actions={<MlLink href={mlHref("runs")}>Training</MlLink>}>
        <MlRead query={runs} what="training runs">
          {() =>
            live.length === 0 ? (
              <div className="px-4"><EmptyNote>No run is active.</EmptyNote></div>
            ) : (
              <MlTable
                testId="ml-overview-runs-table"
                rows={live}
                rowKey={(r) => r.id}
                rowHref={(r) => mlHref("runs", [r.name])}
                columns={[
                  {
                    key: "run",
                    header: "Run",
                    render: (r) => (
                      <div className="leading-tight">
                        <SubjectLink subject={{ kind: MlSubjectKind.RUN, id: r.id, label: r.name }}>{r.name}</SubjectLink>
                        <div className="text-meta text-ink-muted">{r.owner}</div>
                      </div>
                    ),
                  },
                  { key: "state", header: "State", render: (r) => <RunState run={r} /> },
                  { key: "phase", header: "Phase", render: (r) => <PhaseCell run={r} /> },
                  { key: "step", header: "Step", numeric: true, render: (r) => (num(r.totalSteps) ? `${fmtStep(r.step)} / ${fmtStep(r.totalSteps)}` : "—") },
                  { key: "eta", header: "ETA", numeric: true, render: (r) => (r.etaSeconds !== undefined ? fmtDuration(r.etaSeconds) : "—") },
                  { key: "gpus", header: "GPUs", numeric: true, render: (r) => (r.accelerators ? `${r.accelerators}× ${shortProduct(r.acceleratorProduct)}` : "—") },
                  { key: "goodput", header: "Goodput", numeric: true, render: (r) => fmtPct(r.goodput) },
                  { key: "cost", header: "Cost", numeric: true, render: (r) => (r.cost ? <span title={fmtGpuHours(r.cost.gpuHours)}>{fmtUsd(r.cost.usd)}</span> : "—") },
                  { key: "obj", header: "Objective", numeric: true, render: (r) => <Objective run={r} /> },
                ]}
              />
            )
          }
        </MlRead>
      </Panel>

      <Grid cols={2}>
        <Panel title="Inference" note={svc.length ? `${svc.length} services · ${servicesBreachingSlo(svc).length} below SLO` : undefined} testId="ml-overview-inference" flush actions={<MlLink href={mlHref("inference")}>Inference</MlLink>}>
          <MlRead query={services} what="inference services">
            {(d) => (
              <MlTable
                testId="ml-overview-inference-table"
                rows={d.services}
                rowKey={(x) => x.id}
                rowHref={(x) => mlHref("inference", [x.id])}
                columns={[
                  {
                    key: "svc",
                    header: "Service",
                    render: (x) => (
                      <div className="leading-tight">
                        <span className="text-ink">{x.name}</span>
                        <div className="text-meta text-ink-muted">
                          {x.env} · {x.runtime}
                        </div>
                      </div>
                    ),
                  },
                  { key: "rps", header: "rps", numeric: true, render: (x) => <span className="inline-flex items-center gap-2"><Sparkline values={x.rpsSeries?.y ?? []} width={48} />{x.rps.toFixed(0)}</span> },
                  { key: "ttft", header: "TTFT p95", numeric: true, render: (x) => fmtMs(x.ttftP95Ms) },
                  { key: "slo", header: "SLO", numeric: true, render: (x) => <span className={x.sloAttainment < x.sloTarget ? "text-danger-ink" : undefined}>{fmtPct(x.sloAttainment, 1)}</span> },
                  { key: "cost", header: "$/1M tok", numeric: true, render: (x) => fmtUsd(x.usdPerMillionTokens) },
                  { key: "verdict", header: "Release", render: (x) => <StatusDot tone={verdictOf(x.verdict).tone} label={verdictOf(x.verdict).label} /> },
                ]}
              />
            )}
          </MlRead>
        </Panel>

        <Panel title="What changed" note="Every area's notable events, newest first" testId="ml-overview-changes" flush>
          <RecentChanges now={now} />
        </Panel>
      </Grid>

      <Panel title="Allocated vs busy" note="All accelerators, last 24h" testId="ml-overview-alloc">
        <AllocVsBusy />
      </Panel>
    </MlFrame>
  );
}

function shortProduct(p: string): string {
  return p.replace(/^NVIDIA\s+/, "").replace(/\s+\d+GB.*$/, "").replace(/-SXM4.*$/, "");
}

function RunState({ run }: { run: MlRun }) {
  const st = runStateOf(run.state);
  return (
    <span className="inline-flex items-center gap-1.5">
      <StatusDot tone={st.tone} label={st.label} pulse={st.tone === "info"} />
      {run.badges.slice(0, 2).map((b) => (
        <Chip key={b} tone={runBadgeOf(b).tone}>
          {runBadgeOf(b).label}
        </Chip>
      ))}
    </span>
  );
}

function PhaseCell({ run }: { run: MlRun }) {
  const phase = run.phases.find((p) => p.key === run.currentPhaseKey);
  if (!phase) return <span className="text-ink-muted">—</span>;
  return (
    <MlLink href={mlHref("runs", [run.name, "phases", phase.key])}>
      {phase.key} <span className="text-meta text-ink-muted">({phaseKindOf(phase.kind).label}{phase.attempts.length > 1 ? ` · attempt ${phase.attempts.length}` : ""})</span>
    </MlLink>
  );
}

function Objective({ run }: { run: MlRun }) {
  const v = run.summary[run.objectiveKey];
  return (
    <span className="inline-flex items-center gap-2">
      <Sparkline values={run.objective?.y ?? []} width={48} />
      <span className="tabular-nums">{v !== undefined ? v.toFixed(3) : "—"}</span>
    </span>
  );
}

const BUSY_KEYS = ["pool.busy"];
const ALLOC_KEYS = ["fleet.allocated", "fleet.busy"];

function FleetHeatmap() {
  const q = useMlSeries(ORG, BUSY_KEYS, { groupBy: "pool" });
  return (
    <MlRead query={q} what="fleet utilisation">
      {(d) => {
        const series = d.series.filter((s) => s.key === "pool.busy" && s.y.length > 0);
        if (series.length === 0) return <EmptyNote>No utilisation reported yet.</EmptyNote>;
        const cols = series[0].x.map((x) => new Date(x * 1000).toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", hour12: false }));
        const rows: HeatmapRow[] = series.map((s) => ({
          id: s.group || s.label,
          label: s.group || s.label,
          cells: s.y.map((v) => (Number.isFinite(v) ? v : null)),
        }));
        return <Heatmap rows={rows} columns={cols} format={(v) => fmtPct(v)} labelWidth={120} cellHeight={18} testId="ml-overview-heatmap-grid" />;
      }}
    </MlRead>
  );
}

function AllocVsBusy() {
  const q = useMlSeries(ORG, ALLOC_KEYS);
  const events = useMlEvents(ORG, { range: "24h" });
  const ev = events.data?.kind === "ok" ? events.data.data.events : [];
  return (
    <MlRead query={q} what="fleet utilisation">
      {(d) => <MlChart series={d.series} events={ev.filter((e) => e.severity && eventKindOf(e.kind).tone !== "neutral")} label="Allocated vs busy GPUs" height={180} area="fill" syncKey="ml-overview" />}
    </MlRead>
  );
}

function RecentChanges({ now }: { now: number }) {
  const q = useMlEvents(ORG);
  return (
    <MlRead query={q} what="recent changes">
      {(d) => {
        const rows = recentChanges(d.events);
        if (rows.length === 0) return <div className="px-4"><EmptyNote>Nothing changed recently.</EmptyNote></div>;
        return (
          <ul className="divide-y divide-border-subtle" data-testid="ml-overview-changes-list">
            {rows.map((e) => {
              const kind = eventKindOf(e.kind);
              return (
                <li key={e.id} className="flex items-center gap-3 px-4 py-1.5 text-ui">
                  <span className="w-16 shrink-0 text-meta tabular-nums text-ink-muted">{fmtAgo(tsMs(e.at), now)}</span>
                  <StatusDot tone={kind.tone} label={kind.label} className="w-32 shrink-0 text-meta" />
                  <span className="min-w-0 flex-1 truncate">{e.subject ? <SubjectLink subject={e.subject}>{e.title}</SubjectLink> : e.title}</span>
                </li>
              );
            })}
          </ul>
        );
      }}
    </MlRead>
  );
}

export type { ReactNode };
