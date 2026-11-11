// @vitest-environment node
import fs from "node:fs";
import http from "node:http";
import type { AddressInfo } from "node:net";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { MAX_BODY_BYTES, otelProxyMiddleware, type OtelProxyOptions } from "./vite-plugin-otel-proxy";

/**
 * The /_otel middleware behind a real HTTP server, so paths arrive exactly as
 * a client sent them (Node's client does not normalize `..`) and bodies
 * stream. The upstream is a fake fetch that records what reached it: a
 * refused request must leave it empty.
 */

interface Forwarded {
  url: string;
  headers: Headers;
  body: Buffer;
}

const servers: http.Server[] = [];
afterEach(async () => {
  await Promise.all(servers.splice(0).map((s) => new Promise((r) => s.close(r))));
});

async function serve(opts: Partial<OtelProxyOptions> = {}) {
  const forwarded: Forwarded[] = [];
  const fakeFetch = (async (url: string | URL | Request, init?: RequestInit) => {
    forwarded.push({
      url: String(url),
      headers: new Headers(init?.headers),
      body: Buffer.from(init?.body as Uint8Array),
    });
    return new Response(new Uint8Array([1, 2, 3]), { status: 200, headers: { "content-type": "application/x-protobuf" } });
  }) as typeof fetch;
  const mw = otelProxyMiddleware({ upstream: "https://edge.example.test/", headersFile: "", fetch: fakeFetch, ...opts });
  const server = http.createServer((req, res) => {
    void mw(req, res, () => {
      res.writeHead(299);
      res.end("next");
    });
  });
  servers.push(server);
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const { port } = server.address() as AddressInfo;
  return { port, forwarded };
}

interface Sent {
  method?: string;
  path: string;
  headers?: Record<string, string>;
  /** Written as separate chunks (chunked transfer unless content-length is set). */
  chunks?: Buffer[];
  /** Send no Origin header; by default the request carries the page's own origin. */
  noOrigin?: boolean;
}

function send(port: number, s: Sent): Promise<{ status: number; body: Buffer }> {
  const headers: Record<string, string> = s.noOrigin ? {} : { origin: `http://127.0.0.1:${port}` };
  Object.assign(headers, s.headers);
  return new Promise((resolve, reject) => {
    const req = http.request({ host: "127.0.0.1", port, method: s.method ?? "POST", path: s.path, headers }, (res) => {
      const parts: Buffer[] = [];
      res.on("data", (c: Buffer) => parts.push(c));
      res.on("end", () => resolve({ status: res.statusCode ?? 0, body: Buffer.concat(parts) }));
    });
    // A refused upload may see its socket closed mid-write; the response
    // already arrived, so only an error with no response fails the test.
    req.on("error", (err) => reject(err));
    for (const c of s.chunks ?? []) req.write(c);
    req.end();
  });
}

describe("otelProxyMiddleware", () => {
  it("forwards a POST to /_otel/v1/traces with the stack token, and only that token", async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "otel-proxy-"));
    const headersFile = path.join(dir, "otlp-headers-browser");
    fs.writeFileSync(headersFile, "authorization=Bearer%20stack-token\n");
    const { port, forwarded } = await serve({ headersFile });
    const res = await send(port, {
      path: "/_otel/v1/traces",
      headers: {
        "content-type": "application/x-protobuf",
        cookie: "session=secret",
        authorization: "Bearer page-token",
        "x-other": "1",
      },
      chunks: [Buffer.from("span-bytes")],
    });
    expect(res.status).toBe(200);
    expect([...res.body]).toEqual([1, 2, 3]);
    expect(forwarded).toHaveLength(1);
    const f = forwarded[0];
    expect(f.url).toBe("https://edge.example.test/v1/traces");
    expect(f.body.toString()).toBe("span-bytes");
    expect(f.headers.get("authorization")).toBe("Bearer stack-token");
    expect(f.headers.get("content-type")).toBe("application/x-protobuf");
    expect(f.headers.get("cookie")).toBeNull();
    expect(f.headers.get("x-other")).toBeNull();
  });

  it("never forwards the page's own authorization, even with no token file", async () => {
    const { port, forwarded } = await serve();
    await send(port, { path: "/_otel/v1/logs", headers: { authorization: "Bearer page-token" }, chunks: [Buffer.from("x")] });
    expect(forwarded).toHaveLength(1);
    expect(forwarded[0].url).toBe("https://edge.example.test/v1/logs");
    expect(forwarded[0].headers.get("authorization")).toBeNull();
  });

  it("refuses a GET with 405 and never forwards it", async () => {
    const { port, forwarded } = await serve();
    const res = await send(port, { method: "GET", path: "/_otel/v1/traces" });
    expect(res.status).toBe(405);
    expect(forwarded).toHaveLength(0);
  });

  it.each(["/_otel/v1/unknown", "/_otel/", "/_otel", "/_otel/v1/traces/", "/_otel/v1/%74races", "/_otel/v2/traces"])(
    "refuses the unknown path %s with 404 and never forwards it",
    async (p) => {
      const { port, forwarded } = await serve();
      const res = await send(port, { path: p, chunks: [Buffer.from("x")] });
      expect(res.status).toBe(404);
      expect(forwarded).toHaveLength(0);
    },
  );

  it("refuses a traversal (/_otel/v1/traces/../x) with 404 and never forwards it", async () => {
    const { port, forwarded } = await serve();
    const res = await send(port, { path: "/_otel/v1/traces/../x", chunks: [Buffer.from("x")] });
    expect(res.status).toBe(404);
    expect(forwarded).toHaveLength(0);
  });

  it("refuses a declared oversize body with 413 before reading it, and never forwards it", async () => {
    const { port, forwarded } = await serve();
    const res = await send(port, {
      path: "/_otel/v1/traces",
      headers: { "content-length": String(MAX_BODY_BYTES + 1) },
    });
    expect(res.status).toBe(413);
    expect(forwarded).toHaveLength(0);
  });

  it("refuses a chunked body the moment it passes the cap, and never forwards it", async () => {
    const { port, forwarded } = await serve({ maxBodyBytes: 1024 });
    const res = await send(port, { path: "/_otel/v1/metrics", chunks: [Buffer.alloc(600), Buffer.alloc(600), Buffer.alloc(600)] });
    expect(res.status).toBe(413);
    expect(forwarded).toHaveLength(0);
  });

  it("accepts a body exactly at the cap", async () => {
    const { port, forwarded } = await serve({ maxBodyBytes: 1024 });
    const res = await send(port, { path: "/_otel/v1/metrics", chunks: [Buffer.alloc(512), Buffer.alloc(512)] });
    expect(res.status).toBe(200);
    expect(forwarded[0].body.length).toBe(1024);
  });

  it("caps at the gateway's 8 MiB by default", () => {
    expect(MAX_BODY_BYTES).toBe(8 * 1024 * 1024);
  });

  it("with no upstream, answers an accepted POST 204 and applies the same rules", async () => {
    const { port, forwarded } = await serve({ upstream: "" });
    expect((await send(port, { path: "/_otel/v1/traces", chunks: [Buffer.from("x")] })).status).toBe(204);
    expect((await send(port, { method: "GET", path: "/_otel/v1/traces" })).status).toBe(405);
    expect((await send(port, { path: "/_otel/v1/traces/../x" })).status).toBe(404);
    expect((await send(port, { path: "/_otel/v1/logs", headers: { "content-length": String(MAX_BODY_BYTES + 1) } })).status).toBe(413);
    expect(forwarded).toHaveLength(0);
  });

  it("leaves every other path to the next middleware", async () => {
    const { port, forwarded } = await serve();
    for (const p of ["/", "/_otelx/v1/traces", "/reliant.v1.ChatService/List"]) {
      expect((await send(port, { method: "GET", path: p })).status).toBe(299);
    }
    expect(forwarded).toHaveLength(0);
  });

  it("forwards a same-origin POST from the page on localhost", async () => {
    const { port, forwarded } = await serve();
    const res = await send(port, {
      path: "/_otel/v1/traces",
      headers: { host: `localhost:${port}`, origin: `http://localhost:${port}`, "sec-fetch-site": "same-origin" },
      chunks: [Buffer.from("x")],
    });
    expect(res.status).toBe(200);
    expect(forwarded).toHaveLength(1);
  });

  it.each<[string, Record<string, string>]>([
    ["a foreign Origin", { origin: "https://evil.example", "content-type": "text/plain" }],
    ["a cross-site Sec-Fetch-Site", { "sec-fetch-site": "cross-site" }],
    ["an Origin naming another loopback port", { origin: "http://127.0.0.1:1" }],
    ["an opaque Origin", { origin: "null" }],
    ["a tunnel host", { host: "abc.trycloudflare.com", origin: "https://abc.trycloudflare.com" }],
  ])("refuses %s with 403 and never forwards it", async (_name, headers) => {
    const { port, forwarded } = await serve();
    const res = await send(port, { path: "/_otel/v1/traces", headers, chunks: [Buffer.from('{"resourceSpans":[]}')] });
    expect(res.status).toBe(403);
    expect(forwarded).toHaveLength(0);
  });

  it("refuses a POST with no Origin with 403 and never forwards it, with or without an upstream", async () => {
    for (const upstream of ["https://edge.example.test/", ""]) {
      const { port, forwarded } = await serve({ upstream });
      const res = await send(port, { path: "/_otel/v1/logs", noOrigin: true, chunks: [Buffer.from("x")] });
      expect(res.status).toBe(403);
      expect(forwarded).toHaveLength(0);
    }
  });

  it("answers 502 when the upstream cannot be reached", async () => {
    const failing = (async () => {
      throw new TypeError("fetch failed");
    }) as typeof fetch;
    const { port } = await serve({ fetch: failing });
    expect((await send(port, { path: "/_otel/v1/traces", chunks: [Buffer.from("x")] })).status).toBe(502);
  });
});
