// Copyright (c) 2026 Reliant Labs

/**
 * GPU detail (02-views §7.4): identity, the health score with its inputs,
 * EVERY collected field grouped as DCGM groups them (value, unit, source
 * field, alarm; not-collected rows say why and link the fix), the main
 * fields over 24h as synced small multiples with Xid / throttle markers,
 * the workloads that held it and the nodes it has been in.
 */

import { MlCollection, MlSubjectKind, type GetMlDeviceResponse } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { MlFrame, MlRead } from "../MlParts";
import {
  ChartTile,
  FactList,
  fmtPct,
  fmtTime,
  healthOf,
  KpiStrip,
  mlHref,
  MlTable,
  Panel,
  StatusDot,
  SubjectLink,
  tail,
  tsMs,
  useMl,
} from "../kit";
import { fieldOf, fmtField, groupFields } from "./fleetModel";
import { dataOf, FieldTable, WorkloadGantt } from "./parts";

const SYNC = "ml-gpu";
const CHARTS: { title: string; keys: string[] }[] = [
  { title: "Utilisation", keys: ["gpu.util"] },
  { title: "SM active", keys: ["gpu.sm_active"] },
  { title: "Tensor active", keys: ["gpu.tensor_active"] },
  { title: "DRAM active", keys: ["gpu.dram_active"] },
  { title: "Memory used", keys: ["gpu.mem_used_gb"] },
  { title: "Power vs limit", keys: ["gpu.power_w", "gpu.power_limit_w"] },
  { title: "Temperature · HBM", keys: ["gpu.temp_c", "gpu.hbm_temp_c"] },
  { title: "SM clock", keys: ["gpu.sm_clock_mhz"] },
  { title: "HW slowdown share", keys: ["gpu.clock_event_hw_slowdown"] },
  { title: "ECC SBE (volatile)", keys: ["gpu.ecc_sbe"] },
  { title: "NVLink TX", keys: ["gpu.nvlink_tx_gbps"] },
  { title: "NVLink CRC flit errors", keys: ["gpu.nvlink_crc"] },
];

export function DevicePage({ uuid }: { uuid: string }) {
  const q = useMl("getMlDevice", { uuid });
  return (
    <MlRead query={q} what="GPU">
      {(data) => <DeviceBody data={data} />}
    </MlRead>
  );
}

function DeviceBody({ data }: { data: GetMlDeviceResponse }) {
  const d = data.device!;
  const subject = { kind: MlSubjectKind.DEVICE, id: d.uuid };
  const [cluster, nodeName] = d.nodeId.split("/");
  const series = useMl("queryMlSeries", { subject, keys: CHARTS.flatMap((c) => c.keys) });
  const events = dataOf(useMl("listMlEvents", { subject, kinds: [] }))?.events ?? [];
  const h = healthOf(d.health);
  const nowMs = Date.now();
  const value = (key: string) => {
    const f = fieldOf(d.fields, key);
    return f ? (fmtField(f) ?? "not collected") : "—";
  };
  const groups = groupFields(d.fields);
  const total = d.scoreInputs.reduce((a, i) => a + i.points, 0);
  return (
    <MlFrame
      view="fleet"
      title={`${nodeName} · GPU ${d.index}`}
      crumbs={[{ label: nodeName, href: mlHref("fleet", [cluster, nodeName]) }, { label: `GPU ${d.index}` }]}
      meta={<StatusDot tone={h.tone} label={`${h.label} · score ${Math.round(d.healthScore)}`} />}
      subtitle={
        <span>
          {d.healthReason && <span className="text-ink">{d.healthReason} · </span>}
          {d.product} · <span className="font-mono text-code">{d.uuid}</span>
        </span>
      }
    >
      <div className="space-y-4">
        <KpiStrip
          testId="ml-gpu-kpis"
          items={[
            { label: "Health score", value: Math.round(d.healthScore), tone: h.tone === "success" ? undefined : h.tone, sub: h.label, hero: true },
            { label: "Utilisation", value: value("gpu.util"), sub: `SM active ${value("gpu.sm_active")}` },
            { label: "Power / limit", value: `${value("gpu.power")}`, sub: `limit ${value("gpu.power_limit")}` },
            { label: "Temperature", value: value("gpu.temp"), sub: `HBM ${value("gpu.memory_temp")}`, tone: fieldOf(d.fields, "gpu.temp")?.alarm ? "warning" : undefined },
            { label: "Clock events", value: d.clockEventReasons.filter((r) => r !== "GPU idle").join(", ") || "none", tone: d.clockEventReasons.some((r) => r.startsWith("HW")) ? "danger" : undefined },
            { label: "Last Xid", value: d.xidLast || "—", tone: d.xidLast === 79 ? "danger" : undefined, sub: `${d.xids24h} in 24h` },
          ]}
        />
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
          <Panel title="Identity" testId="ml-gpu-identity">
            <FactList
              columns={1}
              facts={[
                { label: "UUID", value: <span className="font-mono text-code">{d.uuid}</span> },
                { label: "Serial", value: <span className="font-mono text-code">{d.serial}</span> },
                { label: "PCI bus id", value: <span className="font-mono text-code">{d.pciBusId}</span> },
                { label: "VBIOS", value: <span className="font-mono text-code">{d.vbios}</span> },
                { label: "Product", value: d.product },
                { label: "MIG", value: d.migMode },
                { label: "Node", value: <SubjectLink subject={{ kind: MlSubjectKind.NODE, id: d.nodeId }}>{tail(d.nodeId)}</SubjectLink> },
                { label: "Index", value: d.index },
                { label: "Held by", value: d.allocatedTo ? <SubjectLink subject={d.allocatedTo} /> : "unallocated" },
              ]}
            />
          </Panel>
          <Panel title="Health score" note="0–100, higher is worse · drain at 60, RMA candidate at 85" flush testId="ml-gpu-score">
            <MlTable
              rows={d.scoreInputs}
              rowKey={(i) => i.input}
              columns={[
                { key: "i", header: "Input", render: (i) => i.input },
                { key: "v", header: "Value", render: (i) => <span className="text-ink-muted">{i.value}</span> },
                { key: "p", header: "Points", numeric: true, render: (i) => `+${i.points}` },
              ]}
            />
            <div className="flex justify-between border-t border-border px-4 py-2 text-ui">
              <span className="text-ink-muted">Score</span>
              <span className="font-medium tabular-nums text-ink">{Math.min(100, total)}</span>
            </div>
          </Panel>
          <Panel title="Placements" note="the nodes this board has been in" flush testId="ml-gpu-placements">
            <MlTable
              rows={data.placements}
              rowKey={(p) => `${p.nodeId}#${tsMs(p.from)}`}
              columns={[
                { key: "n", header: "Node", render: (p) => <SubjectLink subject={{ kind: MlSubjectKind.NODE, id: p.nodeId }}>{tail(p.nodeId)}</SubjectLink> },
                { key: "i", header: "Index", numeric: true, render: (p) => p.index },
                { key: "f", header: "From", render: (p) => fmtTime(tsMs(p.from)) },
                { key: "t", header: "To", render: (p) => (p.to ? fmtTime(tsMs(p.to)) : "now") },
              ]}
            />
          </Panel>
        </div>
        <MlRead query={series} what="GPU series">
          {(s) => (
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4" data-testid="ml-gpu-charts">
              {CHARTS.map((c) => (
                <ChartTile key={c.title} title={c.title} series={s.series.filter((x) => c.keys.includes(x.key))} events={events} syncKey={SYNC} height={100} />
              ))}
            </div>
          )}
        </MlRead>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2 2xl:grid-cols-3" data-testid="ml-gpu-fields">
          {groups.map((g) => (
            <Panel
              key={g.group}
              title={g.label}
              note={[`${g.fields.length} fields`, g.alarms ? `${g.alarms} out of range` : "", g.notCollected ? `${g.notCollected} not collected` : "", g.fields.every((f) => f.collection === MlCollection.NOT_APPLICABLE) ? "not applicable" : ""].filter(Boolean).join(" · ")}
              flush
            >
              <FieldTable fields={g.fields} testId={`ml-gpu-group-${g.group}`} />
            </Panel>
          ))}
        </div>
        <Panel title="Workloads" note={`what held this GPU over 7 days · busy now ${fmtPct(d.busyRatio)}`} testId="ml-gpu-workloads">
          <WorkloadGantt workloads={data.workloads} nowMs={nowMs} />
        </Panel>
      </div>
    </MlFrame>
  );
}
