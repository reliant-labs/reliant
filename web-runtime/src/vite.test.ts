// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The dev-server half of serving a runtime config document rendered outside
// the frontend's tree (see vite.ts). Driven against a fake server: the
// behaviour under test is the middleware's, not Vite's.
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import type { IncomingMessage, ServerResponse } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";

import type { Plugin } from "vite";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  FORGE_RUNTIME_CONFIG_FILE_ENV,
  forgeRuntimeConfig,
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
