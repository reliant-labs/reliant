// The `/node` entry end to end, against a fixture of the KEYED ingest: the
// route a desktop app's main process ships to (control-plane
// internal/proxy/otel_ingest_keyed.go). The fixture enforces what that route
// enforces and a browser satisfies for free — POST /k/<key>/v1/{logs,traces},
// an Origin that is exactly one of the key's allowed origins, an OTLP content
// type — so a client that would be refused in prod is refused here.
//
// The process-error tests run a real child `node` over the BUILT dist/
// (`pretest` builds it), because what they prove is what Node itself does
// with an uncaught exception and an unhandled rejection: a test runner that
// installs its own handlers cannot show that.
import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { context, propagation, trace } from "@opentelemetry/api";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { reportException } from "./error-reporter.js";
import {
  buildNodeResource,
  flushOnQuit,
  frontendServiceName,
  getNodeTelemetry,
  initNodeTelemetry,
  MAX_REPEATS_PER_MINUTE,
  type NodeTelemetryConfig,
  REPORTED_EXCEPTION_BODY,
  resolveNodeIngestUrl,
  unhandledRejectionMode,
} from "./node.js";

const KEY = "pk_test_0123456789";
const ORIGIN = "app://bundle";
const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");

interface Received {
  signal: string;
  origin: string | undefined;
  body: any;
}

interface Fixture {
  endpoint: string;
  received: Received[];
  refused: Array<{ status: number; reason: string }>;
  close(): Promise<void>;
}

async function readBody(req: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const c of req) chunks.push(c as Buffer);
  return Buffer.concat(chunks).toString("utf8");
}

async function keyedIngest(): Promise<Fixture> {
  const received: Received[] = [];
  const refused: Fixture["refused"] = [];
  const server: Server = createServer(async (req, res) => {
    const refuse = (status: number, reason: string) => {
      refused.push({ status, reason });
      res.writeHead(status).end();
    };
    const m = /^\/k\/([^/]+)\/v1\/(logs|traces|metrics)$/.exec(req.url ?? "");
    if (!m) return refuse(404, "bad_path");
    if (req.method !== "POST") return refuse(405, "bad_method");
    if (m[1] !== KEY) return refuse(404, "unknown_key");
    if (req.headers.origin !== ORIGIN) return refuse(403, "bad_origin");
    const ct = (req.headers["content-type"] ?? "").split(";")[0]!.trim();
    if (!["application/json", "application/x-protobuf"].includes(ct))
      return refuse(415, "bad_content_type");
    const body = JSON.parse(await readBody(req));
    received.push({ signal: m[2]!, origin: req.headers.origin, body });
    res.writeHead(200, { "content-type": "application/json" }).end("{}");
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const { port } = server.address() as AddressInfo;
  return {
    endpoint: `http://127.0.0.1:${port}/k/${KEY}`,
    received,
    refused,
    close: () => new Promise<void>((r) => server.close(() => r())),
  };
}

function logRecords(f: Fixture) {
  return f.received
    .filter((r) => r.signal === "logs")
    .flatMap((r) =>
      r.body.resourceLogs.flatMap((rl: any) =>
        rl.scopeLogs.flatMap((sl: any) => sl.logRecords),
      ),
    );
}

function spansOf(f: Fixture) {
  return f.received
    .filter((r) => r.signal === "traces")
    .flatMap((r) =>
      r.body.resourceSpans.flatMap((rs: any) =>
        rs.scopeSpans.flatMap((ss: any) => ss.spans),
      ),
    );
}

function attrs(kvs: any[]): Record<string, unknown> {
  return Object.fromEntries(
    kvs.map((kv) => [
      kv.key,
      kv.value.stringValue ??
        kv.value.intValue ??
        kv.value.boolValue ??
        kv.value.doubleValue,
    ]),
  );
}

let fixture: Fixture;

beforeEach(async () => {
  fixture = await keyedIngest();
});

afterEach(async () => {
  await getNodeTelemetry()?.shutdown(1000);
  await fixture.close();
});

function config(extra: Partial<NodeTelemetryConfig> = {}): NodeTelemetryConfig {
  return {
    serviceName: frontendServiceName("shop", "desktop"),
    serviceVersion: "1.2.3",
    environment: "prod",
    frontend: "desktop",
    appClient: "electron",
    endpoint: fixture.endpoint,
    origin: ORIGIN,
    appRoot: pkgDir,
    batchDelayMs: 60_000,
    // The runner owns process errors in-process; the child tests cover them.
    captureProcessErrors: false,
    ...extra,
  };
}

describe("frontendServiceName", () => {
  it("agrees with forge's naming.FrontendServiceName on the shared cases", () => {
    const cases = JSON.parse(
      readFileSync(
        join(pkgDir, "src", "testdata", "frontend-service-names.json"),
        "utf8",
      ),
    ) as Array<{ project: string; frontend: string; service: string }>;
    expect(cases.length).toBeGreaterThan(0);
    for (const c of cases) {
      expect(frontendServiceName(c.project, c.frontend)).toBe(c.service);
    }
  });
});

describe("configuration", () => {
  it("is off without a usable endpoint, and installs nothing", () => {
    for (const endpoint of [
      undefined,
      "",
      "off",
      "/_otel",
      "ftp://x/y",
      "not a url",
    ]) {
      expect(resolveNodeIngestUrl(endpoint), String(endpoint)).toBeNull();
      expect(
        initNodeTelemetry(config({ endpoint })),
        String(endpoint),
      ).toBeNull();
    }
    expect(resolveNodeIngestUrl("https://ingest.reliantlabs.dev/k/pk_x/")).toBe(
      "https://ingest.reliantlabs.dev/k/pk_x",
    );
  });

  it("is idempotent", () => {
    const a = initNodeTelemetry(config());
    expect(a).not.toBeNull();
    expect(initNodeTelemetry(config({ serviceName: "other" }))).toBe(a);
  });

  it("describes the process without anything that identifies the machine or the user", () => {
    const r = buildNodeResource(
      {
        serviceName: "shop-desktop",
        serviceVersion: "1.2.3",
        environment: "prod",
        frontend: "desktop",
        resourceAttributes: {
          "service.name": "spoofed",
          "app.channel": "beta",
        },
      },
      {
        versions: { node: "22.1.0", electron: "41.0.0" },
        platform: "win32",
        arch: "x64",
      },
      "0.9.3",
    );
    expect(r).toEqual({
      "service.name": "shop-desktop",
      "service.version": "1.2.3",
      "deployment.environment.name": "prod",
      "deployment.environment": "prod",
      "forge.frontend": "desktop",
      "app.channel": "beta",
      "telemetry.sdk.name": "@reliantlabs/forge-web-runtime",
      "telemetry.sdk.language": "nodejs",
      "telemetry.sdk.version": "0.9.3",
      "process.runtime.name": "electron",
      "process.runtime.version": "41.0.0",
      "os.type": "windows",
      "host.arch": "amd64",
    });
  });

  it("parses --unhandled-rejections the way Node does", () => {
    expect(unhandledRejectionMode([], undefined)).toBe("throw");
    expect(
      unhandledRejectionMode(["--unhandled-rejections=warn"], undefined),
    ).toBe("warn");
    expect(
      unhandledRejectionMode(
        [],
        "--max-old-space-size=4096 --unhandled-rejections=none",
      ),
    ).toBe("none");
    expect(
      unhandledRejectionMode(["--unhandled-rejections", "strict"], undefined),
    ).toBe("strict");
  });
});

describe("export to the keyed ingest", () => {
  it("ships logs the keyed route admits: the key in the path, the allowed Origin, OTLP/JSON", async () => {
    const t = initNodeTelemetry(config())!;
    t.setUser("user_42");
    t.logger.info("window opened", {
      "window.kind": "main",
      count: 2,
      payload: { prompt: "never sent" },
    });
    t.logger.debug("below the floor");
    await t.flush(5000);

    expect(fixture.refused).toEqual([]);
    expect(fixture.received.map((r) => r.origin)).toEqual([ORIGIN]);
    const resource = attrs(
      fixture.received[0]!.body.resourceLogs[0].resource.attributes,
    );
    expect(resource["service.name"]).toBe("shop-desktop");
    expect(resource).not.toHaveProperty("rum.sessionId");
    const [record, ...rest] = logRecords(fixture);
    expect(rest).toEqual([]);
    expect(record.severityNumber).toBe(9);
    expect(record.severityText).toBe("INFO");
    expect(record.body).toEqual({ stringValue: "window opened" });
    expect(attrs(record.attributes)).toEqual({
      "app.client": "electron",
      userId: "user_42",
      "window.kind": "main",
      count: "2",
    });
  });

  it("is refused without the allowed Origin, which is why `origin` exists", async () => {
    const errors: Error[] = [];
    const t = initNodeTelemetry(
      config({ origin: undefined, onExportError: (e) => errors.push(e) }),
    )!;
    t.logger.warn("x");
    await t.flush(5000);
    expect(fixture.received).toEqual([]);
    expect(fixture.refused).toEqual([{ status: 403, reason: "bad_origin" }]);
    expect(errors[0]?.message).toContain("403");
  });

  it("records a caught error, and the barrel's reportException, as an sfp1-groupable ERROR", async () => {
    const t = initNodeTelemetry(config())!;
    function syncSettings(): never {
      throw new RangeError(
        "quota exceeded: token=sk-ant-abcdefgh12345678\nraw file contents",
      );
    }
    try {
      syncSettings();
    } catch (err) {
      t.reportError(err, { "settings.section": "privacy" });
      expect(reportException(err)).toBe(true);
    }
    t.logger.error("backend exited", {
      err: new TypeError("spawn ENOENT"),
      "exit.code": 127,
    });
    await t.flush(5000);

    const [reported, viaBarrel, logged] = logRecords(fixture);
    for (const r of [reported, viaBarrel, logged]) {
      expect(r.severityNumber).toBe(17);
    }
    expect(reported.body.stringValue).toBe(REPORTED_EXCEPTION_BODY);
    const a = attrs(reported.attributes);
    expect(a["exception.type"]).toBe("RangeError");
    expect(a["exception.message"]).toBe("quota exceeded: token=[redacted]");
    expect(a["code.function.name"]).toBe("syncSettings (src/node.test.ts)");
    expect(a["code.file.path"]).toBe("src/node.test.ts");
    expect(String(a["exception.stacktrace"])).not.toContain(
      "raw file contents",
    );
    expect(String(a["exception.stacktrace"])).not.toContain(pkgDir);
    expect(a["settings.section"]).toBe("privacy");

    expect(logged.body.stringValue).toBe("backend exited");
    const l = attrs(logged.attributes);
    expect(l["exception.type"]).toBe("TypeError");
    expect(l["err"]).toBe("spawn ENOENT");
    expect(l["exit.code"]).toBe("127");
  });

  it("exports spans, parents them through async work, and puts the trace on logs", async () => {
    const t = initNodeTelemetry(config())!;
    await t.tracer.startActiveSpan("app.ready", async (span) => {
      await new Promise((r) => setTimeout(r, 1));
      const child = trace.getTracer("shop").startSpan("backend.start");
      t.logger.info("inside");
      child.end();
      span.end();
    });
    const carrier: Record<string, string> = {};
    t.tracer.startActiveSpan("outbound", (span) => {
      propagation.inject(context.active(), carrier);
      span.end();
    });
    await t.flush(5000);

    const spans = spansOf(fixture);
    const ready = spans.find((s: any) => s.name === "app.ready");
    const child = spans.find((s: any) => s.name === "backend.start");
    const outbound = spans.find((s: any) => s.name === "outbound");
    expect(child.traceId).toBe(ready.traceId);
    expect(child.parentSpanId).toBe(ready.spanId);
    expect(ready.kind).toBe(1); // SPAN_KIND_INTERNAL on the wire
    expect(attrs(ready.attributes)["app.client"]).toBe("electron");
    expect(carrier.traceparent).toBe(
      `00-${outbound.traceId}-${outbound.spanId}-01`,
    );
    const inside = logRecords(fixture).find(
      (r: any) => r.body.stringValue === "inside",
    );
    expect(inside.traceId).toBe(ready.traceId);
  });

  it("flushes on Electron's before-quit, then quits", async () => {
    const t = initNodeTelemetry(config())!;
    t.logger.info("about to quit");
    let listener: ((e: { preventDefault(): void }) => void) | undefined;
    let quits = 0;
    let prevented = 0;
    flushOnQuit({
      on: (_e, l) => void (listener = l),
      quit: () => void quits++,
    });
    listener!({ preventDefault: () => void prevented++ });
    expect(prevented).toBe(1);
    await expect.poll(() => quits).toBe(1);
    expect(logRecords(fixture).map((r: any) => r.body.stringValue)).toEqual([
      "about to quit",
    ]);
    // The second quit (ours) goes through.
    listener!({ preventDefault: () => void prevented++ });
    expect(prevented).toBe(1);
  });

  it("a flush is bounded by its timeout when the ingest hangs", async () => {
    const hung = createServer(() => {
      /* never answers */
    });
    await new Promise<void>((r) => hung.listen(0, "127.0.0.1", r));
    try {
      const { port } = hung.address() as AddressInfo;
      const t = initNodeTelemetry(
        config({ endpoint: `http://127.0.0.1:${port}/k/${KEY}` }),
      )!;
      t.logger.info("x");
      const start = Date.now();
      await t.flush(200);
      expect(Date.now() - start).toBeLessThan(2000);
    } finally {
      hung.closeAllConnections();
      await new Promise<void>((r) => hung.close(() => r()));
    }
  });
});

// ── Process errors, in a real Node process ──────────────────────────────

const distEntry = pathToFileURL(join(pkgDir, "dist", "node.js")).href;

async function runChild(
  source: string,
): Promise<{ code: number | null; stderr: string; stdout: string }> {
  const dir = mkdtempSync(join(tmpdir(), "forge-node-telemetry-"));
  try {
    const script = join(dir, "main.mjs");
    writeFileSync(
      script,
      `import { initNodeTelemetry } from ${JSON.stringify(distEntry)};\n` +
        `const telemetry = initNodeTelemetry({ serviceName: "shop-desktop", endpoint: ${JSON.stringify(fixture.endpoint)}, origin: ${JSON.stringify(ORIGIN)}, appRoot: ${JSON.stringify(dir)}, batchDelayMs: 60000 });\n` +
        `if (!telemetry) { console.error("telemetry did not start"); process.exit(9); }\n` +
        source,
    );
    const child = spawn(process.execPath, [script], {
      cwd: dir,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stderr = "";
    let stdout = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.stdout.on("data", (d) => (stdout += d));
    const code = await new Promise<number | null>((r) => child.on("close", r));
    return { code, stderr, stdout };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

// A child node pays process start-up on a loaded machine: well past vitest's 5s
// default when every core is busy, which is a timeout, not a failure.
describe(
  "process errors (a child node over dist/)",
  { timeout: 60_000 },
  () => {
    it("records an uncaught exception, then dies as Node would", async () => {
      const { code, stderr } = await runChild(
        `function explode() { throw new TypeError("boom: password=hunter2\\nsecond line"); }\n` +
          `setTimeout(explode, 0);\n`,
      );
      expect(code).toBe(1);
      expect(stderr).toContain("TypeError: boom");
      const [record, ...rest] = logRecords(fixture);
      expect(rest).toEqual([]);
      expect(record.body.stringValue).toBe("uncaught exception");
      expect(record.severityNumber).toBe(17);
      const a = attrs(record.attributes);
      expect(a["exception.type"]).toBe("TypeError");
      expect(a["exception.message"]).toBe("boom: password=[redacted]");
      // V8's frame name, minus the `[as _onTimeout]` alias.
      expect(a["code.function.name"]).toBe("Timeout.explode (main.mjs)");
    });

    it("records an unhandled rejection, then raises it as Node's default does", async () => {
      const { code, stderr } = await runChild(
        `async function load() { throw new RangeError("no such profile"); }\n` +
          `load();\n`,
      );
      expect(code).toBe(1);
      expect(stderr).toContain("RangeError: no such profile");
      const records = logRecords(fixture);
      expect(records.map((r: any) => r.body.stringValue)).toEqual([
        "unhandled rejection",
      ]);
      expect(attrs(records[0].attributes)["code.function.name"]).toBe(
        "load (main.mjs)",
      );
    });

    it("leaves the process to the app's own handlers", async () => {
      const { code, stdout } = await runChild(
        `process.on("uncaughtException", () => console.log("app handled exception"));\n` +
          `process.on("unhandledRejection", () => console.log("app handled rejection"));\n` +
          `Promise.reject(new Error("rejected"));\n` +
          `setTimeout(() => { throw new Error("thrown"); }, 0);\n` +
          `setTimeout(async () => { await telemetry.flush(5000); console.log("still running"); process.exit(0); }, 50);\n`,
      );
      expect(code).toBe(0);
      expect(stdout).toContain("app handled exception");
      expect(stdout).toContain("app handled rejection");
      expect(stdout).toContain("still running");
      expect(
        logRecords(fixture)
          .map((r: any) => r.body.stringValue)
          .sort(),
      ).toEqual(["uncaught exception", "unhandled rejection"]);
    });

    it("records a handler's throw loop a bounded number of times, not once per throw", async () => {
      // reliant's EPIPE loop: a logger writing to a dead pipe throws, the
      // handler logs it, that throws again.
      const { code } = await runChild(
        `process.on("uncaughtException", () => {});\n` +
          `let n = 0;\n` +
          `const timer = setInterval(function writeLog() {\n` +
          `  if (++n > 40) { clearInterval(timer); telemetry.flush(5000).then(() => process.exit(0)); return; }\n` +
          `  throw new Error("write EPIPE");\n` +
          `}, 1);\n`,
      );
      expect(code).toBe(0);
      const records = logRecords(fixture);
      expect(records).toHaveLength(MAX_REPEATS_PER_MINUTE);
      expect(
        new Set(
          records.map((r: any) => attrs(r.attributes)["exception.message"]),
        ),
      ).toEqual(new Set(["write EPIPE"]));
    });

    it("flushes what is queued when an idle process exits", async () => {
      const { code } = await runChild(`telemetry.logger.info("bye");\n`);
      expect(code).toBe(0);
      expect(logRecords(fixture).map((r: any) => r.body.stringValue)).toEqual([
        "bye",
      ]);
    });

    it("loads with require() from CommonJS, as an Electron main.js does", async () => {
      const dir = mkdtempSync(join(tmpdir(), "forge-node-telemetry-cjs-"));
      try {
        const script = join(dir, "main.cjs");
        writeFileSync(
          script,
          `const { initNodeTelemetry } = require(${JSON.stringify(join(pkgDir, "dist", "node.js"))});\n` +
            `const t = initNodeTelemetry({ serviceName: "shop-desktop", endpoint: ${JSON.stringify(fixture.endpoint)}, origin: ${JSON.stringify(ORIGIN)} });\n` +
            `t.logger.info("from cjs");\n`,
        );
        const child = spawn(process.execPath, [script], {
          cwd: dir,
          stdio: ["ignore", "ignore", "pipe"],
        });
        let stderr = "";
        child.stderr.on("data", (d) => (stderr += d));
        const code = await new Promise<number | null>((r) =>
          child.on("close", r),
        );
        expect(stderr).not.toMatch(/Error/);
        expect(code).toBe(0);
        expect(logRecords(fixture).map((r: any) => r.body.stringValue)).toEqual(
          ["from cjs"],
        );
      } finally {
        rmSync(dir, { recursive: true, force: true });
      }
    });
  },
);
