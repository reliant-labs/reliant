/**
 * Reporting a failure the app caught and handled itself.
 *
 * The telemetry SDK records uncaught exceptions and unhandled rejections on
 * its own. A failure the app catches — a failed rpc, an OAuth exchange the
 * provider refused, a chat that would not load — is invisible to it, and
 * reaches the error pipeline (ClickStack's error_events) only through here.
 *
 * Both calls are no-ops unless browser telemetry is running, and they never
 * throw: reporting a failure must not become a second one. The runtime's
 * reportException does nothing until the SDK has started, and telemetry.ts
 * starts it only with an endpoint (telemetryEndpoint.ts) and the user's
 * crash-reporting consent, never in dev; withdrawing consent calls the
 * runtime's disableTelemetry(), which turns reportException off again. So
 * consent is enforced there, not re-read here.
 *
 * Dependency-light on purpose. It imports the runtime's dependency-free
 * barrel, not the `/telemetry` subpath that loads the SDK, and not
 * hooks/privacy-queries: the transport calls this, and privacy-queries reaches
 * the transport through the generated settings hooks, so importing it here
 * is an import cycle.
 */

import { reportException } from "@reliantlabs/forge-web-runtime";
import { clickstackEnabled } from "./telemetryEndpoint";

/**
 * Where a handled failure happened, as span attributes: low-cardinality names
 * (`component`, `operation`, `provider`) and ids. Never user content — a chat
 * message, a file path or a token does not belong here. Undefined values are
 * dropped.
 */
export type ErrorContext = Record<string, string | number | boolean | undefined>;

function attributesOf(
  context: ErrorContext | undefined,
): Record<string, string | number | boolean> | undefined {
  if (!context) return undefined;
  const attributes: Record<string, string | number | boolean> = {};
  for (const [key, value] of Object.entries(context)) {
    if (value !== undefined) attributes[key] = value;
  }
  return attributes;
}

/** Report an error the app caught and handled. */
export function reportError(error: unknown, context?: ErrorContext): void {
  try {
    if (!clickstackEnabled()) return;
    reportException(error, attributesOf(context));
  } catch {
    // Telemetry must never break the app.
  }
}

/**
 * Report a handled failure that has no exception object of its own (a
 * provider's refusal, a state mismatch): an Error carrying `message`, so it
 * groups by that message in the error pipeline.
 */
export function reportFailure(message: string, context?: ErrorContext): void {
  reportError(new Error(message), context);
}
