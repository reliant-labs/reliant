// Copyright (c) 2025 Reliant Labs

/**
 * A GetServiceSignals answer covering each verdict (serviceSignals.ts):
 *
 *   api     sends logs, traces and profiles, and declares all three
 *   worker  sends logs and traces only; its declaration is not recorded
 *   batch   sends logs and traces, and declares profiles that never arrived
 *
 * reliant-daemon, which the overview fixture lists, is absent: it sends
 * nothing in the window and declares nothing.
 */

import type { ServiceSignalsResult } from "@/services/controlPlane/observability/types";

export const FIXTURE_SIGNALS: ServiceSignalsResult = {
  truncated: false,
  services: [
    { service: "api", emitting: ["logs", "profiles", "traces"], declared: ["logs", "profiles", "traces"], declarationKnown: true },
    { service: "batch", emitting: ["logs", "traces"], declared: ["logs", "profiles", "traces"], declarationKnown: true },
    { service: "worker", emitting: ["logs", "traces"], declared: [], declarationKnown: false },
  ],
};
