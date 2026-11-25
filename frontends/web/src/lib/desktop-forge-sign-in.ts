/**
 * Desktop sign-in through forge's sign-in handoff.
 *
 * With `AUTH_PROVIDER: "forge"` in config.js the app signs in against
 * reliant-api's own identity, not Supabase. Inside Electron that sign-in runs
 * in the system browser: the main process opens `/auth/handoff?req=…` there
 * (HandoffConsent.tsx), the person consents with the session that browser
 * already holds, and the app's loopback listener receives the code. The
 * preload exposes the main-process half as `window.electronAPI.forgeAuth`,
 * which desktop builds older than the handoff lack.
 */

/**
 * Where reliant-api's auth routes are reached in forge mode: the page's own
 * origin, through the named same-origin API `@reliant` (the control plane is
 * `/_api/@cp`). The hosted static site and the vite dev proxy both forward it
 * to reliant-api, so the session cookie is first-party to the page.
 */
export const FORGE_AUTH_API_BASE = "/_api/@reliant";

export type AuthProvider = "supabase" | "forge";

/**
 * The runtime auth provider from config.js (`AUTH_PROVIDER`). Anything but
 * `"forge"` — absent, a typo, a non-string — is Supabase, today's default.
 */
export function authProvider(): AuthProvider {
  const raw = typeof window !== "undefined" ? window.__FORGE_CONFIG__?.AUTH_PROVIDER : undefined;
  return raw === "forge" ? "forge" : "supabase";
}

type ForgeAuthBridge = NonNullable<Window["electronAPI"]["forgeAuth"]>;

function forgeAuthBridge(): ForgeAuthBridge | undefined {
  if (typeof window === "undefined") return undefined;
  return window.electronAPI?.forgeAuth;
}

/** True in forge mode inside a desktop build whose preload has the handoff bridge. */
export function isDesktopForgeSignIn(): boolean {
  return authProvider() === "forge" && forgeAuthBridge() !== undefined;
}

/**
 * Sign the desktop app in through the system browser.
 *
 * The forge-mode sign-in screen calls this inside Electron INSTEAD of
 * in-window social or password sign-in: the desktop app signs in through the
 * system browser, because Google refuses sign-in in an embedded webview. It
 * resolves once the main process holds the session; on failure it throws an
 * Error carrying the main process's message.
 */
export async function signInWithDesktopBrowser(): Promise<{ subject?: string; email?: string }> {
  const bridge = forgeAuthBridge();
  if (!bridge) {
    throw new Error("This version of the desktop app cannot sign in through the browser. Update it and try again.");
  }
  const result = await bridge.signIn();
  if (!result.success) {
    throw new Error(result.error || "Sign-in did not complete.");
  }
  return { subject: result.subject, email: result.email };
}

/** Sign the desktop app's main process out. A no-op without the bridge. */
export async function signOutDesktop(): Promise<void> {
  const bridge = forgeAuthBridge();
  if (!bridge) return;
  await bridge.signOut();
}

/** Tell the main process the session changed. A no-op without the bridge. */
export async function notifyDesktopSessionChanged(): Promise<void> {
  const bridge = forgeAuthBridge();
  if (!bridge) return;
  await bridge.sessionChanged();
}
