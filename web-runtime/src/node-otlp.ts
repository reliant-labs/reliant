// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// OTLP/HTTP with JSON bodies, for the `/node` entry: the encoding, the
// exporter and the batching queue. Node built-ins only (global fetch,
// timers), so it runs unchanged under Electron's main process.
//
// ── Why not the OpenTelemetry JS SDK ─────────────────────────────────────
//
// The SDK would be six more peer packages (sdk-logs, sdk-trace, resources,
// two exporters, context-async-hooks) whose major lines move together and
// fast — 3.0 landed with a new package layout and a hard @opentelemetry/api
// 1.10 floor. Every consumer of this package would have to match that set
// exactly. What `/node` needs from it is small and stable: the OTLP/JSON wire
// format (OTLP 1.x, frozen), a batch, a flush. So it is written here, against
// the wire format, and the only OpenTelemetry package involved is the
// @opentelemetry/api this runtime already peers on.
//
// ── The wire format ──────────────────────────────────────────────────────
//
// OTLP/JSON differs from the protobuf JSON mapping in two places that matter:
// traceId and spanId are lowercase HEX (not base64), and enum fields are
// integers. 64-bit integers (timestamps, intValue) are decimal strings.

/** An attribute value a record or span may carry. */
export type AttributeValue =
  | string
  | number
  | boolean
  | ReadonlyArray<string>
  | ReadonlyArray<number>
  | ReadonlyArray<boolean>;
export type Attributes = Record<string, AttributeValue | undefined>;

/** Most attributes on one record or span; the rest are dropped. */
export const MAX_ATTRIBUTES = 128;
/** Longest string attribute value. */
export const MAX_ATTRIBUTE_CHARS = 4096;

interface AnyValue {
  stringValue?: string;
  boolValue?: boolean;
  intValue?: string;
  doubleValue?: number;
  arrayValue?: { values: AnyValue[] };
}

export interface KeyValue {
  key: string;
  value: AnyValue;
}

function scalar(v: unknown): AnyValue | null {
  switch (typeof v) {
    case "string":
      return {
        stringValue:
          v.length > MAX_ATTRIBUTE_CHARS ? v.slice(0, MAX_ATTRIBUTE_CHARS) : v,
      };
    case "boolean":
      return { boolValue: v };
    case "number":
      if (!Number.isFinite(v)) {
        return null;
      }
      return Number.isSafeInteger(v)
        ? { intValue: String(v) }
        : { doubleValue: v };
    default:
      return null;
  }
}

/** OTLP KeyValues for `attrs`: bounded, unencodable values dropped. */
export function encodeAttributes(attrs: Attributes | undefined): KeyValue[] {
  const out: KeyValue[] = [];
  if (!attrs) {
    return out;
  }
  for (const [key, v] of Object.entries(attrs)) {
    if (out.length >= MAX_ATTRIBUTES) {
      break;
    }
    if (v === undefined) {
      continue;
    }
    if (Array.isArray(v)) {
      const values = v.map(scalar).filter((x): x is AnyValue => x !== null);
      out.push({ key, value: { arrayValue: { values } } });
      continue;
    }
    const value = scalar(v);
    if (value) {
      out.push({ key, value });
    }
  }
  return out;
}

/** Nanoseconds since the epoch for `ms` (fractional ms allowed), as OTLP's decimal string. */
export function unixNanos(ms: number): string {
  const whole = Math.floor(ms);
  const frac = Math.round((ms - whole) * 1e6);
  return (BigInt(whole) * 1_000_000n + BigInt(frac)).toString();
}

/** Now, in epoch milliseconds with sub-millisecond precision. */
export function nowMs(): number {
  return performance.timeOrigin + performance.now();
}

/** OTel severity numbers (logs data model). */
export const SEVERITY = {
  DEBUG: 5,
  INFO: 9,
  WARN: 13,
  ERROR: 17,
} as const;
export type SeverityText = keyof typeof SEVERITY;

/** One log record, as OTLP/JSON. */
export interface LogRecord {
  timeUnixNano: string;
  observedTimeUnixNano: string;
  severityNumber: number;
  severityText: SeverityText;
  body: { stringValue: string };
  attributes: KeyValue[];
  traceId?: string;
  spanId?: string;
  flags?: number;
}

/** One finished span, as OTLP/JSON. */
export interface SpanRecord {
  traceId: string;
  spanId: string;
  parentSpanId?: string;
  name: string;
  kind: number;
  startTimeUnixNano: string;
  endTimeUnixNano: string;
  attributes: KeyValue[];
  droppedAttributesCount?: number;
  events: Array<{ timeUnixNano: string; name: string; attributes: KeyValue[] }>;
  links: Array<{ traceId: string; spanId: string; attributes: KeyValue[] }>;
  status: { code: number; message?: string };
  /** Grouping key for scopeSpans; not on the wire. */
  scope: Scope;
}

export interface Scope {
  name: string;
  version?: string;
}

/** The ExportLogsServiceRequest body for one batch. */
export function logsPayload(
  resource: KeyValue[],
  scope: Scope,
  records: LogRecord[],
) {
  return {
    resourceLogs: [
      {
        resource: { attributes: resource },
        scopeLogs: [{ scope, logRecords: records }],
      },
    ],
  };
}

/** The ExportTraceServiceRequest body for one batch, spans grouped by scope. */
export function tracesPayload(resource: KeyValue[], spans: SpanRecord[]) {
  const byScope = new Map<
    string,
    { scope: Scope; spans: Omit<SpanRecord, "scope">[] }
  >();
  for (const { scope, ...span } of spans) {
    const key = `${scope.name}\u001f${scope.version ?? ""}`;
    let group = byScope.get(key);
    if (!group) {
      group = { scope, spans: [] };
      byScope.set(key, group);
    }
    group.spans.push(span);
  }
  return {
    resourceSpans: [
      {
        resource: { attributes: resource },
        scopeSpans: [...byScope.values()],
      },
    ],
  };
}

// ── Exporter ─────────────────────────────────────────────────────────────

export type FetchLike = (
  url: string,
  init: {
    method: string;
    headers: Record<string, string>;
    body: string;
    signal?: AbortSignal;
  },
) => Promise<{ status: number; headers: { get(name: string): string | null } }>;

export interface ExporterOptions {
  /** The OTLP base: `${endpoint}/v1/logs`, `${endpoint}/v1/traces`. */
  endpoint: string;
  /** Sent on every request. The keyed ingest requires an allowlisted `Origin`. */
  headers: Record<string, string>;
  fetch: FetchLike;
  /** One request's budget. */
  timeoutMs: number;
  /** Retries after the first attempt, on a network error, 429 or 5xx gateway answer. */
  retries: number;
  /** Told about a batch that was dropped; never about one that landed. */
  onError?: (error: Error) => void;
  /** Test seam for the backoff wait. */
  sleep?: (ms: number) => Promise<void>;
}

/** Longest backoff between attempts, whatever Retry-After asks for. */
export const MAX_RETRY_DELAY_MS = 5_000;

const RETRYABLE = new Set([429, 502, 503, 504]);

function defaultSleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    const t = setTimeout(resolve, ms);
    (t as { unref?: () => void }).unref?.();
  });
}

function retryDelay(attempt: number, retryAfter: string | null): number {
  const seconds = retryAfter === null ? NaN : Number(retryAfter);
  const base =
    Number.isFinite(seconds) && seconds >= 0
      ? seconds * 1000
      : 250 * 2 ** attempt;
  return Math.min(MAX_RETRY_DELAY_MS, base);
}

/**
 * Posts one OTLP/JSON body to `${endpoint}/v1/<signal>`. Resolves true when
 * the collector accepted it, false when it was dropped (after the retries);
 * never rejects.
 */
export async function exportSignal(
  opts: ExporterOptions,
  signal: "logs" | "traces",
  body: unknown,
): Promise<boolean> {
  const url = `${opts.endpoint}/v1/${signal}`;
  const payload = JSON.stringify(body);
  const sleep = opts.sleep ?? defaultSleep;
  let last: Error | null = null;
  for (let attempt = 0; attempt <= opts.retries; attempt++) {
    let retryAfter: string | null = null;
    try {
      const res = await opts.fetch(url, {
        method: "POST",
        headers: { ...opts.headers, "content-type": "application/json" },
        body: payload,
        signal: AbortSignal.timeout(opts.timeoutMs),
      });
      if (res.status >= 200 && res.status < 300) {
        return true;
      }
      last = new Error(`OTLP ${signal} export: HTTP ${res.status}`);
      if (!RETRYABLE.has(res.status)) {
        break;
      }
      retryAfter = res.headers.get("retry-after");
    } catch (err) {
      last = err instanceof Error ? err : new Error(String(err));
    }
    if (attempt < opts.retries) {
      await sleep(retryDelay(attempt, retryAfter));
    }
  }
  try {
    opts.onError?.(last ?? new Error(`OTLP ${signal} export failed`));
  } catch {
    // A diagnostics hook must not break telemetry.
  }
  return false;
}

// ── Batching ─────────────────────────────────────────────────────────────

export interface BatchOptions<T> {
  /** Most items held; a push beyond it is dropped and counted. */
  maxQueue: number;
  /** Most items in one export. */
  maxBatch: number;
  /** How long an item may wait for a batch to fill. */
  delayMs: number;
  send(items: T[]): Promise<unknown>;
}

/**
 * A bounded queue that exports in batches: when a batch fills, when the delay
 * after the first queued item elapses, or on flush(). Its timer is unref'd, so
 * a pending batch never keeps a process alive by itself.
 */
export class BatchQueue<T> {
  private items: T[] = [];
  private timer: ReturnType<typeof setTimeout> | null = null;
  private inflight = new Set<Promise<unknown>>();
  /** Items refused because the queue was full. */
  dropped = 0;

  constructor(private readonly opts: BatchOptions<T>) {}

  get size(): number {
    return this.items.length;
  }

  push(item: T): void {
    if (this.items.length >= this.opts.maxQueue) {
      this.dropped++;
      return;
    }
    this.items.push(item);
    if (this.items.length >= this.opts.maxBatch) {
      this.drain();
    } else if (!this.timer) {
      this.timer = setTimeout(() => {
        this.timer = null;
        this.drain();
      }, this.opts.delayMs);
      (this.timer as { unref?: () => void }).unref?.();
    }
  }

  /** Start exporting everything queued now, without waiting for it. */
  drain(): void {
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    while (this.items.length > 0) {
      const batch = this.items.splice(0, this.opts.maxBatch);
      const p = this.opts.send(batch).catch(() => undefined);
      this.inflight.add(p);
      void p.finally(() => this.inflight.delete(p));
    }
  }

  /** Export everything queued and wait for every export in flight. */
  async flush(): Promise<void> {
    this.drain();
    while (this.inflight.size > 0) {
      await Promise.all([...this.inflight]);
    }
  }
}
