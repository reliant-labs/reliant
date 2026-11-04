// Slow, opt-in real Chromium/HyperDX contract proof: npm run test:browser.
// No live collector, credentials or deployed service; both HTTP origins bind
// ephemeral loopback ports. The normal vitest tier remains browser-free.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { gunzipSync } from "node:zlib";
import { chromium } from "playwright";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(import.meta.url);
const apiDir = resolve(dirname(require.resolve("@opentelemetry/api")), "../..");
const protoDir = resolve(dirname(require.resolve("@bufbuild/protobuf")), "../..");
const sdk = require.resolve("@hyperdx/browser");
const recorderRequire = createRequire(require.resolve("@hyperdx/otel-web-session-recorder"));
// Decode the OTLP log envelope, not a mocked replay sink. protobufjs is the
// recorder's own dependency. Only fields used by this proof are declared.
const LogsData = recorderRequire("protobufjs").parse(`
  syntax = "proto3";
  message AnyValue { oneof value { string stringValue = 1; bool boolValue = 2; int64 intValue = 3; double doubleValue = 4; ArrayValue arrayValue = 5; KeyValueList kvlistValue = 6; } }
  message ArrayValue { repeated AnyValue values = 1; }
  message KeyValue { string key = 1; AnyValue value = 2; }
  message KeyValueList { repeated KeyValue values = 1; }
  message Scope { string name = 1; }
  message LogRecord { AnyValue body = 5; }
  message ScopeLogs { Scope scope = 1; repeated LogRecord logRecords = 2; }
  message ResourceLogs { repeated ScopeLogs scopeLogs = 2; }
  message LogsData { repeated ResourceLogs resourceLogs = 1; }
`).root.lookupType("LogsData");
const listen = async (server) => {
  await new Promise((done) => server.listen(0, "127.0.0.1", done));
  return `http://127.0.0.1:${server.address().port}`;
};
const close = (server) => new Promise((done) => {
  server.closeAllConnections();
  server.close(done);
});

// URLs select only files in these package roots, never the host filesystem.
async function moduleFile(url) {
  const routes = {
    "/runtime/": join(root, "dist"),
    "/otel/": join(apiDir, "build/esm"),
    "/proto/": join(protoDir, "dist/esm"),
    "/connect/": resolve(dirname(require.resolve("@connectrpc/connect")), "../esm"),
  };
  for (const [prefix, base] of Object.entries(routes)) {
    if (!url.startsWith(prefix)) continue;
    const path = resolve(base, decodeURIComponent(url.slice(prefix.length)));
    if (!path.startsWith(`${base}/`)) throw new Error("invalid module path");
    return readFile(path.endsWith(".js") ? path : `${path}.js`);
  }
  throw new Error("unknown module");
}

test("raw interactions stay in masked replay; explicit action and RPC share real span parents", { timeout: 30000 }, async () => {
  const traces = [];
  const logs = [];
  const requests = [];
  const backend = createServer((req, res) => {
    res.setHeader("Access-Control-Allow-Origin", "*");
    res.setHeader("Access-Control-Allow-Headers", "traceparent,tracestate,content-type");
    if (req.method !== "OPTIONS") requests.push(req.headers.traceparent);
    res.end("{}");
  });
  const backendOrigin = await listen(backend);
  const server = createServer(async (req, res) => {
    try {
      const url = new URL(req.url, "http://localhost").pathname;
      if (url.startsWith("/_otel/")) {
        const chunks = [];
        for await (const chunk of req) chunks.push(chunk);
        const raw = Buffer.concat(chunks);
        const decoded = req.headers["content-encoding"] === "gzip" ? gunzipSync(raw) : raw;
        const payload = url.endsWith("logs") ? LogsData.decode(decoded) : JSON.parse(decoded.toString());
        (url.endsWith("traces") ? traces : logs).push(payload);
        res.setHeader("content-type", "application/json");
        res.end("{}");
      } else if (url === "/sdk.js") {
        res.setHeader("content-type", "text/javascript");
        res.end(await readFile(sdk));
      } else if (url === "/hyperdx.js") {
        res.setHeader("content-type", "text/javascript");
        res.end("export default globalThis.HyperDX;");
      } else if (url === "/") {
        res.setHeader("content-type", "text/html");
        res.end(`<!doctype html><p>PRIVATE_TEXT_SENTINEL</p><input value="PRIVATE_INPUT_SENTINEL"><button id="idle">Idle</button><button id="action">Run</button>
<script src="/sdk.js"></script>
<script type="importmap">{"imports":{"@hyperdx/browser":"/hyperdx.js","@opentelemetry/api":"/otel/index.js","@bufbuild/protobuf/wkt":"/proto/wkt/index.js","@bufbuild/protobuf":"/proto/index.js","@bufbuild/protobuf/wire":"/proto/wire/index.js","@bufbuild/protobuf/reflect":"/proto/reflect/index.js","@bufbuild/protobuf/codegenv2":"/proto/codegenv2/index.js","@connectrpc/connect":"/connect/index.js"}}</script>
<script type="module">
import { initBrowserTelemetry, context, trace } from '/runtime/telemetry.js';
import { buildRuntimeInterceptors } from '/runtime/interceptors.js';
window.ready = await initBrowserTelemetry({ serviceName: 'browser-proof', endpoint: '/_otel', replay: true, apiOrigins: [${JSON.stringify(backendOrigin)}], consoleCapture: false });
window.tracing = trace;
for (const type of ['mousedown','mouseup','click']) document.querySelector('#idle').addEventListener(type, () => {});
document.querySelector('#action').addEventListener('click', () => {
  const action = trace.getTracer('meaningful-action').startSpan('save-record');
  context.with(trace.setSpan(context.active(), action), () => {
    const interceptors = buildRuntimeInterceptors({ getToken: async () => null });
    const call = interceptors.reduceRight((next, interceptor) => interceptor(next), async req => {
      await fetch(${JSON.stringify(`${backendOrigin}/svc/Save`)}, { method: 'POST', headers: req.header });
    });
    window.finished = call({ header: new Headers(), stream: false }).finally(() => action.end());
  });
});
</script>`);
      } else {
        res.setHeader("content-type", "text/javascript");
        res.end(await moduleFile(url));
      }
    } catch (error) {
      res.statusCode = 500;
      res.end(String(error));
    }
  });
  const origin = await listen(server);
  let browser;
  try {
    browser = await chromium.launch({ headless: true });
    const page = await browser.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(origin);
    await page.waitForFunction(() => window.ready === true, undefined, { timeout: 5000 }).catch((error) => {
      throw new Error(`${error.message}; browser errors: ${errors.join("; ")}`);
    });
    await page.locator("#idle").click();
    await page.locator("input").fill("PRIVATE_EDIT_SENTINEL");
    await page.locator("#action").click();
    await page.evaluate(() => window.finished);
    await page.waitForFunction(() => {
      // Fetch spans end after resource-timing collection, not at response.
      // Observe the real SDK batch queue rather than a fixed sleep.
      const provider = window.tracing.getTracerProvider().getDelegate();
      return provider._activeSpanProcessor._spanProcessors.some(processor =>
        processor._finishedSpans?.some(span => span.name === 'HTTP POST'));
    }, undefined, { timeout: 5000 });
    await page.evaluate(async () => {
      // Both are the REAL SDK's providers, not a test substitute.
      await window.tracing.getTracerProvider().getDelegate().forceFlush();
      HyperDX.stopSessionRecorder();
    });
    // Let the real recorder deliver its batch, independently of trace export.
    const replayResponse = page.waitForResponse(response => response.url().endsWith('/_otel/v1/logs') && response.ok(), { timeout: 10000 });
    // Wait on actual replay export (the SDK batches every 5s), not a sleep
    // or an unload handler modern browsers are allowed to suppress.
    await replayResponse;
    const spans = traces.flatMap((batch) => batch.resourceSpans ?? [])
      .flatMap((resource) => resource.scopeSpans ?? [])
      .flatMap((scope) => scope.spans ?? []);
    assert.deepEqual(errors, []);
    assert.equal(spans.some((span) => ["mousedown", "mouseup", "click"].includes(span.name)), false);
    const action = spans.find((span) => span.name === "save-record");
    const fetch = spans.find((span) => span.name === "HTTP POST");
    assert.ok(action, "explicitly instrumented meaningful action is exported");
    assert.ok(fetch, `network span is still exported: ${JSON.stringify(spans.map(s => s.name))}`);
    assert.equal(fetch.traceId, action.traceId);
    assert.equal(fetch.parentSpanId, action.spanId, "fetch is a child, not just the same trace");
    assert.deepEqual(requests, [`00-${fetch.traceId}-${fetch.spanId}-01`], "wire parent is the exported fetch span, not the interceptor's synthetic fallback");
    assert.ok(spans.some((span) => span.name === "documentLoad"), "document-load tracing remains enabled");
    const replay = logs.flatMap((batch) => batch.resourceLogs ?? [])
      .flatMap((resource) => resource.scopeLogs ?? [])
      .filter((scope) => scope.scope?.name === "rum.rr-web")
      .flatMap((scope) => scope.logRecords ?? []);
    assert.ok(replay.length > 0, "session replay still exports records");
    const events = replay.map(record => JSON.parse(record.body.stringValue));
    assert.ok(events.some(event => event.type === 3 && event.data.source === 2), "raw mouse interactions remain replay events");
    const exported = JSON.stringify([...traces, ...logs]);
    for (const secret of ["PRIVATE_TEXT_SENTINEL", "PRIVATE_INPUT_SENTINEL", "PRIVATE_EDIT_SENTINEL"]) {
      assert.equal(exported.includes(secret), false, "private text never leaves the browser");
    }
  } finally {
    await browser?.close();
    await close(server);
    await close(backend);
  }
});

// The benign-error filter (src/benign-errors.ts) acts on what the REAL SDK
// records (its `errors` tracer's spans), never on the events the app sees:
// browser listener phases and the SDK's internals are what this depends on, so
// only a real browser and the real SDK prove it.
test("benign browser errors are counted, not recorded, and every app handler still sees them; real ones and ConnectErrors are recorded", { timeout: 30000 }, async () => {
  const traces = [];
  const server = createServer(async (req, res) => {
    try {
      const url = new URL(req.url, "http://localhost").pathname;
      if (url.startsWith("/_otel/")) {
        const chunks = [];
        for await (const chunk of req) chunks.push(chunk);
        const raw = Buffer.concat(chunks);
        const decoded = req.headers["content-encoding"] === "gzip" ? gunzipSync(raw) : raw;
        if (url.endsWith("traces")) traces.push(JSON.parse(decoded.toString()));
        res.setHeader("content-type", "application/json");
        res.end("{}");
      } else if (url === "/sdk.js") {
        res.setHeader("content-type", "text/javascript");
        res.end(await readFile(sdk));
      } else if (url === "/hyperdx.js") {
        res.setHeader("content-type", "text/javascript");
        res.end("export default globalThis.HyperDX;");
      } else if (url === "/") {
        res.setHeader("content-type", "text/html");
        res.end(`<!doctype html>
<script>
// The app's own error handling, every flavour, BEFORE telemetry starts.
window.seen = [];
const what = (v) => (v && v.name ? v.name + ":" + v.message : String(v));
window.onerror = (msg, src, line, col, err) => { window.seen.push("before/onerror " + what(err)); };
window.onunhandledrejection = (e) => { window.seen.push("before/onunhandledrejection " + what(e.reason)); };
window.addEventListener("error", (e) => window.seen.push("before/error " + what(e.error)));
window.addEventListener("unhandledrejection", (e) => window.seen.push("before/rejection " + what(e.reason)));
window.addEventListener("unhandledrejection", (e) => window.seen.push("before/rejection-capture " + what(e.reason)), true);
</script>
<script src="/sdk.js"></script>
<script type="importmap">{"imports":{"@hyperdx/browser":"/hyperdx.js","@opentelemetry/api":"/otel/index.js"}}</script>
<script type="module">
import { initBrowserTelemetry, trace } from '/runtime/telemetry.js';
window.tracing = trace;
window.ready = await initBrowserTelemetry({ serviceName: 'browser-proof', endpoint: '/_otel', consoleCapture: false });
// ...and AFTER it has started.
const what = (v) => (v && v.name ? v.name + ":" + v.message : String(v));
window.addEventListener("error", (e) => window.seen.push("after/error " + what(e.error)));
window.addEventListener("unhandledrejection", (e) => window.seen.push("after/rejection " + what(e.reason)));
window.addEventListener("unhandledrejection", (e) => window.seen.push("after/rejection-capture " + what(e.reason)), true);
</script>`);
      } else {
        res.setHeader("content-type", "text/javascript");
        res.end(await moduleFile(url));
      }
    } catch (error) {
      res.statusCode = 500;
      res.end(String(error));
    }
  });
  const origin = await listen(server);
  let browser;
  try {
    browser = await chromium.launch({ headless: true });
    const page = await browser.newPage();
    const consoleErrors = [];
    page.on("console", (msg) => { if (msg.type() === "error") consoleErrors.push(msg.text()); });
    await page.goto(origin);
    await page.waitForFunction(() => window.ready === true, undefined, { timeout: 5000 });
    await page.evaluate(async () => {
      // Unawaited, so each reaches window "unhandledrejection".
      Promise.reject(new DOMException("signal is aborted without reason", "AbortError"));
      Promise.reject(new TypeError("Failed to fetch"));
      const rpc = new Error("[unavailable] Failed to fetch");
      rpc.name = "ConnectError";
      Promise.reject(rpc);
      Promise.reject(new Error("REAL_REJECTION"));
      // Uncaught, so it reaches window "error".
      setTimeout(() => { throw new TypeError("Load failed"); });
      console.error("auth:", new TypeError("Failed to fetch (dash.example.io)"));
      console.error(new TypeError("REAL_CONSOLE_TYPEERROR"));
      // A DOMException is an exception whatever its prototype chain says.
      console.error(new DOMException("signal is aborted without reason", "AbortError"));
      console.error(new TypeError("Failed to fetch"), new DOMException("REAL_DOMEXCEPTION", "NotAllowedError"));
      await new Promise((done) => setTimeout(done, 100));
      window.dispatchEvent(new Event("pagehide"));
      await window.tracing.getTracerProvider().getDelegate().forceFlush();
    });

    // Every app handler, of every flavour, before and after telemetry started,
    // saw every benign failure: telemetry changed nothing the app observes.
    const seen = await page.evaluate(() => window.seen);
    for (const handler of ["before/onunhandledrejection", "before/rejection", "before/rejection-capture", "after/rejection", "after/rejection-capture"]) {
      for (const value of ["AbortError:signal is aborted without reason", "TypeError:Failed to fetch"]) {
        assert.ok(seen.includes(`${handler} ${value}`), `${handler} saw ${value}: ${JSON.stringify(seen)}`);
      }
    }
    for (const handler of ["before/onerror", "before/error", "after/error"]) {
      assert.ok(seen.includes(`${handler} TypeError:Load failed`), `${handler} saw the uncaught Load failed: ${JSON.stringify(seen)}`);
    }
    // And the console still printed the benign console.error calls.
    assert.ok(consoleErrors.some((t) => t.includes("dash.example.io")), "a filtered console.error still reaches the console");

    const spans = traces.flatMap((batch) => batch.resourceSpans ?? [])
      .flatMap((resource) => resource.scopeSpans ?? [])
      .flatMap((scope) => scope.spans ?? []);
    const attr = (span, key) => span.attributes?.find((a) => a.key === key)?.value;
    const errorSpans = spans.filter((span) => ["unhandledrejection", "console.error", "onerror"].includes(span.name));
    const messages = errorSpans.map((span) => attr(span, "error.message")?.stringValue);
    assert.ok(messages.includes("REAL_REJECTION"), `a real rejection is recorded: ${JSON.stringify(messages)}`);
    assert.ok(messages.includes("REAL_CONSOLE_TYPEERROR"), "a real console.error TypeError is recorded");
    assert.ok(messages.includes("[unavailable] Failed to fetch"), "a ConnectError is never filtered");
    assert.ok(messages.some((m) => m?.includes("REAL_DOMEXCEPTION")), `a real DOMException beside a fetch failure keeps the call: ${JSON.stringify(messages)}`);
    assert.equal(messages.some((m) => m?.includes("signal is aborted")), false, `no AbortError is recorded: ${JSON.stringify(messages)}`);
    assert.equal(messages.some((m) => m === "Failed to fetch" || m === "Load failed" || m?.includes("dash.example.io")), false, `no generic fetch failure is recorded: ${JSON.stringify(messages)}`);
    const counts = spans.filter((span) => span.name === "forge.errors_filtered")
      .map((span) => `${attr(span, "kind")?.stringValue}/${attr(span, "source")?.stringValue}=${attr(span, "count")?.intValue ?? attr(span, "count")?.doubleValue}`)
      .sort();
    assert.deepEqual(counts, [
      "abort/console.error=1",
      "abort/unhandledrejection=1",
      "network/console.error=1",
      "network/error=1",
      "network/unhandledrejection=1",
    ], "every filtered failure is counted, by kind and source");
  } finally {
    await browser?.close();
    await close(server);
  }
});
