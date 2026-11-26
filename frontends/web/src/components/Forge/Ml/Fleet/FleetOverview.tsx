// Copyright (c) 2026 Reliant Labs

/**
 * Fleet overview (02-views §7.1): how many accelerators, how much of them is
 * allocated and actually busy, what idles on someone's allocation, and what
 * is broken. Pools table → pool page; the heatmap and the allocated-vs-busy
 * chart share the window; the health list ranks devices and nodes by score.
 */

import { MlHealth, MlSubjectKind, type GetMlFleetResponse, type MlCluster, type MlPool } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { MlFrame, MlNoCluster, MlRead } from "../MlParts";
import {
  Chip,
  EmptyNote,
  FindingLink,
  fmtAgo,
  fmtInt,
  fmtPct,
  fmtUsd,
  Grid,
  has,
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
  type MlColumn,
} from "../kit";
import { allocShare, eventCountSeries, heatmapOf, isStale, plural } from "./fleetModel";
import { dataOf, NO_PRICE_SHEET, Uncollected } from "./parts";

const ORG = { kind: MlSubjectKind.ORG, id: "" };
const SYNC = "ml-fleet";

export function FleetOverview() {
  const fleet = useMl("getMlFleet", {});
  return (
    <MlFrame view="fleet">
      <MlRead query={fleet} what="fleet">
        {(data) => (tsMs(data.observedAt) === null ? <MlNoCluster /> : <OverviewBody data={data} />)}
      </MlRead>
    </MlFrame>
  );
}

function OverviewBody({ data }: { data: GetMlFleetResponse }) {
  const s = data.summary;
  const nowMs = tsMs(data.observedAt) ?? Date.now();
  const worst = data.unhealthy[0];
  // Money is known only under a price sheet: unset (NaN) is "no price sheet", never $0.
  const priced = has(s?.usdPerDay);
  return (
    <div className="space-y-4">
      <KpiStrip
        testId="ml-fleet-kpis"
        items={[
          { label: "GPUs", value: fmtInt(s?.gpusTotal), sub: `${plural(data.clusters.length, "cluster")} · ${plural(data.pools.length, "pool")}`, hero: true },
          { label: "Allocated", value: fmtPct(allocShare(s?.gpusAllocated ?? 0, s?.gpusTotal ?? 0)), sub: `${fmtInt(s?.gpusAllocated)} GPUs · ${fmtInt(s?.gpusUnallocated)} free` },
          {
            label: "Busy (of allocated)",
            value: has(s?.busyRatio) ? fmtPct(s?.busyRatio) : <Uncollected label={(s?.gpusAllocated ?? 0) > 0 ? "not collected" : "none allocated"} />,
            sub: has(s?.trainingGoodput) ? `training goodput ${fmtPct(s?.trainingGoodput)}` : "training goodput not collected",
          },
          {
            label: "Idle allocated",
            value: fmtInt(s?.gpusIdleAllocated),
            tone: (s?.gpusIdleAllocated ?? 0) > 0 ? "warning" : undefined,
            sub: priced ? `${fmtUsd(s?.idleUsdPerDay)}/day of ${fmtUsd(s?.usdPerDay)}/day` : <span title={NO_PRICE_SHEET}>cost unknown: no price sheet</span>,
            href: mlHref("recommendations", [], { filter: "cat=waste" }),
          },
          { label: "Faulty", value: fmtInt(s?.gpusFaulty), tone: (s?.gpusFaulty ?? 0) > 0 ? "danger" : undefined, sub: worst ? worst.subject?.label : "none", href: worst?.subject ? mlHref("fleet", ["gpus", worst.subject.id]) : undefined },
          { label: "Throttled", value: fmtInt(s?.gpusThrottled), tone: (s?.gpusThrottled ?? 0) > 0 ? "warning" : undefined, sub: "any clock-event reason" },
        ]}
      />
      <ClustersStrip clusters={data.clusters} nowMs={nowMs} />
      <Panel title="Pools" note={priced ? "allocation, use and cost by node pool" : "allocation and use by node pool · cost unknown: no price sheet"} flush testId="ml-fleet-pools">
        <PoolsTable pools={data.pools} priced={priced} clusterName={(id) => data.clusters.find((c) => c.id === id)?.name ?? id} />
      </Panel>
      <Grid cols={2}>
        <UtilisationPanel nowMs={nowMs} />
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
            { key: "usd", header: "Wasted so far", numeric: true, render: (r) => (has(r.usd) ? fmtUsd(r.usd) : <Uncollected label="unpriced" why={NO_PRICE_SHEET} />) },
            { key: "last", header: "Last active", numeric: true, render: (r) => fmtAgo(tsMs(r.lastActiveAt), Date.now()) },
          ]}
        />
      </Panel>
    </div>
  );
}

function PoolsTable({ pools, priced, clusterName }: { pools: readonly MlPool[]; priced: boolean; clusterName: (id: string) => string }) {
  const columns: MlColumn<MlPool>[] = [
    { key: "name", header: "Pool", render: (p) => <MlLink href={mlHref("fleet", ["pools", p.clusterId, p.name])}>{p.name}</MlLink>, sort: (p) => p.name },
    { key: "cluster", header: "Cluster", render: (p) => <span className="text-ink-muted">{clusterName(p.clusterId)}</span> },
    { key: "product", header: "Product", render: (p) => p.product.replace("NVIDIA ", "") },
    { key: "purpose", header: "Purpose", render: (p) => <span className="text-ink-muted">{p.purpose}</span> },
    { key: "nodes", header: "Nodes", numeric: true, render: (p) => p.nodeCount },
    { key: "gpus", header: "GPUs", numeric: true, render: (p) => p.gpusTotal, sort: (p) => p.gpusTotal },
    { key: "alloc", header: "Allocated", numeric: true, render: (p) => <Meter ratio={allocShare(p.gpusAllocated, p.gpusTotal)} label={`${p.gpusAllocated} · ${fmtPct(allocShare(p.gpusAllocated, p.gpusTotal))}`} />, sort: (p) => allocShare(p.gpusAllocated, p.gpusTotal) },
    {
      key: "busy",
      header: "Busy",
      numeric: true,
      render: (p) => (has(p.busyRatio) ? fmtPct(p.busyRatio) : <Uncollected label={p.gpusAllocated ? "not collected" : "none allocated"} why="busy share of allocated GPUs" />),
      sort: (p) => (has(p.busyRatio) ? p.busyRatio : -1),
    },
    {
      key: "sm",
      header: "SM active p50",
      numeric: true,
      render: (p) => (Number.isFinite(p.smActiveP50) ? fmtPct(p.smActiveP50) : <Uncollected why="DCGM profiling counters are not collected on this cluster" />),
    },
    { key: "idle", header: "Idle-alloc", numeric: true, render: (p) => (p.gpusIdleAllocated ? <StatusDot tone="warning" label={p.gpusIdleAllocated} /> : <span className="text-ink-muted">0</span>), sort: (p) => p.gpusIdleAllocated },
    {
      key: "health",
      header: "Health",
      render: (p) =>
        p.gpusFaulty ? <StatusDot tone="danger" label={`${p.nodesUnhealthy} unhealthy`} /> : p.nodesUnhealthy ? <StatusDot tone="warning" label={`${p.nodesUnhealthy} degraded`} /> : <StatusDot tone="success" label="healthy" />,
      sort: (p) => p.gpusFaulty * 10 + p.nodesUnhealthy,
    },
    ...(priced
      ? ([
          { key: "price", header: "$/GPU-h", numeric: true, render: (p) => (has(p.usdPerGpuHour) ? `$${p.usdPerGpuHour.toFixed(2)}` : <Uncollected label="unpriced" why={NO_PRICE_SHEET} />) },
          { key: "usd", header: "$/day", numeric: true, render: (p) => (has(p.usdPerDay) ? fmtUsd(p.usdPerDay) : <Uncollected label="unpriced" why={NO_PRICE_SHEET} />), sort: (p) => (has(p.usdPerDay) ? p.usdPerDay : -1) },
        ] satisfies MlColumn<MlPool>[])
      : []),
  ];
  return (
    <MlTable
      rows={pools}
      columns={columns}
      rowKey={(p) => p.id}
      rowHref={(p) => mlHref("fleet", ["pools", p.clusterId, p.name])}
      testId="ml-fleet-pools-table"
      initialSort={priced ? { key: "usd", desc: true } : { key: "gpus", desc: true }}
      empty={<EmptyNote>No GPU node pools: the connected clusters report no nodes with GPUs.</EmptyNote>}
    />
  );
}

/**
 * The clusters behind these numbers, when one needs saying: a cluster that
 * stopped reporting (stale: its last inventory is over 10 min old, and its
 * numbers are that old), or one with no GPU nodes. Nothing when all report.
 */
function ClustersStrip({ clusters, nowMs }: { clusters: readonly MlCluster[]; nowMs: number }) {
  // Stale against the newest report (observed_at), or as the server judged it (telemetry lost: UNKNOWN).
  const stale = (c: MlCluster) => isStale(c, nowMs) || c.health === MlHealth.UNKNOWN;
  const notable = clusters.filter((c) => stale(c) || c.gpuCount === 0);
  if (!notable.length) return null;
  return (
    <ul className="flex flex-wrap gap-2 text-ui" data-testid="ml-fleet-clusters">
      {notable.map((c) => (
        <li key={c.id} className="flex items-center gap-2 rounded-md border border-border px-3 py-1.5" data-testid={`ml-fleet-cluster-${c.id}`}>
          <span className="text-ink">{c.name || c.id}</span>
          {stale(c) && (
            <Chip tone="warning" title="No inventory from this cluster in the last 10 minutes: its numbers are as of its last report">
              {tsMs(c.lastSeenAt) === null ? "stale · never reported" : `stale · last seen ${fmtAgo(tsMs(c.lastSeenAt), nowMs)}`}
            </Chip>
          )}
          {c.gpuCount === 0 && <span className="text-meta text-ink-muted">{plural(c.nodeCount, "node")} · no GPUs found</span>}
        </li>
      ))}
    </ul>
  );
}

function UtilisationPanel({ nowMs }: { nowMs: number }) {
  const heat = useMl("queryMlSeries", { subject: ORG, keys: ["pool.busy"], groupBy: "pool", maxPoints: 48 });
  const alloc = useMl("queryMlSeries", { subject: ORG, keys: ["fleet.alloc_vs_busy"], maxPoints: 96 });
  const events = useMl("listMlEvents", { subject: ORG, kinds: [] });
  return (
    <Panel title="Utilisation" note="last 24h · busy share by pool, and allocated against busy GPUs" testId="ml-fleet-util">
      <div className="space-y-4">
        <MlRead query={heat} what="pool utilisation">
          {(d) => {
            const { rows, columns } = heatmapOf(d.series, (s) => (s.subject ? mlHref("fleet", ["pools", ...s.subject.id.split("/")]) : null));
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
  const events = useMl("listMlEvents", { subject: ORG, kinds: [] });
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
