// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The Changes evaluator against forge/pkg/changes's golden vectors, which the
// Go package's tests read too: a vector both pass is a result the twins agree
// on. The property tests run over the same fixed unit sets and assert the
// same recorded counts as pkg/changes/properties_test.go.
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import {
  evaluate,
  murmur3,
  parseSnapshot,
  previewHash,
  type EvalContext,
  type Reason,
  type Result,
  type Snapshot,
} from "./changes.js";

interface VectorResult {
  variant: string;
  value?: unknown;
  reason: Reason;
  run: string;
  enrolled: boolean;
}

interface VectorCase {
  name?: string;
  snapshot?: string;
  key: string;
  context: EvalContext;
  expect: VectorResult;
}

interface SplitVector {
  variant: string;
  weight_bp: number;
}

interface Vectors {
  murmur3: Array<{ input: string; hash: number }>;
  preview_hash: Array<{ key: string; unit: string; hash: string }>;
  snapshots: Record<string, unknown>;
  cases: VectorCase[];
  invalid: { snapshot: string; context: EvalContext; keys: string[] };
  parse: Array<{
    name: string;
    json: string;
    ok: boolean;
    revision?: number;
    cases?: VectorCase[];
  }>;
  properties: {
    unit_prefix: string;
    ramp: {
      key: string;
      salt: string;
      units: number;
      steps_bp: number[];
      enrolled: number[];
    };
    resalt: {
      key: string;
      salts: string[];
      percent_bp: number;
      units: number;
      enrolled: number[];
      overlap: number;
    };
    split: {
      key: string;
      salt: string;
      units: number;
      split: SplitVector[];
      counts: Record<string, number>;
    };
    independence: {
      key: string;
      salt: string;
      units: number;
      percents_bp: number[];
      enrolled: number[];
      treatment: number[];
    };
  };
}

const vectors = JSON.parse(
  readFileSync(
    resolve(
      dirname(fileURLToPath(import.meta.url)),
      "../../pkg/changes/testdata/vectors.json",
    ),
    "utf8",
  ),
) as Vectors;

const FULL_BP = 10000;

function snapshotNamed(name: string): Snapshot {
  const raw = vectors.snapshots[name];
  if (raw === undefined) {
    throw new Error(`no snapshot ${name} in vectors.json`);
  }
  return parseSnapshot(JSON.stringify(raw));
}

function checkResult(name: string, got: Result, want: VectorResult): void {
  expect(
    {
      variant: got.variant,
      reason: got.reason,
      run: got.run,
      enrolled: got.enrolled,
    },
    name,
  ).toEqual({
    variant: want.variant,
    reason: want.reason,
    run: want.run,
    enrolled: want.enrolled,
  });
  if ("value" in want) {
    expect(got.value, name).toEqual(want.value);
  } else {
    expect(got.value, `${name}: the caller's default`).toBeUndefined();
  }
  expect(got.enrolled, `${name}: enrolled with reason ${got.reason}`).toBe(
    got.reason === "rollout" || got.reason === "experiment",
  );
}

describe("golden vectors", () => {
  it("hashes like the reference MurmurHash3 x86_32", () => {
    for (const c of vectors.murmur3) {
      expect(murmur3(c.input), JSON.stringify(c.input)).toBe(c.hash);
    }
    for (const c of vectors.preview_hash) {
      expect(previewHash(c.key, c.unit), `${c.key}/${c.unit}`).toBe(c.hash);
    }
  });

  it("evaluates every case", () => {
    const snaps = new Map<string, Snapshot>();
    for (const c of vectors.cases) {
      const name = c.snapshot ?? "";
      let s = snaps.get(name);
      if (s === undefined) {
        s = snapshotNamed(name);
        snaps.set(name, s);
      }
      checkResult(c.name ?? c.key, evaluate(s, c.key, c.context), c.expect);
    }
    expect(snapshotNamed("main").revision).toBe(42);
  });

  it("isolates each invalid change", () => {
    const s = snapshotNamed(vectors.invalid.snapshot);
    for (const key of vectors.invalid.keys) {
      checkResult(key, evaluate(s, key, vectors.invalid.context), {
        variant: "",
        reason: "invalid",
        run: "",
        enrolled: false,
      });
    }
  });

  it("parses or rejects each snapshot text", () => {
    for (const c of vectors.parse) {
      if (!c.ok) {
        expect(() => parseSnapshot(c.json), c.name).toThrow();
        continue;
      }
      const s = parseSnapshot(c.json);
      expect(s.revision, c.name).toBe(c.revision);
      for (const e of c.cases ?? []) {
        checkResult(
          `${c.name}: ${e.key}`,
          evaluate(s, e.key, e.context),
          e.expect,
        );
      }
    }
  });

  it("treats no snapshot as unknown and freezes values", () => {
    expect(evaluate(undefined, "x", { targetingKey: "u" })).toEqual({
      variant: "",
      value: undefined,
      reason: "unknown",
      run: "",
      enrolled: false,
    });
    const r = evaluate(snapshotNamed("main"), "banner-copy", {});
    expect(Object.isFrozen(r.value)).toBe(true);
  });
});

// ── Properties ───────────────────────────────────────────────────────────

const P = vectors.properties;

function unitIds(n: number): string[] {
  return Array.from({ length: n }, (_, i) => `${P.unit_prefix}${i}`);
}

function propSnapshot(
  key: string,
  variants: Record<string, unknown>,
  delivery: Record<string, unknown>,
): Snapshot {
  return parseSnapshot(
    JSON.stringify({
      schema: "forge.changes/v1",
      revision: 1,
      changes: { [key]: { variants, delivery } },
    }),
  );
}

/** Within five standard deviations of n trials at probability p. */
function withinBinomial(count: number, n: number, p: number): boolean {
  const mean = n * p;
  return Math.abs(count - mean) <= 5 * Math.sqrt(mean * (1 - p)) + 1;
}

const AB = { control: false, treatment: true };
const FIFTY_FIFTY = [
  { variant: "control", weight_bp: 5000 },
  { variant: "treatment", weight_bp: 5000 },
];

function experiment(
  salt: string,
  percentBP: number,
  split: unknown,
): Record<string, unknown> {
  return {
    mode: "experiment",
    unit: "user",
    run: salt,
    salt,
    percent_bp: percentBP,
    split,
  };
}

describe("properties", () => {
  it("ramping only adds units, and each keeps its variant", () => {
    const r = P.ramp;
    const ids = unitIds(r.units);
    const steps = r.steps_bp.map((pct) => {
      const s = propSnapshot(r.key, AB, experiment(r.salt, pct, FIFTY_FIFTY));
      const cur = new Map<string, string>();
      for (const id of ids) {
        const res = evaluate(s, r.key, { targetingKey: id });
        if (res.enrolled) {
          cur.set(id, res.variant);
        }
      }
      expect(
        withinBinomial(cur.size, r.units, pct / FULL_BP),
        `${pct} bp`,
      ).toBe(true);
      return cur;
    });
    steps.forEach((small, i) => {
      steps.forEach((large, j) => {
        if (r.steps_bp[i]! > r.steps_bp[j]!) {
          return;
        }
        for (const [id, variant] of small) {
          expect(
            large.get(id),
            `${id} from ${r.steps_bp[i]} to ${r.steps_bp[j]} bp`,
          ).toBe(variant);
        }
      });
    });
    expect(steps.map((s) => s.size)).toEqual(r.enrolled);
  });

  it("a new salt re-randomizes", () => {
    const r = P.resalt;
    const ids = unitIds(r.units);
    const sets = r.salts.map((salt) => {
      const s = propSnapshot(
        r.key,
        AB,
        experiment(salt, r.percent_bp, FIFTY_FIFTY),
      );
      return new Set(
        ids.filter((id) => evaluate(s, r.key, { targetingKey: id }).enrolled),
      );
    });
    const overlap = [...sets[0]!].filter((id) => sets[1]!.has(id)).length;
    const q = r.percent_bp / FULL_BP;
    expect(withinBinomial(overlap, r.units, q * q)).toBe(true);
    expect(sets.map((s) => s.size)).toEqual(r.enrolled);
    expect(overlap).toBe(r.overlap);
  });

  it("splits in proportion to the weights", () => {
    const sp = P.split;
    const variants = Object.fromEntries(
      sp.split.map((e) => [e.variant, e.variant]),
    );
    const s = propSnapshot(
      sp.key,
      variants,
      experiment(sp.salt, FULL_BP, sp.split),
    );
    const counts: Record<string, number> = {};
    for (const id of unitIds(sp.units)) {
      const res = evaluate(s, sp.key, { targetingKey: id });
      expect(res.enrolled, id).toBe(true);
      counts[res.variant] = (counts[res.variant] ?? 0) + 1;
    }
    for (const e of sp.split) {
      expect(
        withinBinomial(counts[e.variant] ?? 0, sp.units, e.weight_bp / FULL_BP),
        e.variant,
      ).toBe(true);
    }
    expect(counts).toEqual(sp.counts);
  });

  it("enrollment is independent of variant", () => {
    const ind = P.independence;
    const split = P.split.split;
    const variants = Object.fromEntries(
      split.map((e) => [e.variant, e.variant]),
    );
    const treatmentBP = split.find((e) => e.variant === "treatment")!.weight_bp;
    const ids = unitIds(ind.units);
    const assigned = new Map<string, string>();
    const enrolled: number[] = [];
    const treatment: number[] = [];
    for (const pct of ind.percents_bp) {
      const s = propSnapshot(
        ind.key,
        variants,
        experiment(ind.salt, pct, split),
      );
      let n = 0;
      let tr = 0;
      for (const id of ids) {
        const res = evaluate(s, ind.key, { targetingKey: id });
        if (!res.enrolled) {
          continue;
        }
        n++;
        if (res.variant === "treatment") {
          tr++;
        }
        const prev = assigned.get(id);
        if (prev !== undefined) {
          expect(res.variant, `${id} at ${pct} bp`).toBe(prev);
        }
        assigned.set(id, res.variant);
      }
      expect(withinBinomial(tr, n, treatmentBP / FULL_BP), `${pct} bp`).toBe(
        true,
      );
      enrolled.push(n);
      treatment.push(tr);
    }
    expect(enrolled).toEqual(ind.enrolled);
    expect(treatment).toEqual(ind.treatment);
  });

  it("only rollout and experiment ever enroll", () => {
    const s = snapshotNamed("main");
    const keys = [...vectors.cases.map((c) => c.key), ...vectors.invalid.keys];
    const seen = new Set<Reason>();
    for (const id of [...unitIds(500), "", "alice", "bob"]) {
      const contexts: EvalContext[] = [
        { targetingKey: id },
        {
          targetingKey: id,
          attributes: { plan: "paid", account: id, visitor: id },
        },
        { attributes: { plan: "free", account: id } },
      ];
      for (const ctx of contexts) {
        for (const key of keys) {
          const res = evaluate(s, key, ctx);
          seen.add(res.reason);
          if (
            res.enrolled !==
            (res.reason === "rollout" || res.reason === "experiment")
          ) {
            throw new Error(
              `${key}/${JSON.stringify(ctx)}: enrolled ${res.enrolled} with ${res.reason}`,
            );
          }
        }
      }
    }
    expect([...seen].sort()).toEqual(
      [
        "unknown",
        "default",
        "invalid",
        "preview",
        "everyone",
        "no_unit",
        "ineligible",
        "not_enrolled",
        "rollout",
        "experiment",
      ].sort(),
    );
  });

  it("a previewed unit is never enrolled", () => {
    const ids = unitIds(1000);
    const d = experiment("s_pv", FULL_BP, FIFTY_FIFTY);
    d["preview"] = ids.slice(0, 100).map((id) => ({
      unit_hash: previewHash("pv", id),
      variant: "treatment",
    }));
    const s = propSnapshot("pv", AB, d);
    ids.forEach((id, i) => {
      const res = evaluate(s, "pv", { targetingKey: id });
      if (i < 100) {
        expect(res, id).toMatchObject({
          reason: "preview",
          enrolled: false,
          variant: "treatment",
        });
      } else {
        expect(res.enrolled, id).toBe(true);
      }
    });
  });
});
