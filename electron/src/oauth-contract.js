/**
 * Where each OAuth provider is allowed to send the authorization code.
 *
 * ── Why these paths differ, and why they must NOT be unified ──────────
 *
 * `/callback` and `/auth/callback` look like a copy-paste inconsistency. They
 * are not. A redirect URI is REGISTERED WITH THE PROVIDER, and the provider
 * rejects any authorize request whose redirect_uri does not match what it has
 * on file. Anthropic's Claude Code client registers `/callback`; OpenAI's
 * Codex client registers `/auth/callback` on the fixed port 1455. Editing
 * either one to match the other does not remove a discrepancy — it breaks that
 * provider's sign-in with a redirect_uri mismatch.
 *
 * The Supabase loopback in oauth-loopback.js uses `/auth/callback` for a third
 * reason again: that path is a real ROUTE in the renderer bundle, so the code
 * can be handed to the component that owns the exchange. It is unrelated to
 * these two, which is why it keeps its own constant.
 *
 * ── Mirrored from Go ──────────────────────────────────────────────────
 *
 * MIRRORS oauthcallback.InferConfig (internal/auth/oauthcallback/
 * oauthcallback.go), which is the source of truth: the `reliant auth serve`
 * path used by the browser build derives its redirect URI from that function,
 * and desktop and web must hand the provider the SAME URI or one of the two
 * surfaces breaks. oauth-contract.test.js asserts the two agree by reading the
 * Go source, so a change on either side fails the Electron suite instead of
 * shipping a redirect_uri the provider refuses.
 */

/**
 * The address the listener BINDS. Always literal loopback: `localhost` can
 * resolve to ::1 first, and a listener bound to the IPv4 address would then
 * miss the browser's request.
 */
const LISTEN_HOST = '127.0.0.1';

/**
 * The host used to BUILD the redirect URI handed to the provider. Providers
 * allow-list the spelling `localhost`, not `127.0.0.1`, even though they
 * resolve to the same interface.
 */
const DEFAULT_REDIRECT_HOST = 'localhost';

/** Path used when the authorize URL matches no known provider. */
const DEFAULT_CALLBACK_PATH = '/auth/callback';

/**
 * Per-provider redirect contracts, keyed by a substring of the authorize URL's
 * host. `fixedPort: 0` means an OS-assigned port, which the provider must
 * permit (Anthropic does); a non-zero port is one the provider has registered
 * and will not accept a substitute for.
 */
const PROVIDER_CALLBACK_CONTRACTS = [
  {
    hostMatch: 'claude.ai',
    redirectHost: 'localhost',
    callbackPath: '/callback',
    fixedPort: 0,
  },
  {
    hostMatch: 'auth.openai.com',
    redirectHost: 'localhost',
    callbackPath: '/auth/callback',
    fixedPort: 1455,
  },
];

/**
 * Resolve the redirect contract for an authorize URL.
 *
 * Matches on HOST ONLY, exactly as the Go original does — matching on the full
 * URL would let a query parameter that happens to contain "claude.ai" select
 * the wrong provider's path.
 *
 * @param {string} authorizeUrlTemplate Authorize URL, usually still carrying
 *   the literal `{redirect_uri}` placeholder.
 * @returns {{redirectHost: string, callbackPath: string, fixedPort: number}}
 */
function inferCallbackContract(authorizeUrlTemplate) {
  const fallback = {
    redirectHost: DEFAULT_REDIRECT_HOST,
    callbackPath: DEFAULT_CALLBACK_PATH,
    fixedPort: 0,
  };

  let host;
  try {
    host = new URL(authorizeUrlTemplate).hostname.toLowerCase();
  } catch {
    // An unparseable template still gets a usable listener; the provider will
    // reject the request on its own terms, which is a clearer failure than
    // throwing here.
    return fallback;
  }

  const match = PROVIDER_CALLBACK_CONTRACTS.find((contract) =>
    host.includes(contract.hostMatch),
  );
  if (!match) return fallback;

  return {
    redirectHost: match.redirectHost,
    callbackPath: match.callbackPath,
    fixedPort: match.fixedPort,
  };
}

module.exports = {
  LISTEN_HOST,
  DEFAULT_REDIRECT_HOST,
  DEFAULT_CALLBACK_PATH,
  PROVIDER_CALLBACK_CONTRACTS,
  inferCallbackContract,
};
