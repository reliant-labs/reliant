/**
 * Product event pipe.
 *
 * `trackEvent` sends an event to ClickStack, through forge's web runtime
 * `track()`, when the build ships browser telemetry (the runtime config's
 * OTEL_ENDPOINT is set; telemetryEndpoint.ts). The event becomes a span named
 * `product.<event>` on the same session as the page's errors and replay; the
 * runtime bounds and redacts the props. This is where funnels are queried: the
 * backend's product events are log records with the same `product.` names
 * (dev-docs/product-events.md).
 *
 * The user is attached by telemetry.ts (`setTelemetryUserId`), not here.
 */

import { privacyConsent } from "../hooks/privacy-queries";
import { track } from "@reliantlabs/forge-web-runtime";
import { isDev } from "./constants";
import { clickstackEnabled } from "./telemetryEndpoint";

/** Starts every product event's ClickStack name, on web and backend alike. */
export const PRODUCT_EVENT_PREFIX = "product.";

function isEnabled(): boolean {
  if (isDev) return false;
  return privacyConsent().analyticsEnabled;
}

/**
 * Whether trackEvent would reach ClickStack right now. A caller that must know
 * an event went out (to record that it has) asks this first; trackEvent itself
 * returns nothing.
 */
export function clickstackDeliveryActive(): boolean {
  return isEnabled() && clickstackEnabled();
}

/**
 * Track a product event. No-op when analytics is disabled or in dev mode.
 */
export function trackEvent(
  eventName: string,
  metadata?: Record<string, string | number | boolean>,
): void {
  if (!isEnabled()) return;

  if (clickstackEnabled()) sendToClickstack(eventName, metadata);
}

/**
 * The runtime keeps only keys matching [a-z0-9_.]{1,64} and drops the rest, so
 * a camelCase key (`stepId`) would vanish without a trace. Callers keep their
 * names; they are snake_cased here, once.
 */
export function toSnakeCaseKey(key: string): string {
  return key
    .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    .replace(/[^A-Za-z0-9_.]+/g, "_")
    .toLowerCase();
}

/**
 * Send an event through the web runtime's `track()` as `product.<event>`.
 * Best-effort: analytics must never be able to break the app. Bounding (20
 * keys, 256-char values, credential redaction) is the runtime's; calls made
 * before the SDK has started are held and replayed.
 */
function sendToClickstack(
  eventName: string,
  metadata?: Record<string, string | number | boolean>,
): void {
  try {
    const props: Record<string, string | number | boolean> = {};
    if (metadata) {
      for (const [key, value] of Object.entries(metadata)) {
        props[toSnakeCaseKey(key)] = value;
      }
    }
    track(PRODUCT_EVENT_PREFIX + eventName, props);
  } catch {
    // Analytics is best-effort and must never surface to the user.
  }
}
