// Copyright (c) 2026 Reliant Labs

/**
 * The ML tab's small parts, shared by every view so the pages read as one
 * product (02-views §0): a status dot with its word, a link for any entity id,
 * the header strip of numbers, a panel, the "not collected" placeholder,
 * tabs bound to the URL, a dense table whose rows open their entity, a side
 * panel, stacked share bars and inline meters.
 *
 * Forge console vocabulary (web-styling): text-ink / text-ink-muted,
 * bg-surface / bg-surface-sunken, danger-*; status is a dot, never a fill;
 * numbers are tabular and right-aligned; rows are 32px.
 */

import { useMemo, useState, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import { ChevronDown, ChevronUp, EyeOff, X } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import { cn } from "@/lib/utils";
import type { MlSubject } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { subjectHref, useMlSearch, mlHref } from "./links";
import { TONE_DOT, TONE_TEXT, TONE_VAR, type MlTone } from "./labels";

// ── Status ────────────────────────────────────────────────────────────────────

/** A status: an 8px dot and its word. `pulse` for live (running). */
export function StatusDot({ tone, label, pulse, className, hideLabel }: { tone: MlTone; label?: ReactNode; pulse?: boolean; className?: string; hideLabel?: boolean }) {
  return (
    <span className={cn("inline-flex items-center gap-1.5 whitespace-nowrap", className)}>
      <span className={cn("relative inline-block h-2 w-2 shrink-0 rounded-full", TONE_DOT[tone])} aria-hidden="true">
        {pulse && <span className={cn("absolute inset-0 animate-ping rounded-full opacity-60", TONE_DOT[tone])} />}
      </span>
      {label !== undefined && <span className={cn(hideLabel && "sr-only", tone === "danger" || tone === "warning" ? TONE_TEXT[tone] : "text-ink")}>{label}</span>}
    </span>
  );
}

/** A small outlined chip (a badge on a run, a tag): neutral unless it is a status. */
export function Chip({ tone = "neutral", children, title, className }: { tone?: MlTone; children: ReactNode; title?: string; className?: string }) {
  return (
    <span
      title={title}
      className={cn(
        "inline-flex items-center gap-1 whitespace-nowrap rounded-md border px-1.5 py-px text-meta leading-5",
        tone === "danger" ? "border-danger-ink/35 text-danger-ink" : tone === "warning" ? "border-warning-ink/40 text-warning-ink" : "border-border text-ink-muted",
        className,
      )}
    >
      {tone !== "neutral" && <span className={cn("h-1.5 w-1.5 rounded-full", TONE_DOT[tone])} aria-hidden="true" />}
      {children}
    </span>
  );
}

// ── Links ─────────────────────────────────────────────────────────────────────

const linkClass = "text-ink underline-offset-2 hover:underline focus-visible:underline";

/**
 * An entity, as a link to its page (subjectHref). `mono` for ids; a subject
 * the ML tab has no page for renders as text. Never dead text when it has one.
 */
export function SubjectLink({ subject, children, mono, className, testId }: { subject: Pick<MlSubject, "kind" | "id"> & { label?: string }; children?: ReactNode; mono?: boolean; className?: string; testId?: string }) {
  const href = subjectHref(subject);
  const body = children ?? (subject.label || subject.id);
  const cls = cn(mono && "font-mono text-code", className);
  if (!href) return <span className={cls}>{body}</span>;
  return (
    <Link href={href} className={cn(linkClass, cls)} data-testid={testId}>
      {body}
    </Link>
  );
}

/** A plain internal link, styled as every ML link. */
export function MlLink({ href, children, mono, className, testId, title }: { href: string; children: ReactNode; mono?: boolean; className?: string; testId?: string; title?: string }) {
  return (
    <Link href={href} className={cn(linkClass, mono && "font-mono text-code", className)} data-testid={testId} title={title}>
      {children}
    </Link>
  );
}

/** A link to a finding (the Recommendations side panel). */
export function FindingLink({ id, children }: { id: string; children: ReactNode }) {
  return (
    <Link href={mlHref("recommendations", [], { sel: id })} className="text-primary-ink underline-offset-2 hover:underline">
      {children}
    </Link>
  );
}

// ── Header strip ──────────────────────────────────────────────────────────────

export interface Kpi {
  label: string;
  value: ReactNode;
  /** A second line: a delta, a share, the unit's context. */
  sub?: ReactNode;
  tone?: MlTone;
  href?: string;
  testId?: string;
  /** The one number this page is about: larger. */
  hero?: boolean;
}

/**
 * The 4–6 numbers that answer a page's question, above the fold (02-views
 * §0.1): one row of cells divided by hairlines, no card per number.
 */
export function KpiStrip({ items, testId }: { items: readonly Kpi[]; testId?: string }) {
  return (
    <Card padding="none" className="overflow-hidden" data-testid={testId ?? "ml-kpis"}>
      <dl className="grid grid-cols-2 divide-x divide-y divide-border-subtle sm:grid-cols-3 lg:flex lg:divide-y-0">
        {items.map((item) => {
          const value = (
            <dd className={cn("tabular-nums text-ink", item.hero ? "text-2xl font-semibold" : "text-xl font-semibold")}>
              {item.tone && item.tone !== "neutral" ? <StatusDot tone={item.tone} label={item.value} /> : item.value}
            </dd>
          );
          return (
            <div key={item.label} className="min-w-0 flex-1 px-4 py-3" data-testid={item.testId}>
              <dt className="truncate text-meta font-medium text-ink-muted">{item.label}</dt>
              {item.href ? (
                <Link href={item.href} className="block hover:underline">
                  {value}
                </Link>
              ) : (
                value
              )}
              {item.sub && <div className="truncate text-meta text-ink-muted">{item.sub}</div>}
            </div>
          );
        })}
      </dl>
    </Card>
  );
}

// ── Panels ────────────────────────────────────────────────────────────────────

/** A titled panel: heading, an optional note and actions on one line, then the body. `flush` for a table. */
export function Panel({
  title,
  note,
  actions,
  children,
  flush,
  testId,
  className,
}: {
  title: ReactNode;
  note?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  flush?: boolean;
  testId?: string;
  className?: string;
}) {
  return (
    <Card padding="none" className={cn("min-w-0 overflow-hidden", className)} data-testid={testId}>
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 px-4 pb-2 pt-3">
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-3">
          <h2 className="text-section text-ink">{title}</h2>
          {note && <p className="text-meta text-ink-muted">{note}</p>}
        </div>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </div>
      <div className={flush ? undefined : "px-4 pb-4"}>{children}</div>
    </Card>
  );
}

/**
 * A metric nothing reports (02-views §0.6): never an empty chart, never 0.
 * Names the source that would report it and links the finding that fixes it.
 */
export function NotCollected({ what, source, findingId, height, compact }: { what?: string; source?: string; findingId?: string; height?: number; compact?: boolean }) {
  return (
    <div
      className={cn("flex flex-col items-center justify-center gap-1 rounded-md border border-dashed border-border bg-surface-sunken px-3 text-center", compact ? "py-1" : "py-4")}
      style={height ? { minHeight: height } : undefined}
      data-testid="ml-not-collected"
    >
      <span className="inline-flex items-center gap-1.5 text-meta font-medium text-ink-muted">
        <EyeOff className="h-3.5 w-3.5" aria-hidden="true" />
        {what ? `${what}: not collected` : "Not collected"}
      </span>
      {!compact && source && <span className="text-meta text-ink-muted">Needs {source}</span>}
      {!compact && findingId && (
        <FindingLink id={findingId}>
          <span className="text-meta">How to turn it on</span>
        </FindingLink>
      )}
    </div>
  );
}

/** A quiet note inside a panel (nothing to list). */
export function EmptyNote({ children }: { children: ReactNode }) {
  return <p className="py-3 text-meta text-ink-muted">{children}</p>;
}

// ── Tabs bound to the URL ─────────────────────────────────────────────────────

export interface MlTabDef {
  id: string;
  label: string;
  count?: number | string;
  /** Marks a tab that has something wrong in it. */
  tone?: MlTone;
}

/** Underline tabs whose selection is `?tab=` (deep-linkable). The first is the default. */
export function MlTabs({ tabs, testId }: { tabs: readonly MlTabDef[]; testId?: string }) {
  const [search, setSearch] = useMlSearch();
  const active = tabs.some((t) => t.id === search.tab) ? search.tab : tabs[0]?.id;
  return (
    <div role="tablist" className="flex gap-1 overflow-x-auto border-b border-border" data-testid={testId ?? "ml-tabs"}>
      {tabs.map((tab) => {
        const on = tab.id === active;
        return (
          <button
            key={tab.id}
            role="tab"
            type="button"
            aria-selected={on}
            data-testid={`ml-tab-${tab.id}`}
            onClick={() => setSearch({ tab: tab.id === tabs[0]?.id ? undefined : tab.id, sel: undefined })}
            className={cn(
              "-mb-px inline-flex items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2 text-ui",
              on ? "border-primary font-medium text-ink" : "border-transparent text-ink-muted hover:text-ink",
            )}
          >
            {tab.tone && tab.tone !== "neutral" && <span className={cn("h-1.5 w-1.5 rounded-full", TONE_DOT[tab.tone])} aria-hidden="true" />}
            {tab.label}
            {tab.count !== undefined && <span className="rounded-full bg-ink/5 px-1.5 text-meta tabular-nums text-ink-muted">{tab.count}</span>}
          </button>
        );
      })}
    </div>
  );
}

/** The active tab id of a set of tabs (the first when the URL names none of them). */
export function useActiveTab(tabs: readonly { id: string }[]): string {
  const [search] = useMlSearch();
  return tabs.some((t) => t.id === search.tab) ? (search.tab as string) : (tabs[0]?.id ?? "");
}

// ── Tables ────────────────────────────────────────────────────────────────────

export interface MlColumn<T> {
  key: string;
  header: ReactNode;
  render: (row: T) => ReactNode;
  /** Right-aligned, tabular figures. */
  numeric?: boolean;
  /** A value to sort by; a sortable column has one. */
  sort?: (row: T) => number | string;
  className?: string;
  width?: string;
}

/**
 * A dense table (32px rows). A row with an href opens it on click (the
 * cells' own links still work); `selected` marks the row whose side panel is
 * open. Sorting is local and optional per column.
 */
export function MlTable<T>({
  columns,
  rows,
  rowKey,
  rowHref,
  onRowClick,
  selected,
  testId,
  empty,
  initialSort,
  maxRows,
}: {
  columns: readonly MlColumn<T>[];
  rows: readonly T[];
  rowKey: (row: T) => string;
  rowHref?: (row: T) => string | null | undefined;
  onRowClick?: (row: T) => void;
  selected?: string;
  testId?: string;
  empty?: ReactNode;
  initialSort?: { key: string; desc?: boolean };
  maxRows?: number;
}) {
  const navigate = useNavigate();
  const [sort, setSort] = useState(initialSort ?? null);
  const [expanded, setExpanded] = useState(false);
  const sorted = useMemo(() => {
    const col = sort && columns.find((c) => c.key === sort.key);
    if (!col?.sort) return rows;
    const by = col.sort;
    return [...rows].sort((a, b) => {
      const x = by(a);
      const y = by(b);
      const cmp = typeof x === "number" && typeof y === "number" ? x - y : String(x).localeCompare(String(y));
      return sort!.desc ? -cmp : cmp;
    });
  }, [rows, sort, columns]);
  const shown = maxRows && !expanded ? sorted.slice(0, maxRows) : sorted;
  if (rows.length === 0 && empty) return <div className="px-4">{empty}</div>;
  return (
    <div className="min-w-0 overflow-x-auto" data-testid={testId}>
      <table className="w-full border-collapse text-ui">
        <thead>
          <tr className="border-b border-border text-left text-meta text-ink-muted">
            {columns.map((col) => {
              const active = sort?.key === col.key;
              return (
                <th key={col.key} scope="col" className={cn("h-8 whitespace-nowrap px-3 font-medium first:pl-4 last:pr-4", col.numeric && "text-right", col.className)} style={col.width ? { width: col.width } : undefined}>
                  {col.sort ? (
                    <button
                      type="button"
                      className={cn("inline-flex items-center gap-0.5 hover:text-ink", active && "text-ink")}
                      onClick={() => setSort((prev) => (prev?.key === col.key ? { key: col.key, desc: !prev.desc } : { key: col.key, desc: true }))}
                    >
                      {col.header}
                      {active && (sort?.desc ? <ChevronDown className="h-3 w-3" /> : <ChevronUp className="h-3 w-3" />)}
                    </button>
                  ) : (
                    col.header
                  )}
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody>
          {shown.map((row) => {
            const key = rowKey(row);
            const href = rowHref?.(row) ?? null;
            const clickable = !!href || !!onRowClick;
            return (
              <tr
                key={key}
                data-testid={testId ? `${testId}-row-${key}` : undefined}
                aria-selected={selected === key || undefined}
                className={cn("h-8 border-b border-border-subtle last:border-0", clickable && "cursor-pointer hover:bg-ink/5", selected === key && "bg-ink/5")}
                onClick={(e) => {
                  if (!clickable) return;
                  if ((e.target as HTMLElement).closest("a,button,input,select")) return;
                  if (onRowClick) onRowClick(row);
                  else if (href) void navigate({ href } as never);
                }}
              >
                {columns.map((col) => (
                  <td key={col.key} className={cn("whitespace-nowrap px-3 py-1 align-middle first:pl-4 last:pr-4", col.numeric && "text-right tabular-nums", col.className)}>
                    {col.render(row)}
                  </td>
                ))}
              </tr>
            );
          })}
        </tbody>
      </table>
      {maxRows && rows.length > maxRows && (
        <button type="button" className="w-full border-t border-border-subtle py-1.5 text-meta text-ink-muted hover:text-ink" onClick={() => setExpanded((v) => !v)}>
          {expanded ? "Show fewer" : `Show all ${rows.length}`}
        </button>
      )}
    </div>
  );
}

// ── Side panel ────────────────────────────────────────────────────────────────

/** A right-hand panel over the page for one selected row (a finding, a trace). Closing clears `?sel`. */
export function SidePanel({ title, meta, onClose, children, testId, width = "min(640px, 100vw)" }: { title: ReactNode; meta?: ReactNode; onClose: () => void; children: ReactNode; testId?: string; width?: string }) {
  return (
    <aside
      className="fixed inset-y-0 right-0 z-40 flex flex-col border-l border-border bg-surface shadow-xl"
      style={{ width }}
      role="dialog"
      aria-label={typeof title === "string" ? title : "Details"}
      data-testid={testId ?? "ml-side-panel"}
    >
      <div className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div className="min-w-0 space-y-0.5">
          <h2 className="text-section text-ink">{title}</h2>
          {meta && <div className="text-meta text-ink-muted">{meta}</div>}
        </div>
        <button type="button" onClick={onClose} className="rounded-md p-1 text-ink-muted hover:bg-ink/5 hover:text-ink" aria-label="Close">
          <X className="h-4 w-4" />
        </button>
      </div>
      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 py-4">{children}</div>
    </aside>
  );
}

// ── Bars ──────────────────────────────────────────────────────────────────────

export interface BarSegment {
  label: string;
  value: number;
  /** A status tone, or a chart palette index for categorical parts. */
  tone?: MlTone;
  color?: number;
  title?: string;
}

const segColor = (s: BarSegment, i: number) =>
  s.tone ? `hsl(var(${TONE_VAR[s.tone]}))` : `hsl(var(--chart-${((s.color ?? i) % 6) + 1}))`;

/**
 * One whole split into its parts (goodput vs badput by cause, time by stage),
 * with a legend of value and share. The parts are drawn left to right in the
 * order given.
 */
export function StackedBar({ segments, format, height = 10, legend = true, testId }: { segments: readonly BarSegment[]; format: (v: number) => string; height?: number; legend?: boolean; testId?: string }) {
  const total = segments.reduce((sum, s) => sum + Math.max(0, s.value), 0);
  return (
    <div className="space-y-2" data-testid={testId}>
      <div className="flex w-full overflow-hidden rounded-full bg-surface-sunken" style={{ height }} role="img" aria-label={segments.map((s) => `${s.label} ${format(s.value)}`).join(", ")}>
        {total > 0 &&
          segments.map((s, i) => (
            <span
              key={s.label}
              className={i > 0 ? "h-full border-l border-surface" : "h-full"}
              style={{ width: `${(Math.max(0, s.value) / total) * 100}%`, backgroundColor: segColor(s, i) }}
              title={s.title ?? `${s.label}: ${format(s.value)} (${((s.value / total) * 100).toFixed(0)}%)`}
            />
          ))}
      </div>
      {legend && (
        <ul className="flex flex-wrap gap-x-4 gap-y-1 text-meta">
          {segments.map((s, i) => (
            <li key={s.label} className="inline-flex items-center gap-1.5 text-ink-muted">
              <span className="h-2 w-2 rounded-sm" style={{ backgroundColor: segColor(s, i) }} aria-hidden="true" />
              {s.label} <span className="tabular-nums text-ink">{format(s.value)}</span>
              {total > 0 && <span className="tabular-nums">· {((s.value / total) * 100).toFixed(0)}%</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** An inline meter: a ratio as a short bar and its number (allocation, KV cache, utilisation). */
export function Meter({ ratio, label, tone, width = 56 }: { ratio: number | null | undefined; label?: ReactNode; tone?: MlTone; width?: number }) {
  const ok = typeof ratio === "number" && Number.isFinite(ratio);
  const r = ok ? Math.max(0, Math.min(1, ratio)) : 0;
  return (
    <span className="inline-flex items-center justify-end gap-2">
      <span className="inline-block h-1.5 overflow-hidden rounded-full bg-surface-sunken" style={{ width }} aria-hidden="true">
        {ok && <span className="block h-full rounded-full" style={{ width: `${r * 100}%`, backgroundColor: tone ? `hsl(var(${TONE_VAR[tone]}))` : "hsl(var(--chart-1))" }} />}
      </span>
      <span className="tabular-nums">{label ?? (ok ? `${Math.round(r * 100)}%` : "—")}</span>
    </span>
  );
}

/** Facts as a compact two-column list (a model card, a config). */
export function FactList({ facts, columns = 2, testId }: { facts: readonly { label: string; value: ReactNode }[]; columns?: 1 | 2 | 3; testId?: string }) {
  return (
    <dl className={cn("grid gap-x-6 gap-y-1.5 text-ui", columns === 1 ? "grid-cols-1" : columns === 2 ? "grid-cols-1 sm:grid-cols-2" : "grid-cols-1 sm:grid-cols-3")} data-testid={testId}>
      {facts.map((f) => (
        <div key={f.label} className="flex min-w-0 items-baseline justify-between gap-3 border-b border-border-subtle py-1">
          <dt className="shrink-0 text-ink-muted">{f.label}</dt>
          <dd className="min-w-0 truncate text-right text-ink">{f.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** A responsive grid of panels. */
export function Grid({ cols = 2, children, className }: { cols?: 1 | 2 | 3 | 4; children: ReactNode; className?: string }) {
  return (
    <div className={cn("grid min-w-0 items-start gap-4", cols === 1 ? "grid-cols-1" : cols === 2 ? "grid-cols-1 xl:grid-cols-2" : cols === 3 ? "grid-cols-1 lg:grid-cols-2 xl:grid-cols-3" : "grid-cols-1 md:grid-cols-2 xl:grid-cols-4", className)}>
      {children}
    </div>
  );
}
