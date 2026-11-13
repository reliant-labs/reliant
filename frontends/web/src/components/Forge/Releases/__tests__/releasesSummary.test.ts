// @vitest-environment node
// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import type { ChecksSummary, PipelineCell, ReleaseEnv, ReleaseModel, ReleaseRow } from "../releaseModel";
import { releasesSummary } from "../releasesSummary";

const env = (name: string): ReleaseEnv => ({ id: `env-${name}`, name, kind: "long", current: "", promotedByActor: "", promotedByUserId: "" });
const cell = (name: string, base: string | null, over: Partial<PipelineCell> = {}): PipelineCell => ({ env: env(name), base, followUps: [], by: "", relation: { kind: "current" }, ...over });
const row = (version: string, checks: ChecksSummary | null = null): ReleaseRow =>
  ({ release: { version }, cells: {}, previewsLive: [], scoped: [], rolledBack: [], checks }) as unknown as ReleaseRow;
const model = (pipeline: PipelineCell[], rows: ReleaseRow[]): ReleaseModel => ({ pipeline, rows, longEnvs: pipeline.map((c) => c.env), previews: [] });
const checks = (passed: number, failed: number): ChecksSummary => ({ passed, failed, total: passed + failed, gates: [] });

describe("releasesSummary", () => {
  it("says no release was cut yet, and nothing deployed to an environment yet", () => {
    expect(releasesSummary(model([], []), null)).toEqual({ tone: "good", text: "No release has been cut yet." });
    expect(releasesSummary(model([cell("prod", null)], [row("v1")]), null)).toEqual({ tone: "good", text: "Nothing is deployed to prod yet. One release is ready to promote." });
  });

  it("reads prod when every environment is in scope, and the scoped one otherwise", () => {
    const m = model([cell("dev", "v2"), cell("prod", "v1", { by: "jon" })], [row("v2"), row("v1")]);
    expect(releasesSummary(m, null)?.text).toBe("prod runs v1, promoted by jon.");
    expect(releasesSummary(m, "dev")?.text).toBe("dev runs v2.");
  });

  it("is a single environment's own, whatever it is called", () => {
    expect(releasesSummary(model([cell("main", "v9")], [row("v9", checks(2, 0))]), null)).toEqual({ tone: "good", text: "main runs v9. Its 2 checks passed." });
  });

  it("says what failed, and how many newer releases wait", () => {
    const m = model([cell("prod", "v1", { relation: { kind: "newer", count: 2 } })], [row("v3"), row("v2"), row("v1", checks(1, 1))]);
    expect(releasesSummary(m, null)).toEqual({ tone: "bad", text: "prod runs v1. One of its checks failed. 2 newer releases are ready to promote." });
  });

  it("has nothing to say for an environment it does not know", () => {
    expect(releasesSummary(model([cell("prod", "v1")], [row("v1")]), "staging")).toBeNull();
  });
});
