// @vitest-environment node
// Copyright (c) 2026 Reliant Labs

/**
 * The Security fixtures run on the clock the visual harness and the preview
 * freeze, so their times read as the past there ("2h ago"), never the future.
 */

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import { FIXTURE_ENVS, FIXTURE_NOW, FIXTURE_PROJECT, fixtureVulnerabilityApi, type VulnScenario } from "../fixtures";

const SCENARIOS: VulnScenario[] = ["findings", "clean", "nothing-deployed", "no-sbom", "pending", "failed", "stale", "partial"];

/** Every `…Ms` time in `value` except those that are meant to lie ahead (an exemption's expiry). */
function pastTimes(value: unknown, out: { path: string; ms: number }[] = [], path = ""): { path: string; ms: number }[] {
  if (Array.isArray(value)) value.forEach((item, i) => pastTimes(item, out, `${path}[${i}]`));
  else if (value && typeof value === "object") {
    for (const [key, item] of Object.entries(value)) {
      if (key.endsWith("Ms") && key !== "expiresMs" && typeof item === "number") out.push({ path: `${path}.${key}`, ms: item });
      else pastTimes(item, out, `${path}.${key}`);
    }
  }
  return out;
}

describe("the Security fixtures' clock", () => {
  it("is the visual harness's FIXED_NOW (e2e/visual/clock.ts), which the preview freezes too", () => {
    // src cannot import e2e/ (a composite project includes only src), so the literal is read and compared.
    const base = readFileSync(resolve(__dirname, "../../../../../e2e/visual/clock.ts"), "utf8");
    const args = /export const FIXED_NOW = Date\.UTC\(([^)]*)\)/.exec(base)?.[1];
    expect(args, "FIXED_NOW is no longer a Date.UTC literal in e2e/visual/clock.ts").toBeDefined();
    const [year, month, ...rest] = args!.split(",").map((part) => Number(part.trim()));
    expect(FIXTURE_NOW).toBe(Date.UTC(year!, month!, ...rest));
  });

  it.each(SCENARIOS)("puts nothing in %s that already happened after it", async (scenario) => {
    const api = fixtureVulnerabilityApi(scenario);
    const req = { orgId: null, project: FIXTURE_PROJECT };
    const answers = [
      await api.getVulnerabilitySummary(req),
      await api.listVulnerabilities({ ...req, filters: { includeExempt: true } }),
      await api.getReleaseVulnerabilities({ ...req, version: FIXTURE_ENVS.prod.release }),
      await api.getReleaseVulnerabilities({ ...req, version: FIXTURE_ENVS.staging.release }),
      await api.getVulnerability({ ...req, advisoryId: "CVE-2026-4101" }).catch(() => null),
    ];
    const times = pastTimes(answers);
    // …and the walk saw the times at all.
    if (scenario === "findings") expect(times.length).toBeGreaterThan(10);
    expect(times.filter((t) => t.ms > FIXTURE_NOW)).toEqual([]);
  });
});
