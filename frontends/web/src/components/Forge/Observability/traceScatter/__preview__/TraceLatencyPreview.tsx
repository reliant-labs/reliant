// Copyright (c) 2025 Reliant Labs

/**
 * Proving harness for the traces page's latency chart. NOT a product surface.
 *
 * The chart alone, inside the real console chrome, in every state it has —
 * a busy window (2,000 traces: a fast body, a slow tail, a burst of failures),
 * the newest page of many, a single trace, a brushed region, loading, error and
 * empty — so each can be looked at in every colour scheme and screenshotted.
 * The whole page (chart, list and the brush narrowing the list) is the
 * Observability preview's traces view: /forge-observability-preview?view=traces.
 *
 * `?case=<id>` picks the state, so a screenshot is a link. Dev builds only:
 * the route sits behind DevOnlyRoute in routes.tsx.
 */

import { useMemo, useState } from "react";

import PageHeader from "@/components/forge-ui/page_header";

import { ConsolePreviewShell } from "../../../Console/ConsolePreviewShell";
import { TraceLatencyChart, type TraceLatencyChartProps } from "../TraceLatencyChart";
import type { TracePoint, TraceRegion } from "../traceScatterModel";

const SCHEMES = ["professional-blue", "refined-neutral", "modern-teal", "slate", "purple-classic", "pure-black"] as const;

const END = Date.UTC(2026, 9, 25, 15, 0, 0);
const HOUR = 3_600_000;

/** A seeded generator, so every screenshot of a case is the same picture. */
function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 2 ** 32;
  };
}

/** A log-normal body around `median` ms with a long tail, a failure burst at 40 minutes in, and a slow deploy at 20. */
function busyWindow(count: number, spanMs: number, seed = 7): TracePoint[] {
  const random = rng(seed);
  const gauss = () => Math.sqrt(-2 * Math.log(random() || 1e-9)) * Math.cos(2 * Math.PI * random());
  return Array.from({ length: count }, (_, i) => {
    const startMs = END - random() * spanMs;
    const minute = (END - startMs) / 60_000;
    const slowPath = random() < 0.18;
    const median = slowPath ? 220 : 24;
    const deploy = minute > 18 && minute < 22 ? 4 : 1;
    const durationMs = Math.max(0.05, median * deploy * Math.exp(0.55 * gauss()));
    const burst = minute > 38 && minute < 41;
    const error = random() < (burst ? 0.35 : 0.015);
    return {
      traceId: `t${i.toString(16).padStart(8, "0")}`,
      startMs,
      durationMs: error && random() < 0.5 ? durationMs * 6 : durationMs,
      error,
      label: slowPath ? "api orders.v1.OrdersService/ListOrders" : "api orders.v1.OrdersService/GetOrder",
    };
  });
}

const BUSY = busyWindow(2000, HOUR);
const NEWEST = busyWindow(100, 4 * 60_000, 11);
const REGION: TraceRegion = { startMs: END - 42 * 60_000, endMs: END - 37 * 60_000, minMs: 40, maxMs: 4000 };

type Case = { id: string; label: string; props: Partial<TraceLatencyChartProps> };

const CASES: Case[] = [
  { id: "busy", label: "busy hour: 2,000 traces, every one loaded", props: { points: BUSY, complete: true, total: BUSY.length } },
  { id: "newest", label: "the newest 100 of 12,480 (older pages not loaded)", props: { points: NEWEST, complete: false, total: 12_480 } },
  { id: "region", label: "a brushed region (the failure burst)", props: { points: BUSY, complete: true, total: BUSY.length, region: REGION } },
  { id: "one", label: "a single trace", props: { points: BUSY.slice(0, 1), complete: true, total: 1 } },
  { id: "all-failed", label: "every trace failed", props: { points: BUSY.slice(0, 60).map((p) => ({ ...p, error: true })), complete: true, total: 60 } },
  { id: "loading", label: "loading the first page", props: { status: "loading", points: [] } },
  { id: "error", label: "the trace query failed", props: { status: "error", points: [] } },
  { id: "empty", label: "nothing traced in the window", props: { points: [] } },
  { id: "empty-filtered", label: "nothing matches the filters", props: { points: [], filtered: true } },
];

export default function TraceLatencyPreview() {
  const initial = new URLSearchParams(window.location.search);
  const [caseId, setCaseId] = useState(() => initial.get("case") ?? CASES[0].id);
  const [scheme, setScheme] = useState(() => document.documentElement.getAttribute("data-color-scheme") ?? "professional-blue");
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));
  const active = CASES.find((c) => c.id === caseId) ?? CASES[0];
  const [region, setRegion] = useState<TraceRegion | null>(null);
  const [opened, setOpened] = useState<string | null>(null);

  const props: TraceLatencyChartProps = useMemo(
    () => ({
      status: "ready",
      points: [],
      window: { startMs: END - HOUR, endMs: END },
      complete: true,
      total: null,
      filtered: false,
      region: null,
      onRegion: () => {},
      onOpen: () => {},
      ...active.props,
    }),
    [active]
  );

  return (
    <ConsolePreviewShell
      page={{ kind: "obs-view", view: "traces" }}
      controls={
        <>
          <select
            data-testid="preview-case"
            value={caseId}
            onChange={(e) => {
              setCaseId(e.target.value);
              setRegion(null);
            }}
            className="rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink"
          >
            {CASES.map((c) => (
              <option key={c.id} value={c.id}>
                {c.label}
              </option>
            ))}
          </select>
          <select
            data-testid="preview-scheme"
            value={scheme}
            onChange={(e) => {
              setScheme(e.target.value);
              document.documentElement.setAttribute("data-color-scheme", e.target.value);
            }}
            className="rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink"
          >
            {SCHEMES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
          <button
            type="button"
            data-testid="preview-dark"
            onClick={() => {
              setDark(!dark);
              document.documentElement.classList.toggle("dark", !dark);
            }}
            className="rounded-md border border-border bg-surface px-3 py-1 text-sm text-ink"
          >
            {dark ? "Dark" : "Light"}
          </button>
        </>
      }
    >
      <div className="space-y-3" data-testid="trace-latency-preview">
        <PageHeader title="Traces" meta={<span>Latency chart states</span>} className="mb-0" />
        <TraceLatencyChart
          key={caseId}
          {...props}
          region={region ?? props.region}
          onRegion={setRegion}
          onOpen={setOpened}
        />
        <p className="text-xs text-ink-muted" data-testid="preview-last-action">
          {opened ? `Opened trace ${opened}.` : region ? `Region: ${new Date(region.startMs).toISOString()} to ${new Date(region.endMs).toISOString()}, ${region.minMs.toFixed(1)}ms to ${region.maxMs.toFixed(1)}ms.` : "Click a dot, or drag a region."}
        </p>
      </div>
    </ConsolePreviewShell>
  );
}
