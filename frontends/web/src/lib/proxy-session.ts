import { getControlPlaneURL } from "./constants";
import { logger } from "./logger";
import { supabase } from "./supabase";
import { controlPlaneForgeBase, credentialedFetch, isForgeAuth } from "./auth/forge-api";

/**
 * Workspace-proxy sign-in, done up front.
 *
 * A preview URL is behind the workspace-proxy, which admits a browser that
 * carries its `__proxy_session` cookie. Without one it redirects to the app's
 * /auth/proxy (ProxyAuth), which mints a token at the platform-api
 * (POST /api/proxy-session) and sends the browser to the preview's
 * `/__proxy/callback?token=…`; the callback sets the cookie and redirects to
 * the URL the token names.
 *
 * The embedded browser is a separate session with no Reliant sign-in, so that
 * bounce lands on a sign-in page. The app itself IS signed in, so it mints the
 * token here and opens the callback directly: the user sees the preview and
 * nothing else.
 *
 * forge sign-in: the mint authenticates with the login broker's session
 * cookie instead of a bearer — same-origin under /_api/@cp, no Authorization
 * header (the control plane reads the cookie only on a header-less request).
 */

/**
 * Mints a proxy session for returnURL and returns its `/__proxy/callback` URL. Throws on failure.
 *
 * accessToken is the Supabase session's bearer. forge sign-in ignores it (pass
 * null): the session cookie is the credential.
 */
export async function mintProxySessionCallbackUrl(returnURL: string, accessToken: string | null): Promise<string> {
  const controlPlaneURL = getControlPlaneURL();
  if (!controlPlaneURL) throw new Error("Control plane API URL not configured.");

  // This must hit platform-api (the control-plane API), NOT admin-web. In
  // cloud-dev they are separate origins.
  const body = JSON.stringify({ return_url: returnURL });
  let response: Response;
  if (isForgeAuth()) {
    response = await credentialedFetch(`${controlPlaneForgeBase()}/api/proxy-session`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body,
    });
  } else {
    if (!accessToken) throw new Error("Not signed in.");
    response = await fetch(`${controlPlaneURL}/api/proxy-session`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${accessToken}`,
      },
      body,
    });
  }
  if (!response.ok) {
    const text = await response.text();
    logger.error("[proxy-session] Token mint failed", { status: response.status, body: text });
    throw new Error(`Failed to authenticate: ${text}`);
  }
  const { token } = (await response.json()) as { token: string };
  return `${new URL(returnURL).origin}/__proxy/callback?token=${encodeURIComponent(token)}`;
}

/**
 * The URL that opens previewUrl already signed in to the workspace-proxy, or
 * previewUrl itself when that cannot be arranged (no session, mint refused):
 * the proxy then falls back to the /auth/proxy bounce, which is no worse than
 * before.
 */
export async function signedInPreviewUrl(previewUrl: string): Promise<string> {
  try {
    // forge sign-in: the cookie is the session, so the mint itself is the
    // check; signed out, it is refused and the preview opens as before.
    if (isForgeAuth()) return await mintProxySessionCallbackUrl(previewUrl, null);
    const {
      data: { session },
    } = await supabase.auth.getSession();
    if (!session?.access_token) return previewUrl;
    return await mintProxySessionCallbackUrl(previewUrl, session.access_token);
  } catch (error) {
    logger.warn("[proxy-session] Opening the preview without a pre-minted session:", error);
    return previewUrl;
  }
}
