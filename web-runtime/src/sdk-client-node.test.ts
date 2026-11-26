// @vitest-environment jsdom
//
// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Telemetry starts, records and exports under a Node test runner, the way a
// consumer's tests see it. The real HyperDX SDK runs here, not a fake.
//
// A consumer's vitest does not transform this package: it is "type":
// "module", so vitest externalizes it and Node's own ESM loader runs dist/.
// The SDK's ESM build (@hyperdx/otel-web dist/esm) has directory imports that
// loader refuses (ERR_UNSUPPORTED_DIR_IMPORT). So when sdk-client.ts imported
// the SDK by name, initBrowserTelemetry gave up and every span was gone.
// reliant's transport.trace test is the consumer that would have caught it.
// The fix is package.json's "#hyperdx-otel-web" import map. Under the `node`
// condition it resolves to dist/hyperdx-otel-web.node.js, the SDK built for
// the browser platform into one module Node can load.
//
// This test imports the BUILT dist/telemetry.js natively, as such a consumer
// does. Vitest's own transform would resolve the SDK by itself and hide the
// bug. `pretest` builds dist/ first, as published-surface.test.ts relies on.
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";

type Telemetry = typeof import("./telemetry.js");

interface OtlpSpan {
  name: string;
  kind: number;
  attributes: Array<{ key: string; value: Record<string, unknown> }>;
}

const API = "http://api.example.test";
const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");

// Node's own loader, past vitest's module runner. require() of an ES module
// (Node 22.12 and later) runs dist/telemetry.js, and every import() in it, in
// Node, as an externalized package runs.
const nativeRequire = createRequire(import.meta.url);

const sent: string[] = [];
const exported: OtlpSpan[] = [];
let ingest = "";

function attr(span: OtlpSpan, key: string): unknown {
  const v = span.attributes.find((a) => a.key === key)?.value;
  return v && Object.values(v)[0];
}

async function capture(body: unknown): Promise<void> {
  const text =
    typeof body === "string"
      ? body
      : body instanceof Uint8Array
        ? new TextDecoder().decode(body)
        : "";
  if (!text) return;
  const doc = JSON.parse(text) as {
    resourceSpans?: Array<{ scopeSpans: Array<{ spans: OtlpSpan[] }> }>;
  };
  for (const rs of doc.resourceSpans ?? []) {
    exported.push(...rs.scopeSpans.flatMap((s) => s.spans));
  }
}

describe("browser telemetry under a Node test runner", () => {
  const warn = vi.spyOn(console, "warn");

  beforeAll(async () => {
    ingest = `${window.location.origin}/_otel`;
    // The SDK wraps whatever fetch exists when it starts, so the recording
    // network goes in first.
    globalThis.fetch = (async (
      input: RequestInfo | URL,
      init?: RequestInit,
    ) => {
      const req = input instanceof Request ? input : new Request(input, init);
      if (req.url.startsWith(ingest)) {
        await capture(await req.text());
      } else {
        sent.push(req.url);
      }
      return new Response("{}", {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as typeof fetch;
    const t = nativeRequire(join(pkgDir, "dist", "telemetry.js")) as Telemetry;
    const running = await t.initBrowserTelemetry({
      serviceName: "web-runtime-node-test",
      endpoint: "/_otel",
      apiOrigins: [API],
      start: "eager",
      clickCapture: false,
      frameStalls: false,
    });
    expect(
      warn.mock.calls.filter((c) => String(c[0]).startsWith("[telemetry]")),
      "telemetry logged a failed start",
    ).toEqual([]);
    expect(running).toBe(true);
  }, 30_000);

  afterAll(() => {
    warn.mockRestore();
  });

  it("records a fetch span and exports it", { timeout: 20_000 }, async () => {
    await fetch(`${API}/ping`, { method: "POST" });
    expect(sent).toEqual([`${API}/ping`]);

    const { trace } = await import("@opentelemetry/api");
    const provider = trace.getTracerProvider() as unknown as {
      getDelegate(): { forceFlush(): Promise<void> };
    };
    // The SDK ends a fetch span once the response's resource timing is in,
    // some 300 ms after the response, so wait for this one.
    await vi.waitFor(
      async () => {
        await provider.getDelegate().forceFlush();
        expect(
          exported.filter((s) => attr(s, "http.url") === `${API}/ping`),
        ).toHaveLength(1);
      },
      { timeout: 10_000, interval: 100 },
    );
    const span = exported.find((s) => attr(s, "http.url") === `${API}/ping`)!;
    expect(span.kind).toBe(3); // CLIENT
    expect(attr(span, "http.method")).toBe("POST");
  });

  it("leaves console calls working with console capture on", () => {
    // Console capture is on by default, so the SDK wraps console.log. Its
    // CommonJS build, the obvious other way to load it under Node, threw
    // "(0, isObject_1.default) is not a function" from every console call.
    expect(() => console.log("a message", { with: "an object" })).not.toThrow();
  });
});
