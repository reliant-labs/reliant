// Copyright (c) 2026 Reliant Labs

/**
 * A service's replicas: where each runs (node and GPUs, as links into
 * Fleet), how loaded it is, and how long a new one takes to serve (the cold
 * start by stage, and each replica's own pod-start waterfall). A row opens
 * the replica in a side panel (`?sel=<replica id>`).
 */

import { MlSubjectKind, type GetMlInferenceServiceResponse, type MlEvent, type MlReplica } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { MlRead } from "../MlParts";
import {
  EmptyNote,
  eventKindOf,
  FactList,
  FindingLink,
  fmtAgo,
  fmtMs,
  fmtPct,
  fmtTime,
  Gantt,
  Grid,
  Meter,
  MlTable,
  Panel,
  SidePanel,
  StackedBar,
  StatusDot,
  SubjectLink,
  tail,
  tsMs,
  useMl,
  useMlSearch,
  type MlColumn,
} from "../kit";
import { fmtWait, podStartGantt, podStartSegments, POD_START_STATES, topStage } from "../Queues/queuesModel";
import { byKey, kvTone } from "./inferenceModel";
import { SeriesTile, useServingSeries } from "./parts";

const short = (pod: string) => pod.split("-").slice(-2).join("-");

export function ReplicasTab({ d, events }: { d: GetMlInferenceServiceResponse; events: readonly MlEvent[] }) {
  const [search, setSearch] = useMlSearch();
  const svc = d.service!;
  const now = Date.now();
  const releaseOf = (revisionId: string) => d.revisions.find((r) => r.id === revisionId)?.release ?? tail(revisionId);
  const selected = d.replicas.find((r) => r.id === search.sel);
  const util = useServingSeries({ kind: MlSubjectKind.INFERENCE_SERVICE, id: svc.id, label: svc.name }, ["gpu.util"], "replica");

  const columns: MlColumn<MlReplica>[] = [
    { key: "pod", header: "Replica", render: (r) => <span className="font-mono text-code">{short(r.pod)}</span>, sort: (r) => r.pod },
    { key: "rev", header: "Revision", render: (r) => <SubjectLink subject={{ kind: MlSubjectKind.REVISION, id: r.revisionId }} mono>{releaseOf(r.revisionId)}</SubjectLink> },
    { key: "node", header: "Node", render: (r) => <SubjectLink subject={{ kind: MlSubjectKind.NODE, id: r.nodeId }} mono>{tail(r.nodeId)}</SubjectLink>, sort: (r) => r.nodeId },
    {
      key: "gpus",
      header: "GPUs",
      render: (r) => (
        <span className="inline-flex gap-1.5">
          {r.deviceUuids.map((u) => (
            <SubjectLink key={u} subject={{ kind: MlSubjectKind.DEVICE, id: u }} mono>
              {u.slice(4, 12)}
            </SubjectLink>
          ))}
        </span>
      ),
    },
    { key: "state", header: "State", render: (r) => <StatusDot tone={r.state === "ready" ? "success" : r.state === "crashloop" ? "danger" : "info"} label={r.state} /> },
    { key: "rps", header: "rps", numeric: true, render: (r) => r.rps.toFixed(1), sort: (r) => r.rps },
    { key: "busy", header: "Busy", numeric: true, render: (r) => <Meter ratio={r.busyRatio} width={40} />, sort: (r) => r.busyRatio },
    { key: "kv", header: "KV cache", numeric: true, render: (r) => <Meter ratio={r.kvCacheRatio} tone={kvTone(r.kvCacheRatio)} width={40} />, sort: (r) => r.kvCacheRatio || 0 },
    { key: "run", header: "Running / waiting", numeric: true, render: (r) => <span className={r.waiting > 0 ? "text-warning-ink" : undefined}>{`${r.running} / ${r.waiting}`}</span>, sort: (r) => r.waiting },
    { key: "ttft", header: "TTFT p95", numeric: true, render: (r) => fmtMs(r.ttftP95Ms), sort: (r) => r.ttftP95Ms || 0 },
    { key: "restarts", header: "Restarts", numeric: true, render: (r) => <span className={r.restarts ? "text-warning-ink" : undefined}>{r.restarts}</span>, sort: (r) => r.restarts },
    { key: "age", header: "Age", numeric: true, render: (r) => fmtAgo(tsMs(r.startedAt), now).replace(" ago", ""), sort: (r) => -(tsMs(r.startedAt) ?? 0) },
  ];

  const segments = podStartSegments(d.coldStart);
  const top = topStage(d.coldStart);
  const gantt = podStartGantt(d.coldStartReplicas, (w) => ({ label: short(w.pod) }));

  return (
    <div className="space-y-4">
      <Panel title="Replicas" note={`${d.replicas.length} of ${svc.replicasDesired} desired · ${svc.accelerators} × ${svc.acceleratorProduct.replace("NVIDIA ", "")}. Click a replica for its load and events.`} flush>
        <MlTable columns={columns} rows={d.replicas} rowKey={(r) => r.id} selected={search.sel} onRowClick={(r) => setSearch({ sel: r.id })} testId="ml-inference-replicas" />
      </Panel>
      <MlRead query={util} what="GPU utilisation">{({ series }) => <SeriesTile title="GPU utilisation per replica" series={series} events={events} unit="ratio" height={140} />}</MlRead>
      <Grid cols={2}>
        <Panel title="Cold start" note={d.coldStart ? `p50 by stage over ${d.coldStart.sampleCount} pod starts (7d)${top ? ` · top: ${top.stage}` : ""}` : undefined}>
          {d.coldStart ? (
            <div className="space-y-3">
              <StackedBar segments={segments} format={fmtWait} testId="ml-inference-cold-start" />
              <table className="w-full text-ui">
                <thead>
                  <tr className="h-8 border-b border-border text-left text-meta text-ink-muted">
                    <th className="font-medium">Stage</th>
                    <th className="text-right font-medium">p50</th>
                    <th className="text-right font-medium">p95</th>
                    <th className="pl-3 font-medium">Top cause</th>
                  </tr>
                </thead>
                <tbody>
                  {d.coldStart.stages.map((s) => (
                    <tr key={s.stage} className="h-8 border-b border-border-subtle last:border-0">
                      <td className="text-ink">{s.stage}</td>
                      <td className="text-right tabular-nums">{fmtWait(s.p50Seconds)}</td>
                      <td className="text-right tabular-nums text-ink-muted">{fmtWait(s.p95Seconds)}</td>
                      <td className="max-w-xs truncate pl-3 text-ink-muted" title={s.topCause}>
                        {s.findingId ? <FindingLink id={s.findingId}>{s.topCause}</FindingLink> : s.topCause}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <EmptyNote>No pod starts recorded.</EmptyNote>
          )}
        </Panel>
        <Panel title="Pod start per replica" note="From pod creation to ready, seconds">
          {gantt.rows.length ? <Gantt rows={gantt.rows} from={0} to={gantt.to} formatTick={fmtWait} states={POD_START_STATES} labelWidth={72} testId="ml-inference-cold-start-gantt" /> : <EmptyNote>No waterfalls.</EmptyNote>}
        </Panel>
      </Grid>
      {selected && <ReplicaPanel replica={selected} release={releaseOf(selected.revisionId)} onClose={() => setSearch({ sel: undefined })} />}
    </div>
  );
}

function ReplicaPanel({ replica, release, onClose }: { replica: MlReplica; release: string; onClose: () => void }) {
  const subject = { kind: MlSubjectKind.REPLICA, id: replica.id, label: short(replica.pod) };
  const series = useServingSeries(subject, ["engine.kv_cache_ratio", "gpu.util", "serve.ttft"]);
  const events = useMl("listMlEvents", { subject });
  const evs = events.data?.kind === "ok" ? events.data.data.events : [];
  const now = Date.now();
  return (
    <SidePanel title={<span className="font-mono">{replica.pod}</span>} meta={`${release} · ${tail(replica.nodeId)}`} onClose={onClose} testId="ml-inference-replica-panel">
      <FactList
        facts={[
          { label: "Node", value: <SubjectLink subject={{ kind: MlSubjectKind.NODE, id: replica.nodeId }} mono>{tail(replica.nodeId)}</SubjectLink> },
          {
            label: "GPUs",
            value: (
              <span className="inline-flex gap-2">
                {replica.deviceUuids.map((u) => (
                  <SubjectLink key={u} subject={{ kind: MlSubjectKind.DEVICE, id: u }} mono>
                    {u.slice(0, 12)}…
                  </SubjectLink>
                ))}
              </span>
            ),
          },
          { label: "Revision", value: <SubjectLink subject={{ kind: MlSubjectKind.REVISION, id: replica.revisionId }} mono>{release}</SubjectLink> },
          { label: "Started", value: `${fmtTime(tsMs(replica.startedAt))} (${fmtAgo(tsMs(replica.startedAt), now)})` },
          { label: "KV cache now", value: fmtPct(replica.kvCacheRatio) },
          { label: "Busy now", value: fmtPct(replica.busyRatio) },
          { label: "Running / waiting", value: `${replica.running} / ${replica.waiting}` },
          { label: "Restarts", value: replica.restarts },
        ]}
      />
      <MlRead query={series} what="replica metrics">
        {({ series: all }) => (
          <div className="space-y-3">
            <SeriesTile title="KV cache" series={byKey(all, "engine.kv_cache_ratio")} events={evs} />
            <SeriesTile title="GPU utilisation" series={byKey(all, "gpu.util")} events={evs} />
            <SeriesTile title="TTFT" series={byKey(all, "serve.ttft").map((s) => ({ ...s, stat: "" }) as typeof s)} events={evs} />
          </div>
        )}
      </MlRead>
      <Panel title="Events" flush>
        {evs.length === 0 ? (
          <div className="px-4">
            <EmptyNote>No events.</EmptyNote>
          </div>
        ) : (
          <ul className="divide-y divide-border-subtle">
            {evs.map((e) => (
              <li key={e.id} className="space-y-0.5 px-4 py-2">
                <div className="flex items-center justify-between gap-3">
                  <StatusDot tone={eventKindOf(e.kind).tone} label={e.title} />
                  <span className="shrink-0 text-meta tabular-nums text-ink-muted">{fmtAgo(tsMs(e.at), now)}</span>
                </div>
                {e.detail && <p className="text-meta text-ink-muted">{e.detail}</p>}
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </SidePanel>
  );
}
