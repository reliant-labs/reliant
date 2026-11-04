// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import { clientBuiltinDocument, isClientBuiltin, LLM_USAGE_KEY, LLM_USAGE_SUMMARY, withClientBuiltins } from "../clientBuiltins";
import type { DashboardIndex, DashboardSummary } from "../dashboardTypes";

const row = (key: string, source: DashboardSummary["source"]): DashboardSummary => ({
  ...LLM_USAGE_SUMMARY,
  ref: { kind: source === "builtin" ? "builtin" : "workspace", projectId: source === "builtin" ? "" : "shop", key },
  source,
  viewKind: undefined,
  group: undefined,
  title: key,
});

const index = (dashboards: DashboardSummary[]): DashboardIndex => ({ dashboards, canWrite: false });
const project = { projectId: "shop", envId: undefined };

describe("client-owned built-in dashboards", () => {
  it("lists LLM usage in the Product group after the server's built-ins, in a project's list", () => {
    const listed = withClientBuiltins(index([row("org-costs", "builtin"), row("org-usage", "builtin"), row("latency", "workspace")]), project);
    expect(listed.dashboards.map((d) => d.ref.key)).toEqual(["org-costs", "org-usage", LLM_USAGE_KEY, "latency"]);
    expect(listed.dashboards[2]).toMatchObject({ source: "builtin", viewKind: "llm-usage", group: "product", readOnly: true });
    // Without billing:read the server lists no built-ins; LLM usage needs none.
    expect(withClientBuiltins(index([row("latency", "workspace")]), { projectId: "shop", envId: "env-prod" }).dashboards[0]!.ref.key).toBe(LLM_USAGE_KEY);
  });

  it("is not added to the organization-wide list, to an empty project, nor twice", () => {
    const base = index([row("latency", "workspace")]);
    expect(withClientBuiltins(base, { projectId: undefined, envId: undefined })).toBe(base);
    const empty = index([]);
    expect(withClientBuiltins(empty, project)).toBe(empty);
    const once = withClientBuiltins(base, project);
    expect(withClientBuiltins(once, project).dashboards.filter((d) => isClientBuiltin(d.ref))).toHaveLength(1);
  });

  it("opens without a DashboardService document", () => {
    expect(isClientBuiltin({ kind: "builtin", projectId: "", key: LLM_USAGE_KEY })).toBe(true);
    expect(isClientBuiltin({ kind: "builtin", projectId: "", key: "org-usage" })).toBe(false);
    expect(clientBuiltinDocument()).toMatchObject({ summary: { viewKind: "llm-usage" }, canWrite: false, spec: { panels: [] } });
  });
});
