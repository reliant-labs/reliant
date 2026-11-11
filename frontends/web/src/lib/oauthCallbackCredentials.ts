// No SDK/router/logger imports: this runs before they can observe the initial URL.
type CallbackCredentials = Partial<Record<"code" | "token_hash" | "error" | "error_description", string>>;
const CREDENTIAL_KEYS = new Set([
  "code", "token_hash", "token", "access_token", "refresh_token", "id_token",
  "provider_token", "provider_refresh_token", "state", "error", "error_code",
  "error_description", "error_uri", "code_verifier",
]);
const DECISION_KEYS = new Set([...CREDENTIAL_KEYS, "type"]);
const TOKEN_KEYS = ["access_token", "refresh_token", "id_token", "provider_token", "provider_refresh_token"];
let pending: CallbackCredentials | undefined;

/** Repeats are ambiguous even if identical, empty, or split across query/fragment. */
function ambiguousCallback(query: URLSearchParams, fragment: URLSearchParams): boolean {
  const counts = new Map<string, number>();
  for (const params of [query, fragment]) {
    for (const key of params.keys()) {
      const normalized = key.toLowerCase();
      if (!DECISION_KEYS.has(normalized)) continue;
      const count = (counts.get(normalized) ?? 0) + 1;
      if (count > 1) return true;
      counts.set(normalized, count);
    }
  }
  const has = (key: string) => counts.has(key);
  const credential = has("code") || has("token_hash");
  const error = has("error") || has("error_code") || has("error_description") || has("error_uri");
  if (credential && error) return true;
  if (credential && TOKEN_KEYS.some(has)) return true;
  // Existing email links can carry code + token_hash in ONE source: the
  // documented callback policy prefers code. Do not mix sources to make one.
  return (query.has("code") && fragment.has("token_hash")) ||
    (fragment.has("code") && query.has("token_hash"));
}

/** Only the Supabase callback is ours. GitHub repo linking/other callback protocols are untouched. */
export function captureOAuthCallbackCredentials(): void {
  if (typeof window === "undefined" || window.location.pathname !== "/auth/callback") return;
  const url = new URL(window.location.href);
  const fragment = new URLSearchParams(url.hash.slice(1));
  // Decide ambiguity before selecting any value. Rejection still scrubs every
  // credential below; no raw value is exposed as the error or exchanged.
  const ambiguous = ambiguousCallback(url.searchParams, fragment);
  const captured: CallbackCredentials = ambiguous ? { error: "invalid_callback" } : {};
  if (!ambiguous) {
    for (const key of ["code", "token_hash", "error", "error_description"] as const) {
      const value = url.searchParams.get(key) ?? fragment.get(key);
      if (value !== null) captured[key] = value;
    }
  }
  let changed = false;
  let fragmentChanged = false;
  for (const params of [url.searchParams, fragment]) {
    for (const key of [...params.keys()]) {
      if (CREDENTIAL_KEYS.has(key.toLowerCase()) || (ambiguous && key.toLowerCase() === "type")) {
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
