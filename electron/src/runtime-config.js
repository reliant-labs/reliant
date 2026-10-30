/**
 * The packaged renderer's runtime config document: what `app://bundle/config.js`
 * answers.
 *
 * The web bundle reads every per-environment value from config.js
 * (window.__FORGE_CONFIG__, typed by frontends/web/src/lib/config_gen.ts) and
 * nothing at build time, so ONE build serves the hosted SPA and the desktop app.
 * On the web, control-plane renders config.js from the frontend's
 * `runtime_config`. In the desktop app there is no server: the main process
 * answers the request itself (app-protocol.js), from two layers, later wins:
 *
 *   1. the RELEASE document — electron/release.config.json's `renderer` block,
 *      projected from control-plane's KCL (reliant_desktop_release_config) and
 *      written into src/release-renderer-config.js by
 *      .github/scripts/generate-build-config.mjs. Same keys as the hosted
 *      SPA's runtime_config.
 *   2. the main process's RESOLVED endpoints — what BackendManager points the
 *      daemon at (build-config.js, overridable from the environment). So a
 *      packaged app launched with RELIANT_API_URL=… re-targets its renderer
 *      and its daemon together, instead of the renderer staying on the
 *      release's hosts while the daemon moves.
 *
 * An OSS build from source has no release document: the renderer then gets
 * the resolved endpoints alone (the server its daemon dials) and no
 * AUTH_MODE, i.e. the self-hosted app.
 *
 * Everything here is PUBLIC: it is served to the renderer.
 */

const RELEASE_RENDERER_CONFIG_MODULE = "./release-renderer-config";

/**
 * The release `renderer` block, or {} when this build has none (dev, an OSS
 * build from source).
 *
 * @param {(id: string) => unknown} [load] - module loader, injectable for tests
 * @returns {Record<string, string>}
 */
function loadReleaseRendererConfig(load = require) {
  try {
    const doc = load(RELEASE_RENDERER_CONFIG_MODULE);
    return doc && typeof doc === "object" ? { ...doc } : {};
  } catch {
    return {};
  }
}

/**
 * The document, release values first and the resolved endpoints over them.
 * A resolved value only overrides when it is set: an unset override must not
 * blank a release value.
 *
 * @param {object} input
 * @param {Record<string, string>} input.release - loadReleaseRendererConfig()
 * @param {{ rendererApiUrl?: string, apiUrl?: string, gatewayUrl?: string, controlPlaneUrl?: string }} [input.status]
 *   - BackendManager.getStatus(); absent before the backend manager exists
 * @param {Record<string, string | undefined>} [input.env] - the main process's environment
 * @returns {Record<string, string>}
 */
function resolveRendererConfig({ release, status = {}, env = {} }) {
  const doc = {};
  for (const [key, value] of Object.entries(release || {})) {
    if (typeof value === "string") doc[key] = value;
  }
  const resolved = {
    API_URL: status.rendererApiUrl || status.apiUrl,
    GATEWAY_URL: status.gatewayUrl,
    CONTROL_PLANE_API_URL: status.controlPlaneUrl,
    SUPABASE_URL: env.SUPABASE_URL,
    SUPABASE_ANON_KEY: env.SUPABASE_ANON_KEY,
  };
  for (const [key, value] of Object.entries(resolved)) {
    if (typeof value === "string" && value.trim() !== "") doc[key] = value;
  }
  return doc;
}

/**
 * config.js's body: the same shape forge renders for the web
 * (`window.__FORGE_CONFIG__ = {...};`). JSON.stringify escapes nothing that
 * matters inside a script served as its own file, but `<` is escaped anyway so
 * the body stays safe if it is ever inlined.
 *
 * @param {Record<string, string>} doc
 * @returns {string}
 */
function configScript(doc) {
  const json = JSON.stringify(doc, null, 2).replace(/</g, "\\u003c");
  return (
    "// Runtime configuration for the Reliant desktop renderer, answered by the\n" +
    "// main process (electron/src/runtime-config.js). Everything here is PUBLIC.\n" +
    `window.__FORGE_CONFIG__ = ${json};\n`
  );
}

module.exports = {
  RELEASE_RENDERER_CONFIG_MODULE,
  configScript,
  loadReleaseRendererConfig,
  resolveRendererConfig,
};
