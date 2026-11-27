// Copyright (c) 2026 Reliant Labs

import { expect, it } from "vitest";

import { ML_VIEWS } from "../../Console/paths";
import { DEMO_TIPS } from "../tips/tipCopy";
import { demoTipKey, isDemoSectionPage } from "../tips/tipKeys";

it("every ML view has a tip of its own, and its drill-downs show it", () => {
  for (const view of ML_VIEWS) {
    const key = demoTipKey(`/forge/ml/${view}`, {});
    expect(key, view).toBe(`ml-${view}`);
    expect(DEMO_TIPS[key!].body, view).toBeTruthy();
  }
  expect(demoTipKey("/forge/ml/runs/llama-8b-support-v3/phases/sft", { tab: "system" })).toBe("ml-runs");
  expect(demoTipKey("/forge/ml/fleet/gpus/GPU-1", {})).toBe("ml-fleet");
  // Not an ML view: no tip, as for any other path the demo has no page for.
  expect(demoTipKey("/forge/ml/nope", {})).toBeNull();
});

it("an ML view opens its tip by itself; a drill-down or an open row does not", () => {
  expect(isDemoSectionPage("/forge/ml/overview", {})).toBe(true);
  expect(isDemoSectionPage("/forge/ml/runs", { app: "shop" })).toBe(true);
  expect(isDemoSectionPage("/forge/ml/runs/llama-8b-support-v3", {})).toBe(false);
  expect(isDemoSectionPage("/forge/ml/queues", { sel: "reranker-hpo-trial-7" })).toBe(false);
});
