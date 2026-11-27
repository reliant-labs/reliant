// Copyright (c) 2026 Reliant Labs

import "@/components/Forge/Ml/__tests__/jsdomShims";
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { ListMlFindingsResponseSchema, MlFindingCategory, MlFindingSchema, MlFindingSeverity } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { findingsAnswers } from "@/fixtures/shop/model/ml/findings";
import { FINDINGS } from "@/fixtures/shop/model/ml/world";

import { filterFindings, groupFindings, inboxKpis, parseDiff, parseFilter, prNumber, serializeFilter, sortFindings } from "../recsModel";

const finding = (id: string, category: MlFindingCategory, severity: MlFindingSeverity, usd: number, gpuH = 0) => create(MlFindingSchema, { id, category, severity, title: id, state: 1, impact: { usdPerDay: usd, gpuHoursPerDay: gpuH } });

describe("recsModel", () => {
  it("round-trips the URL filter and ignores junk", () => {
    const f = parseFilter("sev=critical;cat=waste;state=all;sort=age;bogus=1;cat2");
    expect(f).toEqual({ severity: "critical", category: "waste", state: "all", sort: "age" });
    expect(parseFilter(serializeFilter(f))).toEqual(f);
    expect(serializeFilter(parseFilter(undefined))).toBeUndefined();
  });

  it("sorts by money, GPU-hours, then severity", () => {
    const xs = [finding("a", 1, MlFindingSeverity.INFO, 0), finding("b", 2, MlFindingSeverity.WARNING, 47), finding("c", 1, MlFindingSeverity.CRITICAL, 0), finding("d", 5, MlFindingSeverity.WARNING, 657, 230), finding("e", 2, MlFindingSeverity.WARNING, 0, 10)];
    expect(sortFindings(xs, "impact").map((f) => f.id)).toEqual(["d", "b", "e", "c", "a"]);
    expect(sortFindings(xs, "severity")[0].id).toBe("c");
  });

  it("groups by category by money, with coverage folded last", () => {
    const xs = sortFindings(
      [finding("h", MlFindingCategory.HEALTH, MlFindingSeverity.CRITICAL, 547), finding("w", MlFindingCategory.WASTE, MlFindingSeverity.WARNING, 47), finding("cov1", MlFindingCategory.COVERAGE, MlFindingSeverity.INFO, 0), finding("t", MlFindingCategory.TRAINING_EFFICIENCY, MlFindingSeverity.WARNING, 657), finding("cov2", MlFindingCategory.COVERAGE, MlFindingSeverity.INFO, 0)],
      "impact",
    );
    const groups = groupFindings(xs, "category");
    expect(groups.map((g) => g.label)).toEqual(["Training efficiency", "Health", "Waste", "Telemetry coverage"]);
    expect(groups.at(-1)).toMatchObject({ coverage: true });
    expect(groups.at(-1)!.findings).toHaveLength(2);
    expect(groupFindings(xs, "none").map((g) => g.key)).toEqual(["all", "coverage"]);
    expect(groupFindings(xs, "severity")[0].label).toBe("Critical");
  });

  it("on the fixture: the inbox leads with money and the KPIs add up", () => {
    const res = create(ListMlFindingsResponseSchema, findingsAnswers.listMlFindings!({ orgId: "o" } as never, () => Date.UTC(2026, 10, 26, 15)) as never);
    const shown = sortFindings(filterFindings(res.findings, parseFilter(undefined)), "impact");
    expect(shown[0].id).toBe(FINDINGS.straggler);
    const groups = groupFindings(shown, "category");
    expect(groups.at(-1)?.coverage).toBe(true);
    expect(groups.at(-1)?.findings).toHaveLength(5);
    const k = inboxKpis(res.findings, res.coverageRatio);
    expect(k.open).toBe(shown.length);
    expect(k.usdPerDay).toBeCloseTo(shown.reduce((a, f) => a + (f.impact?.usdPerDay ?? 0), 0));
    expect(k.runsAtRisk).toBeGreaterThan(0);
    expect(filterFindings(res.findings, parseFilter("cat=waste")).every((f) => f.category === MlFindingCategory.WASTE)).toBe(true);
    expect(filterFindings(res.findings, parseFilter(undefined), "r4vb").map((f) => f.id)).toContain(FINDINGS.xid79);
  });

  it("splits a diff into coloured lines", () => {
    const lines = parseDiff("--- a/x.k\n+++ b/x.k\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n");
    expect(lines.map((l) => l.kind)).toEqual(["file", "file", "hunk", "ctx", "del", "add"]);
    expect(prNumber("https://github.com/acme/shop/pull/412")).toBe("#412");
  });
});
