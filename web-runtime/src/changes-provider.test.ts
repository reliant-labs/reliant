// The OpenFeature web provider's reading of a forge result: the reason
// mapping, errors with the call site's default, type mismatches, flag
// metadata and the evaluation context. The twin of
// pkg/changes/openfeature/provider_test.go.
import { describe, expect, it } from "vitest";

import type { EvalContext, Result } from "./changes.js";
import { forgeChangesProvider } from "./changes-provider.js";
import type { ChangesStore } from "./changes-store.js";

/** A store answering with fixed results, recording the contexts it saw. */
function fakeStore(results: Record<string, Partial<Result>>): {
  store: ChangesStore;
  seen: EvalContext[];
} {
  const seen: EvalContext[] = [];
  return {
    seen,
    store: {
      revision: 9,
      evaluate(key: string, ctx: EvalContext = {}): Result {
        seen.push(ctx);
        return {
          variant: "",
          value: undefined,
          reason: "unknown",
          run: "",
          enrolled: false,
          ...results[key],
        };
      },
    },
  };
}

const RESULTS: Record<string, Partial<Result>> = {
  exp: {
    variant: "treatment",
    value: true,
    reason: "experiment",
    run: "r_1",
    enrolled: true,
  },
  roll: {
    variant: "on",
    value: true,
    reason: "rollout",
    run: "r_2",
    enrolled: true,
  },
  prev: { variant: "control", value: false, reason: "preview" },
  all: { variant: "blue", value: "blue", reason: "everyone" },
  dflt: { variant: "control", value: false, reason: "default" },
  zero: { reason: "not_enrolled", run: "r_3" },
  inel: { variant: "control", value: false, reason: "ineligible", run: "r_1" },
  nounit: { variant: "control", value: false, reason: "no_unit", run: "r_1" },
  bad: { reason: "invalid" },
  num: { variant: "half", value: 0.5, reason: "everyone" },
  obj: { variant: "small", value: { max: 3 }, reason: "everyone" },
};

describe("forgeChangesProvider", () => {
  const { store, seen } = fakeStore(RESULTS);
  const p = forgeChangesProvider(store);

  it("names itself forge, for the client", () => {
    expect(p.metadata.name).toBe("forge");
    expect(p.runsOn).toBe("client");
  });

  it.each([
    ["exp", "SPLIT", true, "treatment", "r_1"],
    ["roll", "SPLIT", true, "on", "r_2"],
    ["prev", "TARGETING_MATCH", false, "control", undefined],
    ["dflt", "DEFAULT", false, "control", undefined],
    ["inel", "DEFAULT", false, "control", "r_1"],
    ["nounit", "DEFAULT", false, "control", "r_1"],
    ["zero", "DEFAULT", true, undefined, "r_3"],
  ])("maps %s to %s", (key, reason, value, variant, run) => {
    const r = p.resolveBooleanEvaluation(key, true, {});
    expect(r.reason).toBe(reason);
    expect(r.value).toBe(value);
    expect(r.variant).toBe(variant);
    expect(r.errorCode).toBeUndefined();
    expect(r.flagMetadata.revision).toBe(9);
    expect(r.flagMetadata.run).toBe(run);
    expect(typeof r.flagMetadata.reason).toBe("string");
  });

  it("reports unknown and invalid changes as errors with the call site's default", () => {
    expect(p.resolveBooleanEvaluation("nope", true, {})).toMatchObject({
      value: true,
      reason: "ERROR",
      errorCode: "FLAG_NOT_FOUND",
    });
    expect(p.resolveBooleanEvaluation("bad", true, {})).toMatchObject({
      value: true,
      reason: "ERROR",
      errorCode: "PARSE_ERROR",
    });
  });

  it("reads each type, and a value of another type is a mismatch", () => {
    expect(p.resolveStringEvaluation("all", "red", {})).toMatchObject({
      value: "blue",
      variant: "blue",
      reason: "STATIC",
    });
    expect(p.resolveNumberEvaluation("num", 1, {}).value).toBe(0.5);
    expect(p.resolveObjectEvaluation("obj", {}, {}).value).toEqual({ max: 3 });
    expect(p.resolveBooleanEvaluation("all", true, {})).toMatchObject({
      value: true,
      reason: "ERROR",
      errorCode: "TYPE_MISMATCH",
    });
    expect(p.resolveNumberEvaluation("all", 1, {}).errorCode).toBe(
      "TYPE_MISMATCH",
    );
  });

  it("passes the targeting key and string attributes, dropping the rest", () => {
    seen.length = 0;
    p.resolveBooleanEvaluation("exp", false, {
      targetingKey: "u1",
      plan: "paid",
      seats: 4,
      beta: true,
    });
    expect(seen).toEqual([
      { targetingKey: "u1", attributes: { plan: "paid" } },
    ]);
  });
});
