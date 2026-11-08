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
// dev server — another device on the LAN, a workspace preview URL, a tunnel —
// can use it. A proxy that forwards every path and method would let any of
// them submit arbitrary routes or unbounded bodies to the edge AS THIS STACK.
// So it forwards exactly what a browser exporter sends and nothing else:
//
//   - `POST <prefix>/v1/traces`, `/v1/logs` and `/v1/metrics`. Any other
//     method on those paths is 405, any other path (traversal spellings, a
//     trailing slash and percent-encoding included: the path is compared
//     byte for byte, never decoded) is 404. Neither contacts the upstream.
//   - a body of at most 8 MiB, counted while it streams in and buffered
//     before anything is sent upstream; above that, 413, and the upstream
//     never sees a byte of it.
//   - the page's cookies and its own authorization header are never
//     forwarded, whether or not there is a token to attach. Neither is a
//     cookie the upstream tries to set.
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

// Request headers never forwarded: the page's credentials, and everything
// that frames THIS hop (the proxy sets its own host and content-length).
const DROPPED_REQUEST_HEADERS = new Set([
  "authorization",
  "proxy-authorization",
  "cookie",
  "host",
  "content-length",
  "transfer-encoding",
  "connection",
  "keep-alive",
  "proxy-connection",
  "te",
  "trailer",
  "upgrade",
  "expect",
]);

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

function forwardedHeaders(
  incoming: IncomingHttpHeaders,
  extra: Array<[string, string]>,
  host: string,
  length: number,
): OutgoingHttpHeaders {
  const out: OutgoingHttpHeaders = {};
  for (const [name, value] of Object.entries(incoming)) {
    if (value === undefined || DROPPED_REQUEST_HEADERS.has(name)) continue;
    out[name] = value;
  }
  for (const [name, value] of extra) out[name] = value;
  out.host = host;
  out["content-length"] = length;
  return out;
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
      reply(res, 404);
      return;
    }
    if (req.method !== "POST") {
      reply(res, 405, { allow: "POST" });
      return;
    }
    const declared = Number(req.headers["content-length"]);
    if (Number.isFinite(declared) && declared > max) {
      reply(res, 413);
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
 */
export function startOtelRelay(
  options: OtelProxyOptions = {},
): Promise<string> {
  const handle = createOtelProxyHandler(options);
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
