// Copyright (c) 2026 Reliant Labs

/**
 * The simple view's traces, session recordings and alerts: first-class here,
 * not hidden behind the Advanced view.
 *
 *   Slow requests  (traces)    SimpleSlow: the slowest real request of the
 *                              last hour drawn inline as a step-by-step
 *                              timeline (PlainWaterfall), then every kind of
 *                              request, each opening its slowest trace
 *   One trace                  TraceLead above the console's trace page:
 *                              "What happened" and the plain timeline, with
 *                              the technical waterfall under it
 *   Recordings     (sessions)  SimpleRecordings: replays with play buttons;
 *                              SimpleRecording plays one at once, beside what
 *                              the visitor did in words
 *   Alerts                     SimpleAlerts: what is going off, what is
 *                              watched and who it tells, in sentences
 *   One problem    (error)     IssueLead above the console's issue page
 *
 * Every figure is read from the demo's ObservabilityApi (the same reads the
 * console makes), so a trace here is the trace the technical page opens. The
 * technical names stay on screen as a quieter second line (TechLine).
 */

import { useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, ArrowRight, BellRing, CircleAlert, MonitorPlay, Play, Waypoints } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { useSession, useSessionTimeline } from "@/hooks/observability-session-queries";
import { useTrace } from "@/hooks/observability-queries";
import { useObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { SessionSummary, TimelineEntry } from "@/services/controlPlane/observability/rumTypes";
import type { Span } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { ReplayPlayer } from "../../Observability/Sessions/ReplayPlayer";
import { rumSessionIdOf } from "../../Observability/Sessions/sessionModel";
import { buildTraceTree, flattenTree, traceExtent } from "../../Observability/waterfall/traceTree";
import { DEMO_ALERT_ROUTES, DEMO_ALERT_RULES } from "../demoData/consoleScenario";
import { shopAlerting } from "../demoData/shop/alerting";
import { DAY, DEMO_INCIDENT, DEMO_ORG, DEMO_TOURED_TRACE, demoNow, HOUR } from "../demoData/shopStory";
import { PageAgentStrip } from "./AgentCards";
import { DEMO_LINKS, issueHref, ruleHref, sessionHref, traceHref } from "./demoLinks";
import { clock, PLAIN_ALERTS, plainIssue, plainPath, plainStep, routeSpeeds, SHOP_PROD_SCOPE, overallSpeed, useShopIssues, type RouteSpeed } from "./simpleData";
import { ago, CardLink, Code, count, OpenCue, Panel, Section, seconds, ShortList, ShowDetailedView, SimplePage, StatusDot, TechLine, Term, capital } from "./ui";

const RUM_SCOPE = { orgId: SHOP_PROD_SCOPE.orgId, projectId: SHOP_PROD_SCOPE.projectId, envIds: SHOP_PROD_SCOPE.envIds, services: [] };
const shortId = (id: string) => id.slice(0, 8);

const BUTTON =
  "inline-flex min-h-11 items-center gap-2 rounded-lg border border-border bg-surface px-4 text-base font-medium text-ink hover:border-primary/50 hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

// ── Reads ───────────────────────────────────────────────────────────────────

/** Each kind of request's slowest trace in the last hour (one of its slowest 1 in 20). */
function useSlowestTraces(routes: RouteSpeed[]) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: ["simple-demo", "slowest", routes.map((r) => r.op)],
    queryFn: async () => {
      const now = demoNow();
      const found = await Promise.all(
        routes.map(async (route) => {
          const res = await api.listTraces({
            ...SHOP_PROD_SCOPE,
            window: { startMs: now - HOUR, endMs: now },
            rootSpanName: route.op,
            services: [],
            errorsOnly: false,
            minDurationMs: Math.floor(route.slowMs),
            limit: 50,
          });
          const slowest = [...res.traces].sort((a, b) => b.durationMs - a.durationMs)[0];
          return [route.op, slowest ?? null] as const;
        }),
      );
      return new Map(found);
    },
    staleTime: Infinity,
  });
}

/** shop-web's recorded visits in production, newest first, over the last day. */
export function useRecordings() {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: ["simple-demo", "recordings"],
    queryFn: async () => {
      const now = demoNow();
      const res = await api.listSessions({ ...RUM_SCOPE, window: { startMs: now - DAY, endMs: now }, hasError: false, hasReplay: true, userId: "", pathContains: "", pageSize: 50 });
      return res.sessions;
    },
    staleTime: Infinity,
  });
}

// ── A request, step by step ─────────────────────────────────────────────────

interface Step {
  id: string;
  label: string;
  span: Span;
  level: number;
  /** Identical steps in a row (a statement per line item) are one row: how many, and from the first's start to the last's end. */
  times: number;
  startMs: number;
  endMs: number;
}

function stepsOf(spans: Span[]): Step[] {
  const tree = buildTraceTree(spans);
  const server = serverSpanOf(spans);
  const steps: Step[] = [];
  for (const { node, level } of flattenTree(tree.roots, new Set())) {
    const span = node.span;
    const prev = steps[steps.length - 1];
    if (prev && prev.level === level && prev.span.name === span.name && prev.span.service === span.service && prev.span.status === span.status && span.parentSpanId === prev.span.parentSpanId) {
      prev.times += 1;
      prev.endMs = Math.max(prev.endMs, span.startMs + span.durationMs);
      continue;
    }
    steps.push({ id: node.id, label: plainStep(span, span === server), span, level, times: 1, startMs: span.startMs, endMs: span.startMs + span.durationMs });
  }
  return steps;
}

/** The span that IS the request on the server: the root, or the server span under the browser's. */
function serverSpanOf(spans: Span[]): Span | undefined {
  const root = spans.find((s) => !s.parentSpanId);
  if (root?.service !== "shop-web") return root ?? spans[0];
  return spans.find((s) => s.parentSpanId === root.spanId && s.kind === "server") ?? root;
}

/**
 * Where the time went: the longest step inside the request (not the request
 * itself, nor the browser waiting on it). A step whose own child took nearly
 * all of it names the parent ("Charge the card"), not the attempt under it.
 */
function bottleneckOf(spans: Span[]): Span | null {
  const server = serverSpanOf(spans);
  const outer = new Set(spans.filter((s) => !s.parentSpanId || s === server).map((s) => s.spanId));
  const inner = spans.filter((s) => !outer.has(s.spanId));
  return inner.sort((a, b) => b.durationMs - a.durationMs)[0] ?? null;
}

/** The request in one or two plain sentences. */
export function whatHappened(spans: Span[]): { title: string; sentence: string; failed: boolean } {
  const extent = traceExtent(spans);
  const total = extent?.totalDurationMs ?? 0;
  const server = serverSpanOf(spans);
  const name = server ? plainStep(server, true) : "This request";
  const failed = spans.some((s) => s.status === "error");
  const neck = bottleneckOf(spans);
  const share = neck && total ? Math.round((neck.durationMs / total) * 100) : 0;
  const errorLeaf = spans.filter((s) => s.status === "error").sort((a, b) => b.startMs - a.startMs)[0];
  const timedOut = !!errorLeaf && /time|deadline/i.test(errorLeaf.statusMessage);
  // A retry: an attempt that timed out (an event, or an errored attempt), then another.
  const retried = spans.some((s) => s.events.some((e) => e.name === "retry" || e.name === "timeout"));
  const parts = [
    neck && share >= 25
      ? `The longest step was “${plainStep(neck, false).toLowerCase()}”: ${seconds(neck.durationMs)} of the ${seconds(total)}.`
      : "The time was spread across its steps; no single one stands out.",
    retried && "The payment provider didn't answer the first time, so the shop waited and asked again.",
    failed
      ? timedOut
        ? `${retried ? "The second try" : "It"} ran past the 3-second time limit, so the customer saw an error.`
        : "One of its steps failed, so the customer saw an error."
      : "It worked in the end; the customer just waited.",
  ].filter(Boolean);
  return { title: `${name} took ${seconds(total)}${failed ? " and failed" : ""}`, sentence: parts.join(" "), failed };
}

/**
 * A trace as a short, readable timeline: one row per step in plain words, the
 * raw span name and service under it, a bar on the request's own clock. Red
 * means the step failed. The full technical waterfall is the trace's page.
 */
export function PlainWaterfall({ spans, maxRows = 40, traceId }: { spans: Span[]; maxRows?: number; traceId?: string }) {
  const steps = useMemo(() => stepsOf(spans), [spans]);
  const extent = useMemo(() => traceExtent(spans), [spans]);
  const total = extent?.totalDurationMs ?? 0;
  const origin = extent?.startMs ?? 0;
  const shown = steps.slice(0, maxRows);
  const neck = useMemo(() => bottleneckOf(spans), [spans]);
  return (
    <div className="space-y-3" data-testid="plain-waterfall">
      <ol className="divide-y divide-border/60 overflow-hidden rounded-xl border border-border bg-surface-sunken/40" aria-label="The request's steps, in order">
        {shown.map((step) => {
          const failed = step.span.status === "error";
          const slow = neck?.spanId === step.span.spanId && step.level > 0;
          const left = total ? ((step.startMs - origin) / total) * 100 : 0;
          const width = total ? Math.max(0.6, ((step.endMs - step.startMs) / total) * 100) : 100;
          const took = step.endMs - step.startMs;
          return (
            <li key={step.id} className="grid gap-x-4 gap-y-1.5 px-4 py-2.5 sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)] sm:items-center" data-testid="plain-step">
              <div className="min-w-0" style={{ paddingLeft: `${Math.min(step.level, 5) * 0.75}rem` }}>
                <p className={cn("text-base leading-snug", failed ? "font-medium text-danger-ink" : slow ? "font-semibold text-ink" : "text-ink")}>
                  {step.label}
                  {step.times > 1 && <span className="font-normal text-ink-muted"> ×{step.times}</span>}
                  {failed && <span className="font-normal"> — failed</span>}
                </p>
                <p className="truncate font-mono text-xs text-ink-subtle" title={`${step.span.service} · ${step.span.name}`}>
                  {step.span.service} · {step.span.name}
                </p>
              </div>
              <div className="flex items-center gap-3">
                <div className="relative h-5 flex-1" aria-hidden="true">
                  <div className="absolute inset-y-1.5 left-0 right-0 rounded-full bg-surface-sunken" />
                  <div
                    className={cn("absolute inset-y-0.5 rounded-full", failed ? "bg-danger" : slow ? "bg-warning" : "bg-primary/60")}
                    style={{ left: `${Math.min(left, 99.4)}%`, width: `${Math.min(width, 100 - Math.min(left, 99.4))}%` }}
                  />
                </div>
                <span className={cn("w-12 shrink-0 text-right text-sm tabular-nums", failed ? "text-danger-ink" : slow ? "font-semibold text-ink" : "text-ink-muted")}>
                  {took >= 1000 ? seconds(took) : `${Math.max(1, Math.round(took))}ms`}
                </span>
              </div>
            </li>
          );
        })}
      </ol>
      <div className="flex flex-wrap items-center justify-between gap-3 text-sm text-ink-muted">
        <span className="flex flex-wrap items-center gap-x-4 gap-y-1">
          <Legend tone="bg-primary/60" label="A step" />
          <Legend tone="bg-warning" label="Where the time went" />
          <Legend tone="bg-danger" label="Failed" />
        </span>
        {steps.length > shown.length && traceId && (
          <Link href={traceHref(traceId)} className="font-medium text-primary-ink hover:underline">
            {steps.length - shown.length} more steps in the full timeline
          </Link>
        )}
      </div>
    </div>
  );
}

function Legend({ tone, label }: { tone: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={cn("h-2.5 w-4 rounded-full", tone)} aria-hidden="true" />
      {label}
    </span>
  );
}

// ── Slow requests (traces) ──────────────────────────────────────────────────

export function SimpleSlow() {
  const routes = useMemo(() => routeSpeeds().filter((r) => r.requests > 0), []);
  const overall = useMemo(() => overallSpeed(), []);
  const slowest = routes[0];
  const max = Math.max(...routes.map((r) => r.slowMs), 1);
  const traces = useSlowestTraces(routes);
  const top = slowest ? traces.data?.get(slowest.op) : undefined;
  return (
    <SimplePage
      testId="simple-slow"
      title="Slow requests"
      term="traces"
      tone={slowest && slowest.slowMs > 1000 ? "warn" : "good"}
      summary={
        slowest && slowest.slowMs > 1000 ? (
          <>
            Most things in the shop answer in {seconds(overall.typicalMs)}. <strong className="font-semibold">{slowest.name} is the slow one:</strong> 1 in 20 takes {seconds(slowest.slowMs)} today, up from {seconds(slowest.slowYesterdayMs)} yesterday. Below is one of those slow requests, step by step.
          </>
        ) : (
          <>All good: everything answers in {seconds(overall.typicalMs)}, the same as yesterday.</>
        )
      }
    >
      <Section title="The slowest request right now" about="One real request from the last hour, step by step (a trace). Each bar is one step on the request's own clock.">
        {top ? <TopTrace traceId={top.traceId} startMs={top.startMs} /> : <Panel className="text-base text-ink-muted">{traces.isLoading ? "Finding the slowest request…" : "No slow requests in the last hour."}</Panel>}
      </Section>

      <Section title="Every kind of request, slowest first" about="How long customers wait, over the last hour. The bar is the slowest 1 in 20 visits; open one to see its slowest request.">
        <ShortList
          items={routes}
          noun="kinds of request"
          render={(route) => {
            const trace = traces.data?.get(route.op);
            return (
              <CardLink key={route.op} href={trace ? traceHref(trace.traceId) : DEMO_LINKS.traces} cue={trace ? "See the slowest" : undefined} testId={`simple-route-${route.op}`}>
                <span className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                  <span className="text-lg font-semibold text-ink">{route.name}</span>
                  <span className="text-base text-ink">
                    usually {seconds(route.typicalMs)} · slowest {seconds(route.slowMs)}
                  </span>
                </span>
                <span className="mt-3 block h-3 w-full overflow-hidden rounded-full bg-surface-sunken" aria-hidden="true">
                  <span className={cn("block h-full rounded-full", route.slowMs > 1000 ? "bg-warning" : "bg-primary/60")} style={{ width: `${Math.max(3, (route.slowMs / max) * 100)}%` }} />
                </span>
                {route.slowMs > route.slowYesterdayMs * 1.5 && <span className="mt-2 block text-base text-warning-ink">Slower than yesterday ({seconds(route.slowYesterdayMs)})</span>}
                <TechLine
                  items={[
                    <Code key="op">{route.op}</Code>,
                    route.service,
                    `p50 ${Math.round(route.typicalMs)}ms / p95 ${Math.round(route.slowMs)}ms`,
                    `${count(route.requests)} requests in the last hour`,
                    route.errorRate > 0 && <span className="text-danger-ink">{(route.errorRate * 100).toFixed(1)}% errors</span>,
                  ]}
                />
              </CardLink>
            );
          }}
        />
      </Section>
      <PageAgentStrip id="slow-pages" lead="We spent three months guessing at slow pages. Handing an agent one trace like the one above found the cause in an afternoon." />
      <ShowDetailedView what="trace search and filters" />
    </SimplePage>
  );
}

function TopTrace({ traceId, startMs }: { traceId: string; startMs: number }) {
  const trace = useTrace(SHOP_PROD_SCOPE, traceId, "24h");
  const spans = trace.data?.spans ?? [];
  if (!spans.length) return <Panel className="text-base text-ink-muted">{trace.isLoading ? "Loading its steps…" : "That request's steps are no longer kept."}</Panel>;
  const what = whatHappened(spans);
  return (
    <Panel className="space-y-4" data-testid="simple-top-trace">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <p className="flex items-center gap-2 text-lg font-semibold text-ink">
            <StatusDot tone={what.failed ? "bad" : "warn"} />
            {what.title}
          </p>
          <p className="max-w-3xl text-base leading-relaxed text-ink">{what.sentence}</p>
        </div>
        <Link href={traceHref(traceId)} className={BUTTON} data-testid="simple-top-trace-open">
          <Waypoints className="h-4 w-4" aria-hidden="true" />
          Open the full timeline
        </Link>
      </div>
      <PlainWaterfall spans={spans} maxRows={8} traceId={traceId} />
      <TechLine items={[<>trace <Code>{shortId(traceId)}</Code></>, `${spans.length} spans`, `${spans.filter((s) => s.status === "error").length} errors`, `started ${ago(demoNow() - startMs)}`]} />
    </Panel>
  );
}

/** One trace in the simple view: what happened and its steps in words, above the console's technical page. */
export function TraceLead({ traceId }: { traceId: string }) {
  const trace = useTrace(SHOP_PROD_SCOPE, traceId, "7d");
  const spans = trace.data?.spans ?? [];
  if (!spans.length) return null;
  const what = whatHappened(spans);
  const sessionId = rumSessionIdOf(spans);
  return (
    <section className="mx-auto w-full max-w-5xl space-y-5 pb-4" data-testid="simple-trace-lead" aria-labelledby="trace-lead-title">
      <BackLink href={DEMO_LINKS.traces} label="All slow requests" />
      <header className="space-y-3">
        <h1 id="trace-lead-title" className="text-3xl font-semibold tracking-tight text-ink">
          {what.title}
          <Term className="ml-2 text-2xl">trace</Term>
        </h1>
        <p className="flex max-w-3xl items-start gap-3 text-lg leading-relaxed text-ink">
          <StatusDot tone={what.failed ? "bad" : "warn"} className="mt-2.5" />
          <span>
            <strong className="font-semibold">What happened: </strong>
            {what.sentence}
          </span>
        </p>
      </header>
      <PlainWaterfall spans={spans} traceId={traceId} />
      {sessionId && (
        <Link href={sessionHref(sessionId)} className={BUTTON}>
          <MonitorPlay className="h-4 w-4" aria-hidden="true" />
          Watch what the customer saw (recording)
        </Link>
      )}
      <p className="border-t border-border pt-5 text-base text-ink-muted">Below is the same request as an engineer sees it: every step's technical name, timing and details. Click a step to see them.</p>
    </section>
  );
}

// ── Recordings (sessions) ───────────────────────────────────────────────────

/** A visit in words: "A visitor on the checkout page". */
const visitTitle = (s: SessionSummary) => `${s.userId ? "A signed-in customer" : "A visitor"} on ${plainPath(s.entryPath)}`;
const visitWhere = (s: SessionSummary) => [s.city, s.client.browserName && `${s.client.browserName}${s.client.osName ? ` on ${s.client.osName}` : ""}`].filter(Boolean).join(" · ");

export function SimpleRecordings() {
  const recordings = useRecordings();
  const all = recordings.data ?? [];
  const hit = all.filter((s) => s.errors > 0);
  const now = demoNow();
  // The visits that hit the broken checkout first: they are the ones worth watching.
  const ordered = [...hit, ...all.filter((s) => s.errors === 0)];
  return (
    <SimplePage
      testId="simple-recordings"
      title="Recordings"
      term="sessions"
      tone={hit.length ? "bad" : "good"}
      summary={
        recordings.isLoading ? (
          "Loading today's visits…"
        ) : hit.length ? (
          <>
            {count(all.length)} visits to the shop were recorded today. <strong className="font-semibold">{hit.length} of them hit the broken checkout</strong>: press play on one to watch exactly what the customer saw.
          </>
        ) : (
          <>{count(all.length)} visits were recorded today, and none of them hit an error.</>
        )
      }
    >
      <Section title="Today's visits" about="Each one is a replay of a real visit, newest first, with the ones that hit a problem on top. Typing and personal details are hidden before anything is recorded.">
        <ShortList
          items={ordered}
          limit={6}
          noun="recordings"
          render={(s) => (
            <Link key={s.sessionId} href={sessionHref(s.sessionId)} className="group flex items-center gap-4 rounded-2xl border border-border bg-surface p-4 transition-colors hover:border-primary/50 hover:bg-ink/[0.02] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:p-5" data-testid={`simple-recording-${s.sessionId}`}>
              <ReplayThumb failed={s.errors > 0} />
              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-center gap-2">
                  {s.errors > 0 && <span className="rounded-full border border-danger-border bg-danger-surface px-2 py-0.5 text-sm font-medium text-danger-ink">Hit an error</span>}
                  <span className="text-lg font-semibold text-ink">{visitTitle(s)}</span>
                </span>
                <span className="mt-1 block text-base text-ink-muted">
                  {ago(now - s.startedMs)} · watched for {clock(s.endedMs - s.startedMs)} · {s.pageViews} page{s.pageViews === 1 ? "" : "s"}
                  {s.errors > 0 ? " · their payment failed" : ""}
                </span>
                <TechLine items={[<>session <Code>{s.sessionId}</Code></>, <Code key="p">{s.entryPath}</Code>, visitWhere(s), s.release, `${s.interactions} interactions`]} className="mt-1" />
              </span>
              <span className="max-sm:hidden">
                <OpenCue label="Play" />
              </span>
            </Link>
          )}
        />
      </Section>
      <PageAgentStrip id="fix-errors" lead="An agent can watch the recordings of failed visits, tie each one to its request and error, and draft the fix." />
      <ShowDetailedView what="session list and filters" />
    </SimplePage>
  );
}

/** A small "video" with a big play button: the recording is the thing to click. */
function ReplayThumb({ failed }: { failed: boolean }) {
  return (
    <span className="relative flex h-16 w-24 shrink-0 items-center justify-center overflow-hidden rounded-xl border border-border bg-surface-sunken sm:h-20 sm:w-32" aria-hidden="true">
      <span className="absolute left-2 right-2 top-2 h-1.5 rounded-full bg-ink/10" />
      <span className="absolute left-2 top-5 h-1.5 w-1/2 rounded-full bg-ink/10" />
      <span className="absolute bottom-2 left-2 right-6 h-1.5 rounded-full bg-ink/10" />
      {failed && <span className="absolute bottom-2 right-2 h-2.5 w-2.5 rounded-full bg-danger" />}
      <span className="relative inline-flex h-10 w-10 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-sm transition-transform group-hover:scale-110">
        <Play className="ml-0.5 h-5 w-5" fill="currentColor" />
      </span>
    </span>
  );
}

/** One timeline line in words, with its technical form and where it leads. */
function plainMoment(e: TimelineEntry): { text: string; tone: "bad" | "neutral" | "good"; tech: ReactNode[] } {
  switch (e.kind) {
    case "navigation":
      return { text: e.name === "documentLoad" ? `Opened ${plainPath(e.path)}` : `Went to ${plainPath(e.path)}`, tone: "neutral", tech: [<Code key="n">{e.name}</Code>, <Code key="p">{e.path}</Code>, e.durationMs > 0 && `${Math.round(e.durationMs)}ms`] };
    case "click":
      return { text: e.element === "INPUT" ? "Clicked into a text field" : e.element === "BUTTON" ? "Pressed a button" : "Clicked", tone: "neutral", tech: [<Code key="c">click {e.element.toLowerCase()}</Code>] };
    case "request_failed":
      return {
        text: e.path.includes("Checkout") ? "Their payment failed: the shop took too long to answer" : "A request to the shop failed",
        tone: "bad",
        tech: [<Code key="r">{`${e.method} ${e.path}`}</Code>, e.status ? `HTTP ${e.status}` : "network error", e.durationMs > 0 && `${seconds(e.durationMs)}`, e.message && <Code key="m">{e.message}</Code>],
      };
    case "error":
      return { text: "The page hit an error", tone: "bad", tech: [<Code key="e">{e.errorType || e.name}</Code>, e.message && <Code key="m">{e.message}</Code>] };
    case "event":
      return {
        text: e.name === "purchase" ? `Bought ${e.props.items ?? "an"} item${e.props.items === "1" ? "" : "s"}` : e.name === "checkout_retry" ? "Tried to pay again" : `Did “${e.name.replace(/_/g, " ")}”`,
        tone: e.name === "purchase" ? "good" : "neutral",
        tech: [<Code key="ev">event {e.name}</Code>, ...Object.entries(e.props).map(([k, v]) => <Code key={k}>{`${k}=${v}`}</Code>)],
      };
    default:
      return { text: e.name || "Something happened", tone: "neutral", tech: [] };
  }
}

/** One recording: it plays at once, with what the visitor did in words beside it. */
export function SimpleRecording({ sessionId, atMs }: { sessionId: string; atMs?: number }) {
  const session = useSession(RUM_SCOPE, sessionId);
  const timeline = useSessionTimeline(RUM_SCOPE, sessionId);
  const entries = useMemo(() => timeline.data?.pages.flatMap((page) => page.entries) ?? [], [timeline.data]);
  const [seek, setSeek] = useState<{ atMs: number; nonce: number } | undefined>(undefined);
  const s = session.data;
  if (!s) {
    return (
      <div className="mx-auto w-full max-w-5xl space-y-4 pt-2">
        <BackLink href={DEMO_LINKS.sessions} label="All recordings" />
        <Panel className="text-base text-ink-muted">{session.isLoading ? "Loading the recording…" : "That recording isn't here any more."}</Panel>
      </div>
    );
  }
  const failed = entries.find((e) => e.kind === "request_failed");
  const bought = entries.some((e) => e.kind === "event" && e.name === "purchase");
  const summary = failed
    ? `They opened ${plainPath(s.entryPath)}, entered their card and pressed Pay. ${clock(failed.timeMs - s.startedMs)} in, the payment failed and the page showed an error, so they tried again.`
    : bought
      ? `They opened ${plainPath(s.entryPath)} and bought something. Nothing went wrong.`
      : `They opened ${plainPath(s.entryPath)} and looked around for ${clock(s.endedMs - s.startedMs)}. Nothing went wrong.`;
  return (
    <SimplePage testId="simple-recording" title={visitTitle(s)} term="session replay" tone={failed ? "bad" : "good"} summary={summary}>
      <div className="-mt-4 flex flex-wrap items-center gap-3">
        <BackLink href={DEMO_LINKS.sessions} label="All recordings" />
        <TechLine className="mt-0" items={[<>session <Code>{s.sessionId}</Code></>, visitWhere(s), s.release && <>release <Code>{s.release}</Code></>, `${ago(demoNow() - s.startedMs)}`]} />
      </div>
      <div className="grid gap-6 lg:grid-cols-[minmax(0,3fr)_minmax(18rem,2fr)]">
        <Panel className="self-start p-3 sm:p-4" data-testid="simple-recording-player">
          {s.hasReplay ? (
            <ReplayPlayer key={s.sessionId} scope={RUM_SCOPE} session={s} live={false} startAtMs={atMs} seek={seek} markers={entries} autoPlay />
          ) : (
            <p className="p-4 text-base text-ink-muted">This visit was not recorded on video; what they did is listed beside it.</p>
          )}
        </Panel>
        <section className="space-y-3" aria-labelledby="recording-steps">
          <h2 id="recording-steps" className="text-xl font-semibold text-ink">
            What they did
          </h2>
          <ol className="space-y-2">
            {entries.map((e, index) => {
              const m = plainMoment(e);
              return (
                <li key={`${e.timeMs}-${index}`} className={cn("rounded-xl border bg-surface p-3", m.tone === "bad" ? "border-danger-border" : "border-border")}>
                  <button
                    type="button"
                    onClick={() => setSeek((prev) => ({ atMs: e.timeMs, nonce: (prev?.nonce ?? 0) + 1 }))}
                    className="flex w-full items-start gap-3 rounded-lg text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    title="Jump the recording to this moment"
                  >
                    <span className="mt-0.5 w-11 shrink-0 rounded-md bg-surface-sunken px-1.5 py-0.5 text-center text-sm tabular-nums text-ink-muted">{clock(e.timeMs - s.startedMs)}</span>
                    <span className={cn("text-base", m.tone === "bad" ? "font-medium text-danger-ink" : "text-ink")}>{m.text}</span>
                  </button>
                  <TechLine className="mt-1 pl-14" items={m.tech} />
                  {(e.traceId || e.issueId) && (
                    <span className="mt-2 flex flex-wrap gap-2 pl-14">
                      {e.traceId && (
                        <Link href={traceHref(e.traceId)} className="inline-flex min-h-9 items-center gap-1.5 rounded-lg border border-border px-3 text-sm font-medium text-ink hover:border-primary/50 hover:bg-ink/5">
                          <Waypoints className="h-3.5 w-3.5" aria-hidden="true" />
                          The request, step by step (trace)
                        </Link>
                      )}
                      {e.issueId && (
                        <Link href={issueHref(e.issueId)} className="inline-flex min-h-9 items-center gap-1.5 rounded-lg border border-border px-3 text-sm font-medium text-ink hover:border-primary/50 hover:bg-ink/5">
                          <CircleAlert className="h-3.5 w-3.5" aria-hidden="true" />
                          The problem (error)
                        </Link>
                      )}
                    </span>
                  )}
                </li>
              );
            })}
          </ol>
        </section>
      </div>
      {failed && <PageAgentStrip id="fix-errors" lead="An agent can watch this visit, follow the failed payment to its cause, and draft the fix." />}
      <ShowDetailedView what="session page" />
    </SimplePage>
  );
}

function BackLink({ href, label }: { href: string; label: string }) {
  return (
    <Link href={href} className="inline-flex min-h-11 items-center gap-1.5 rounded-lg px-2 text-base font-medium text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
      <ArrowLeft className="h-4 w-4" aria-hidden="true" />
      {label}
    </Link>
  );
}

// ── One problem (error) ─────────────────────────────────────────────────────

/** One problem in the simple view: what it means and where to see it, above the console's issue page. */
export function IssueLead({ issueId }: { issueId: string }) {
  const { open, fixed } = useShopIssues();
  const recordings = useRecordings();
  const issue = [...(open ?? []), ...(fixed ?? [])].find((i) => i.id === issueId);
  if (!issue) return null;
  const plain = plainIssue(issue);
  const now = demoNow();
  const isIncident = issue.id === DEMO_INCIDENT.issueId;
  const watch = isIncident ? recordings.data?.find((s) => s.errors > 0) : undefined;
  const resolved = issue.status === "resolved";
  return (
    <section className="mx-auto w-full max-w-5xl space-y-5 pb-4" data-testid="simple-issue-lead" aria-labelledby="issue-lead-title">
      <BackLink href={DEMO_LINKS.errors} label="Everything that broke" />
      <header className="space-y-3">
        <h1 id="issue-lead-title" className="text-3xl font-semibold tracking-tight text-ink">
          {plain.title}
          <Term className="ml-2 text-2xl">error</Term>
        </h1>
        <p className="flex max-w-3xl items-start gap-3 text-lg leading-relaxed text-ink">
          <StatusDot tone={resolved ? "good" : "bad"} className="mt-2.5" />
          <span>
            {plain.why} {resolved ? `It was fixed in ${issue.resolvedVersion}.` : `It has hit ${count(issue.affected)} orders since release ${issue.firstVersion}, ${ago(now - issue.firstSeenMs)}.`}
            {issue.assignee && !resolved ? ` ${capital(issue.assignee.name)} is on it.` : ""}
          </span>
        </p>
        <TechLine items={[<Code key="t">{issue.title}</Code>, <Code key="c">{issue.culprit}</Code>, issue.service, <>id <Code>{issue.id}</Code></>]} />
      </header>
      {isIncident && (
        <div className="flex flex-wrap gap-3">
          {watch && (
            <Link href={sessionHref(watch.sessionId)} className={BUTTON} data-testid="issue-lead-recording">
              <MonitorPlay className="h-4 w-4" aria-hidden="true" />
              Watch a customer hit it (recording)
            </Link>
          )}
          <Link href={traceHref(DEMO_TOURED_TRACE.traceId)} className={BUTTON} data-testid="issue-lead-trace">
            <Waypoints className="h-4 w-4" aria-hidden="true" />
            See one failed order, step by step (trace)
          </Link>
          <Link href={DEMO_LINKS.badRelease} className={BUTTON}>
            The release that caused it
            <ArrowRight className="h-4 w-4" aria-hidden="true" />
          </Link>
        </div>
      )}
      <p className="border-t border-border pt-5 text-base text-ink-muted">Below is the full report engineers work from: every occurrence, the stack trace, and who is on it.</p>
    </section>
  );
}

// ── Alerts ──────────────────────────────────────────────────────────────────

const PLAIN_RULE_TITLES: Record<string, string> = {
  OrdersErrorRateHigh: "Orders are failing",
  ListOrdersSlow: "“Your orders” is slow",
  WorkerBacklog: "Orders are piling up for the background worker",
};

function useShopAlerts() {
  return useQuery({
    queryKey: ["simple-demo", "alerts"],
    queryFn: async () => {
      const api = shopAlerting(demoNow());
      const [firing, history] = await Promise.all([api.listAlerts(DEMO_ORG), api.listAlertHistory(DEMO_ORG, { sinceMs: demoNow() - 14 * DAY })]);
      return { firing, history };
    },
    staleTime: Infinity,
  });
}

export function SimpleAlerts() {
  const alerts = useShopAlerts();
  const firing = alerts.data?.firing ?? [];
  const history = alerts.data?.history ?? [];
  const now = demoNow();
  const receiverOf = (severity: string) => DEMO_ALERT_ROUTES.find((r) => r.match.severity === severity)?.receiver ?? "";
  return (
    <SimplePage
      testId="simple-alerts"
      title="Alerts"
      tone={firing.length ? "bad" : "good"}
      summary={
        alerts.isLoading ? (
          "Checking…"
        ) : firing.length ? (
          <>
            <strong className="font-semibold">{firing.length === 1 ? "1 alert is going off" : `${firing.length} alerts are going off`}:</strong> {PLAIN_RULE_TITLES[firing[0].rule]?.toLowerCase() ?? firing[0].rule}. It went off {ago(now - (firing[0].firingAtMs ?? now))} and told the on-call person. The shop watches {DEMO_ALERT_RULES.length} things in all.
          </>
        ) : (
          <>All quiet: none of the {DEMO_ALERT_RULES.length} things the shop watches is going off.</>
        )
      }
    >
      {firing.length > 0 && (
        <Section title="Going off now">
          <div className="space-y-3">
            {firing.map((a) => (
              <CardLink key={a.fingerprint} href={ruleHref(a.rule)} cue="See the rule" testId={`simple-alert-${a.rule}`}>
                <span className="flex flex-wrap items-center gap-2">
                  <BellRing className="h-5 w-5 text-danger-ink" aria-hidden="true" />
                  <span className="text-lg font-semibold text-ink">{PLAIN_RULE_TITLES[a.rule] ?? a.rule}</span>
                </span>
                <span className="mt-1 block text-base text-ink">
                  Right now {(a.lastValue * 100).toFixed(1)}% of orders fail; the limit is 5%. Going off since {ago(now - (a.firingAtMs ?? now))}.
                </span>
                <TechLine items={[<Code key="r">{a.rule}</Code>, `severity ${a.labels.severity}`, `→ ${receiverOf(a.labels.severity ?? "")}`, `value ${a.lastValue}`, a.envId.replace("env-", "")]} />
              </CardLink>
            ))}
            {firing.some((a) => a.rule === DEMO_INCIDENT.alert) && (
              <Link href={issueHref(DEMO_INCIDENT.issueId)} className={BUTTON}>
                <CircleAlert className="h-4 w-4" aria-hidden="true" />
                Why: the broken checkout (error)
              </Link>
            )}
          </div>
        </Section>
      )}

      <Section title="What the shop watches" about="Each rule is written down in the app's code, ships with it, and is the same in testing and production.">
        <div className="space-y-3">
          {DEMO_ALERT_RULES.map((rule) => {
            const on = firing.some((a) => a.rule === rule.alert);
            const plain = PLAIN_ALERTS[rule.alert];
            return (
              <CardLink key={rule.alert} href={ruleHref(rule.alert)} cue="Open" testId={`simple-rule-${rule.alert}`}>
                <span className="flex flex-wrap items-center gap-2">
                  <StatusDot tone={on ? "bad" : "good"} />
                  <span className="text-lg font-semibold text-ink">{PLAIN_RULE_TITLES[rule.alert] ?? rule.alert}</span>
                  <span className={cn("text-base", on ? "text-danger-ink" : "text-ink-muted")}>{on ? "Going off" : "Quiet"}</span>
                </span>
                <span className="mt-1 block text-base text-ink">
                  {plain?.watches ?? rule.annotations.summary}. {plain?.tells ?? ""}.
                </span>
                <TechLine items={[<Code key="a">{rule.alert}</Code>, `for ${rule.for}`, `${rule.labels.severity} → ${receiverOf(rule.labels.severity)}`]} />
                <span className="mt-1 block truncate font-mono text-xs text-ink-subtle" title={rule.expr}>
                  {rule.expr}
                </span>
              </CardLink>
            );
          })}
        </div>
      </Section>

      {history.length > 0 && (
        <Section title="Recently" about="Every time an alert went off or calmed down, in the last two weeks.">
          <Panel className="divide-y divide-border p-0">
            {history.map((e) => (
              <div key={`${e.rule}-${e.atMs}-${e.transition}`} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-5 py-3">
                <StatusDot tone={e.transition === "firing" ? "bad" : e.transition === "resolved" ? "good" : "warn"} />
                <span className="flex-1 text-base text-ink">
                  {PLAIN_RULE_TITLES[e.rule] ?? e.rule}: {e.transition === "firing" ? "went off" : e.transition === "resolved" ? "calmed down" : "getting close"}
                  <span className="text-ink-muted"> · {e.envId === "env-prod" ? "production" : "testing"}</span>
                </span>
                <span className="text-base text-ink-muted">{ago(now - e.atMs)}</span>
                <TechLine className="mt-0 basis-full pl-5" items={[<Code key="r">{e.rule}</Code>, e.transition, `value ${e.value}`]} />
              </div>
            ))}
          </Panel>
        </Section>
      )}
      <PageAgentStrip id="watch-alerts" lead="When an alert goes off at 2am, an agent can look first and have a fix waiting by morning." />
      <ShowDetailedView what="alerts, silences and routing" />
    </SimplePage>
  );
}
