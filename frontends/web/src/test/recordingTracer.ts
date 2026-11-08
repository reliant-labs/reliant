// A recording OpenTelemetry tracer, registered globally the way the HyperDX
// SDK registers its own, for tests that assert on the spans the web runtime's
// interaction() records: name, attributes, events, status and whether it ended.

import {
  SpanStatusCode,
  trace,
  type Attributes,
  type AttributeValue,
  type Span,
  type SpanOptions,
  type Tracer,
} from "@opentelemetry/api";

export interface RecordedSpan {
  name: string;
  traceId: string;
  attributes: Attributes;
  events: Array<{ name: string; attributes: Attributes }>;
  status: SpanStatusCode | undefined;
  ended: boolean;
}

const hex = (n: number) =>
  Array.from({ length: n }, () => Math.floor(Math.random() * 16).toString(16)).join("");

function recordingSpan(rec: RecordedSpan): Span {
  const spanContext = { traceId: rec.traceId, spanId: hex(16), traceFlags: 1 };
  const span = {
    spanContext: () => spanContext,
    setAttribute: (key: string, value: AttributeValue) => {
      rec.attributes[key] = value;
      return span;
    },
    setAttributes: (attributes: Attributes) => {
      Object.assign(rec.attributes, attributes);
      return span;
    },
    addEvent: (name: string, attributes?: Attributes) => {
      rec.events.push({ name, attributes: { ...attributes } });
      return span;
    },
    addLink: () => span,
    addLinks: () => span,
    setStatus: (status: { code: SpanStatusCode }) => {
      rec.status = status.code;
      return span;
    },
    updateName: () => span,
    end: () => {
      rec.ended = true;
    },
    isRecording: () => !rec.ended,
    recordException: () => {},
  };
  return span as unknown as Span;
}

/**
 * Register a recording tracer provider. Returns the spans it records and an
 * uninstall that restores the no-op provider.
 */
export function installRecordingTracer(): { spans: RecordedSpan[]; uninstall: () => void } {
  const spans: RecordedSpan[] = [];
  const tracer = {
    startSpan: (name: string, options?: SpanOptions) => {
      const rec: RecordedSpan = {
        name,
        traceId: hex(32),
        attributes: { ...options?.attributes },
        events: [],
        status: undefined,
        ended: false,
      };
      spans.push(rec);
      return recordingSpan(rec);
    },
    startActiveSpan: () => {
      throw new Error("not used");
    },
  } as unknown as Tracer;
  trace.setGlobalTracerProvider({ getTracer: () => tracer });
  return { spans, uninstall: () => trace.disable() };
}
