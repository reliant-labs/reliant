// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Spans for the `/node` entry: a TracerProvider behind @opentelemetry/api, an
// AsyncLocalStorage context manager, and the W3C traceparent propagator. Every
// span is recorded and sampled (a desktop app's main process opens few), and a
// finished span is handed to the exporter's queue.
//
// This is the part of the OpenTelemetry SDK `/node` needs and no more; see
// node-otlp.ts for why the SDK itself is not a dependency.
import { AsyncLocalStorage } from "node:async_hooks";
import { randomBytes } from "node:crypto";

import {
  type Context,
  type ContextManager,
  type Exception,
  type HrTime,
  type Link,
  ROOT_CONTEXT,
  type Span,
  type SpanAttributes,
  type SpanAttributeValue,
  type SpanContext,
  SpanKind,
  type SpanOptions,
  SpanStatusCode,
  type SpanStatus,
  type TextMapGetter,
  type TextMapPropagator,
  type TextMapSetter,
  type TimeInput,
  trace,
  TraceFlags,
  type Tracer,
  type TracerProvider,
} from "@opentelemetry/api";

import {
  type Attributes,
  encodeAttributes,
  MAX_ATTRIBUTES,
  nowMs,
  type Scope,
  type SpanRecord,
  unixNanos,
} from "./node-otlp.js";
import { TRACEPARENT_RE } from "./trace.js";

/** Most events kept on one span. */
export const MAX_SPAN_EVENTS = 128;
/** Most links kept on one span. */
export const MAX_SPAN_LINKS = 128;

const ZERO_TRACE = "0".repeat(32);
const ZERO_SPAN = "0".repeat(16);

function hexId(bytes: number, zero: string): string {
  let id = randomBytes(bytes).toString("hex");
  while (id === zero) {
    id = randomBytes(bytes).toString("hex");
  }
  return id;
}

/** Epoch milliseconds for an OTel TimeInput, as the SDK reads one. */
export function timeInputMs(t: TimeInput | undefined): number {
  if (t === undefined) {
    return nowMs();
  }
  if (t instanceof Date) {
    return t.getTime();
  }
  if (Array.isArray(t)) {
    const [s, ns] = t as HrTime;
    return s * 1000 + ns / 1e6;
  }
  // The SDK's rule: a number below the process's time origin is a
  // performance.now() reading, anything else is epoch milliseconds.
  return t < performance.timeOrigin ? performance.timeOrigin + t : t;
}

function isTimeInput(v: unknown): v is TimeInput {
  return (
    typeof v === "number" ||
    v instanceof Date ||
    (Array.isArray(v) && v.length === 2 && typeof v[0] === "number")
  );
}

type Sink = (span: SpanRecord) => void;

/** The values every span carries, set once by the entry (app.client, userId). */
export type GlobalAttributes = () => Attributes;

class RecordingSpan implements Span {
  private ended = false;
  private readonly attributes: Attributes = {};
  private droppedAttributes = 0;
  private readonly events: SpanRecord["events"] = [];
  private readonly links: SpanRecord["links"] = [];
  private status: SpanStatus = { code: SpanStatusCode.UNSET };

  constructor(
    private readonly ctx: SpanContext,
    private readonly parentSpanId: string | undefined,
    private name: string,
    private readonly kind: SpanKind,
    private readonly startMs: number,
    private readonly scope: Scope,
    private readonly sink: Sink,
  ) {}

  spanContext(): SpanContext {
    return this.ctx;
  }

  setAttribute(key: string, value: SpanAttributeValue): this {
    if (this.ended || value === undefined || value === null) {
      return this;
    }
    if (
      !(key in this.attributes) &&
      Object.keys(this.attributes).length >= MAX_ATTRIBUTES
    ) {
      this.droppedAttributes++;
      return this;
    }
    this.attributes[key] = value as Attributes[string];
    return this;
  }

  setAttributes(attributes: SpanAttributes): this {
    for (const [k, v] of Object.entries(attributes)) {
      if (v !== undefined) {
        this.setAttribute(k, v);
      }
    }
    return this;
  }

  addEvent(
    name: string,
    attributesOrStartTime?: SpanAttributes | TimeInput,
    startTime?: TimeInput,
  ): this {
    if (this.ended || this.events.length >= MAX_SPAN_EVENTS) {
      return this;
    }
    let attrs: SpanAttributes | undefined;
    let time = startTime;
    if (isTimeInput(attributesOrStartTime)) {
      time = attributesOrStartTime;
    } else {
      attrs = attributesOrStartTime;
    }
    this.events.push({
      timeUnixNano: unixNanos(timeInputMs(time)),
      name,
      attributes: encodeAttributes(attrs as Attributes | undefined),
    });
    return this;
  }

  addLink(link: Link): this {
    if (!this.ended && this.links.length < MAX_SPAN_LINKS) {
      this.links.push({
        traceId: link.context.traceId,
        spanId: link.context.spanId,
        attributes: encodeAttributes(link.attributes as Attributes | undefined),
      });
    }
    return this;
  }

  addLinks(links: Link[]): this {
    for (const l of links) {
      this.addLink(l);
    }
    return this;
  }

  setStatus(status: SpanStatus): this {
    if (this.ended) {
      return this;
    }
    // OK is final, as in the SDK: a later ERROR does not overwrite it.
    if (this.status.code === SpanStatusCode.OK) {
      return this;
    }
    this.status = status;
    return this;
  }

  updateName(name: string): this {
    if (!this.ended) {
      this.name = name;
    }
    return this;
  }

  recordException(exception: Exception, time?: TimeInput): void {
    const attrs: SpanAttributes = {};
    if (typeof exception === "string") {
      attrs["exception.message"] = exception;
    } else if (exception) {
      const e = exception as {
        name?: unknown;
        message?: unknown;
        stack?: unknown;
        code?: unknown;
      };
      if (typeof e.name === "string") attrs["exception.type"] = e.name;
      else if (typeof e.code === "string" || typeof e.code === "number")
        attrs["exception.type"] = String(e.code);
      if (typeof e.message === "string") attrs["exception.message"] = e.message;
      if (typeof e.stack === "string") attrs["exception.stacktrace"] = e.stack;
    }
    this.addEvent("exception", attrs, time);
  }

  end(endTime?: TimeInput): void {
    if (this.ended) {
      return;
    }
    this.ended = true;
    const endMs = Math.max(this.startMs, timeInputMs(endTime));
    this.sink({
      traceId: this.ctx.traceId,
      spanId: this.ctx.spanId,
      parentSpanId: this.parentSpanId,
      name: this.name,
      // OTLP's SpanKind is the API's plus one (0 is UNSPECIFIED on the wire).
      kind: this.kind + 1,
      startTimeUnixNano: unixNanos(this.startMs),
      endTimeUnixNano: unixNanos(endMs),
      attributes: encodeAttributes(this.attributes),
      droppedAttributesCount: this.droppedAttributes || undefined,
      events: this.events,
      links: this.links,
      status:
        this.status.message !== undefined
          ? { code: this.status.code, message: this.status.message }
          : { code: this.status.code },
      scope: this.scope,
    });
  }

  isRecording(): boolean {
    return !this.ended;
  }
}

/** The context operations a tracer runs on: the global API's, or a manager's own. */
export interface ContextOps {
  active(): Context;
  with<T>(ctx: Context, fn: () => T): T;
}

class NodeTracer implements Tracer {
  constructor(
    private readonly scope: Scope,
    private readonly sink: Sink,
    private readonly globals: GlobalAttributes,
    private readonly ctx: ContextOps,
  ) {}

  startSpan(name: string, options: SpanOptions = {}, ctx?: Context): Span {
    const parentCtx = options.root
      ? undefined
      : trace.getSpanContext(ctx ?? this.ctx.active());
    const parent =
      parentCtx && trace.isSpanContextValid(parentCtx) ? parentCtx : undefined;
    const spanContext: SpanContext = {
      traceId: parent?.traceId ?? hexId(16, ZERO_TRACE),
      spanId: hexId(8, ZERO_SPAN),
      traceFlags: TraceFlags.SAMPLED,
    };
    const span = new RecordingSpan(
      spanContext,
      parent?.spanId,
      name,
      options.kind ?? SpanKind.INTERNAL,
      timeInputMs(options.startTime),
      this.scope,
      this.sink,
    );
    for (const [k, v] of Object.entries(this.globals())) {
      if (v !== undefined) span.setAttribute(k, v as SpanAttributeValue);
    }
    if (options.attributes) {
      span.setAttributes(options.attributes);
    }
    if (options.links) {
      span.addLinks(options.links);
    }
    return span;
  }

  startActiveSpan<F extends (span: Span) => unknown>(
    name: string,
    fn: F,
  ): ReturnType<F>;
  startActiveSpan<F extends (span: Span) => unknown>(
    name: string,
    options: SpanOptions,
    fn: F,
  ): ReturnType<F>;
  startActiveSpan<F extends (span: Span) => unknown>(
    name: string,
    options: SpanOptions,
    context: Context,
    fn: F,
  ): ReturnType<F>;
  startActiveSpan<F extends (span: Span) => unknown>(
    name: string,
    a?: SpanOptions | F,
    b?: Context | F,
    c?: F,
  ): ReturnType<F> {
    let options: SpanOptions = {};
    let parent: Context | undefined;
    let fn: F;
    if (typeof a === "function") {
      fn = a;
    } else if (typeof b === "function") {
      options = a ?? {};
      fn = b;
    } else {
      options = a ?? {};
      parent = b;
      fn = c as F;
    }
    const base = parent ?? this.ctx.active();
    const span = this.startSpan(name, options, base);
    const ctx = trace.setSpan(base, span);
    return this.ctx.with(ctx, () => fn(span)) as ReturnType<F>;
  }
}

/** A TracerProvider whose spans go to `sink`. */
export class NodeTracerProvider implements TracerProvider {
  private readonly tracers = new Map<string, Tracer>();

  constructor(
    private readonly sink: Sink,
    private readonly globals: GlobalAttributes,
    private readonly ctx: ContextOps,
  ) {}

  getTracer(name: string, version?: string): Tracer {
    const key = `${name}\u001f${version ?? ""}`;
    let t = this.tracers.get(key);
    if (!t) {
      t = new NodeTracer(
        version ? { name, version } : { name },
        this.sink,
        this.globals,
        this.ctx,
      );
      this.tracers.set(key, t);
    }
    return t;
  }
}

/** The OTel ContextManager over AsyncLocalStorage: context follows async calls. */
export class AsyncLocalContextManager implements ContextManager {
  private store = new AsyncLocalStorage<Context>();

  active(): Context {
    return this.store.getStore() ?? ROOT_CONTEXT;
  }

  with<A extends unknown[], F extends (...args: A) => ReturnType<F>>(
    context: Context,
    fn: F,
    thisArg?: ThisParameterType<F>,
    ...args: A
  ): ReturnType<F> {
    const cb = thisArg == null ? fn : fn.bind(thisArg);
    return this.store.run(
      context,
      cb as (...args: A) => ReturnType<F>,
      ...args,
    );
  }

  bind<T>(context: Context, target: T): T {
    if (typeof target !== "function") {
      return target;
    }
    const self = this;
    const fn = target as unknown as (...args: unknown[]) => unknown;
    return function (this: unknown, ...args: unknown[]) {
      return self.with(context, () => fn.apply(this, args));
    } as unknown as T;
  }

  enable(): this {
    return this;
  }

  disable(): this {
    this.store.disable();
    this.store = new AsyncLocalStorage<Context>();
    return this;
  }
}

/** W3C Trace Context: `traceparent` in and out. */
export class TraceparentPropagator implements TextMapPropagator {
  inject(context: Context, carrier: unknown, setter: TextMapSetter): void {
    const sc = trace.getSpanContext(context);
    if (!sc || !trace.isSpanContextValid(sc)) {
      return;
    }
    const flags =
      (sc.traceFlags & TraceFlags.SAMPLED) === TraceFlags.SAMPLED ? "01" : "00";
    setter.set(
      carrier,
      "traceparent",
      `00-${sc.traceId}-${sc.spanId}-${flags}`,
    );
  }

  extract(context: Context, carrier: unknown, getter: TextMapGetter): Context {
    const raw = getter.get(carrier, "traceparent");
    const value = (Array.isArray(raw) ? raw[0] : raw)?.trim();
    if (!value || !TRACEPARENT_RE.test(value)) {
      return context;
    }
    const [, traceId, spanId, flags] = value.split("-");
    if (!traceId || !spanId || traceId === ZERO_TRACE || spanId === ZERO_SPAN) {
      return context;
    }
    return trace.setSpanContext(context, {
      traceId,
      spanId,
      traceFlags: parseInt(flags ?? "00", 16) & TraceFlags.SAMPLED,
      isRemote: true,
    });
  }

  fields(): string[] {
    return ["traceparent"];
  }
}
