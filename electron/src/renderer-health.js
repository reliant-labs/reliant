/**
 * Detects the failure mode where the renderer loads but never starts.
 *
 * `did-fail-load` only fires when the top-level document fails. When
 * index.html loads correctly but its module scripts 404 — the v1.6.3 bug —
 * every main-process signal reports success: `did-finish-load` fires, the
 * preload runs and injects config, and the window is shown. The app is blank
 * and nothing anywhere says so.
 *
 * So we assert the thing that actually matters, which is that React mounted:
 * check whether #root has children a few seconds after load. That is the
 * difference between "the user reports a white screen and we have no idea" and
 * a log line naming the exact asset that failed.
 */

const DEFAULT_GRACE_PERIOD_MS = 8000;

/**
 * Decide whether a renderer that finished loading actually started.
 *
 * Pure and injectable so the decision is testable without a real BrowserWindow.
 *
 * @param {{ rootChildCount: number, hasBundleError: boolean }} probe
 * @returns {{ healthy: boolean, reason?: string }}
 */
function assessRendererHealth(probe) {
  if (probe.hasBundleError) {
    return { healthy: false, reason: "renderer reported a script/module load error" };
  }
  if (probe.rootChildCount > 0) {
    return { healthy: true };
  }
  return {
    healthy: false,
    reason: "#root is empty — the app bundle never executed (assets likely failed to resolve)",
  };
}

/**
 * The script evaluated in the renderer to sample its state.
 *
 * Kept as a string constant so a test can assert we probe #root rather than
 * something incidental, and deliberately defensive: it runs in a renderer that
 * may be in an arbitrarily broken state, so it must never throw.
 */
const PROBE_SCRIPT = `(() => {
  try {
    const root = document.getElementById('root');
    return {
      rootChildCount: root ? root.childElementCount : 0,
      hasBundleError: Boolean(window.__RELIANT_BUNDLE_ERROR__),
    };
  } catch (e) {
    return { rootChildCount: 0, hasBundleError: true };
  }
})()`;

/**
 * Watch a window for the blank-renderer failure and report it.
 *
 * Diagnosis only — it does not reload or otherwise try to recover. A blank
 * renderer here means the packaged bundle cannot resolve its own assets, which
 * a reload would reproduce exactly; retrying would just hide the evidence
 * behind a loop.
 *
 * @param {import('electron').BrowserWindow} window
 * @param {{ info: Function, error: Function, warn: Function }} log
 * @param {{ gracePeriodMs?: number, onUnhealthy?: (reason: string) => void }} [options]
 * @returns {() => void} cancel function
 */
function watchRendererHealth(window, log, options = {}) {
  const gracePeriodMs = options.gracePeriodMs ?? DEFAULT_GRACE_PERIOD_MS;
  const failedResources = [];
  let timer = null;
  let cancelled = false;

  // Subresource failures surface here and NOWHERE else — this listener is the
  // only reason we can name the missing asset instead of guessing.
  const onFailedSubresource = (_event, _code, desc, url, isMainFrame) => {
    if (!isMainFrame) {
      failedResources.push(`${url} (${desc})`);
    }
  };
  window.webContents.on("did-fail-load", onFailedSubresource);

  const onFinishLoad = () => {
    if (timer) clearTimeout(timer);
    timer = setTimeout(async () => {
      if (cancelled || window.isDestroyed()) return;

      let probe;
      try {
        probe = await window.webContents.executeJavaScript(PROBE_SCRIPT, true);
      } catch (err) {
        log.error("[RendererHealth] could not probe renderer:", err.message);
        return;
      }

      const result = assessRendererHealth(probe);
      if (result.healthy) {
        log.info("[RendererHealth] renderer mounted successfully");
        return;
      }

      log.error(
        `[RendererHealth] BLANK WINDOW DETECTED after ${gracePeriodMs}ms: ${result.reason}`
      );
      log.error("[RendererHealth] renderer URL:", window.webContents.getURL());
      if (failedResources.length > 0) {
        log.error("[RendererHealth] failed subresources:");
        for (const resource of failedResources) {
          log.error("[RendererHealth]   -", resource);
        }
      } else {
        log.error(
          "[RendererHealth] no subresource failures were reported — check that index.html's asset paths match the scheme serving them"
        );
      }

      if (options.onUnhealthy) {
        options.onUnhealthy(result.reason);
      }
    }, gracePeriodMs);
  };

  window.webContents.on("did-finish-load", onFinishLoad);

  return () => {
    cancelled = true;
    if (timer) clearTimeout(timer);
    window.webContents.off("did-finish-load", onFinishLoad);
    window.webContents.off("did-fail-load", onFailedSubresource);
  };
}

module.exports = {
  DEFAULT_GRACE_PERIOD_MS,
  PROBE_SCRIPT,
  assessRendererHealth,
  watchRendererHealth,
};
