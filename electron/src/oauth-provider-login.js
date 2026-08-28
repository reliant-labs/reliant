/**
 * Loopback receiver for PROVIDER sign-in (Claude Code, Codex) in the desktop
 * app.
 *
 * ── The bug this replaces ─────────────────────────────────────────────
 *
 * The desktop app used to delegate this to the daemon over a single
 * request/response RPC: `DaemonService/StartOAuthFlow` →
 * `auth.start_oauth` → `oauthcallback.Run`, which starts a listener AND THEN
 * BLOCKS UNTIL THE HUMAN FINISHES SIGNING IN. One RPC therefore had to stay
 * open across a browser round trip, a consent screen and possibly 2FA.
 *
 * Nothing on that path survives it. The observed failure was ~15s: the
 * renderer surfaced "Failed to fetch (api.reliantapi.com)", and — worse — the
 * daemon's listener was torn down with the cancelled request context, so when
 * the browser finally redirected there was nothing bound and the user got
 * ERR_CONNECTION_REFUSED. Both reported symptoms were this one cause.
 *
 * Raising the deadline is not the fix. A request/response RPC that must
 * outlive a human's attention span is the wrong shape at any duration, and the
 * bound is not ours to raise: no timeout in this repo produced the 15s (the
 * client table and the server table both register this method at "no timeout",
 * and NATS is given an hour), so it comes from the network path itself.
 *
 * ── Why the receiver belongs HERE, not on the daemon ──────────────────
 *
 * The daemon may not run on the user's machine. In distributed mode it is a
 * pod in a cluster, and a loopback listener there is not reachable from the
 * user's browser at all — the flow could not work even with an unlimited
 * deadline. The desktop app, by definition, IS on the user's machine.
 *
 * So the listener is local and the RPC disappears. Starting it is IPC that
 * returns in microseconds; waiting for the code is a separate IPC with no
 * proxy, no gateway and no deadline between the two ends.
 *
 * ── What this does NOT do ─────────────────────────────────────────────
 *
 * It never exchanges anything. The renderer holds the PKCE verifier and
 * exchanges the code through the authenticated backend, exactly as the browser
 * build does. This process only sees a single-use authorization code in
 * transit, which is useless without that verifier.
 *
 * It also does not navigate the window, which is what separates it from
 * oauth-loopback.js. That module delivers Supabase sign-in by loading
 * `/auth/callback`, because there the renderer's route owns the exchange.
 * Doing that here would DESTROY the renderer holding the verifier mid-flow, so
 * the code is delivered back through the pending IPC instead.
 */
const http = require('http');
const log = require('electron-log');

const { LISTEN_HOST, inferCallbackContract } = require('./oauth-contract');

/**
 * How long a flow may stay open before it is abandoned. Generous — the user
 * may have to log in, pick an org and clear 2FA — but never unbounded, because
 * a leaked listener holds a port (1455 for Codex is FIXED, so a leak blocks
 * every later attempt).
 */
const FLOW_TIMEOUT_MS = 10 * 60 * 1000;

/**
 * How long a SETTLED flow's outcome is retained after its port is released.
 *
 * The outcome must outlive the listener, because start and wait are separate
 * calls and the callback can land between them — a provider that redirects
 * instantly, or a user already signed in to the provider, produces exactly
 * that ordering. Dropping the flow on settle would make `wait` report "unknown
 * flow" for the FASTEST sign-ins, which is a nastier version of the bug being
 * fixed. Retaining the result decouples "the port is closed" from "nobody has
 * collected the code yet".
 */
const RESULT_RETENTION_MS = 5 * 60 * 1000;

/** Flows, keyed by the id handed to the renderer. Settled ones linger briefly. */
const flows = new Map();
let nextFlowId = 1;

/**
 * Resolve or reject a flow exactly once, then release its port.
 *
 * Single-settle matters: a provider that redirects twice, or a timeout that
 * races a real callback, would otherwise resolve a promise twice and leave the
 * server bound.
 *
 * The port closes here; the RESULT stays reachable for RESULT_RETENTION_MS so
 * a `wait` that arrives after the callback still collects it.
 */
function settle(flow, error, result) {
  if (flow.settled) return;
  flow.settled = true;

  clearTimeout(flow.timer);

  try {
    flow.server.close();
    // close() only stops NEW connections; it leaves established keep-alive
    // sockets open, and the port stays bound until they drain. Browsers keep
    // them alive for many seconds, so the port would outlive the flow — fatal
    // for Codex, whose port 1455 is fixed by OpenAI and blocks every later
    // attempt while it is held.
    if (typeof flow.server.closeAllConnections === 'function') {
      flow.server.closeAllConnections();
    }
  } catch (closeError) {
    log.warn('[OAuthProviderLogin] Error closing listener:', closeError);
  }

  if (error) {
    flow.reject(error);
  } else {
    flow.resolve(result);
  }

  const reaper = setTimeout(() => flows.delete(flow.id), RESULT_RETENTION_MS);
  if (typeof reaper.unref === 'function') reaper.unref();
  flow.reaper = reaper;
}

/**
 * Start a loopback listener for a provider sign-in and return immediately.
 *
 * Returning before the user has done anything is the entire point: the caller
 * gets the redirect URI it needs to open the consent page, and the wait for
 * the code happens on a separate call that no network deadline bounds.
 *
 * @param {string} authorizeUrlTemplate Authorize URL carrying the literal
 *   `{redirect_uri}` placeholder.
 * @returns {Promise<{flowId: string, redirectUri: string, authorizeUrl: string}>}
 */
function startProviderLogin(authorizeUrlTemplate) {
  const contract = inferCallbackContract(authorizeUrlTemplate);

  return new Promise((resolve, reject) => {
    const server = http.createServer((req, res) => {
      let url;
      try {
        url = new URL(req.url, `http://${LISTEN_HOST}`);
      } catch {
        res.writeHead(400).end('Bad request');
        return;
      }

      if (url.pathname !== contract.callbackPath) {
        res.writeHead(404).end('Not found');
        return;
      }

      const code = (url.searchParams.get('code') || '').trim();
      const oauthError = (url.searchParams.get('error') || '').trim();
      const description = (url.searchParams.get('error_description') || '').trim();

      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
      res.end(page(code ? 'Signed in' : 'Sign-in failed', code
        ? 'You can close this tab and return to Reliant.'
        : [oauthError, description].filter(Boolean).join(': ') ||
            'No authorization code was returned. You can close this tab and try again.'));

      log.info('[OAuthProviderLogin] Callback received', {
        hasCode: !!code,
        oauthError: oauthError || undefined,
      });

      const flow = flows.get(server.__flowId);
      if (!flow) return;

      if (!code) {
        settle(
          flow,
          new Error(
            [oauthError, description].filter(Boolean).join(': ') ||
              'Authorization failed without an error message.',
          ),
        );
        return;
      }

      settle(flow, null, {
        code,
        state: url.searchParams.get('state') || '',
        redirectUri: flow.redirectUri,
      });
    });

    server.on('error', (error) => {
      // Only meaningful before listen() succeeds; afterwards the flow owns the
      // rejection path.
      const flow = flows.get(server.__flowId);
      if (flow) {
        settle(flow, error);
        return;
      }
      log.error('[OAuthProviderLogin] Listener failed:', error);
      reject(error);
    });

    server.listen(contract.fixedPort, LISTEN_HOST, () => {
      const { port } = server.address();
      // Bind 127.0.0.1, ADVERTISE localhost: providers allow-list the latter
      // spelling, and a listener on the former is what actually receives it.
      const redirectUri = `http://${contract.redirectHost}:${port}${contract.callbackPath}`;
      const authorizeUrl = authorizeUrlTemplate.replace(
        '{redirect_uri}',
        encodeURIComponent(redirectUri),
      );

      const flowId = String(nextFlowId++);
      server.__flowId = flowId;

      const flow = {
        id: flowId,
        server,
        redirectUri,
        settled: false,
        resolve: () => {},
        reject: () => {},
      };
      flow.promise = new Promise((res, rej) => {
        flow.resolve = res;
        flow.reject = rej;
      });
      // Nothing awaits this promise until waitForProviderLogin is called, and
      // an unobserved rejection would crash the main process. Attach an inert
      // handler now; the real consumer still sees the rejection.
      flow.promise.catch(() => {});

      flow.timer = setTimeout(
        () => settle(flow, new Error('Timed out waiting for the sign-in to complete.')),
        FLOW_TIMEOUT_MS,
      );
      if (typeof flow.timer.unref === 'function') flow.timer.unref();

      flows.set(flowId, flow);
      log.info('[OAuthProviderLogin] Listening for the OAuth redirect on', redirectUri);

      resolve({ flowId, redirectUri, authorizeUrl });
    });
  });
}

/**
 * Wait for the authorization code from a started flow.
 *
 * This is the call that spans human time, and it is deliberately plain IPC
 * between the renderer and its own main process — no proxy, gateway or NATS
 * hop, so nothing in between can impose a deadline on it.
 *
 * @param {string} flowId Id returned by startProviderLogin.
 * @returns {Promise<{code: string, state: string, redirectUri: string}>}
 */
function waitForProviderLogin(flowId) {
  const flow = flows.get(flowId);
  if (!flow) {
    return Promise.reject(new Error(`Unknown OAuth flow: ${flowId}`));
  }
  // Resolves immediately if the callback already landed — see
  // RESULT_RETENTION_MS for why that ordering is normal, not exceptional.
  return flow.promise;
}

/**
 * Abandon a flow the user cancelled, releasing its port immediately rather
 * than at the timeout.
 *
 * Cancelling also drops the retained outcome: the caller has gone away, so
 * there is nobody left to collect it.
 */
function cancelProviderLogin(flowId, reason = 'cancelled') {
  const flow = flows.get(flowId);
  if (!flow) return false;
  settle(flow, new Error(`OAuth flow ${reason}`));
  clearTimeout(flow.reaper);
  flows.delete(flowId);
  return true;
}

function escapeHTML(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function page(title, body) {
  const t = escapeHTML(title);
  const b = escapeHTML(body);
  return (
    `<!doctype html><meta charset="utf-8"><title>${t}</title>` +
    `<style>body{font:14px -apple-system,system-ui,sans-serif;` +
    `display:grid;place-items:center;height:100vh;margin:0;color:#333}</style>` +
    `<div style="text-align:center"><h2>${t}</h2><p>${b}</p></div>`
  );
}

module.exports = {
  FLOW_TIMEOUT_MS,
  startProviderLogin,
  waitForProviderLogin,
  cancelProviderLogin,
  // Flows still holding a bound port. Settled flows are excluded: they have
  // released their listener and only retain an outcome for collection.
  __activeFlowCount: () =>
    [...flows.values()].filter((flow) => !flow.settled).length,
};
