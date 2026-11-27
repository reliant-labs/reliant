// Copyright (c) 2026 Reliant Labs

/**
 * A run's System tab (02-views §3.5): the hardware under one attempt, per
 * rank and per node. Ranks: a rank × time heatmap (SM active, power,
 * temperature or HW slowdown; a cell opens that rank) and the ranks table
 * with each rank's mesh coordinates, pod, node and GPU. Nodes: small
 * multiples of utilisation, SM, power against its limit, GPU and HBM
 * temperature, NVLink and network, one line per node. Both: the straggler
 * panel (why rank N is slow: clock-event reasons, temperatures, power), what
 * is allocated against what is used, and step time p50 / p95 / p99. Every
 * series is an ObservabilityService metric filtered to the attempt's ml.*
 * attributes.
 */

import { useMemo, useState } from "react";

import Button from "@/components/forge-ui/button";
import { FilterBar, FilterChip } from "@/components/forge-ui/filter_bar";
import { MlSubjectKind, type MlRank, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import type { RangePreset } from "@/services/controlPlane/observability/window";

import { MlRead } from "../../MlParts";
import {
  ChartTile,
  EmptyNote,
  FactList,
  fmtMs,
  fmtPct,
  fmtUnit,
  Grid,
  KpiStrip,
  lastValue,
  MlChart,
  Meter,
  MlLink,
  MlTable,
  Panel,
  StatusDot,
  SubjectLink,
  subjectHref,
  tail,
  useMlSearch,
  useMlSeries,
  type MlColumn,
} from "../../kit";
import { RankHeat } from "../obs/RankHeat";
import { attemptOfScope, attemptSubjectOf, firstCrossing, rankHeatmap, stragglersOf } from "../obs/obsModel";
import { scopeRange, type RunScope } from "../runScope";

const SYNC = "ml-run-system";

const HEAT_METRICS = [
  { key: "system/sm_active", label: "SM active" },
  { key: "system/power_w", label: "Power" },
  { key: "system/gpu_temp_c", label: "GPU temp" },
  { key: "system/clock_event_hw_slowdown", label: "HW slowdown" },
] as const;

const NODE_TILES = [
  { title: "GPU util", keys: ["system/gpu_util"] },
  { title: "SM active", keys: ["system/sm_active"] },
  { title: "Power vs limit", keys: ["system/power_w", "system/power_limit_w"] },
  { title: "GPU temp", keys: ["system/gpu_temp_c"] },
  { title: "HBM temp", keys: ["system/hbm_temp_c"] },
  { title: "NVLink tx", keys: ["system/nvlink_tx_gbps"] },
  { title: "Network tx (IB / RoCE)", keys: ["system/ib_tx_gbps"] },
  { title: "Host CPU", keys: ["system/host_cpu"] },
] as const;

const AGG_KEYS = ["system/gpu_util", "system/sm_active", "system/gpu_mem_used_gb", "system/power_w", "system/power_limit_w", "system/gpu_temp_c", "system/hbm_temp_c", "system/host_cpu", "system/host_mem", "system/shm_used_gb", "system/clock_event_hw_slowdown", "memory/allocated_gb", "memory/reserved_gb"];
const SLOWDOWN_KEYS = ["system/clock_event_hw_slowdown"];
const STEP_TIME_KEYS = ["timers/step_ms"];
const STEP_TIME_STATS = ["p50", "p95", "p99"];
const STRAGGLER_KEYS = ["system/gpu_temp_c", "system/hbm_temp_c", "system/power_w", "system/power_limit_w", "system/clock_event_hw_slowdown", "system/sm_active"];

const clock = (sec: number) => new Date(sec * 1000).toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", hour12: false });
const shortNode = (id: string) => (id.split("/").pop() ?? id).replace(/^gke-(train|dev)-/, "");
const byKey = (series: readonly MlSeries[] | undefined, key: string) => series?.find((s) => s.key === key);

export function SystemTab({ scope }: { scope: RunScope }) {
  const [search, setSearch] = useMlSearch();
  const attempt = attemptOfScope(scope);
  const subject = attemptSubjectOf(scope);
  const view = search.group === "nodes" ? "nodes" : "ranks";
  if (!attempt || !subject || !scope.phase) {
    return (
      <div data-testid="ml-run-system">
        <EmptyNote>This phase has not started: no pods, no hardware to show.</EmptyNote>
      </div>
    );
  }
  const attemptSubject = { kind: MlSubjectKind.ATTEMPT, id: `${scope.run.id}/${scope.phase.key}/${attempt.n}`, label: `${scope.phase.key} attempt ${attempt.n}` };
  return (
    <div className="space-y-4" data-testid="ml-run-system">
      <FilterBar
        label="System view"
        testId="ml-system-controls"
        trailing={<span className="text-meta text-ink-muted">{scope.phase.key} attempt {attempt.n} · {attempt.worldSize || attempt.ranks.length} GPUs on {attempt.nodeIds.length} nodes</span>}
      >
        <FilterChip on={view === "ranks"} onClick={() => setSearch({ group: undefined })} testId="ml-system-view-ranks">
          Ranks
        </FilterChip>
        <FilterChip on={view === "nodes"} onClick={() => setSearch({ group: "nodes" })} testId="ml-system-view-nodes">
          Nodes
        </FilterChip>
        {scope.rank !== null && (
          <FilterChip on onClick={() => setSearch({ rank: undefined })} testId="ml-system-rank-clear">
            rank {scope.rank} ×
          </FilterChip>
        )}
      </FilterBar>
      <SystemBody scope={scope} ranks={attempt.ranks} attemptSubject={attemptSubject} view={view} onRank={(rank) => setSearch({ rank })} />
    </div>
  );
}

function SystemBody({ scope, ranks, attemptSubject, view, onRank }: { scope: RunScope; ranks: readonly MlRank[]; attemptSubject: { kind: MlSubjectKind; id: string; label: string }; view: "ranks" | "nodes"; onRank: (rank: number) => void }) {
  const range = scopeRange(scope);
  const agg = useMlSeries(attemptSubject, AGG_KEYS, { range });
  // Which ranks were slowed, and since when: HW slowdown per rank (a fallback when GetMlRun carries no ranks).
  const slow = useMlSeries(attemptSubject, SLOWDOWN_KEYS, { groupBy: "rank", range });
  const slowSeries = slow.data?.kind === "ok" ? slow.data.data.series : [];
  const flagged = stragglersOf(ranks);
  const slowRank = slowSeries.filter((s) => (lastValue(s) ?? 0) > 0.3).map((s) => Number(/\d+/.exec(s.group)?.[0]));
  const stragglerRank = scope.rank ?? flagged[0]?.rank ?? slowRank[0] ?? null;
  const straggler = ranks.find((r) => r.rank === stragglerRank) ?? null;
  const since = firstCrossing(slowSeries.find((s) => s.group === `rank ${stragglerRank}`), 0.3);
  const aggSeries = agg.data?.kind === "ok" ? agg.data.data.series : undefined;

  const commShare = ranks.length ? ranks.reduce((s, r) => s + r.commExposedMsP50, 0) / Math.max(1, ranks.reduce((s, r) => s + r.stepTimeMsP50, 0)) : NaN;
  const commMs = ranks.length ? ranks.reduce((s, r) => s + r.commExposedMsP50, 0) / ranks.length : NaN;
  const temp = lastValue(byKey(aggSeries, "system/gpu_temp_c"));
  const hbm = lastValue(byKey(aggSeries, "system/hbm_temp_c"));
  return (
    <>
      <KpiStrip
        testId="ml-system-kpis"
        items={[
          {
            label: "Straggler",
            value: stragglerRank === null ? "None" : `rank ${stragglerRank}`,
            tone: stragglerRank !== null && (straggler?.straggler || slowRank.includes(stragglerRank)) ? "warning" : undefined,
            sub: straggler ? `${shortNode(straggler.nodeId)} · GPU ${straggler.deviceIndex}${straggler.stragglerReason ? ` · ${straggler.stragglerReason}` : ""}` : "every rank within 5% of the median",
            hero: true,
          },
          { label: "SM active (mean)", value: fmtUnit(lastValue(byKey(aggSeries, "system/sm_active")), "ratio"), sub: `GPU util ${fmtUnit(lastValue(byKey(aggSeries, "system/gpu_util")), "ratio")} (counts NCCL spin)` },
          { label: "Comm exposed", value: fmtPct(commShare), sub: Number.isFinite(commMs) ? `${fmtMs(commMs)} of each step, rank mean` : "needs per-rank timers" },
          { label: "Hottest GPU", value: fmtUnit(temp, "C"), sub: `HBM ${fmtUnit(hbm, "C")}`, tone: temp !== null && temp >= 85 ? "warning" : undefined },
          { label: "HW slowdown", value: fmtUnit(lastValue(byKey(aggSeries, "system/clock_event_hw_slowdown")), "ratio"), sub: "share of time, worst GPU" },
        ]}
      />
      {view === "ranks" ? (
        <Grid cols={2}>
          <RankHeatmap subject={attemptSubject} range={range} ranks={ranks} straggler={stragglerRank} selected={scope.rank} onRank={onRank} />
          <div className="space-y-4">
            <StragglerPanel scope={scope} rank={stragglerRank} detail={straggler} since={since} />
            <StepTime subject={attemptSubject} range={range} />
          </div>
        </Grid>
      ) : (
        <>
          <NodeMultiples subject={attemptSubject} range={range} hotNode={straggler?.nodeId} />
          <Grid cols={2}>
            <StragglerPanel scope={scope} rank={stragglerRank} detail={straggler} since={since} />
            <StepTime subject={attemptSubject} range={range} />
          </Grid>
        </>
      )}
      <Grid cols={2}>
        <AllocationPanel scope={scope} series={aggSeries} />
        <Panel title="Where the step goes" note="p50 across ranks" testId="ml-system-breakdown">
          <StepBreakdown ranks={ranks} straggler={stragglerRank} />
        </Panel>
      </Grid>
      <RanksTable ranks={ranks} selected={scope.rank} scope={scope} />
    </>
  );
}

function RankHeatmap({ subject, range, ranks, straggler, selected, onRank }: { subject: { kind: MlSubjectKind; id: string; label: string }; range: RangePreset; ranks: readonly MlRank[]; straggler: number | null; selected: number | null; onRank: (rank: number) => void }) {
  const nodeByRank = new Map(ranks.map((r) => [r.rank, shortNode(r.nodeId)]));
  const [metric, setMetric] = useState<(typeof HEAT_METRICS)[number]>(HEAT_METRICS[0]);
  const keys = useMemo(() => [metric.key], [metric.key]);
  const heat = useMlSeries(subject, keys, { groupBy: "rank", range });
  return (
    <Panel
      title="Ranks over time"
      note={`${metric.label}, scaled to its range · a cell scopes the page to that rank`}
      testId="ml-system-heatmap"
      actions={
        <div className="flex gap-1">
          {HEAT_METRICS.map((m) => (
            <FilterChip key={m.key} on={m.key === metric.key} onClick={() => setMetric(m)} testId={`ml-system-heat-${m.key}`}>
              {m.label}
            </FilterChip>
          ))}
        </div>
      }
    >
      <MlRead query={heat} what="per-rank telemetry">
        {(data) => {
          const { rows, xs } = rankHeatmap(data.series);
          const unit = data.series[0]?.unit ?? "";
          if (rows.length === 0) return <EmptyNote>No per-rank samples for this attempt.</EmptyNote>;
          return (
            <RankHeat
              rows={rows}
              columns={xs.map(clock)}
              format={(v) => fmtUnit(v, unit)}
              callout={straggler}
              selected={selected}
              nodeOf={(r) => nodeByRank.get(r) ?? `node ${Math.floor(r / 8)}`}
              onRank={onRank}
              testId="ml-system-heatmap-grid"
            />
          );
        }}
      </MlRead>
    </Panel>
  );
}

function StragglerPanel({ scope, rank, detail, since }: { scope: RunScope; rank: number | null; detail: MlRank | null; since: number | null }) {
  const attempt = attemptOfScope(scope);
  const rankSubject = rank !== null && attempt && scope.phase ? { kind: MlSubjectKind.RANK, id: `${scope.run.id}/${scope.phase.key}/${attempt.n}/${rank}`, label: `rank ${rank}` } : null;
  const series = useMlSeries(rankSubject ?? { kind: MlSubjectKind.RUN, id: scope.run.id }, STRAGGLER_KEYS, { enabled: !!rankSubject, range: scopeRange(scope) });
  if (!rankSubject) {
    return (
      <Panel title="Straggler" testId="ml-system-straggler">
        <p className="flex items-center gap-2 text-ui text-ink">
          <StatusDot tone="success" /> No straggler: every rank's step compute is within 5% of the median.
        </p>
      </Panel>
    );
  }
  const s = series.data?.kind === "ok" ? series.data.data.series : [];
  const v = (k: string) => lastValue(byKey(s, k));
  const slowed = (v("system/clock_event_hw_slowdown") ?? 0) > 0.3;
  return (
    <Panel
      title={
        <span className="inline-flex items-center gap-2">
          {detail?.straggler || slowed ? <StatusDot tone="warning" /> : null}
          {detail?.straggler || slowed ? "Straggler" : "Rank"}: <SubjectLink subject={rankSubject}>rank {rank}</SubjectLink>
        </span>
      }
      note={since ? `since ${clock(since)}` : undefined}
      testId="ml-system-straggler"
      actions={
        <>
          {detail?.deviceUuid && (
            <MlLink href={subjectHref({ kind: MlSubjectKind.DEVICE, id: detail.deviceUuid }) ?? "#"} testId="ml-system-open-device">
              Open device
            </MlLink>
          )}
          <Button variant="outline" size="xs" disabled title="Demo shell: read-only. Cordoning is declared in the pool's KCL or run with reliant forge ml cordon.">
            Cordon node
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <FactList
          columns={2}
          facts={[
            { label: "Pod", value: <span className="font-mono text-code">{detail?.pod || "—"}</span> },
            { label: "Node", value: detail?.nodeId ? <SubjectLink subject={{ kind: MlSubjectKind.NODE, id: detail.nodeId }} mono>{shortNode(detail.nodeId)}</SubjectLink> : "—" },
            { label: "GPU", value: detail?.deviceUuid ? <SubjectLink subject={{ kind: MlSubjectKind.DEVICE, id: detail.deviceUuid }} mono>{`GPU ${detail.deviceIndex} · ${detail.deviceUuid.slice(0, 13)}…`}</SubjectLink> : "—" },
            { label: "Mesh", value: detail ? `dp ${detail.dp} · tp ${detail.tp} · local rank ${detail.localRank}` : "—" },
            { label: "Clock events", value: slowed ? <span className="text-warning-ink">HW_SLOWDOWN · HW_THERMAL ({fmtPct(v("system/clock_event_hw_slowdown"))} of time)</span> : "none" },
            { label: "Temperature", value: `${fmtUnit(v("system/gpu_temp_c"), "C")} · HBM ${fmtUnit(v("system/hbm_temp_c"), "C")}` },
            { label: "Power", value: `${fmtUnit(v("system/power_w"), "W")} of ${fmtUnit(v("system/power_limit_w"), "W")}` },
            { label: "Step p50", value: detail ? `${fmtMs(detail.stepTimeMsP50)} · compute ${fmtMs(detail.computeMsP50)}` : "—" },
          ]}
        />
        {detail?.stragglerReason && <p className="text-meta text-ink-muted">{detail.stragglerReason}</p>}
        <div className="grid gap-3 sm:grid-cols-2">
          <ChartTile title="GPU / HBM temp" series={s.filter((x) => x.key === "system/gpu_temp_c" || x.key === "system/hbm_temp_c")} syncKey={SYNC} height={80} value={fmtUnit(v("system/gpu_temp_c"), "C")} />
          <ChartTile title="HW slowdown" series={s.filter((x) => x.key === "system/clock_event_hw_slowdown")} syncKey={SYNC} height={80} tones={["warning"]} />
        </div>
      </div>
    </Panel>
  );
}

function StepTime({ subject, range }: { subject: { kind: MlSubjectKind; id: string; label: string }; range: RangePreset }) {
  const q = useMlSeries(subject, STEP_TIME_KEYS, { stats: STEP_TIME_STATS, range });
  return (
    <Panel title="Step time" note="p50 / p95 / p99 across the attempt's ranks" testId="ml-system-histogram">
      <MlRead query={q} what="step time">
        {(data) => <MlChart series={data.series} label="Step time" height={110} syncKey={SYNC} what="Step time" />}
      </MlRead>
    </Panel>
  );
}

function NodeMultiples({ subject, range, hotNode }: { subject: { kind: MlSubjectKind; id: string; label: string }; range: RangePreset; hotNode?: string }) {
  const keys = useMemo(() => [...new Set(NODE_TILES.flatMap((t) => t.keys))], []);
  const nodes = useMlSeries(subject, keys, { groupBy: "node", range });
  return (
    <Panel title="Per node" note="one line per node; the straggler's node in amber" testId="ml-system-nodes">
      <MlRead query={nodes} what="per-node telemetry">
        {(data) => (
          <div className="grid min-w-0 gap-3 sm:grid-cols-2 xl:grid-cols-4">
            {NODE_TILES.map((tile) => {
              const s = data.series.filter((x) => (tile.keys as readonly string[]).includes(x.key));
              // The limit draws as one muted line; the hot node's lines in warning.
              const shown = tile.keys.length > 1 ? [...s.filter((x) => x.key === tile.keys[0]), ...s.filter((x) => x.key === tile.keys[1]).slice(0, 1)] : s;
              const tones = shown.map((x) => (x.key === "system/power_limit_w" ? ("neutral" as const) : hotNode && x.group === tail(hotNode) ? ("warning" as const) : undefined));
              return <ChartTile key={tile.title} title={tile.title} series={shown} tones={tones} syncKey={SYNC} height={100} value="" />;
            })}
          </div>
        )}
      </MlRead>
    </Panel>
  );
}

function AllocationPanel({ scope, series }: { scope: RunScope; series: readonly MlSeries[] | undefined }) {
  const attempt = attemptOfScope(scope);
  const product = scope.run.acceleratorProduct;
  const hbmGb = product.includes("40GB") ? 40 : product.includes("24GB") ? 24 : 80;
  const v = (k: string) => lastValue(byKey(series, k));
  const used = v("system/gpu_mem_used_gb");
  const memAlloc = series?.find((s) => s.key === "memory/allocated_gb" && (s.stat === "p50" || !s.stat));
  const memRes = series?.find((s) => s.key === "memory/reserved_gb" && (s.stat === "p50" || !s.stat));
  const gpus = attempt?.worldSize || attempt?.ranks.length || scope.run.accelerators;
  const rows = [
    { label: "GPUs allocated", value: `${gpus} × ${product || "GPU"}`, meter: <Meter ratio={v("system/gpu_util")} label={`util ${fmtUnit(v("system/gpu_util"), "ratio")}`} /> },
    { label: "SM active", value: "work actually on the SMs", meter: <Meter ratio={v("system/sm_active")} /> },
    {
      label: "GPU memory",
      value: `${fmtUnit(used, "GB")} used of ${hbmGb} GB · torch allocated ${fmtUnit(lastValue(memAlloc), "GB")}, reserved ${fmtUnit(lastValue(memRes), "GB")}`,
      meter: <Meter ratio={used !== null ? used / hbmGb : null} />,
    },
    { label: "Host CPU", value: "dataloader workers, NCCL proxy", meter: <Meter ratio={v("system/host_cpu")} /> },
    { label: "Host memory", value: `/dev/shm ${fmtUnit(v("system/shm_used_gb"), "GB")} used`, meter: <Meter ratio={v("system/host_mem")} /> },
  ];
  return (
    <Panel title="Allocated vs used" note="now, mean per GPU / node" testId="ml-system-allocation">
      <dl className="divide-y divide-border-subtle">
        {rows.map((r) => (
          <div key={r.label} className="flex h-9 items-center justify-between gap-3">
            <dt className="w-28 shrink-0 text-ui text-ink">{r.label}</dt>
            <dd className="min-w-0 flex-1 truncate text-meta text-ink-muted">{r.value}</dd>
            <dd className="shrink-0 text-ui">{r.meter}</dd>
          </div>
        ))}
      </dl>
    </Panel>
  );
}

function StepBreakdown({ ranks, straggler }: { ranks: readonly MlRank[]; straggler: number | null }) {
  if (ranks.length === 0) return <EmptyNote>Per-rank timers are not reported for this attempt.</EmptyNote>;
  const others = ranks.filter((r) => r.rank !== straggler);
  const mean = (rs: readonly MlRank[], f: (r: MlRank) => number) => rs.reduce((s, r) => s + f(r), 0) / Math.max(1, rs.length);
  const rowsOf = (label: string, rs: readonly MlRank[]) => ({ label, compute: mean(rs, (r) => r.computeMsP50), comm: mean(rs, (r) => r.commExposedMsP50), data: mean(rs, (r) => r.dataWaitMsP50), step: mean(rs, (r) => r.stepTimeMsP50) });
  const rows = [rowsOf(straggler !== null ? "Other ranks" : "All ranks", others), ...(straggler !== null ? [rowsOf(`rank ${straggler}`, ranks.filter((r) => r.rank === straggler))] : [])];
  const max = Math.max(...rows.map((r) => r.step), 1);
  return (
    <div className="space-y-3">
      {rows.map((r) => {
        const other = Math.max(0, r.step - r.compute - r.comm - r.data);
        const parts = [
          { label: "compute", v: r.compute, c: "--chart-1" },
          { label: "comm exposed", v: r.comm, c: "--chart-3" },
          { label: "data wait", v: r.data, c: "--chart-4" },
          { label: "other", v: other, c: "--chart-6" },
        ];
        return (
          <div key={r.label} className="space-y-1">
            <div className="flex justify-between text-meta">
              <span className="text-ink">{r.label}</span>
              <span className="tabular-nums text-ink-muted">{fmtMs(r.step)}</span>
            </div>
            <div className="flex h-2.5 overflow-hidden rounded-full bg-surface-sunken" style={{ width: `${(r.step / max) * 100}%` }}>
              {parts.map((p) => (
                <span key={p.label} className="h-full border-l border-surface first:border-0" style={{ width: `${(p.v / r.step) * 100}%`, backgroundColor: `hsl(var(${p.c}))` }} title={`${p.label}: ${fmtMs(p.v)}`} />
              ))}
            </div>
          </div>
        );
      })}
      <ul className="flex flex-wrap gap-x-4 text-meta text-ink-muted">
        {[
          ["compute", "--chart-1"],
          ["comm exposed", "--chart-3"],
          ["data wait", "--chart-4"],
          ["other", "--chart-6"],
        ].map(([l, c]) => (
          <li key={l} className="inline-flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-sm" style={{ backgroundColor: `hsl(var(${c}))` }} aria-hidden="true" />
            {l}
          </li>
        ))}
      </ul>
    </div>
  );
}

function RanksTable({ ranks, selected, scope }: { ranks: readonly MlRank[]; selected: number | null; scope: RunScope }) {
  const attempt = attemptOfScope(scope);
  const base = attempt && scope.phase ? `${scope.run.id}/${scope.phase.key}/${attempt.n}` : "";
  const columns: MlColumn<MlRank>[] = [
    { key: "rank", header: "Rank", render: (r) => <SubjectLink subject={{ kind: MlSubjectKind.RANK, id: `${base}/${r.rank}` }}>{r.rank}</SubjectLink>, sort: (r) => r.rank },
    { key: "local", header: "Local", numeric: true, render: (r) => r.localRank },
    { key: "mesh", header: "dp · tp", render: (r) => <span className="tabular-nums">{r.dp} · {r.tp}</span> },
    { key: "pod", header: "Pod", render: (r) => <span className="font-mono text-code text-ink-muted">{r.pod.replace(/^.*-(w-\d+)$/, "$1")}</span> },
    { key: "node", header: "Node", render: (r) => (r.nodeId ? <SubjectLink subject={{ kind: MlSubjectKind.NODE, id: r.nodeId }} mono>{shortNode(r.nodeId)}</SubjectLink> : "—") },
    { key: "gpu", header: "GPU", render: (r) => (r.deviceUuid ? <SubjectLink subject={{ kind: MlSubjectKind.DEVICE, id: r.deviceUuid }} mono>{`${r.deviceIndex} · ${r.deviceUuid.slice(4, 12)}`}</SubjectLink> : "—") },
    { key: "step", header: "Step p50", numeric: true, render: (r) => fmtMs(r.stepTimeMsP50), sort: (r) => r.stepTimeMsP50 },
    { key: "compute", header: "Compute", numeric: true, render: (r) => fmtMs(r.computeMsP50), sort: (r) => r.computeMsP50 },
    { key: "comm", header: "Comm exposed", numeric: true, render: (r) => fmtMs(r.commExposedMsP50), sort: (r) => r.commExposedMsP50 },
    { key: "data", header: "Data wait", numeric: true, render: (r) => fmtMs(r.dataWaitMsP50), sort: (r) => r.dataWaitMsP50 },
    { key: "tflops", header: "TFLOP/s", numeric: true, render: (r) => fmtUnit(r.tflops, "TFLOP/s").replace(" TFLOP/s", ""), sort: (r) => r.tflops },
    { key: "mem", header: "Mem alloc / peak", numeric: true, render: (r) => `${fmtUnit(r.memoryAllocatedGb, "GB")} / ${fmtUnit(r.memoryPeakGb, "GB")}`, sort: (r) => r.memoryPeakGb },
    { key: "straggler", header: "", render: (r) => (r.straggler ? <StatusDot tone="warning" label="straggler" /> : null), sort: (r) => Number(r.straggler) },
  ];
  return (
    <Panel title="Ranks" note={`${ranks.length} ranks · mesh coordinates, placement and per-rank timers`} flush testId="ml-system-ranks">
      <MlTable
        columns={columns}
        rows={ranks}
        rowKey={(r) => String(r.rank)}
        rowHref={(r) => subjectHref({ kind: MlSubjectKind.RANK, id: `${base}/${r.rank}` })}
        selected={selected !== null ? String(selected) : undefined}
        initialSort={{ key: "step", desc: true }}
        maxRows={12}
        testId="ml-system-ranks-table"
        empty={<EmptyNote>No per-rank data for this attempt.</EmptyNote>}
      />
    </Panel>
  );
}
