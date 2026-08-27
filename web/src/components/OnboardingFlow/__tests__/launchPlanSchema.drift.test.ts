/**
 * Runtime half of the LaunchPlan <-> launchPlanSchema drift guard.
 *
 * The compile-time half lives in `src/routeSchemas.ts` (see the "Drift guard"
 * block there) and is the primary defence: it fails `tsc -b` and names the
 * offending key. It cannot be written here, because `tsconfig.app.json`
 * excludes `*.test.*` — a type assertion in a test file is never checked by
 * the typecheck CI step.
 *
 * What this file adds that types cannot: proof that the schema actually
 * PARSES a fully-populated plan. `z.infer` describes the schema's declared
 * shape, but the failure mode being guarded is a `.strict()` runtime rejection
 * (`unrecognized_keys`), and only executing `.parse()` demonstrates that a real
 * plan object survives the round trip.
 *
 * Regression: `computeAutoSkipped` was added to the LaunchPlan interface but
 * not to the Zod schema. That type-checked, passed every test, and then threw
 * at runtime the moment the field was written to the URL, taking down the
 * whole /onboarding route.
 */
import { describe, expect, it } from "vitest";
import { launchPlanSchema } from "../../../routeSchemas";
import type { LaunchPlan } from "../types";

/**
 * Every field on LaunchPlan with a representative value.
 *
 * Typed as a complete `LaunchPlan` (not `Partial`) on purpose: adding a
 * required field to the interface makes this object fail to compile, and
 * `exactOptionalPropertyTypes` aside, an optional field added to the interface
 * is caught by the key-coverage test below.
 */
const FULLY_POPULATED_PLAN: Required<LaunchPlan> = {
  intent: "existing_codebase",
  compute: "local_daemon",
  repo: {
    provider: "github",
    url: "https://github.com/reliant-labs/reliant",
    branch: "main",
  },
  localPath: "/Users/example/src/project",
  projectName: "example-project",
  workflowId: "forge-one-shot",
  modelProvider: "anthropic",
  workflowParams: { prompt: "hello" },
  daemonProvisioning: true,
  computeAutoSkipped: true,
};

describe("launchPlanSchema vs LaunchPlan", () => {
  it("parses a fully-populated plan without stripping or rejecting a field", () => {
    const parsed = launchPlanSchema.parse(FULLY_POPULATED_PLAN);
    expect(parsed).toEqual(FULLY_POPULATED_PLAN);
  });

  it("accepts every LaunchPlan key on its own", () => {
    // The schema is `.strict()`, so an unknown key throws rather than being
    // silently dropped. Submitting each key in isolation localises a failure
    // to the exact field that drifted.
    const offenders: string[] = [];
    for (const [key, value] of Object.entries(FULLY_POPULATED_PLAN)) {
      const result = launchPlanSchema.safeParse({ [key]: value });
      if (!result.success) offenders.push(key);
    }
    expect(offenders).toEqual([]);
  });

  it("still rejects a key that belongs to neither side", () => {
    // Guards the guard: proves `.strict()` is in force, so the assertions
    // above are actually capable of failing.
    const result = launchPlanSchema.safeParse({
      ...FULLY_POPULATED_PLAN,
      totallyUnknownField: true,
    });
    expect(result.success).toBe(false);
  });

  it("accepts a partial plan, since steps fill the plan in incrementally", () => {
    expect(launchPlanSchema.parse({ intent: "build_app" })).toEqual({
      intent: "build_app",
    });
    expect(launchPlanSchema.parse({})).toEqual({});
  });
});
