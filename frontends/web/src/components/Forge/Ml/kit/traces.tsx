// Copyright (c) 2026 Reliant Labs

/**
 * One ML trace (a training step, a checkpoint save, an inference request),
 * drawn with Observability's own waterfall (waterfall/TraceWaterfall) and
 * attribute table, plus what is ML's: the join keys every span carries (run,
 * phase, attempt, rank, node, GPU, pod) as links, so a slow allreduce span
 * opens the rank, its node and its GPU's DCGM fields at that moment.
 */

import { useMemo, useState } from "react";

import { MlSpanStatus, MlSubjectKind, type GetMlTraceResponse, type MlSpan } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { Span } from "@/services/controlPlane/observability/types";

import { AttributeTable } from "../../Observability/AttributeTable";
import { TraceWaterfall } from "../../Observability/waterfall/TraceWaterfall";
import { MlRead } from "../MlParts";
import { fmtMs, fmtStep, fmtTime, num, tsMs } from "./format";
import { attemptId, phaseId } from "./ids";
import { Chip, FactList, Panel, SubjectLink } from "./ui";
import { useMl } from "./useMl";

/** An MlSpan as Observability's Span (the waterfall's input). The join keys go into its attributes too. */
export function spanOf(s: MlSpan): Span {
  const joins: Record<string, string> = {};
  if (s.runId) joins["ml.run.id"] = s.runId;
  if (s.phaseKey) joins["ml.phase.key"] = s.phaseKey;
  if (s.attempt) joins["ml.attempt"] = String(s.attempt);
  if (s.runId && s.rank >= 0 && (s.attempt || s.phaseKey)) joins["ml.rank"] = String(s.rank);
  if (s.nodeId) joins["k8s.node.name"] = s.nodeId.split("/").pop() ?? s.nodeId;
  if (s.deviceUuid) joins["gpu.uuid"] = s.deviceUuid;
  if (s.pod) joins["k8s.pod.name"] = s.pod;
  return {
    spanId: s.spanId,
    parentSpanId: s.parentSpanId,
    name: s.name,
    service: s.service,
    kind: s.kind || "internal",
    startMs: tsMs(s.start) ?? 0,
    durationMs: s.durationMs,
    status: s.status === MlSpanStatus.ERROR ? "error" : s.status === MlSpanStatus.OK ? "ok" : "unset",
    statusMessage: s.statusMessage,
    attributes: { ...joins, ...s.attributes },
    resource: {},
    events: s.events.map((e) => ({ timeMs: tsMs(e.time) ?? 0, name: e.name, attributes: { ...e.attributes } })),
    source: "",
    operatorData: false,
  };
}

/** The links a span's join keys open. */
export function SpanJoins({ span }: { span: MlSpan }) {
  const chips = [];
  if (span.runId) chips.push(<SubjectLink key="run" subject={{ kind: MlSubjectKind.RUN, id: span.runId }} mono>{span.runId}</SubjectLink>);
  if (span.runId && span.phaseKey) chips.push(<SubjectLink key="phase" subject={{ kind: MlSubjectKind.PHASE, id: phaseId(span.runId, span.phaseKey) }}>{span.phaseKey}</SubjectLink>);
  if (span.runId && span.phaseKey && span.attempt) chips.push(<SubjectLink key="attempt" subject={{ kind: MlSubjectKind.ATTEMPT, id: attemptId(span.runId, span.phaseKey, span.attempt) }}>attempt {span.attempt}</SubjectLink>);
  if (span.runId && span.phaseKey && span.attempt && span.rank >= 0)
    chips.push(<SubjectLink key="rank" subject={{ kind: MlSubjectKind.RANK, id: `${attemptId(span.runId, span.phaseKey, span.attempt)}/${span.rank}` }}>rank {span.rank}</SubjectLink>);
  if (span.nodeId) chips.push(<SubjectLink key="node" subject={{ kind: MlSubjectKind.NODE, id: span.nodeId }} mono>{span.nodeId.split("/").pop()}</SubjectLink>);
  if (span.deviceUuid) chips.push(<SubjectLink key="gpu" subject={{ kind: MlSubjectKind.DEVICE, id: span.deviceUuid }} mono>{span.deviceUuid.slice(0, 12)}…</SubjectLink>);
  if (span.pod) chips.push(<span key="pod" className="font-mono text-code text-ink-muted">{span.pod}</span>);
  if (chips.length === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-meta" data-testid="ml-span-joins">
      <span className="text-ink-muted">Joins</span>
      {chips}
    </div>
  );
}

/** A trace already read: summary, waterfall, and the selected span's joins and attributes. */
export function MlTraceBody({ trace, testId }: { trace: GetMlTraceResponse; testId?: string }) {
  const spans = useMemo(() => trace.spans.map(spanOf), [trace.spans]);
  const root = trace.spans.find((s) => !s.parentSpanId) ?? trace.spans[0];
  const [selected, setSelected] = useState<string | undefined>(() => {
    // Open on the slowest leaf that is not the root: where a reader looks first.
    const leaves = trace.spans.filter((s) => s !== root && !trace.spans.some((c) => c.parentSpanId === s.spanId));
    return [...leaves].sort((a, b) => b.durationMs - a.durationMs)[0]?.spanId ?? root?.spanId;
  });
  const span = trace.spans.find((s) => s.spanId === selected) ?? root;
  const sum = trace.summary;
  return (
    <div className="space-y-4" data-testid={testId ?? "ml-trace"}>
      {sum && (
        <FactList
          columns={3}
          facts={[
            { label: "Duration", value: <span className="tabular-nums">{fmtMs(sum.durationMs)}</span> },
            { label: "vs median", value: <span className="tabular-nums">{sum.vsMedian ? `${sum.vsMedian.toFixed(1)}×` : "—"}</span> },
            { label: "Started", value: fmtTime(tsMs(sum.start)) },
            { label: "Dominant span", value: <span className="font-mono text-code">{sum.dominantSpan || "—"}</span> },
            ...(num(sum.step) ? [{ label: "Step", value: fmtStep(sum.step) }] : []),
            { label: "Spans", value: `${trace.spans.length}${trace.truncated ? "+" : ""}` },
          ]}
        />
      )}
      <Panel title="Waterfall" note="Every span carries the run, rank, node and GPU it ran on" flush>
        <div className="border-t border-border-subtle">
          <TraceWaterfall spans={spans} truncated={trace.truncated} selectedSpanId={span?.spanId} onSelect={setSelected} />
        </div>
      </Panel>
      {span && (
        <Panel
          title={<span className="font-mono text-code">{span.name}</span>}
          note={
            <>
              {fmtMs(span.durationMs)} · {span.service}
              {span.status === MlSpanStatus.ERROR && (
                <Chip tone="danger" className="ml-2">
                  {span.statusMessage || "error"}
                </Chip>
              )}
            </>
          }
        >
          <div className="space-y-3">
            <SpanJoins span={span} />
            <AttributeTable title="Attributes" attributes={span.attributes} />
            {span.events.length > 0 && (
              <AttributeTable title="Events" attributes={Object.fromEntries(span.events.map((e) => [fmtTime(tsMs(e.time)), `${e.name}${Object.keys(e.attributes).length ? ` ${JSON.stringify(e.attributes)}` : ""}`]))} />
            )}
          </div>
        </Panel>
      )}
    </div>
  );
}

/** One trace by id, read and drawn. */
export function MlTraceView({ traceId, testId }: { traceId: string; testId?: string }) {
  const trace = useMl("getMlTrace", { traceId });
  return (
    <MlRead query={trace} what="this trace">
      {(t) => <MlTraceBody trace={t} testId={testId} />}
    </MlRead>
  );
}
