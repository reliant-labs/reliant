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
 */

/** Endpoint the client posts to. Keep in sync with lib/browser-log-forward.ts. */
export const BROWSER_LOG_ENDPOINT = "/__forge/log";

/** Cap one line so a render loop cannot flood the log. */
const MAX_LINE = 8_000;

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
        let tooLong = false;
        req.on("data", (chunk: Buffer) => {
          if (tooLong) return;
          body += chunk.toString();
          if (body.length > MAX_LINE * 2) tooLong = true;
        });

        req.on("end", () => {
          try {
            const { level = "log", msg = "" } = JSON.parse(body || "{}") as {
              level?: string;
              msg?: string;
            };
            const line =
              msg.length > MAX_LINE
                ? `${msg.slice(0, MAX_LINE)}… (truncated)`
                : msg;
            // This IS the log sink: stdout is what forge tees to disk.
            console.log(`[browser:${level}] ${line}`);
          } catch {
            // A malformed post is not worth failing a dev request over.
          }
          res.statusCode = 204;
          res.end();
        });
      });
    },
  };
}
