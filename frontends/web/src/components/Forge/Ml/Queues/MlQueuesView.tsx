// Copyright (c) 2026 Reliant Labs

/**
 * The ML "queues" view's route component (02-views §8): why hasn't my job
 * started, who holds the quota, is borrowing and preemption fair, and how
 * much accelerator time is lost to waiting and preemption.
 *
 * `?sel=` selects a cluster queue (its demand vs capacity charts) or a
 * pending workload (why it waits, and its events); `?filter=` keeps the
 * pending workloads of one classified reason.
 */

import type { ReactNode } from "react";

import { MlSubjectKind, type GetMlQueuesResponse, type MlPendingWorkload, type MlPreemption, type MlSubject } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { MlFrame, MlRead } from "../MlParts";
import {
  Chip,
  EmptyNote,
  eventKindOf,
  FindingLink,
  fmtAgo,
  fmtGpuHours,
  fmtTime,
  Gantt,
  Grid,
  KpiStrip,
  Meter,
  MlChart,
  MlTable,
  Panel,
  pendingReasonOf,
  StackedBar,
  StatusDot,
  subjectHref,
  SubjectLink,
  tail,
  tsMs,
  useMl,
  useMlEvents,
  useMlSeries,
  useMlSearch,
  type MlColumn,
} from "../kit";
import { filterPending, fmtWait, podStartGantt, podStartSegments, POD_START_STATES, queueRows, reasonCounts, selectionOf, summaryNumbers, topStage, type QueueRow } from "./queuesModel";

const SYNC = "ml-queues";

export function MlQueuesView({ sub: _sub }: { sub: readonly string[] }) {
  const q = useMl("getMlQueues", {});
  return (
    <MlFrame view="queues">
      <MlRead query={q} what="queues">
        {(data) => <QueuesBody data={data} />}
      </MlRead>
    </MlFrame>
  );
}

function QueuesBody({ data }: { data: GetMlQueuesResponse }) {
  const [search, setSearch] = useMlSearch();
  const now = Date.now();
  const k = summaryNumbers(data.summary);
  const sel = selectionOf(search.sel, data.queues, data.pending);
  const chartQueue = sel.queue ?? data.queues.find((x) => x.pending > 0) ?? data.queues[0];
  const rows = queueRows(data.queues);
  const reasons = reasonCounts(data.pending);
  const pending = filterPending(data.pending, search.filter);
  const queueName = (id: string) => data.queues.find((x) => x.id === id)?.name ?? tail(id);

  const queueColumns: MlColumn<QueueRow>[] = [
    { key: "queue", header: "Cluster queue", render: (r) => <span className="font-medium text-ink">{r.queue.name}</span>, sort: (r) => r.queue.name },
    {
      key: "cohort",
      header: "Cluster · cohort",
      render: (r) => (
        <span className="inline-flex items-center gap-1.5">
          <SubjectLink subject={{ kind: MlSubjectKind.CLUSTER, id: r.queue.clusterId }}>{r.queue.clusterId.split("-").slice(0, 1).join("")}</SubjectLink>
          <span className="text-ink-muted">· {r.queue.cohort || "no cohort"}</span>
        </span>
      ),
    },
    { key: "flavor", header: "Flavor", render: (r) => r.flavor },
    { key: "nominal", header: "Nominal", numeric: true, render: (r) => r.nominal, sort: (r) => r.nominal },
    { key: "used", header: "Used", numeric: true, render: (r) => <Meter ratio={r.fill} label={`${r.used}`} tone={r.used > r.nominal ? "warning" : undefined} width={48} />, sort: (r) => r.used },
    { key: "borrowed", header: "Borrowed", numeric: true, render: (r) => (r.borrowed ? <span className="text-warning-ink">+{r.borrowed}</span> : <span className="text-ink-muted">0</span>), sort: (r) => r.borrowed },
    { key: "lendable", header: "Lendable", numeric: true, render: (r) => r.lendable, sort: (r) => r.lendable },
    { key: "pending", header: "Pending", numeric: true, render: (r) => <span className={r.queue.pending ? "text-warning-ink" : "text-ink-muted"}>{r.queue.pending}</span>, sort: (r) => r.queue.pending },
    { key: "admitted", header: "Admitted", numeric: true, render: (r) => r.queue.admitted, sort: (r) => r.queue.admitted },
    { key: "wait", header: "Wait p50 / p95", numeric: true, render: (r) => `${fmtWait(r.queue.waitP50Seconds)} / ${fmtWait(r.queue.waitP95Seconds)}`, sort: (r) => r.queue.waitP95Seconds },
    { key: "fair", header: "Fair share", numeric: true, render: (r) => <span className={r.queue.fairShare > 1 ? "text-warning-ink" : undefined}>{r.queue.fairShare.toFixed(2)}</span>, sort: (r) => r.queue.fairShare },
    { key: "policy", header: "Preempts", render: (r) => <span className="text-ink-muted" title={`${r.queue.queueingStrategy}; preemption within the queue: ${r.queue.preemptionPolicy}`}>{r.queue.preemptionPolicy}</span> },
  ];

  const pendingColumns: MlColumn<MlPendingWorkload>[] = [
    { key: "pos", header: "#", numeric: true, render: (p) => p.position, sort: (p) => p.position },
    { key: "name", header: "Workload", render: (p) => <span className="font-mono text-code">{p.name}</span>, sort: (p) => p.name },
    { key: "work", header: "Admits", render: (p) => (p.subject?.kind === MlSubjectKind.ATTEMPT ? <WorkLink subject={p.subject} /> : <span className="text-ink-muted">{p.subject?.label || "—"}</span>) },
    { key: "queue", header: "Queue", render: (p) => queueName(p.queueId), sort: (p) => p.queueId },
    { key: "prio", header: "Priority", numeric: true, render: (p) => <span className="text-ink-muted" title={`${p.priorityClass} · owner ${p.owner}`}>{p.priority}</span>, sort: (p) => p.priority },
    { key: "gpus", header: "GPUs", numeric: true, render: (p) => `${p.gpus}×${p.flavor}`, sort: (p) => p.gpus },
    { key: "waiting", header: "Waiting", numeric: true, render: (p) => fmtWait(p.waitingSeconds), sort: (p) => p.waitingSeconds },
    {
      key: "reason",
      header: "Reason",
      render: (p) => {
        const r = pendingReasonOf(p.reason);
        return (
          <Chip tone={r.tone} title={p.reasonMessage}>
            {r.label}
          </Chip>
        );
      },
    },
  ];

  const preemptionColumns: MlColumn<MlPreemption>[] = [
    { key: "at", header: "When", render: (p) => <span className="tabular-nums text-ink-muted" title={fmtTime(tsMs(p.at))}>{fmtAgo(tsMs(p.at), now)}</span>, sort: (p) => tsMs(p.at) ?? 0 },
    { key: "preempted", header: "Preempted", render: (p) => <WorkLink subject={p.preempted} /> },
    { key: "by", header: "By", render: (p) => <WorkLink subject={p.preemptor} /> },
    { key: "queue", header: "Queue", render: (p) => queueName(p.queueId) },
    { key: "reason", header: "Reason", render: (p) => <span className="block max-w-md truncate text-ink-muted" title={p.reason}>{p.reason}</span> },
    { key: "lost", header: "GPU-h lost", numeric: true, render: (p) => <span className={p.gpuHoursLost > 5 ? "text-warning-ink" : undefined}>{fmtGpuHours(p.gpuHoursLost)}</span>, sort: (p) => p.gpuHoursLost },
  ];

  const top = topStage(data.podStart);
  const gantt = podStartGantt(data.podStartExamples, (w) => ({ label: w.workload?.kind === MlSubjectKind.ATTEMPT ? `${w.workload.label} · ${w.pod.split("-").slice(-2).join("-")}` : w.pod.split("-").slice(-2).join("-"), sub: w.workload?.kind === MlSubjectKind.REPLICA ? w.pod.replace(/-vllm-\d+$/, "") : undefined, href: w.workload ? (subjectHref(w.workload) ?? undefined) : undefined }));

  return (
    <div className="space-y-4">
      <KpiStrip
        items={[
          { label: "Pending workloads", value: k.pending, tone: k.pending ? "warning" : undefined, sub: reasons.map((r) => `${r.workloads} ${r.label.toLowerCase()}`).join(" · ") },
          { label: "GPUs demanded vs free", value: `${k.demanded} / ${k.free}`, sub: k.shortfall ? `${k.shortfall} short` : "enough free", hero: true },
          { label: "Admission wait p95", value: fmtWait(k.waitP95), sub: `p50 ${fmtWait(k.waitP50)}` },
          { label: "Preemptions · 24h", value: k.preemptions, sub: `${data.preemptions.length} in 7d` },
          { label: "GPU-h lost to preemption · 24h", value: fmtGpuHours(k.lost), sub: `${fmtGpuHours(data.preemptions.reduce((s, p) => s + p.gpuHoursLost, 0))} in 7d` },
        ]}
      />
      <Panel title="Cluster queues" note="Kueue quota per flavor; a row shows that queue's demand over time below" flush>
        <MlTable columns={queueColumns} rows={rows} rowKey={(r) => r.key} selected={chartQueue?.id} onRowClick={(r) => setSearch({ sel: r.queue.id })} testId="ml-queues-queues" />
      </Panel>
      {chartQueue && <QueueCharts queueId={chartQueue.id} name={chartQueue.name} />}
      <Panel
        title="Pending workloads"
        note="What waits and the classified reason (hover a reason for Kueue's message)"
        actions={
          <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Filter by reason">
            <FilterChip on={!search.filter} onClick={() => setSearch({ filter: undefined })}>
              All {data.pending.length}
            </FilterChip>
            {reasons.map((r) => (
              <FilterChip key={r.key} on={search.filter === r.key} onClick={() => setSearch({ filter: r.key })}>
                {r.label} {r.workloads}
              </FilterChip>
            ))}
          </div>
        }
        flush
      >
        <MlTable columns={pendingColumns} rows={pending} rowKey={(p) => p.subject?.id || p.name} selected={sel.workload ? sel.workload.subject?.id || sel.workload.name : undefined} onRowClick={(p) => setSearch({ sel: p.subject?.id || p.name })} testId="ml-queues-pending" empty={<EmptyNote>Nothing waits.</EmptyNote>} />
      </Panel>
      {sel.workload && <WhyItWaits workload={sel.workload} queueName={queueName(sel.workload.queueId)} />}
      <Panel title="Preemptions and evictions" note="Who preempted whom, and the GPU-hours redone since the last checkpoint (7d)" flush>
        <MlTable columns={preemptionColumns} rows={data.preemptions} rowKey={(p) => `${tsMs(p.at)}-${p.preempted?.id}`} initialSort={{ key: "at", desc: true }} testId="ml-queues-preemptions" empty={<EmptyNote>No preemptions in 7 days.</EmptyNote>} />
      </Panel>
      {data.podStart && (
        <Grid cols={2}>
          <Panel title="Pod start" note={`${data.podStart.workload}: ${data.podStart.sampleCount} starts · p50 by stage${top ? ` · top: ${top.stage}` : ""}`} testId="ml-queues-pod-start">
            <div className="space-y-3">
              <StackedBar segments={podStartSegments(data.podStart)} format={(v) => fmtWait(v)} />
              <table className="w-full text-ui">
                <thead>
                  <tr className="h-8 border-b border-border text-left text-meta text-ink-muted">
                    <th className="font-medium">Stage</th>
                    <th className="text-right font-medium">p50</th>
                    <th className="text-right font-medium">p95</th>
                    <th className="text-right font-medium">Share</th>
                    <th className="pl-3 font-medium">Top cause</th>
                  </tr>
                </thead>
                <tbody>
                  {data.podStart.stages.map((s) => (
                    <tr key={s.stage} className="h-8 border-b border-border-subtle last:border-0">
                      <td>{s.stage}</td>
                      <td className="text-right tabular-nums">{fmtWait(s.p50Seconds)}</td>
                      <td className="text-right tabular-nums text-ink-muted">{fmtWait(s.p95Seconds)}</td>
                      <td className="text-right tabular-nums">{Math.round(s.share * 100)}%</td>
                      <td className="max-w-xs truncate pl-3 text-ink-muted" title={s.topCause}>
                        {s.findingId ? <FindingLink id={s.findingId}>{s.topCause}</FindingLink> : s.topCause}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Panel>
          <Panel title="Example pod starts" note="Kueue → scheduled → node → sandbox → image → model → ready, seconds from creation">
            {gantt.rows.length ? <Gantt rows={gantt.rows} from={0} to={gantt.to} formatTick={(v) => fmtWait(v)} states={POD_START_STATES} labelWidth={150} testId="ml-queues-pod-start-gantt" /> : <EmptyNote>No examples.</EmptyNote>}
          </Panel>
        </Grid>
      )}
    </div>
  );
}

/** A run attempt as "run · phase attempt n", anything else as its name; always a link. */
function WorkLink({ subject }: { subject?: MlSubject }) {
  if (!subject) return <span className="text-ink-muted">—</span>;
  if (subject.kind === MlSubjectKind.ATTEMPT) {
    const [run, phase, n] = subject.id.split("/");
    return (
      <span className="inline-flex items-center gap-1">
        <SubjectLink subject={{ kind: MlSubjectKind.RUN, id: run, label: subject.label.split(" · ")[0] }}>{subject.label.split(" · ")[0] || run}</SubjectLink>
        <span className="text-ink-muted">·</span>
        <SubjectLink subject={{ kind: MlSubjectKind.ATTEMPT, id: subject.id }}>{`${phase} attempt ${n}`}</SubjectLink>
      </span>
    );
  }
  return (
    <SubjectLink subject={{ kind: subject.kind, id: subject.id }} mono={subject.kind === MlSubjectKind.WORKLOAD}>
      {subject.label || subject.id}
    </SubjectLink>
  );
}

const QUEUE_KEYS = ["queue.demand_gpus", "queue.wait_p95"];

function QueueCharts({ queueId, name }: { queueId: string; name: string }) {
  const subject = { kind: MlSubjectKind.QUEUE, id: queueId, label: name };
  const series = useMlSeries(subject, QUEUE_KEYS, { range: "7d" });
  const events = useMlEvents(subject, { range: "7d" });
  const evs = events.data?.kind === "ok" ? events.data.data.events : [];
  return (
    <MlRead query={series} what="queue history">
      {({ series: all }) => (
        <Grid cols={2}>
          <Panel title={`${name}: demand vs capacity`} note="7d, GPUs: admitted and pending by reason (stacked), dashed: nominal + borrowing limit">
            <MlChart series={all.filter((s) => s.key === "queue.demand_gpus")} events={evs} label={`${name} demand vs capacity`} area="stacked" syncKey={SYNC} height={200} legend testId="ml-queues-demand" />
          </Panel>
          <Panel title={`${name}: admission wait p95`} note="7d; markers: preemptions, admissions, scheduling failures">
            <MlChart series={all.filter((s) => s.key === "queue.wait_p95")} events={evs} label={`${name} admission wait p95`} syncKey={SYNC} height={200} />
          </Panel>
        </Grid>
      )}
    </MlRead>
  );
}

function WhyItWaits({ workload, queueName }: { workload: MlPendingWorkload; queueName: string }) {
  const now = Date.now();
  const kind = workload.subject?.kind ?? MlSubjectKind.WORKLOAD;
  const events = useMlEvents({ kind: MlSubjectKind.WORKLOAD, id: kind === MlSubjectKind.WORKLOAD ? (workload.subject?.id ?? workload.name) : workload.name }, { range: "7d" });
  const evs = events.data?.kind === "ok" ? events.data.data.events : [];
  const r = pendingReasonOf(workload.reason);
  return (
    <Panel title={<span className="font-mono">{workload.name}</span>} note={<StatusDot tone={r.tone} label={`${r.label} · waiting ${fmtWait(workload.waitingSeconds)} · #${workload.position} in ${queueName}`} />} testId="ml-queues-why">
      <div className="space-y-3">
        <p className="rounded-md border border-border-subtle bg-surface-sunken px-3 py-2 font-mono text-code text-ink">{workload.reasonMessage}</p>
        <p className="text-meta text-ink-muted">
          Admits <WorkLink subject={workload.subject} /> · {workload.gpus} × {workload.flavor} · {workload.priorityClass} ({workload.priority}) · local queue {workload.localQueue} · since {fmtTime(tsMs(workload.createdAt))}
        </p>
        {evs.length > 0 && (
          <ul className="divide-y divide-border-subtle">
            {evs.map((e) => (
              <li key={e.id} className="flex items-center justify-between gap-3 py-1.5 text-ui">
                <StatusDot tone={eventKindOf(e.kind).tone} label={e.title} />
                <span className="shrink-0 text-meta tabular-nums text-ink-muted">{fmtAgo(tsMs(e.at), now)}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </Panel>
  );
}

function FilterChip({ on, onClick, children }: { on: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" aria-pressed={on} onClick={onClick} className={cn("rounded-md border px-2 py-0.5 text-meta tabular-nums", on ? "border-primary text-ink" : "border-border text-ink-muted hover:text-ink")}>
      {children}
    </button>
  );
}
