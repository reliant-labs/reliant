// Copyright (c) 2026 Reliant Labs

/**
 * ML › Evals (02-views §4). Two pages:
 *
 *   evals                          the matrix: every candidate against the
 *                                  baseline on a suite (?suite, ?baseline,
 *                                  ?filter = task category), eval-periodic's
 *                                  score vs step, the gates, recent runs
 *   evals/runs/<eval_run>[?task]   one eval run; ?task opens the task's
 *                                  samples (?filter, ?group = slice, ?sel)
 */

import { EvalMatrixPage } from "./EvalMatrixPage";
import { EvalRunPage } from "./EvalRunPage";

export function MlEvalsView({ sub }: { sub: readonly string[] }) {
  if (sub[0] === "runs" && sub[1]) return <EvalRunPage id={sub[1]} />;
  return <EvalMatrixPage />;
}
