/**
 * Statsig analytics client
 *
 * Thin wrapper around @statsig/js-client that respects privacy settings
 * and is a no-op in dev mode.
 */

import { StatsigClient } from "@statsig/js-client";
import { getPrivacySettings } from "../store/privacyStore";
import { isDev } from "./constants";

let client: StatsigClient | null = null;
let initPromise: Promise<void> | null = null;

function isEnabled(): boolean {
  if (isDev) return false;
  return getPrivacySettings().analyticsEnabled;
}

function getClient(): StatsigClient | null {
  if (!isEnabled()) return null;

  if (!client) {
    const key = import.meta.env.VITE_STATSIG_CLIENT_KEY;
    if (!key) return null;

    client = new StatsigClient(key, {});
    initPromise = client
      .initializeAsync()
      .then(() => undefined)
      .catch(() => {
        // Silently ignore init failures — analytics is best-effort
      });
  }

  return client;
}

/**
 * Track an analytics event. No-op when analytics is disabled or in dev mode.
 */
export function trackEvent(
  eventName: string,
  metadata?: Record<string, string | number | boolean>,
): void {
  const c = getClient();
  if (!c) return;

  const stringMeta: Record<string, string> | undefined = metadata
    ? Object.fromEntries(
        Object.entries(metadata).map(([k, v]) => [k, String(v)]),
      )
    : undefined;

  c.logEvent(eventName, undefined, stringMeta);
}

/**
 * Identify a user after authentication.
 */
export async function identifyUser(
  userId: string,
  email?: string,
): Promise<void> {
  if (!isEnabled()) return;

  const c = getClient();
  if (!c) return;

  // Wait for init to finish before updating user
  if (initPromise) await initPromise;

  await c.updateUserAsync({ userID: userId, email: email ?? undefined });
}

/**
 * Reset user identity on logout.
 */
export async function resetUser(): Promise<void> {
  if (!client) return;
  if (initPromise) await initPromise;
  await client.updateUserAsync({});
}
