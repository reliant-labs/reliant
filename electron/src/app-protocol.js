/**
 * The `app://` scheme that serves the packaged renderer bundle.
 *
 * Packaged builds used to load the renderer with `loadFile(index.html)`, which
 * gives the page a `file://` origin. That is incompatible with the bundle Vite
 * actually produces:
 *
 *   - Vite builds with `base: "/"` because the same bundle is deployed to
 *     app.reliantlabs.io, where a relative base breaks asset resolution on any
 *     2+-segment route. So index.html asks for `/assets/index-<hash>.js`.
 *   - Under `file://`, a root-absolute path resolves against the FILESYSTEM
 *     root — the renderer requested `file:///assets/index-<hash>.js`, which
 *     does not exist, so no script ever executed and the window stayed blank.
 *
 * The failure was silent by construction: index.html itself loads fine, so
 * `did-finish-load` fires and `did-fail-load` never does. Nothing in the main
 * process could tell that the app had not started.
 *
 * A custom scheme fixes both halves at once. `app://bundle/` is a real origin
 * with a real root, so `/assets/...` resolves inside the packaged web
 * directory, and a deep route like `/chat/abc` can fall back to index.html the
 * way a web server's SPA fallback does — which `file://` cannot do, since
 * `history.pushState` there yields `file:///chat/abc` and a reload 404s.
 *
 * Registering it as `standard` + `secure` is what makes the origin behave:
 * without it the renderer is treated as an opaque origin and localStorage,
 * which every Zustand persist store depends on, throws on access.
 */

const path = require("path");
const fs = require("fs");

const APP_SCHEME = "app";
const APP_HOST = "bundle";
const APP_ORIGIN = `${APP_SCHEME}://${APP_HOST}`;
const APP_INDEX_URL = `${APP_ORIGIN}/`;

const MIME_TYPES = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".mjs": "text/javascript",
  ".css": "text/css",
  ".json": "application/json",
  ".map": "application/json",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".webp": "image/webp",
  ".ico": "image/x-icon",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".ttf": "font/ttf",
  ".otf": "font/otf",
  ".wasm": "application/wasm",
  ".txt": "text/plain",
};

/**
 * Content type for a file path, defaulting to octet-stream.
 *
 * Getting this right matters more than it looks: a module script served as
 * anything but a JavaScript MIME type is rejected outright by the module
 * loader, which reproduces the blank window this scheme exists to prevent.
 *
 * @param {string} filePath
 * @returns {string}
 */
function contentTypeFor(filePath) {
  return MIME_TYPES[path.extname(filePath).toLowerCase()] || "application/octet-stream";
}

/**
 * Map a request URL onto a file inside the packaged web root.
 *
 * Split out from the protocol handler so the interesting cases — traversal
 * attempts, SPA fallback, encoded paths — are unit-testable without booting
 * Electron.
 *
 * @param {string} requestUrl - Full `app://` URL being requested
 * @param {string} webRoot - Absolute path to the packaged web directory
 * @param {(p: string) => boolean} [isFile] - Existence probe, injectable for tests
 * @returns {{ filePath: string, isFallback: boolean } | null} null if the
 *   request escapes the web root and must be refused
 */
function resolveRequestPath(requestUrl, webRoot, isFile = defaultIsFile) {
  let pathname;
  try {
    ({ pathname } = new URL(requestUrl));
  } catch {
    return null;
  }

  // decodeURIComponent throws on malformed escapes (e.g. "%zz"); a request we
  // cannot decode is one we cannot safely resolve.
  let decoded;
  try {
    decoded = decodeURIComponent(pathname);
  } catch {
    return null;
  }

  const root = path.resolve(webRoot);
  const candidate = path.resolve(root, "." + normalizeSeparators(decoded));

  // Refuse anything that resolves outside the bundle. `path.resolve` has
  // already collapsed any "..", so a prefix check is sufficient — but it must
  // be a path-segment prefix, or "/webhook" would pass a check against "/web".
  if (candidate !== root && !candidate.startsWith(root + path.sep)) {
    return null;
  }

  if (isFile(candidate)) {
    return { filePath: candidate, isFallback: false };
  }

  // SPA fallback: a client route such as /chat/abc has no file behind it and
  // must be answered with index.html so the router can take over. Requests
  // that look like assets are NOT rewritten — answering a missing .js with
  // HTML is precisely the "Expected a JavaScript module" failure mode, and a
  // real 404 is far easier to diagnose than an HTML file pretending to be code.
  if (path.extname(candidate) === "") {
    const indexPath = path.join(root, "index.html");
    if (isFile(indexPath)) {
      return { filePath: indexPath, isFallback: true };
    }
  }

  return null;
}

/**
 * Windows paths arrive with forward slashes in the URL; keep resolution
 * platform-correct by converting before handing to path.resolve.
 */
function normalizeSeparators(urlPath) {
  return path.sep === "\\" ? urlPath.replace(/\//g, "\\") : urlPath;
}

function defaultIsFile(candidate) {
  try {
    return fs.statSync(candidate).isFile();
  } catch {
    return false;
  }
}

/**
 * Declare the scheme's privileges. Must run before `app.whenReady()`.
 *
 * @param {import('electron').Protocol} protocol
 */
function registerSchemePrivileges(protocol) {
  protocol.registerSchemesAsPrivileged([
    {
      scheme: APP_SCHEME,
      privileges: {
        standard: true, // real origin => localStorage/IndexedDB work
        secure: true, // treated as a secure context (crypto.subtle, etc.)
        supportFetchAPI: true,
        corsEnabled: true,
        stream: true, // range requests, for media
      },
    },
  ]);
}

/**
 * Serve the packaged web directory over `app://`. Call after the app is ready.
 *
 * @param {import('electron').Protocol} protocol
 * @param {string} webRoot - Absolute path to the packaged web directory
 * @param {{ error: Function, warn: Function }} log
 */
function registerAppProtocol(protocol, webRoot, log) {
  // The scheme is passed WITHOUT a trailing colon. `protocol.handle("app:")`
  // does not throw — it silently registers nothing, and every load then fails
  // with ERR_FAILED before the handler is consulted. Verified against Electron
  // 39 with protocol.isProtocolHandled(), which is asserted below.
  protocol.handle(APP_SCHEME, async (request) => {
    const resolved = resolveRequestPath(request.url, webRoot);

    if (!resolved) {
      log.warn("[AppProtocol] refusing request:", request.url);
      return new Response("Not found", {
        status: 404,
        headers: { "content-type": "text/plain" },
      });
    }

    try {
      const body = await fs.promises.readFile(resolved.filePath);
      return new Response(body, {
        status: 200,
        headers: { "content-type": contentTypeFor(resolved.filePath) },
      });
    } catch (err) {
      // A read failure here means the bundle is damaged. Say so loudly: this
      // is the class of fault that otherwise presents as an empty window.
      log.error("[AppProtocol] failed to read", resolved.filePath, err.message);
      return new Response("Internal error", {
        status: 500,
        headers: { "content-type": "text/plain" },
      });
    }
  });

  // Fail loudly if registration did not take. A mis-registered scheme produces
  // exactly the symptom this whole change exists to eliminate: a window that
  // loads nothing, with no error attributable to a cause.
  if (typeof protocol.isProtocolHandled === "function" && !protocol.isProtocolHandled(APP_SCHEME)) {
    throw new Error(
      `app-protocol: ${APP_SCHEME}:// did not register — the renderer cannot load. ` +
        `Check that registerSchemePrivileges() ran before app ready.`
    );
  }
}

module.exports = {
  APP_SCHEME,
  APP_ORIGIN,
  APP_INDEX_URL,
  contentTypeFor,
  registerAppProtocol,
  registerSchemePrivileges,
  resolveRequestPath,
};
