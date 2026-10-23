// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Dev-server half of browser log forwarding.
//
// The browser console is a dead end for anyone who is not sitting in front of
// devtools — which includes every LLM agent debugging this app. This plugin
// accepts the log lines that @reliantlabs/forge-web-runtime's
// installDevLogging() posts and prints them to the dev server's stdout, where
// `forge env up` is already tee-ing them to:
//
//   .forge/logs/<env>/frontend_<name>.log
//
// So a `console.error` in a React component — and any uncaught error or
// unhandled rejection, which produce no console call at all — lands in a file
// on disk next to the backend service logs.
//
// ── The wire protocol ────────────────────────────────────────────────────
//
// v2 (current) posts a BATCH, because one request per console line saturated
// the browser's six-connections-per-origin pool and measurably slowed the app's
// own RPCs:
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
//   - Every accepted post answers `X-Forge-Devlog: 2`. That header is how the
//     client learns this endpoint understands batches; without it, it falls
//     back to one request per line. So an endpoint that dropped the header
//     would still "work" — just one request per line again.
//
// A malformed or oversized post prints a warn line rather than being swallowed.
// A silent drop here is indistinguishable from "my code never ran", which is
// the single most expensive way for a log sink to fail.
//
// DEV ONLY, structurally: `apply: "serve"` means Vite loads this for the dev
// server and never for `vite build`, so the endpoint cannot exist in a
// production bundle.
//
// To turn it off: delete the `devLogPlugin()` entry from vite.config.ts (and
// the installDevLogging() call in src/main.tsx). Nothing else depends on it.

import type { Plugin } from "vite";

/** Endpoint the web-runtime client posts to. Keep in sync with DEV_LOG_ENDPOINT. */
const ENDPOINT = "/__forge/log";

/** Cap a single log line so a runaway loop cannot fill the disk. */
const MAX_LINE = 8_000;

/** Cap a whole post. A batch is capped at 32 KiB client-side; this is slack. */
const MAX_BODY = 1024 * 1024;

/** Levels that print as themselves. Anything else is printed as `log`. */
const LEVELS = new Set(["log", "info", "warn", "error", "debug"]);

interface DevLogEntry {
  level?: unknown;
  msg?: unknown;
}

interface DevLogBody extends DevLogEntry {
  entries?: unknown;
}

/** What handleDevLogBody decided: an HTTP status and the lines to print. */
interface DevLogResult {
  status: number;
  lines: string[];
}

function formatEntry(entry: DevLogEntry): string {
  const level =
    typeof entry.level === "string" && LEVELS.has(entry.level)
      ? entry.level
      : "log";
  const msg = typeof entry.msg === "string" ? entry.msg : "";
  const line =
    msg.length > MAX_LINE ? `${msg.slice(0, MAX_LINE)}… (truncated)` : msg;
  return `[browser:${level}] ${line}`;
}

/**
 * Turn a posted body into the lines to print. Pure, so it is unit-testable
 * without a dev server — the glue below is deliberately thin.
 */
export function handleDevLogBody(
  body: string,
  byteLength?: number,
): DevLogResult {
  const size = byteLength ?? Buffer.byteLength(body);
  if (size > MAX_BODY) {
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

  // v2 batch, else treat the object itself as a v1 single entry.
  const entries: DevLogEntry[] = Array.isArray(parsed.entries)
    ? (parsed.entries as DevLogEntry[]).filter(
        (e): e is DevLogEntry => e !== null && typeof e === "object",
      )
    : [parsed];

  return { status: 204, lines: entries.map(formatEntry) };
}

export function devLogPlugin(): Plugin {
  return {
    name: "forge-dev-log",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use(ENDPOINT, (req, res) => {
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
          if (size <= MAX_BODY) body += chunk.toString();
        });

        req.on("end", () => {
          const { status, lines } = handleDevLogBody(body, size);
          if (lines.length > 0) {
            // ONE console.log for the whole batch: a second concurrent writer
            // can interleave BETWEEN calls, and a batch split across calls
            // would have unrelated lines spliced into it.
            //
            // This console.log IS the log sink — it is what puts the browser
            // line on the dev server's stdout. No eslint-disable here: the
            // scaffold does not enable no-console, so a directive naming it is
            // an UNUSED directive, which ESLint 9 reports as a warning and the
            // lint-clean gate rejects.
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
