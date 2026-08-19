/**
 * Navigation policy for `will-navigate`.
 *
 * Windows externalize outbound links so clicking an https:// link opens the
 * user's browser instead of replacing the app. The check must be by ORIGIN, not
 * by exact URL: the app is a SPA, so an in-app route change from /chat/abc to
 * /auth is a different URL at the same origin. Comparing full strings treats
 * that as outbound, cancels it, and hands it to shell.openExternal — which
 * spawns a browser tab and leaves the renderer stranded on the old route.
 *
 * That is not hypothetical. The 401 interceptor in web/src/api/transport.ts
 * redirects to /auth to recover from a rejected token; under a string
 * comparison every such redirect became a new browser tab, and because the
 * navigation never happened the dead session kept firing 401s — one tab per
 * rejection, with the in-app sign-out escape hatch swallowed the same way.
 *
 * Extracted from main.js so it can be unit-tested without booting Electron.
 */

// Duplicated rather than imported from app-protocol.js: that module pulls in
// `fs` and Electron's `protocol`, and this one is deliberately dependency-free
// so it can be unit-tested in plain node. The scheme name is asserted against
// app-protocol's export in navigation-policy.test.js, so the two cannot drift.
const APP_SCHEME = "app";

/**
 * Decide whether a navigation target should be opened outside the app.
 *
 * Unparseable or non-http(s) targets are treated as external so the window is
 * never navigated somewhere it cannot render. A missing/unparseable current URL
 * means we have no origin to compare against; staying in-app is the safe answer
 * there, since the alternative is externalizing the app's own first navigation.
 *
 * @param {string} targetUrl - The URL the window is trying to navigate to
 * @param {string} currentUrl - The window's current URL
 * @returns {boolean} true if the target should be handed to the system browser
 */
function shouldOpenExternally(targetUrl, currentUrl) {
  let target;
  try {
    target = new URL(targetUrl);
  } catch {
    return true;
  }

  // app:// is the packaged renderer's own scheme, so a navigation to it is
  // in-app by definition. It has to be allowed explicitly: it is not http(s),
  // and the rule below would hand every internal route change to the system
  // browser — one tab per navigation, with the window stranded on the old
  // route.
  if (target.protocol === `${APP_SCHEME}:`) {
    return false;
  }

  // Anything else that isn't web content (mailto:, custom schemes) belongs to the OS.
  if (target.protocol !== "http:" && target.protocol !== "https:") {
    return true;
  }

  let current;
  try {
    current = new URL(currentUrl);
  } catch {
    return false;
  }

  return target.origin !== current.origin;
}

module.exports = {
  shouldOpenExternally,
};
