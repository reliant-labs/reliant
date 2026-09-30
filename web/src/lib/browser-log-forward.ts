// VENDORED VERBATIM from forge/web-runtime/src/devlog.ts
// (@reliantlabs/forge-web-runtime), below this header. Do not edit it here:
// fix forge, then re-copy. Its tests are vendored alongside, in
// lib/__tests__/browser-log-forward.test.ts. Once a runtime release that
// includes batched posts (protocol v2) is published, delete this file and
// import installDevLogging from "@reliantlabs/forge-web-runtime" instead.
//
// The receiving endpoint is web/vite-plugin-browser-logs.ts, which speaks the
// same v2 protocol as forge's scaffolded devlog-plugin.ts.
//
// ─────────────────────────────────────────────────────────────────────────
//
// Dev-only browser log forwarding.
//
// A browser's console is a dead end: an agent debugging a scaffolded frontend
// cannot open devtools, so every console line and — worse — every uncaught
// error is invisible to it. The backend has no such gap (structured slog, the
// observe ComponentChain, and forge's dev loop already tees each process's
// stdout to .forge/logs/<env>/). This closes the frontend half by POSTing
// browser console output to a dev-server endpoint that prints it, so it lands
// in .forge/logs/<env>/frontend_<name>.log next to the server logs.
//
// ── Why this batches (protocol v2) ───────────────────────────────────────
//
// The first version sent one fetch POST per console call. Measured in a real
// app that logs freely, that is ~80 requests/s at light load and 200+/s in a
// burst, and those requests are not free bystanders: they share the browser's
// six-connections-per-origin pool and the dev server's single event loop with
// the app's own RPCs. In the same session, RPC latency averaged 37ms in quiet
// seconds and 127ms in seconds carrying 200+ log lines. Worse, `keepalive`
// requests draw on a 64 KiB in-flight browser quota shared across the page, so
// a burst does not just slow things down — some of it gets rejected, and the
// original `.catch(() => {})` made that indistinguishable from silence.
//
// So entries are buffered and flushed as a batch:
//
//   - every FLUSH_INTERVAL_MS, timer armed when an entry lands in an empty
//     buffer (the common case: a few lines coalesce into one request),
//   - immediately once buffered output reaches MAX_BATCH_BYTES,
//   - on a microtask for anything error-level, so a crash never waits 250ms
//     while a same-tick burst still coalesces,
//   - synchronously via sendBeacon on pagehide / visibilitychange→hidden,
//     which is the only way output survives a navigation or a crash-reload.
//
// At most ONE request is in flight. Entries logged during it keep buffering and
// go out when it settles, which is what makes the printed order the log order —
// six parallel POSTs would interleave on the server.
//
// Nothing is ever dropped silently. A full buffer, a rejected request and an
// over-quota request all increment a counter, and the next batch to leave
// carries a synthetic warn line saying how many lines were lost and why. The
// failure this replaces was a dead sink that looked exactly like working code.
//
// ── Legacy endpoints ─────────────────────────────────────────────────────
//
// The receiving endpoint is a scaffold-once file the PROJECT owns (a Vite
// plugin / a Next.js route handler), so upgrading this package through npm will
// routinely meet an endpoint that predates batching. Such an endpoint answers
// 204 and prints one empty line for the whole batch — total, silent loss. A v2
// endpoint therefore returns `X-Forge-Devlog: 2`, and a 2xx without it makes
// this client fall back permanently to one v1 POST per line (plus one warn
// line telling the developer to update the endpoint).
//
// DEV ONLY, and structurally so. The receiving endpoint is a Vite plugin
// gated `apply: "serve"` / a Next.js route behind a NODE_ENV check, so it does
// not exist in a production build; installDevLogging() additionally no-ops
// unless it is handed dev: true. A production bundle therefore has no
// endpoint to post to AND no code that would post.
//
// It is also reversible: uninstallDevLogging() restores the original console
// methods, and the scaffolded call site is an ordinary line in main.tsx /
// providers.tsx that a project can delete outright.

/** Console methods this module mirrors. */
const LEVELS = ["log", "info", "warn", "error", "debug"] as const;

type Level = (typeof LEVELS)[number];

/**
 * One log line on the wire.
 *
 * Also the whole v1 body: a legacy endpoint receives exactly this object, which
 * is why the shape is unchanged and still exported.
 */
export interface DevLogPayload {
  level: Level | "error";
  msg: string;
}

/** The v2 body: many lines, one request. */
export interface DevLogBatch {
  entries: DevLogPayload[];
}

export interface DevLoggingOptions {
  /**
   * Whether to install at all. Pass `import.meta.env.DEV` (Vite) or
   * `process.env.NODE_ENV !== "production"` (Next.js). When false this is a
   * no-op, so the call site needs no conditional of its own.
   */
  dev: boolean;
  /** Endpoint the dev server serves. Defaults to the forge convention. */
  endpoint?: string;
  /**
   * Also keep writing to the real console. Default true — the browser devtools
   * experience should be unchanged; this feature ADDS a sink, it does not move
   * one.
   */
  mirrorToConsole?: boolean;
}

export const DEV_LOG_ENDPOINT = "/__forge/log";

/** Response header a batching endpoint sets. Its ABSENCE selects v1 fallback. */
const PROTOCOL_HEADER = "X-Forge-Devlog";

/** Cap a single line so a runaway loop cannot fill the disk. Matches the endpoints. */
const MAX_LINE = 8_000;

/** How long a line may wait for company before it is sent. */
const FLUSH_INTERVAL_MS = 250;

/**
 * Per-request ceiling. Deliberately half of the browser's 64 KiB shared
 * `keepalive` in-flight quota: with one request in flight, a batch cannot be
 * the thing that pushes the page over it.
 */
const MAX_BATCH_BYTES = 32 * 1024;

/** Buffer ceiling. Past it entries are dropped and counted, never queued forever. */
const MAX_BUFFER_ENTRIES = 5_000;

const original: Partial<Record<Level, (...args: unknown[]) => void>> = {};
let installed = false;

// Guards against infinite recursion. The forwarder calls fetch(), and anything
// that logs inside fetch (an interceptor, a polyfill, a devtools extension)
// would otherwise re-enter the override forever. Such a line is still mirrored
// to the real console — it is only forwarding that is suppressed.
let forwarding = false;

let endpointUrl = DEV_LOG_ENDPOINT;
let buffer: DevLogPayload[] = [];
let bufferedBytes = 0;
let flushTimer: ReturnType<typeof setTimeout> | undefined;
let requestInFlight = false;
let errorFlushQueued = false;
let droppedCount = 0;
let droppedReason = "";
/** Set for good once an endpoint is known not to understand batches. */
let legacyEndpoint = false;

/**
 * Render one console argument as a string.
 *
 * Circular structures and BigInt are the two cases a plain JSON.stringify
 * throws on, and the old fallback to String(value) turned both into
 * "[object Object]" — deleting exactly the shape you were trying to read. The
 * replacer keeps the object and names the cycle instead.
 */
function render(value: unknown): string {
  if (typeof value === "string") return value;
  if (value instanceof Error) {
    return `${value.name}: ${value.message}${value.stack ? `\n${value.stack}` : ""}`;
  }
  try {
    const seen = new WeakSet<object>();
    return (
      JSON.stringify(value, (_key, val: unknown) => {
        if (typeof val === "bigint") return `${val}n`;
        if (typeof val === "object" && val !== null) {
          if (seen.has(val)) return "[Circular]";
          seen.add(val);
        }
        return val;
      }) ?? String(value)
    );
  } catch {
    // DOM nodes, getters that throw — never let logging throw.
    return String(value);
  }
}

function truncate(msg: string): string {
  return msg.length > MAX_LINE ? `${msg.slice(0, MAX_LINE)}… (truncated)` : msg;
}

function entryBytes(entry: DevLogPayload): number {
  return JSON.stringify(entry).length;
}

/**
 * Buffer one line and decide whether it can wait.
 *
 * Arguments are rendered HERE rather than at flush time: a console argument is
 * a live object, and by the time a batch leaves 250ms later the array it logged
 * may have been mutated or emptied. The snapshot is the value at the call.
 */
function capture(level: DevLogPayload["level"], args: unknown[]): void {
  if (forwarding) return;

  if (buffer.length >= MAX_BUFFER_ENTRIES) {
    droppedCount += 1;
    droppedReason = "buffer full";
    return;
  }

  const entry: DevLogPayload = { level, msg: truncate(args.map(render).join(" ")) };
  const wasEmpty = buffer.length === 0;
  buffer.push(entry);
  bufferedBytes += entryBytes(entry);

  if (bufferedBytes >= MAX_BATCH_BYTES) {
    flush();
    return;
  }

  if (level === "error") {
    // An error is what someone is waiting for; it must not sit in a timer. The
    // microtask (rather than a direct flush) lets a same-tick burst — a failing
    // render logging three times — still leave as one request.
    if (!errorFlushQueued) {
      errorFlushQueued = true;
      queueMicrotask(() => {
        errorFlushQueued = false;
        flush();
      });
    }
    return;
  }

  if (wasEmpty) armTimer();
}

function armTimer(): void {
  if (flushTimer !== undefined) return;
  flushTimer = setTimeout(() => {
    flushTimer = undefined;
    flush();
  }, FLUSH_INTERVAL_MS);
}

function clearFlushTimer(): void {
  if (flushTimer === undefined) return;
  clearTimeout(flushTimer);
  flushTimer = undefined;
}

/** Pull entries off the front of the buffer without exceeding MAX_BATCH_BYTES. */
function takeBatch(limit = MAX_BATCH_BYTES): DevLogPayload[] {
  const batch: DevLogPayload[] = [];
  let size = '{"entries":[]}'.length;
  while (buffer.length > 0) {
    const entry = buffer[0] as DevLogPayload;
    const cost = entryBytes(entry) + (batch.length > 0 ? 1 : 0);
    // Always take at least one entry: a single line that somehow exceeds the
    // limit must still leave, or it stalls every line behind it forever.
    if (batch.length > 0 && size + cost > limit) break;
    buffer.shift();
    bufferedBytes -= entryBytes(entry);
    batch.push(entry);
    size += cost;
  }
  if (buffer.length === 0) bufferedBytes = 0;
  return batch;
}

/**
 * Prepend the "we lost lines" notice, if any, so it travels with the next batch
 * rather than needing a request of its own.
 */
function drainDroppedNotice(): void {
  if (droppedCount === 0) return;
  const notice: DevLogPayload = {
    level: "warn",
    msg: `[forge-devlog] dropped ${droppedCount} lines (${droppedReason})`,
  };
  buffer.unshift(notice);
  bufferedBytes += entryBytes(notice);
  droppedCount = 0;
  droppedReason = "";
}

function post(body: string): Promise<Response> {
  // The recursion guard must wrap the CALL, not the promise: a fetch polyfill
  // that logs does so synchronously from inside fetch().
  forwarding = true;
  try {
    return fetch(endpointUrl, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body,
      // A log emitted during unload (the interesting case for a crash on
      // navigation) is cancelled without this.
      keepalive: true,
      // Never let log forwarding trip a credentials or CORS policy.
      credentials: "omit",
    });
  } finally {
    forwarding = false;
  }
}

/**
 * Send one request's worth of buffered output, if nothing is in flight.
 *
 * The single-flight rule is what guarantees the dev server prints lines in the
 * order they were logged, and it doubles as backpressure: a slow endpoint makes
 * batches bigger rather than making requests pile up.
 */
function flush(): void {
  if (requestInFlight) return;
  drainDroppedNotice();
  if (buffer.length === 0) return;
  clearFlushTimer();

  const batch = legacyEndpoint ? takeBatch(0) : takeBatch();
  if (batch.length === 0) return;

  const body = legacyEndpoint
    ? JSON.stringify(batch[0])
    : JSON.stringify({ entries: batch } satisfies DevLogBatch);

  requestInFlight = true;
  post(body).then(
    (response) => {
      requestInFlight = false;
      if (!response.ok) {
        countDropped(batch.length, `endpoint returned ${response.status}`);
      } else if (!legacyEndpoint && !response.headers.get(PROTOCOL_HEADER)) {
        adoptLegacyMode(batch);
      }
      if (buffer.length > 0 || droppedCount > 0) flush();
    },
    () => {
      requestInFlight = false;
      countDropped(batch.length, "request failed");
      if (buffer.length > 0) flush();
    },
  );
}

function countDropped(count: number, reason: string): void {
  droppedCount += count;
  droppedReason = reason;
}

/**
 * A 2xx with no protocol header came from an endpoint written before batching.
 * It parsed `{entries:[...]}` as a v1 body, found no `msg`, and printed one
 * empty line — so this batch is lost unless it is re-sent one line at a time,
 * and so is everything after it.
 */
function adoptLegacyMode(lost: DevLogPayload[]): void {
  legacyEndpoint = true;
  const notice: DevLogPayload = {
    level: "warn",
    msg:
      "[forge-devlog] this dev server's /__forge/log endpoint predates batched " +
      "posts; update it (forge skill: debug) — falling back to one request per line",
  };
  buffer.unshift(notice, ...lost);
  bufferedBytes += [notice, ...lost].reduce((n, e) => n + entryBytes(e), 0);
}

/**
 * Last-chance flush on page teardown.
 *
 * fetch — even with keepalive — is not reliable here, and an async flush is
 * hopeless: the event loop may not run again. sendBeacon hands the bytes to the
 * browser to deliver after the document is gone, which is precisely the case
 * where the last few log lines explain what happened.
 */
function flushOnUnload(): void {
  drainDroppedNotice();
  if (buffer.length === 0) return;

  const canBeacon =
    typeof navigator !== "undefined" && typeof navigator.sendBeacon === "function";
  if (!canBeacon) {
    // No beacon: keepalive fetch is the only remaining option, and one request
    // has a better chance of leaving than a queue of them.
    flush();
    return;
  }

  forwarding = true;
  try {
    while (buffer.length > 0) {
      const batch = legacyEndpoint ? takeBatch(0) : takeBatch();
      const body = legacyEndpoint
        ? JSON.stringify(batch[0])
        : JSON.stringify({ entries: batch } satisfies DevLogBatch);
      // text/plain is not a decoration: sendBeacon with application/json is a
      // non-simple request and would need a CORS preflight it cannot make. The
      // endpoints parse the body regardless of Content-Type for this reason.
      const blob = new Blob([body], { type: "text/plain;charset=UTF-8" });
      if (!navigator.sendBeacon(endpointUrl, blob)) {
        // The beacon queue is full (64 KiB per origin). Nothing later will fit
        // either, so stop and account for what is left.
        countDropped(batch.length + buffer.length, "beacon queue full");
        buffer = [];
        bufferedBytes = 0;
        return;
      }
    }
  } finally {
    forwarding = false;
  }
}

function onWindowError(event: ErrorEvent): void {
  capture("error", [
    event.error ??
      `${event.message} @ ${event.filename}:${event.lineno}:${event.colno}`,
  ]);
}

function onRejection(event: PromiseRejectionEvent): void {
  capture("error", ["unhandled rejection:", event.reason]);
}

let errorHandler: ((e: ErrorEvent) => void) | undefined;
let rejectionHandler: ((e: PromiseRejectionEvent) => void) | undefined;
let pageHideHandler: (() => void) | undefined;
let visibilityHandler: (() => void) | undefined;

/**
 * Mirror console output and uncaught errors to the dev server, which prints
 * them into .forge/logs/<env>/frontend_<name>.log.
 *
 * The window listeners are the point of the feature as much as the console
 * override is: an uncaught TypeError or a rejected promise produces no console
 * call of its own, and those are exactly the failures worth seeing.
 *
 * Safe to call more than once; the second call is a no-op.
 */
export function installDevLogging(options: DevLoggingOptions): void {
  // `dev` is read off `options` rather than destructured with a default, and
  // checked FIRST. A bundler constant-folds `import.meta.env.DEV` to false at
  // the CALL SITE and can then drop the argument entirely, leaving a bare
  // `installDevLogging({})` — with a defaulted `dev` that call would install
  // the override in a production bundle. `options?.dev !== true` is therefore
  // fail-closed: absent, undefined, or anything non-true means do nothing.
  if (options?.dev !== true) return;
  if (installed) return;
  if (typeof window === "undefined") return; // SSR / prerender pass

  const { endpoint = DEV_LOG_ENDPOINT, mirrorToConsole = true } = options;
  installed = true;
  endpointUrl = endpoint;
  buffer = [];
  bufferedBytes = 0;
  droppedCount = 0;
  droppedReason = "";
  legacyEndpoint = false;
  requestInFlight = false;

  for (const level of LEVELS) {
    // Keep the ORIGINAL reference, not a bound copy: uninstall must restore
    // console.log to the identical function it replaced, so a second consumer
    // holding that reference (or a test asserting on it) sees no difference.
    const previous = console[level] as (...args: unknown[]) => void;
    original[level] = previous;
    console[level] = (...args: unknown[]) => {
      if (mirrorToConsole) previous.apply(console, args);
      capture(level, args);
    };
  }

  errorHandler = (e) => onWindowError(e);
  rejectionHandler = (e) => onRejection(e);
  pageHideHandler = () => flushOnUnload();
  window.addEventListener("error", errorHandler);
  window.addEventListener("unhandledrejection", rejectionHandler);
  window.addEventListener("pagehide", pageHideHandler);

  if (typeof document !== "undefined") {
    // visibilitychange→hidden is the only teardown signal mobile Safari
    // reliably fires; pagehide alone loses the whole buffer there.
    visibilityHandler = () => {
      if (document.visibilityState === "hidden") flushOnUnload();
    };
    document.addEventListener("visibilitychange", visibilityHandler);
  }
}

/** Restore the original console methods and remove the window listeners. */
export function uninstallDevLogging(): void {
  if (!installed) return;

  // Flush before restoring: whatever is buffered was logged while this was
  // installed, and a teardown is no reason to lose it.
  clearFlushTimer();
  flush();

  for (const level of LEVELS) {
    const previous = original[level];
    if (previous) console[level] = previous;
    delete original[level];
  }
  if (errorHandler) window.removeEventListener("error", errorHandler);
  if (rejectionHandler)
    window.removeEventListener("unhandledrejection", rejectionHandler);
  if (pageHideHandler) window.removeEventListener("pagehide", pageHideHandler);
  if (visibilityHandler && typeof document !== "undefined")
    document.removeEventListener("visibilitychange", visibilityHandler);
  errorHandler = undefined;
  rejectionHandler = undefined;
  pageHideHandler = undefined;
  visibilityHandler = undefined;
  installed = false;
}

/** Whether the console override is currently installed. Exposed for tests. */
export function devLoggingInstalled(): boolean {
  return installed;
}
