// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The dev-server half of serving a runtime config document rendered outside
// the frontend's tree (see vite.ts). Driven against a fake server: the
// behaviour under test is the middleware's, not Vite's.
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import http, { type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";

import type { Plugin } from "vite";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DEV_LOG_ENDPOINT } from "./devlog.js";
import {
  type DevLogServer,
  FORGE_RUNTIME_CONFIG_FILE_ENV,
  forgeDevLog,
  forgeOtelProxy,
  forgeRuntimeConfig,
  type OtelProxyServer,
  type RuntimeConfigServer,
} from "./vite.js";

type Middleware = Parameters<RuntimeConfigServer["middlewares"]["use"]>[0];

function fakeServer(base = "/") {
  const middlewares: Middleware[] = [];
  const watched: string[] = [];
  const listeners: ((path: string) => void)[] = [];
  const sent: unknown[] = [];
  const warn = vi.fn();
  const server: RuntimeConfigServer = {
    config: { base, logger: { warn } },
    middlewares: { use: (fn) => middlewares.push(fn) },
    watcher: {
      add: (p) => watched.push(...(typeof p === "string" ? [p] : p)),
      on: (_event, listener) => listeners.push(listener),
    },
    ws: { send: (payload) => sent.push(payload) },
  };
  return { server, middlewares, watched, listeners, sent, warn };
}

interface Served {
  next: boolean;
  status?: number;
  headers: Record<string, string>;
  body?: string;
}

/** request runs the middleware chain for one request and reports the outcome. */
function request(
  middlewares: Middleware[],
  url: string,
  method = "GET",
): Promise<Served> {
  return new Promise((resolveServed) => {
    const out: Served = { next: false, headers: {} };
    const res = {
      statusCode: 0,
      setHeader(k: string, v: string) {
        out.headers[k.toLowerCase()] = v;
      },
      end(body?: string) {
        out.status = res.statusCode;
        out.body = body;
        resolveServed(out);
      },
    };
    const mw = middlewares[0];
    if (!mw) {
      resolveServed({ next: true, headers: {} });
      return;
    }
    mw(
      { url, method } as IncomingMessage,
      res as unknown as ServerResponse,
      () => {
        out.next = true;
        resolveServed(out);
      },
    );
  });
}

describe("forgeRuntimeConfig", () => {
  let dir: string;
  let file: string;
  const DOC = 'window.__FORGE_CONFIG__ = {"API_URL":"http://api.test"};\n';

  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "forge-rc-"));
    file = join(dir, "config.js");
    writeFileSync(file, DOC);
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
    vi.unstubAllEnvs();
  });

  it("is a Vite plugin", () => {
    // Compile-time: the structural plugin type is assignable to Vite's own.
    const plugin: Plugin = forgeRuntimeConfig({ file });
    expect(plugin.name).toBe("forge-runtime-config");
  });

  it("serves <base>config.js from the rendered file, uncached", async () => {
    const f = fakeServer();
    forgeRuntimeConfig({ file }).configureServer(f.server);
    const got = await request(f.middlewares, "/config.js?v=1");
    expect(got.next).toBe(false);
    expect(got.status).toBe(200);
    expect(got.body).toBe(DOC);
    expect(got.headers["content-type"]).toMatch(/^text\/javascript/);
    expect(got.headers["cache-control"]).toBe("no-store");
  });

  it("serves under a base path, and only there", async () => {
    const f = fakeServer("/app/");
    forgeRuntimeConfig({ file }).configureServer(f.server);
    expect((await request(f.middlewares, "/app/config.js")).body).toBe(DOC);
    expect((await request(f.middlewares, "/config.js")).next).toBe(true);
  });

  it("passes every other request through", async () => {
    const f = fakeServer();
    forgeRuntimeConfig({ file }).configureServer(f.server);
    expect((await request(f.middlewares, "/src/main.tsx")).next).toBe(true);
    expect((await request(f.middlewares, "/config.js", "POST")).next).toBe(
      true,
    );
  });

  it("reads the file on every request, so a re-render is seen without a restart", async () => {
    const f = fakeServer();
    forgeRuntimeConfig({ file }).configureServer(f.server);
    await request(f.middlewares, "/config.js");
    writeFileSync(file, "window.__FORGE_CONFIG__ = {};\n");
    expect((await request(f.middlewares, "/config.js")).body).toBe(
      "window.__FORGE_CONFIG__ = {};\n",
    );
  });

  it("reloads the page when the file changes", () => {
    const f = fakeServer();
    forgeRuntimeConfig({ file }).configureServer(f.server);
    expect(f.watched).toEqual([file]);
    f.listeners.forEach((l) => l(join(dir, "other.js")));
    expect(f.sent).toEqual([]);
    f.listeners.forEach((l) => l(file));
    expect(f.sent).toEqual([{ type: "full-reload", path: "*" }]);
  });

  it("falls through LOUDLY when the file is missing", async () => {
    const f = fakeServer();
    forgeRuntimeConfig({ file: join(dir, "missing.js") }).configureServer(
      f.server,
    );
    expect((await request(f.middlewares, "/config.js")).next).toBe(true);
    expect(f.warn).toHaveBeenCalledOnce();
    expect(String(f.warn.mock.calls[0]?.[0])).toContain(
      FORGE_RUNTIME_CONFIG_FILE_ENV,
    );
  });

  it("reads FORGE_RUNTIME_CONFIG_FILE by default", async () => {
    vi.stubEnv(FORGE_RUNTIME_CONFIG_FILE_ENV, file);
    const f = fakeServer();
    forgeRuntimeConfig().configureServer(f.server);
    expect((await request(f.middlewares, "/config.js")).body).toBe(DOC);
  });

  it("is inert without a file, leaving public/config.js to Vite", () => {
    vi.stubEnv(FORGE_RUNTIME_CONFIG_FILE_ENV, "");
    const f = fakeServer();
    forgeRuntimeConfig().configureServer(f.server);
    forgeRuntimeConfig().configurePreviewServer(f.server);
    expect(f.middlewares).toEqual([]);
    expect(f.watched).toEqual([]);
  });

  it("serves the preview server too, without watching", async () => {
    const f = fakeServer();
    forgeRuntimeConfig({ file }).configurePreviewServer(f.server);
    expect((await request(f.middlewares, "/config.js")).body).toBe(DOC);
    expect(f.watched).toEqual([]);
  });
});

// forgeOtelProxy is ./otel-proxy mounted as middleware; otel-proxy.test.ts
// covers the rules. Here: what the mount claims, and what it passes through.
describe("forgeOtelProxy", () => {
  let upstream: http.Server;
  let upstreamURL: string;
  let seen: string[];
  let dev: http.Server;
  let devURL: string;

  beforeEach(async () => {
    seen = [];
    upstream = http.createServer((req, res) => {
      seen.push(`${req.method} ${req.url} ${req.headers.authorization ?? "-"}`);
      req.resume();
      req.on("end", () => res.end("ok"));
    });
    await new Promise<void>((r) => upstream.listen(0, "127.0.0.1", r));
    upstreamURL = `http://127.0.0.1:${(upstream.address() as AddressInfo).port}`;

    // A dev server whose middleware stack is the plugin's, then a sentinel
    // answering 418 for whatever the plugin passed on.
    const f = fakeServer();
    const headers = join(mkdtempSync(join(tmpdir(), "forge-otel-vite-")), "h");
    writeFileSync(headers, "authorization=Bearer%20tok");
    forgeOtelProxy({
      upstream: upstreamURL,
      headersFile: headers,
    }).configureServer(f.server as OtelProxyServer);
    const mw = f.middlewares[0];
    dev = http.createServer((req, res) =>
      mw?.(req, res, () => {
        res.statusCode = 418;
        res.end();
      }),
    );
    await new Promise<void>((r) => dev.listen(0, "127.0.0.1", r));
    devURL = `127.0.0.1:${(dev.address() as AddressInfo).port}`;
  });
  afterEach(async () => {
    await new Promise((r) => dev.close(r));
    await new Promise((r) => upstream.close(r));
  });

  // The page's own exporter: Origin names the host it addressed. The dev
  // server is reached through a preview host, not loopback, as on a
  // Reliant workspace.
  const PAGE_HOST = "5173-ws.workspaces.example";
  const status = (
    path: string,
    method = "POST",
    origin: string | null = `https://${PAGE_HOST}`,
  ): Promise<number> =>
    new Promise((resolveStatus, reject) => {
      const [host, port] = devURL.split(":");
      const headers: Record<string, string> = { host: PAGE_HOST };
      if (origin !== null) headers.origin = origin;
      const req = http.request({ host, port, path, method, headers }, (res) => {
        res.resume();
        res.on("end", () => resolveStatus(res.statusCode ?? 0));
      });
      req.on("error", reject);
      req.end(method === "POST" ? "{}" : undefined);
    });

  it("is a Vite plugin", () => {
    const plugin: Plugin = forgeOtelProxy({ upstream: "" });
    expect(plugin.name).toBe("forge-otel-proxy");
  });

  it("forwards an ingest POST under /_otel, prefix stripped, token attached", async () => {
    expect(await status("/_otel/v1/traces")).toBe(200);
    expect(seen).toEqual(["POST /v1/traces Bearer tok"]);
  });

  it("refuses everything else under /_otel without contacting the upstream", async () => {
    expect(await status("/_otel")).toBe(404);
    expect(await status("/_otel/")).toBe(404);
    expect(await status("/_otel/v1/../admin")).toBe(404);
    expect(await status("/_otel/api/v1/orgs")).toBe(404);
    expect(await status("/_otel/v1/logs", "GET")).toBe(405);
    expect(seen).toEqual([]);
  });

  it("refuses a foreign or Origin-less caller without contacting the upstream", async () => {
    expect(
      await status("/_otel/v1/traces", "POST", "https://evil.example"),
    ).toBe(403);
    expect(await status("/_otel/v1/traces", "POST", null)).toBe(403);
    expect(seen).toEqual([]);
  });

  it("passes every request outside the prefix through", async () => {
    expect(await status("/src/main.tsx", "GET")).toBe(418);
    expect(await status("/_otelx/v1/traces")).toBe(418);
    expect(await status("/api/_otel/v1/traces")).toBe(418);
    expect(seen).toEqual([]);
  });
});

describe("forgeDevLog", () => {
  let dev: http.Server;
  let devURL: string;
  let printed: string[];

  beforeEach(async () => {
    printed = [];
    vi.spyOn(console, "log").mockImplementation((...args: unknown[]) => {
      printed.push(args.map(String).join(" "));
    });
    // A dev server whose middleware stack is the plugin's, then a sentinel
    // answering 418 for whatever the plugin passed on.
    const middlewares: Middleware[] = [];
    forgeDevLog().configureServer({
      middlewares: { use: (fn) => middlewares.push(fn) },
    } as DevLogServer);
    const mw = middlewares[0];
    dev = http.createServer((req, res) =>
      mw?.(req, res, () => {
        res.statusCode = 418;
        res.end();
      }),
    );
    await new Promise<void>((r) => dev.listen(0, "127.0.0.1", r));
    devURL = `127.0.0.1:${(dev.address() as AddressInfo).port}`;
  });
  afterEach(async () => {
    vi.restoreAllMocks();
    await new Promise((r) => dev.close(r));
  });

  const post = (
    body: string | undefined,
    path = DEV_LOG_ENDPOINT,
    method = "POST",
    contentType = "application/json",
  ): Promise<{ status: number; protocol: string | undefined }> =>
    new Promise((resolvePost, reject) => {
      const [host, port] = devURL.split(":");
      const req = http.request(
        { host, port, path, method, headers: { "content-type": contentType } },
        (res) => {
          res.resume();
          res.on("end", () =>
            resolvePost({
              status: res.statusCode ?? 0,
              protocol: res.headers["x-forge-devlog"] as string | undefined,
            }),
          );
        },
      );
      req.on("error", reject);
      req.end(body);
    });

  it("is a dev-server-only Vite plugin at installDevLogging's endpoint", () => {
    const plugin: Plugin = forgeDevLog();
    expect(plugin.name).toBe("forge-dev-log");
    expect(plugin.apply).toBe("serve");
  });

  it("prints a v2 batch as one write, one [browser:<level>] line per entry", async () => {
    const res = await post(
      JSON.stringify({
        entries: [
          { level: "info", msg: "hello" },
          { level: "error", msg: "boom" },
          { level: "trace", msg: "odd level" },
          null,
        ],
      }),
    );
    expect(res).toEqual({ status: 204, protocol: "2" });
    expect(printed).toEqual([
      "[browser:info] hello\n[browser:error] boom\n[browser:log] odd level",
    ]);
  });

  it("accepts a legacy v1 single entry, and a text/plain beacon", async () => {
    expect(await post(JSON.stringify({ level: "warn", msg: "v1" }))).toEqual({
      status: 204,
      protocol: "2",
    });
    await post(
      JSON.stringify({ entries: [{ level: "debug", msg: "beacon" }] }),
      DEV_LOG_ENDPOINT,
      "POST",
      "text/plain",
    );
    expect(printed).toEqual(["[browser:warn] v1", "[browser:debug] beacon"]);
  });

  it("truncates an over-long line", async () => {
    await post(JSON.stringify({ level: "info", msg: "x".repeat(8_001) }));
    expect(printed).toEqual([
      `[browser:info] ${"x".repeat(8_000)}… (truncated)`,
    ]);
  });

  it("says so, never silently, when a post is malformed or oversized", async () => {
    expect(await post("{not json")).toEqual({ status: 400, protocol: "2" });
    expect(await post("42")).toEqual({ status: 400, protocol: "2" });
    const big = JSON.stringify({ level: "info", msg: "y".repeat(1024 * 1024) });
    expect(await post(big)).toEqual({ status: 413, protocol: "2" });
    expect(printed).toEqual([
      "[browser:warn] [forge-devlog] dropped malformed post",
      "[browser:warn] [forge-devlog] dropped malformed post",
      `[browser:warn] [forge-devlog] dropped oversized post (${Buffer.byteLength(big)} bytes)`,
    ]);
  });

  it("refuses any other method at the endpoint, and passes every other path through", async () => {
    expect((await post(undefined, DEV_LOG_ENDPOINT, "GET")).status).toBe(405);
    expect((await post("{}", "/src/main.tsx")).status).toBe(418);
    expect((await post("{}", "/__forge/logs")).status).toBe(418);
    for (const method of ["GET", "POST"]) {
      for (const path of ["/__forge/log/", "/__forge/log/assets/app.js?raw"]) {
        expect((await post("{}", path, method)).status).toBe(418);
      }
    }
    expect(printed).toEqual([]);
  });

  it("matches the exact endpoint with a query string", async () => {
    expect(await post("{}", `${DEV_LOG_ENDPOINT}?source=browser`)).toEqual({
      status: 204,
      protocol: "2",
    });
  });

  it("serves a custom endpoint", async () => {
    const middlewares: Middleware[] = [];
    forgeDevLog({ endpoint: "/custom/log/" }).configureServer({
      middlewares: { use: (fn) => middlewares.push(fn) },
    });
    const outcome = await request(middlewares, "/custom/log", "GET");
    expect(outcome.status).toBe(405);
    for (const method of ["GET", "POST"]) {
      expect(
        (await request(middlewares, "/custom/log/assets/app.js?raw", method))
          .next,
      ).toBe(true);
    }
    expect((await request(middlewares, DEV_LOG_ENDPOINT, "GET")).next).toBe(
      true,
    );
  });
});
