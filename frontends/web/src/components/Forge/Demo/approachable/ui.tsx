// Copyright (c) 2026 Reliant Labs

/**
 * The plain-language pages' few building blocks, so every simple page reads
 * the same way: a title (with the technical term beside it, muted) and ONE
 * summary sentence first, then a short list of big, obviously clickable
 * cards. The detail an engineer would want — raw names, IDs, p95s — is never
 * hidden: it sits under each card as a quieter second line (TechLine).
 *
 * Every clickable thing looks clickable: a card has a border that lights on
 * hover, a focus ring, and an "Open" with a chevron; a button is a button.
 * Targets are at least 44px tall. Colours are forge-ui tokens only.
 */

import { useState, type ReactNode } from "react";
import { ChevronRight, SlidersHorizontal } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { cn } from "@/lib/utils";

import { setDemoViewMode } from "./viewMode";

export type Tone = "good" | "bad" | "warn" | "neutral";

const DOT: Record<Tone, string> = {
  good: "bg-success",
  bad: "bg-danger",
  warn: "bg-warning",
  neutral: "bg-ink-subtle",
};

export function StatusDot({ tone, className }: { tone: Tone; className?: string }) {
  return <span className={cn("inline-block h-2.5 w-2.5 shrink-0 rounded-full", DOT[tone], className)} aria-hidden="true" />;
}

/** The console's word for a plain name, muted beside it: "Slow requests (traces)". */
export function Term({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn("font-normal text-ink-subtle", className)}>({children})</span>;
}

/** A page of the simple view: title (and its technical term), one summary sentence, then the body. */
export function SimplePage({ title, term, summary, tone, children, testId }: { title: string; term?: string; summary: ReactNode; tone?: Tone; children: ReactNode; testId: string }) {
  return (
    <div className="mx-auto w-full max-w-5xl space-y-8 pb-16 pt-2" data-testid={testId} data-simple-page="">
      <header className="space-y-3">
        <h1 className="text-3xl font-semibold tracking-tight text-ink">
          {title}
          {term && <Term className="ml-2 text-2xl">{term}</Term>}
        </h1>
        <p className="flex max-w-3xl items-start gap-3 text-lg leading-relaxed text-ink">
          {tone && <StatusDot tone={tone} className="mt-2.5" />}
          <span>{summary}</span>
        </p>
      </header>
      {children}
    </div>
  );
}

export function Section({ title, about, children, action }: { title: string; about?: ReactNode; children: ReactNode; action?: ReactNode }) {
  return (
    <section className="space-y-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="space-y-1">
          <h2 className="text-xl font-semibold text-ink">{title}</h2>
          {about && <p className="text-base text-ink-muted">{about}</p>}
        </div>
        {action}
      </div>
      {children}
    </section>
  );
}

/** "Open" with a chevron: the visible promise that a card goes somewhere. */
export function OpenCue({ label = "Open" }: { label?: string }) {
  return (
    <span className="inline-flex shrink-0 items-center gap-1 text-base font-medium text-primary-ink">
      {label}
      <ChevronRight className="h-4 w-4 transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
    </span>
  );
}

const CARD = "group block rounded-2xl border border-border bg-surface p-5 transition-colors hover:border-primary/50 hover:bg-ink/[0.02] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/** A whole card that links somewhere: border lights on hover, focus ring, "Open ›". */
export function CardLink({ href, children, cue, className, testId }: { href: string; children: ReactNode; cue?: string; className?: string; testId?: string }) {
  return (
    <Link href={href} className={cn(CARD, "flex min-h-[44px] flex-col items-start gap-3 sm:flex-row sm:items-center sm:gap-4", className)} data-testid={testId}>
      <span className="w-full min-w-0 flex-1">{children}</span>
      <OpenCue label={cue} />
    </Link>
  );
}

/** A card that does not link: a plain container in the same shape. */
export function Panel({ children, className, ...rest }: { children: ReactNode; className?: string; "data-testid"?: string }) {
  return (
    <div className={cn("rounded-2xl border border-border bg-surface p-5", className)} {...rest}>
      {children}
    </div>
  );
}

/**
 * The technical detail, shown inline as a quieter second line rather than
 * behind a disclosure: raw names in mono, figures as the console says them.
 * Falsy items are skipped, so a caller can list what it has.
 */
export function TechLine({ items, className }: { items: (ReactNode | false | null | undefined)[]; className?: string }) {
  const shown = items.filter((item) => item !== false && item !== null && item !== undefined && item !== "");
  if (!shown.length) return null;
  return (
    <span className={cn("mt-2 block text-sm leading-relaxed text-ink-subtle [overflow-wrap:anywhere]", className)} data-testid="tech-line">
      {shown.map((item, index) => (
        <span key={index}>
          {index > 0 && (
            <span aria-hidden="true" className="px-1.5">
              ·
            </span>
          )}
          {item}
        </span>
      ))}
    </span>
  );
}

/** A raw name inside a TechLine. */
export const Code = ({ children }: { children: ReactNode }) => <code className="font-mono text-[0.8125rem] text-ink-muted">{children}</code>;

/** The first `limit` items, and "Show all N" for the rest. */
export function ShortList<T>({ items, limit = 5, render, noun }: { items: readonly T[]; limit?: number; render: (item: T) => ReactNode; noun: string }) {
  const [all, setAll] = useState(false);
  const shown = all ? items : items.slice(0, limit);
  return (
    <div className="space-y-3">
      {shown.map(render)}
      {items.length > limit && (
        <button
          type="button"
          onClick={() => setAll((was) => !was)}
          className="inline-flex min-h-11 items-center rounded-lg border border-border bg-surface px-4 text-base font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {all ? "Show fewer" : `Show all ${items.length} ${noun}`}
        </button>
      )}
    </div>
  );
}

/** The way into the full console from a simple page: the Advanced view. */
export function ShowDetailedView({ what }: { what: string }) {
  return (
    <div className="flex flex-wrap items-center gap-3 border-t border-border pt-6 text-base text-ink-muted">
      <span>Want every number, filter and chart?</span>
      <button
        type="button"
        onClick={() => setDemoViewMode("advanced")}
        className="inline-flex min-h-11 items-center gap-2 rounded-lg border border-border bg-surface px-4 font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid="simple-show-detailed"
      >
        <SlidersHorizontal className="h-4 w-4" aria-hidden="true" />
        Show the detailed {what}
      </button>
    </div>
  );
}

/** "40 minutes ago", "3 hours ago", "6 days ago". */
export function ago(ms: number): string {
  const minutes = Math.round(ms / 60_000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? "" : "s"} ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} ago`;
  const days = Math.round(hours / 24);
  return `${days} day${days === 1 ? "" : "s"} ago`;
}

/** 48 → "under 0.1s", 320 → "0.3s", 3000 → "3.0s": nobody reads milliseconds. */
export function seconds(ms: number): string {
  if (ms < 100) return "under 0.1s";
  return `${(ms / 1000).toFixed(1)}s`;
}

export const count = (n: number) => Math.round(n).toLocaleString("en-US");

/** "maya" → "Maya": a person's handle at the start of a sentence. */
export const capital = (text: string) => text.charAt(0).toUpperCase() + text.slice(1);
