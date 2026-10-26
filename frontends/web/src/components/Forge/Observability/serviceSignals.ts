// Copyright (c) 2025 Reliant Labs

/**
 * WHICH SIGNAL VIEWS A SERVICE OFFERS.
 *
 * A service that never sends profiles should not offer a Profiles tab that can
 * only ever be empty. GetServiceSignals answers, per service, what it sends in
 * a window (observed) and what the current release of its environments
 * switches on (declared, from the recorded bundle). This module turns that
 * answer into one verdict per signal, and every per-service picker, tab and
 * link reads it:
 *
 *   available  it sends the signal: offered normally (sent but undeclared too —
 *              data that exists is never hidden).
 *   idle       declared on, but nothing arrived in the window: offered muted,
 *              with a hint ("none in 24h"), so a profiler that stopped is
 *              noticed rather than silently dropped from the page.
 *   absent     neither sent nor declared: not offered.
 *
 * Anything uncertain is `available`: no service in view (the all-services
 * scope keeps every option), the answer still loading or failed (a control
 * plane that predates the RPC answers Unimplemented), or a service missing from
 * a truncated answer. Offering an empty view costs a click; hiding one that
 * has data costs the reader the data.
 *
 * The window is never narrower than a day (signalsRange): a profiler that
 * pushes once a minute would otherwise flicker in and out of a 15-minute view.
 * Within that window "absent" also means the view would be empty, so hiding it
 * never hides data the view could show.
 */

import { useMemo } from "react";

import { useServiceSignals } from "@/hooks/observability-queries";
import type { ObsScope, ServiceSignal, ServiceSignals, ServiceSignalsResult } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

/** The scope's signals index for a view over `range`; null while unknown (every view stays offered). */
export function useSignalsIndex(scope: ObsScope | null, range: RangePreset): SignalsIndex | null {
  const probe = signalsRange(range);
  const signals = useServiceSignals(scope, probe);
  return useMemo(() => signalsIndex(signals.data, probe), [signals.data, probe]);
}

export type SignalState = { kind: "available" } | { kind: "idle"; badge: string; hint: string } | { kind: "absent" };

const AVAILABLE: SignalState = { kind: "available" };

/** The window a view's signals are judged over: its own when it is a week, else a day. */
export function signalsRange(range: RangePreset): RangePreset {
  return range === "7d" ? "7d" : "24h";
}

const RANGE_WORDS: Record<"24h" | "7d", { badge: string; phrase: string }> = {
  "24h": { badge: "none in 24h", phrase: "the last 24 hours" },
  "7d": { badge: "none in 7d", phrase: "the last 7 days" },
};

const SIGNAL_WORDS: Record<ServiceSignal, string> = { logs: "logs", traces: "traces", profiles: "profiles" };

/** GetServiceSignals' answer, indexed for lookups. */
export interface SignalsIndex {
  byService: ReadonlyMap<string, ServiceSignals>;
  truncated: boolean;
  range: RangePreset;
}

export function signalsIndex(result: ServiceSignalsResult | undefined | null, range: RangePreset): SignalsIndex | null {
  if (!result) return null;
  return { byService: new Map(result.services.map((s) => [s.service, s])), truncated: result.truncated, range: signalsRange(range) };
}

/** The verdict for one service and signal. `index` null (loading, failed, unknown) or no service: available. */
export function signalState(index: SignalsIndex | null, service: string | undefined, signal: ServiceSignal): SignalState {
  if (!index || !service) return AVAILABLE;
  const known = index.byService.get(service);
  if (!known) return index.truncated ? AVAILABLE : { kind: "absent" };
  if (known.emitting.includes(signal)) return AVAILABLE;
  if (known.declarationKnown && known.declared.includes(signal)) {
    const words = RANGE_WORDS[index.range === "7d" ? "7d" : "24h"];
    return { kind: "idle", badge: words.badge, hint: `No ${SIGNAL_WORDS[signal]} from ${service} in ${words.phrase}` };
  }
  return { kind: "absent" };
}

/**
 * The services a picker for `signal` offers, in order: the ones sending it,
 * then the ones that declare it and are idle (muted). `seen` are the names the
 * picker already knew (the overview's), kept when the answer cannot judge them;
 * `current` is always kept, so a link never lands on a picker that dropped its
 * own selection.
 */
export function servicesFor(
  index: SignalsIndex | null,
  signal: ServiceSignal,
  seen: string[],
  current: string | undefined
): { name: string; state: SignalState }[] {
  const names = new Set(seen);
  if (index) for (const s of index.byService.values()) if (s.emitting.includes(signal) || (s.declarationKnown && s.declared.includes(signal))) names.add(s.service);
  if (current) names.add(current);
  const rows = [...names]
    .map((name) => ({ name, state: signalState(index, name, signal) }))
    .filter((row) => row.state.kind !== "absent" || row.name === current);
  const rank = (row: { state: SignalState }) => (row.state.kind === "idle" ? 1 : 0);
  const order = new Map(seen.map((name, i) => [name, i]));
  return rows.sort((a, b) => rank(a) - rank(b) || (order.get(a.name) ?? Infinity) - (order.get(b.name) ?? Infinity) || a.name.localeCompare(b.name));
}
