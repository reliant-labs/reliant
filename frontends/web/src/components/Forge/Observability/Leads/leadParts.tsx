// Copyright (c) 2026 Reliant Labs

/**
 * The pieces every led page is built from (Console/ledConsole.tsx): a title
 * with its plain subtitle and one summary sentence, a card for the one item
 * worth opening first, compact one-line rows with "Show all", and the
 * "What happened" block on a detail page. Presentational only: the sentences
 * come from leadStory.ts, the data from the page.
 */

import { useState, type ReactNode } from "react";
import { ChevronRight, SlidersHorizontal } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { cn } from "@/lib/utils";

import type { Tone } from "./leadStory";

const DOT: Record<Tone, string> = { good: "bg-success", warn: "bg-warning", bad: "bg-danger" };

/** A status dot that sits on the first line of the text beside it. */
export function ToneDot({ tone }: { tone: Tone }) {
  return <span className={cn("mt-[0.45rem] inline-block h-1.5 w-1.5 shrink-0 rounded-full", DOT[tone])} aria-hidden="true" />;
}

/** One summary sentence, led by its tone. */
export function SummaryLine({ tone, children }: { tone?: Tone; children: ReactNode }) {
  return (
    <p className="flex max-w-4xl items-start gap-2 text-sm leading-relaxed text-ink" data-testid="lead-summary">
      {tone && <ToneDot tone={tone} />}
      <span className="min-w-0 [overflow-wrap:anywhere]">{children}</span>
    </p>
  );
}

/** A list page's header: its title and plain subtitle on one line, an action on the right, then its sentence. */
export function ListLeadHeader({ title, subtitle, action, children }: { title: string; subtitle?: string; action?: ReactNode; children: ReactNode }) {
  return (
    <header className="space-y-1">
      <div className="flex min-h-8 items-center justify-between gap-3">
        <div className="flex min-w-0 items-baseline gap-x-2.5">
          <h1 className="shrink-0 text-base font-semibold tracking-tight text-ink">{title}</h1>
          {subtitle && <p className="min-w-0 truncate text-sm text-ink-muted max-sm:hidden">{subtitle}</p>}
        </div>
        {action}
      </div>
      {children}
    </header>
  );
}

/** The way to the console's full list, with every filter. */
export function FullListLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <Link
      href={href}
      className="inline-flex min-h-8 shrink-0 items-center gap-1.5 rounded-md border border-border bg-surface px-2.5 text-sm font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      data-testid="lead-full-list"
    >
      <SlidersHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
      {children}
    </Link>
  );
}

/** The one item a list leads with, as a card; its CardOpen link covers the whole card. */
export function LeadCard({ label, testId, className, attrs, children }: { label: string; testId: string; className?: string; attrs?: Record<`data-${string}`, string>; children: ReactNode }) {
  return (
    <section
      {...attrs}
      className={cn("group relative rounded-xl border border-border bg-surface p-4 transition-colors hover:border-primary/50 focus-within:ring-2 focus-within:ring-ring", className)}
      aria-label={label}
      data-testid={testId}
    >
      {children}
    </section>
  );
}

/** "Open issue ›": the card's own link, stretched over the whole card. */
export function CardOpen({ href, label, testId }: { href: string; label: string; testId?: string }) {
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
export function ListHeading({ id, count, children }: { id: string; count?: number; children: ReactNode }) {
  return (
    <h2 id={id} className="text-sm font-medium text-ink">
      {children}
      {count !== undefined && <span className="ml-1.5 text-xs font-normal tabular-nums text-ink-muted">{count}</span>}
    </h2>
  );
}

/** How many rows a led list shows before "Show all". */
export const LEAD_TOP = 10;

/**
 * At most `top` compact one-line rows in four columns, each row one link
 * (hover, focus ring, chevron), then "Show all N". The middle two columns
 * hide on a phone.
 */
export function TopRows<T>({
  label,
  columns,
  rows,
  rowKey,
  rowHref,
  cells,
  noun,
  top = LEAD_TOP,
  rowAttrs,
}: {
  label: string;
  columns: [string, string, string, string];
  rows: readonly T[];
  rowKey: (row: T) => string;
  rowHref: (row: T) => string;
  cells: (row: T) => [ReactNode, ReactNode, ReactNode, ReactNode];
  noun: string;
  top?: number;
  /** Data attributes for a row (a stable target for the guided tour). */
  rowAttrs?: (row: T) => Record<`data-${string}`, string>;
}) {
  const [all, setAll] = useState(false);
  const shown = all ? rows : rows.slice(0, top);
  return (
    <div className="space-y-2">
      <div className="overflow-hidden rounded-xl border border-border bg-surface">
        <table className="w-full table-fixed text-sm" aria-label={label}>
          <thead>
            <tr className="border-b border-border text-left text-xs text-ink-muted">
              <th scope="col" className="w-auto px-3 py-2 font-medium">
                {columns[0]}
              </th>
              <th scope="col" className="w-24 px-3 py-2 text-right font-medium max-sm:hidden">
                {columns[1]}
              </th>
              <th scope="col" className="w-24 px-3 py-2 text-right font-medium max-sm:hidden">
                {columns[2]}
              </th>
              <th scope="col" className="w-24 px-3 py-2 text-right font-medium">
                {columns[3]}
              </th>
              <th scope="col" className="w-8 px-1">
                <span className="sr-only">Open</span>
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border">
            {shown.map((row) => {
              const [first, second, third, fourth] = cells(row);
              return (
                <tr key={rowKey(row)} {...rowAttrs?.(row)} className="group relative cursor-pointer transition-colors hover:bg-ink/[0.03] focus-within:bg-ink/[0.03]">
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

/** A row's first cell: an optional marker, its name (truncated, its full text on hover), and a muted note after it. */
export function RowName({ marker, name, note, strong = true }: { marker?: ReactNode; name: string; note?: string; strong?: boolean }) {
  return (
    <>
      {marker}
      <span className={cn("min-w-0 truncate", strong ? "font-medium text-ink" : "text-ink")} title={name}>
        {name}
      </span>
      {note && (
        <span className="min-w-0 shrink truncate text-xs text-ink-muted max-sm:hidden" title={note}>
          {note}
        </span>
      )}
    </>
  );
}

export const RedDot = ({ label }: { label: string }) => <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-danger" role="img" aria-label={label} />;

export function ErrorBadge({ errors }: { errors: number }) {
  return (
    <span className="shrink-0 rounded-full border border-danger-border bg-danger-surface px-1.5 text-2xs font-semibold leading-4 text-danger-ink">
      {errors === 1 ? "1 error" : `${errors} errors`}
    </span>
  );
}

export interface Fact {
  value: ReactNode;
  label?: string;
  tone?: "bad";
}

/** The top of a detail page: "What happened" in a line (when the data carries one), and the key facts as one short row. */
export function DetailLead({ story, facts, children }: { story: string | null; facts: readonly Fact[]; children?: ReactNode }) {
  if (!story && !facts.length) return null;
  return (
    <section className="rounded-lg border border-primary/25 bg-primary/5 px-3 py-2" aria-label="What happened" data-testid="lead-what-happened">
      {story && (
        <p className="text-sm text-ink [overflow-wrap:anywhere]">
          <span className="font-semibold">What happened: </span>
          {story}
        </p>
      )}
      {children}
      {facts.length > 0 && (
        <ul className={cn("flex flex-wrap gap-x-4 gap-y-0.5 text-xs text-ink-muted", story && "mt-1")} aria-label="Key facts" data-testid="lead-key-facts">
          {facts.map((fact, index) => (
            <li key={index} className="min-w-0 max-w-full truncate">
              <span className={cn("font-medium tabular-nums", fact.tone === "bad" ? "text-danger-ink" : "text-ink")}>{fact.value}</span>
              {fact.label && <> {fact.label}</>}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
