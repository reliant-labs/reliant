// No SDK/router/logger imports: this runs before they can observe the initial URL.
type CallbackCredentials = Partial<Record<"code" | "token_hash" | "error" | "error_description", string>>;
const CREDENTIAL_KEYS = new Set([
  "code", "token_hash", "token", "access_token", "refresh_token", "id_token",
  "provider_token", "provider_refresh_token", "state", "error", "error_code",
  "error_description", "error_uri", "code_verifier",
]);
let pending: CallbackCredentials | undefined;

/** Only the Supabase callback is ours. GitHub repo linking/other callback protocols are untouched. */
export function captureOAuthCallbackCredentials(): void {
  if (typeof window === "undefined" || window.location.pathname !== "/auth/callback") return;
  const url = new URL(window.location.href);
  const fragment = new URLSearchParams(url.hash.slice(1));
  const captured: CallbackCredentials = {};
  for (const key of ["code", "token_hash", "error", "error_description"] as const) {
    const value = url.searchParams.get(key) ?? fragment.get(key);
    if (value !== null) captured[key] = value;
  }
  let changed = false;
  let fragmentChanged = false;
  for (const params of [url.searchParams, fragment]) {
    for (const key of [...params.keys()]) {
      if (CREDENTIAL_KEYS.has(key.toLowerCase())) {
        params.delete(key);
        changed = true;
        if (params === fragment) fragmentChanged = true;
      }
    }
  }
  if (!changed) return;
  // Keep only the exchange inputs in page memory, never storage/history.state.
  // detectSessionInUrl is false in supabase.ts: OAuthCallback owns the exchange.
  if (fragmentChanged) url.hash = fragment.toString();
  try {
    window.history.replaceState(window.history.state, "", url.pathname + url.search + url.hash);
  } catch {
    pending = undefined;
    // Do not initialize telemetry/router with credentials still in the URL.
    // A browser policy may forbid history writes. Recovery is a NEW safe
    // navigation chosen by the user; do not silently replay the callback.
    const root = document.getElementById("root");
    if (root) {
      const message = document.createElement("p");
      message.textContent = "We couldn't complete sign-in safely in this browser. Please restart sign-in.";
      const restart = document.createElement("a");
      restart.href = "/auth";
      restart.textContent = "Restart sign-in";
      root.replaceChildren(message, restart);
    }
    // No original exception/cause: it can contain the unredacted callback URL.
    throw new Error("Authentication callback URL could not be cleared safely.");
  }
  pending = captured;
}

export function takeOAuthCallbackCredentials(): CallbackCredentials {
  captureOAuthCallbackCredentials(); // Also cover a client-side callback navigation.
  const result = pending ?? {};
  pending = undefined;
  return result;
}
