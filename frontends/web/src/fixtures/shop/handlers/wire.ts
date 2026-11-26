// Copyright (c) 2025 Reliant Labs

/**
 * What every shop handler needs to put the story's model on the wire and read
 * a request back into it: time, durations, counts and points. Each module
 * of services/controlPlane/observability maps wire → model; a handler is the
 * same mapping run backwards.
 */

import { create } from "@bufbuild/protobuf";
import { DurationSchema, TimestampSchema, type Duration, type Timestamp } from "@bufbuild/protobuf/wkt";

import { demoNow } from "../shopStory";

/** Epoch ms → Timestamp, keeping the sub-millisecond part (api.ts's msOf reads it back). */
export function ts(ms: number): Timestamp {
  const seconds = Math.floor(ms / 1000);
  return create(TimestampSchema, { seconds: BigInt(seconds), nanos: Math.round((ms - seconds * 1000) * 1e6) });
}

/** A model time that may be unset (0 or null) → an absent Timestamp, as a server leaves it unset. */
export const tsOpt = (ms: number | null | undefined): Timestamp | undefined => (ms ? ts(ms) : undefined);

export function msOf(t: Timestamp | undefined): number {
  return t ? Number(t.seconds) * 1000 + t.nanos / 1e6 : 0;
}

export function dur(ms: number): Duration {
  const seconds = Math.floor(ms / 1000);
  return create(DurationSchema, { seconds: BigInt(seconds), nanos: Math.round((ms - seconds * 1000) * 1e6) });
}

export const durOpt = (ms: number | null | undefined): Duration | undefined => (ms === null || ms === undefined ? undefined : dur(ms));

export function msOfDuration(d: Duration | undefined): number | undefined {
  return d ? Number(d.seconds) * 1000 + d.nanos / 1e6 : undefined;
}

/** A model count → int64. */
export const int64 = (n: number | null | undefined): bigint => BigInt(Math.round(n ?? 0));

/** A model double that may be null (a gap) → the wire's NaN, which the client reads back as null. */
export const dbl = (n: number | null | undefined): number => (n === null || n === undefined ? Number.NaN : n);

/**
 * The clock a handler answers at: the pack's fixed instant (shopStory.ts
 * demoNow, the page load or a pinned screenshot time), never the wall clock,
 * so a window resolves to the same instant on every read of the story. A
 * number is that instant itself (a test's).
 */
export type PackClock = (() => number) | number;

/** The instant `clock` names. */
export function instantOf(clock: PackClock): number {
  return typeof clock === "number" ? clock : clock();
}

/**
 * A TimeRange back into a window, or undefined when the request names none. A
 * `relative` one (RelativeWindow) is resolved as control-plane resolves it,
 * against the clock it answers at: `clock`, the pack's (demoNow) by default.
 */
export function windowOf(
  range: { start?: Timestamp; end?: Timestamp; relative?: { last?: Duration; endsAgo?: Duration } } | undefined,
  clock: PackClock = demoNow,
): { startMs: number; endMs: number } | undefined {
  const last = msOfDuration(range?.relative?.last);
  if (last) {
    const endMs = instantOf(clock) - (msOfDuration(range?.relative?.endsAgo) ?? 0);
    return { startMs: endMs - last, endMs };
  }
  return range?.start && range.end ? { startMs: msOf(range.start), endMs: msOf(range.end) } : undefined;
}

/** A model series (epoch seconds, values with null gaps) as the wire's TimePoints. */
export function points(timestamps: readonly number[], values: readonly (number | null)[], partial?: readonly boolean[]) {
  return timestamps.map((t, i) => ({ time: ts(t * 1000), value: values[i] ?? 0, null: values[i] === null || values[i] === undefined, partial: partial?.[i] ?? false }));
}
