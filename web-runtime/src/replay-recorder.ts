// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The session-replay recorder: rrweb, owned by the runtime.
//
// ── Why the runtime owns it ──────────────────────────────────────────────
//
// The recorder bundled in @hyperdx/browser (@hyperdx/otel-web-session-recorder
// 4.0.0) did its export on the main thread: every 5 s it protobuf-encoded the
// batch with protobufjs and gzipped it with fflate, 50–560 ms per flush on a
// phone-class CPU, so taps and scrolls stalled every few seconds while a
// session was recorded. It flushed only on `unload`, which mobile browsers do
// not fire, and shipped rrweb in the SDK's one chunk whether replay was on or
// not. None of that was reachable through the SDK's options.
//
// So the runtime records itself, with the same wire shape HyperDX's replay
// player reads:
//
//   - rrweb is imported lazily, only when replay runs: bundlers put it in its
//     own chunk, and an app without replay never loads it.
//   - Records are batched (ReplayBatcher) and flushed every
//     REPLAY_FLUSH_INTERVAL_MS, within a second once REPLAY_TAIL_MAX_BYTES are
//     held, and at once when REPLAY_MAX_PENDING_BYTES are.
//   - A batch is encoded as OTLP/JSON (encodeReplayRequest: one native
//     JSON.stringify) and gzipped with CompressionStream, both in a Worker
//     (ReplayTransport). Where a Worker cannot run (a CSP without
//     `worker-src blob:`, an old browser), the gzip runs on the main thread in
//     slices that yield between them; where there is no CompressionStream, the
//     JSON goes uncompressed.
//   - When the page is hidden (`visibilitychange`) or left (`pagehide`), what
//     is held is sent at once, with fetch keepalive, so it outlives the page.
//   - With `start: "interaction"` nothing is loaded or recorded until the
//     person first interacts, and then only after that frame has painted.
//
// ── What HyperDX's replay player reads ───────────────────────────────────
//
// OTLP logs posted to `${ingest}/v1/logs`, scope `rum.rr-web` (version
// 4.0.0), each record's body the JSON of one rrweb event (split into
// MAX_CHUNK_CHARS chunks), with numeric attributes `rr-web.offset`,
// `rr-web.event`, `rr-web.chunk` and `rr-web.total-chunks`, and the SDK's
// resource, `rum.sessionId` included. All of it is kept exactly. So is what
// the bundled recorder did to stay correct: a full snapshot when the RUM
// session changes, a 4 h cap per session, and a per-node rate limit on
// attribute mutations (MutationRateLimiter) that a runaway animation would
// otherwise turn into megabytes.
//
// ── Withdrawal ───────────────────────────────────────────────────────────
//
// stop() is final: rrweb stops, what is held is dropped, a batch still being
// encoded is never sent, and no flush runs again (see "Withdrawal stops replay
// now" in telemetry.ts). A request already sent stays sent.

import type { Tracer } from "@opentelemetry/api";

/** The scope the recorder's records and spans carry: HyperDX's player keys on it. */
export const REPLAY_SCOPE = { name: "rum.rr-web", version: "4.0.0" } as const;

/** Where the recorder posts, under the ingest base. */
export const REPLAY_LOGS_PATH = "/v1/logs";

/** How often held records are sent. HyperDX's recorder sent every 5 s. */
export const REPLAY_FLUSH_INTERVAL_MS = 15_000;

/** Held record bytes (JSON chars) that send a batch before the interval. */
export const REPLAY_MAX_PENDING_BYTES = 512 * 1024;

/**
 * Held record bytes (JSON chars) that send a batch within
 * REPLAY_SNAPSHOT_FLUSH_MS rather than wait for the interval. What is held
 * when the page is left goes uncompressed, and only KEEPALIVE_MAX_BYTES of it
 * is sure to arrive; this keeps that tail to about a second's records, and
 * sends the rest gzipped, a fifth to a tenth of the bytes.
 */
export const REPLAY_TAIL_MAX_BYTES = 48 * 1024;

/**
 * How soon (ms) a full snapshot is sent once taken. It is most of a session's
 * bytes and the one record a replay cannot play without, so it goes gzipped in
 * the background straight away rather than wait for the interval and risk
 * leaving, uncompressed and best effort, with the page.
 */
export const REPLAY_SNAPSHOT_FLUSH_MS = 1_000;

/**
 * The largest record body; a longer event is split into chunks, which the
 * player joins by `rr-web.event` and `rr-web.chunk` / `rr-web.total-chunks`.
 * Small enough that any one record's request fits KEEPALIVE_MAX_BYTES, so
 * nothing held when the page leaves is too big to outlive it: a body
 * character encodes to at most 3 bytes (UTF-8; a JSON-escaped quote or
 * backslash is 2, and the body is itself JSON, so it holds no raw control
 * character), so a chunk is at most 48 KiB, plus at most two 6-byte
 * escapes for a surrogate pair split at its edges, plus the record's own
 * fields. HyperDX's recorder chunked at 950 KB; a large snapshot is now more
 * records, which only the uncompressed leaving path ever notices.
 */
export const MAX_CHUNK_CHARS = 16 * 1024;

/** The longest a session is recorded: 4 h and a minute, as HyperDX's recorder. */
export const MAX_RECORDING_MS = (4 * 60 + 1) * 60 * 1000;

/** A stringified event over this has its data: URIs replaced or removed. */
export const MAX_MESSAGE_CHARS = 5_000_000;

/**
 * The most a keepalive request may carry. Browsers cap all of a page's
 * in-flight keepalive bodies at 64 KiB together; this leaves headroom.
 */
export const KEEPALIVE_MAX_BYTES = 60 * 1024;

/** The longest a Worker may take to encode one batch before it is given up on. */
export const WORKER_TIMEOUT_MS = 10_000;

/** When recording starts. */
export type ReplayStart = "load" | "interaction";

/** One OTLP log record of the recorder. */
export interface ReplayRecord {
  /** The rrweb event's timestamp, ms since the epoch. */
  time: number;
  /** The event's JSON, or one chunk of it. */
  body: string;
  attributes: Record<string, number>;
}

// ── OTLP/JSON encoding ───────────────────────────────────────────────────

/**
 * The OTLP/JSON ExportLogsServiceRequest for `records`. Self-contained on
 * purpose: its source is also the Worker's (replayWorkerSource), so it may
 * reference nothing outside its own body. Values follow the bundled
 * recorder's mapping: strings, numbers as doubleValue, booleans, arrays and
 * objects; anything else is an empty AnyValue. timeUnixNano is a decimal
 * string (OTLP/JSON's 64-bit integer form), exact where ms·1e6 as a number is
 * not.
 */
export function encodeReplayRequest(
  resource: Record<string, unknown>,
  scope: { name: string; version: string },
  records: ReadonlyArray<{
    time: number;
    body: string;
    attributes: Record<string, unknown>;
  }>,
): string {
  const anyValue = (value: unknown): Record<string, unknown> => {
    if (typeof value === "string") {
      return { stringValue: value };
    }
    if (typeof value === "number") {
      return { doubleValue: value };
    }
    if (typeof value === "boolean") {
      return { boolValue: value };
    }
    if (Array.isArray(value)) {
      return { arrayValue: { values: value.map(anyValue) } };
    }
    if (value && typeof value === "object") {
      return { kvlistValue: { values: keyValues(value) } };
    }
    return {};
  };
  const keyValues = (o: object) =>
    Object.keys(o).map((key) => ({
      key,
      value: anyValue((o as Record<string, unknown>)[key]),
    }));
  const nanos = (ms: number) =>
    Number.isFinite(ms) && ms > 0 ? `${Math.round(ms)}000000` : "0";
  return JSON.stringify({
    resourceLogs: [
      {
        resource: { attributes: keyValues(resource) },
        scopeLogs: [
          {
            scope: { name: scope.name, version: scope.version },
            logRecords: records.map((r) => ({
              timeUnixNano: nanos(r.time),
              body: { stringValue: r.body },
              attributes: keyValues(r.attributes),
            })),
          },
        ],
      },
    ],
  });
}

/**
 * The Worker's whole program: it encodes a batch with encodeReplayRequest and
 * gzips it with CompressionStream, and hands the bytes back (transferred, not
 * copied). Without CompressionStream it hands back the JSON. Exported for
 * tests.
 */
export function replayWorkerSource(): string {
  return `"use strict";
const encodeReplayRequest = ${encodeReplayRequest.toString()};
self.onmessage = async (e) => {
  const { id, resource, scope, records } = e.data;
  try {
    const json = encodeReplayRequest(resource, scope, records);
    if (typeof CompressionStream !== "function") {
      self.postMessage({ id, json });
      return;
    }
    const stream = new Blob([json]).stream().pipeThrough(new CompressionStream("gzip"));
    const body = await new Response(stream).arrayBuffer();
    self.postMessage({ id, body }, [body]);
  } catch (err) {
    self.postMessage({ id, error: String(err) });
  }
};`;
}

// ── Batching ─────────────────────────────────────────────────────────────

/**
 * How a batch leaves. "background": encoded and gzipped off the main thread.
 * "hidden": the same, sent with keepalive, because the page was just hidden
 * and may not come back (a phone backgrounding the app). "leaving": encoded
 * now and sent uncompressed with keepalive, because the page is being
 * unloaded and nothing asynchronous will finish.
 */
export type FlushMode = "background" | "hidden" | "leaving";

export interface ReplayBatcherOptions {
  flushIntervalMs?: number;
  maxPendingBytes?: number;
  tailMaxBytes?: number;
  send(records: ReplayRecord[], mode: FlushMode): void;
}

/**
 * Holds records and hands them to `send` in batches: REPLAY_FLUSH_INTERVAL_MS
 * after the first record held since the last batch, within
 * REPLAY_SNAPSHOT_FLUSH_MS once REPLAY_TAIL_MAX_BYTES are held, at once when
 * REPLAY_MAX_PENDING_BYTES are held, and on flush().
 */
export class ReplayBatcher {
  private pending: ReplayRecord[] = [];
  private bytes = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private readonly interval: number;
  private readonly maxBytes: number;
  private readonly tailBytes: number;

  constructor(private readonly options: ReplayBatcherOptions) {
    this.interval = options.flushIntervalMs ?? REPLAY_FLUSH_INTERVAL_MS;
    this.maxBytes = options.maxPendingBytes ?? REPLAY_MAX_PENDING_BYTES;
    this.tailBytes = options.tailMaxBytes ?? REPLAY_TAIL_MAX_BYTES;
  }

  /** Records held, not yet sent. */
  get size(): number {
    return this.pending.length;
  }

  add(record: ReplayRecord): void {
    this.pending.push(record);
    this.bytes += record.body.length;
    if (this.bytes >= this.maxBytes) {
      this.flush("background");
    } else if (this.bytes >= this.tailBytes) {
      this.flushWithin(REPLAY_SNAPSHOT_FLUSH_MS);
    } else if (this.timer === undefined) {
      this.schedule(this.interval);
    }
  }

  private due = 0;

  private schedule(ms: number): void {
    this.due = Date.now() + ms;
    this.timer = setTimeout(() => {
      this.timer = undefined;
      this.flush("background");
    }, ms);
  }

  /** Sends what is held within `ms`, sooner than the interval would. */
  flushWithin(ms: number): void {
    if (this.pending.length === 0) {
      return;
    }
    if (this.timer !== undefined && this.due <= Date.now() + ms) {
      return;
    }
    this.clearTimer();
    this.schedule(ms);
  }

  flush(mode: FlushMode): void {
    this.clearTimer();
    if (this.pending.length === 0) {
      return;
    }
    const batch = this.pending;
    this.pending = [];
    this.bytes = 0;
    this.options.send(batch, mode);
  }

  /** Drops everything held and stops the timer. */
  drop(): void {
    this.clearTimer();
    this.pending = [];
    this.bytes = 0;
  }

  private clearTimer(): void {
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
  }
}

// ── Transport ────────────────────────────────────────────────────────────

type FetchFn = (input: string, init: RequestInit) => Promise<unknown>;

/** A Worker as the transport uses it. */
export interface WorkerLike {
  postMessage(message: unknown): void;
  addEventListener(type: string, listener: (event: unknown) => void): void;
  terminate(): void;
}

export interface ReplayTransportOptions {
  /** `${ingest}/v1/logs`. */
  url: string;
  /** Sent as `authorization`, as the SDK did; the ingest proxy owns the real one. */
  apiKey?: string;
  /** The resource to stamp on a batch, read when the batch is sent. */
  resource(): Record<string, unknown>;
  /** Defaults to the global fetch. */
  fetch?: FetchFn;
  /** Starts the encoding Worker, or null where there is none. Defaults to a Blob-URL Worker. */
  createWorker?: () => WorkerLike | null;
  /** Ends the current task. Defaults to yieldToMain-style scheduling. */
  yieldToMain?: () => Promise<void>;
}

type Encoded = { body: BodyInit; gzip: boolean; bytes: number };

/**
 * Sends batches of records to the logs URL. In the background the batch is
 * encoded and gzipped off the main thread (see the header); when the page is
 * leaving it is encoded now, uncompressed, and sent with keepalive in requests
 * of at most KEEPALIVE_MAX_BYTES, so it outlives the page.
 */
export class ReplayTransport {
  private worker: WorkerLike | null | undefined;
  private workerBroken = false;
  private nextId = 1;
  private readonly jobs = new Map<
    number,
    {
      resolve(v: Encoded): void;
      reject(e: unknown): void;
      timer: ReturnType<typeof setTimeout>;
    }
  >();
  private closed = false;
  // Set once the page is being unloaded (a "leaving" send): see post().
  private unloading = false;
  // Batches encoding in the background: taken over by flushInFlight() when
  // the page is unloaded before they finish.
  private readonly inFlight = new Set<{
    records: ReplayRecord[];
    taken: boolean;
  }>();
  private readonly doFetch: FetchFn;

  constructor(private readonly options: ReplayTransportOptions) {
    this.doFetch =
      options.fetch ??
      ((input, init) =>
        (globalThis.fetch as unknown as FetchFn).call(globalThis, input, init));
  }

  send(records: ReplayRecord[], mode: FlushMode): void {
    if (this.closed || records.length === 0) {
      return;
    }
    if (mode === "leaving") {
      this.unloading = true;
      this.sendNow(records);
      return;
    }
    void this.sendInBackground(records);
  }

  /** The page came back from the back-forward cache: it lives again. */
  restored(): void {
    this.unloading = false;
  }

  /**
   * The page is being unloaded: every batch still being encoded is sent now,
   * uncompressed, with keepalive, and its background result is dropped, so
   * each batch is sent once.
   */
  flushInFlight(): void {
    this.unloading = true;
    for (const batch of this.inFlight) {
      if (!batch.taken) {
        batch.taken = true;
        this.sendNow(batch.records);
      }
    }
    this.inFlight.clear();
  }

  /** Nothing is sent from here on, and nothing still encoding is. */
  close(): void {
    this.closed = true;
    this.inFlight.clear();
    for (const [, job] of this.jobs) {
      clearTimeout(job.timer);
      job.reject(new Error("closed"));
    }
    this.jobs.clear();
    try {
      this.worker?.terminate();
    } catch {
      // Telemetry must never break the app.
    }
    this.worker = null;
  }

  private sendNow(records: ReplayRecord[]): void {
    const resource = this.options.resource();
    // Requests of at most KEEPALIVE_MAX_BYTES, while the keepalive budget
    // lasts; whatever does not fit goes without keepalive (best effort: it
    // arrives when the page is only hidden, not when it is gone). A group is
    // sized by the request it encodes to, exactly: the empty request's UTF-8
    // bytes, plus each record's own encoded bytes (its body JSON-escaped, as
    // UTF-8) and a comma between them, since logRecords is one JSON array.
    // A body's length in characters is not that: escaping and multi-byte
    // text can double it. Every record fits a request of its own
    // (MAX_CHUNK_CHARS), so every request is within the cap; only what is
    // past the page's whole keepalive budget goes without it.
    const empty = utf8Length(encodeReplayRequest(resource, REPLAY_SCOPE, []));
    let budget = KEEPALIVE_MAX_BYTES;
    let group: ReplayRecord[] = [];
    let groupBytes = empty;
    const post = () => {
      if (group.length === 0) {
        return;
      }
      const json = encodeReplayRequest(resource, REPLAY_SCOPE, group);
      const bytes = utf8Length(json);
      const keepalive = bytes <= budget;
      if (keepalive) {
        budget -= bytes;
      }
      this.post({ body: json, gzip: false, bytes }, keepalive);
      group = [];
      groupBytes = empty;
    };
    for (const record of records) {
      const size =
        utf8Length(encodeReplayRequest(resource, REPLAY_SCOPE, [record])) -
        empty;
      if (group.length > 0 && groupBytes + 1 + size > KEEPALIVE_MAX_BYTES) {
        post();
      }
      groupBytes += (group.length > 0 ? 1 : 0) + size;
      group.push(record);
    }
    post();
  }

  private async sendInBackground(records: ReplayRecord[]): Promise<void> {
    const batch = { records, taken: false };
    this.inFlight.add(batch);
    const resource = this.options.resource();
    let encoded: Encoded;
    try {
      encoded = await this.encodeInWorker(resource, records);
    } catch {
      if (this.closed) {
        return;
      }
      encoded = await this.encodeOnMain(resource, records);
    }
    this.inFlight.delete(batch);
    if (this.closed || batch.taken) {
      // Withdrawn while it was encoding: it never leaves. Taken over by
      // flushInFlight(): it already left.
      return;
    }
    this.post(encoded, encoded.bytes <= KEEPALIVE_MAX_BYTES);
  }

  private post(encoded: Encoded, keepalive: boolean): void {
    const headers: Record<string, string> = {
      "content-type": "application/json",
    };
    if (encoded.gzip) {
      headers["content-encoding"] = "gzip";
    }
    if (this.options.apiKey) {
      headers.authorization = this.options.apiKey;
    }
    const init: RequestInit = {
      method: "POST",
      headers,
      body: encoded.body,
      keepalive,
    };
    const sentUnloading = this.unloading;
    try {
      this.doFetch(this.options.url, init).catch(() => {
        // Over the keepalive budget the browser refuses the request: retry
        // without it, which arrives unless the page is already gone. Never
        // for a request sent or rejected while the page unloads: the browser
        // then rejects a keepalive request it DID send (the document is
        // going), and a retry would deliver the batch twice.
        if (keepalive && !this.closed && !sentUnloading && !this.unloading) {
          this.doFetch(this.options.url, { ...init, keepalive: false }).catch(
            () => {},
          );
        }
      });
    } catch {
      // Telemetry must never break the app.
    }
  }

  private encodeInWorker(
    resource: Record<string, unknown>,
    records: ReplayRecord[],
  ): Promise<Encoded> {
    const worker = this.ensureWorker();
    if (!worker) {
      return Promise.reject(new Error("no worker"));
    }
    const id = this.nextId++;
    return new Promise<Encoded>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.jobs.delete(id);
        this.breakWorker();
        reject(new Error("worker timed out"));
      }, WORKER_TIMEOUT_MS);
      this.jobs.set(id, { resolve, reject, timer });
      try {
        worker.postMessage({ id, resource, scope: REPLAY_SCOPE, records });
      } catch (err) {
        clearTimeout(timer);
        this.jobs.delete(id);
        this.breakWorker();
        reject(err);
      }
    });
  }

  private ensureWorker(): WorkerLike | null {
    if (this.workerBroken || this.closed) {
      return null;
    }
    if (this.worker !== undefined) {
      return this.worker;
    }
    try {
      this.worker = (this.options.createWorker ?? createBlobWorker)();
    } catch {
      this.worker = null;
    }
    const worker = this.worker;
    if (!worker) {
      this.workerBroken = true;
      return null;
    }
    worker.addEventListener("message", (event) => {
      const data = (event as { data?: Record<string, unknown> }).data ?? {};
      const job = this.jobs.get(data.id as number);
      if (!job) {
        return;
      }
      this.jobs.delete(data.id as number);
      clearTimeout(job.timer);
      if (data.body instanceof ArrayBuffer) {
        job.resolve({
          body: data.body,
          gzip: true,
          bytes: data.body.byteLength,
        });
      } else if (typeof data.json === "string") {
        job.resolve({
          body: data.json,
          gzip: false,
          bytes: utf8Length(data.json),
        });
      } else {
        job.reject(new Error(String(data.error ?? "worker failed")));
      }
    });
    // A Worker the page's CSP refuses, or one that fails to load, reports an
    // error event: everything from then on is encoded on the main thread.
    worker.addEventListener("error", () => this.breakWorker());
    return worker;
  }

  private breakWorker(): void {
    this.workerBroken = true;
    for (const [, job] of this.jobs) {
      clearTimeout(job.timer);
      job.reject(new Error("worker failed"));
    }
    this.jobs.clear();
    try {
      this.worker?.terminate();
    } catch {
      // Telemetry must never break the app.
    }
    this.worker = null;
  }

  /**
   * The main-thread fallback: one JSON.stringify, then gzip through
   * CompressionStream in slices, a task each, so no single task holds the
   * whole batch's compression. Without CompressionStream, the JSON.
   */
  private async encodeOnMain(
    resource: Record<string, unknown>,
    records: ReplayRecord[],
  ): Promise<Encoded> {
    const json = encodeReplayRequest(resource, REPLAY_SCOPE, records);
    if (typeof CompressionStream !== "function") {
      return { body: json, gzip: false, bytes: utf8Length(json) };
    }
    try {
      const yieldNow = this.options.yieldToMain ?? defaultYield;
      const bytes = new TextEncoder().encode(json);
      const stream = new CompressionStream("gzip");
      const out = new Response(stream.readable).arrayBuffer();
      const writer = stream.writable.getWriter();
      const SLICE = 64 * 1024;
      for (let at = 0; at < bytes.length; at += SLICE) {
        await writer.write(bytes.subarray(at, at + SLICE));
        await yieldNow();
      }
      await writer.close();
      const body = await out;
      return { body, gzip: true, bytes: body.byteLength };
    } catch {
      return { body: json, gzip: false, bytes: utf8Length(json) };
    }
  }
}

function createBlobWorker(): WorkerLike | null {
  if (
    typeof Worker !== "function" ||
    typeof Blob !== "function" ||
    typeof URL === "undefined" ||
    typeof URL.createObjectURL !== "function"
  ) {
    return null;
  }
  const url = URL.createObjectURL(
    new Blob([replayWorkerSource()], { type: "text/javascript" }),
  );
  try {
    return new Worker(url) as unknown as WorkerLike;
  } finally {
    // The Worker has its script once constructed; the URL can go.
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }
}

function defaultYield(): Promise<void> {
  const scheduler = (
    globalThis as { scheduler?: { yield?: () => Promise<void> } }
  ).scheduler;
  if (typeof scheduler?.yield === "function") {
    return scheduler.yield().catch(() => {});
  }
  return new Promise((resolve) => setTimeout(resolve, 0));
}

function utf8Length(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x80) {
      n += 1;
    } else if (c < 0x800) {
      n += 2;
    } else if (c >= 0xd800 && c <= 0xdbff) {
      n += 4;
      i++;
    } else {
      n += 3;
    }
  }
  return n;
}

// ── Mutation rate limiting ───────────────────────────────────────────────

/** The slice of rrweb's mirror the rate limiter reads. */
export interface MirrorLike {
  getNode(id: number): Node | null;
  getId(node: Node): number;
}

interface RrwebEvent {
  type: number;
  timestamp: number;
  data?: unknown;
}

interface MutationData {
  source: number;
  adds?: unknown[];
  removes?: unknown[];
  texts?: unknown[];
  attributes?: Array<{ id: number }>;
}

const FULL_SNAPSHOT = 2;
const INCREMENTAL_SNAPSHOT = 3;
const META = 4;
const MUTATION_SOURCE = 0;

/**
 * The longest a new session's events wait for its full snapshot before they
 * are recorded anyway (rrweb could not take one).
 */
export const SNAPSHOT_WAIT_MS = 1_000;

/**
 * Drops attribute mutations of a node (or the svg it is in) that changes
 * more than `bucketSize` times without `refillRate` per second of rest,
 * as HyperDX's recorder (after PostHog's) does. An event left with no
 * change is dropped whole. `onBlockedNode` is told once per node.
 */
export class MutationRateLimiter {
  private readonly buckets: Record<number, number> = {};
  private readonly logged: Record<number, boolean> = {};
  private readonly bucketSize: number;
  private readonly refillRate: number;
  private interval: ReturnType<typeof setInterval> | undefined;

  constructor(
    private readonly mirror: MirrorLike,
    private readonly options: {
      bucketSize?: number;
      refillRate?: number;
      onBlockedNode?: (id: number, node: Node | null) => void;
    } = {},
  ) {
    this.bucketSize = options.bucketSize ?? 100;
    this.refillRate = options.refillRate ?? 10;
    this.interval = setInterval(() => this.refill(), 1000);
  }

  stop(): void {
    if (this.interval !== undefined) {
      clearInterval(this.interval);
      this.interval = undefined;
    }
  }

  /** The event to record: as given, with blocked mutations cut, or undefined. */
  throttle<E extends RrwebEvent>(event: E): E | undefined {
    const data = event.data as MutationData | undefined;
    if (
      event.type !== INCREMENTAL_SNAPSHOT ||
      data?.source !== MUTATION_SOURCE
    ) {
      return event;
    }
    const before = changes(data);
    if (data.attributes) {
      data.attributes = data.attributes.filter((attr) => {
        const [id, node] = this.nodeOrSvg(attr.id);
        if (this.buckets[id] === 0) {
          return false;
        }
        this.buckets[id] = Math.max(
          (this.buckets[id] ?? this.bucketSize) - 1,
          0,
        );
        if (this.buckets[id] === 0 && !this.logged[id]) {
          this.logged[id] = true;
          this.options.onBlockedNode?.(id, node);
        }
        return true;
      });
    }
    const after = changes(data);
    return after === 0 && before !== after ? undefined : event;
  }

  private refill(): void {
    for (const key of Object.keys(this.buckets)) {
      const id = Number(key);
      this.buckets[id] = (this.buckets[id] ?? 0) + this.refillRate;
      if ((this.buckets[id] ?? 0) >= this.bucketSize) {
        delete this.buckets[id];
      }
    }
  }

  private nodeOrSvg(id: number): [number, Node | null] {
    const node = this.mirror.getNode(id);
    if (
      node &&
      node.nodeName !== "svg" &&
      typeof (node as Element).closest === "function"
    ) {
      const svg = (node as Element).closest("svg");
      if (svg) {
        return [this.mirror.getId(svg), svg];
      }
    }
    return [id, node];
  }
}

function changes(data: MutationData): number {
  return (
    (data.removes?.length ?? 0) +
    (data.attributes?.length ?? 0) +
    (data.texts?.length ?? 0) +
    (data.adds?.length ?? 0)
  );
}

// ── The recorder ─────────────────────────────────────────────────────────

/** The slice of rrweb's record() the recorder uses. */
export interface RrwebRecord {
  (options: Record<string, unknown>): (() => void) | undefined;
  takeFullSnapshot(isCheckout?: boolean): void;
  mirror: MirrorLike;
}

export interface ReplayRecorderOptions {
  /** `${ingest}/v1/logs`. */
  url: string;
  apiKey?: string;
  /** rrweb record() options: masking, blocking, sampling, slimDOM, plugins. */
  rrweb: Record<string, unknown>;
  /** The SDK's resource attributes, read when a batch is sent. */
  resource(): Record<string, unknown>;
  /** The RUM session id now. A change takes a full snapshot. */
  sessionId(): string | undefined;
  /** The `rum.rr-web` tracer, for `record init` and rate-limit warnings. */
  tracer?: Tracer;
  start?: ReplayStart;
  flushIntervalMs?: number;
  debug?: boolean;
  /** Test seams. */
  loadRecord?: () => Promise<RrwebRecord>;
  transport?: Pick<ReplayTransport, "send" | "close"> &
    Partial<Pick<ReplayTransport, "flushInFlight" | "restored">>;
  now?: () => number;
}

/** A recorder initBrowserTelemetry starts, and stops for good on withdrawal. */
export interface ReplayRecorder {
  /** Resolves once rrweb is recording, or never started (false). */
  readonly started: Promise<boolean>;
  /** Sends what is held now, as when the page is hidden. */
  flush(): void;
  /** Final: see "Withdrawal" in the header. */
  stop(): void;
}

const INTERACTION_EVENTS = ["pointerdown", "keydown", "touchstart"] as const;

/**
 * Starts the recorder: now (`start: "load"`) or at the first interaction.
 * Never throws; a recorder that cannot start resolves `started` to false.
 */
export function startReplayRecorder(
  options: ReplayRecorderOptions,
): ReplayRecorder {
  const now = options.now ?? Date.now;
  const transport =
    options.transport ??
    new ReplayTransport({
      url: options.url,
      apiKey: options.apiKey,
      resource: options.resource,
    });
  const batcher = new ReplayBatcher({
    flushIntervalMs: options.flushIntervalMs,
    send: (records, mode) => transport.send(records, mode),
  });
  let stopped = false;
  let stopRecording: (() => void) | undefined;
  let limiter: MutationRateLimiter | undefined;
  let snapshotTimer: ReturnType<typeof setTimeout> | undefined;
  const undo: Array<() => void> = [];

  // Unloaded: what is held, and what is still encoding, goes now.
  const leave = () => {
    if (!stopped) {
      batcher.flush("leaving");
      transport.flushInFlight?.();
    }
  };
  // Hidden: what is held goes, gzipped, with keepalive. If the page is being
  // unloaded, pagehide follows and takes over whatever has not finished.
  const onVisibility = () => {
    if (
      !stopped &&
      typeof document !== "undefined" &&
      document.visibilityState === "hidden"
    ) {
      batcher.flush("hidden");
    }
  };
  if (
    typeof document !== "undefined" &&
    typeof document.addEventListener === "function"
  ) {
    document.addEventListener("visibilitychange", onVisibility);
    undo.push(() =>
      document.removeEventListener?.("visibilitychange", onVisibility),
    );
  }
  if (
    typeof window !== "undefined" &&
    typeof window.addEventListener === "function"
  ) {
    window.addEventListener("pagehide", leave);
    undo.push(() => window.removeEventListener?.("pagehide", leave));
    // Back from the back-forward cache: the page was not unloaded after all.
    const back = (e: Event) => {
      if ((e as PageTransitionEvent).persisted) transport.restored?.();
    };
    window.addEventListener("pageshow", back);
    undo.push(() => window.removeEventListener?.("pageshow", back));
  }

  const begin = async (): Promise<boolean> => {
    let record: RrwebRecord;
    try {
      record = await (options.loadRecord ?? loadRrwebRecord)();
    } catch (err) {
      if (options.debug) {
        console.debug("[telemetry] replay recorder did not load:", err);
      }
      return false;
    }
    if (stopped) {
      return false;
    }
    const tracer = options.tracer;
    tracer?.startSpan("record init").end();
    let lastSession = options.sessionId();
    let sessionStart = now();
    let eventCounter = 1;
    let logCounter = 1;
    limiter = new MutationRateLimiter(record.mirror, {
      onBlockedNode: (id, node) => {
        const at = now();
        const span = tracer?.startSpan("console.warn", { startTime: at });
        span?.setAttribute("component", "console");
        span?.setAttribute("level", "warn");
        span?.setAttribute(
          "message",
          `OpenTelemetry Session Recorder: Rate limiting recording mutations on node ${id}. This can be due to excessive animations.`,
        );
        span?.end(at);
        if (options.debug) {
          console.warn("[telemetry] replay rate-limited node", id, node);
        }
        clearTimeout(snapshotTimer);
        snapshotTimer = setTimeout(() => {
          if (!stopped) {
            record.takeFullSnapshot();
          }
        }, 1000);
      },
    });
    // A new session's events wait here until its full snapshot is recorded:
    // a replay cannot play an incremental event before its baseline. rrweb
    // 2.x emits the snapshot synchronously, back through emit, so this holds
    // nothing for longer than takeFullSnapshot() runs; the order does not
    // depend on that, and a snapshot that never comes (rrweb could not take
    // one) releases what waits after SNAPSHOT_WAIT_MS rather than hold it.
    let held: RrwebEvent[] | null = null;
    let heldTimer: ReturnType<typeof setTimeout> | undefined;
    const release = () => {
      clearTimeout(heldTimer);
      const waiting = held ?? [];
      held = null;
      for (const event of waiting) {
        add(event);
      }
    };
    const emit = (source: RrwebEvent) => {
      if (stopped) {
        return;
      }
      try {
        const session = options.sessionId();
        if (session !== lastSession) {
          if (typeof document !== "undefined" && document.hidden) {
            return;
          }
          lastSession = session;
          sessionStart = now();
          eventCounter = 1;
          logCounter = 1;
          held = [source];
          clearTimeout(heldTimer);
          heldTimer = setTimeout(release, SNAPSHOT_WAIT_MS);
          try {
            record.takeFullSnapshot();
          } catch {
            release();
          }
          return;
        }
        if (held && source.type !== META && source.type !== FULL_SNAPSHOT) {
          held.push(source);
          return;
        }
        add(source);
        if (held && source.type === FULL_SNAPSHOT) {
          release();
        }
      } catch {
        // Telemetry must never break the app.
      }
    };
    const add = (source: RrwebEvent) => {
      try {
        if (stopped) {
          return;
        }
        if (source.timestamp > sessionStart + MAX_RECORDING_MS) {
          return;
        }
        const event = limiter?.throttle(source);
        if (!event) {
          return;
        }
        const eventIndex = eventCounter++;
        const json = limitMessageSize(JSON.stringify(event));
        const total = Math.max(1, Math.ceil(json.length / MAX_CHUNK_CHARS));
        for (let i = 0; i < total; i++) {
          batcher.add({
            time: event.timestamp,
            body: json.slice(i * MAX_CHUNK_CHARS, (i + 1) * MAX_CHUNK_CHARS),
            attributes: {
              "rr-web.offset": logCounter++,
              "rr-web.event": eventIndex,
              "rr-web.chunk": i + 1,
              "rr-web.total-chunks": total,
            },
          });
        }
        if (event.type === FULL_SNAPSHOT) {
          batcher.flushWithin(REPLAY_SNAPSHOT_FLUSH_MS);
        }
      } catch {
        // Telemetry must never break the app.
      }
    };
    try {
      stopRecording =
        record({
          maskAllInputs: true,
          maskTextSelector: "*",
          ...options.rrweb,
          emit,
        }) ?? undefined;
    } catch (err) {
      if (options.debug) {
        console.debug("[telemetry] replay recorder did not start:", err);
      }
      limiter.stop();
      return false;
    }
    if (stopped) {
      stopRecording?.();
      return false;
    }
    return true;
  };

  let started: Promise<boolean>;
  if (options.start === "interaction") {
    started = new Promise<boolean>((resolve) => {
      if (
        typeof window === "undefined" ||
        typeof window.addEventListener !== "function"
      ) {
        resolve(false);
        return;
      }
      const listenOptions = { capture: true, passive: true } as const;
      const onInteraction = () => {
        for (const type of INTERACTION_EVENTS) {
          window.removeEventListener?.(type, onInteraction, listenOptions);
        }
        // After the interaction's frame has painted: its response comes
        // first, the recorder's snapshot after.
        afterNextPaint(() => {
          if (stopped) {
            resolve(false);
            return;
          }
          void begin().then(resolve);
        });
      };
      for (const type of INTERACTION_EVENTS) {
        window.addEventListener(type, onInteraction, listenOptions);
      }
      undo.push(() => {
        for (const type of INTERACTION_EVENTS) {
          window.removeEventListener?.(type, onInteraction, listenOptions);
        }
        resolve(false);
      });
    });
  } else {
    started = begin();
  }

  return {
    started,
    flush: leave,
    stop() {
      if (stopped) {
        return;
      }
      stopped = true;
      batcher.drop();
      transport.close();
      clearTimeout(snapshotTimer);
      limiter?.stop();
      try {
        stopRecording?.();
      } catch {
        // Telemetry must never break the app.
      }
      for (const fn of undo.splice(0)) {
        try {
          fn();
        } catch {
          // Telemetry must never break the app.
        }
      }
    },
  };
}

/** rrweb's record(), in its own chunk: loaded only when replay runs. */
async function loadRrwebRecord(): Promise<RrwebRecord> {
  const mod = (await import("rrweb")) as unknown as {
    record?: RrwebRecord;
    default?: { record?: RrwebRecord };
  };
  const record = mod.record ?? mod.default?.record;
  if (typeof record !== "function") {
    throw new Error("rrweb has no record()");
  }
  return record;
}

function afterNextPaint(fn: () => void): void {
  let done = false;
  const once = () => {
    if (!done) {
      done = true;
      fn();
    }
  };
  if (typeof requestAnimationFrame === "function") {
    requestAnimationFrame(() => setTimeout(once, 0));
  }
  // A hidden page paints no frames.
  setTimeout(once, 250);
}

const DATA_URI = /data:([\w/\-.]+);(\w+),([^)"]*)/gim;
const REPLACEMENT_IMAGE =
  "data:image/svg+xml;base64,PHN2ZyB3aWR0aD0iMTYiIGhlaWdodD0iMTYiIHZpZXdCb3g9IjAgMCAxNiAxNiIgZmlsbD0ibm9uZSIgeG1sbnM9Imh0dHA6Ly93d3cudzMub3JnLzIwMDAvc3ZnIj4KPHJlY3Qgd2lkdGg9IjE2IiBoZWlnaHQ9IjE2IiBmaWxsPSJibGFjayIvPgo8cGF0aCBkPSJNOCAwSDE2TDAgMTZWOEw4IDBaIiBmaWxsPSIjMkQyRDJEIi8+CjxwYXRoIGQ9Ik0xNiA4VjE2SDhMMTYgOFoiIGZpbGw9IiMyRDJEMkQiLz4KPC9zdmc+Cg==";

/**
 * An event JSON over MAX_MESSAGE_CHARS with its data: URIs replaced (images,
 * by a placeholder) or removed, as HyperDX's recorder (after PostHog's) did.
 */
export function limitMessageSize(json: string): string {
  if (json.length <= MAX_MESSAGE_CHARS) {
    return json;
  }
  return json.replace(DATA_URI, (match, type: string) =>
    type.toLowerCase().startsWith("image/") ? REPLACEMENT_IMAGE : "",
  );
}
