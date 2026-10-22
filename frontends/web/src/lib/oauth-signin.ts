/**
 * Provider sign-in (Google / GitHub / Apple), shared by web and Electron.
 *
 * ONE CODE PATH, TWO SURFACES. Everything here runs identically in a mobile
 * browser and in the packaged desktop app: the same `signInWithOAuth` call, the
 * same PKCE verifier, and the same `exchangeCodeForSession` at
 * `/auth/callback`. The ONLY thing that differs is where the provider is told
 * to redirect, and that is a single `await resolveRedirectTarget()` below.
 *
 * ── Why this replaces the previous Electron branch ────────────────────
 *
 * The desktop app used to call a SERVER RPC (SystemService/StartOAuthSignIn)
 * that ran the CLI login flow — `net.Listen("127.0.0.1:0")`, open a browser,
 * block on the callback — inside the hosted API pod. That cannot work: the pod
 * has no browser and its loopback is not the user's machine. Against prod it
 * failed closed ("RELIANT_AUTH_URL must be set"), which was the better of the
 * two possible outcomes.
 *
 * It also could not be fixed by pointing at the local daemon: sign-in must work
 * when the daemon is remote or absent. Claude/Codex OAuth is genuinely
 * different — those credentials belong to the daemon, so they keep their own
 * flow.
 *
 * ── Why the desktop app uses a loopback redirect ──────────────────────
 *
 * The packaged renderer is `app://bundle` (a real origin — see
 * electron/src/app-protocol.js), so it CAN hold the PKCE verifier and do the
 * exchange itself. What it cannot do is render the provider's consent page:
 * Google refuses OAuth in embedded webviews (`disallowed_useragent`), so
 * consent has to happen in the system browser.
 *
 * That leaves one question — how does the code get back to the renderer that
 * holds the verifier — and the answer is RFC 8252's loopback redirect: the
 * desktop app listens on `127.0.0.1:<port>` and hands the provider
 * `http://127.0.0.1:<port>/auth/callback`. The browser's redirect is a
 * TOP-LEVEL NAVIGATION to loopback, which browsers permit (it is how every CLI
 * OAuth flow works, including this repo's own Claude/Codex helper). It is NOT
 * a cross-origin fetch, so it does not trip Chrome's Local Network Access
 * prompt the way `useOAuthAvailability`'s /health probe does.
 *
 * The listener then serves a redirect back into the app's own origin, so the
 * code lands on the SAME `/auth/callback` route the browser build uses, in the
 * SAME renderer that started the flow. Nothing is exchanged outside it.
 */
import { supabase } from "./supabase";
import { getAppURL } from "./constants";
import { logger } from "./logger";

export type OAuthProvider = "google" | "github" | "apple";

/** Round-trip state preserved across the redirect, as query params. */
export type OAuthRedirectState = {
  source?: "signin" | "link";
  returnTo?: string;
};

/**
 * Where the desktop app exposes its loopback callback listener, if it has one.
 *
 * Optional by design: a build whose main process does not implement it (and
 * every build shipped before this change) simply has no `startOAuthRedirect`,
 * and falls back to the hosted URL below rather than throwing.
 */
type DesktopOAuthBridge = {
  startOAuthRedirect?: () => Promise<{ redirectUri: string }>;
};

const desktopBridge = (): DesktopOAuthBridge | undefined =>
  typeof window === "undefined"
    ? undefined
    : (window as unknown as { electronAPI?: DesktopOAuthBridge }).electronAPI;

/**
 * The redirect target handed to the provider.
 *
 * Desktop: the loopback URI its main process is listening on. Web: the app's
 * own `/auth/callback`, which is also the correct answer for a desktop build
 * that predates the bridge — `getAppURL()` deliberately refuses a `file://` or
 * loopback document origin and falls back to the hosted app, because a
 * packaged origin is unreachable from the system browser.
 */
const resolveRedirectTarget = async (): Promise<string> => {
  const bridge = desktopBridge();
  if (bridge?.startOAuthRedirect) {
    try {
      const { redirectUri } = await bridge.startOAuthRedirect();
      if (redirectUri) return redirectUri;
      logger.warn("[OAuth] Desktop bridge returned no redirectUri; using hosted callback");
    } catch (error) {
      // A failed listener must not block sign-in: the hosted callback still
      // works, it just cannot hand the session back to this renderer.
      logger.warn("[OAuth] Desktop loopback listener unavailable; using hosted callback", error);
    }
  }
  return `${getAppURL()}/auth/callback`;
};

/** Append round-trip state to the redirect URL as query params. */
const withState = (baseUrl: string, state: OAuthRedirectState): string => {
  try {
    const url = new URL(baseUrl);
    if (state.source) url.searchParams.set("source", state.source);
    if (state.returnTo) url.searchParams.set("returnTo", state.returnTo);
    return url.toString();
  } catch {
    return baseUrl;
  }
};

/**
 * Whether this surface navigates itself to the provider, or hands the URL to
 * the system browser.
 *
 * A packaged desktop window must NOT navigate to the provider: Google rejects
 * embedded webviews, and electron/src/navigation-policy.js would externalize
 * the navigation anyway, stranding the renderer on the old route.
 */
const opensExternally = (): boolean => Boolean(desktopBridge()?.startOAuthRedirect);

/**
 * Begin provider sign-in. Identical on every surface up to the two lines that
 * differ: where the provider redirects, and who opens the consent page.
 *
 * Returns once consent has been HANDED OFF, not once sign-in completes — the
 * session arrives at `/auth/callback`, which is the shared exchange point.
 */
export const startOAuthSignIn = async (
  provider: OAuthProvider,
  state: OAuthRedirectState = {},
): Promise<void> => {
  const redirectTo = withState(await resolveRedirectTarget(), state);

  const { data, error } = await supabase.auth.signInWithOAuth({
    provider,
    options: {
      redirectTo,
      // Always true: we hand `data.url` to the right place ourselves rather
      // than letting supabase-js navigate the current document, which would be
      // wrong in the desktop app and identical to what we do in the browser.
      skipBrowserRedirect: true,
    },
  });

  if (error) {
    logger.error(`[OAuth] ${provider} sign-in failed to start:`, error);
    throw error;
  }
  if (!data?.url) {
    throw new Error(`No OAuth URL returned from Supabase for ${provider}`);
  }

  if (opensExternally()) {
    // openExternal via the app's own link helper; the consent page must render
    // in the user's real browser.
    window.open(data.url, "_blank", "noopener,noreferrer");
    return;
  }

  window.location.href = data.url;
};

/** Which transport this surface uses, for the auth funnel analytics. */
export const oauthCallbackTransport = (): "loopback" | "web" =>
  opensExternally() ? "loopback" : "web";
