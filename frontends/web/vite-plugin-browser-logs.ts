import type { Plugin } from "vite";

/**
 * Dev-server half of browser-console forwarding.
 *
 * ── Where the lines end up, and why this writes no file ───────────────
 *
 * `forge env up` already tees every host process's stdout to
 * `control-plane/.forge/logs/<env>/<name>.log`, and this Vite server is one of
 * those processes (`frontend_reliant-web.log`). So the sink is simply
 * `console.log` — forge does the rest, and browser lines land next to
 * admin-server, reliant-api-server and the temporal worker, in one directory,
 * with one grep.
 *
 * An earlier version of this plugin wrote its own file under `data/logs/`.
 * That was a mistake worth naming: it created a FOURTH log location in a
 * project that already had too many, and it was invisible to anyone following
 * the forge logs — which is where everything else already is.
 *
 * This mirrors forge's own scaffold convention (`devLogPlugin` in
 * internal/templates/frontend/vite-spa), including the `[browser:<level>]`
 * prefix, so the documented greps work here too:
 *
 *   grep '\[browser:'       control-plane/.forge/logs/dev/frontend_reliant-web.log
 *   grep '\[browser:error\]' control-plane/.forge/logs/dev/*.log
 *
 * DEV ONLY, structurally: `apply: "serve"` means Vite loads this for the dev
 * server and never for `vite build`, so the endpoint cannot exist in a
 * production bundle.
 *
 * ── Protocol v2: batched ──────────────────────────────────────────────
 *
 * A client that posts once per console line pays a fetch per line on the main
 * thread, which is the cost this endpoint exists to avoid. v2 therefore takes a
 * BATCH — `{"entries":[{level,msg}, …]}` — and the whole batch becomes one
 * `console.log`, so N browser lines cost one request and one stdout write
 * instead of N of each.
 *
 * The v1 shape (`{level,msg}`, one line per post) is still accepted and always
 * will be: the two halves of this protocol live in different repos and ship on
 * different clocks, so the server must never require a client it cannot see.
 *
 * The body is parsed regardless of Content-Type, because `navigator.sendBeacon`
 * — how a batching client flushes on pagehide — sends `text/plain`.
 */

/** Endpoint the client posts to. Keep in sync with lib/browser-log-forward.ts. */
export const BROWSER_LOG_ENDPOINT = "/__forge/log";

/** Cap one line so a render loop cannot flood the log. */
export const MAX_LINE = 8_000;

/** Cap one post so a runaway batch cannot exhaust dev-server memory. */
export const MAX_BODY = 1024 * 1024;

/** Levels we print verbatim; anything else is printed as `log`. */
const LEVELS = new Set(["log", "info", "warn", "error", "debug"]);

interface DevLogEntry {
  level?: unknown;
  msg?: unknown;
}

export interface DevLogPost {
  /** The drained body, up to MAX_BODY. Meaningless when `overCap`. */
  body: string;
  /** Total bytes the client sent, including anything past the cap. */
  bytes: number;
  /** Whether the client exceeded MAX_BODY. */
  overCap: boolean;
}

export interface DevLogResult {
  status: 204 | 400 | 413;
  /** Stdout lines to emit, in order. Joined into ONE console.log by the caller. */
  lines: string[];
}

function formatEntry(entry: DevLogEntry): string {
  const rawLevel = typeof entry.level === "string" ? entry.level : "log";
  const level = LEVELS.has(rawLevel) ? rawLevel : "log";
  const msg = typeof entry.msg === "string" ? entry.msg : "";
  const line =
    msg.length > MAX_LINE ? `${msg.slice(0, MAX_LINE)}… (truncated)` : msg;
  return `[browser:${level}] ${line}`;
}

/**
 * Turn one posted body into the status code and the stdout lines it produces.
 *
 * Pure, so the protocol is testable without a dev server: the middleware below
 * only drains the request and applies what this returns.
 */
export function renderDevLogPost({
  body,
  bytes,
  overCap,
}: DevLogPost): DevLogResult {
  if (overCap) {
    return {
      status: 413,
      lines: [
        `[browser:warn] [forge-devlog] dropped oversized post (${bytes} bytes)`,
      ],
    };
  }

  if (!body.trim()) return { status: 204, lines: [] };

  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
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

  const entries = (parsed as { entries?: unknown }).entries;
  if (Array.isArray(entries)) {
    return {
      status: 204,
      lines: entries.map((entry) =>
        formatEntry(
          entry !== null && typeof entry === "object"
            ? (entry as DevLogEntry)
            : {},
        ),
      ),
    };
  }

  // v1: the body IS a single entry.
  return { status: 204, lines: [formatEntry(parsed as DevLogEntry)] };
}

export function browserLogSink(): Plugin {
  return {
    name: "reliant:browser-log-sink",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use(BROWSER_LOG_ENDPOINT, (req, res) => {
        if (req.method !== "POST") {
          res.statusCode = 405;
          res.end();
          return;
        }

        let body = "";
        let bytes = 0;
        let overCap = false;
        req.on("data", (chunk: Buffer) => {
          bytes += chunk.length;
          // Keep draining past the cap — abandoning the stream leaves the
          // socket half-read and the client's fetch hanging — but stop
          // accumulating, which is the memory we are actually protecting.
          if (overCap) return;
          if (bytes > MAX_BODY) {
            overCap = true;
            body = "";
            return;
          }
          body += chunk.toString();
        });

        req.on("end", () => {
          const { status, lines } = renderDevLogPost({ body, bytes, overCap });
          // This IS the log sink: stdout is what forge tees to disk. One call
          // per batch, so a 50-line flush is one write rather than 50.
          if (lines.length > 0) console.log(lines.join("\n"));
          res.statusCode = status;
          if (status === 204) res.setHeader("X-Forge-Devlog", "2");
          res.end();
        });
      });
    },
  };
}
