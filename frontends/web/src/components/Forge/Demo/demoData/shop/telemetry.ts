// Copyright (c) 2025 Reliant Labs

/** shop's traces, logs, service overview, profiles and signals: the busy app (busyApp.ts) over the story. */

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { createBusyApp } from "../busyApp";
import type { TelemetryKeys } from "./shopApi";

export function shopTelemetry(now: number): Pick<ObservabilityApi, TelemetryKeys> {
  return createBusyApp(now);
}
