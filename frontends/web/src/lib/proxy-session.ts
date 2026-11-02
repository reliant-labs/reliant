import { getControlPlaneURL } from "./constants";
import { logger } from "./logger";
import { supabase } from "./supabase";

/**
 * Workspace-proxy sign-in, done up front.
 *
 * A preview URL is behind the workspace-proxy, which admits a browser that
 * carries its `__proxy_session` cookie. Without one it redirects to the app's
 * /auth/proxy (ProxyAuth), which mints a token at the admin-server
 * (POST /api/proxy-session) and sends the browser to the preview's
 * `/__proxy/callback?token=…`; the callback sets the cookie and redirects to
 * the URL the token names.
 *
 * The embedded browser is a separate session with no Reliant sign-in, so that
 * bounce lands on a sign-in page. The app itself IS signed in, so it mints the
 * token here and opens the callback directly: the user sees the preview and
 * nothing else.
 */

/** Mints a proxy session for returnURL and returns its `/__proxy/callback` URL. Throws on failure. */
export async function mintProxySessionCallbackUrl(returnURL: string, accessToken: string): Promise<string> {
  const controlPlaneURL = getControlPlaneURL();
  if (!controlPlaneURL) throw new Error("Control plane API URL not configured.");

  // This must hit admin-server (the control-plane API), NOT admin-web. In
  // cloud-dev they are separate origins.
  const response = await fetch(`${controlPlaneURL}/api/proxy-session`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${accessToken}`,
    },
    body: JSON.stringify({ return_url: returnURL }),
  });
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
