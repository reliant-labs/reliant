/**
 * Analytics event pipe.
 *
 * Events fan out to two sinks, and BOTH are optional:
 *
 *  - Statsig, when VITE_STATSIG_CLIENT_KEY is set. This is the growth-analytics
 *    sink and it is currently unset in every environment, which is why the whole
 *    onboarding funnel has been discarded in prod: the instrumentation in
 *    OnboardingFlow/analytics.ts is complete and its output went nowhere.
 *
 *  - Sentry, whenever Sentry is live. This is the detection sink, and it is the
 *    one that works today: the prod DSN is confirmed present in the shipped
 *    bundle bytes, so it is the only pipe already proven to reach production.
 *
 * Sending to Sentry as well as Statsig is deliberate rather than redundant.
 * Sentry is a worse analytics product and a far better debugging one, and the
 * purpose here is detection-and-diagnosis: because the funnel events and the
 * error events share a session, a funnel cliff arrives WITH a session replay of
 * the user hitting it, and Sentry's native `release` field attributes it to a
 * deploy. Statsig can tell you conversion fell; it cannot show you the user
 * falling.
 *
 * Events land as breadcrumbs (cheap, and they ride along on any error the
 * session later reports) plus, for the small set of funnel-defining events, an
 * explicit message that alert rules can match on. See
 * docs/observability/alert-rules.md for the rules that consume these.
 */

import { StatsigClient } from "@statsig/js-client";
import * as Sentry from "@sentry/react";
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
 * Funnel-defining events, which are emitted to Sentry as explicit messages in
 * addition to breadcrumbs so an alert rule has something to match.
 *
 * Kept to a deliberately short list. A breadcrumb is free and invisible until
 * something goes wrong; a message is an event with a cost and a chance of
 * becoming noise, and noise is what gets an alert muted. These are the events
 * the absolute-zero alerts in docs/observability/alert-rules.md are written
 * against — nothing else needs to be a message.
 */
const FUNNEL_MARKER_EVENTS = new Set([
  "onboarding_flow_started",
  "onboarding_completed",
  "onboarding_exited",
  "app_boot_started",
  "app_boot_succeeded",
]);

/**
 * Track an analytics event. No-op when analytics is disabled or in dev mode.
 *
 * Delivery to the two sinks is independent: a missing Statsig key must not stop
 * the event reaching Sentry, which is the whole reason the funnel is dark today.
 */
export function trackEvent(
  eventName: string,
  metadata?: Record<string, string | number | boolean>,
): void {
  if (!isEnabled()) return;

  const stringMeta: Record<string, string> | undefined = metadata
    ? Object.fromEntries(
        Object.entries(metadata).map(([k, v]) => [k, String(v)]),
      )
    : undefined;

  sendToSentry(eventName, stringMeta);

  const c = getClient();
  if (!c) return;

  c.logEvent(eventName, undefined, stringMeta);
}

/**
 * Mirror an event into Sentry. Best-effort by construction: analytics must
 * never be able to break the app, and Sentry's own calls are no-ops when it was
 * never initialised, so this needs no "is Sentry live" check of its own.
 */
function sendToSentry(
  eventName: string,
  metadata?: Record<string, string>,
): void {
  try {
    Sentry.addBreadcrumb({
      category: "funnel",
      type: "user",
      level: "info",
      message: eventName,
      data: metadata,
    });

    if (FUNNEL_MARKER_EVENTS.has(eventName)) {
      Sentry.withScope((scope) => {
        scope.setLevel("info");
        scope.setTag("funnel_event", eventName);
        // Tags are what alert rules filter on, so the low-cardinality fields
        // the rules need are promoted out of the metadata blob.
        if (metadata?.reason) scope.setTag("funnel_reason", metadata.reason);
        if (metadata?.step) scope.setTag("funnel_step", metadata.step);
        if (metadata?.last_step) scope.setTag("funnel_step", metadata.last_step);
        if (metadata) scope.setContext("funnel", metadata);
        Sentry.captureMessage(`funnel: ${eventName}`, "info");
      });
    }
  } catch {
    // Analytics is best-effort and must never surface to the user.
  }
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
