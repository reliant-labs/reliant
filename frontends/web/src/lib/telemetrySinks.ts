/**
 * Which telemetry sinks this deployment ships to.
 *
 * The runtime config's `TELEMETRY_SINKS` is a comma list of `sentry` and `clickstack`. The two
 * run in parallel until web Sentry is removed; see the exit criteria in
 * research/frontend-observability/DESIGN.md 6.6.
 *
 * Unset (or no recognised name) means today's behaviour: Sentry on, and
 * ClickStack on only if an ingest endpoint is configured. So a build that
 * configures nothing new behaves exactly as before. `clickstack` without an
 * endpoint is off too: there is nowhere to send to.
 *
 * Dependency-free on purpose (beyond the generated config module):
 * sentry.ts, analytics.ts, ErrorBoundary.tsx and telemetry.ts all read it,
 * and none should pull in another's imports.
 */

import { loadConfig } from "./config_gen";

export interface TelemetrySinks {
  sentry: boolean;
  clickstack: boolean;
}

/** Whether `OTEL_ENDPOINT` names somewhere to send to. */
export function endpointConfigured(endpoint: string | undefined): boolean {
  const value = (endpoint ?? "").trim().toLowerCase();
  return value !== "" && value !== "off";
}

export function parseTelemetrySinks(
  raw: string | undefined,
  endpoint: string | undefined,
): TelemetrySinks {
  const hasEndpoint = endpointConfigured(endpoint);
  const names = (raw ?? "")
    .split(",")
    .map((s) => s.trim().toLowerCase())
    .filter((s) => s === "sentry" || s === "clickstack");

  // A typo must not silently turn off the sink that works today.
  if (names.length === 0) {
    return { sentry: true, clickstack: hasEndpoint };
  }
  return {
    sentry: names.includes("sentry"),
    clickstack: names.includes("clickstack") && hasEndpoint,
  };
}

export function telemetrySinks(): TelemetrySinks {
  const config = loadConfig();
  return parseTelemetrySinks(config.TELEMETRY_SINKS, config.OTEL_ENDPOINT);
}

export function sentryEnabled(): boolean {
  return telemetrySinks().sentry;
}

export function clickstackEnabled(): boolean {
  return telemetrySinks().clickstack;
}
