/**
 * A `returnTo` that brings a flow started on the forge site back there.
 *
 * GitHub's OAuth callback is Reliant's (GITHUB_REDIRECT_URI is
 * app.reliantlabs.io/auth/github/callback), and the control plane keeps only a
 * relative `returnTo` (gitcredential.safeReturnTo). So the onboarding wizard's
 * GitHub step, run on the forge host, came back to a page on the Reliant host:
 * a different session, and the wrong shell.
 *
 * The forge site therefore sends its path under FORGE_SHELL_RETURN_PREFIX, and
 * the callback page maps that prefix to the forge site's origin. The origin is
 * this bundle's own config (forgeAppOrigin), never the URL's, so the prefix
 * cannot name another host.
 */

import { forgeAppOrigin, isForgeShell } from "@/lib/shell";
import { isSafeReturnTo } from "@/lib/returnTo";

/** The path prefix that means "on the forge site". */
export const FORGE_SHELL_RETURN_PREFIX = "/__forge";

/** `path` (a same-origin path of this page) as a `returnTo` that survives a hop through the Reliant host. */
export function crossShellReturnTo(path: string): string {
  return isForgeShell() ? `${FORGE_SHELL_RETURN_PREFIX}${path}` : path;
}

/** The forge-site URL a prefixed `returnTo` means, or null when it is not one. */
export function forgeShellReturnUrl(returnTo: string | undefined | null): URL | null {
  if (!returnTo || !returnTo.startsWith(`${FORGE_SHELL_RETURN_PREFIX}/`)) return null;
  const path = returnTo.slice(FORGE_SHELL_RETURN_PREFIX.length);
  if (!isSafeReturnTo(path)) return null;
  const origin = forgeAppOrigin();
  const url = new URL(path, origin);
  return url.origin === origin ? url : null;
}
