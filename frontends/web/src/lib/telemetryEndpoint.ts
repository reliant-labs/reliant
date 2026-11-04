/**
 * Whether this deployment ships browser telemetry (ClickStack) at all.
 *
 * It does exactly when the runtime config's `OTEL_ENDPOINT` names somewhere to
 * send to. There is one sink: web Sentry ran beside ClickStack until its soak
 * ended, and with it went the `TELEMETRY_SINKS` switch that chose between them.
 *
 * Dependency-free on purpose (beyond the generated config module):
 * analytics.ts, errorReporting.ts and telemetry.ts all read it, and none should
 * pull in another's imports.
 */

import { loadConfig } from "./config_gen";

/** Whether `OTEL_ENDPOINT` names somewhere to send to. */
export function endpointConfigured(endpoint: string | undefined): boolean {
  const value = (endpoint ?? "").trim().toLowerCase();
  return value !== "" && value !== "off";
}

export function clickstackEnabled(): boolean {
  return endpointConfigured(loadConfig().OTEL_ENDPOINT);
}
