// Copyright (c) 2026 Reliant Labs

/**
 * Node detail (02-views §7.3): health with its reason and cordon, the
 * devices (one row per GPU with sparklines), per-GPU charts (synced, with
 * the node's Xid / throttle / NPD markers), the workloads that held it over
 * a week, interconnect (NVLink matrix, InfiniBand ports), node health
 * (kubelet and node-problem-detector conditions, NPD monitors and events),
 * host (node-exporter and kubelet fields), and history (incarnations, DCGM
 * diagnostics). Cordon and diag are shown and refused in the demo.
 */

import { Ban, Stethoscope } from "lucide-react";

import {
  MlCollection,
  MlEventKind,
  MlFieldGroup,
  MlSubjectKind,
  type GetMlNodeResponse,
  type MlDevice,
  type MlFieldValue,
  type MlNodeCondition,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { MlFrame, MlRead } from "../MlParts";
import {
  ChartTile,
  Chip,
  EmptyNote,
  eventKindOf,
  FindingLink,
  fmtAgo,
  fmtDuration,
  fmtInt,
  fmtPct,
  fmtTime,
  Grid,
  has,
  healthOf,
  KpiStrip,
  MlLink,
  mlHref,
  MlTable,
  NotCollected,
  Panel,
  Sparkline,
  StatusDot,
  SubjectLink,
  relabel,
  tsMs,
  useMl,
  useMlEvents,
  useMlSeries,
  type MlColumn,
} from "../kit";
import { byGroup, fieldOf, fmtField, groupFields, groupNotApplicable, groupUncollected, nvlinkMatrix, nvlinkTone } from "./fleetModel";
import { dataOf, DemoActions, FieldTable, Uncollected, WorkloadGantt } from "./parts";

const SYNC = "ml-node";

export function NodePage({ nodeId, node }: { nodeId: string; cluster: string; node: string }) {
  const q = useMl("getMlNode", { nodeId });
  return (
    <MlRead query={q} what="node">
      {(data) => <NodeBody data={data} name={node} />}
    </MlRead>
  );
}

const DEVICE_KEYS = ["gpu.util", "gpu.sm_active", "gpu.temp_c", "gpu.power_w", "gpu.sm_clock_mhz", "gpu.nvlink_tx_gbps"];
const HOST_KEYS = ["host.cpu", "host.mem", "host.psi_cpu", "host.psi_io", "host.net_rx", "ib.xmit_gbps"];

function NodeBody({ data, name }: { data: GetMlNodeResponse; name: string }) {
  const n = data.node!;
  const h = healthOf(n.healthState);
  const subject = { kind: MlSubjectKind.NODE, id: n.id };
  const [cluster, poolName] = n.poolId.split("/");
  const fleet = dataOf(useMl("getMlFleet", { clusterId: n.clusterId }));
  const findings = new Map((fleet?.unhealthy ?? []).filter((u) => u.nodeId === n.id && u.findingId).map((u) => [u.subject?.id ?? "", u.findingId]));
  const nodeFinding = findings.get(n.id) ?? [...findings.values()][0];
  const byDevice = useMlSeries(subject, DEVICE_KEYS, { groupBy: "device" });
  // The device dimension is the GPU's UUID; the page names a GPU by its index.
  const gpuNames = new Map(n.devices.map((d) => [d.uuid, `GPU ${d.index}`]));
  const series = { ...byDevice, data: byDevice.data?.kind === "ok" ? { kind: "ok" as const, data: { series: relabel(byDevice.data.data.series, gpuNames) } } : byDevice.data };
  const host = useMlSeries(subject, HOST_KEYS);
  const events = dataOf(useMlEvents(subject))?.events ?? [];
  const nowMs = Date.now();
  const product = n.devices[0]?.product.replace("NVIDIA ", "") ?? "";
  // Headline numbers from what was collected; a field nothing reports reads "not collected", never 0.
  const powered = n.devices.filter((d) => has(d.powerW));
  const power = powered.reduce((a, d) => a + d.powerW, 0);
  const deviceFields = n.devices.flatMap((d) => d.fields);
  const xidUncollected = groupUncollected(deviceFields, MlFieldGroup.XID);
  const xids = n.devices.filter((d) => d.xidLast > 0).length;
  const facts = [
    n.driverVersion && `driver ${n.driverVersion}`,
    n.cudaVersion && `CUDA ${n.cudaVersion}`,
    n.kernelVersion && `kernel ${n.kernelVersion}`,
    n.osImage,
    n.instanceType,
    n.zone,
    tsMs(n.bootedAt) !== null && `up ${fmtDuration((nowMs - (tsMs(n.bootedAt) ?? nowMs)) / 1000)}`,
    n.incarnation > 0 && `incarnation ${n.incarnation}`,
  ].filter(Boolean);
  const bad = n.conditions.filter((c) => c.bad);
  return (
    <MlFrame
      view="fleet"
      title={name}
      crumbs={[{ label: poolName, href: mlHref("fleet", ["pools", cluster, poolName]) }, { label: name }]}
      meta={
        <>
          <StatusDot tone={h.tone} label={`${h.label} · score ${Math.round(n.healthScore)}`} />
          {n.cordoned && (
            <Chip tone="danger">
              <Ban className="h-3 w-3" aria-hidden="true" />
              cordoned
            </Chip>
          )}
        </>
      }
      subtitle={
        <span>
          {n.healthReason && <span className="text-ink">{n.healthReason} · </span>}
          {[`${n.gpus}× ${product}`, ...facts].join(" · ")}
        </span>
      }
      actions={
        <>
          {nodeFinding && (
            <MlLink href={mlHref("recommendations", [], { sel: nodeFinding })} className="text-ui">
              Open in Recommendations
            </MlLink>
          )}
          <DemoActions actions={[{ label: "Cordon", icon: <Ban /> }, { label: "Run DCGM diag", icon: <Stethoscope /> }]} />
        </>
      }
    >
      <div className="space-y-4">
        <KpiStrip
          testId="ml-node-kpis"
          items={[
            { label: "Allocated", value: `${n.gpusAllocated} / ${n.gpus}`, sub: `${n.gpusAllocatable} allocatable`, tone: n.gpusAllocatable < n.gpus ? "danger" : undefined, hero: true },
            { label: "Busy", value: has(n.busyRatio) ? fmtPct(n.busyRatio) : <Uncollected label={n.gpusAllocated ? "not collected" : "none allocated"} />, sub: "mean of allocated GPUs" },
            { label: "Health score", value: Math.round(n.healthScore), tone: h.tone === "success" ? undefined : h.tone, sub: "0–100, higher is worse" },
            {
              label: "Power",
              value: powered.length ? `${fmtInt(power)} W` : <Uncollected why="no GPU on this node reports DCGM_FI_DEV_POWER_USAGE" />,
              sub: powered.length && powered.length < n.devices.length ? `${powered.length} of ${n.devices.length} GPUs report it` : `${n.devices.length} GPUs`,
            },
            {
              label: "GPUs with an Xid",
              value: xidUncollected ? <Uncollected why="no GPU on this node reports DCGM_FI_DEV_XID_ERRORS" /> : xids,
              tone: xids ? "danger" : undefined,
              sub: xidUncollected ? "Xid errors" : "last Xid set",
            },
            { label: "Bad conditions", value: bad.length, tone: bad.length ? "warning" : undefined, sub: bad.map((c) => c.type).join(", ") || "none" },
          ]}
        />
        <Panel title="Devices" note="latest values · sparkline: GPU utilisation, 24h" flush testId="ml-node-devices">
          {/* The table is the node's own answer; the sparklines join when series answer (none yet is no sparkline). */}
          <DevicesTable devices={n.devices} util={byGroup(dataOf(series)?.series ?? [], "gpu.util")} findings={findings} />
        </Panel>
        <MlRead query={series} what="device series">
          {(d) => (
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3" data-testid="ml-node-charts">
              {[
                { key: "gpu.util", title: "GPU utilisation" },
                { key: "gpu.sm_active", title: "SM active" },
                { key: "gpu.temp_c", title: "Temperature" },
                { key: "gpu.power_w", title: "Power" },
                { key: "gpu.sm_clock_mhz", title: "SM clock" },
                { key: "gpu.nvlink_tx_gbps", title: "NVLink TX" },
              ].map((c) => {
                const s = d.series.filter((x) => x.key === c.key);
                const tones = s.map((x) => {
                  const dev = n.devices.find((dv) => `GPU ${dv.index}` === x.group);
                  return dev && dev.health !== 1 ? healthOf(dev.health).tone : undefined;
                });
                return <ChartTile key={c.key} title={c.title} series={s} events={events} syncKey={SYNC} height={120} tones={tones} />;
              })}
            </div>
          )}
        </MlRead>
        <Panel title="Workloads" note="what held this node's GPUs over 7 days, by pod" testId="ml-node-workloads">
          <WorkloadGantt workloads={data.workloads} nowMs={nowMs} testId="ml-node-gantt" />
        </Panel>
        <Grid cols={2}>
          <Panel title="NVLink" note={data.nvlink.length ? `GPU × GPU · ${data.nvlink[0].links} links each through NVSwitch` : undefined} testId="ml-node-nvlink">
            {data.nvlink.length ? (
              <NvlinkMatrix data={data} />
            ) : groupNotApplicable(deviceFields, MlFieldGroup.NVLINK) ? (
              <EmptyNote>No NVLink on this node: GPUs talk over PCIe.</EmptyNote>
            ) : (
              <NotCollected what="NVLink topology" source="DCGM NVLink fields (DCGM_FI_DEV_NVLINK_*)" compact />
            )}
          </Panel>
          <Panel title="InfiniBand" note={data.ibPorts.length ? "ConnectX-7 · node_infiniband_*" : undefined} flush testId="ml-node-ib">
            <MlTable
              rows={data.ibPorts}
              rowKey={(p) => `${p.hca}/${p.port}`}
              empty={data.host ? <EmptyNote>No InfiniBand HCAs on this node.</EmptyNote> : <div className="px-4 py-3"><NotCollected what="InfiniBand" source="node-exporter's infiniband collector" compact /></div>}
              columns={[
                { key: "hca", header: "HCA", render: (p) => <span className="font-mono text-code">{`${p.hca}/${p.port}`}</span> },
                { key: "state", header: "State", render: (p) => <StatusDot tone={p.state === "ACTIVE" ? "success" : "danger"} label={p.state} /> },
                { key: "rate", header: "Rate", numeric: true, render: (p) => `${p.rateGbps} Gb/s` },
                { key: "x", header: "Xmit", numeric: true, render: (p) => `${p.xmitGbps.toFixed(0)} Gb/s` },
                { key: "r", header: "Rcv", numeric: true, render: (p) => `${p.rcvGbps.toFixed(0)} Gb/s` },
                { key: "sym", header: "Symbol errors", numeric: true, render: (p) => <span className={cn(Number(p.symbolErrors) > 10 && "text-danger-ink")}>{String(p.symbolErrors)}</span> },
                { key: "down", header: "Link downed", numeric: true, render: (p) => <span className={cn(Number(p.linkDowned) > 0 && "text-danger-ink")}>{String(p.linkDowned)}</span> },
              ]}
            />
          </Panel>
        </Grid>
        <Grid cols={2}>
          <Panel title="Conditions" note="kubelet and node-problem-detector" flush testId="ml-node-conditions">
            <ConditionsTable conditions={n.conditions} nowMs={nowMs} />
          </Panel>
          <Panel title="Node problem detector" note="monitors on this node, and what they reported" testId="ml-node-npd">
            <div className="space-y-3">
              <div className="flex flex-wrap gap-1.5">
                {data.host?.npdMonitors.map((m) => (
                  <Chip key={m}>{m}</Chip>
                ))}
              </div>
              <ul className="space-y-1.5 text-ui">
                {events
                  .filter((e) => [MlEventKind.NPD_CONDITION, MlEventKind.NODE_NOT_READY, MlEventKind.DCGM_DIAG, MlEventKind.XID].includes(e.kind))
                  .slice(0, 8)
                  .map((e) => (
                    <li key={e.id} className="flex items-baseline gap-2">
                      <StatusDot tone={eventKindOf(e.kind).tone} label={eventKindOf(e.kind).label} />
                      <span className="min-w-0 flex-1 truncate" title={e.detail}>
                        {e.title}
                      </span>
                      <span className="shrink-0 text-meta tabular-nums text-ink-muted">{fmtAgo(tsMs(e.at), nowMs)}</span>
                    </li>
                  ))}
              </ul>
              {data.host ? (
                <p className="text-meta text-ink-muted">
                  kubelet restarts 24h: <span className="tabular-nums text-ink">{data.host.kubeletRestarts24h}</span> · containerd restarts 24h:{" "}
                  <span className="tabular-nums text-ink">{data.host.containerdRestarts24h}</span>
                </p>
              ) : (
                <p className="text-meta text-ink-muted">node-problem-detector and host restarts: not collected</p>
              )}
            </div>
          </Panel>
        </Grid>
        <Grid cols={2}>
          <Panel title="Host" note="node-exporter and kubelet" flush testId="ml-node-host">
            {data.host?.fields.length ? <HostFields fields={data.host.fields} /> : <div className="px-4 py-3"><NotCollected what="Host fields" source="node-exporter and kubelet metrics" compact /></div>}
          </Panel>
          <MlRead query={host} what="host series">
            {(d) => (
              <div className="grid grid-cols-1 content-start items-start gap-3 sm:grid-cols-2" data-testid="ml-node-host-charts">
                {d.series.map((s) => (
                  <ChartTile key={s.key} title={s.label} series={[s]} syncKey={SYNC} events={events} height={90} />
                ))}
              </div>
            )}
          </MlRead>
        </Grid>
        <Grid cols={2}>
          <Panel title="Incarnations" note="a re-imaged node is a new incarnation; history is kept" flush testId="ml-node-incarnations">
            <MlTable
              rows={data.incarnations}
              empty={<EmptyNote>No incarnation history reported.</EmptyNote>}
              rowKey={(i) => String(i.incarnation)}
              columns={[
                { key: "n", header: "#", numeric: true, render: (i) => i.incarnation, width: "40px" },
                { key: "from", header: "From", render: (i) => fmtTime(tsMs(i.from)) },
                { key: "to", header: "To", render: (i) => (i.to ? fmtTime(tsMs(i.to)) : "now") },
                { key: "note", header: "Note", render: (i) => <span className="block max-w-96 truncate text-ink-muted" title={i.note}>{i.note}</span> },
              ]}
            />
          </Panel>
          <Panel title="DCGM diagnostics" flush testId="ml-node-diag">
            <MlTable
              rows={data.diagnostics}
              empty={<EmptyNote>No DCGM diagnostics reported for this node.</EmptyNote>}
              rowKey={(d) => `${tsMs(d.at)}`}
              columns={[
                { key: "at", header: "When", render: (d) => fmtAgo(tsMs(d.at), nowMs) },
                { key: "level", header: "Level", render: (d) => `r${d.level}` },
                { key: "res", header: "Result", render: (d) => <StatusDot tone={d.result === "fail" ? "danger" : d.result === "warn" ? "warning" : "success"} label={d.result} /> },
                { key: "detail", header: "Detail", render: (d) => <span className="block max-w-96 truncate text-ink-muted" title={d.detail}>{d.detail}</span> },
              ]}
            />
          </Panel>
        </Grid>
      </div>
    </MlFrame>
  );
}

function cell(f: MlFieldValue | undefined, finding?: string) {
  if (!f) return <Uncollected label="not reported" why="this device reports no such field" />;
  const v = fmtField(f);
  if (v === null)
    return (
      <span className="text-meta text-ink-muted" title={f.missingSource}>
        {f.collection === MlCollection.NOT_APPLICABLE ? "n/a" : finding || f.fixFindingId ? <FindingLink id={finding || f.fixFindingId}>not collected</FindingLink> : "not collected"}
      </span>
    );
  return <span className={cn(f.alarm && "font-medium text-danger-ink")}>{v}</span>;
}

function DevicesTable({ devices, util, findings }: { devices: readonly MlDevice[]; util: Map<string, { y: number[] }>; findings: Map<string, string> }) {
  const F = (d: MlDevice, key: string) => fieldOf(d.fields, key);
  const columns: MlColumn<MlDevice>[] = [
    { key: "i", header: "GPU", numeric: true, render: (d) => d.index, width: "48px" },
    { key: "uuid", header: "UUID", render: (d) => <MlLink href={mlHref("fleet", ["gpus", d.uuid])} mono>{`${d.uuid.slice(0, 13)}…`}</MlLink> },
    { key: "held", header: "Held by", render: (d) => (d.allocatedTo ? <SubjectLink subject={d.allocatedTo} className="block max-w-56 truncate" /> : <span className="text-ink-muted">unallocated</span>) },
    {
      key: "util",
      header: "Util",
      numeric: true,
      render: (d) => (
        <span className="inline-flex items-center gap-2">
          <Sparkline values={util.get(`GPU ${d.index}`)?.y ?? []} width={56} height={16} tone={d.health === 3 ? "danger" : d.health === 2 ? "warning" : undefined} />
          {cell(F(d, "gpu.util"))}
        </span>
      ),
    },
    { key: "sm", header: "SM", numeric: true, render: (d) => cell(F(d, "gpu.sm_active")) },
    { key: "tensor", header: "Tensor", numeric: true, render: (d) => cell(F(d, "gpu.tensor_active")) },
    { key: "mem", header: "Memory", numeric: true, render: (d) => (Number.isFinite(d.memoryUsedGb) ? `${d.memoryUsedGb.toFixed(1)} / ${d.memoryTotalGb} GB` : cell(F(d, "gpu.fb_used"))) },
    { key: "power", header: "Power / limit", numeric: true, render: (d) => (Number.isFinite(d.powerW) ? `${Math.round(d.powerW)} / ${Math.round(d.powerLimitW)} W` : cell(F(d, "gpu.power"))) },
    {
      key: "temp",
      header: "Temp / HBM",
      numeric: true,
      render: (d) => (
        <span>
          {cell(F(d, "gpu.temp"))}
          <span className="text-ink-muted"> / </span>
          {cell(F(d, "gpu.memory_temp"))}
        </span>
      ),
    },
    {
      key: "clk",
      header: "Clock events",
      render: (d) => {
        const r = d.clockEventReasons.filter((x) => x !== "GPU idle");
        if (r.length) return <StatusDot tone={r.some((x) => x.startsWith("HW")) ? "danger" : "warning"} label={r.join(", ")} />;
        if (!d.clockEventReasons.length && groupUncollected(d.fields, MlFieldGroup.CLOCKS)) return <Uncollected why="DCGM_FI_DEV_CLOCKS_EVENT_REASONS is not reported" />;
        return <span className="text-ink-muted">{d.clockEventReasons.length ? "idle" : "none"}</span>;
      },
    },
    { key: "ecc", header: "SBE", numeric: true, render: (d) => cell(F(d, "gpu.ecc.sbe_volatile")) },
    { key: "remap", header: "Remap pend.", numeric: true, render: (d) => cell(F(d, "gpu.row_remap.pending")) },
    {
      key: "xid",
      header: "Xid",
      numeric: true,
      render: (d) =>
        d.xidLast ? <span className={cn(d.xidLast === 79 ? "font-medium text-danger-ink" : "text-ink")}>{d.xidLast}</span> : groupUncollected(d.fields, MlFieldGroup.XID) ? cell(F(d, "gpu.xid.last")) : <span className="text-ink-muted" title="no Xid reported">none</span>,
    },
    {
      key: "health",
      header: "Health",
      render: (d) => {
        const h = healthOf(d.health);
        const f = findings.get(d.uuid);
        const dot = <StatusDot tone={h.tone} label={`${Math.round(d.healthScore)}`} />;
        return <span title={d.healthReason || undefined}>{f ? <FindingLink id={f}>{dot}</FindingLink> : dot}</span>;
      },
    },
  ];
  return <MlTable rows={devices} columns={columns} rowKey={(d) => `${d.uuid}#${d.index}`} rowHref={(d) => mlHref("fleet", ["gpus", d.uuid])} testId="ml-node-devices-table" />;
}

function NvlinkMatrix({ data }: { data: GetMlNodeResponse }) {
  const n = data.node!.devices.length;
  const m = nvlinkMatrix(data.nvlink, n);
  return (
    <div className="space-y-2">
      <table className="w-full table-fixed border-collapse text-meta tabular-nums" data-testid="ml-node-nvlink-matrix">
        <thead>
          <tr>
            <th className="w-12" />
            {m.map((_, b) => (
              <th key={b} className="h-6 font-medium text-ink-muted">
                {b}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {m.map((row, a) => (
            <tr key={a}>
              <th className="h-7 pr-2 text-right font-medium text-ink-muted">GPU {a}</th>
              {row.map((c, b) => (
                <td
                  key={b}
                  className={cn("h-7 border border-border-subtle text-center", c.state === "self" && "bg-surface-sunken")}
                  title={c.pair ? `GPU ${c.pair.fromIndex} ↔ GPU ${c.pair.toIndex}: ${c.state}, ${Math.round(c.pair.bandwidthGbps)} GB/s, CRC ${c.pair.crcErrors}, replays ${c.pair.replayErrors}` : undefined}
                  data-state={c.state}
                >
                  {c.state === "up" ? (
                    <span className="text-ink-muted">{Math.round(c.pair!.bandwidthGbps)}</span>
                  ) : c.state === "degraded" || c.state === "down" ? (
                    <StatusDot tone={nvlinkTone(c.state)} label={c.state === "down" ? "down" : String(c.pair!.crcErrors)} />
                  ) : null}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <p className="text-meta text-ink-muted">Cells: GB/s per pair. Dots: degraded (CRC flit errors) or down.</p>
    </div>
  );
}

function ConditionsTable({ conditions, nowMs }: { conditions: readonly MlNodeCondition[]; nowMs: number }) {
  const rows = [...conditions].sort((a, b) => Number(b.bad) - Number(a.bad));
  return (
    <MlTable
      rows={rows}
      rowKey={(c) => c.type}
      maxRows={10}
      columns={[
        { key: "t", header: "Condition", render: (c) => (c.bad ? <StatusDot tone="danger" label={c.type} /> : <span className="pl-3.5 text-ink">{c.type}</span>) },
        { key: "s", header: "Status", render: (c) => c.status },
        { key: "src", header: "Source", render: (c) => <span className="text-ink-muted">{c.source}</span> },
        { key: "r", header: "Reason", render: (c) => <span className="block max-w-72 truncate text-ink-muted" title={c.message}>{c.bad ? c.message : c.reason}</span> },
        { key: "since", header: "Since", numeric: true, render: (c) => fmtAgo(tsMs(c.since), nowMs) },
      ]}
    />
  );
}

function HostFields({ fields }: { fields: readonly MlFieldValue[] }) {
  const groups = groupFields(fields).filter((g) => g.group >= MlFieldGroup.HOST_CPU);
  return (
    <div className="divide-y divide-border-subtle">
      {groups.map((g) => (
        <div key={g.group}>
          <h3 className="px-4 pb-1 pt-2 text-meta font-medium text-ink-muted">{g.label}</h3>
          <FieldTable fields={g.fields} />
        </div>
      ))}
    </div>
  );
}
