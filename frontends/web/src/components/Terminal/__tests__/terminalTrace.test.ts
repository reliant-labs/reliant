import { afterEach, describe, expect, it } from "vitest";
import {
  propagation,
  trace,
  type AttributeValue,
  type Context,
  type Span,
  type SpanContext,
  type TextMapPropagator,
  type Tracer,
  type TracerProvider,
} from "@opentelemetry/api";
import { startTerminalSessionSpan } from "../terminalTrace";

// The terminal websocket's upgrade cannot carry headers, so the client span's
// trace context rides the URL. These pin that the server gets a traceparent
// naming the client span, and that the span records how the connection ended.

const SPAN_CONTEXT: SpanContext = {
  traceId: "0af7651916cd43dd8448eb211c80319c",
  spanId: "b7ad6b7169203331",
  traceFlags: 1,
};

interface RecordedSpan {
  name: string;
  attributes: Record<string, AttributeValue | undefined>;
  events: string[];
  ends: number;
}

/** A tracer provider that records the spans it starts. */
function recordingProvider(spans: RecordedSpan[]): TracerProvider {
  const tracer: Tracer = {
    startSpan(name, options) {
      const rec: RecordedSpan = { name, attributes: { ...options?.attributes }, events: [], ends: 0 };
      spans.push(rec);
      const span = trace.wrapSpanContext(SPAN_CONTEXT);
      return Object.assign(span, {
        setAttribute(key: string, value: AttributeValue) {
          rec.attributes[key] = value;
          return span;
        },
        addEvent(event: string) {
          rec.events.push(event);
          return span;
        },
        end() {
          rec.ends += 1;
        },
      }) as Span;
    },
    startActiveSpan: (() => {
      throw new Error("not used");
    }) as Tracer["startActiveSpan"],
  };
  return { getTracer: () => tracer };
}

/** W3C traceparent for whatever span is in the context. */
const traceparentPropagator: TextMapPropagator = {
  inject(ctx: Context, carrier, setter) {
    const sc = trace.getSpanContext(ctx);
    if (sc) setter.set(carrier, "traceparent", `00-${sc.traceId}-${sc.spanId}-01`);
  },
  extract: (ctx) => ctx,
  fields: () => ["traceparent"],
};

afterEach(() => {
  trace.disable();
  propagation.disable();
});

describe("startTerminalSessionSpan", () => {
  it("puts the client span's traceparent and the reconnect flag on the upgrade URL", () => {
    const spans: RecordedSpan[] = [];
    trace.setGlobalTracerProvider(recordingProvider(spans));
    propagation.setGlobalPropagator(traceparentPropagator);

    const params = new URLSearchParams({ token: "t", workingDir: "/home/me/secret" });
    const span = startTerminalSessionSpan(params, { reconnect: true, projectId: "proj-1" });

    expect(params.get("traceparent")).toBe(`00-${SPAN_CONTEXT.traceId}-${SPAN_CONTEXT.spanId}-01`);
    expect(params.get("reconnect")).toBe("1");
    expect(spans).toHaveLength(1);
    expect(spans[0].name).toBe("terminal.session");
    expect(spans[0].attributes).toMatchObject({ "terminal.reconnect": true, "terminal.project_id": "proj-1" });
    expect(JSON.stringify(spans[0].attributes)).not.toContain("/home/me/secret");

    span.established();
    span.end(1000, "closed");
    span.end(undefined, "unmounted"); // the later retirement of the same socket
    expect(spans[0].events).toEqual(["terminal.established"]);
    expect(spans[0].ends).toBe(1);
    expect(spans[0].attributes).toMatchObject({ "terminal.close_code": 1000, "terminal.end_reason": "closed" });
  });

  it("adds no trace context when tracing is off, and no reconnect flag on a first connection", () => {
    const params = new URLSearchParams({ token: "t" });
    const span = startTerminalSessionSpan(params, { reconnect: false });

    expect(params.has("traceparent")).toBe(false);
    expect(params.has("reconnect")).toBe(false);
    expect(() => span.end(1006, "closed")).not.toThrow();
  });
});
