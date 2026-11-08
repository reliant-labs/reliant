// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The dev servers' /_otel proxy (see otel-proxy.ts), driven over real
// sockets: a fake upstream records what reaches it, and every refusal is
// asserted to have reached nothing. Requests go out through node:http with
// the path written byte for byte — fetch() would normalize `..` away before
// the proxy ever saw it.
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import http from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  createOtelProxyHandler,
  OTEL_PROXY_MAX_BODY_BYTES,
  type OtelProxyOptions,
  parseOtelHeaders,
  startOtelRelay,
} from "./otel-proxy.js";

interface Seen {
  method: string;
  url: string;
  headers: http.IncomingHttpHeaders;
  body: string;
}

interface Upstream {
  url: string;
  seen: Seen[];
  close(): Promise<void>;
}

/** fakeUpstream records every request and answers 200 with a cookie. */
async function fakeUpstream(): Promise<Upstream> {
  const seen: Seen[] = [];
  const server = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      seen.push({
        method: req.method ?? "",
        url: req.url ?? "",
        headers: req.headers,
        body: Buffer.concat(chunks).toString("utf8"),
      });
      res.writeHead(200, {
        "content-type": "application/json",
        "set-cookie": "edge=1",
        "x-upstream": "yes",
      });
      res.end('{"partialSuccess":{}}');
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const { port } = server.address() as AddressInfo;
  return {
    url: `http://127.0.0.1:${port}`,
    seen,
    close: () => new Promise((r) => server.close(() => r())),
  };
}

interface Reply {
  status: number;
  headers: http.IncomingHttpHeaders;
  body: string;
}

interface Send {
  method?: string;
  headers?: http.OutgoingHttpHeaders;
  body?: string | Buffer;
  /** Written as separate chunks with no content-length (chunked encoding). */
  chunks?: Buffer[];
}

/** send issues one request with `path` exactly as written. */
function send(base: string, path: string, opts: Send = {}): Promise<Reply> {
  const { hostname, port } = new URL(base);
  return new Promise((resolve, reject) => {
    const req = http.request(
      {
        hostname,
        port,
        path,
        method: opts.method ?? "POST",
        headers: opts.headers,
      },
      (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (c: Buffer) => chunks.push(c));
        res.on("end", () =>
          resolve({
            status: res.statusCode ?? 0,
            headers: res.headers,
            body: Buffer.concat(chunks).toString("utf8"),
          }),
        );
      },
    );
    req.on("error", reject);
    if (opts.chunks) {
      for (const c of opts.chunks) req.write(c);
      req.end();
    } else {
      req.end(opts.body);
    }
  });
}

/** serve mounts the handler at the root of a plain server, as the relay does. */
async function serve(
  options: OtelProxyOptions,
): Promise<{ url: string; close(): Promise<void> }> {
  const handle = createOtelProxyHandler(options);
  const server = http.createServer((req, res) =>
    handle(req, res, req.url ?? "/"),
  );
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const { port } = server.address() as AddressInfo;
  return {
    url: `http://127.0.0.1:${port}`,
    close: () => new Promise((r) => server.close(() => r())),
  };
}

const BAD_PATHS = [
  "/",
  "/v1",
  "/v1/",
  "/v1/traces/",
  "/v1/traces/extra",
  "/v1/profiles",
  "/v2/traces",
  "/V1/TRACES",
  "//v1/traces",
  "/v1//traces",
  "/v1/../v1/traces",
  "/v1/traces/../../admin",
  "/../v1/traces",
  "/%2e%2e/admin",
  "/v1%2Ftraces",
  "/v1/%74races",
  "/api/v1/orgs",
  "/v1/traces#",
  "/v1/traces#admin",
  "/v1/traces#?x=1",
];

describe("the /_otel proxy", () => {
  let upstream: Upstream;
  let dir: string;
  let headersFile: string;

  beforeEach(async () => {
    upstream = await fakeUpstream();
    dir = mkdtempSync(join(tmpdir(), "forge-otel-proxy-"));
    headersFile = join(dir, "headers");
    writeFileSync(headersFile, "authorization=Bearer%20stack-token-1\n");
  });
  afterEach(async () => {
    await upstream.close();
    rmSync(dir, { recursive: true, force: true });
  });

  describe("the Next.js relay (startOtelRelay)", () => {
    it("forwards a valid POST with the stack's token, and none of the page's credentials", async () => {
      const relay = await startOtelRelay({
        upstream: `${upstream.url}/`,
        headersFile,
      });
      for (const path of ["/v1/traces", "/v1/logs", "/v1/metrics"]) {
        const got = await send(relay, path, {
          headers: {
            "content-type": "application/json",
            cookie: "session=page-secret",
            authorization: "Bearer page-supplied",
            origin: "http://localhost:3000",
          },
          body: '{"resourceSpans":[]}',
        });
        expect(got.status).toBe(200);
        expect(got.body).toBe('{"partialSuccess":{}}');
        expect(got.headers["x-upstream"]).toBe("yes");
        // The edge may not set a cookie on the dev origin.
        expect(got.headers["set-cookie"]).toBeUndefined();
      }
      expect(upstream.seen.map((s) => s.url)).toEqual([
        "/v1/traces",
        "/v1/logs",
        "/v1/metrics",
      ]);
      for (const s of upstream.seen) {
        expect(s.method).toBe("POST");
        expect(s.body).toBe('{"resourceSpans":[]}');
        expect(s.headers.authorization).toBe("Bearer stack-token-1");
        expect(s.headers.cookie).toBeUndefined();
        expect(s.headers["content-type"]).toBe("application/json");
        expect(s.headers.origin).toBe("http://localhost:3000");
        expect(s.headers.host).toBe(new URL(upstream.url).host);
      }
    });

    it("re-reads the headers file on every request, so a renewed token is used at once", async () => {
      const relay = await startOtelRelay({
        upstream: upstream.url,
        headersFile,
      });
      await send(relay, "/v1/logs", { body: "{}" });
      writeFileSync(headersFile, "authorization=Bearer%20stack-token-2");
      await send(relay, "/v1/logs", { body: "{}" });
      expect(upstream.seen.map((s) => s.headers.authorization)).toEqual([
        "Bearer stack-token-1",
        "Bearer stack-token-2",
      ]);
    });

    it("still drops the page's credentials when the headers file is missing", async () => {
      const relay = await startOtelRelay({
        upstream: upstream.url,
        headersFile: join(dir, "not-written-yet"),
      });
      await send(relay, "/v1/traces", {
        headers: { cookie: "a=b", authorization: "Bearer page" },
        body: "{}",
      });
      expect(upstream.seen).toHaveLength(1);
      expect(upstream.seen[0]?.headers.authorization).toBeUndefined();
      expect(upstream.seen[0]?.headers.cookie).toBeUndefined();
    });

    it("keeps an upstream base path and drops a query string", async () => {
      const relay = await startOtelRelay({
        upstream: `${upstream.url}/ingest/`,
        headersFile,
      });
      await send(relay, "/v1/traces?forward=me", { body: "{}" });
      expect(upstream.seen.map((s) => s.url)).toEqual(["/ingest/v1/traces"]);
    });

    it("answers 404 for every other path, traversal spellings included, without contacting the upstream", async () => {
      const relay = await startOtelRelay({
        upstream: upstream.url,
        headersFile,
      });
      for (const path of BAD_PATHS) {
        const got = await send(relay, path, { body: "{}" });
        expect(got.status, path).toBe(404);
      }
      expect(upstream.seen).toEqual([]);
    });

    it("answers 405 for any method but POST, without contacting the upstream", async () => {
      const relay = await startOtelRelay({
        upstream: upstream.url,
        headersFile,
      });
      for (const method of [
        "GET",
        "HEAD",
        "PUT",
        "PATCH",
        "DELETE",
        "OPTIONS",
      ]) {
        const got = await send(relay, "/v1/traces", { method });
        expect(got.status, method).toBe(405);
        expect(got.headers.allow).toBe("POST");
      }
      expect(upstream.seen).toEqual([]);
    });

    it("answers 413 above 8 MiB, declared or streamed, without contacting the upstream", async () => {
      const relay = await startOtelRelay({
        upstream: upstream.url,
        headersFile,
      });
      const over = Buffer.alloc(OTEL_PROXY_MAX_BODY_BYTES + 1, 0x61);
      // Declared: refused on the header, before the body is read.
      expect((await send(relay, "/v1/logs", { body: over })).status).toBe(413);
      // Streamed: no content-length, so the cap is counted as it arrives.
      const chunk = Buffer.alloc(256 * 1024, 0x62);
      const chunks = Array.from(
        { length: OTEL_PROXY_MAX_BODY_BYTES / chunk.length + 1 },
        () => chunk,
      );
      expect((await send(relay, "/v1/logs", { chunks })).status).toBe(413);
      expect(upstream.seen).toEqual([]);
    });

    it("forwards a body of exactly the cap", async () => {
      const relay = await startOtelRelay({
        upstream: upstream.url,
        headersFile,
        maxBodyBytes: 1024,
      });
      const got = await send(relay, "/v1/logs", {
        chunks: [Buffer.alloc(512, 0x61), Buffer.alloc(512, 0x61)],
      });
      expect(got.status).toBe(200);
      expect(upstream.seen[0]?.body).toHaveLength(1024);
      expect(upstream.seen[0]?.headers["content-length"]).toBe("1024");
    });

    it("answers 502 when the upstream is unreachable", async () => {
      const gone = upstream.url;
      await upstream.close();
      upstream = await fakeUpstream(); // afterEach closes something real
      const relay = await startOtelRelay({ upstream: gone, headersFile });
      expect((await send(relay, "/v1/traces", { body: "{}" })).status).toBe(
        502,
      );
    });
  });

  describe("with nowhere to forward", () => {
    it("answers a valid POST 204 and refuses everything else by the same rules", async () => {
      const proxy = await serve({
        upstream: "",
        headersFile,
        maxBodyBytes: 1024,
      });
      try {
        expect(
          (await send(proxy.url, "/v1/traces", { body: "{}" })).status,
        ).toBe(204);
        expect(
          (await send(proxy.url, "/v1/../admin", { body: "{}" })).status,
        ).toBe(404);
        expect(
          (await send(proxy.url, "/v1/logs", { method: "GET" })).status,
        ).toBe(405);
        expect(
          (await send(proxy.url, "/v1/logs", { body: Buffer.alloc(1025) }))
            .status,
        ).toBe(413);
        expect(
          (
            await send(proxy.url, "/v1/logs", {
              chunks: [Buffer.alloc(1000), Buffer.alloc(1000)],
            })
          ).status,
        ).toBe(413);
      } finally {
        await proxy.close();
      }
      expect(upstream.seen).toEqual([]);
    });
  });
});

describe("parseOtelHeaders", () => {
  it("reads name=value pairs, percent-decoded, names lower-cased", () => {
    expect(
      parseOtelHeaders(
        "Authorization=Bearer%20tok\nx-scope = dev , x-raw=100%\n\n",
      ),
    ).toEqual([
      ["authorization", "Bearer tok"],
      ["x-scope", "dev"],
      ["x-raw", "100%"],
    ]);
  });

  it("skips what is not a header, or would reframe the request", () => {
    expect(
      parseOtelHeaders(
        "=nameless\nno-equals\nbad name=1\nhost=evil\ncontent-length=1\nx-crlf=a%0D%0Ab\nok=1",
      ),
    ).toEqual([["ok", "1"]]);
  });
});
