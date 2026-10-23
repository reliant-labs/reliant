// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Dev-server half of browser log forwarding (Next.js).
//
// The Vite scaffold does this with a `apply: "serve"` plugin; the App Router
// has no equivalent hook, so the receiver is an ordinary route handler that
// refuses to do anything in production.
//
// Why the folder is spelled `%5F_forge`: the App Router treats any folder
// whose name starts with `_` as PRIVATE — excluded from routing — so a
// literal `src/app/__forge/log/route.ts` compiles cleanly and then answers
// 404 to every post, silently dropping every browser line. `%5F` is the
// URL-encoded underscore, which is Next's documented escape for a URL
// segment that must begin with one: this folder serves `/__forge/log`,
// the path installDevLogging() posts to.
//
// It accepts the log lines that @reliantlabs/forge-web-runtime's
// installDevLogging() posts and prints them to the dev server's stdout, where
// `forge env up` is already tee-ing them to:
//
//   .forge/logs/<env>/frontend_<name>.log
//
// So a `console.error` in a component — and any uncaught error or unhandled
// rejection, which produce no console call at all — lands in a file on disk
// next to the backend service logs.
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
//   - The body is read as TEXT and parsed by hand, not via request.json().
//     The client's unload flush uses navigator.sendBeacon, which can only send
//     text/plain without triggering a CORS preflight it is unable to make;
//     request.json() is fine with that today but the contract is "parse
//     regardless of Content-Type", so the parse is explicit.
//   - Every accepted post answers `X-Forge-Devlog: 2`. That header is how the
//     client learns this endpoint understands batches; without it, it falls
//     back to one request per line.
//
// A malformed or oversized post prints a warn line rather than being swallowed.
// A silent drop here is indistinguishable from "my code never ran", which is
// the single most expensive way for a log sink to fail.
//
// The production guard is the first statement in the handler: `next build`
// still compiles this route, so unlike the Vite plugin it CAN exist in a
// deployed app, and must answer 404 there. installDevLogging() also no-ops in
// production, so nothing posts to it in the first place.
//
// It is also why this route does not break a static export (`output:
// static`): it answers POST only, and `output: "export"` leaves a POST-only
// handler out of `out/` rather than failing the build. Adding a GET export
// here would change that — an exported GET handler must be static.
//
// To turn it off: delete this file and the installDevLogging() call in
// src/app/providers.tsx. Nothing else depends on it.

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
  const level = typeof entry.level === "string" && LEVELS.has(entry.level) ? entry.level : "log";
  const msg = typeof entry.msg === "string" ? entry.msg : "";
  const line = msg.length > MAX_LINE ? `${msg.slice(0, MAX_LINE)}… (truncated)` : msg;
  return `[browser:${level}] ${line}`;
}

/**
 * Turn a posted body into the lines to print. Pure, so it is unit-testable
 * without a running dev server — the handler below is deliberately thin.
 */
export function handleDevLogBody(body: string): DevLogResult {
  const size = new TextEncoder().encode(body).length;
  if (size > MAX_BODY) {
    return {
      status: 413,
      lines: [`[browser:warn] [forge-devlog] dropped oversized post (${size} bytes)`],
    };
  }

  let parsed: DevLogBody;
  try {
    parsed = JSON.parse(body) as DevLogBody;
  } catch {
    return { status: 400, lines: ["[browser:warn] [forge-devlog] dropped malformed post"] };
  }

  if (parsed === null || typeof parsed !== "object") {
    return { status: 400, lines: ["[browser:warn] [forge-devlog] dropped malformed post"] };
  }

  // v2 batch, else treat the object itself as a v1 single entry.
  const entries: DevLogEntry[] = Array.isArray(parsed.entries)
    ? (parsed.entries as DevLogEntry[]).filter(
        (e): e is DevLogEntry => e !== null && typeof e === "object",
      )
    : [parsed];

  return { status: 204, lines: entries.map(formatEntry) };
}

export async function POST(request: Request): Promise<Response> {
  if (process.env.NODE_ENV === "production") {
    return new Response(null, { status: 404 });
  }

  const { status, lines } = handleDevLogBody(await request.text());
  if (lines.length > 0) {
    // ONE console.log for the whole batch: a second concurrent writer can
    // interleave BETWEEN calls, and a batch split across calls would have
    // unrelated lines spliced into it.
    //
    // This console.log IS the log sink — it is what puts the browser line on
    // the dev server's stdout. No eslint-disable here: the scaffold does not
    // enable no-console, so a directive naming it is an UNUSED directive,
    // which ESLint 9 reports as a warning and the lint-clean gate rejects.
    console.log(lines.join("\n"));
  }

  // Tells a v2 client its batches were understood. Set on 4xx too: the post
  // was rejected, but the protocol was not the reason.
  return new Response(null, { status, headers: { "X-Forge-Devlog": "2" } });
}
