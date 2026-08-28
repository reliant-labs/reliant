/**
 * Client half of browser-console forwarding.
 *
 * Posts every console line to the Vite dev server, which prints it to its own
 * stdout. `forge env up` tees that to:
 *
 *   control-plane/.forge/logs/dev/frontend_reliant-web.log
 *
 * so frontend logs sit next to admin-server, reliant-api-server and the
 * temporal worker — one directory, one grep:
 *
 *   grep '\[browser:'        control-plane/.forge/logs/dev/frontend_reliant-web.log
 *   grep '\[browser:error\]' control-plane/.forge/logs/dev/*.log
 *
 * ── Why this runs in Electron too ─────────────────────────────────────
 *
 * The desktop app has its own file sink (electron-log via IPC), so an earlier
 * version skipped forwarding whenever `window.electronAPI` existed. That
 * assumption cost hours: the Electron renderer loads the SAME localhost origin
 * as a browser tab, so "am I in Electron" is invisible from the outside, and
 * when its electron-log sink broke the frontend went dark with no indication
 * why. Both paths now forward. Duplicate lines in two files are a trivial
 * cost; a silent gap in the only frontend log is not.
 */

const ENDPOINT = "/__forge/log";

let installed = false;

/** Depth-safe stringify — console args are often deep React/proto objects. */
function format(value: unknown): string {
  if (typeof value === "string") return value;
  if (value instanceof Error) {
    return `${value.name}: ${value.message}${value.stack ? `\n${value.stack}` : ""}`;
  }
  try {
    const seen = new WeakSet<object>();
    return (
      JSON.stringify(value, (_key, val) => {
        if (typeof val === "bigint") return `${val}n`;
        if (typeof val === "object" && val !== null) {
          if (seen.has(val)) return "[Circular]";
          seen.add(val);
        }
        return val;
      }) ?? String(value)
    );
  } catch {
    return String(value);
  }
}

function post(level: string, msg: string): void {
  try {
    // keepalive: the line explaining a navigation must survive that
    // navigation, and those are exactly the lines worth having.
    void fetch(ENDPOINT, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ level, msg }),
      keepalive: true,
    }).catch(() => {
      // Never let logging break the app, and never recurse into console.
    });
  } catch {
    // Same.
  }
}

/**
 * Begin forwarding console output to the dev server.
 *
 * Wraps `console.*` rather than introducing a new API, so existing call sites
 * — and everything `lib/logger.ts` funnels through them — are captured with no
 * changes.
 */
export function installBrowserLogForwarding(): void {
  if (installed) return;
  if (typeof window === "undefined") return;
  installed = true;

  const levels = ["log", "info", "warn", "error", "debug"] as const;
  for (const level of levels) {
    const original = console[level].bind(console);
    console[level] = (...args: unknown[]) => {
      original(...args);
      post(level, args.map(format).join(" "));
    };
  }

  // Uncaught errors and rejected promises produce NO console call, so they are
  // invisible to any amount of grepping for log statements — and they are the
  // failures most worth seeing.
  window.addEventListener("error", (event) => {
    post(
      "error",
      `[uncaught] ${event.message} @ ${event.filename}:${event.lineno}:${event.colno}`,
    );
  });
  window.addEventListener("unhandledrejection", (event) => {
    post("error", `[unhandled-rejection] ${format(event.reason)}`);
  });
}
