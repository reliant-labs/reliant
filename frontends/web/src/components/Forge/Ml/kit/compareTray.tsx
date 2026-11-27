// Copyright (c) 2026 Reliant Labs

/**
 * The compare tray (02-views §0.7): runs picked from the Training list or a
 * run's page, held across pages for the tab's session, and a bar at the
 * bottom of the Training pages that opens the comparison of 2–4 of them.
 *
 * The selection is the tab's (sessionStorage), not the URL's: it follows the
 * reader from the list into a run and back. The comparison page itself is
 * deep-linkable (`runs/compare?compare=a,b,c`), so a shared link never
 * depends on the tray.
 */

import { useCallback, useSyncExternalStore } from "react";
import { GitCompare, X } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { cn } from "@/lib/utils";

import { mlHref } from "./links";

/** At most this many runs are compared at once (four curves stay readable). */
export const COMPARE_MAX = 4;

export interface TrayRun {
  id: string;
  name: string;
}

const KEY = "ml.compare-tray";
const listeners = new Set<() => void>();
let cache: { raw: string | null; runs: TrayRun[] } = { raw: null, runs: [] };

function read(): TrayRun[] {
  let raw: string | null = null;
  try {
    raw = typeof sessionStorage === "undefined" ? null : sessionStorage.getItem(KEY);
  } catch {
    raw = null;
  }
  if (raw === cache.raw) return cache.runs;
  let runs: TrayRun[] = [];
  try {
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    if (Array.isArray(parsed)) runs = parsed.filter((r): r is TrayRun => !!r && typeof r.id === "string" && typeof r.name === "string").slice(0, COMPARE_MAX);
  } catch {
    runs = [];
  }
  cache = { raw, runs };
  return runs;
}

function write(runs: readonly TrayRun[]) {
  try {
    sessionStorage.setItem(KEY, JSON.stringify(runs.slice(0, COMPARE_MAX)));
  } catch {
    // A storage refusal (private mode, quota) keeps the in-memory tray only.
    cache = { raw: "\u0000", runs: runs.slice(0, COMPARE_MAX) };
  }
  listeners.forEach((l) => l());
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** Pure: the tray after toggling `run` (added at the end; full trays refuse more). */
export function toggled(runs: readonly TrayRun[], run: TrayRun): TrayRun[] {
  if (runs.some((r) => r.id === run.id)) return runs.filter((r) => r.id !== run.id);
  return runs.length >= COMPARE_MAX ? [...runs] : [...runs, run];
}

/** The tray's runs and how to change them. */
export function useCompareTray() {
  const runs = useSyncExternalStore(subscribe, read, () => [] as TrayRun[]);
  const toggle = useCallback((run: TrayRun) => write(toggled(read(), run)), []);
  const clear = useCallback(() => write([]), []);
  return { runs, toggle, clear, has: (id: string) => runs.some((r) => r.id === id), full: runs.length >= COMPARE_MAX };
}

/** The bar at the bottom of a Training page while the tray holds a run. */
export function CompareTray() {
  const { runs, toggle, clear } = useCompareTray();
  if (runs.length === 0) return null;
  const ready = runs.length >= 2;
  return (
    <div
      className="sticky bottom-3 z-30 mx-auto flex w-fit max-w-full flex-wrap items-center gap-2 rounded-xl border border-border bg-surface px-3 py-2 shadow-lg"
      role="region"
      aria-label="Runs to compare"
      data-testid="ml-compare-tray"
    >
      <GitCompare className="h-4 w-4 text-ink-muted" aria-hidden="true" />
      <span className="text-meta text-ink-muted">
        Compare {runs.length}/{COMPARE_MAX}
      </span>
      {runs.map((r) => (
        <span key={r.id} className="inline-flex items-center gap-1 rounded-md border border-border px-1.5 py-0.5 text-meta text-ink">
          <Link href={mlHref("runs", [r.name || r.id])} className="hover:underline">
            {r.name}
          </Link>
          <button type="button" onClick={() => toggle(r)} className="rounded text-ink-muted hover:text-ink" aria-label={`Remove ${r.name}`}>
            <X className="h-3 w-3" />
          </button>
        </span>
      ))}
      <Link
        href={ready ? mlHref("runs", ["compare"], { compare: runs.map((r) => r.id).join(",") }) : "#"}
        aria-disabled={!ready}
        onClick={(e) => {
          if (!ready) e.preventDefault();
        }}
        className={cn(
          "inline-flex h-7 items-center rounded-md px-2.5 text-ui font-medium",
          ready ? "bg-primary text-primary-foreground hover:bg-primary/90" : "cursor-not-allowed border border-border text-ink-muted",
        )}
        title={ready ? undefined : "Pick at least 2 runs"}
        data-testid="ml-compare-tray-open"
      >
        Compare
      </Link>
      <button type="button" onClick={clear} className="text-meta text-ink-muted hover:text-ink" data-testid="ml-compare-tray-clear">
        Clear
      </button>
    </div>
  );
}
