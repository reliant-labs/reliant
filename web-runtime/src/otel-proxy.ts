// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The dev server's `/_otel` proxy: the one place a page's browser telemetry
// leaves the dev server for a collector.
//
// # Why this is a module and not a few lines of config
//
// The browser sends OTLP/HTTP to `/_otel/v1/{traces,logs}` on its OWN origin
// and holds no collector address and no secret. The dev server forwards it to
// OTEL_EXPORTER_OTLP_ENDPOINT. On a hosted dev stack
// (observability.dev_telemetry) that endpoint is the org's hosted edge and
// FORGE_OTEL_HEADERS_FILE holds this stack's bearer token, which the proxy
// attaches server-side so the bundle never sees it.
//
// That makes the proxy a credentialed relay, and anything that can reach the
// dev server — another device on the LAN, a workspace preview URL, a tunnel,
// and any website the developer's browser has open — can use it. A proxy that
// forwards every caller, path and method would let any of them submit
// arbitrary routes or unbounded bodies to the edge AS THIS STACK. So it
// forwards exactly what the page's own exporter sends and nothing else:
//
//   - only a SAME-ORIGIN caller: the request's `Origin` must name the host
//     the browser addressed (`Host`, or for the Next.js relay the
//     `X-Forwarded-Host` Next's rewrite proxy sets from it), and
//     `Sec-Fetch-Site`, when the browser sends it, must be `same-origin`.
//     Browsers send `Origin` on every POST, same-origin fetches included, so
//     the page's exporter always carries it; a foreign page carries its own
//     origin and cannot forge either header. A missing or foreign `Origin`,
//     any of these headers sent twice, or a host value that is not exactly
//     one host[:port] is 403, before the method is looked at or the body
//     read. Loopback is deliberately NOT
//     required: a dev server is legitimately opened through a workspace
//     preview URL, a tunnel or a LAN address, and same-origin is the
//     boundary in all of them.
//   - `POST <prefix>/v1/traces`, `/v1/logs` and `/v1/metrics`. Any other
//     method on those paths is 405, any other path (traversal spellings, a
//     trailing slash and percent-encoding included: the path is compared
//     byte for byte, never decoded) is 404. Neither contacts the upstream,
//     and every refusal made before the body is read closes the connection,
//     so no caller can keep the dev server reading a body it will discard.
//   - a body of at most 8 MiB, counted while it streams in and buffered
//     before anything is sent upstream; above that, 413, and the upstream
//     never sees a byte of it.
//   - only `content-type` and `content-encoding` of the page's headers are
//     forwarded, plus the token. Its cookies, its own authorization header
//     and everything else stay behind, whether or not there is a token to
//     attach; so does any cookie the upstream tries to set.
//
// The same rules hold with no upstream at all (a dev server told there is
// nowhere to forward): a valid ingest POST is answered 204 and dropped, and
// everything else is refused exactly as above.
//
// One implementation serves both dev servers. Vite mounts it as middleware
// (`forgeOtelProxy` in ./vite); Next.js, whose rewrites can neither filter nor
// add a header, rewrites `/_otel/:path*` to a loopback relay this module
// starts (`startOtelRelay`). A fix here reaches every project through a
// runtime upgrade, with no owned config file to hand-edit.
//
// Node only: it imports node:http, node:https and node:fs, and is loaded by a
// dev server's config, never by a page.
import { readFileSync } from "node:fs";
import http, {
  type IncomingHttpHeaders,
  type IncomingMessage,
  type OutgoingHttpHeaders,
  type ServerResponse,
} from "node:http";
import https from "node:https";

/** The variable naming the collector (or hosted edge) BASE URL. */
export const OTEL_UPSTREAM_ENV = "OTEL_EXPORTER_OTLP_ENDPOINT";

/**
 * The variable naming the file of headers to attach upstream, as
 * `name=value` pairs (comma- or newline-separated, values optionally
 * percent-encoded — the OTEL_EXPORTER_OTLP_HEADERS format). forge writes it
 * on a hosted dev stack and renews the token in place, so it is read on every
 * request.
 */
export const OTEL_HEADERS_FILE_ENV = "FORGE_OTEL_HEADERS_FILE";

/** The path prefix the browser sends telemetry under. */
export const OTEL_PROXY_PREFIX = "/_otel";

/** The OTLP/HTTP ingest paths the proxy forwards, below the prefix. */
export const OTEL_INGEST_PATHS: readonly string[] = [
  "/v1/traces",
  "/v1/logs",
  "/v1/metrics",
];

/** The largest request body the proxy forwards: 8 MiB. */
export const OTEL_PROXY_MAX_BODY_BYTES = 8 * 1024 * 1024;

/** How long the upstream gets to answer before the proxy answers 504. */
const UPSTREAM_TIMEOUT_MS = 30_000;

export interface OtelProxyOptions {
  /**
   * The collector BASE URL the ingest paths are appended to. Defaults to
   * `process.env.OTEL_EXPORTER_OTLP_ENDPOINT`. Empty means nowhere to
   * forward: a valid ingest POST is answered 204 and dropped.
   */
  upstream?: string;
  /**
   * The file of headers to attach upstream, read on every request. Defaults
   * to `process.env.FORGE_OTEL_HEADERS_FILE`. Empty attaches nothing; a
   * missing or unreadable file attaches nothing for that request.
   */
  headersFile?: string;
  /** The body cap in bytes. Defaults to OTEL_PROXY_MAX_BODY_BYTES (8 MiB). */
  maxBodyBytes?: number;
  /**
   * The request header naming the host the browser addressed, which the
   * caller's `Origin` must match. `"host"` (the default) for a server the
   * browser reaches directly, as Vite's is. `"x-forwarded-host"` behind a
   * proxy that sets it from the browser's `Host`: startOtelRelay uses it,
   * because Next's rewrite proxy replaces `Host` with the relay's own address
   * and writes the original into `X-Forwarded-Host`, overwriting any value
   * the client sent.
   */
  callerHostHeader?: "host" | "x-forwarded-host";
}

/**
 * Handles one request already known to be under the proxy's prefix. `path`
 * is the request path BELOW the prefix, as received (`/v1/traces`,
 * `/v1/traces?x=1`, `/../admin`): it is matched, never decoded.
 */
export type OtelProxyHandler = (
  req: IncomingMessage,
  res: ServerResponse,
  path: string,
) => void;

// The only request headers forwarded from the page: what the collector needs
// to decode the body. Everything else — cookies, the page's authorization,
// whatever a caller added — stays behind; the proxy sets its own host and
// content-length, and the token comes from the headers file.
const FORWARDED_REQUEST_HEADERS = ["content-type", "content-encoding"] as const;

// Response headers never passed back: hop framing (Node frames the reply
// itself) and any cookie the upstream tries to set on the dev origin.
const DROPPED_RESPONSE_HEADERS = new Set([
  "set-cookie",
  "connection",
  "keep-alive",
  "transfer-encoding",
  "proxy-connection",
  "trailer",
  "upgrade",
]);

// Names the headers file may not set: they frame the request.
const FRAMING_HEADERS = new Set([
  "host",
  "content-length",
  "transfer-encoding",
  "connection",
]);

const HEADER_NAME = /^[!#$%&'*+.^_`|~0-9a-z-]+$/;
const HEADER_VALUE_FORBIDDEN = /[\r\n\0]/;

/**
 * parseOtelHeaders reads `name=value` pairs (comma- or newline-separated,
 * values optionally percent-encoded). Names are lower-cased; a pair whose
 * name or value is not a legal header, or that would reframe the request, is
 * skipped rather than sent.
 */
export function parseOtelHeaders(text: string): Array<[string, string]> {
  const out: Array<[string, string]> = [];
  for (const pair of text.split(/[\n,]/)) {
    const i = pair.indexOf("=");
    if (i <= 0) continue;
    const name = pair.slice(0, i).trim().toLowerCase();
    let value = pair.slice(i + 1).trim();
    try {
      value = decodeURIComponent(value);
    } catch {
      // Not percent-encoded: use it as written.
    }
    if (!HEADER_NAME.test(name) || FRAMING_HEADERS.has(name)) continue;
    if (HEADER_VALUE_FORBIDDEN.test(value)) continue;
    out.push([name, value]);
  }
  return out;
}

function readHeadersFile(file: string): Array<[string, string]> {
  if (file === "") return [];
  let text = "";
  try {
    text = readFileSync(file, "utf8");
  } catch {
    return []; // not written yet, or removed: send nothing extra
  }
  return parseOtelHeaders(text);
}

function reply(
  res: ServerResponse,
  status: number,
  headers: OutgoingHttpHeaders = {},
): void {
  if (res.writableEnded) return; // already answered (a timeout, then its error)
  if (res.headersSent) {
    res.destroy(); // mid-reply: cut it rather than append a second status
    return;
  }
  res.writeHead(status, { ...headers, "content-length": 0 });
  res.end();
}

/**
 * refuse answers a request whose body is never read, and closes the
 * connection: otherwise Node would keep it alive by reading and discarding
 * whatever body the caller keeps sending, without bound.
 */
function refuse(
  res: ServerResponse,
  status: number,
  headers: OutgoingHttpHeaders = {},
): void {
  reply(res, status, { ...headers, connection: "close" });
}

function forwardedHeaders(
  incoming: IncomingHttpHeaders,
  extra: Array<[string, string]>,
  host: string,
  length: number,
): OutgoingHttpHeaders {
  const out: OutgoingHttpHeaders = {};
  for (const name of FORWARDED_REQUEST_HEADERS) {
    const value = incoming[name];
    if (value !== undefined) out[name] = value;
  }
  for (const [name, value] of extra) out[name] = value;
  out.host = host;
  out["content-length"] = length;
  return out;
}

/** The parts of a request the caller check reads. */
export type CallerRequest = Pick<IncomingMessage, "headers" | "rawHeaders">;

// One authority, host[:port], and nothing else: a DNS name or an IPv4
// address, or a bracketed IPv6 address, then an optional decimal port. No
// userinfo, path, comma, whitespace or second value can match.
const AUTHORITY =
  /^(\[[0-9a-f:.]+\]|[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*)(?::([1-9][0-9]{0,4}))?$/;

interface Authority {
  hostname: string;
  /** The port as written, or undefined when none was. */
  port: number | undefined;
}

/**
 * parseAuthority reads one canonical host[:port], lower-cased, or returns
 * undefined. A host the WHATWG URL parser would rewrite (`0x7f.1`, an
 * uncompressed IPv6 address, a trailing dot) is refused rather than
 * normalized, so the value compared is the value every hop forwarded.
 */
function parseAuthority(value: string): Authority | undefined {
  const m = AUTHORITY.exec(value.toLowerCase());
  if (m === null) return undefined;
  const hostname = m[1] ?? "";
  const port = m[2] === undefined ? undefined : Number(m[2]);
  if (port !== undefined && port > 65535) return undefined;
  try {
    if (new URL(`http://${hostname}`).hostname !== hostname) return undefined;
  } catch {
    return undefined;
  }
  return { hostname, port };
}

/** How many times the request carried `name` (any case) on the wire. */
function headerCount(req: CallerRequest, name: string): number {
  let n = 0;
  for (let i = 0; i < req.rawHeaders.length; i += 2) {
    if (req.rawHeaders[i]?.toLowerCase() === name) n++;
  }
  return n;
}

/**
 * sameOriginCaller reports whether the request comes from a page on the very
 * origin it was sent to: `Origin` is a serialized http(s) origin whose host
 * and port equal those of `hostHeader`'s value (a missing port on either side
 * is the scheme's default), and `Sec-Fetch-Site`, when present, is
 * `same-origin`.
 *
 * Anything ambiguous is not a caller: an absent `Origin`, the opaque `null`
 * origin, any of the three headers sent more than once (Node keeps the first
 * `Host` and comma-joins the rest, and a proxy in between may pick
 * differently), or a host value that is not exactly one canonical
 * host[:port] — a comma, userinfo, a path, whitespace or an invalid port.
 *
 * The scheme is not compared, because the dev server cannot know it: a
 * workspace preview URL or tunnel terminates TLS and hands it plain HTTP for
 * an `https://` page, and one host:port cannot serve a second scheme.
 */
export function sameOriginCaller(
  req: CallerRequest,
  hostHeader: "host" | "x-forwarded-host" = "host",
): boolean {
  for (const name of ["origin", hostHeader, "sec-fetch-site"]) {
    if (headerCount(req, name) > 1) return false;
  }
  const site = req.headers["sec-fetch-site"];
  if (site !== undefined && site !== "same-origin") return false;
  const origin = req.headers.origin;
  const host = req.headers[hostHeader];
  if (typeof origin !== "string" || typeof host !== "string") return false;
  let url: URL;
  try {
    url = new URL(origin);
  } catch {
    return false;
  }
  // A serialized origin is scheme://host[:port] and nothing else.
  if (url.protocol !== "http:" && url.protocol !== "https:") return false;
  if (url.origin !== origin.toLowerCase()) return false;
  const defaultPort = url.protocol === "https:" ? 443 : 80;
  const page = parseAuthority(url.host);
  const addressed = parseAuthority(host);
  if (page === undefined || addressed === undefined) return false;
  return (
    page.hostname === addressed.hostname &&
    (page.port ?? defaultPort) === (addressed.port ?? defaultPort)
  );
}

/**
 * The request path without its query string. An HTTP request target has no
 * fragment, so a literal `#` stays part of the path, and `/v1/traces#x`
 * matches no ingest path.
 */
function pathOnly(path: string): string {
  const q = path.indexOf("?");
  return q < 0 ? path : path.slice(0, q);
}

/**
 * createOtelProxyHandler builds the request handler both dev servers mount.
 * The options are read once; the headers file is read on every forwarded
 * request.
 */
export function createOtelProxyHandler(
  options: OtelProxyOptions = {},
): OtelProxyHandler {
  const upstream = (options.upstream ?? process.env[OTEL_UPSTREAM_ENV] ?? "")
    .trim()
    .replace(/\/+$/, "");
  const headersFile = (
    options.headersFile ??
    process.env[OTEL_HEADERS_FILE_ENV] ??
    ""
  ).trim();
  const max = options.maxBodyBytes ?? OTEL_PROXY_MAX_BODY_BYTES;
  const callerHost = options.callerHostHeader ?? "host";
  const ingest = new Set(OTEL_INGEST_PATHS);

  const forward = (
    req: IncomingMessage,
    res: ServerResponse,
    path: string,
    body: Buffer,
  ): void => {
    if (upstream === "") {
      reply(res, 204); // nowhere to forward: drop it quietly
      return;
    }
    let target: URL;
    try {
      const base = new URL(upstream);
      target = new URL(base.pathname.replace(/\/+$/, "") + path, base);
    } catch {
      reply(res, 502); // a malformed upstream forwards nothing
      return;
    }
    const headers = forwardedHeaders(
      req.headers,
      readHeadersFile(headersFile),
      target.host,
      body.length,
    );
    const send = target.protocol === "https:" ? https.request : http.request;
    const up = send(target, { method: "POST", headers }, (upRes) => {
      const back: OutgoingHttpHeaders = {};
      for (const [name, value] of Object.entries(upRes.headers)) {
        if (value !== undefined && !DROPPED_RESPONSE_HEADERS.has(name)) {
          back[name] = value;
        }
      }
      res.writeHead(upRes.statusCode ?? 502, back);
      upRes.pipe(res);
    });
    up.setTimeout(UPSTREAM_TIMEOUT_MS, () => {
      up.destroy();
      reply(res, 504);
    });
    up.on("error", () => reply(res, 502));
    up.end(body);
  };

  return (req, res, rawPath) => {
    const path = pathOnly(rawPath);
    if (!ingest.has(path)) {
      refuse(res, 404);
      return;
    }
    // Before the method and the body: a foreign page never gets a byte
    // buffered, let alone forwarded with the token, whatever it sends.
    if (!sameOriginCaller(req, callerHost)) {
      refuse(res, 403);
      return;
    }
    if (req.method !== "POST") {
      refuse(res, 405, { allow: "POST" });
      return;
    }
    const declared = Number(req.headers["content-length"]);
    if (Number.isFinite(declared) && declared > max) {
      refuse(res, 413);
      return;
    }
    // Buffer the body under the cap BEFORE contacting the upstream, so an
    // oversize body never reaches it. Past the cap the rest is read and
    // discarded rather than the socket cut, so the client reads the 413
    // instead of a reset.
    const chunks: Buffer[] = [];
    let size = 0;
    let settled = false;
    req.on("data", (chunk: Buffer) => {
      if (settled) return;
      size += chunk.length;
      if (size > max) {
        settled = true;
        chunks.length = 0;
        reply(res, 413);
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => {
      if (settled) return;
      settled = true;
      forward(req, res, path, Buffer.concat(chunks, size));
    });
    req.on("error", () => {
      if (settled) return;
      settled = true;
      res.destroy();
    });
  };
}

/**
 * startOtelRelay starts the proxy on an ephemeral 127.0.0.1 port and resolves
 * to its base URL (`http://127.0.0.1:<port>`). It serves the ingest paths at
 * its root: a Next.js config rewrites `/_otel/:path*` to `<relay>/:path*`,
 * which strips the prefix on the way. The server is unref'd, so it never
 * holds the dev server's process open. Rejects if the port cannot be bound.
 *
 * The caller check reads `X-Forwarded-Host` unless `callerHostHeader` says
 * otherwise: Next's rewrite proxy sets it to the `Host` the browser
 * addressed (overwriting any the client sent) and points `Host` at the
 * relay. A request that reaches the relay directly carries none, and is
 * refused.
 */
export function startOtelRelay(
  options: OtelProxyOptions = {},
): Promise<string> {
  const handle = createOtelProxyHandler({
    ...options,
    callerHostHeader: options.callerHostHeader ?? "x-forwarded-host",
  });
  const server = http.createServer((req, res) =>
    handle(req, res, req.url ?? "/"),
  );
  return new Promise<string>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const addr = server.address();
      if (typeof addr !== "object" || addr === null) {
        server.close();
        reject(new Error("otel relay: no address after listen"));
        return;
      }
      server.unref();
      resolve(`http://127.0.0.1:${addr.port}`);
    });
  });
}
