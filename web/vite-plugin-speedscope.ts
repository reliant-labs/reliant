import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
import type { Plugin } from "vite";

/**
 * Ships speedscope's prebuilt static app at `/speedscope/` — the flamegraph
 * viewer the observability area's Profiles view embeds in an iframe.
 *
 * ── Why an iframe of the static build, not a bundled import ───────────────
 *
 * speedscope publishes no library entry point: its npm package IS the built
 * viewer (`dist/release/`: index.html, one JS file, CSS, fonts), and its
 * documented embedding is to host that and pass a profile by URL
 * (`#profileURL=`). Hosting it as static files also keeps every byte of it out
 * of the app's JavaScript: nothing loads until a reader opens Profiles, and the
 * main bundle does not grow at all.
 *
 * The files are served from node_modules in dev and copied into the build
 * output verbatim, so the version is exactly the one package-lock.json pins and
 * the license gate inspects (speedscope is MIT). The packaged desktop app's
 * app:// handler serves any file in the bundle, so the same path works there.
 */

const MOUNT = "/speedscope/";

function releaseDir(): string {
  const require = createRequire(import.meta.url);
  return path.join(path.dirname(require.resolve("speedscope/package.json")), "dist", "release");
}

const CONTENT_TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json",
  ".wasm": "application/wasm",
  ".woff2": "font/woff2",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".txt": "text/plain; charset=utf-8",
  ".md": "text/markdown; charset=utf-8",
};

export function speedscopeStatic(): Plugin {
  let outDir = "dist";
  return {
    name: "reliant-speedscope-static",
    configResolved(config) {
      outDir = path.resolve(config.root, config.build.outDir);
    },
    configureServer(server) {
      const root = releaseDir();
      server.middlewares.use((req, res, next) => {
        if (!req.url?.startsWith(MOUNT)) return next();
        const rel = decodeURIComponent(req.url.slice(MOUNT.length).split(/[?#]/)[0] || "index.html");
        const file = path.resolve(root, rel);
        if (!file.startsWith(root + path.sep) || !fs.existsSync(file) || !fs.statSync(file).isFile()) return next();
        res.setHeader("Content-Type", CONTENT_TYPES[path.extname(file)] ?? "application/octet-stream");
        fs.createReadStream(file).pipe(res);
      });
    },
    writeBundle() {
      fs.cpSync(releaseDir(), path.join(outDir, "speedscope"), {
        recursive: true,
        // Source maps are excluded from the npm package already; the sample
        // profile is speedscope's demo data, which nothing here links to.
        filter: (src) => !src.endsWith(".map") && !path.basename(src).startsWith("perf-vertx-stacks"),
      });
    },
  };
}
