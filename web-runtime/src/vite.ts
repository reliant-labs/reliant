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
// package takes no dependency on any Vite major. The plugin object is
// assignable to Vite's `Plugin` (vite.test.ts pins that).
import { readFile } from "node:fs/promises";
import type { IncomingMessage, ServerResponse } from "node:http";
import { resolve } from "node:path";

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
