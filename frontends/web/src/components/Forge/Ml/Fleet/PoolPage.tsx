// Copyright (c) 2026 Reliant Labs

/**
 * Pool detail (02-views §7.2): one tile per node (health dot, cordoned and
 * allocated markers, one cell per device by utilisation), percentile bands
 * for the device fields that matter (synced, with the pool's events),
 * throttle-reason shares, version skew and allocation by namespace.
 */

import { Ban } from "lucide-react";

import { MlSubjectKind, type GetMlPoolResponse, type MlNode, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import Link from "@/components/forge-ui/link";
import { cn } from "@/lib/utils";

import { MlFrame, MlRead } from "../MlParts";
import { ChartTile, EmptyNote, fmtInt, fmtPct, fmtUnit, fmtUsd, lastValue, Grid, has, healthOf, KpiStrip, MlChart, mlHref, MlTable, Panel, StackedBar, StatusDot, useMl } from "../kit";
import { allocShare, utilFill } from "./fleetModel";
import { dataOf, NO_PRICE_SHEET, Uncollected } from "./parts";

const SYNC = "ml-pool";
const BANDS: { key: string; title: string }[] = [
  { key: "gpu.sm_active", title: "SM active" },
  { key: "gpu.tensor_active", title: "Tensor active" },
  { key: "gpu.dram_active", title: "DRAM active" },
  { key: "gpu.mem_used_gb", title: "Memory used" },
  { key: "gpu.power_ratio", title: "Power / limit" },
  { key: "gpu.temp_c", title: "GPU temperature" },
];

export function PoolPage({ poolId, cluster, pool }: { poolId: string; cluster: string; pool: string }) {
  const q = useMl("getMlPool", { poolId });
  return (
    <MlFrame view="fleet" title={pool} subtitle={cluster} crumbs={[{ label: "Pools" }, { label: pool }]}>
      <MlRead query={q} what="pool">
        {(data) => <PoolBody data={data} />}
      </MlRead>
    </MlFrame>
  );
}

function PoolBody({ data }: { data: GetMlPoolResponse }) {
  const p = data.pool!;
  const subject = { kind: MlSubjectKind.POOL, id: p.id };
  const bands = useMl("queryMlSeries", { subject, keys: BANDS.map((b) => b.key) });
  const byNs = useMl("queryMlSeries", { subject, keys: ["pool.allocated_by_namespace"], maxPoints: 96 });
  const events = dataOf(useMl("listMlEvents", { subject, kinds: [] }))?.events ?? [];
  const throttled = data.throttleReasons.filter((r) => r.name !== "none" && r.name !== "GPU idle").reduce((a, r) => a + r.share, 0);
  return (
    <div className="space-y-4">
      <KpiStrip
        testId="ml-pool-kpis"
        items={[
          { label: "GPUs", value: fmtInt(p.gpusTotal), sub: `${p.nodeCount} nodes · ${p.product.replace("NVIDIA ", "")}`, hero: true },
          { label: "Allocated", value: fmtPct(allocShare(p.gpusAllocated, p.gpusTotal)), sub: `${p.gpusAllocated} of ${p.gpusTotal}` },
          { label: "Busy", value: has(p.busyRatio) ? fmtPct(p.busyRatio) : <Uncollected label={p.gpusAllocated ? "not collected" : "none allocated"} />, sub: Number.isFinite(p.smActiveP50) ? `SM active p50 ${fmtPct(p.smActiveP50)}` : "SM active not collected" },
          { label: "Idle allocated", value: fmtInt(p.gpusIdleAllocated), tone: p.gpusIdleAllocated ? "warning" : undefined },
          { label: "Unhealthy nodes", value: fmtInt(p.nodesUnhealthy), tone: p.gpusFaulty ? "danger" : p.nodesUnhealthy ? "warning" : undefined, sub: `${p.gpusFaulty} faulty GPU${p.gpusFaulty === 1 ? "" : "s"}` },
          has(p.usdPerDay) && has(p.usdPerGpuHour)
            ? { label: "Cost", value: `${fmtUsd(p.usdPerDay)}/day`, sub: `$${p.usdPerGpuHour.toFixed(2)}/GPU-h · ${p.capacityType === 3 ? "reserved" : p.capacityType === 2 ? "spot" : "on-demand"}` }
            : { label: "Cost", value: <Uncollected label="unknown" why={NO_PRICE_SHEET} />, sub: "no price sheet covers this pool" },
        ]}
      />
      <Panel title="Nodes" note="one tile per node · one cell per GPU, shaded by utilisation · hatched: no telemetry" testId="ml-pool-grid">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {data.nodes.map((n) => (
            <NodeTile key={n.id} node={n} />
          ))}
        </div>
      </Panel>
      <Panel title="Device fields across the pool" note="p5 · p50 · p95 over every GPU, last 24h" testId="ml-pool-bands">
        <MlRead query={bands} what="pool series">
          {(d) => (
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
              {BANDS.map((b) => {
                const series: MlSeries[] = d.series.filter((s) => s.key === b.key);
                const p50 = series.find((s) => s.stat === "p50");
                return <ChartTile key={b.key} title={b.title} series={series} events={events} syncKey={SYNC} height={110} value={p50 ? `p50 ${fmtUnit(lastValue(p50), p50.unit)}` : undefined} />;
              })}
            </div>
          )}
        </MlRead>
      </Panel>
      <Grid cols={2}>
        <Panel title="Clock-event reasons" note={`share of device time, 24h · ${fmtPct(throttled, 1)} throttled`} testId="ml-pool-throttle">
          {data.throttleReasons.length ? (
            <StackedBar
              segments={data.throttleReasons.map((r) => ({
                label: r.name,
                value: r.share,
                tone: r.name.startsWith("HW") ? "danger" : r.name === "SW power cap" ? "warning" : undefined,
              }))}
              format={(v) => fmtPct(v, 1)}
            />
          ) : (
            <EmptyNote>No clock-event reasons reported.</EmptyNote>
          )}
        </Panel>
        <Panel title="Allocation by namespace" note="now, and over 24h" testId="ml-pool-ns">
          <div className="space-y-3">
            <StackedBar segments={data.allocationByNamespace.map((s) => ({ label: s.name, value: s.value, tone: s.name === "unallocated" ? "neutral" : undefined }))} format={(v) => `${v} GPUs`} />
            <MlRead query={byNs} what="allocation">
              {(d) => <MlChart series={d.series} label="Allocated GPUs by namespace" area="stacked" height={120} syncKey={SYNC} unit="count" legend />}
            </MlRead>
          </div>
        </Panel>
      </Grid>
      <Panel title="Driver, CUDA and image skew" note={data.skew.length > 1 ? `${data.skew.length} combinations: a job across them can behave differently` : "every node on one combination"} flush testId="ml-pool-skew">
        <MlTable
          rows={data.skew}
          rowKey={(s) => `${s.driver}|${s.cuda}|${s.osImage}`}
          columns={[
            { key: "driver", header: "Driver", render: (s) => <span className="font-mono text-code">{s.driver}</span> },
            { key: "cuda", header: "CUDA", render: (s) => s.cuda },
            { key: "os", header: "OS image", render: (s) => <span className="font-mono text-code">{s.osImage}</span> },
            { key: "nodes", header: "Nodes", numeric: true, render: (s) => s.nodes },
          ]}
        />
      </Panel>
    </div>
  );
}

function NodeTile({ node }: { node: MlNode }) {
  const h = healthOf(node.health);
  const [cluster, name] = node.id.split("/");
  return (
    <Link href={mlHref("fleet", [cluster, name])} className="block rounded-xl border border-border bg-surface p-3 hover:border-ink-muted" data-testid={`ml-pool-tile-${node.name}`}>
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="truncate text-ui font-medium text-ink">{node.name}</span>
        <StatusDot tone={h.tone} label={node.health === 1 ? `${Math.round(node.healthScore)}` : `${h.label} · ${Math.round(node.healthScore)}`} />
      </div>
      <div className={cn("grid gap-1", node.devices.length > 4 ? "grid-cols-8" : "grid-cols-4")}>
        {node.devices.map((d) => {
          const bad = d.health === 3 ? "danger" : d.health === 2 ? "warning" : null;
          return (
            <span
              key={d.uuid}
              className={cn("relative h-6 rounded-sm", bad === "danger" && "ring-1 ring-danger", bad === "warning" && "ring-1 ring-warning")}
              style={{ background: utilFill(Number.isFinite(d.busyRatio) ? d.busyRatio : null) }}
              title={`GPU ${d.index}: ${Number.isFinite(d.busyRatio) ? fmtPct(d.busyRatio) : "no telemetry"}${d.allocatedTo ? ` · ${d.allocatedTo.label}` : " · unallocated"}${d.healthReason ? ` · ${d.healthReason}` : ""}`}
            >
              {d.allocatedTo && <span className="absolute bottom-0.5 left-1/2 h-1 w-1 -translate-x-1/2 rounded-full bg-ink/60" aria-hidden="true" />}
            </span>
          );
        })}
      </div>
      <div className="mt-2 flex items-center justify-between text-meta text-ink-muted">
        <span className="tabular-nums">
          {node.gpusAllocated}/{node.gpus} allocated · {fmtPct(node.busyRatio)} busy
        </span>
        {node.cordoned && (
          <span className="inline-flex items-center gap-1 text-danger-ink">
            <Ban className="h-3 w-3" aria-hidden="true" />
            cordoned
          </span>
        )}
      </div>
      {node.healthReason && <p className="mt-1 truncate text-meta text-ink-muted">{node.healthReason}</p>}
    </Link>
  );
}
