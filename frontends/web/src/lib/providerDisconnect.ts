/**
 * The confirmation Settings → Providers shows before it removes a provider's
 * credential — shared by desktop (CombinedGeneralSettings) and mobile
 * (MobileAIProvidersPanel) so the two cannot drift.
 *
 * Why the copy is this explicit: on 2026-10-09 a user whose only provider was
 * Codex disconnected it, then signed in to Claude seconds later, and believed
 * connecting Claude had removed Codex. The server never couples the two (see
 * settings_provider_isolation_test.go); what the old prompt — "Are you sure you
 * want to remove the API key for Codex (ChatGPT)?" — never said was which
 * provider goes away, that a sign-in provider has no "API key" to remove, what
 * stops working, or that adding a provider never requires disconnecting one.
 */
export interface ProviderDisconnectTarget {
  /** Provider id as Settings stores it ("codex", "anthropic", "reliant", …). */
  provider: string;
  /** The name the user sees on the provider's card. */
  displayName: string;
  /** True for sign-in providers (Claude, Codex, Antigravity, Copilot). */
  usesSignIn: boolean;
}

const OTHERS_STAY_CONNECTED =
  "Your other providers stay connected — you never need to disconnect one provider to add another.";

export function providerDisconnectConfirmation({
  provider,
  displayName,
  usesSignIn,
}: ProviderDisconnectTarget): string {
  if (provider === "reliant") {
    return [
      "Disconnect Reliant?",
      "Chats using Reliant's models stop working until you re-enable it from Settings.",
      OTHERS_STAY_CONNECTED,
    ].join("\n\n");
  }
  if (usesSignIn) {
    return [
      `Disconnect ${displayName}?`,
      `This signs Reliant out of ${displayName}. Chats using ${displayName} models stop working until you sign in again.`,
      OTHERS_STAY_CONNECTED,
    ].join("\n\n");
  }
  return [
    `Delete your ${displayName} API key?`,
    `Chats using ${displayName} models stop working until you add a key again.`,
    OTHERS_STAY_CONNECTED,
  ].join("\n\n");
}
