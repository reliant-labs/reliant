/**
 * Scroll debug recorder — an instrument for the chat timeline's scroll
 * jitter investigation (see data/scroll-jitter-brief.md).
 *
 * Two controllers write `scrollTop` to the timeline's scroller: our own
 * follow-to-bottom layer, and react-virtuoso's internal SIZE_INCREASED
 * correction, which fires on its own when a rendered row grows and emits NO
 * event we can subscribe to. This recorder samples scrollTop every animation
 * frame and lets call sites stamp "why" via `mark()`, so a scrollTop movement
 * with no explaining input event or programmatic call can be attributed to
 * that otherwise-invisible correction.
 *
 * Every exported function is a bare `if (!enabled) return` when off — no rAF
 * loop, no observers, no listeners, no allocation. This ships in the normal
 * build and must cost nothing when disabled.
 *
 * ON BY DEFAULT in dev builds (see getIsDev in ./constants) so a jitter report
 * is never missed for lack of having enabled the recorder first. It also
 * auto-detects a jitter pattern (a large or bursty run of `correction` frames)
 * and dumps the surrounding frames to disk — see AUTO-DUMP below — so "it
 * jittered" is answerable from a file instead of a devtools console that was
 * never open.
 *
 * `localStorage.setItem('reliant:scrollDebug', '0')` forces it off (read once
 * at module init); `'1'` forces it on outside of dev. `window.__scrollDebug.*`
 * remains available from the console for manual enable/disable/summary.
 */

import { getIsDev } from "./constants";

type Attribution = "user" | "ours" | "correction" | "unknown" | "none";

interface MarkEvent {
  event: string;
  detail?: unknown;
  t: number;
}

export interface ScrollFrame {
  t: number;
  scrollTop: number;
  delta: number;
  distanceFromBottom: number;
  scrollHeight: number;
  clientHeight: number;
  attribution: Attribution;
  marks: MarkEvent[];
}

/** Snapshot the recorder pulls at dump time — cheap to read, not sampled per frame. */
export interface ScrollDebugContextSnapshot {
  itemCount: number;
  isStreaming: boolean;
  followState: { atBottom: boolean; userScrolledUp: boolean } | null;
}

interface RowResizeEvent {
  t: number;
  rowKey: string;
  oldHeight: number;
  newHeight: number;
  delta: number;
  aboveViewport: boolean;
}

export interface ScrollDebugHeader {
  triggerReason: string;
  timestamp: string;
  itemCount: number | null;
  isStreaming: boolean | null;
  followState: { atBottom: boolean; userScrolledUp: boolean } | null;
  viewportHeight: number | null;
  scrollHeight: number | null;
  rowResizeEvents: RowResizeEvent[];
}

export interface ScrollDebugDumpFile {
  header: ScrollDebugHeader;
  frames: ScrollFrame[];
}

/**
 * A movement inside this window of a raw input signal (wheel/touch/key) is
 * "user". Matches the gesture-gap reasoning in followState.ts: an input event
 * and the inertial tail that follows it belong to the same intent.
 */
const INPUT_ATTRIBUTION_WINDOW_MS = 150;

/**
 * A movement inside this window of a followOutput/scrollToIndex call is
 * "ours". Wider than the input window because a scrollToIndex on a
 * virtualized list settles over several frames — Virtuoso jumps using
 * estimated row heights, measures what rendered, and corrects — and every one
 * of those frames belongs to the call that started it. Matches
 * PROGRAMMATIC_CLAIM_TTL_MS in followState.ts for the same reason.
 */
const SELF_SCROLL_ATTRIBUTION_WINDOW_MS = 500;

/** ~2000 frames at 60fps is ~30s of history. */
const RING_CAPACITY = 2000;

// --- Auto-dump thresholds ---
//
// The two shapes of "correction" that ARE the visible jitter: one big
// unexplained jump, or several smaller unexplained jumps close together.
// Either one auto-flushes the ring buffer to disk so a report of "it
// jittered" is answerable without the user having had devtools open.

/** A single 'correction' frame at or above this |delta| triggers a dump on its own. */
export const AUTO_DUMP_LARGE_CORRECTION_PX = 4;

/** This many 'correction' frames inside AUTO_DUMP_BURST_WINDOW_MS also triggers a dump. */
export const AUTO_DUMP_BURST_COUNT = 3;

/** Window for counting a correction burst. */
export const AUTO_DUMP_BURST_WINDOW_MS = 500;

/** Frames of history kept before the trigger frame in a dump. */
export const AUTO_DUMP_PRE_TRIGGER_FRAMES = 60;

/** Frames still recorded after the trigger frame before a dump is flushed. */
export const AUTO_DUMP_POST_TRIGGER_FRAMES = 30;

/** Floor between auto-dumps so a sustained correction stream cannot spam disk. */
export const AUTO_DUMP_MIN_INTERVAL_MS = 10_000;

/**
 * The greppable marker on every emitted line. Extract a session's records with:
 *   rg '\[SCROLL-JITTER\]' .reliant/logs/main.log
 */
export const SCROLL_JITTER_TAG = "[SCROLL-JITTER]";

/** Max characters per emitted log line; longer payloads are split into numbered chunks. */
const MAX_LOG_LINE_CHARS = 3000;

/**
 * Frames carried in the emitted payload. The ring holds far more, but the log
 * is a shared, noisy file — only the frames around the trigger are worth the
 * bytes, and zero-delta frames are dropped before this cap is applied.
 */
const MAX_PAYLOAD_FRAMES = 60;

/** Refuse to emit a payload larger than this — a safety cap, not an expected size. */
const MAX_DUMP_BYTES = 256 * 1024;

/**
 * Decide whether the current frame's correction should start an auto-dump.
 * Pure function of the frame's delta and the recent correction-frame
 * timestamps (already pruned to the burst window) so it can be tested without
 * driving the recorder's rAF loop.
 */
export function evaluateAutoDumpTrigger(
  delta: number,
  recentCorrectionTimestamps: number[],
): string | null {
  if (Math.abs(delta) >= AUTO_DUMP_LARGE_CORRECTION_PX) {
    return `single-frame correction ${delta.toFixed(1)}px`;
  }
  if (recentCorrectionTimestamps.length >= AUTO_DUMP_BURST_COUNT) {
    return `${recentCorrectionTimestamps.length} correction frames within ${AUTO_DUMP_BURST_WINDOW_MS}ms`;
  }
  return null;
}

/** Rate limit: has enough time passed since the last auto-dump to start another? */
export function isAutoDumpAllowed(lastAutoDumpAt: number | null, t: number): boolean {
  return lastAutoDumpAt === null || t - lastAutoDumpAt >= AUTO_DUMP_MIN_INTERVAL_MS;
}

/** Drop timestamps older than the burst window, in place semantics via return value. */
export function pruneToWindow(timestamps: number[], t: number, windowMs: number): number[] {
  const cutoff = t - windowMs;
  let dropCount = 0;
  while (dropCount < timestamps.length && timestamps[dropCount] < cutoff) {
    dropCount++;
  }
  return dropCount === 0 ? timestamps : timestamps.slice(dropCount);
}

const now = (): number =>
  typeof performance !== "undefined" ? performance.now() : Date.now();

/**
 * Fixed-capacity circular buffer. Overwrites the oldest entry once full, so a
 * long-running debug session cannot grow unbounded.
 */
export class RingBuffer<T> {
  private buf: (T | undefined)[];
  private start = 0;
  private len = 0;
  private readonly capacity: number;

  constructor(capacity: number) {
    this.capacity = capacity;
    this.buf = new Array(capacity);
  }

  push(item: T): void {
    const idx = (this.start + this.len) % this.capacity;
    this.buf[idx] = item;
    if (this.len < this.capacity) {
      this.len++;
    } else {
      this.start = (this.start + 1) % this.capacity;
    }
  }

  toArray(): T[] {
    const out: T[] = [];
    for (let i = 0; i < this.len; i++) {
      out.push(this.buf[(this.start + i) % this.capacity] as T);
    }
    return out;
  }

  clear(): void {
    this.start = 0;
    this.len = 0;
    this.buf = new Array(this.capacity);
  }

  get length(): number {
    return this.len;
  }
}

/**
 * Classify a frame's scrollTop delta by cause.
 *
 * - 'none': no movement, nothing to attribute.
 * - 'user': a raw input signal (wheel/touch/key) landed within
 *   INPUT_ATTRIBUTION_WINDOW_MS of this frame.
 * - 'ours': we called scrollToIndex or followOutput returned a behavior
 *   within SELF_SCROLL_ATTRIBUTION_WINDOW_MS of this frame.
 * - 'unknown': neither an input nor a self-scroll baseline has been observed
 *   yet (e.g. the first frames after enabling) — there isn't enough history
 *   to rule anything out.
 * - 'correction': movement with an established baseline for both signals,
 *   but outside both windows. This is the frame we cannot explain any other
 *   way, and is the proxy for Virtuoso's unobservable SIZE_INCREASED pass.
 */
export function classifyFrame(
  delta: number,
  t: number,
  lastInputAt: number | null,
  lastSelfScrollAt: number | null,
): Attribution {
  if (delta === 0) return "none";
  if (lastInputAt !== null && t - lastInputAt <= INPUT_ATTRIBUTION_WINDOW_MS) {
    return "user";
  }
  if (
    lastSelfScrollAt !== null &&
    t - lastSelfScrollAt <= SELF_SCROLL_ATTRIBUTION_WINDOW_MS
  ) {
    return "ours";
  }
  if (lastInputAt === null && lastSelfScrollAt === null) return "unknown";
  return "correction";
}

/** True when the row is scrolled past — above the scroller's visible area. */
function isRowAboveViewport(rowEl: Element, scrollerEl: Element): boolean {
  const rowRect = rowEl.getBoundingClientRect();
  const scrollerRect = scrollerEl.getBoundingClientRect();
  return rowRect.bottom <= scrollerRect.top;
}

class ScrollDebugRecorder {
  private enabled = false;
  private scrollerEl: HTMLElement | null = null;
  private rafId: number | null = null;

  private ring = new RingBuffer<ScrollFrame>(RING_CAPACITY);
  private pendingMarks: MarkEvent[] = [];
  private prevScrollTop: number | null = null;
  private lastInputAt: number | null = null;
  private lastSelfScrollAt: number | null = null;

  private rowObserver: ResizeObserver | null = null;
  private rowKeyByElement = new WeakMap<Element, string>();
  private lastRowHeight = new WeakMap<Element, number>();
  private rowDeltaByKey = new Map<string, number>();

  /** Timestamps of recent 'correction' frames, pruned to AUTO_DUMP_BURST_WINDOW_MS. */
  private recentCorrectionTimestamps: number[] = [];
  private lastAutoDumpAt: number | null = null;
  /** Set while collecting the post-trigger frames of an in-flight auto-dump. */
  private pendingAutoDump: { reason: string; framesRemaining: number } | null = null;

  /** Supplies the cheap, non-DOM context (item count, streaming, follow state) for a dump header. */
  private contextProvider: (() => ScrollDebugContextSnapshot) | null = null;

  /**
   * Monotonic clock. Injectable so tests can advance time deterministically
   * rather than sleeping — same reasoning as createFollowState's `now`.
   */
  private readonly clock: () => number;

  constructor(clock: () => number = now) {
    this.clock = clock;
  }

  isEnabled(): boolean {
    return this.enabled;
  }

  enable(): void {
    if (this.enabled) return;
    this.enabled = true;
    this.prevScrollTop = null;
    this.lastInputAt = null;
    this.lastSelfScrollAt = null;
    this.recentCorrectionTimestamps = [];
    this.pendingAutoDump = null;
    try {
      localStorage.setItem("reliant:scrollDebug", "1");
    } catch {
      // ignore
    }
    this.rafId = requestAnimationFrame(this.tick);
    console.log(
      `[scrollDebug] enabled — reproduce the jitter; it is auto-detected and written to .reliant/logs/main.log (grep ${SCROLL_JITTER_TAG})`,
    );
  }

  disable(): void {
    if (!this.enabled) return;
    this.enabled = false;
    try {
      localStorage.setItem("reliant:scrollDebug", "0");
    } catch {
      // ignore
    }
    if (this.rafId !== null) {
      cancelAnimationFrame(this.rafId);
      this.rafId = null;
    }
    if (this.rowObserver) {
      this.rowObserver.disconnect();
      this.rowObserver = null;
    }
    this.rowKeyByElement = new WeakMap();
    this.lastRowHeight = new WeakMap();
    this.pendingAutoDump = null;
  }

  clear(): void {
    this.ring.clear();
    this.rowDeltaByKey.clear();
    this.pendingMarks = [];
  }

  /** Register (or unregister, with null) the scroller element to sample. */
  registerScroller(el: HTMLElement | null): void {
    this.scrollerEl = el;
  }

  /**
   * Register the callback the recorder reads at dump time for context that
   * has no DOM signal of its own (item count, isStreaming, follow state).
   * Not called per-frame — only when a dump is actually written.
   */
  registerContext(provider: (() => ScrollDebugContextSnapshot) | null): void {
    this.contextProvider = provider;
  }

  /** Stamp the current frame with an event. Cheap no-op when disabled. */
  mark(event: string, detail?: unknown): void {
    if (!this.enabled) return;
    const t = this.clock();
    this.pendingMarks.push({ event, detail, t });
    if (event.startsWith("input:")) {
      this.lastInputAt = t;
    } else if (event === "scrollToIndex") {
      this.lastSelfScrollAt = t;
    } else if (
      event === "followOutput" &&
      (detail as { behavior?: unknown } | undefined)?.behavior !== false
    ) {
      this.lastSelfScrollAt = t;
    }
  }

  /**
   * Ref callback for a rendered row wrapper, keyed by the same key Virtuoso
   * uses for the row (see timelineItemKey in InterleavedTimeline). Returns
   * undefined when disabled — no ResizeObserver, no allocation.
   */
  rowRef(key: string): ((el: HTMLElement | null) => (() => void) | void) | undefined {
    if (!this.enabled) return undefined;
    return (el: HTMLElement | null) => {
      if (!el) return;
      const observer = this.ensureRowObserver();
      // No ResizeObserver in this environment (e.g. jsdom under test) — the
      // ref must still behave like a normal React ref, just inert.
      if (!observer) return () => {};
      observer.observe(el);
      this.rowKeyByElement.set(el, key);
      return () => {
        observer.unobserve(el);
        this.rowKeyByElement.delete(el);
        this.lastRowHeight.delete(el);
      };
    };
  }

  dump(): void {
    const frames = this.ring.toArray().filter((f) => f.delta !== 0);
    console.table(
      frames.map((f) => ({
        t: f.t.toFixed(1),
        scrollTop: f.scrollTop.toFixed(0),
        delta: f.delta.toFixed(1),
        distFromBottom: f.distanceFromBottom.toFixed(0),
        attribution: f.attribution,
        marks: f.marks.map((m) => m.event).join(",") || "-",
      })),
    );
  }

  summary(): string {
    const frames = this.ring.toArray();
    const moved = frames.filter((f) => f.delta !== 0);

    const byClass = new Map<Attribution, { count: number; totalPx: number }>();
    for (const f of moved) {
      const bucket = byClass.get(f.attribution) ?? { count: 0, totalPx: 0 };
      bucket.count++;
      bucket.totalPx += Math.abs(f.delta);
      byClass.set(f.attribution, bucket);
    }

    const worstRows = Array.from(this.rowDeltaByKey.entries())
      .sort((a, b) => b[1] - a[1])
      .slice(0, 10);

    const biggestJumps = [...moved]
      .sort((a, b) => Math.abs(b.delta) - Math.abs(a.delta))
      .slice(0, 10);

    const lines: string[] = [];
    lines.push(
      `scrollDebug summary — ${frames.length} frames sampled, ${moved.length} with nonzero scrollTop delta`,
    );
    lines.push("");
    lines.push("By cause:");
    if (byClass.size === 0) {
      lines.push("  (no scroll movement recorded)");
    }
    for (const [cls, { count, totalPx }] of byClass) {
      lines.push(
        `  ${cls.padEnd(11)} ${String(count).padStart(5)} frames  ${totalPx
          .toFixed(0)
          .padStart(7)}px total`,
      );
    }
    lines.push("");
    lines.push("Worst offending rows (cumulative |Δheight|):");
    if (worstRows.length === 0) {
      lines.push("  (no row resizes recorded)");
    }
    for (const [key, px] of worstRows) {
      lines.push(`  ${px.toFixed(0).padStart(6)}px  ${key}`);
    }
    lines.push("");
    lines.push("Biggest single-frame jumps:");
    if (biggestJumps.length === 0) {
      lines.push("  (none)");
    }
    for (const f of biggestJumps) {
      const markNames = f.marks.map((m) => m.event).join(",") || "-";
      lines.push(
        `  ${f.delta.toFixed(0).padStart(6)}px  [${f.attribution}]  t=${f.t.toFixed(
          0,
        )}  marks=${markNames}`,
      );
    }

    const text = lines.join("\n");
    console.log(text);
    return text;
  }

  export(): string {
    return JSON.stringify(this.ring.toArray());
  }

  /**
   * Build the on-disk record for the given window of frames. This is the whole
   * diagnostic artifact — it is all that survives the session, so it carries
   * the header context that `summary()` reads live off the DOM.
   */
  buildDump(reason: string, frames: ScrollFrame[]): ScrollDebugDumpFile {
    const context = this.readContext();
    const el = this.scrollerEl;

    // Row-height changes inside the dumped window, recovered from the
    // row:resize marks already stamped onto those frames. `aboveViewport` was
    // evaluated at resize time, when the row's rect was still meaningful —
    // re-deriving it now would read a layout that has since moved on.
    const rowResizeEvents: RowResizeEvent[] = [];
    for (const frame of frames) {
      for (const m of frame.marks) {
        if (m.event !== "row:resize") continue;
        const d = m.detail as Omit<RowResizeEvent, "t"> | undefined;
        if (!d) continue;
        rowResizeEvents.push({
          t: m.t,
          rowKey: d.rowKey,
          oldHeight: d.oldHeight,
          newHeight: d.newHeight,
          delta: d.delta,
          aboveViewport: d.aboveViewport,
        });
      }
    }

    // Prefer the last sampled frame's geometry over a fresh DOM read: it is
    // the geometry that was in force when the trigger fired, and reading the
    // element here would force a layout off the sampling path.
    const lastFrame = frames.length > 0 ? frames[frames.length - 1] : null;

    return {
      header: {
        triggerReason: reason,
        timestamp: new Date().toISOString(),
        itemCount: context?.itemCount ?? null,
        isStreaming: context?.isStreaming ?? null,
        followState: context?.followState ?? null,
        viewportHeight: lastFrame?.clientHeight ?? el?.clientHeight ?? null,
        scrollHeight: lastFrame?.scrollHeight ?? el?.scrollHeight ?? null,
        rowResizeEvents,
      },
      frames,
    };
  }

  /**
   * Emit a dump for the retained buffer on demand. Returns the dump's name, or
   * null when disabled. Exposed on the console API as `dumpToLog()`.
   */
  dumpToLog(reason = "manual"): string | null {
    if (!this.enabled) return null;
    const name = this.writeDump(reason, this.ring.toArray());
    if (name) {
      console.log(
        `[scrollDebug] wrote record ${name} to .reliant/logs/main.log (grep ${SCROLL_JITTER_TAG})`,
      );
    }
    return name;
  }

  private readContext(): ScrollDebugContextSnapshot | null {
    if (!this.contextProvider) return null;
    try {
      return this.contextProvider();
    } catch {
      // A throwing provider must never take the recorder down with it.
      return null;
    }
  }

  /**
   * Serialize and emit the dump. Returns the dump's name, or null if nothing
   * was emitted. Emission is fire-and-forget and never awaited on the sampling
   * path — an IPC send must not stall the rAF loop it is measuring.
   */
  private writeDump(reason: string, frames: ScrollFrame[]): string | null {
    // Zero-delta frames are the overwhelming majority and carry no signal
    // beyond their marks; drop them, then keep only the most recent window.
    const moved = frames.filter((f) => f.delta !== 0 || f.marks.length > 0);
    const trimmed = moved.slice(Math.max(0, moved.length - MAX_PAYLOAD_FRAMES));

    const record = this.buildDump(reason, trimmed);
    const name = record.header.timestamp.replace(/[:.]/g, "-");

    let payload: string;
    try {
      payload = JSON.stringify(record);
    } catch {
      return null;
    }

    if (payload.length > MAX_DUMP_BYTES) {
      // Header alone still tells us the trigger and the row-resize picture.
      payload = JSON.stringify({
        header: record.header,
        frames: [],
        truncated: `payload of ${payload.length} chars exceeded the ${MAX_DUMP_BYTES} cap`,
      });
    }

    persistScrollDebug(formatSummaryLine(record), payload, name);
    return name;
  }

  /** Returns null when ResizeObserver is unavailable (e.g. jsdom) rather than throwing. */
  private ensureRowObserver(): ResizeObserver | null {
    if (typeof ResizeObserver === "undefined") return null;
    if (!this.rowObserver) {
      this.rowObserver = new ResizeObserver(this.handleRowResize);
    }
    return this.rowObserver;
  }

  private handleRowResize = (entries: ResizeObserverEntry[]): void => {
    if (!this.enabled) return;
    const scroller = this.scrollerEl;
    for (const entry of entries) {
      const el = entry.target;
      const key = this.rowKeyByElement.get(el);
      if (!key) continue;
      const newHeight = entry.contentRect.height;
      const oldHeight = this.lastRowHeight.get(el) ?? newHeight;
      this.lastRowHeight.set(el, newHeight);
      const delta = newHeight - oldHeight;
      if (delta === 0) continue;
      const aboveViewport = scroller ? isRowAboveViewport(el, scroller) : false;
      this.mark("row:resize", { rowKey: key, oldHeight, newHeight, delta, aboveViewport });
      this.rowDeltaByKey.set(key, (this.rowDeltaByKey.get(key) ?? 0) + Math.abs(delta));
    }
  };

  /**
   * Drive exactly one sample, as the rAF loop would. Test-only seam: it lets a
   * synthetic frame sequence exercise the real classification and auto-dump
   * path deterministically, instead of waiting on animation frames.
   */
  sampleFrameForTest(): void {
    this.sampleFrame();
  }

  private tick = (): void => {
    if (!this.enabled) {
      this.rafId = null;
      return;
    }
    this.sampleFrame();
    this.rafId = requestAnimationFrame(this.tick);
  };

  private sampleFrame(): void {
    const el = this.scrollerEl;
    const marks = this.pendingMarks;
    this.pendingMarks = [];
    if (!el) return;

    // scrollTop/scrollHeight/clientHeight force layout — read each exactly
    // once per frame here, never per row.
    const t = this.clock();
    const scrollTop = el.scrollTop;
    const scrollHeight = el.scrollHeight;
    const clientHeight = el.clientHeight;
    const delta = this.prevScrollTop === null ? 0 : scrollTop - this.prevScrollTop;
    this.prevScrollTop = scrollTop;
    const distanceFromBottom = scrollHeight - clientHeight - scrollTop;

    const attribution = classifyFrame(delta, t, this.lastInputAt, this.lastSelfScrollAt);

    this.ring.push({
      t,
      scrollTop,
      delta,
      distanceFromBottom,
      scrollHeight,
      clientHeight,
      attribution,
      marks,
    });

    this.evaluateAutoDump(delta, t, attribution);
  }

  /**
   * Decide whether this frame starts an auto-dump, and flush one that is
   * already collecting its post-trigger tail.
   *
   * A dump is deliberately NOT written the instant it triggers: the frames
   * AFTER the trigger are what show whether the correction settled or kept
   * fighting, so the write waits AUTO_DUMP_POST_TRIGGER_FRAMES.
   */
  private evaluateAutoDump(delta: number, t: number, attribution: Attribution): void {
    if (this.pendingAutoDump) {
      this.pendingAutoDump.framesRemaining--;
      if (this.pendingAutoDump.framesRemaining <= 0) {
        const { reason } = this.pendingAutoDump;
        this.pendingAutoDump = null;
        this.flushAutoDump(reason, t);
      }
      return;
    }

    if (attribution !== "correction") return;

    this.recentCorrectionTimestamps = pruneToWindow(
      this.recentCorrectionTimestamps,
      t,
      AUTO_DUMP_BURST_WINDOW_MS,
    );
    this.recentCorrectionTimestamps.push(t);

    const reason = evaluateAutoDumpTrigger(delta, this.recentCorrectionTimestamps);
    if (reason === null) return;
    // Rate-limited, but the trigger still counts as observed — the pruning
    // above keeps the burst window honest for the NEXT eligible dump.
    if (!isAutoDumpAllowed(this.lastAutoDumpAt, t)) return;

    this.lastAutoDumpAt = t;
    this.pendingAutoDump = {
      reason,
      framesRemaining: AUTO_DUMP_POST_TRIGGER_FRAMES,
    };
  }

  /** Take the pre-trigger history plus the post-trigger tail and emit it. */
  private flushAutoDump(reason: string, t: number): void {
    const all = this.ring.toArray();
    const windowSize = AUTO_DUMP_PRE_TRIGGER_FRAMES + AUTO_DUMP_POST_TRIGGER_FRAMES;
    const frames = all.slice(Math.max(0, all.length - windowSize));
    const name = this.writeDump(reason, frames);
    if (name) {
      console.log(
        `[scrollDebug] jitter detected (${reason}) at t=${t.toFixed(0)} — record ${name} written to .reliant/logs/main.log`,
      );
    }
  }
}

/** One-line human-readable digest, appended to summary.log beside the JSON. */
function formatSummaryLine(record: ScrollDebugDumpFile): string {
  const { header, frames } = record;
  const corrections = frames.filter((f) => f.attribution === "correction");
  const totalCorrectionPx = corrections.reduce((sum, f) => sum + Math.abs(f.delta), 0);
  const aboveViewportResizes = header.rowResizeEvents.filter((r) => r.aboveViewport);
  const follow = header.followState
    ? `atBottom=${header.followState.atBottom} userScrolledUp=${header.followState.userScrolledUp}`
    : "followState=unavailable";

  return [
    header.timestamp,
    `trigger="${header.triggerReason}"`,
    `frames=${frames.length}`,
    `corrections=${corrections.length}/${totalCorrectionPx.toFixed(0)}px`,
    `rowResizes=${header.rowResizeEvents.length} (${aboveViewportResizes.length} aboveViewport)`,
    `items=${header.itemCount ?? "?"}`,
    `streaming=${header.isStreaming ?? "?"}`,
    follow,
    `viewport=${header.viewportHeight ?? "?"} scrollHeight=${header.scrollHeight ?? "?"}`,
  ].join("  ");
}

/**
 * Persist a dump. Electron is the runtime that matters, and it already has a
 * renderer→disk path: preload's `electronAPI.log` sends on the
 * `log-from-renderer` IPC channel, which main.js forwards to electron-log,
 * which writes `.reliant/logs/main.log`. No new channel is needed.
 *
 * ⚠️ This calls `electronAPI.log` DIRECTLY rather than `console.warn` or
 * `logger.warn`. lib/logger.ts installs a global console override that runs
 * every argument through safeStringify, which TRUNCATES any string over 200
 * characters — that path would silently shred the payload into a useless
 * `...(N chars)` stub. The preload method does no such thing.
 *
 * In a plain browser there is no channel at all, so the dump degrades to a
 * file download. Neither path is allowed to throw: a failed diagnostic write
 * must not become a second bug on top of the one being diagnosed.
 */
function persistScrollDebug(headerLine: string, payload: string, name: string): void {
  const api = typeof window !== "undefined" ? window.electronAPI : undefined;

  if (typeof api?.log === "function") {
    try {
      api.log("warn", `${SCROLL_JITTER_TAG} ${headerLine}`);
      // Chunk defensively. electron-log itself imposes no line limit, but a
      // single multi-kilobyte line is awkward to read back and risks being
      // clipped by anything downstream; numbered chunks reassemble losslessly.
      const chunks = chunkString(payload, MAX_LOG_LINE_CHARS);
      for (let i = 0; i < chunks.length; i++) {
        api.log("warn", `${SCROLL_JITTER_TAG} json ${i + 1}/${chunks.length} ${chunks[i]}`);
      }
      return;
    } catch (err) {
      // Deliberately the ONE place that uses error level — a real failure.
      try {
        api.log("error", `${SCROLL_JITTER_TAG} failed to emit dump: ${String(err)}`);
      } catch {
        // ignore
      }
      return;
    }
  }

  // Plain-browser fallback: hand the user the file. Guarded because a
  // headless/jsdom environment has neither Blob URLs nor a document to click
  // through, and the recorder must stay silent there rather than throw.
  try {
    if (typeof document === "undefined" || typeof URL?.createObjectURL !== "function") {
      return;
    }
    const url = URL.createObjectURL(new Blob([payload], { type: "application/json" }));
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `scroll-debug-${name}`;
    anchor.click();
    URL.revokeObjectURL(url);
  } catch {
    // ignore — the download fallback is best-effort
  }
}

/** Split into fixed-size pieces. Returns [""] for empty input so a dump always emits. */
export function chunkString(text: string, size: number): string[] {
  if (text.length === 0) return [""];
  const out: string[] = [];
  for (let i = 0; i < text.length; i += size) {
    out.push(text.slice(i, i + size));
  }
  return out;
}

const recorder = new ScrollDebugRecorder();

/**
 * Fresh recorder instance with an injectable clock, for tests that need to
 * drive the emit path without touching the module-level singleton the app is
 * using, and without waiting on real animation frames.
 */
export function createRecorderForTest(clock: () => number): ScrollDebugRecorder {
  return new ScrollDebugRecorder(clock);
}

export function mark(event: string, detail?: unknown): void {
  recorder.mark(event, detail);
}

export function registerScroller(el: HTMLElement | null): void {
  recorder.registerScroller(el);
}

export function rowRef(
  key: string,
): ((el: HTMLElement | null) => (() => void) | void) | undefined {
  return recorder.rowRef(key);
}

/**
 * Register the timeline's context provider. Read only when a dump is written,
 * never per frame. Pass null on unmount.
 */
export function registerContext(
  provider: (() => ScrollDebugContextSnapshot) | null,
): void {
  recorder.registerContext(provider);
}

export interface ScrollDebugWindowApi {
  enable(): void;
  disable(): void;
  clear(): void;
  dump(): void;
  dumpToLog(reason?: string): string | null;
  summary(): string;
  export(): string;
}

declare global {
  interface Window {
    __scrollDebug: ScrollDebugWindowApi;
  }
}

/**
 * Whether the recorder should arm itself at module init.
 *
 * Dev builds default to ON so the jitter the user just saw was already being
 * recorded — requiring a manual enable() first meant every first report was
 * unrecorded by construction. The localStorage flag is an explicit override in
 * both directions: '0' forces off (including in dev), '1' forces on.
 */
export function shouldAutoEnable(
  storedFlag: string | null,
  isDevBuild: boolean,
): boolean {
  if (storedFlag === "0") return false;
  if (storedFlag === "1") return true;
  return isDevBuild;
}

if (typeof window !== "undefined") {
  let storedFlag: string | null = null;
  try {
    storedFlag = localStorage.getItem("reliant:scrollDebug");
  } catch {
    // ignore — private mode / disabled storage falls back to the dev default
  }
  if (shouldAutoEnable(storedFlag, getIsDev())) {
    recorder.enable();
  }
  window.__scrollDebug = recorder;
}
