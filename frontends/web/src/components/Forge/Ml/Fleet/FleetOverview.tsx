// Copyright (c) 2026 Reliant Labs

/**
 * Fleet overview (02-views §7.1): how many accelerators, how much of them is
 * allocated and actually busy, what idles on someone's allocation, and what
 * is broken. Pools table → pool page; the heatmap and the allocated-vs-busy
 * chart share the window; the health list ranks devices and nodes by score.
 */

import { MlSubjectKind, type GetMlFleetResponse, type MlPool } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { MlFrame, MlRead } from "../MlParts";
import {
  EmptyNote,
  FindingLink,
  fmtAgo,
  fmtInt,
  fmtPct,
  fmtUsd,
  Grid,
  healthOf,
  Heatmap,
  KpiStrip,
  Meter,
  MlChart,
  MlLink,
  mlHref,
  MlTable,
  Panel,
  StatusDot,
  SubjectLink,
  eventKindOf,
  tail,
  tsMs,
  useMl,
  useMlEvents,
  useMlSeries,
  type MlColumn,
} from "../kit";
import { allocShare, eventCountSeries, heatmapOf } from "./fleetModel";
import { dataOf } from "./parts";

const ORG = { kind: MlSubjectKind.ORG, id: "" };
const SYNC = "ml-fleet";

export function FleetOverview() {
  const fleet = useMl("getMlFleet", {});
  return (
    <MlFrame view="fleet">
      <MlRead query={fleet} what="fleet">
        {(data) => <OverviewBody data={data} />}
      </MlRead>
    </MlFrame>
  );
}

function OverviewBody({ data }: { data: GetMlFleetResponse }) {
  const s = data.summary;
  const nowMs = tsMs(data.observedAt) ?? Date.now();
  const worst = data.unhealthy[0];
  return (
    <div className="space-y-4">
      <KpiStrip
        testId="ml-fleet-kpis"
        items={[
          { label: "GPUs", value: fmtInt(s?.gpusTotal), sub: `${data.clusters.length} clusters · ${data.pools.length} pools`, hero: true },
          { label: "Allocated", value: fmtPct(allocShare(s?.gpusAllocated ?? 0, s?.gpusTotal ?? 0)), sub: `${fmtInt(s?.gpusAllocated)} GPUs · ${fmtInt(s?.gpusUnallocated)} free` },
          { label: "Busy (of allocated)", value: fmtPct(s?.busyRatio), sub: `training goodput ${fmtPct(s?.trainingGoodput)}` },
          {
            label: "Idle allocated",
            value: fmtInt(s?.gpusIdleAllocated),
            tone: (s?.gpusIdleAllocated ?? 0) > 0 ? "warning" : undefined,
            sub: `${fmtUsd(s?.idleUsdPerDay)}/day of ${fmtUsd(s?.usdPerDay)}/day`,
            href: mlHref("recommendations", [], { filter: "cat=waste" }),
          },
          { label: "Faulty", value: fmtInt(s?.gpusFaulty), tone: (s?.gpusFaulty ?? 0) > 0 ? "danger" : undefined, sub: worst ? worst.subject?.label : "none", href: worst?.subject ? mlHref("fleet", ["gpus", worst.subject.id]) : undefined },
          { label: "Throttled", value: fmtInt(s?.gpusThrottled), tone: (s?.gpusThrottled ?? 0) > 0 ? "warning" : undefined, sub: "any clock-event reason" },
        ]}
      />
      <Panel title="Pools" note="allocation, use and cost by node pool" flush testId="ml-fleet-pools">
        <PoolsTable pools={data.pools} clusterName={(id) => data.clusters.find((c) => c.id === id)?.name ?? id} />
      </Panel>
      <Grid cols={2}>
        <UtilisationPanel nowMs={nowMs} pools={data.pools} />
        <HealthPanel data={data} nowMs={nowMs} />
      </Grid>
      <Panel title="Top idle-allocated" note="allocated GPUs doing nothing, most money first" flush testId="ml-fleet-idle">
        <MlTable
          rows={data.topIdle}
          rowKey={(r) => `${r.workload?.id}|${r.nodeId}`}
          rowHref={() => mlHref("recommendations", [], { filter: "cat=waste" })}
          empty={<EmptyNote>No allocated GPU is idle.</EmptyNote>}
          columns={[
            { key: "ns", header: "Namespace", render: (r) => <span className="font-mono text-code">{r.namespace}</span> },
            { key: "wl", header: "Workload", render: (r) => (r.workload ? <SubjectLink subject={r.workload} /> : "—") },
            { key: "owner", header: "Owner", render: (r) => r.owner },
            { key: "node", header: "Node", render: (r) => <SubjectLink subject={{ kind: MlSubjectKind.NODE, id: r.nodeId }}>{tail(r.nodeId)}</SubjectLink> },
            { key: "gpus", header: "GPUs", numeric: true, render: (r) => r.gpus },
            { key: "idle", header: "Idle for", numeric: true, render: (r) => `${r.idleHours.toFixed(1)}h` },
            { key: "usd", header: "Wasted so far", numeric: true, render: (r) => fmtUsd(r.usd) },
            { key: "last", header: "Last active", numeric: true, render: (r) => fmtAgo(tsMs(r.lastActiveAt), Date.now()) },
          ]}
        />
      </Panel>
    </div>
  );
}

function PoolsTable({ pools, clusterName }: { pools: readonly MlPool[]; clusterName: (id: string) => string }) {
  const columns: MlColumn<MlPool>[] = [
    { key: "name", header: "Pool", render: (p) => <MlLink href={mlHref("fleet", ["pools", p.clusterId, p.name])}>{p.name}</MlLink>, sort: (p) => p.name },
    { key: "cluster", header: "Cluster", render: (p) => <span className="text-ink-muted">{clusterName(p.clusterId)}</span> },
    { key: "product", header: "Product", render: (p) => p.product.replace("NVIDIA ", "") },
    { key: "purpose", header: "Purpose", render: (p) => <span className="text-ink-muted">{p.purpose}</span> },
    { key: "nodes", header: "Nodes", numeric: true, render: (p) => p.nodeCount },
    { key: "gpus", header: "GPUs", numeric: true, render: (p) => p.gpusTotal, sort: (p) => p.gpusTotal },
    { key: "alloc", header: "Allocated", numeric: true, render: (p) => <Meter ratio={allocShare(p.gpusAllocated, p.gpusTotal)} label={`${p.gpusAllocated} · ${fmtPct(allocShare(p.gpusAllocated, p.gpusTotal))}`} />, sort: (p) => allocShare(p.gpusAllocated, p.gpusTotal) },
    { key: "busy", header: "Busy", numeric: true, render: (p) => fmtPct(p.busyRatio), sort: (p) => p.busyRatio },
    {
      key: "sm",
      header: "SM active p50",
      numeric: true,
      render: (p) => (Number.isFinite(p.smActiveP50) ? fmtPct(p.smActiveP50) : <span className="text-meta text-ink-muted" title="DCGM profiling counters are not collected on this cluster">not collected</span>),
    },
    { key: "idle", header: "Idle-alloc", numeric: true, render: (p) => (p.gpusIdleAllocated ? <StatusDot tone="warning" label={p.gpusIdleAllocated} /> : <span className="text-ink-muted">0</span>), sort: (p) => p.gpusIdleAllocated },
    {
      key: "health",
      header: "Health",
      render: (p) =>
        p.gpusFaulty ? <StatusDot tone="danger" label={`${p.nodesUnhealthy} unhealthy`} /> : p.nodesUnhealthy ? <StatusDot tone="warning" label={`${p.nodesUnhealthy} degraded`} /> : <StatusDot tone="success" label="healthy" />,
      sort: (p) => p.gpusFaulty * 10 + p.nodesUnhealthy,
    },
    { key: "price", header: "$/GPU-h", numeric: true, render: (p) => `$${p.usdPerGpuHour.toFixed(2)}` },
    { key: "usd", header: "$/day", numeric: true, render: (p) => fmtUsd(p.usdPerDay), sort: (p) => p.usdPerDay },
  ];
  return <MlTable rows={pools} columns={columns} rowKey={(p) => p.id} rowHref={(p) => mlHref("fleet", ["pools", p.clusterId, p.name])} testId="ml-fleet-pools-table" initialSort={{ key: "usd", desc: true }} />;
}

const BUSY_KEYS = ["pool.busy"];
const ALLOC_KEYS = ["fleet.alloc_vs_busy"];

function UtilisationPanel({ nowMs, pools }: { nowMs: number; pools: readonly MlPool[] }) {
  const heat = useMlSeries(ORG, BUSY_KEYS, { groupBy: "pool" });
  const alloc = useMlSeries(ORG, ALLOC_KEYS);
  const events = useMlEvents(ORG);
  return (
    <Panel title="Utilisation" note="last 24h · busy share by pool, and allocated against busy GPUs" testId="ml-fleet-util">
      <div className="space-y-4">
        <MlRead query={heat} what="pool utilisation">
          {(d) => {
            const { rows, columns } = heatmapOf(d.series, (s) => {
              const pool = pools.find((p) => p.name === s.group);
              return pool ? mlHref("fleet", ["pools", pool.clusterId, pool.name]) : null;
            });
            return rows.length ? <Heatmap rows={rows} columns={columns} format={(v) => fmtPct(v)} labelWidth={96} testId="ml-fleet-heatmap" /> : <EmptyNote>No utilisation reported.</EmptyNote>;
          }}
        </MlRead>
        <MlRead query={alloc} what="allocation">
          {(d) => (
            <MlChart
              series={d.series}
              events={(dataOf(events)?.events ?? []).filter((e) => (tsMs(e.at) ?? 0) > nowMs - 86_400_000)}
              label="Allocated vs busy GPUs"
              unit="count"
              area="fill"
              height={170}
              syncKey={SYNC}
              legend
              testId="ml-fleet-alloc-chart"
            />
          )}
        </MlRead>
      </div>
    </Panel>
  );
}

function HealthPanel({ data, nowMs }: { data: GetMlFleetResponse; nowMs: number }) {
  const events = useMlEvents(ORG);
  return (
    <Panel title="Health" note="open device and node problems, worst score first" testId="ml-fleet-health">
      <div className="-mx-4 space-y-3">
        <MlTable
          rows={data.unhealthy}
          rowKey={(r) => r.subject?.id ?? ""}
          rowHref={(r) => (r.subject?.kind === MlSubjectKind.DEVICE ? mlHref("fleet", ["gpus", r.subject.id]) : r.subject ? mlHref("fleet", r.subject.id.split("/")) : null)}
          testId="ml-fleet-unhealthy"
          columns={[
            { key: "h", header: "", render: (r) => <StatusDot tone={healthOf(r.health).tone} label={healthOf(r.health).label} hideLabel />, width: "24px" },
            { key: "s", header: "Subject", render: (r) => (r.subject ? <SubjectLink subject={r.subject} /> : "—") },
            { key: "score", header: "Score", numeric: true, render: (r) => Math.round(r.score) },
            { key: "reason", header: "Reason", render: (r) => <span className="block max-w-72 truncate text-ink-muted" title={r.reason}>{r.reason}</span> },
            { key: "ev", header: "Events 24h", numeric: true, render: (r) => r.events24h },
            { key: "f", header: "", render: (r) => (r.findingId ? <FindingLink id={r.findingId}>Finding</FindingLink> : null) },
          ]}
        />
        <div className="px-4">
          <MlRead query={events} what="hardware events">
            {(d) => (
              <div className="space-y-2">
                <MlChart series={eventCountSeries(d.events, nowMs)} label="Hardware events (48h, per 3h)" bars height={110} unit="count" legend />
                <ul className="space-y-1 text-ui" data-testid="ml-fleet-events">
                  {d.events.slice(0, 5).map((e) => (
                    <li key={e.id} className="flex items-baseline gap-2">
                      <StatusDot tone={eventKindOf(e.kind).tone} label={eventKindOf(e.kind).label} />
                      <span className="min-w-0 flex-1 truncate">{e.subject ? <SubjectLink subject={e.subject}>{e.title}</SubjectLink> : e.title}</span>
                      <span className="shrink-0 text-meta tabular-nums text-ink-muted">{fmtAgo(tsMs(e.at), nowMs)}</span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </MlRead>
        </div>
      </div>
    </Panel>
  );
}
