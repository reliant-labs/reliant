// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// forgeRuntimeConfig — a Vite plugin that serves the frontend's runtime
// config document (`config.js`, which sets `window.__FORGE_CONFIG__`) from a
// file forge rendered OUTSIDE the frontend's own tree.
//
// # Why
//
// A frontend normally carries its dev document at `public/config.js`, written
// by `forge generate` / `forge env up` in the project that owns the frontend.
// A frontend whose code lives in ANOTHER repository — a sibling checkout
// (`path: "../reliant/web"`) in dev, a source pin when building — has no
// directory forge may write into: putting one repo's rendered environment in
// another repo's working tree is a diff in someone else's `git status`. So the
// project that declares the environment renders the document to its own
// `.forge/runtime-config/<frontend>/config.js`, and `forge env up` hands the
// path to the frontend's dev server as FORGE_RUNTIME_CONFIG_FILE. This plugin
// is the dev server's half: it answers `<base>config.js` from that file.
//
// With the variable unset (the frontend's own repo runs its own dev server,
// or a build) the plugin does nothing, and `public/config.js` is served as
// before — so adding it to a vite.config.ts is safe in every mode.
//
// The file is read on EVERY request rather than once: `forge env up`
// re-renders it after identity and jobs converge, and a page reload must see
// that, not the value from when the dev server started. A change also sends
// the page a full reload, so an open tab picks it up without a click.
//
// Structural types only: this module imports nothing from `vite`, so the
// package takes no dependency on any Vite major. The plugin objects are
// assignable to Vite's `Plugin` (vite.test.ts pins that).
//
// forgeOtelProxy, below, is the second plugin: the dev server's `/_otel`
// browser-telemetry proxy, mounted over ./otel-proxy. forgeDevLog, the third,
// is the receiving half of installDevLogging(): it prints the browser's
// console lines on the dev server's stdout. forgeSiteFiles (vite-site-files.ts)
// is the one build plugin: it writes robots.txt and sitemap.xml.
import { readFile } from "node:fs/promises";
import type { IncomingMessage, ServerResponse } from "node:http";
import { resolve } from "node:path";

import {
  createOtelProxyHandler,
  OTEL_PROXY_PREFIX,
  type OtelProxyOptions,
} from "./otel-proxy.js";

export type { OtelProxyOptions } from "./otel-proxy.js";

// forgeSiteFiles is the build-time plugin: robots.txt and sitemap.xml for the
// site's absolute SITE_URL. Its own module, re-exported here, because it shares
// nothing with the dev-server plugins below.
export {
  forgeSiteFiles,
  type ForgeSiteFilesOptions,
  type ForgeSiteFilesPlugin,
  type SiteFilesConfig,
  type SiteFilesContext,
} from "./vite-site-files.js";

/**
 * The environment variable `forge env up` sets on a frontend's dev server
 * when the frontend's runtime config document was rendered outside its tree.
 * Its value is an absolute path.
 */
export const FORGE_RUNTIME_CONFIG_FILE_ENV = "FORGE_RUNTIME_CONFIG_FILE";

/** The document's file name, which is also the path it is served at. */
const CONFIG_JS = "config.js";

type NextFn = (err?: unknown) => void;
type Middleware = (
  req: IncomingMessage,
  res: ServerResponse,
  next: NextFn,
) => void;

/** The part of a Vite dev / preview server this plugin uses. */
export interface RuntimeConfigServer {
  config: {
    base: string;
    logger: { warn(msg: string): void };
  };
  middlewares: { use(fn: Middleware): unknown };
  watcher?: {
    add(paths: string | readonly string[]): unknown;
    on(event: "change", listener: (path: string) => void): unknown;
  };
  ws?: { send(payload: { type: "full-reload"; path?: string }): void };
}

export interface ForgeRuntimeConfigOptions {
  /**
   * The document to serve. Defaults to `process.env.FORGE_RUNTIME_CONFIG_FILE`
   * as the dev server sees it. Empty or unset makes the plugin inert.
   */
  file?: string;
}

/** The plugin object; structurally a Vite `Plugin`. */
export interface ForgeRuntimeConfigPlugin {
  name: string;
  configureServer(server: RuntimeConfigServer): void;
  configurePreviewServer(server: RuntimeConfigServer): void;
}

/**
 * configPath is the request path the document is served at under `base`.
 * Vite normalizes a dev server's base to "/" or "/prefix/"; a relative base
 * ("./", a build-only setting) is served from the root.
 */
function configPath(base: string): string {
  const b = base.startsWith("/") ? base : "/";
  return (b.endsWith("/") ? b : `${b}/`) + CONFIG_JS;
}

function requestPath(req: IncomingMessage): string {
  const url = req.url ?? "";
  const q = url.search(/[?#]/);
  return q < 0 ? url : url.slice(0, q);
}

/**
 * forgeRuntimeConfig serves `<base>config.js` from the file forge rendered
 * for this frontend, when there is one. Add it to `plugins` in
 * vite.config.ts:
 *
 * ```ts
 * import { forgeRuntimeConfig } from "@reliantlabs/forge-web-runtime/vite";
 * export default defineConfig({ plugins: [react(), forgeRuntimeConfig()] });
 * ```
 */
export function forgeRuntimeConfig(
  options: ForgeRuntimeConfigOptions = {},
): ForgeRuntimeConfigPlugin {
  const raw = options.file ?? process.env[FORGE_RUNTIME_CONFIG_FILE_ENV] ?? "";
  const file = raw.trim() === "" ? "" : resolve(raw.trim());

  const install = (server: RuntimeConfigServer, live: boolean): void => {
    if (file === "") return;
    const path = configPath(server.config.base);
    server.middlewares.use((req, res, next) => {
      if (
        (req.method !== "GET" && req.method !== "HEAD") ||
        requestPath(req) !== path
      ) {
        next();
        return;
      }
      readFile(file, "utf8").then(
        (body) => {
          res.statusCode = 200;
          res.setHeader("Content-Type", "text/javascript; charset=utf-8");
          // The document is per environment and re-rendered while the dev
          // server runs; a cached copy is a stale environment.
          res.setHeader("Cache-Control", "no-store");
          res.end(req.method === "HEAD" ? undefined : body);
        },
        (err: unknown) => {
          // Loud, not silent: the page will fall back to whatever
          // public/config.js says (or to config_gen.ts's defaults), and the
          // only clue is this line in the dev server's log.
          server.config.logger.warn(
            `[forge-runtime-config] ${FORGE_RUNTIME_CONFIG_FILE_ENV}=${file} is unreadable ` +
              `(${err instanceof Error ? err.message : String(err)}); ` +
              `serving ${path} from the frontend's own tree instead. ` +
              "Re-run `forge env up` in the project that declares this frontend.",
          );
          next();
        },
      );
    });
    if (live && server.watcher) {
      server.watcher.add(file);
      server.watcher.on("change", (changed) => {
        if (resolve(changed) === file) {
          server.ws?.send({ type: "full-reload", path: "*" });
        }
      });
    }
  };

  return {
    name: "forge-runtime-config",
    configureServer(server) {
      install(server, true);
    },
    configurePreviewServer(server) {
      install(server, false);
    },
  };
}

/** The part of a Vite dev / preview server forgeOtelProxy uses. */
export interface OtelProxyServer {
  middlewares: { use(fn: Middleware): unknown };
}

export interface ForgeOtelProxyOptions extends OtelProxyOptions {
  /** The path prefix the browser sends telemetry under. Default "/_otel". */
  prefix?: string;
}

/** The plugin object; structurally a Vite `Plugin`. */
export interface ForgeOtelProxyPlugin {
  name: string;
  configureServer(server: OtelProxyServer): void;
  configurePreviewServer(server: OtelProxyServer): void;
}

/**
 * forgeOtelProxy is the dev server's `/_otel` browser-telemetry proxy (see
 * ./otel-proxy for the rules it enforces): a same-origin page's
 * `POST /_otel/v1/{traces,logs,metrics}` goes to the collector at
 * OTEL_EXPORTER_OTLP_ENDPOINT with only its content-type and content-encoding
 * and the headers in FORGE_OTEL_HEADERS_FILE; every other request under
 * `/_otel` — a foreign or Origin-less caller included — is refused without
 * contacting the collector. Requests outside the prefix pass through
 * untouched.
 *
 * It replaces a `server.proxy["/_otel"]` entry, which forwards every path
 * and method and cannot bound a body. Plugin middleware runs before Vite's
 * proxy, so a leftover entry is shadowed rather than reached.
 *
 * ```ts
 * import { forgeOtelProxy } from "@reliantlabs/forge-web-runtime/vite";
 * export default defineConfig({
 *   plugins: [react(), forgeOtelProxy({ upstream: "http://127.0.0.1:4318" })],
 * });
 * ```
 */
export function forgeOtelProxy(
  options: ForgeOtelProxyOptions = {},
): ForgeOtelProxyPlugin {
  const prefix = (options.prefix ?? OTEL_PROXY_PREFIX).replace(/\/+$/, "");
  const handle = createOtelProxyHandler(options);
  const install = (server: OtelProxyServer): void => {
    server.middlewares.use((req, res, next) => {
      const url = req.url ?? "";
      // `/_otel`, `/_otel/…` and `/_otel?…` are the proxy's; `/_otelx` is not.
      const rest = url.startsWith(prefix) ? url.slice(prefix.length) : null;
      if (rest === null || (rest !== "" && !/^[/?#]/.test(rest))) {
        next();
        return;
      }
      handle(req, res, rest);
    });
  };
  return {
    name: "forge-otel-proxy",
    configureServer: install,
    configurePreviewServer: install,
  };
}

// ── forgeDevLog ─────────────────────────────────────────────────────────
//
// The dev-server half of browser log forwarding. The browser console is a dead
// end for anyone who is not sitting in front of devtools — which includes every
// LLM agent debugging the app. installDevLogging() (the barrel) posts each
// console line, uncaught error and unhandled rejection to this endpoint, and
// the plugin prints them to the dev server's stdout, which `forge env up` tees
// to `.forge/logs/<env>/frontend_<name>.log`, next to the backend's logs.
//
// It is the plugin forge used to scaffold into every Vite frontend as
// src/lib/devlog-plugin.ts, moved here unchanged in behaviour, so a fix reaches
// every app with a runtime bump instead of a hand edit.
//
// The wire protocol. v2 (current) posts a BATCH, because one request per
// console line saturated the browser's six-connections-per-origin pool and
// slowed the app's own RPCs:
//
//   {"entries":[{"level":"info","msg":"…"},{"level":"error","msg":"…"}]}
//
// v1 (legacy) posts one line per request, and is accepted forever — an older
// runtime in some other frontend, or a cached bundle, still speaks it:
//
//   {"level":"info","msg":"…"}
//
// Two details that look incidental and are not:
//
//   - The body is parsed regardless of Content-Type. The client's unload flush
//     uses navigator.sendBeacon, which can only send text/plain without
//     triggering a CORS preflight it is unable to make.
//   - Every answered post carries `X-Forge-Devlog: 2`. That header is how the
//     client learns this endpoint understands batches; without it, it falls
//     back to one request per line.
//
// A malformed or oversized post prints a warn line rather than being swallowed:
// a silent drop is indistinguishable from "my code never ran".
//
// DEV ONLY, structurally: `apply: "serve"` means Vite loads it for the dev
// server and never for `vite build`, so the endpoint cannot exist in a
// production bundle.

/**
 * The endpoint installDevLogging() posts to. The same value as devlog.ts's
 * DEV_LOG_ENDPOINT (vite.test.ts holds them equal); not imported from it,
 * because that module is the browser half and this one runs in Node.
 */
const DEV_LOG_PATH = "/__forge/log";

/** Cap a single log line so a runaway loop cannot fill the disk. */
const DEV_LOG_MAX_LINE = 8_000;

/** Cap a whole post. A batch is capped at 32 KiB client-side; this is slack. */
const DEV_LOG_MAX_BODY = 1024 * 1024;

/** Levels that print as themselves. Anything else is printed as `log`. */
const DEV_LOG_LEVELS = new Set(["log", "info", "warn", "error", "debug"]);

interface DevLogEntry {
  level?: unknown;
  msg?: unknown;
}

interface DevLogBody extends DevLogEntry {
  entries?: unknown;
}

function formatDevLogEntry(entry: DevLogEntry): string {
  const level =
    typeof entry.level === "string" && DEV_LOG_LEVELS.has(entry.level)
      ? entry.level
      : "log";
  const msg = typeof entry.msg === "string" ? entry.msg : "";
  const line =
    msg.length > DEV_LOG_MAX_LINE
      ? `${msg.slice(0, DEV_LOG_MAX_LINE)}… (truncated)`
      : msg;
  return `[browser:${level}] ${line}`;
}

/** Turn a posted body into an HTTP status and the lines to print. */
function handleDevLogBody(
  body: string,
  size: number,
): { status: number; lines: string[] } {
  if (size > DEV_LOG_MAX_BODY) {
    return {
      status: 413,
      lines: [
        `[browser:warn] [forge-devlog] dropped oversized post (${size} bytes)`,
      ],
    };
  }
  let parsed: DevLogBody;
  try {
    parsed = JSON.parse(body) as DevLogBody;
  } catch {
    return {
      status: 400,
      lines: ["[browser:warn] [forge-devlog] dropped malformed post"],
    };
  }
  if (parsed === null || typeof parsed !== "object") {
    return {
      status: 400,
      lines: ["[browser:warn] [forge-devlog] dropped malformed post"],
    };
  }
  // v2 batch, else the object itself is a v1 single entry.
  const entries: DevLogEntry[] = Array.isArray(parsed.entries)
    ? (parsed.entries as unknown[]).filter(
        (e): e is DevLogEntry => e !== null && typeof e === "object",
      )
    : [parsed];
  return { status: 204, lines: entries.map(formatDevLogEntry) };
}

/** The part of a Vite dev server forgeDevLog uses. */
export interface DevLogServer {
  middlewares: { use(fn: Middleware): unknown };
}

export interface ForgeDevLogOptions {
  /**
   * The path the browser posts to. Default "/__forge/log", installDevLogging's
   * default; change both together.
   */
  endpoint?: string;
}

/** The plugin object; structurally a Vite `Plugin`. */
export interface ForgeDevLogPlugin {
  name: string;
  apply: "serve";
  configureServer(server: DevLogServer): void;
}

/**
 * forgeDevLog receives the browser log lines installDevLogging() posts and
 * prints them, `[browser:<level>] <line>`, on the dev server's stdout, where
 * `forge env up` writes them to `.forge/logs/<env>/frontend_<name>.log`.
 * Dev server only; requests to any other path pass through.
 *
 * ```ts
 * import { forgeDevLog } from "@reliantlabs/forge-web-runtime/vite";
 * export default defineConfig({ plugins: [react(), forgeDevLog()] });
 * ```
 */
export function forgeDevLog(
  options: ForgeDevLogOptions = {},
): ForgeDevLogPlugin {
  const endpoint = (options.endpoint ?? DEV_LOG_PATH).replace(/\/+$/, "");
  return {
    name: "forge-dev-log",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        // Own only the endpoint, not routes beneath it; ignore query strings.
        const path = requestPath(req);
        if (path !== endpoint) {
          next();
          return;
        }
        if (req.method !== "POST") {
          res.statusCode = 405;
          res.end();
          return;
        }
        let body = "";
        let size = 0;
        req.on("data", (chunk: Buffer) => {
          size += chunk.length;
          // Keep DRAINING past the cap — stopping mid-stream leaves the socket
          // half-read and the client waiting — but stop accumulating.
          if (size <= DEV_LOG_MAX_BODY) body += chunk.toString();
        });
        req.on("end", () => {
          const { status, lines } = handleDevLogBody(body, size);
          if (lines.length > 0) {
            // ONE write for the whole batch: a second concurrent writer can
            // interleave BETWEEN calls, splicing unrelated lines into it. This
            // console.log IS the sink: stdout is what `forge env up` tees.
            console.log(lines.join("\n"));
          }
          res.statusCode = status;
          // Tells a v2 client its batches were understood. Set on 4xx too: the
          // post was rejected, but the protocol was not the reason.
          res.setHeader("X-Forge-Devlog", "2");
          res.end();
        });
      });
    },
  };
}
