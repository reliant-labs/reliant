// yours: scaffolded once, never touched again — forge will not overwrite this file
// Dev-server receiver for installDevLogging(): prints the browser's console lines and uncaught errors
// to stdout, which `forge env up` tees to .forge/logs/<env>/frontend_<name>.log. 404 in production.
// The folder is `%5F_forge` because Next treats `_`-prefixed folders as private (a silent 404): keep it.
// POST only, so a static export leaves it out. Depth: `forge skill load debug`.

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

  // The client endpoint is path-only: payloads belong in POST bodies. Next's
  // own request logger is separately excluded in next.config.ts, since rejecting
  // a query here alone would still leak it through that upstream logger.
  if (new URL(request.url).search) {
    return new Response(null, { status: 400, headers: { "X-Forge-Devlog": "2" } });
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
