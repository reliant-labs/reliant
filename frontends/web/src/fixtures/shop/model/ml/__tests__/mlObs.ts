// Copyright (c) 2026 Reliant Labs

/**
 * The ML world's telemetry as the ML tab reads it, for tests: the shop
 * pack's observability model with the ML world on it (../telemetry.ts),
 * asked the way kit/telemetry.ts asks (subject → attribute filters, keys →
 * a metrics panel) and answered as the views receive it (MlSeries, MlEvent).
 */

import type { MlEventKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { eventOfRecord, seriesOfPanel, seriesPanel } from "@/components/Forge/Ml/kit/telemetry";
import { eventQuery, subjectFilters, type MlSignal, type SubjectRef } from "@/services/controlPlane/ml/telemetry";

import { createShopApi } from "../../shopApi";
import { withMlTelemetry } from "../telemetry";

const DAY = 86_400_000;

export function mlObs(nowMs: number) {
  const now = () => nowMs;
  const api = withMlTelemetry(createShopApi(nowMs), now);
  const window = { startMs: nowMs - 30 * DAY, endMs: nowMs + 1 };
  const scope = (subject: SubjectRef, signal: MlSignal) => ({ orgId: "org", envIds: [], filters: subjectFilters(subject, signal) });
  return {
    api,
    window,
    scope,
    /** QueryPanel of `keys` on `subject`, as useMlSeries answers it. */
    series: async (subject: SubjectRef, keys: string[], opts: { groupBy?: string; stats?: string[] } = {}) =>
      seriesOfPanel(await api.queryPanel({ ...scope(subject, "metrics"), window, panel: seriesPanel(keys, opts), services: [], stepSeconds: 0 }), keys, opts.stats),
    /** SearchLogs over the subject's ML event records, as useMlEvents answers it. */
    events: async (subject: SubjectRef, kinds: MlEventKind[] = []) =>
      (await api.searchLogs({ ...scope(subject, "logs"), window, query: eventQuery(kinds), services: [], severities: [], limit: 1000 })).records.map(eventOfRecord),
    logs: async (subject: SubjectRef, query = "") => (await api.searchLogs({ ...scope(subject, "logs"), window, query, services: [], severities: [], limit: 5000 })).records,
    traces: async (subject: SubjectRef) => (await api.listTraces({ ...scope(subject, "traces"), window, rootSpanName: "", services: [], errorsOnly: false, minDurationMs: 0, limit: 500 })).traces,
    trace: (traceId: string) => api.getTrace({ orgId: "org", envIds: [], filters: [], window, traceId }),
  };
}
