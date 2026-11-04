/**
 * Trace propagation through the real chain: buildInterceptors → connect-web
 * transport → the HyperDX SDK's patched fetch → the wire.
 *
 * Nothing here is mocked but the network. Telemetry is started the way the app
 * starts it (the runtime's initBrowserTelemetry), so what is asserted is what
 * the backend and the ingest actually see: a valid W3C `traceparent` on the
 * request, and exactly one browser span for the RPC, in the service and with
 * the resource attributes the dashboards and alerts read.
 */

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { trace } from "@opentelemetry/api";
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { initBrowserTelemetry } from "@reliantlabs/forge-web-runtime/telemetry";
import { TRACEPARENT_RE } from "@reliantlabs/forge-web-runtime/interceptors";
import { buildTelemetryConfig } from "@/lib/telemetry";
import { ChatService } from "@/gen/reliant/v1/chat_pb";
import { StreamingService } from "@/gen/reliant/v1/streaming_pb";

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));
vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "https://example.test",
}));

const API = "http://api.example.test";
const CONTROL_PLANE = "http://admin.example.test";
const INGEST = "http://localhost:3000/_otel";

interface Sent {
  url: string;
  traceparent: string | null;
}
interface OtlpSpan {
  name: string;
  traceId: string;
  spanId: string;
  parentSpanId?: string;
  kind: number;
  attributes: Array<{ key: string; value: Record<string, unknown> }>;
}

const sent: Sent[] = [];
const exported: Array<{
  resource: Record<string, string>;
  spans: OtlpSpan[];
}> = [];

function attr(span: OtlpSpan, key: string): unknown {
  const v = span.attributes.find((a) => a.key === key)?.value;
  return v && Object.values(v)[0];
}

// The SDK ships spans with fetch (the unwrapped original, so the stub below
// sees them but the instrumentation does not); decode the OTLP/JSON body.
function captureExport(body: unknown): void {
  const text =
    typeof body === "string"
      ? body
      : body instanceof Uint8Array
        ? new TextDecoder().decode(body)
        : "";
  if (!text) return;
  const doc = JSON.parse(text) as {
    resourceSpans?: Array<{
      resource: { attributes: Array<{ key: string; value: { stringValue?: string } }> };
      scopeSpans: Array<{ spans: OtlpSpan[] }>;
    }>;
  };
  for (const rs of doc.resourceSpans ?? []) {
    exported.push({
      resource: Object.fromEntries(
        rs.resource.attributes.map((a) => [a.key, a.value.stringValue ?? ""]),
      ),
      spans: rs.scopeSpans.flatMap((s) => s.spans),
    });
  }
}

async function flushedSpans(): Promise<OtlpSpan[]> {
  // The fetch span ends after the response body is read, so flush until the
  // batch processor has it.
  const provider = trace.getTracerProvider() as unknown as {
    getDelegate(): { forceFlush(): Promise<void> };
  };
  await vi.waitFor(
    async () => {
      await provider.getDelegate().forceFlush();
      expect(exported.length).toBeGreaterThan(0);
    },
    { timeout: 5000, interval: 100 },
  );
  return exported.flatMap((e) => e.spans);
}

// The exported span for one request, waiting for the batch processor.
async function exportedSpanFor(match: (url: string) => boolean): Promise<OtlpSpan[]> {
  const provider = trace.getTracerProvider() as unknown as {
    getDelegate(): { forceFlush(): Promise<void> };
  };
  let found: OtlpSpan[] = [];
  await vi.waitFor(
    async () => {
      await provider.getDelegate().forceFlush();
      found = exported
        .flatMap((e) => e.spans)
        .filter((s) => match(String(attr(s, "http.url"))));
      expect(found.length).toBeGreaterThan(0);
    },
    { timeout: 5000, interval: 100 },
  );
  return found;
}

describe("an RPC through the full interceptor chain", () => {
  beforeAll(async () => {
    vi.stubGlobal("localStorage", {
      getItem: vi.fn(() => null),
      setItem: vi.fn(),
      removeItem: vi.fn(),
      clear: vi.fn(),
    });
    // The SDK wraps whatever fetch exists when it starts, so the recording
    // "network" goes in first.
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = input instanceof Request ? input : new Request(input, init);
      if (req.url.startsWith(INGEST)) {
        captureExport(await req.text());
        return new Response("{}", { status: 200 });
      }
      sent.push({
        url: req.url,
        traceparent: req.headers.get("traceparent"),
      });
      return new Response("{}", {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as typeof fetch;
    const config = buildTelemetryConfig({
      config: {
        OTEL_ENDPOINT: INGEST,
        API_URL: API,
        CONTROL_PLANE_API_URL: CONTROL_PLANE,
      },
      prerelease: false,
      buildVersion: "1.8.1",
      desktop: false,
      replayRate: 0,
    });
    expect(await initBrowserTelemetry(config)).toBe(true);
  });

  afterAll(() => {
    vi.unstubAllGlobals();
  });

  it("sends a valid traceparent and produces one span with the dashboards' attributes", async () => {
    const { buildInterceptors } = await import("../transport");
    const client = createClient(
      ChatService,
      createConnectTransport({
        baseUrl: API,
        interceptors: buildInterceptors({ withAuth: false }),
      }),
    );

    await client.listChats({}).catch(() => undefined);

    const rpc = sent.filter((s) => s.url.startsWith(API));
    expect(rpc).toHaveLength(1);
    const traceparent = rpc[0].traceparent;
    expect(traceparent).toMatch(TRACEPARENT_RE);
    const [, traceId, spanId] = (traceparent as string).split("-");

    const all = await flushedSpans();
    // reliant no longer opens its own `grpc.<service>/<method>` span.
    expect(all.filter((s) => s.name.startsWith("grpc."))).toEqual([]);

    const spans = all.filter(
      (s) => attr(s, "http.url") === `${API}/${ChatService.typeName}/ListChats`,
    );
    // One RPC, one span: the SDK's fetch span. No second span from reliant.
    expect(spans).toHaveLength(1);
    const span = spans[0];
    expect(span.kind).toBe(3); // CLIENT
    expect(span.parentSpanId ?? "").toBe(""); // the RPC's root span
    expect(attr(span, "component")).toBe("fetch");
    expect(attr(span, "http.method")).toBe("POST");
    expect(Number(attr(span, "http.status_code"))).toBe(200);
    // The header names the span that is exported, so the server span's parent
    // exists in the same trace.
    expect(span.traceId).toBe(traceId);
    expect(span.spanId).toBe(spanId);

    // The dashboards and alerts key on these (reliant-frontend_gen.json).
    const resource = exported[0].resource;
    expect(resource["service.name"]).toBe("reliant-web");
    expect(resource["service.version"]).toBe("1.8.1");
    expect(resource["deployment.environment.name"]).toBe("prod");
    expect(resource["rum.sessionId"]).toBeTruthy();
  });

  // A server stream (StreamChatUpdates, the chat page's long-lived feed) is a
  // fetch like any unary call, and must carry the same header: the server's
  // stream span is otherwise a root no browser span leads to.
  it("sends the SDK span's traceparent on a server-streaming RPC", async () => {
    const { buildInterceptors } = await import("../transport");
    const client = createClient(
      StreamingService,
      createConnectTransport({
        baseUrl: API,
        interceptors: buildInterceptors({ withAuth: false }),
      }),
    );

    const before = sent.length;
    try {
      for await (const _ of client.streamChatUpdates({})) {
        break;
      }
    } catch {
      // The stub's body is not a stream envelope; only the request matters.
    }

    const rpc = sent
      .slice(before)
      .filter((s) => s.url.endsWith(`${StreamingService.typeName}/StreamChatUpdates`));
    expect(rpc).toHaveLength(1);
    expect(rpc[0].traceparent).toMatch(TRACEPARENT_RE);
    const [, traceId, spanId] = (rpc[0].traceparent as string).split("-");
    const spans = await exportedSpanFor((url) => url.endsWith("/StreamChatUpdates"));
    expect(spans.map((s) => [s.traceId, s.spanId])).toContainEqual([traceId, spanId]);
  });

  // The control-plane API (admin.*) is a different origin from the page and
  // the reliant API: its calls must be propagation targets too, or the SDK
  // drops the header and the server roots a trace no browser span joins.
  it("propagates the SDK span to the control-plane API origin", async () => {
    const { buildInterceptors } = await import("../transport");
    const client = createClient(
      ChatService,
      createConnectTransport({
        baseUrl: CONTROL_PLANE,
        interceptors: buildInterceptors({ withAuth: false, backend: "control-plane" }),
      }),
    );

    const before = sent.length;
    await client.listChats({}).catch(() => undefined);

    const rpc = sent.slice(before).filter((s) => s.url.startsWith(CONTROL_PLANE));
    expect(rpc).toHaveLength(1);
    expect(rpc[0].traceparent).toMatch(TRACEPARENT_RE);
    const [, traceId, spanId] = (rpc[0].traceparent as string).split("-");
    const spans = await exportedSpanFor((url) => url.startsWith(CONTROL_PLANE));
    expect(spans.map((s) => [s.traceId, s.spanId])).toContainEqual([traceId, spanId]);
  });
});
