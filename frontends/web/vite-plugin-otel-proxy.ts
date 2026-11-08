import fs from "node:fs";
import type { IncomingMessage, ServerResponse } from "node:http";
import type { Plugin } from "vite";

/**
 * Dev-server half of browser telemetry: the page's same-origin `/_otel`.
 *
 * The page sends OTLP/HTTP to `/_otel/v1/{traces,logs,metrics}` on its own
 * origin (OTEL_ENDPOINT in config.js, src/lib/telemetry.ts) and holds no
 * collector address and no secret. This forwards it to the base URL
 * `forge env up` gives the dev server in OTEL_EXPORTER_OTLP_ENDPOINT.
 *
 * A HOSTED dev stack (observability.dev_telemetry, deploy/kcl/dev/main.k):
 * the upstream is the org's hosted edge and FORGE_OTEL_HEADERS_FILE names the
 * file holding this stack's browser token, which forge renews in place. It is
 * read on EVERY request and attached server-side, so the token never reaches
 * the bundle.
 *
 * ── What is forwarded, and nothing else ──────────────────────────────────
 *
 * The token authorizes writes to the org's telemetry store, so this is not
 * a general proxy. Only a POST to exactly one of OTLP_PATHS is forwarded:
 * another method is 405, another path (a traversal like
 * `/_otel/v1/traces/../x` included: paths are compared as sent, never
 * normalized) is 404, and neither touches the upstream. The caller must be
 * this page on a loopback host (callerAllowed): its Origin must name the very
 * host it sent the request to, that host must be loopback, and a browser's
 * Sec-Fetch-Site, when sent, must be same-origin. Anything else is 403 before
 * the body is read. CORS does not stop a foreign site from SENDING a simple
 * cross-origin POST to the loopback dev server, only from reading the reply,
 * so without this any page the developer visits could spend the org's
 * ingestion allowance under the stack token; and a dev server reached through
 * a tunnel (allowedHosts in vite.config.ts) has no caller credential to
 * check, so its telemetry is refused rather than forwarded for anyone who
 * can reach the tunnel. A body over
 * MAX_BODY_BYTES (the gateway's own max_request_body_size) is 413: a declared
 * Content-Length is refused before reading, and a chunked body is counted as
 * it streams and refused the moment it passes the cap, so at most the cap is
 * ever held. Only content-type and content-encoding go upstream from the
 * page's headers, so its cookies and any authorization it sent never do.
 * Redirects are not followed (the token stays on the one origin).
 *
 * With no upstream (the stack's telemetry is local and there is no local
 * store), the same rules apply and an accepted request is answered 204 and
 * dropped, so a page that still sends does not log a failed export every few
 * seconds.
 *
 * Unlike forge's vite-spa template there is no 127.0.0.1:4318 default:
 * reliant's own dev stack runs no local collector, so a guessed port is at
 * best nothing and at worst another stack's ClickStack.
 *
 * DEV ONLY, structurally: `apply: "serve"`.
 */

export const OTEL_PREFIX = "/_otel";
export const OTLP_PATHS: ReadonlySet<string> = new Set(["/v1/traces", "/v1/logs", "/v1/metrics"]);
/** The gateway's max_request_body_size: 8 MiB. */
export const MAX_BODY_BYTES = 8 * 1024 * 1024;
const UPSTREAM_TIMEOUT_MS = 10_000;
const FORWARDED_REQUEST_HEADERS = ["content-type", "content-encoding"] as const;

export interface OtelProxyOptions {
  /** Upstream base URL; "" means nowhere to forward (accepted requests get 204). */
  upstream: string;
  /** File holding `name=value` header pairs to attach upstream; "" for none. */
  headersFile: string;
  maxBodyBytes?: number;
  /** Seam for tests. */
  fetch?: typeof fetch;
}

/** Header pairs from the file forge writes (`authorization=Bearer …`), read per request. */
export function readHeadersFile(file: string): Array<[string, string]> {
  if (!file) return [];
  let text = "";
  try {
    text = fs.readFileSync(file, "utf8");
  } catch {
    return []; // not written yet, or removed: send nothing extra
  }
  const out: Array<[string, string]> = [];
  for (const pair of text.split(/[\n,]/)) {
    const i = pair.indexOf("=");
    if (i <= 0) continue;
    let value = pair.slice(i + 1).trim();
    try {
      value = decodeURIComponent(value);
    } catch {
      // Not percent-encoded: use it as written.
    }
    out.push([pair.slice(0, i).trim(), value]);
  }
  return out;
}

function isLoopbackHost(hostname: string): boolean {
  const h = hostname.toLowerCase();
  return h === "localhost" || h.endsWith(".localhost") || h === "[::1]" || /^127(\.\d{1,3}){3}$/.test(h);
}

/**
 * Whether the request is the dev page itself on a loopback host: an Origin
 * naming exactly the Host the request was sent to, that host loopback, and
 * no cross-site Sec-Fetch-Site. A browser always sends Origin on a POST, so
 * a request without one is not the page.
 */
export function callerAllowed(req: IncomingMessage): boolean {
  const origin = req.headers.origin;
  const host = req.headers.host;
  if (typeof origin !== "string" || typeof host !== "string" || !host) return false;
  let parsed: URL;
  try {
    parsed = new URL(origin);
  } catch {
    return false;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return false;
  if (parsed.host.toLowerCase() !== host.toLowerCase()) return false;
  if (!isLoopbackHost(parsed.hostname)) return false;
  const site = req.headers["sec-fetch-site"];
  if (site !== undefined && site !== "same-origin") return false;
  return true;
}

function reply(res: ServerResponse, status: number, extra: Record<string, string> = {}): void {
  res.writeHead(status, { "content-length": "0", ...extra });
  res.end();
}

/** Refuses with 413 and stops reading: the socket goes once the reply is flushed. */
function tooLarge(req: IncomingMessage, res: ServerResponse): void {
  res.on("finish", () => req.destroy());
  reply(res, 413, { connection: "close" });
}

/** The body, or null once it was refused (413) or the client went away. */
function readCapped(req: IncomingMessage, res: ServerResponse, max: number): Promise<Buffer | null> {
  return new Promise((resolve) => {
    const chunks: Buffer[] = [];
    let size = 0;
    let done = false;
    const finish = (body: Buffer | null) => {
      if (done) return;
      done = true;
      resolve(body);
    };
    req.on("data", (chunk: Buffer) => {
      if (done) return;
      size += chunk.length;
      if (size > max) {
        req.pause();
        tooLarge(req, res);
        finish(null);
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => finish(Buffer.concat(chunks, size)));
    req.on("error", () => finish(null));
    req.on("aborted", () => finish(null));
  });
}

/**
 * The middleware: handles every request under /_otel (and only those), never
 * calling next for them.
 */
export function otelProxyMiddleware(opts: OtelProxyOptions) {
  const upstream = opts.upstream.trim().replace(/\/+$/, "");
  const max = opts.maxBodyBytes ?? MAX_BODY_BYTES;
  const doFetch = opts.fetch ?? fetch;
  return async (req: IncomingMessage, res: ServerResponse, next: () => void): Promise<void> => {
    const url = req.url ?? "";
    const q = url.search(/[?#]/);
    const path = q < 0 ? url : url.slice(0, q);
    if (path !== OTEL_PREFIX && !path.startsWith(OTEL_PREFIX + "/")) {
      next();
      return;
    }
    const rest = path.slice(OTEL_PREFIX.length);
    if (!OTLP_PATHS.has(rest)) {
      reply(res, 404);
      return;
    }
    if (req.method !== "POST") {
      reply(res, 405, { allow: "POST" });
      return;
    }
    if (!callerAllowed(req)) {
      reply(res, 403, { connection: "close" });
      return;
    }
    const declared = Number(req.headers["content-length"]);
    if (Number.isFinite(declared) && declared > max) {
      tooLarge(req, res);
      return;
    }
    const body = await readCapped(req, res, max);
    if (body === null) return;
    if (!upstream) {
      reply(res, 204);
      return;
    }
    const headers = new Headers();
    for (const name of FORWARDED_REQUEST_HEADERS) {
      const v = req.headers[name];
      if (typeof v === "string") headers.set(name, v);
    }
    for (const [name, value] of readHeadersFile(opts.headersFile)) headers.set(name, value);
    let upstreamRes: Response;
    try {
      upstreamRes = await doFetch(upstream + rest, {
        method: "POST",
        headers,
        body,
        redirect: "manual",
        signal: AbortSignal.timeout(UPSTREAM_TIMEOUT_MS),
      });
    } catch {
      reply(res, 502);
      return;
    }
    const out = Buffer.from(await upstreamRes.arrayBuffer().catch(() => new ArrayBuffer(0)));
    const extra: Record<string, string> = { "content-length": String(out.length) };
    const ct = upstreamRes.headers.get("content-type");
    if (ct) extra["content-type"] = ct;
    res.writeHead(upstreamRes.status, extra);
    res.end(out);
  };
}

/** The dev-server plugin over otelProxyMiddleware. */
export function otelProxy(opts: Omit<OtelProxyOptions, "fetch">): Plugin {
  return {
    name: "reliant-otel-proxy",
    apply: "serve",
    configureServer(server) {
      const mw = otelProxyMiddleware(opts);
      server.middlewares.use((req, res, next) => {
        void mw(req, res, next);
      });
    },
  };
}
