// Copyright (c) 2026 Reliant Labs

/**
 * The console's busiest list pages at the "62" density, for the public demo:
 * the same names and the same data, but each page leads with ONE sentence,
 * shows the most important item as a card (the newest issue, the slowest
 * request with its timeline, the replay worth watching), and lists the top
 * ten as compact one-line rows with "Show all". The full console page, with
 * every filter, is one click away ("All filters").
 *
 * Every detail page (one issue, one trace, one session) is the console's own,
 * led by a compact plain block: a "What happened" line and the key facts in
 * one short row (DetailLead). Its less-used panels are folded
 * (Observability/detailDensity, set by DemoNavExtras).
 *
 *   Errors      BalancedErrors      newest issue card, then the inbox
 *   Traces      BalancedTraces      slowest request beside its mini waterfall, then the list
 *   Sessions    BalancedSessions    the replay worth watching, then compact rows with thumbnails
 *
 * Leads under the console's own page headers (ConsoleNavExtras `intro`):
 * TraceWhatHappened, IssueWhatHappened, SessionWhatHappened, ReleasesIntro,
 * OverviewIntro.
 */

import { useMemo, useState, type ReactNode } from "react";
import { ArrowRight, ChevronRight, CircleAlert, Play, SlidersHorizontal } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { useListTraces, useObservabilityOrg, useTrace, type TraceFilters } from "@/hooks/observability-queries";
import { useSession, useSessions } from "@/hooks/observability-session-queries";
import type { Issue } from "@/services/controlPlane/observability/issuesTypes";
import type { SessionSummary } from "@/services/controlPlane/observability/rumTypes";
import type { ObsScope, Span, TraceSummary } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { formatAgo, formatCount, formatDuration } from "../../Observability/format";
import { FIXTURE_PROJECT } from "../../Observability/__fixtures__/fixtureApi";
import { DEMO_INCIDENT, demoNow, RUNNING } from "../demoData/shopStory";
import { demoSectionHref, DEMO_SECTION_RELEASES } from "../demoPath";
import { PageAgentStrip } from "../approachable/AgentCards";
import { INCIDENT_CHECK, plainIssue, useShopIssues } from "../approachable/simpleData";
import { demoHref, SUBTITLES } from "./balancedNav";

// ── Shared pieces ───────────────────────────────────────────────────────────

type Tone = "good" | "bad" | "warn";
const DOT: Record<Tone, string> = { good: "bg-success", bad: "bg-danger", warn: "bg-warning" };

/** The environment the reader is looking at ("prod" unless they chose), and its id. */
export function envOf(search: Record<string, unknown>): { name: string; ids: string[] } {
  const name = typeof search.env === "string" && search.env ? search.env : "prod";
  return { name, ids: name === "all" ? [] : [`env-${name}`] };
}

function useDemoScope(envIds: string[]): ObsScope | null {
  const org = useObservabilityOrg();
  const key = envIds.join(",");
  return useMemo(() => (org.orgId ? { orgId: org.orgId, projectId: FIXTURE_PROJECT, envIds: key ? key.split(",") : [], filters: [] } : null), [org.orgId, key]);
}

/** A status dot that sits on the first line of the text beside it. */
function Dot({ tone }: { tone: Tone }) {
  return <span className={cn("mt-[0.45rem] inline-block h-1.5 w-1.5 shrink-0 rounded-full", DOT[tone])} aria-hidden="true" />;
}

/** The page's title and its plain subtitle on one line, then its one sentence. */
function Lead({ title, subtitle, tone, children, action }: { title: string; subtitle: string; tone?: Tone; children: ReactNode; action?: ReactNode }) {
  return (
    <header className="space-y-1">
      <div className="flex min-h-8 items-center justify-between gap-3">
        <div className="flex min-w-0 items-baseline gap-x-2.5">
          <h1 className="shrink-0 text-base font-semibold tracking-tight text-ink">{title}</h1>
          <p className="min-w-0 truncate text-sm text-ink-muted">{subtitle}</p>
        </div>
        {action}
      </div>
      <p className="flex max-w-4xl items-start gap-2 text-sm leading-relaxed text-ink" data-testid="balanced-summary">
        {tone && <Dot tone={tone} />}
        <span>{children}</span>
      </p>
    </header>
  );
}

/** The way to the console's full list, with every filter. */
function AllFilters({ href, what }: { href: string; what: string }) {
  return (
    <Link
      href={href}
      className="inline-flex min-h-8 shrink-0 items-center gap-1.5 rounded-md border border-border bg-surface px-2.5 text-sm font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      data-testid="balanced-all-filters"
    >
      <SlidersHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
      {what}
    </Link>
  );
}

const CARD = "group relative rounded-xl border border-border bg-surface p-4 transition-colors hover:border-primary/50 focus-within:ring-2 focus-within:ring-ring";

/** "Open issue ›": the card's own link, stretched over the whole card. */
function CardOpen({ href, label, testId }: { href: string; label: string; testId?: string }) {
  return (
    <Link
      href={href}
      className="inline-flex shrink-0 items-center gap-1 text-sm font-semibold text-primary-ink after:absolute after:inset-0 after:rounded-xl after:content-[''] focus-visible:outline-none"
      data-testid={testId}
    >
      {label}
      <ChevronRight className="h-4 w-4 transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
    </Link>
  );
}

/** A list's heading: a small label, and the count beside it. */
function ListHeading({ id, children, count }: { id: string; children: ReactNode; count?: number }) {
  return (
    <h2 id={id} className="text-sm font-medium text-ink">
      {children}
      {count !== undefined && <span className="ml-1.5 text-xs font-normal tabular-nums text-ink-muted">{count}</span>}
    </h2>
  );
}

const TOP = 10;

/**
 * A list of at most TOP compact one-line rows in four columns, each row one
 * link (hover, focus ring, chevron), then "Show all N".
 */
function TopTable<T>({
  label,
  columns,
  rows,
  rowKey,
  rowHref,
  cells,
  noun,
  top = TOP,
}: {
  label: string;
  columns: [string, string, string, string];
  rows: readonly T[];
  rowKey: (row: T) => string;
  rowHref: (row: T) => string;
  cells: (row: T) => [ReactNode, ReactNode, ReactNode, ReactNode];
  noun: string;
  top?: number;
}) {
  const [all, setAll] = useState(false);
  const shown = all ? rows : rows.slice(0, top);
  return (
    <div className="space-y-2">
      <div className="overflow-hidden rounded-xl border border-border bg-surface">
        <table className="w-full table-fixed text-sm" aria-label={label}>
          <thead>
            <tr className="border-b border-border text-left text-xs text-ink-muted">
              <th scope="col" className="w-auto px-3 py-2 font-medium">{columns[0]}</th>
              <th scope="col" className="w-24 px-3 py-2 text-right font-medium max-sm:hidden">{columns[1]}</th>
              <th scope="col" className="w-24 px-3 py-2 text-right font-medium max-sm:hidden">{columns[2]}</th>
              <th scope="col" className="w-24 px-3 py-2 text-right font-medium">{columns[3]}</th>
              <th scope="col" className="w-8 px-1"><span className="sr-only">Open</span></th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {shown.map((row) => {
              const [first, second, third, fourth] = cells(row);
              return (
                <tr key={rowKey(row)} className="group relative cursor-pointer transition-colors hover:bg-ink/[0.03] focus-within:bg-ink/[0.03]">
                  <td className="px-3 py-2">
                    <Link href={rowHref(row)} className="flex min-w-0 items-center gap-2 after:absolute after:inset-0 after:content-[''] focus-visible:outline-none">
                      {first}
                    </Link>
                  </td>
                  <td className="px-3 py-2 text-right tabular-nums text-ink max-sm:hidden">{second}</td>
                  <td className="px-3 py-2 text-right tabular-nums text-ink max-sm:hidden">{third}</td>
                  <td className="whitespace-nowrap px-3 py-2 text-right tabular-nums text-ink-muted">{fourth}</td>
                  <td className="px-1 text-ink-subtle group-hover:text-ink">
                    <ChevronRight className="h-4 w-4 transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {rows.length > top && (
        <button
          type="button"
          onClick={() => setAll((was) => !was)}
          className="inline-flex min-h-8 items-center rounded-md border border-border bg-surface px-3 text-sm font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {all ? `Show the top ${top}` : `Show all ${rows.length} ${noun}`}
        </button>
      )}
    </div>
  );
}

/** A row's first cell: an optional marker, its name, and a muted note after it on the same line. */
function RowName({ marker, name, note, strong = true }: { marker?: ReactNode; name: ReactNode; note?: ReactNode; strong?: boolean }) {
  return (
    <>
      {marker}
      <span className={cn("min-w-0 truncate", strong ? "font-medium text-ink" : "text-ink")}>{name}</span>
      {note && <span className="shrink-0 truncate text-xs text-ink-muted max-sm:hidden">{note}</span>}
    </>
  );
}

const plural = (n: number, one: string, many = `${one}s`) => `${formatCount(n)} ${n === 1 ? one : many}`;

const RedDot = ({ label }: { label: string }) => <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-danger" aria-label={label} />;

// ── Errors ──────────────────────────────────────────────────────────────────

export function BalancedErrors({ search }: { search: Record<string, unknown> }) {
  const env = envOf(search);
  const { open, fixed } = useShopIssues();
  const now = demoNow();
  const inEnv = (issue: Issue) => env.name === "all" || issue.envName === env.name;
  const rows = useMemo(() => (open ?? []).filter(inEnv).sort((a, b) => b.lastSeenMs - a.lastSeenMs), [open, env.name]); // eslint-disable-line react-hooks/exhaustive-deps
  const resolved = (fixed ?? []).filter(inEnv);
  const fresh = rows.filter((issue) => issue.status === "new");
  const featured = [...rows].sort((a, b) => b.firstSeenMs - a.firstSeenMs)[0];
  const issueHref = (issue: Issue) => demoHref({ kind: "obs-view", view: "errors" }, search.env as string | undefined, { issue: issue.id });
  return (
    <div className="space-y-4" data-testid="balanced-errors">
      <Lead
        title="Errors"
        subtitle={SUBTITLES.errors}
        tone={rows.length ? "bad" : "good"}
        action={<AllFilters href={demoHref({ kind: "obs-view", view: "errors" }, search.env as string | undefined, { isort: "last" })} what="All filters" />}
      >
        {!open ? (
          "Reading the error inbox…"
        ) : rows.length ? (
          <>
            <strong className="font-semibold">{plural(rows.length, "open issue")}</strong> in {env.name === "all" ? "every environment" : env.name}
            {fresh.length ? `, ${fresh.length} of them new` : ""}. {resolved.length ? `${plural(resolved.length, "issue")} resolved in the last two weeks.` : ""}
          </>
        ) : (
          <>Nothing is broken in {env.name}. {resolved.length ? `${plural(resolved.length, "issue")} resolved in the last two weeks.` : ""}</>
        )}
      </Lead>

      {featured && (
        <section className={CARD} aria-label="Newest issue" data-testid="balanced-featured-issue">
          <p className="flex flex-wrap items-center gap-2 text-xs font-medium text-ink-muted">
            <CircleAlert className="h-3.5 w-3.5 text-danger-ink" aria-hidden="true" />
            Newest issue
            {featured.status === "new" && <span className="rounded-full border border-danger-border bg-danger-surface px-1.5 text-[11px] font-medium leading-4 text-danger-ink">New</span>}
          </p>
          <h2 className="mt-1 text-base font-semibold text-ink [overflow-wrap:anywhere]">{featured.title}</h2>
          {plainIssue(featured).why && <p className="max-w-4xl text-sm text-ink-muted">{plainIssue(featured).why}</p>}
          <div className="mt-2 flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
            <p className="text-xs text-ink-muted">
              <span className="font-medium text-ink">{plural(featured.events, "event")}</span> · {plural(featured.affected, "request")} affected · first seen{" "}
              {formatAgo(featured.firstSeenMs, now)} in {featured.firstVersion}
              {featured.assignee ? ` · ${featured.assignee.name} is on it` : ""}
            </p>
            <CardOpen href={issueHref(featured)} label="Open issue" testId="balanced-featured-issue-open" />
          </div>
        </section>
      )}

      {rows.length > 0 && (
        <section className="space-y-2" aria-labelledby="balanced-errors-open">
          <ListHeading id="balanced-errors-open" count={rows.length}>
            Open issues
          </ListHeading>
          <TopTable
            label="Open issues"
            columns={["Issue", "Events", "Affected", "Last seen"]}
            rows={rows}
            rowKey={(issue) => issue.id}
            rowHref={issueHref}
            noun="issues"
            cells={(issue) => [
              <RowName
                marker={issue.status === "new" ? <RedDot label="New" /> : undefined}
                name={issue.title}
                note={`${issue.service} · ${issue.envName}`}
              />,
              formatCount(issue.events),
              formatCount(issue.affected),
              formatAgo(issue.lastSeenMs, now),
            ]}
          />
        </section>
      )}

      <PageAgentStrip id="fix-errors" lead="An agent can read an issue like this one, find the change that caused it, and open the fix for you to review." />

      {resolved.length > 0 && (
        <section className="space-y-2" aria-labelledby="balanced-errors-resolved">
          <ListHeading id="balanced-errors-resolved" count={resolved.length}>
            Resolved recently
          </ListHeading>
          <TopTable
            label="Resolved issues"
            columns={["Issue", "Events", "Fixed in", "Resolved"]}
            rows={resolved}
            rowKey={(issue) => issue.id}
            rowHref={issueHref}
            noun="resolved issues"
            cells={(issue) => [
              <RowName strong={false} name={issue.title} note={`${issue.service} · ${issue.envName}`} />,
              formatCount(issue.events),
              issue.resolvedVersion || "—",
              issue.resolvedAtMs ? formatAgo(issue.resolvedAtMs, now) : "—",
            ]}
          />
        </section>
      )}
    </div>
  );
}

// ── Traces ──────────────────────────────────────────────────────────────────

const TRACE_FILTERS: TraceFilters = { range: "1h", rootSpanName: "", services: [], errorsOnly: false, minDurationMs: 0 };

export function BalancedTraces({ search }: { search: Record<string, unknown> }) {
  const env = envOf(search);
  const scope = useDemoScope(env.ids);
  const traces = useListTraces(scope, TRACE_FILTERS);
  const all = useMemo(() => traces.data?.pages.flatMap((page) => page.traces) ?? [], [traces.data]);
  const slowest = useMemo(() => [...all].sort((a, b) => b.durationMs - a.durationMs), [all]);
  const typical = useMemo(() => {
    const sorted = all.map((t) => t.durationMs).sort((a, b) => a - b);
    return sorted.length ? sorted[Math.floor(sorted.length / 2)] : 0;
  }, [all]);
  const failed = all.filter((t) => t.errorCount > 0).length;
  const top = slowest[0];
  const traceHref = (trace: TraceSummary) => demoHref({ kind: "obs-view", view: "traces" }, search.env as string | undefined, { trace: trace.traceId });
  return (
    <div className="space-y-4" data-testid="balanced-traces">
      <Lead
        title="Traces"
        subtitle={SUBTITLES.traces}
        tone={top && top.durationMs >= 1000 ? "warn" : "good"}
        action={<AllFilters href={demoHref({ kind: "obs-view", view: "traces" }, search.env as string | undefined, { range: "1h" })} what="Chart and filters" />}
      >
        {traces.isLoading || !scope ? (
          "Reading the last hour of requests…"
        ) : top ? (
          <>
            {plural(all.length, "request")} in the last hour. Half answered in under {formatDuration(typical)};{" "}
            <strong className="font-semibold">
              the slowest, {top.rootOperation}, took {formatDuration(top.durationMs)}
            </strong>
            {failed ? `. ${plural(failed, "request")} ended in an error.` : "."}
          </>
        ) : (
          "No requests in the last hour."
        )}
      </Lead>

      {top && scope && <SlowestTrace scope={scope} trace={top} href={traceHref(top)} />}

      {slowest.length > 0 && (
        <section className="space-y-2" aria-labelledby="balanced-traces-slowest">
          <ListHeading id="balanced-traces-slowest" count={slowest.length}>
            Slowest requests, last hour
          </ListHeading>
          <TopTable
            label="Slowest requests"
            columns={["Request", "Duration", "Steps", "When"]}
            rows={slowest}
            rowKey={(trace) => trace.traceId}
            rowHref={traceHref}
            noun="requests"
            cells={(trace) => [
              <RowName marker={trace.errorCount > 0 ? <RedDot label="Failed" /> : undefined} name={trace.rootOperation} note={trace.rootService} />,
              <span className={cn(trace.durationMs >= 1000 && "font-semibold text-warning-ink")}>{formatDuration(trace.durationMs)}</span>,
              formatCount(trace.spanCount),
              formatAgo(trace.startMs),
            ]}
          />
        </section>
      )}

      <PageAgentStrip
        id="slow-pages"
        lead="We spent three months trying to make our own chat pages load faster. The day we handed an agent a trace like these, it found the slow query and fixed it."
      />
    </div>
  );
}

/** The slowest request, as a card: what happened on the left, its timeline in miniature on the right. */
function SlowestTrace({ scope, trace, href }: { scope: ObsScope; trace: TraceSummary; href: string }) {
  const detail = useTrace(scope, trace.traceId, "1h");
  const spans = detail.data?.spans ?? [];
  return (
    <section className={CARD} aria-label="Slowest request" data-testid="balanced-slowest-trace">
      <div className="grid gap-x-6 gap-y-3 lg:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
        <div className="flex min-w-0 flex-col gap-1">
          <p className="text-xs font-medium text-ink-muted">Slowest request</p>
          <div className="flex flex-wrap items-baseline justify-between gap-x-3">
            <h2 className="min-w-0 text-base font-semibold text-ink [overflow-wrap:anywhere]">{trace.rootOperation}</h2>
            <p className="text-xl font-semibold tabular-nums text-ink">{formatDuration(trace.durationMs)}</p>
          </div>
          {spans.length > 0 && <p className="text-sm text-ink-muted">{whatHappened(spans) ?? ""}</p>}
          <div className="mt-auto flex flex-wrap items-center justify-between gap-x-3 gap-y-1 pt-1">
            <p className="text-xs text-ink-muted">
              {trace.rootService} · {plural(trace.spanCount, "step")} · {formatAgo(trace.startMs)}
            </p>
            <CardOpen href={href} label="Open the full timeline" testId="balanced-slowest-trace-open" />
          </div>
        </div>
        {spans.length > 0 && <MiniWaterfall spans={spans} />}
      </div>
    </section>
  );
}

interface Step {
  span: Span;
  depth: number;
  self: number;
}

function stepsOf(spans: readonly Span[]): Step[] {
  const ids = new Set(spans.map((s) => s.spanId));
  const children = new Map<string, Span[]>();
  for (const span of spans) {
    const parent = span.parentSpanId && ids.has(span.parentSpanId) ? span.parentSpanId : "";
    children.set(parent, [...(children.get(parent) ?? []), span]);
  }
  const out: Step[] = [];
  const walk = (span: Span, depth: number) => {
    const kids = (children.get(span.spanId) ?? []).sort((a, b) => a.startMs - b.startMs);
    out.push({ span, depth, self: Math.max(0, span.durationMs - kids.reduce((a, k) => a + k.durationMs, 0)) });
    for (const kid of kids) walk(kid, depth + 1);
  };
  for (const root of (children.get("") ?? []).sort((a, b) => a.startMs - b.startMs)) walk(root, 0);
  return out;
}

/** One sentence: how long it took, where most of the time went, and what failed. */
export function whatHappened(spans: readonly Span[]): string | null {
  const steps = stepsOf(spans);
  const root = steps[0]?.span;
  if (!root) return null;
  const slow = steps.slice(1).sort((a, b) => b.self - a.self)[0];
  // Where the failure started: the deepest failed step (its callers fail because it did).
  const failed = [...steps].reverse().find((s) => s.span.status === "error")?.span;
  const pct = slow && root.durationMs ? Math.round((slow.self / root.durationMs) * 100) : 0;
  const where = slow && pct >= 20 ? ` The longest step (${pct}% of it) was ${slow.span.name}.` : " No single step stands out.";
  const fail = failed ? ` It failed in ${failed.service}${failed.statusMessage ? `: “${failed.statusMessage}”` : ""}.` : "";
  return `${root.name} took ${formatDuration(root.durationMs)}.${where}${fail}`;
}

/** Up to eight steps of a trace on one time axis: the slow step and failed ones stand out. */
export function MiniWaterfall({ spans, max = 8, className }: { spans: readonly Span[]; max?: number; className?: string }) {
  const steps = stepsOf(spans);
  const root = steps[0]?.span;
  if (!root) return null;
  const total = Math.max(root.durationMs, 1);
  const slowId = steps.slice(1).sort((a, b) => b.self - a.self)[0]?.span.spanId;
  const shown = steps.slice(0, max);
  return (
    <div className={cn("min-w-0 space-y-1", className)} role="img" aria-label={`Timeline of ${root.name}: ${shown.length} of ${steps.length} steps`} data-testid="balanced-mini-waterfall">
      {shown.map(({ span, depth }) => {
        const left = ((span.startMs - root.startMs) / total) * 100;
        const width = Math.max(0.8, (span.durationMs / total) * 100);
        return (
          <div key={span.spanId} className="grid grid-cols-[minmax(6rem,13rem)_1fr_3.5rem] items-center gap-2 text-xs leading-4 max-sm:grid-cols-[minmax(5rem,8rem)_1fr_3rem]">
            <span className={cn("truncate", span.spanId === slowId ? "font-semibold text-ink" : "text-ink-muted")} style={{ paddingLeft: `${Math.min(depth, 4) * 0.6}rem` }} title={`${span.service}: ${span.name}`}>
              {span.name}
            </span>
            <span className="relative h-2 rounded-sm bg-surface-sunken">
              <span
                className={cn("absolute inset-y-0 rounded-sm", span.status === "error" ? "bg-danger" : span.spanId === slowId ? "bg-warning" : "bg-primary/50")}
                style={{ left: `${Math.min(left, 99)}%`, width: `${Math.min(width, 100 - Math.min(left, 99))}%` }}
              />
            </span>
            <span className={cn("text-right tabular-nums", span.spanId === slowId ? "font-semibold text-warning-ink" : "text-ink-muted")}>{formatDuration(span.durationMs)}</span>
          </div>
        );
      })}
      {steps.length > shown.length && <p className="text-xs text-ink-muted">and {plural(steps.length - shown.length, "more step")} in the full timeline</p>}
    </div>
  );
}

/** The shop's slowest request in prod, as a few rows of its timeline: the landing's trace story. */
export function SlowestRequestPreview() {
  const scope = useDemoScope(["env-prod"]);
  const traces = useListTraces(scope, TRACE_FILTERS);
  const slowest = useMemo(() => [...(traces.data?.pages.flatMap((page) => page.traces) ?? [])].sort((a, b) => b.durationMs - a.durationMs)[0], [traces.data]);
  const detail = useTrace(scope, slowest?.traceId ?? null, "1h");
  const spans = detail.data?.spans ?? [];
  return spans.length ? <MiniWaterfall spans={spans} max={5} /> : <div className="h-24" aria-hidden="true" />;
}

// ── Detail pages: a compact plain block on top ──────────────────────────────

interface Fact {
  value: ReactNode;
  label?: string;
  tone?: "bad";
}

/** "What happened" in one line, and the key facts as one short row: the top of every detail page. */
function DetailLead({ children, facts }: { children: ReactNode; facts: Fact[] }) {
  return (
    <section className="rounded-lg border border-primary/25 bg-primary/5 px-3 py-2" aria-label="What happened" data-testid="balanced-what-happened">
      <p className="text-sm text-ink">
        <span className="font-semibold">What happened: </span>
        {children}
      </p>
      {facts.length > 0 && (
        <ul className="mt-1 flex flex-wrap gap-x-4 gap-y-0.5 text-xs text-ink-muted" aria-label="Key facts" data-testid="balanced-key-facts">
          {facts.map((fact, index) => (
            <li key={index}>
              <span className={cn("font-medium tabular-nums", fact.tone === "bad" ? "text-danger-ink" : "text-ink")}>{fact.value}</span>
              {fact.label && <> {fact.label}</>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** The console's trace page, with its "What happened" first. */
export function TraceWhatHappened({ search }: { search: Record<string, unknown> }) {
  const scope = useDemoScope(envOf(search).ids);
  const trace = useTrace(scope, typeof search.trace === "string" ? search.trace : null);
  const spans = trace.data?.spans ?? [];
  const sentence = spans.length ? whatHappened(spans) : null;
  if (!sentence) return null;
  const root = stepsOf(spans)[0]?.span;
  const services = new Set(spans.map((span) => span.service)).size;
  const failed = spans.filter((span) => span.status === "error").length;
  return (
    <DetailLead
      facts={[
        { value: root ? formatDuration(root.durationMs) : "—", label: "end to end" },
        { value: formatCount(spans.length), label: spans.length === 1 ? "step" : "steps" },
        { value: formatCount(services), label: services === 1 ? "service" : "services" },
        ...(failed ? [{ value: formatCount(failed), label: failed === 1 ? "failed step" : "failed steps", tone: "bad" as const }] : []),
        ...(root ? [{ value: formatAgo(root.startMs), label: `in ${root.service}` }] : []),
      ]}
    >
      {sentence}
    </DetailLead>
  );
}

export function IssueWhatHappened({ search }: { search: Record<string, unknown> }) {
  const { open, fixed } = useShopIssues();
  const issue = [...(open ?? []), ...(fixed ?? [])].find((candidate) => candidate.id === search.issue);
  const plain = issue ? plainIssue(issue) : null;
  if (!issue || !plain?.why) return null;
  const now = demoNow();
  return (
    <DetailLead
      facts={[
        { value: formatCount(issue.events), label: issue.events === 1 ? "event" : "events", tone: "bad" },
        { value: formatCount(issue.affected), label: "requests affected" },
        { value: issue.firstVersion || "—", label: `first, ${formatAgo(issue.firstSeenMs, now)}` },
        { value: issue.service, label: `in ${issue.envName}` },
        ...(issue.assignee ? [{ value: issue.assignee.name, label: "is on it" }] : []),
      ]}
    >
      {plain.why}
      {issue.firstVersion ? ` It started with release ${issue.firstVersion}.` : ""}
    </DetailLead>
  );
}

export function SessionWhatHappened({ search }: { search: Record<string, unknown> }) {
  const scope = useDemoScope(envOf(search).ids);
  const session = useSession(scope, typeof search.session === "string" ? search.session : undefined);
  const s = session.data;
  if (!s) return null;
  return (
    <DetailLead
      facts={[
        { value: s.userId || "Signed-out visitor" },
        { value: formatCount(s.pageViews), label: s.pageViews === 1 ? "page" : "pages" },
        { value: formatDuration(Math.max(0, s.endedMs - s.startedMs)), label: "long" },
        { value: formatCount(s.errors), label: s.errors === 1 ? "error" : "errors", tone: s.errors ? "bad" : undefined },
        ...(s.release ? [{ value: s.release, label: "build" }] : []),
        { value: formatAgo(s.startedMs), label: s.envName ? `in ${s.envName}` : undefined },
      ]}
    >
      A visitor landed on <span className="font-mono text-xs">{s.entryPath || "/"}</span>, looked at {plural(s.pageViews, "page")}
      {s.errors ? `, and hit ${plural(s.errors, "error")} on the way.` : ", with no errors."} Press play on the replay to watch it.
    </DetailLead>
  );
}

// ── Sessions ────────────────────────────────────────────────────────────────

export function BalancedSessions({ search }: { search: Record<string, unknown> }) {
  const env = envOf(search);
  const scope = useDemoScope(env.ids);
  const sessions = useSessions(scope, { range: "24h", hasError: false, userId: "", pathContains: "" });
  // The visits that hit an error first: those are the replays worth watching.
  const rows = useMemo(() => [...(sessions.data?.sessions ?? [])].sort((a, b) => Number(b.errors > 0) - Number(a.errors > 0)), [sessions.data]);
  const withErrors = rows.filter((s) => s.errors > 0).length;
  const [featured, ...rest] = rows;
  const sessionHref = (id: string) => demoHref({ kind: "obs-view", view: "sessions" }, search.env as string | undefined, { session: id });
  return (
    <div className="space-y-4" data-testid="balanced-sessions">
      <Lead
        title="Sessions"
        subtitle={SUBTITLES.sessions}
        tone={withErrors ? "warn" : "good"}
        action={<AllFilters href={demoHref({ kind: "obs-view", view: "sessions" }, search.env as string | undefined, { range: "24h" })} what="All filters" />}
      >
        {sessions.isLoading || !scope ? (
          "Reading the last day's visits…"
        ) : rows.length ? (
          <>
            {plural(rows.length, "visit")} recorded in the last 24 hours{sessions.data?.nextPageToken ? " (the newest shown)" : ""}.{" "}
            {withErrors ? <strong className="font-semibold">{plural(withErrors, "visitor")} hit an error; their replays are marked.</strong> : "Nobody hit an error."}
          </>
        ) : (
          "No visits recorded in the last 24 hours."
        )}
      </Lead>

      {featured && <FeaturedReplay session={featured} href={sessionHref(featured.sessionId)} />}

      {rest.length > 0 && (
        <section className="space-y-2" aria-labelledby="balanced-sessions-list">
          <ListHeading id="balanced-sessions-list" count={rest.length}>
            Other visits
          </ListHeading>
          <TopTable
            label="Recorded visits"
            columns={["Visit", "Pages", "Length", "When"]}
            rows={rest}
            rowKey={(session) => session.sessionId}
            rowHref={(session) => sessionHref(session.sessionId)}
            noun="visits"
            cells={(session) => [
              <>
                <MiniReplay replay={session.hasReplay} />
                <span className="min-w-0 truncate font-medium text-ink" data-testid={`balanced-session-${session.sessionId}`}>
                  {session.entryPath || "/"}
                </span>
                {session.errors > 0 && <ErrorBadge errors={session.errors} />}
                <span className="shrink-0 truncate text-xs text-ink-muted max-sm:hidden">{session.userId || "Signed-out visitor"}</span>
              </>,
              formatCount(session.pageViews),
              formatDuration(Math.max(0, session.endedMs - session.startedMs)),
              formatAgo(session.startedMs),
            ]}
          />
        </section>
      )}
    </div>
  );
}

function ErrorBadge({ errors }: { errors: number }) {
  return (
    <span className="shrink-0 rounded-full border border-danger-border bg-danger-surface px-1.5 text-[11px] font-semibold leading-4 text-danger-ink">
      {errors === 1 ? "1 error" : `${errors} errors`}
    </span>
  );
}

/** The replay worth watching: its first frame with a play button beside what the visitor did. */
function FeaturedReplay({ session, href }: { session: SessionSummary; href: string }) {
  const length = formatDuration(Math.max(0, session.endedMs - session.startedMs));
  return (
    <section className={cn(CARD, "p-3")} aria-label="Replay worth watching" data-testid={`balanced-session-${session.sessionId}`}>
      <div className="grid gap-4 sm:grid-cols-[18rem_minmax(0,1fr)]">
        <ReplayThumb path={session.entryPath} errors={session.errors} replay={session.hasReplay} />
        <div className="flex min-w-0 flex-col gap-1 py-1">
          <p className="text-xs font-medium text-ink-muted">{session.errors ? "A visit that hit an error" : "The newest visit"}</p>
          <h2 className="truncate text-base font-semibold text-ink">{session.entryPath || "/"}</h2>
          <p className="text-sm text-ink-muted">
            {session.userId || "A signed-out visitor"} looked at {plural(session.pageViews, "page")} over {length}
            {session.errors ? ` and hit ${plural(session.errors, "error")}. Watch where it went wrong.` : "."}
          </p>
          <div className="mt-auto flex flex-wrap items-center justify-between gap-x-3 gap-y-1 pt-1">
            <p className="text-xs text-ink-muted">
              {formatAgo(session.startedMs)}
              {session.release ? ` · ${session.release}` : ""}
              {session.envName ? ` · ${session.envName}` : ""}
            </p>
            <CardOpen href={href} label="Watch the replay" testId="balanced-featured-replay-open" />
          </div>
        </div>
      </div>
    </section>
  );
}

/** A replay's thumbnail: a little browser on its first page, a play button, and its errors. */
function ReplayThumb({ path, errors, replay }: { path: string; errors: number; replay: boolean }) {
  return (
    <div className="relative overflow-hidden rounded-lg border border-border bg-surface-sunken" aria-hidden="true">
      <div className="flex items-center gap-1 border-b border-border bg-surface px-2 py-1.5">
        <span className="h-1.5 w-1.5 rounded-full bg-ink/15" />
        <span className="h-1.5 w-1.5 rounded-full bg-ink/15" />
        <span className="h-1.5 w-1.5 rounded-full bg-ink/15" />
        <span className="ml-1.5 truncate rounded bg-surface-sunken px-1.5 font-mono text-[10px] leading-4 text-ink-muted">shop.example{path || "/"}</span>
      </div>
      <div className="space-y-1.5 px-3 py-3">
        <div className="h-2 w-2/3 rounded bg-ink/10" />
        <div className="h-1.5 w-5/6 rounded bg-ink/[0.07]" />
        <div className="grid grid-cols-3 gap-1.5 pt-1">
          <div className="h-7 rounded bg-ink/[0.06]" />
          <div className="h-7 rounded bg-ink/[0.06]" />
          <div className="h-7 rounded bg-ink/[0.06]" />
        </div>
      </div>
      {replay && (
        <span className="absolute inset-0 top-6 flex items-center justify-center">
          <span className="flex h-10 w-10 items-center justify-center rounded-full bg-primary text-primary-foreground shadow-md transition-transform group-hover:scale-110">
            <Play className="ml-0.5 h-4 w-4" fill="currentColor" />
          </span>
        </span>
      )}
      {errors > 0 && (
        <span className="absolute right-2 top-8">
          <ErrorBadge errors={errors} />
        </span>
      )}
    </div>
  );
}

/** A row's tiny replay frame with its play glyph. */
function MiniReplay({ replay }: { replay: boolean }) {
  return (
    <span className="relative flex h-6 w-9 shrink-0 items-center justify-center rounded border border-border bg-surface-sunken" aria-hidden="true">
      <span className="absolute inset-x-0 top-0 h-1.5 border-b border-border bg-surface" />
      {replay && <Play className="mt-1 h-2.5 w-2.5 text-primary-ink" fill="currentColor" />}
    </span>
  );
}

// ── Releases and Overview: the console's own, with a lead on top ────────────

export function ReleasesIntro() {
  const prod = RUNNING["env-prod"];
  const failed = prod.health && prod.health !== "none" && prod.health.status === "failed";
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-2">
        <p className="flex max-w-4xl flex-1 items-start gap-2 text-sm leading-relaxed text-ink" data-testid="balanced-summary">
          <Dot tone={failed ? "bad" : "good"} />
          <span>
            Production runs {prod.version}, shipped by {prod.by}.{" "}
            {failed ? (
              <strong className="font-semibold">
                Its health check failed (failed checkouts {INCIDENT_CHECK.errorsBefore} → {INCIDENT_CHECK.errorsAfter}), so it is a candidate to roll back.
              </strong>
            ) : (
              "It passed its health check."
            )}{" "}
            Every release is built once and promoted, unchanged, from dev to staging to prod.
          </span>
        </p>
        <Link
          href={demoSectionHref(DEMO_SECTION_RELEASES)}
          className="group inline-flex min-h-8 shrink-0 items-center gap-1 text-sm font-medium text-primary-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="balanced-releases-explainer"
        >
          How releases work
          <ArrowRight className="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
        </Link>
      </div>
      <PageAgentStrip id="release-guard" lead="An agent can check every release against the hour before it went out, and move prod back to the last good build if it's bad." />
    </div>
  );
}

export function OverviewIntro() {
  const { open } = useShopIssues();
  const prod = RUNNING["env-prod"];
  const broken = (open ?? []).filter((issue) => issue.envName === "prod" && issue.id === DEMO_INCIDENT.issueId).length > 0;
  return (
    <p className="flex items-start gap-2 text-sm leading-relaxed text-ink" data-testid="balanced-summary">
      <Dot tone={broken ? "bad" : "good"} />
      <span>
        {broken ? (
          <>
            <strong className="font-semibold">One thing needs attention:</strong> some checkouts fail since {prod.version} reached prod. Everything else is healthy.
          </>
        ) : (
          "Everything is healthy."
        )}
      </span>
    </p>
  );
}
