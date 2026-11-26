// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world (./world.ts) as answers: every domain, in the order
 * handlers/ml.ts asks them.
 */

import type { MlDomain } from "./domain";
import { evalsAnswers } from "./evals";
import { findingsAnswers } from "./findings";
import { fleetAnswers } from "./fleet";
import { inferenceAnswers } from "./inference";
import { lineageAnswers } from "./lineage";
import { modelsAnswers } from "./models";
import { queuesAnswers } from "./queues";
import { setupAnswers } from "./setup";
import { trainingAnswers } from "./training";
import { trainingTelemetryAnswers } from "./trainingTelemetry";

export const ML_DOMAINS: readonly MlDomain[] = [
  trainingAnswers,
  trainingTelemetryAnswers,
  fleetAnswers,
  queuesAnswers,
  findingsAnswers,
  setupAnswers,
  evalsAnswers,
  modelsAnswers,
  lineageAnswers,
  inferenceAnswers,
];

export * from "./domain";
export * from "./world";
