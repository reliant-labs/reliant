// @vitest-environment node
// Copyright (c) 2025 Reliant Labs
// Public client contract only. Authorization/database isolation is executed by
// TestConsumerProductUsageHostedTenantIsolation in the private hosted executor;
// this transport seam is deliberately not claimed as a server-security proof.
import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { beforeEach, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { QueryPanelResponseSchema } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { panelFromJson } from "../dashboardsApi";
import { queryPanel } from "../api";

const client = vi.hoisted(() => ({ queryPanel: vi.fn() }));
// Stop at the hosted client boundary; scheduler concurrency is covered in its
// own tests and is not an authorization seam.
vi.mock("../obsClient", () => ({ obsClient: () => client }));
const raw = readFileSync("../../deploy/observability/spec/project_gen.json");
const spec = JSON.parse(raw.toString()) as { dashboards: { id: string; panels: unknown[] }[] };
const usage = { panels: spec.dashboards.find(dashboard => dashboard.id === "usage")!.panels.map(panelFromJson) };
const window = { startMs: Date.UTC(2026, 2, 16, 0), endMs: Date.UTC(2026, 2, 16, 12) };
beforeEach(() => client.queryPanel.mockReset());

it("loads the actual generated deployment dashboard and sends every unchanged panel through the hosted adapter", async () => {
  expect(createHash("sha256").update(raw).digest("hex")).toBe("15000552a4f7b5fe6e8616a2076b98c8116709a691ecd068c97a4ba93a9eb22f");
  expect(usage.panels).toHaveLength(17);
  client.queryPanel.mockResolvedValue(create(QueryPanelResponseSchema, { kind: "sql", columns: ["events"], rows: [{ group: { event: "synthetic" }, values: [2] }] }));
  for (const panel of usage.panels) {
    const result = await queryPanel({ orgId: "org-a", projectId: "shop", envIds: ["env-a-prod"], filters: [], window, services: ["api"], panel, stepSeconds: 86400 });
    expect(client.queryPanel.mock.lastCall![0]).toMatchObject({ scope: { orgId: "org-a", projectId: "shop", envIds: ["env-a-prod"], services: ["api"] }, panel: { id: panel.id, kind: "sql", sql: panel.sql } });
    expect(result.rows).toEqual([{ group: { event: "synthetic" }, values: [2] }]);
  }
});

it("keeps a restricted-empty project explicit and maps an authorized empty response without inventing rows", async () => {
  client.queryPanel.mockResolvedValue(create(QueryPanelResponseSchema, { kind: "sql" }));
  const result = await queryPanel({ orgId: "org-a", projectId: "no-live-environments", envIds: [], filters: [], window, services: [], panel: usage.panels[0], stepSeconds: 86400 });
  expect(result.rows).toEqual([]);
  expect(client.queryPanel).toHaveBeenCalledTimes(1);
  expect(client.queryPanel.mock.lastCall![0].scope).toMatchObject({ orgId: "org-a", projectId: "no-live-environments", envIds: [] });
});
