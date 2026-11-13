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

import { createOtelProxyHandler } from "../dist/otel-proxy.js";

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
// Poll until `condition()` holds; fails with `what` after `ms`.
const until = async (condition, ms, what) => {
  const deadline = Date.now() + ms;
  while (!condition()) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((done) => setTimeout(done, 100));
  }
};
// The rrweb records in decoded OTLP log batches.
const replayRecords = (logs) => logs.flatMap((batch) => batch.resourceLogs ?? [])
  .flatMap((resource) => resource.scopeLogs ?? [])
  .filter((scope) => scope.scope?.name === "rum.rr-web")
  .flatMap((scope) => scope.logRecords ?? []);

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
  // The page's /_otel goes through the runtime's real dev proxy (the one
  // forgeOtelProxy mounts), to a collector behind it. This proves the real
  // SDK's exports pass the proxy's same-origin caller check: the browser
  // sends Origin and Sec-Fetch-Site: same-origin on every export.
  const callers = [];
  const collector = createServer(async (req, res) => {
    const url = req.url;
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const raw = Buffer.concat(chunks);
    const decoded = req.headers["content-encoding"] === "gzip" ? gunzipSync(raw) : raw;
    const payload = url.endsWith("logs") ? LogsData.decode(decoded) : JSON.parse(decoded.toString());
    (url.endsWith("traces") ? traces : logs).push(payload);
    res.setHeader("content-type", "application/json");
    res.end("{}");
  });
  const proxy = createOtelProxyHandler({ upstream: await listen(collector), headersFile: "" });
  const backend = createServer((req, res) => {
    res.setHeader("Access-Control-Allow-Origin", "*");
    res.setHeader("Access-Control-Allow-Headers", "traceparent,tracestate,baggage,content-type");
    if (req.method !== "OPTIONS") requests.push(req.headers.traceparent);
    res.end("{}");
  });
  const backendOrigin = await listen(backend);
  const server = createServer(async (req, res) => {
    try {
      const url = new URL(req.url, "http://localhost").pathname;
      if (url.startsWith("/_otel/")) {
        callers.push({ host: req.headers.host, origin: req.headers.origin, site: req.headers["sec-fetch-site"] });
        proxy(req, res, req.url.slice("/_otel".length));
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
import { disableTelemetry, grantAnalyticsConsent } from '/runtime/track.js';
window.withdrawConsent = disableTelemetry;
import { buildRuntimeInterceptors } from '/runtime/interceptors.js';
// Replay records a person: it runs only in the full analytics tier.
grantAnalyticsConsent();
window.ready = await initBrowserTelemetry({ serviceName: 'browser-proof', endpoint: '/_otel', analytics: 'full', replay: true, apiOrigins: [${JSON.stringify(backendOrigin)}], consoleCapture: false });
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
      // The REAL SDK's provider, not a test substitute.
      await window.tracing.getTracerProvider().getDelegate().forceFlush();
    });
    // Let the real recorder deliver the clicks, independently of trace
    // export: wait on its actual export (it batches every 5s), not a sleep or
    // an unload handler modern browsers are allowed to suppress. Withdrawal
    // drops whatever it still holds, so this must arrive before it.
    await until(() => replayRecords(logs).some((record) => {
      const event = JSON.parse(record.body.stringValue);
      return event.type === 3 && event.data.source === 2;
    }), 15000, "a replay batch carrying the clicks");
    await page.evaluate(() => window.withdrawConsent());
    const logsAtWithdrawal = logs.length;
    const spans = traces.flatMap((batch) => batch.resourceSpans ?? [])
      .flatMap((resource) => resource.scopeSpans ?? [])
      .flatMap((scope) => scope.spans ?? []);
    assert.deepEqual(errors, []);
    assert.ok(callers.length >= 2, "traces and replay logs both went through the proxy");
    for (const caller of callers) {
      assert.deepEqual(caller, { host: new URL(origin).host, origin, site: "same-origin" }, "the SDK's exports are same-origin to the proxy");
    }
    assert.equal(spans.some((span) => ["mousedown", "mouseup", "click"].includes(span.name)), false);
    const action = spans.find((span) => span.name === "save-record");
    const fetch = spans.find((span) => span.name === "HTTP POST");
    assert.ok(action, "explicitly instrumented meaningful action is exported");
    assert.ok(fetch, `network span is still exported: ${JSON.stringify(spans.map(s => s.name))}`);
    assert.equal(fetch.traceId, action.traceId);
    assert.equal(fetch.parentSpanId, action.spanId, "fetch is a child, not just the same trace");
    assert.deepEqual(requests, [`00-${fetch.traceId}-${fetch.spanId}-01`], "wire parent is the exported fetch span, not the interceptor's synthetic fallback");
    assert.ok(spans.some((span) => span.name === "documentLoad"), "document-load tracing remains enabled");
    const replay = replayRecords(logs);
    assert.ok(replay.length > 0, "session replay still exports records");
    const events = replay.map(record => JSON.parse(record.body.stringValue));
    assert.ok(events.some(event => event.type === 3 && event.data.source === 2), "raw mouse interactions remain replay events");
    // Withdrawal goes through the public runtime API, not an SDK-only test
    // substitute. No post-withdrawal DOM mutation becomes a replay event, and
    // nothing at all is posted after it (the dedicated test below covers
    // what was buffered before it).
    await page.evaluate(() => {
      const marker = document.createElement('div');
      marker.id = 'after-consent-withdrawn';
      document.body.append(marker);
    });
    await new Promise(resolve => setTimeout(resolve, 5500));
    assert.equal(JSON.stringify(logs).includes('after-consent-withdrawn'), false,
      'consent withdrawal stops the real recorder');
    assert.equal(logs.length, logsAtWithdrawal, "no replay batch is posted after consent is withdrawn");
    const exported = JSON.stringify([...traces, ...logs]);
    for (const secret of ["PRIVATE_TEXT_SENTINEL", "PRIVATE_INPUT_SENTINEL", "PRIVATE_EDIT_SENTINEL"]) {
      assert.equal(exported.includes(secret), false, "private text never leaves the browser");
    }
  } finally {
    await browser?.close();
    await close(server);
    await close(backend);
    await close(collector);
  }
});

// Withdrawal means stop now (telemetry.ts, "Withdrawal stops replay now"),
// proven against the real recorder: it buffers records for 5s and still posts
// them once stopped, and a second stop() starts a `record stop` span on its
// `rum.rr-web` tracer. After the withdrawal no replay batch is posted, not even
// one holding what was recorded before it, and no replay span is exported, even
// when the SDK's recorder is stopped again directly.
for (const withdraw of ["disableTelemetry", "revokeAnalyticsConsent"]) {
test(`${withdraw}(): no replay record or replay span leaves the page after it, not even one recorded before it`, { timeout: 45000 }, async () => {
  const traces = [];
  const logs = [];
  const server = createServer(async (req, res) => {
    try {
      const url = new URL(req.url, "http://localhost").pathname;
      if (url.startsWith("/_otel/")) {
        const chunks = [];
        for await (const chunk of req) chunks.push(chunk);
        const raw = Buffer.concat(chunks);
        const decoded = req.headers["content-encoding"] === "gzip" ? gunzipSync(raw) : raw;
        if (url.endsWith("logs")) logs.push(LogsData.decode(decoded));
        else traces.push(JSON.parse(decoded.toString()));
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
        res.end(`<!doctype html><p>PRIVATE_TEXT_SENTINEL</p>
<script src="/sdk.js"></script>
<script type="importmap">{"imports":{"@hyperdx/browser":"/hyperdx.js","@opentelemetry/api":"/otel/index.js"}}</script>
<script type="module">
import { initBrowserTelemetry, trace } from '/runtime/telemetry.js';
import { disableTelemetry, grantAnalyticsConsent, revokeAnalyticsConsent } from '/runtime/track.js';
window.tracing = trace;
window.withdraw = { disableTelemetry, revokeAnalyticsConsent }[${JSON.stringify(withdraw)}];
// Replay records a person: it runs only in the full analytics tier.
grantAnalyticsConsent();
window.ready = await initBrowserTelemetry({ serviceName: 'browser-proof', endpoint: '/_otel', analytics: 'full', replay: true, consoleCapture: false });
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
    await page.waitForFunction(() => window.ready === true, undefined, { timeout: 5000 });
    // Replay runs while consented: the recorder's first batch (the page's
    // snapshot) is posted. Right after a batch went, the next is due 5s after
    // the recorder's next record, so the record below stays buffered.
    await until(() => replayRecords(logs).length > 0, 15000, "the first replay batch");
    await page.evaluate(async () => {
      const marker = document.createElement("p");
      marker.id = "recorded-before-withdrawal";
      document.body.append(marker);
      // The recorder sees the mutation once the observer runs.
      await new Promise((done) => setTimeout(done, 200));
    });
    const logsAtWithdrawal = logs.length;
    await page.evaluate(async () => {
      // Every span made before the withdrawal leaves before it.
      await window.tracing.getTracerProvider().getDelegate().forceFlush();
      window.withdraw();
      // Anything else that stops the recorder again: the app, or the SDK.
      globalThis.HyperDX.stopSessionRecorder();
      const marker = document.createElement("p");
      marker.id = "recorded-after-withdrawal";
      document.body.append(marker);
    });
    const tracesAtWithdrawal = traces.length;
    // Past the recorder's 5s batch timer, then everything still queued leaves:
    // the trace provider is flushed and the page unloads (the recorder flushes
    // on unload too).
    await new Promise((done) => setTimeout(done, 6000));
    await page.evaluate(async () => {
      await window.tracing.getTracerProvider().getDelegate().forceFlush();
    });
    await page.goto("about:blank");
    await new Promise((done) => setTimeout(done, 500));

    assert.deepEqual(errors, []);
    assert.equal(logs.length, logsAtWithdrawal, "no replay batch is posted after the withdrawal");
    const posted = JSON.stringify(logs);
    assert.equal(posted.includes("recorded-before-withdrawal"), false, "what the recorder buffered before the withdrawal is dropped, not sent");
    assert.equal(posted.includes("recorded-after-withdrawal"), false, "nothing is recorded after the withdrawal");
    const replaySpans = (batches) => batches.flatMap((batch) => batch.resourceSpans ?? [])
      .flatMap((resource) => resource.scopeSpans ?? [])
      .filter((scope) => scope.scope?.name === "rum.rr-web")
      .flatMap((scope) => scope.spans ?? []);
    assert.ok(replaySpans(traces.slice(0, tracesAtWithdrawal)).some((span) => span.name === "record init"), "the recorder's own spans are exported while it runs");
    assert.deepEqual(replaySpans(traces.slice(tracesAtWithdrawal)).map((span) => span.name), [], "no replay span is exported after the withdrawal");
    assert.equal(JSON.stringify([...traces, ...logs]).includes("PRIVATE_TEXT_SENTINEL"), false, "private text never leaves the browser");
  } finally {
    await browser?.close();
    await close(server);
  }
});
}

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

// The viewport and screen (src/client-context.ts) go through the SDK's
// setGlobalAttributes, whose span processor stamps the current set on every
// span as it STARTS. Only the real SDK in a real browser proves that contract:
// a span before a resize carries the old size, a span after it the new one.
test("every span carries the viewport and screen current when it started, restamped after a resize", { timeout: 30000 }, async () => {
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
<script src="/sdk.js"></script>
<script type="importmap">{"imports":{"@hyperdx/browser":"/hyperdx.js","@opentelemetry/api":"/otel/index.js"}}</script>
<script type="module">
import { initBrowserTelemetry, trace } from '/runtime/telemetry.js';
window.tracing = trace;
window.ready = await initBrowserTelemetry({ serviceName: 'browser-proof', endpoint: '/_otel', consoleCapture: false });
window.mark = (name) => trace.getTracer('viewport-proof').startSpan(name).end();
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
    const page = await browser.newPage({ viewport: { width: 800, height: 600 } });
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(origin);
    await page.waitForFunction(() => window.ready === true, undefined, { timeout: 5000 });
    await page.evaluate(() => window.mark("before-resize"));
    await page.setViewportSize({ width: 1024, height: 700 });
    // The resize is debounced (VIEWPORT_DEBOUNCE_MS): observe the SDK's own
    // global attributes rather than sleeping a fixed time.
    await page.waitForFunction(() => {
      const provider = window.tracing.getTracerProvider().getDelegate();
      return provider._activeSpanProcessor._spanProcessors.some((processor) =>
        processor.getGlobalAttributes?.()["app.viewport.width"] === "1024");
    }, undefined, { timeout: 5000 });
    await page.evaluate(async () => {
      window.mark("after-resize");
      await window.tracing.getTracerProvider().getDelegate().forceFlush();
    });

    assert.deepEqual(errors, []);
    const spans = traces.flatMap((batch) => batch.resourceSpans ?? [])
      .flatMap((resource) => resource.scopeSpans ?? [])
      .flatMap((scope) => scope.spans ?? []);
    const size = (name) => {
      const span = spans.find((s) => s.name === name);
      assert.ok(span, `${name} is exported: ${JSON.stringify(spans.map((s) => s.name))}`);
      const attr = (key) => span.attributes?.find((a) => a.key === key)?.value?.stringValue;
      return {
        viewport: `${attr("app.viewport.width")}x${attr("app.viewport.height")}`,
        screen: attr("app.screen.width") !== undefined && attr("app.screen.height") !== undefined,
      };
    };
    assert.deepEqual(size("before-resize"), { viewport: "800x600", screen: true });
    assert.deepEqual(size("after-resize"), { viewport: "1024x700", screen: true });
  } finally {
    await browser?.close();
    await close(server);
  }
});

// The cookieless analytics modes (identified, the default, and aggregate)
// store NOTHING on the device, proven with the real SDK in a real browser: after every emitter the runtime has has run,
// the cookie jar, localStorage and sessionStorage are empty, and the product
// events still went out, on one session, with no persistent id.
for (const [mode, tier] of [[undefined, "identified"], ["aggregate", "aggregate"]]) {
test(`analytics mode ${mode ?? "unset (identified)"} stores nothing on the device and still measures`, { timeout: 30000 }, async () => {
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
<script src="/sdk.js"></script>
<script type="importmap">{"imports":{"@hyperdx/browser":"/hyperdx.js","@opentelemetry/api":"/otel/index.js"}}</script>
<script type="module">
import { initBrowserTelemetry, trace } from '/runtime/telemetry.js';
import { analyticsTier, track } from '/runtime/track.js';
import { identifyUser, trackAuthEvent } from '/runtime/auth-events.js';
import { trackExposure } from '/runtime/exposure.js';
window.tracing = trace;
const mode = new URLSearchParams(location.search).get('mode') || undefined;
window.ready = await initBrowserTelemetry({ serviceName: 'browser-proof', endpoint: '/_otel', consoleCapture: false, pageViews: true, replay: true, analytics: mode });
track('checkout_started', { plan: 'pro' });
history.pushState({}, '', '/orders/17');
identifyUser('u-1', { provider: 'clerk' });
trackAuthEvent('signed_in', { userId: 'u-1', method: 'password' });
trackExposure('new-checkout', 'treatment');
window.tier = analyticsTier();
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
    const context = await browser.newContext();
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`${origin}/?utm_source=news&gclid=SECRET-CLICK-ID${mode ? `&mode=${mode}` : ""}`);
    await page.waitForFunction((want) => window.ready === true && window.tier === want, tier, { timeout: 5000 });
    await page.evaluate(async () => {
      await window.tracing.getTracerProvider().getDelegate().forceFlush();
    });
    const device = await page.evaluate(() => ({
      localStorage: localStorage.length,
      sessionStorage: sessionStorage.length,
    }));

    assert.deepEqual(errors, []);
    assert.deepEqual(await context.cookies(), [], "no cookie in the browser's jar");
    assert.deepEqual(device, { localStorage: 0, sessionStorage: 0 }, "no web storage");

    const spans = traces.flatMap((batch) => batch.resourceSpans ?? [])
      .flatMap((resource) => resource.scopeSpans ?? [])
      .flatMap((scope) => scope.spans ?? []);
    const attrs = (span) => Object.fromEntries((span.attributes ?? []).map((a) => [a.key, a.value?.stringValue ?? a.value?.boolValue]));
    const actions = spans.map((s) => ({ name: s.name, attrs: attrs(s) }));
    assert.equal(actions.some((a) => a.attrs["visitor.id"]), false, "no persistent visitor id");
    const views = actions.filter((a) => a.name === "page_viewed");
    assert.equal(views.length, 2, `two page views: ${JSON.stringify(actions.map((a) => a.name))}`);
    const entry = views.find((v) => v.attrs.entry === "true");
    assert.ok(entry, "the first view is the entry");
    assert.equal(entry.attrs.utm_source, "news");
    assert.equal(entry.attrs.click_id, "google");
    for (const name of ["checkout_started", "flag_evaluated", "auth_signed_in"]) {
      assert.ok(actions.some((a) => a.name === name), `${name} is exported`);
    }
    // The SDK stamps its session on the resource of every batch.
    const sessions = new Set(traces.flatMap((batch) => batch.resourceSpans ?? [])
      .map((r) => r.resource?.attributes?.find((a) => a.key === "rum.sessionId")?.value?.stringValue)
      .filter(Boolean));
    assert.equal(sessions.size, 1, `one in-memory session: ${[...sessions]}`);
    // The product events never carry the click id. (The SDK's own
    // location.href span attribute is the page URL, query included; the
    // ingest side scrubs it, see "Network URLs" in telemetry.ts.)
    const productAttrs = actions
      .filter((a) => ["page_viewed", "checkout_started", "flag_evaluated", "auth_signed_in"].includes(a.name))
      .map((a) => Object.fromEntries(Object.entries(a.attrs).filter(([k]) => k !== "location.href")));
    assert.equal(JSON.stringify(productAttrs).includes("SECRET-CLICK-ID"), false, "the click id is never a product-event prop");
    // identified carries the signed-in subject; aggregate never does.
    const users = new Set(actions.map((a) => a.attrs.userId).filter(Boolean));
    assert.deepEqual([...users], tier === "identified" ? ["u-1"] : []);
  } finally {
    await browser?.close();
    await close(server);
  }
});
}

// interaction(): RPCs made while it is open — after the click handler has
// returned, with no span active, as an effect or a stream hook makes them —
// are children of the interaction in the REAL SDK's export. The two terminal
// `next`s are connect-web's two shapes: unary calls fetch() synchronously
// inside next(); a server stream awaits its request body first, which a stack
// context manager does not survive, so only the runtime's fetch shim carries
// the interaction to the SDK's fetch span.
test("interaction(): unary and stream fetch spans made after the click are the children of the interaction they started under, even when a newer one opens while auth waits", { timeout: 30000 }, async () => {
  const traces = [];
  const requests = [];
  const backend = createServer((req, res) => {
    res.setHeader("Access-Control-Allow-Origin", "*");
    res.setHeader("Access-Control-Allow-Headers", "traceparent,tracestate,baggage,content-type");
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
        res.end(`<!doctype html><button id="open">Open</button>
<script src="/sdk.js"></script>
<script type="importmap">{"imports":{"@hyperdx/browser":"/hyperdx.js","@opentelemetry/api":"/otel/index.js","@bufbuild/protobuf/wkt":"/proto/wkt/index.js","@bufbuild/protobuf":"/proto/index.js","@bufbuild/protobuf/wire":"/proto/wire/index.js","@bufbuild/protobuf/reflect":"/proto/reflect/index.js","@bufbuild/protobuf/codegenv2":"/proto/codegenv2/index.js","@connectrpc/connect":"/connect/index.js"}}</script>
<script type="module">
import { initBrowserTelemetry, trace } from '/runtime/telemetry.js';
import { buildRuntimeInterceptors } from '/runtime/interceptors.js';
import { interaction } from '/runtime/interaction.js';
window.tracing = trace;
window.ready = await initBrowserTelemetry({ serviceName: 'browser-proof', endpoint: '/_otel', apiOrigins: [${JSON.stringify(backendOrigin)}], consoleCapture: false });
// The token arrives only when the test says so: the calls wait in auth while a
// second interaction opens, and must still belong to the first.
let releaseToken;
const token = new Promise((r) => (releaseToken = r));
const chain = (terminal) => buildRuntimeInterceptors({ getToken: () => token })
  .reduceRight((next, interceptor) => interceptor(next), terminal);
const unary = chain((req) => fetch(${JSON.stringify(`${backendOrigin}/chat.v1.ChatService/GetChat`)}, { method: 'POST', headers: req.header }));
const stream = chain(async (req) => {
  await Promise.resolve(); // connect-web: await createRequestBody(...)
  return fetch(${JSON.stringify(`${backendOrigin}/chat.v1.ChatService/StreamChat`)}, { method: 'POST', headers: req.header });
});
document.querySelector('#open').addEventListener('click', () => {
  const op = interaction('chat.open', { 'chat.id': 'c1' });
  window.traceId = op.traceId;
  // Outside the handler: no span is active when these calls are made.
  setTimeout(() => {
    const calls = Promise.all([
      unary({ header: new Headers(), stream: false }),
      stream({ header: new Headers(), stream: true }),
    ]);
    const next = interaction('chat.open', { 'chat.id': 'c2' }); // supersedes op
    window.nextTraceId = next.traceId;
    releaseToken(null);
    window.finished = calls.finally(() => next.end({ outcome: 'ok' }));
  }, 50);
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
    await page.waitForFunction(() => window.ready === true, undefined, { timeout: 5000 });
    await page.locator("#open").click();
    await page.waitForFunction(() => window.finished !== undefined, undefined, { timeout: 5000 });
    await page.evaluate(() => window.finished);
    await page.waitForFunction(() => {
      const provider = window.tracing.getTracerProvider().getDelegate();
      return provider._activeSpanProcessor._spanProcessors.some((processor) =>
        (processor._finishedSpans ?? []).filter((span) => span.name.includes("chat.v1.ChatService")).length === 2);
    }, undefined, { timeout: 5000 });
    await page.evaluate(() => window.tracing.getTracerProvider().getDelegate().forceFlush());

    assert.deepEqual(errors, []);
    const spans = traces.flatMap((batch) => batch.resourceSpans ?? [])
      .flatMap((resource) => resource.scopeSpans ?? [])
      .flatMap((scope) => scope.spans ?? []);
    const chatId = (span) => span.attributes?.find((a) => a.key === "chat.id")?.value?.stringValue;
    const root = spans.find((span) => span.name === "chat.open" && chatId(span) === "c1");
    assert.ok(root, `the interaction is exported: ${JSON.stringify(spans.map((s) => s.name))}`);
    assert.equal(root.traceId, await page.evaluate(() => window.traceId));
    assert.ok(!root.parentSpanId, "the interaction is a root span");
    const superseding = spans.find((span) => span.name === "chat.open" && chatId(span) === "c2");
    assert.ok(superseding, "the second interaction is exported");
    assert.equal(superseding.traceId, await page.evaluate(() => window.nextTraceId));
    const fetches = spans.filter((span) => span.name.includes("chat.v1.ChatService"));
    assert.equal(fetches.length, 2, JSON.stringify(spans.map((s) => s.name)));
    for (const fetch of fetches) {
      assert.equal(fetch.traceId, root.traceId, `${fetch.name} is in the interaction's trace`);
      assert.equal(fetch.parentSpanId, root.spanId, `${fetch.name} is the child of the interaction it started under, not the one opened while auth waited`);
    }
    assert.deepEqual(
      [...requests].sort(),
      fetches.map((fetch) => `00-${fetch.traceId}-${fetch.spanId}-01`).sort(),
      "the server is handed each exported fetch span as its parent",
    );
  } finally {
    await browser?.close();
    await close(server);
    await close(backend);
  }
});

// page_engaged (src/page-engagement.ts) with the REAL SDK in a real browser:
// a route change reports the view that ended, and leaving the page reports
// the last one through the SDK's own exporter (fetch keepalive) before the
// document is gone. Exactly once per view, with the visible, active time and
// the deepest scroll.
test("page_engaged reports each view once: on a route change, and on leaving the page through a keepalive export", { timeout: 30000 }, async () => {
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
      } else if (url === "/elsewhere") {
        res.setHeader("content-type", "text/html");
        res.end("<!doctype html><p>another document</p>");
      } else if (url.startsWith("/blog/")) {
        res.setHeader("content-type", "text/html");
        res.end(`<!doctype html><body style="margin:0"><div style="height:4000px">a long post</div>
<script src="/sdk.js"></script>
<script type="importmap">{"imports":{"@hyperdx/browser":"/hyperdx.js","@opentelemetry/api":"/otel/index.js"}}</script>
<script type="module">
import { initBrowserTelemetry, trace } from '/runtime/telemetry.js';
window.tracing = trace;
window.ready = await initBrowserTelemetry({ serviceName: 'browser-proof', endpoint: '/_otel', consoleCapture: false, pageViews: true });
</script></body>`);
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
    const page = await browser.newPage({ viewport: { width: 800, height: 1000 } });
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`${origin}/blog/first-post`);
    await page.waitForFunction(() => window.ready === true, undefined, { timeout: 5000 });
    // Read for ~1.2 s, moving the mouse, and scroll half way:
    // (1500 + 1000) / 4000 = 62.5%, reported as 63.
    for (let i = 0; i < 6; i++) {
      await page.mouse.move(100 + i * 10, 100);
      await new Promise((done) => setTimeout(done, 200));
    }
    // Resolved by the scroll event itself, so the depth is seen before the
    // route changes.
    await page.evaluate(() => new Promise((done) => {
      document.addEventListener("scroll", () => done(), { once: true });
      window.scrollTo(0, 1500);
    }));
    await page.evaluate(() => history.pushState({}, "", "/blog/second-post"));
    await page.evaluate(() => window.scrollTo(0, 0));
    for (let i = 0; i < 4; i++) {
      await page.mouse.move(200 + i * 10, 100);
      await new Promise((done) => setTimeout(done, 200));
    }
    // Leave: a new document. No flush is called from the test: the runtime's
    // own hide/unload handling must get the last view out.
    await page.goto(`${origin}/elsewhere`);
    const spans = () => traces.flatMap((batch) => batch.resourceSpans ?? [])
      .flatMap((resource) => resource.scopeSpans ?? [])
      .flatMap((scope) => scope.spans ?? []);
    const value = (v) => v?.stringValue ?? (v?.intValue !== undefined ? Number(v.intValue) : v?.doubleValue);
    const engaged = () => spans().filter((s) => s.name === "page_engaged")
      .map((s) => Object.fromEntries((s.attributes ?? []).map((a) => [a.key, value(a.value)])));
    const deadline = Date.now() + 10000;
    while (engaged().length < 2 && Date.now() < deadline) {
      await new Promise((done) => setTimeout(done, 100));
    }
    // Room for a duplicate to arrive, if there were one.
    await new Promise((done) => setTimeout(done, 500));

    assert.deepEqual(errors, []);
    const views = engaged();
    assert.deepEqual(views.map((v) => v.route), ["/blog/first-post", "/blog/second-post"],
      `one page_engaged per view: ${JSON.stringify(views)}`);
    const [first, second] = views;
    assert.equal(first.entry, "true");
    assert.equal(second.entry, undefined);
    assert.equal(first.scroll_pct, 63);
    assert.ok(first.engaged_ms >= 1000 && first.engaged_ms < 10000, `first view engaged ${first.engaged_ms} ms`);
    assert.ok(second.engaged_ms >= 500 && second.engaged_ms < 10000, `second view engaged ${second.engaged_ms} ms`);
    for (const v of views) {
      assert.equal(v.resumed, undefined);
      assert.match(v.view_id, /^[0-9a-f]{16}$/);
    }
    assert.notEqual(first.view_id, second.view_id);
  } finally {
    await browser?.close();
    await close(server);
  }
});
