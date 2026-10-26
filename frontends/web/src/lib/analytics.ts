/**
 * Product event pipe.
 *
 * `trackEvent` fans out to the sinks telemetrySinks.ts selects. Both are
 * optional and independent: one failing never stops the other.
 *
 *  - ClickStack, through forge's web runtime `track()`, when the build ships to
 *    it (the runtime config's TELEMETRY_SINKS includes `clickstack` and its
 *    OTEL_ENDPOINT is set). The event becomes a span named `product.<event>` on the same session
 *    as the page's errors and replay; the runtime bounds and redacts the props.
 *    This is where funnels are queried: the backend's product events are log
 *    records with the same `product.` names (dev-docs/product-events.md).
 *
 *  - Sentry, while web Sentry is still on (it is being replaced by ClickStack;
 *    research/frontend-observability/DESIGN.md 6.6). It keeps the
 *    funnel visible until ClickStack is the only sink, and because funnel events
 *    and errors share a session, a funnel cliff arrives with the replay of the
 *    user hitting it.
 *
 * In Sentry, events land as breadcrumbs (cheap, and they ride along on any
 * error the session later reports) plus, for the small set of funnel-defining
 * events, an explicit message that alert rules can match on. See
 * docs/observability/alert-rules.md for the rules that consume these.
 *
 * The user is attached by telemetry.ts (`setTelemetryUserId`), not here.
 */

import * as Sentry from "@sentry/react";
import { privacyConsent } from "../hooks/privacy-queries";
import { track } from "@reliantlabs/forge-web-runtime";
import { isDev } from "./constants";
import { clickstackEnabled, sentryEnabled } from "./telemetrySinks";

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
 * Track a product event. No-op when analytics is disabled or in dev mode.
 * Delivery to the sinks is independent.
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

  if (sentryEnabled()) sendToSentry(eventName, stringMeta);
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
 * Best-effort like the other sink. Bounding (20 keys, 256-char values, credential redaction) is the
 * runtime's; calls made before the SDK has started are held and replayed.
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
