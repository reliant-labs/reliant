// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Node / Electron-main telemetry: logs, errors and spans over OTLP/HTTP to the
// same ingest the browser entry (`/telemetry`) ships to — usually the KEYED
// ingest, `https://<ingest host>/k/<key>`, since a desktop app's main process
// has no same-origin `/_otel`.
//
// ── What it records ──────────────────────────────────────────────────────
//
//   - uncaught exceptions and unhandled rejections, as ERROR log records in
//     the shape forge/pkg/observe gives a server error (node-exception.ts), so
//     `forge obs issues` groups them with sfp1;
//   - what the app logs through `telemetry.logger`, and errors it hands to
//     `telemetry.reportError` (and to the barrel's `reportException`);
//   - spans: the global @opentelemetry/api tracer is this one (node-trace.ts).
//
// Records carry no `rum.sessionId`. That is what makes the issues pipeline
// treat them as server-side errors (obsquery.ServerErrorFilterSQL), which is
// what a main process is.
//
// ── What it changes about the process ────────────────────────────────────
//
// Nothing an app can observe, by design. Exceptions are watched through
// `uncaughtExceptionMonitor`, which leaves the process's fate to whoever owns
// it (the app's own handler, Electron's dialog, Node's exit). Rejections have
// no monitor event, and any `unhandledRejection` listener — this one included
// — switches off Node's default of raising the rejection as an uncaught
// exception. So when this listener is the only one, it puts the default back:
// it flushes the record, then raises the rejection as Node would have
// (`--unhandled-rejections` is honoured). In plain Node with no
// `uncaughtException` handler of the app's own, the process would die before
// an async export could finish, so the runtime flushes first and then exits
// as Node does (stderr, code 1). Under Electron the main process outlives an
// uncaught exception by default, and nothing is installed.
//
// ── Batching and quitting ────────────────────────────────────────────────
//
// Records are queued and exported in batches (delay and size below), with
// unref'd timers, so telemetry never keeps a process alive. A fatal record
// starts its export at once. `flush(timeoutMs)` exports everything queued and
// resolves by the timeout whatever the network does; `flushOnQuit(app)` runs
// it from Electron's `before-quit`. An idle Node process flushes on
// `beforeExit`.
//
// ── Privacy ──────────────────────────────────────────────────────────────
//
// Nothing about the user beyond what the browser entry sends: an opaque user
// id when the app sets one, the app client, and the record itself. Messages,
// bodies and string attributes have credential shapes redacted with the same
// patterns `track()` uses; an exception keeps only its message's first line;
// stack paths lose the home directory and the install location; object-valued
// attributes are dropped, never serialized. No host name, no IP, no argv, no
// environment.
import { createRequire } from "node:module";
import { homedir } from "node:os";
import { inspect } from "node:util";

import {
  context as contextApi,
  propagation,
  trace,
  type Tracer,
} from "@opentelemetry/api";

import { setExceptionReporter } from "./error-reporter.js";
import {
  bound,
  boundMessage,
  ERROR_GROUP,
  exceptionAttributes,
  type PathScrub,
} from "./node-exception.js";
import {
  type Attributes,
  type AttributeValue,
  BatchQueue,
  encodeAttributes,
  exportSignal,
  type ExporterOptions,
  type FetchLike,
  type KeyValue,
  type LogRecord,
  logsPayload,
  MAX_ATTRIBUTE_CHARS,
  nowMs,
  type Scope,
  SEVERITY,
  type SeverityText,
  type SpanRecord,
  tracesPayload,
  unixNanos,
} from "./node-otlp.js";
import {
  AsyncLocalContextManager,
  type ContextOps,
  NodeTracerProvider,
  TraceparentPropagator,
} from "./node-trace.js";
import { redactSecrets } from "./track.js";

export { frontendServiceName } from "./service-name.js";
export {
  CODE_FILE,
  CODE_FUNCTION,
  CODE_LINE,
  ERROR_GROUP,
  EXCEPTION_MESSAGE,
  EXCEPTION_STACKTRACE,
  EXCEPTION_TYPE,
  exceptionAttributes,
  type PathScrub,
} from "./node-exception.js";

/** The instrumentation scope of every log record this entry writes. */
export const NODE_SCOPE_NAME = "@reliantlabs/forge-web-runtime/node";

/** Body of the record for an uncaught exception. */
export const UNCAUGHT_EXCEPTION_BODY = "uncaught exception";
/** Body of the record for an unhandled rejection. */
export const UNHANDLED_REJECTION_BODY = "unhandled rejection";
/** Body of the record for an error passed to reportError / reportException. */
export const REPORTED_EXCEPTION_BODY = "caught exception";

/** Longest log body. */
export const MAX_BODY_CHARS = 4096;
/** Longest user id, as the browser entry bounds it. */
export const MAX_USER_ID_CHARS = 128;

export const DEFAULT_FLUSH_TIMEOUT_MS = 2_000;
export const DEFAULT_BATCH_DELAY_MS = 5_000;
export const DEFAULT_MAX_BATCH = 256;
export const DEFAULT_MAX_QUEUE = 2_048;
export const DEFAULT_EXPORT_TIMEOUT_MS = 10_000;
/**
 * Most records of ONE fatal error (same type, message and frame) per minute.
 * A handler that throws while handling — an EPIPE logger writing to the pipe
 * that just broke — raises the same exception in a tight loop, and each one
 * would otherwise be a record and a request.
 */
export const MAX_REPEATS_PER_MINUTE = 5;

/** A value a caller may attach to a log record; anything else is dropped. */
export type LogAttributes = Record<string, unknown>;

export interface NodeTelemetryConfig {
  /** `service.name`. For a forge frontend: frontendServiceName(project, frontend). */
  serviceName: string;
  /** `service.version` — the release. */
  serviceVersion?: string;
  /** `service.namespace`, conventionally the project name. */
  serviceNamespace?: string;
  /** Emitted as `deployment.environment.name` and the legacy `deployment.environment`. */
  environment?: string;
  /** The forge frontend this process belongs to, as `forge.frontend`. */
  frontend?: string;
  /** `app.client` on every record and span (`electron`, `cli`, ...). */
  appClient?: string;
  /**
   * The OTLP base URL: `${endpoint}/v1/logs` and `/v1/traces` are posted to.
   * The keyed ingest is `https://<ingest host>/k/<key>`. Unset, empty, `off`
   * or not an absolute http(s) URL disables telemetry: init returns null and
   * installs nothing.
   */
  endpoint?: string;
  /**
   * The `Origin` header sent with every export. The keyed ingest admits a
   * request only when its Origin is EXACTLY one of the key's allowed origins,
   * so a desktop app sends its renderer's origin (`app://bundle`).
   */
  origin?: string;
  /** Extra request headers. */
  headers?: Record<string, string>;
  /** Files below it are reported relative to it. Electron: app.getAppPath(). Default process.cwd(). */
  appRoot?: string;
  /** Extra resource attributes. They cannot override `service.name`. */
  resourceAttributes?: Record<string, string>;
  /** Record uncaught exceptions and unhandled rejections. Default true. */
  captureProcessErrors?: boolean;
  /** Register the tracer, context manager and propagator globally. Default true. */
  registerGlobal?: boolean;
  /** Least severe level the logger exports. Default `INFO`. */
  minSeverity?: SeverityText;
  /** How long a record may wait for its batch. Default 5s. */
  batchDelayMs?: number;
  /** Most records in one request. Default 256. */
  maxBatchSize?: number;
  /** Most records held per signal; more are dropped and counted. Default 2048. */
  maxQueueSize?: number;
  /** One request's budget. Default 10s. */
  exportTimeoutMs?: number;
  /** How long a fatal error's record may delay the process's default exit. Default 2s. */
  fatalFlushTimeoutMs?: number;
  /** Told when a batch is dropped (the ingest refused it or was unreachable). */
  onExportError?: (error: Error) => void;
  /** Test seam: the fetch exports go through. Default globalThis.fetch. */
  fetch?: FetchLike;
}

export interface NodeLogger {
  debug(body: string, attributes?: LogAttributes): void;
  info(body: string, attributes?: LogAttributes): void;
  warn(body: string, attributes?: LogAttributes): void;
  /** An ERROR record. An Error among the attributes supplies its exception.* keys. */
  error(body: string, attributes?: LogAttributes): void;
}

export interface NodeTelemetry {
  readonly logger: NodeLogger;
  /** The tracer to open spans on (also the global one, unless registerGlobal is false). */
  readonly tracer: Tracer;
  /** One ERROR record for an error the app caught. */
  reportError(error: unknown, attributes?: LogAttributes): void;
  /** The opaque `userId` on every later record and span; null clears it. Never an email. */
  setUser(id: string | null): void;
  /** Export everything queued; resolves by `timeoutMs` (default 2s) whatever the network does. */
  flush(timeoutMs?: number): Promise<void>;
  /** Flush, then stop: every later call is a no-op. init may run again afterwards. */
  shutdown(timeoutMs?: number): Promise<void>;
  /** Records refused because a queue was full. */
  readonly dropped: number;
}

let current: NodeTelemetry | null = null;

/** The running instance, or null before init / after shutdown. */
export function getNodeTelemetry(): NodeTelemetry | null {
  return current;
}

/**
 * The absolute OTLP base for `endpoint`, or null when telemetry is off: unset,
 * empty, `off`, or not an absolute http(s) URL. Trailing slashes are dropped.
 */
export function resolveNodeIngestUrl(
  endpoint: string | undefined,
): string | null {
  const value = (endpoint ?? "").trim();
  if (value === "" || value.toLowerCase() === "off") {
    return null;
  }
  try {
    const url = new URL(value);
    if (url.protocol !== "http:" && url.protocol !== "https:") {
      return null;
    }
    url.search = "";
    url.hash = "";
    return url.toString().replace(/\/+$/, "");
  } catch {
    return null;
  }
}

function sdkVersion(): string {
  try {
    const req = createRequire(import.meta.url);
    const v = (req("../package.json") as { version?: unknown }).version;
    return typeof v === "string" ? v : "";
  } catch {
    return "";
  }
}

const OS_TYPE: Record<string, string> = { win32: "windows", sunos: "solaris" };
const HOST_ARCH: Record<string, string> = {
  x64: "amd64",
  ia32: "x86",
  ppc64: "ppc64",
};

/**
 * The resource every record and span carries. Pure given its inputs;
 * exported for tests and for an app that wants to see exactly what is sent.
 */
export function buildNodeResource(
  config: Pick<
    NodeTelemetryConfig,
    | "serviceName"
    | "serviceVersion"
    | "serviceNamespace"
    | "environment"
    | "frontend"
    | "resourceAttributes"
  >,
  runtime: {
    versions: Record<string, string | undefined>;
    platform: string;
    arch: string;
  },
  version = sdkVersion(),
): Record<string, string> {
  const electron = runtime.versions.electron;
  const out: Record<string, string> = {
    "telemetry.sdk.name": "@reliantlabs/forge-web-runtime",
    "telemetry.sdk.language": "nodejs",
    "process.runtime.name": electron ? "electron" : "nodejs",
    "process.runtime.version": (electron ??
      runtime.versions.node ??
      "") as string,
    "os.type": OS_TYPE[runtime.platform] ?? runtime.platform,
    "host.arch": HOST_ARCH[runtime.arch] ?? runtime.arch,
  };
  if (version) out["telemetry.sdk.version"] = version;
  if (config.serviceVersion) out["service.version"] = config.serviceVersion;
  if (config.serviceNamespace)
    out["service.namespace"] = config.serviceNamespace;
  if (config.frontend) out["forge.frontend"] = config.frontend;
  if (config.environment) {
    out["deployment.environment.name"] = config.environment;
    out["deployment.environment"] = config.environment;
  }
  Object.assign(out, config.resourceAttributes ?? {});
  out["service.name"] = config.serviceName;
  return out;
}

function scrubString(value: string, max: number): string {
  return bound(redactSecrets(value.slice(0, max * 2)), max);
}

/**
 * Reduce caller attributes to what a record may carry: strings redacted and
 * bounded, finite numbers, booleans, arrays of those; an Error becomes its
 * bounded message (its stack goes in exception.*). Objects are dropped.
 */
function sanitizeAttributes(attrs: LogAttributes | undefined): {
  attributes: Attributes;
  error: unknown;
} {
  const out: Attributes = {};
  let error: unknown = undefined;
  if (!attrs || typeof attrs !== "object") {
    return { attributes: out, error };
  }
  for (const [key, v] of Object.entries(attrs)) {
    if (v instanceof Error) {
      if (error === undefined) error = v;
      out[key] = boundMessage(v.message ?? "");
    } else if (typeof v === "string") {
      out[key] = scrubString(v, MAX_ATTRIBUTE_CHARS);
    } else if (
      (typeof v === "number" && Number.isFinite(v)) ||
      typeof v === "boolean"
    ) {
      out[key] = v;
    } else if (Array.isArray(v)) {
      const kept = v
        .filter(
          (x) =>
            typeof x === "string" ||
            typeof x === "boolean" ||
            (typeof x === "number" && Number.isFinite(x)),
        )
        .map((x) =>
          typeof x === "string" ? scrubString(x, MAX_ATTRIBUTE_CHARS) : x,
        );
      out[key] = kept as AttributeValue;
    }
  }
  return { attributes: out, error };
}

function withTimeout(p: Promise<unknown>, ms: number): Promise<void> {
  return new Promise((resolve) => {
    const t = setTimeout(resolve, Math.max(0, ms));
    (t as { unref?: () => void }).unref?.();
    void p.then(
      () => {
        clearTimeout(t);
        resolve();
      },
      () => {
        clearTimeout(t);
        resolve();
      },
    );
  });
}

/** What `--unhandled-rejections` says Node does with a rejection nobody handles. */
export function unhandledRejectionMode(
  execArgv: readonly string[],
  nodeOptions: string | undefined,
): string {
  const args = [...execArgv, ...(nodeOptions ?? "").split(/\s+/)];
  let mode = "throw";
  for (let i = 0; i < args.length; i++) {
    const a = args[i] ?? "";
    if (a.startsWith("--unhandled-rejections=")) {
      mode = a.slice("--unhandled-rejections=".length);
    } else if (a === "--unhandled-rejections" && args[i + 1]) {
      mode = args[i + 1] as string;
    }
  }
  return mode;
}

/** The error Node raises for a rejection whose reason is not an Error. */
function rejectionError(reason: unknown): Error {
  const err = new Error(
    "This error originated either by throwing inside of an async function without a catch block, " +
      "or by rejecting a promise which was not handled with .catch(). The promise rejected with the reason " +
      `"${inspect(reason)}".`,
  );
  err.name = "UnhandledPromiseRejection";
  (err as { code?: string }).code = "ERR_UNHANDLED_REJECTION";
  return err;
}

/**
 * initNodeTelemetry starts telemetry for this process and returns it, or null
 * when it is off (no usable endpoint, no fetch, not Node). Idempotent: a
 * second call returns the running instance and ignores its config. Never
 * throws.
 */
export function initNodeTelemetry(
  config: NodeTelemetryConfig,
): NodeTelemetry | null {
  if (current) {
    return current;
  }
  try {
    const instance = start(config);
    current = instance;
    return instance;
  } catch {
    // Telemetry must never break the app.
    return null;
  }
}

function start(config: NodeTelemetryConfig): NodeTelemetry | null {
  if (typeof process === "undefined" || !process.versions?.node) {
    return null;
  }
  const endpoint = resolveNodeIngestUrl(config.endpoint);
  const fetchFn: FetchLike | undefined =
    config.fetch ??
    (typeof globalThis.fetch === "function"
      ? (globalThis.fetch as unknown as FetchLike)
      : undefined);
  if (!endpoint || !fetchFn || !config.serviceName) {
    return null;
  }

  const isElectron = Boolean(process.versions.electron);
  const scrub: PathScrub = {
    appRoot: config.appRoot ?? process.cwd(),
    homeDir: safeHomedir(),
    caseInsensitive: process.platform === "win32",
  };
  const resource: KeyValue[] = encodeAttributes(
    buildNodeResource(config, {
      versions: process.versions,
      platform: process.platform,
      arch: process.arch,
    }),
  );
  const headers: Record<string, string> = { ...(config.headers ?? {}) };
  const origin = config.origin?.trim().replace(/\/+$/, "");
  if (origin) {
    headers["origin"] = origin;
  }
  const exporter: ExporterOptions = {
    endpoint,
    headers,
    fetch: fetchFn,
    timeoutMs: config.exportTimeoutMs ?? DEFAULT_EXPORT_TIMEOUT_MS,
    retries: 2,
    onError: config.onExportError,
  };
  const batch = {
    maxQueue: config.maxQueueSize ?? DEFAULT_MAX_QUEUE,
    maxBatch: config.maxBatchSize ?? DEFAULT_MAX_BATCH,
    delayMs: config.batchDelayMs ?? DEFAULT_BATCH_DELAY_MS,
  };
  const logScope: Scope = {
    name: NODE_SCOPE_NAME,
    version: sdkVersion() || undefined,
  };
  const logs = new BatchQueue<LogRecord>({
    ...batch,
    send: (records) =>
      exportSignal(exporter, "logs", logsPayload(resource, logScope, records)),
  });
  const spans = new BatchQueue<SpanRecord>({
    ...batch,
    send: (records) =>
      exportSignal(exporter, "traces", tracesPayload(resource, records)),
  });

  let stopped = false;
  let userId: string | null = null;
  const globals = (): Attributes => {
    const g: Attributes = {};
    if (config.appClient) g["app.client"] = config.appClient;
    if (userId) g["userId"] = userId;
    return g;
  };

  // Context: ours, registered globally when allowed. If another manager got
  // there first, spans follow ITS context so the app's context.active() and
  // ours agree.
  const manager = new AsyncLocalContextManager();
  const registerGlobal = config.registerGlobal ?? true;
  const ownsContext =
    registerGlobal && contextApi.setGlobalContextManager(manager);
  const ctxOps: ContextOps = registerGlobal
    ? {
        active: () => contextApi.active(),
        with: (c, fn) => contextApi.with(c, fn),
      }
    : { active: () => manager.active(), with: (c, fn) => manager.with(c, fn) };
  const provider = new NodeTracerProvider(
    (span) => {
      if (!stopped) spans.push(span);
    },
    globals,
    ctxOps,
  );
  const ownsTrace = registerGlobal && trace.setGlobalTracerProvider(provider);
  const ownsPropagation =
    registerGlobal &&
    propagation.setGlobalPropagator(new TraceparentPropagator());
  const tracer = provider.getTracer(NODE_SCOPE_NAME, sdkVersion() || undefined);

  const minSeverity = SEVERITY[config.minSeverity ?? "INFO"] ?? SEVERITY.INFO;

  function emit(
    severity: SeverityText,
    body: string,
    attributes: Attributes,
  ): void {
    if (stopped || SEVERITY[severity] < minSeverity) {
      return;
    }
    const now = unixNanos(nowMs());
    const record: LogRecord = {
      timeUnixNano: now,
      observedTimeUnixNano: now,
      severityNumber: SEVERITY[severity],
      severityText: severity,
      body: { stringValue: scrubString(body, MAX_BODY_CHARS) },
      attributes: encodeAttributes({ ...globals(), ...attributes }),
    };
    const sc = trace.getSpanContext(ctxOps.active());
    if (sc && trace.isSpanContextValid(sc)) {
      record.traceId = sc.traceId;
      record.spanId = sc.spanId;
      record.flags = sc.traceFlags;
    }
    logs.push(record);
  }

  function log(
    severity: SeverityText,
    body: string,
    attrs?: LogAttributes,
  ): void {
    try {
      const { attributes, error } = sanitizeAttributes(attrs);
      // As observe: only an ERROR record gets exception inputs, and an
      // attribute the caller set wins over the derived one.
      const derived =
        severity === "ERROR" && error !== undefined
          ? exceptionAttributes(error, scrub)
          : {};
      emit(severity, body, { ...derived, ...attributes });
    } catch {
      // Telemetry must never break the app.
    }
  }

  const logger: NodeLogger = {
    debug: (b, a) => log("DEBUG", b, a),
    info: (b, a) => log("INFO", b, a),
    warn: (b, a) => log("WARN", b, a),
    error: (b, a) => log("ERROR", b, a),
  };

  function reportError(error: unknown, attrs?: LogAttributes): void {
    try {
      const { attributes } = sanitizeAttributes(attrs);
      emit("ERROR", REPORTED_EXCEPTION_BODY, {
        ...exceptionAttributes(error, scrub),
        ...attributes,
      });
    } catch {
      // Telemetry must never break the app.
    }
  }

  // ── Process errors ──────────────────────────────────────────────────────
  const recorded = new WeakSet<object>();
  const repeats = new Map<string, { since: number; count: number }>();
  let drainScheduled = false;
  function recordFatal(value: unknown, body: string): void {
    if (typeof value === "object" && value !== null) {
      if (recorded.has(value)) return;
      recorded.add(value);
    }
    try {
      const attributes = exceptionAttributes(value, scrub);
      const key = [
        body,
        attributes["exception.type"],
        attributes["code.function.name"],
        attributes["exception.message"],
      ].join("\u001f");
      const now = Date.now();
      let seen = repeats.get(key);
      if (!seen || now - seen.since >= 60_000) {
        if (repeats.size >= 256) repeats.clear();
        seen = { since: now, count: 0 };
        repeats.set(key, seen);
      }
      if (++seen.count > MAX_REPEATS_PER_MINUTE) {
        return;
      }
      emit("ERROR", body, attributes);
      // A fatal record does not wait for its batch; a burst in one turn of the
      // event loop goes out as one request.
      if (!drainScheduled) {
        drainScheduled = true;
        setImmediate(() => {
          drainScheduled = false;
          logs.drain();
        }).unref();
      }
    } catch {
      // Telemetry must never break the app.
    }
  }
  const fatalFlushMs = config.fatalFlushTimeoutMs ?? DEFAULT_FLUSH_TIMEOUT_MS;

  const onMonitor = (err: unknown, origin: string): void => {
    recordFatal(
      err,
      origin === "unhandledRejection"
        ? UNHANDLED_REJECTION_BODY
        : UNCAUGHT_EXCEPTION_BODY,
    );
  };
  const onRejection = (reason: unknown): void => {
    recordFatal(reason, UNHANDLED_REJECTION_BODY);
    if (process.listenerCount("unhandledRejection") > 1) {
      return; // The app handles rejections; it decides what happens next.
    }
    const mode = unhandledRejectionMode(
      process.execArgv,
      process.env.NODE_OPTIONS,
    );
    if (mode === "warn" || mode === "none") {
      return;
    }
    if (mode === "warn-with-error-code") {
      process.exitCode = 1;
      return;
    }
    // throw / strict: what Node does with no listener at all — raise it as an
    // uncaught exception — once the record is out.
    const err = reason instanceof Error ? reason : rejectionError(reason);
    recorded.add(err);
    void withTimeout(flushAll(), fatalFlushMs).then(() =>
      process.nextTick(() => {
        throw err;
      }),
    );
  };
  let exiting = false;
  const onUncaught = (err: unknown): void => {
    if (process.listenerCount("uncaughtException") > 1 || exiting) {
      return; // The app's own handler owns the process's fate.
    }
    // Node's default (print, exit 1), after the record is out.
    exiting = true;
    void withTimeout(flushAll(), fatalFlushMs).then(() => {
      try {
        process.stderr.write(
          `${err instanceof Error && err.stack ? err.stack : inspect(err)}\n`,
        );
      } catch {
        // stderr may be gone; the exit code still says what happened.
      }
      process.exit(1);
    });
  };
  const onBeforeExit = (): void => {
    if (logs.size > 0 || spans.size > 0) {
      void flushAll();
    }
  };

  const captureErrors = config.captureProcessErrors ?? true;
  if (captureErrors) {
    process.on("uncaughtExceptionMonitor", onMonitor);
    process.on("unhandledRejection", onRejection);
    if (!isElectron) {
      process.on("uncaughtException", onUncaught);
    }
  }
  process.on("beforeExit", onBeforeExit);
  setExceptionReporter((error, attributes) => reportError(error, attributes));

  function flushAll(): Promise<void> {
    return Promise.all([logs.flush(), spans.flush()]).then(() => undefined);
  }

  const instance: NodeTelemetry = {
    logger,
    tracer,
    reportError,
    setUser(id) {
      userId =
        typeof id === "string" && id !== ""
          ? id.slice(0, MAX_USER_ID_CHARS)
          : null;
    },
    flush(timeoutMs = DEFAULT_FLUSH_TIMEOUT_MS) {
      return withTimeout(flushAll(), timeoutMs);
    },
    async shutdown(timeoutMs = DEFAULT_FLUSH_TIMEOUT_MS) {
      if (stopped) return;
      await withTimeout(flushAll(), timeoutMs);
      stopped = true;
      process.removeListener("uncaughtExceptionMonitor", onMonitor);
      process.removeListener("unhandledRejection", onRejection);
      process.removeListener("uncaughtException", onUncaught);
      process.removeListener("beforeExit", onBeforeExit);
      setExceptionReporter(null);
      if (ownsTrace) trace.disable();
      if (ownsPropagation) propagation.disable();
      if (ownsContext) contextApi.disable();
      if (current === instance) current = null;
    },
    get dropped() {
      return logs.dropped + spans.dropped;
    },
  };
  return instance;
}

function safeHomedir(): string | undefined {
  try {
    return homedir() || undefined;
  } catch {
    return undefined;
  }
}

/** The slice of Electron's `app` flushOnQuit needs. */
export interface QuitApp {
  on(
    event: "before-quit",
    listener: (event: { preventDefault(): void }) => void,
  ): unknown;
  removeListener?(
    event: "before-quit",
    listener: (event: { preventDefault(): void }) => void,
  ): unknown;
  quit(): void;
}

/**
 * Hold Electron's quit until the running telemetry has flushed (bounded by
 * `timeoutMs`, default 2s), then quit. Returns a function that removes the
 * hook.
 *
 * It defers the FIRST quit and calls app.quit() again when the flush is done.
 * An app that already defers quit for its own shutdown (stops a backend, then
 * exits) should instead `await telemetry.flush()` as the last step of that
 * sequence: two listeners that each defer and re-quit cut each other short.
 */
export function flushOnQuit(
  app: QuitApp,
  options: { timeoutMs?: number } = {},
): () => void {
  let flushed = false;
  const listener = (event: { preventDefault(): void }): void => {
    const t = current;
    if (flushed || !t) {
      return;
    }
    flushed = true;
    event.preventDefault();
    void t
      .flush(options.timeoutMs ?? DEFAULT_FLUSH_TIMEOUT_MS)
      .finally(() => app.quit());
  };
  app.on("before-quit", listener);
  return () => {
    app.removeListener?.("before-quit", listener);
  };
}
