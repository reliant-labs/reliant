// Copyright (c) 2026 Reliant Labs

/** What the service detail's tabs share: the subject, the series read, and a tile that knows "not applicable". */

import type { ReactNode } from "react";

import { useMemo } from "react";

import { MlSubjectKind, type MlEvent, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { SubjectRef } from "@/services/controlPlane/ml/telemetry";

import { fmtUnit, lastValue, mergeSeries, MlChart, useMlSeries, type MlTone } from "../kit";
import { notApplicable } from "./inferenceModel";

/** Every chart on a service page shares one cursor. */
export const SYNC = "ml-inference-service";

export const serviceSubject = (id: string, label = id) => ({ kind: MlSubjectKind.INFERENCE_SERVICE, id, label });

/** The latency histograms: read as p50 / p95 / p99, one series each. */
const LATENCY_KEYS: ReadonlySet<string> = new Set(["serve.ttft", "serve.itl", "serve.e2e", "serve.queue_time"]);
const LATENCY_STATS = ["p50", "p95", "p99"];

/** A serving subject's metrics over the last 24h: latencies as their percentiles, everything else as is. */
export function useServingSeries(subject: SubjectRef, keys: readonly string[], groupBy?: string) {
  const id = keys.join(",");
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const latency = useMemo(() => keys.filter((k) => LATENCY_KEYS.has(k)), [id]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const plain = useMemo(() => keys.filter((k) => !LATENCY_KEYS.has(k)), [id]);
  const a = useMlSeries(subject, latency, { groupBy, stats: LATENCY_STATS, enabled: latency.length > 0 });
  const b = useMlSeries(subject, plain, { groupBy, enabled: plain.length > 0 });
  const none = { data: { kind: "ok" as const, data: { series: [] } }, error: null, isLoading: false, isFetching: false, refetch: b.refetch };
  return mergeSeries(latency.length ? a : none, plain.length ? b : none);
}

/**
 * A small multiple that says "not applicable" with the reason when the metric
 * has no meaning for this service (Triton has no TTFT), and draws otherwise.
 */
export function SeriesTile({
  title,
  series,
  events,
  unit,
  note,
  area,
  tones,
  value,
  height = 120,
  legend,
  bars,
  testId,
}: {
  title: string;
  series: readonly MlSeries[];
  events?: readonly MlEvent[];
  unit?: string;
  note?: ReactNode;
  area?: "fill" | "stacked";
  tones?: readonly (MlTone | undefined)[];
  value?: ReactNode;
  height?: number;
  legend?: boolean;
  bars?: boolean;
  testId?: string;
}) {
  const na = notApplicable(series);
  if (na) {
    return (
      <div className="min-w-0 rounded-xl border border-border bg-surface px-3 pb-3 pt-2.5" data-testid={testId}>
        <h3 className="mb-1 truncate text-ui font-medium text-ink">{title}</h3>
        <div className="flex items-center justify-center rounded-md border border-dashed border-border bg-surface-sunken px-3 text-center text-meta text-ink-muted" style={{ minHeight: height }}>
          Not applicable: {na}
        </div>
      </div>
    );
  }
  const u = unit ?? series[0]?.unit ?? "";
  const latest = value ?? (series.length === 1 ? fmtUnit(lastValue(series[0]), u) : undefined);
  return (
    <div className="min-w-0 rounded-xl border border-border bg-surface px-3 pb-2 pt-2.5" data-testid={testId}>
      <div className="mb-1 flex items-baseline justify-between gap-2">
        <h3 className="truncate text-ui font-medium text-ink">{title}</h3>
        {latest !== undefined && <span className="shrink-0 text-ui tabular-nums text-ink">{latest}</span>}
      </div>
      {note && <p className="-mt-0.5 mb-1 truncate text-meta text-ink-muted">{note}</p>}
      <MlChart series={series} events={events} label={title} height={height} syncKey={SYNC} unit={unit} tones={tones} area={area} bars={bars} legend={legend ?? series.length > 1} what={title} />
    </div>
  );
}
