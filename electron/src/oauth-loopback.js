/**
 * Loopback OAuth redirect receiver for the desktop app (RFC 8252).
 *
 * ── What this is for ──────────────────────────────────────────────────
 *
 * The renderer runs the SAME sign-in code as the web app: it calls
 * `signInWithOAuth`, keeps the PKCE verifier, and exchanges the code at
 * `/auth/callback`. The one thing it cannot do is render the provider's
 * consent page — Google refuses OAuth inside embedded webviews
 * (`disallowed_useragent`) — so consent happens in the user's real browser.
 *
 * This module answers the only remaining question: how the authorization code
 * gets from that browser back to the renderer holding the verifier. It listens
 * on `127.0.0.1:<port>`, hands the provider
 * `http://127.0.0.1:<port>/auth/callback` as the redirect URI, and when the
 * browser lands there it 302s STRAIGHT BACK INTO THE APP'S OWN ORIGIN,
 * preserving the query string. The code therefore re-enters the same
 * `/auth/callback` route the browser build uses, in the same renderer that
 * started the flow, and the shared `exchangeCodeForSession` runs unchanged.
 *
 * Nothing is exchanged here. This process never sees a session — only a
 * single-use authorization code in transit, which is useless without the
 * verifier the renderer holds.
 *
 * ── Why loopback rather than a custom scheme or an IPC hop ────────────
 *
 * A `reliant://` deep link would work on macOS but needs per-platform
 * registration (and the Linux .desktop MimeType is not currently declared),
 * plus providers are inconsistent about non-http redirect URIs. Loopback is
 * the pattern every CLI OAuth flow already uses — including this repo's own
 * Claude/Codex helper (internal/auth/oauthcallback) — and it behaves
 * identically on macOS, Windows and Linux.
 *
 * It also keeps the shared code path intact: an IPC-only handoff would mean
 * the desktop app receives the code by a different mechanism than the browser,
 * and the two would drift. Here both surfaces converge on one HTTP redirect
 * into one route.
 *
 * NOT a cross-origin fetch. The redirect is a TOP-LEVEL NAVIGATION to
 * loopback, which browsers permit; it does not trigger Chrome's Local Network
 * Access prompt (that fires for background requests from a public origin, like
 * the /health probe in web/src/hooks/useOAuthAvailability.ts).
 */
const http = require("http");
const log = require("electron-log");

const CALLBACK_PATH = "/auth/callback";

// How long a listener waits for the browser before giving up. Generous: the
// user may have to pick an account, enter a password and clear 2FA. A leaked
// listener is worse than a slow one, so it always closes.
const LISTEN_TIMEOUT_MS = 5 * 60 * 1000;

/** The single in-flight listener, if any. */
let active = null;

function closeActive(reason) {
  if (!active) return;
  const { server, timer } = active;
  active = null;
  clearTimeout(timer);
  try {
    server.close();
  } catch (error) {
    log.warn("[OAuthLoopback] Error closing listener:", error);
  }
  log.info(`[OAuthLoopback] Listener closed (${reason})`);
}

/**
 * Start (or reuse) a loopback listener and return the redirect URI to hand the
 * provider.
 *
 * Reuses an in-flight listener rather than starting a second one: a user who
 * clicks "Sign in with Google", changes their mind, then clicks "Sign in with
 * GitHub" would otherwise leave the first port listening until it times out,
 * and whichever redirect arrived first would land on the wrong one.
 *
 * @param {string} appOrigin Where to send the browser after the code arrives —
 *   the app's own origin (`app://bundle` when packaged, the dev server URL
 *   otherwise), so the code re-enters the renderer's shared callback route.
 * @param {(url: string) => void} deliver Invoked with that app-origin URL when
 *   the code arrives. Passed in rather than registered afterwards so a redirect
 *   that lands before registration cannot be dropped.
 * @returns {Promise<{redirectUri: string}>}
 */
function startOAuthRedirect(appOrigin, deliver) {
  if (active) {
    log.info("[OAuthLoopback] Reusing in-flight listener:", active.redirectUri);
    active.deliver = deliver;
    return Promise.resolve({ redirectUri: active.redirectUri });
  }

  return new Promise((resolve, reject) => {
    const server = http.createServer((req, res) => {
      let url;
      try {
        url = new URL(req.url, "http://127.0.0.1");
      } catch {
        res.writeHead(400).end("Bad request");
        return;
      }

      if (url.pathname !== CALLBACK_PATH) {
        res.writeHead(404).end("Not found");
        return;
      }

      // Hand the query string (code + state, or error + error_description)
      // straight back to the renderer. This process deliberately does not
      // parse or exchange it — the renderer owns that, exactly as in the
      // browser build.
      const target = `${appOrigin}${CALLBACK_PATH}${url.search}`;
      log.info("[OAuthLoopback] Callback received; redirecting into the app");

      // A plain 302 to app://bundle is not something a browser will follow —
      // it is not a scheme the browser knows. Serve a tiny page that closes
      // itself, and signal the renderer through the resolved promise instead.
      res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
      res.end(
        `<!doctype html><meta charset="utf-8"><title>Signed in</title>` +
          `<style>body{font:14px -apple-system,system-ui,sans-serif;` +
          `display:grid;place-items:center;height:100vh;margin:0;color:#333}</style>` +
          `<p>Signed in. You can close this tab and return to Reliant.</p>` +
          `<script>window.close()</script>`,
      );

      const handler = active && active.deliver;
      closeActive("callback received");
      if (handler) handler(target);
    });

    server.on("error", (error) => {
      closeActive("listen error");
      log.error("[OAuthLoopback] Listener failed:", error);
      reject(error);
    });

    // Port 0 => OS-assigned. No fixed port to collide with another instance of
    // the app, another worktree's dev stack, or an unrelated service.
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      const redirectUri = `http://127.0.0.1:${port}${CALLBACK_PATH}`;

      const timer = setTimeout(() => closeActive("timed out"), LISTEN_TIMEOUT_MS);
      // Never hold the app open just because a listener is idle.
      if (typeof timer.unref === "function") timer.unref();

      active = { server, timer, redirectUri, deliver };
      log.info("[OAuthLoopback] Listening for the OAuth redirect on", redirectUri);
      resolve({ redirectUri });
    });
  });
}

module.exports = {
  CALLBACK_PATH,
  startOAuthRedirect,
  closeActive,
  __getActive: () => active,
};
