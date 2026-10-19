// Copyright (c) 2025 Reliant Labs

/**
 * The Releases pages' rules, over the fixture ledger (releaseFixtures.ts):
 * live / partly live / ran before / never per environment, scoped deploys
 * never overstated as live (D5), rolled back worked out from cut order, the
 * pipeline's distance from prod, checks, filters, and what a release is
 * compared with by default.
 */

import { describe, expect, it } from "vitest";

import type { ReleasePromotion } from "@/services/forge/releases";

import {
  buildReleaseModel,
  defaultBaseOf,
  detailEnvOf,
  filterRows,
  leadTimeMs,
  releaseStatus,
  releaseTimeline,
  shortVersion,
  type PromotionsByEnv,
} from "../releaseModel";
import { FIXTURE_NAMES, PROMOTIONS, RELEASE_ENVS, RELEASES } from "../__fixtures__/releaseFixtures";

const ledger: PromotionsByEnv = { get: (id) => PROMOTIONS[id] };
const V = RELEASES.map((release) => release.version);
const model = buildReleaseModel(RELEASES, RELEASE_ENVS, ledger, FIXTURE_NAMES);
const row = (index: number) => model.rows[index]!;

describe("shortVersion", () => {
  it("reads a date-named auto version by its date, and leaves anything else whole", () => {
    expect(shortVersion("20261018.155157-8aecd00c380b")).toBe("20261018.155157");
    expect(shortVersion("v1.8.2")).toBe("v1.8.2");
    expect(shortVersion("v1.8.2-rc.1")).toBe("v1.8.2-rc.1");
  });
});

describe("environment cells", () => {
  it("marks the newest whole-release promotion live, and what ran before as ran", () => {
    expect(row(0).cells).toMatchObject({ prod: { state: "live" }, dev: { state: "live" }, e2e: { state: "never" } });
    expect(row(1).cells).toMatchObject({ prod: { state: "ran" }, dev: { state: "ran" }, e2e: { state: "never" } });
    expect(row(3).cells).toMatchObject({ prod: { state: "ran" }, e2e: { state: "live" }, dev: { state: "ran" } });
  });

  it("tags a release whose newest promotion was scoped, without calling the other components live on it", () => {
    expect(row(0).scoped).toEqual(["admin-web", "internal-console"]);
    expect(row(1).scoped).toEqual([]);
    // A scoped deploy of a release OTHER than the base is partly live, never live.
    const scopedOther: ReleasePromotion = { ...PROMOTIONS["env-prod"]![0]!, id: "p-x", releaseVersion: V[2]!, targets: ["admin-web"] };
    const partial = buildReleaseModel(RELEASES, RELEASE_ENVS, { get: (id) => (id === "env-prod" ? [scopedOther, ...PROMOTIONS[id]!] : PROMOTIONS[id]) });
    expect(partial.rows[2]!.cells.prod).toEqual({ state: "partial", targets: ["admin-web"] });
    expect(partial.rows[0]!.cells.prod!.state).toBe("live");
    expect(releaseStatus({ ...partial.rows[2]!, previewsLive: [] }).label).toBe("Live on prod for admin-web");
  });

  it("collapses the previews into one cell: the ones running the release now", () => {
    expect(row(0).previewsLive).toEqual(["pr-984", "pr-986"]);
    expect(row(4).previewsLive).toEqual(["pr-981"]);
    expect(row(2).previewsLive).toEqual([]);
  });

  it("falls back to the live view when an environment's ledger is unreadable, and claims nothing about the past", () => {
    const blind = buildReleaseModel(RELEASES, RELEASE_ENVS, { get: () => undefined });
    expect(blind.rows[3]!.cells.e2e!.state).toBe("live");
    expect(blind.rows[1]!.cells.prod!.state).toBe("never");
    expect(blind.rows[0]!.checks).toBeNull();
  });
});

describe("rolled back", () => {
  it("is a whole-release promotion of an OLDER release replacing this one", () => {
    expect(row(2).rolledBack).toEqual(["prod"]);
    expect(row(5).rolledBack).toEqual(["e2e"]);
    expect(row(0).rolledBack).toEqual([]);
    expect(row(3).rolledBack).toEqual([]);
  });
});

describe("checks", () => {
  it("keeps one verdict per gate (the latest), and counts a failure", () => {
    expect(row(0).checks).toMatchObject({ passed: 2, total: 2, failed: 0 });
    expect(row(2).checks).toMatchObject({ passed: 1, total: 2, failed: 1 });
    expect(row(6).checks).toBeNull();
  });
});

describe("pipeline", () => {
  it("says what is live where, how far from prod, and who put it there", () => {
    const [prod, e2e, dev] = model.pipeline;
    expect(prod).toMatchObject({ base: V[0], by: "sean", relation: { kind: "current" } });
    expect(prod!.followUps).toMatchObject([{ version: V[0], targets: ["admin-web", "internal-console"], by: "G3" }]);
    expect(e2e).toMatchObject({ base: V[3], relation: { kind: "behind", count: 3, of: "prod" }, by: "reliant-01" });
    expect(dev).toMatchObject({ base: V[0], relation: { kind: "current" } });
  });
});

describe("filters", () => {
  const filter = (over: Partial<Parameters<typeof filterRows>[1]>) =>
    filterRows(model.rows, { env: "prod", everyRelease: false, query: "", ...over }).map((r) => r.release.version);

  it("narrows to the releases that ran in the scoped environment, unless asked for every one", () => {
    expect(filter({ env: "e2e" })).toEqual([V[3], V[4], V[5], V[6]]);
    expect(filter({ env: "e2e", everyRelease: true })).toHaveLength(RELEASES.length);
    expect(filter({ env: "pr-984" })).toEqual([V[0]]);
  });

  it("matches version, sha, subject and author; and filters by author and status", () => {
    expect(filter({ query: "fluxobserve" })).toEqual([V[3]]);
    expect(filter({ query: RELEASES[1]!.gitCommit.slice(0, 8) })).toEqual([V[1]]);
    expect(filter({ author: "sean" })).toEqual([V[0], V[4], V[5]]);
    expect(filter({ status: "live" })).toEqual([V[0]]);
    expect(filter({ status: "rolled-back", env: "all" })).toEqual([V[2], V[5]]);
    expect(filter({ status: "scoped", env: "all" })).toEqual([V[0]]);
  });
});

describe("one release", () => {
  it("states where it is live, or that it ran before, or that it never ran", () => {
    expect(releaseStatus(row(0))).toEqual({ tone: "success", label: "Live on prod, dev, pr-984, pr-986" });
    expect(releaseStatus(row(1))).toEqual({ tone: "neutral", label: "Ran before on prod, dev" });
  });

  it("reads the scoped environment when the release was promoted there, else the most recent one", () => {
    expect(detailEnvOf(V[3]!, "e2e", RELEASE_ENVS, ledger)!.env.name).toBe("e2e");
    expect(detailEnvOf(V[1]!, "e2e", RELEASE_ENVS, ledger)!.env.name).toBe("prod");
  });

  it("compares with what the environment ran before it, else the release cut before it", () => {
    expect(defaultBaseOf(V[1]!, RELEASES, PROMOTIONS["env-prod"])).toBe(V[3]);
    expect(defaultBaseOf(V[0]!, RELEASES, PROMOTIONS["env-prod"])).toBe(V[1]);
    expect(defaultBaseOf(V[1]!, RELEASES, undefined)).toBe(V[2]);
    expect(defaultBaseOf(V[6]!, RELEASES, undefined)).toBe(V[7]);
    expect(defaultBaseOf(V[7]!, RELEASES, undefined)).toBeNull();
  });

  it("tells the promotion story: cut, each promotion, and what replaced it", () => {
    const titles = releaseTimeline(RELEASES[2]!, RELEASE_ENVS, ledger, RELEASES, FIXTURE_NAMES).map((entry) => `${entry.title}|${entry.tone}`);
    // Prod's promotion is "bad": its smoke check failed. Then an older release replaced it there.
    expect(titles).toEqual(["Cut|ok", "Promoted to dev|ok", "Promoted to prod|bad", "Rolled back in prod|bad", "Superseded in dev|neutral"]);
  });

  it("measures lead time from the commit to the first promotion, and says nothing when the commit time is unknown", () => {
    expect(leadTimeMs(RELEASES[0]!, PROMOTIONS["env-prod"])).toBeGreaterThan(0);
    expect(leadTimeMs(RELEASES[6]!, PROMOTIONS["env-prod"])).toBeNull();
  });
});
