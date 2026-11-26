// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world (./world.ts) as answers: every domain, in the order
 * handlers/ml.ts asks them; and its telemetry, which ./telemetry.ts serves
 * through the observability handlers.
 */

import type { MlDomain } from "./domain";
import type { MlTelemetryDomain } from "./telemetryTypes";
import { evalsAnswers, evalsTelemetry } from "./evals";
import { findingsAnswers, findingsTelemetry } from "./findings";
import { fleetAnswers, fleetTelemetry } from "./fleet";
import { inferenceAnswers, inferenceTelemetry } from "./inference";
import { lineageAnswers } from "./lineage";
import { modelsAnswers, modelsTelemetry } from "./models";
import { queuesAnswers, queuesTelemetry } from "./queues";
import { setupAnswers } from "./setup";
import { trainingAnswers, trainingEventsAndAlerts } from "./training";
import { trainingTelemetry } from "./trainingTelemetry";

export const ML_DOMAINS: readonly MlDomain[] = [
  trainingAnswers,
  fleetAnswers,
  queuesAnswers,
  findingsAnswers,
  setupAnswers,
  evalsAnswers,
  modelsAnswers,
  lineageAnswers,
  inferenceAnswers,
];

/** Every domain's telemetry, served through ObservabilityService and AlertingService (./telemetry.ts). */
export const ML_TELEMETRY: readonly MlTelemetryDomain[] = [trainingTelemetry, trainingEventsAndAlerts, fleetTelemetry, queuesTelemetry, findingsTelemetry, evalsTelemetry, modelsTelemetry, inferenceTelemetry];

export * from "./domain";
export * from "./world";
