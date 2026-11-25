// Copyright (c) 2026 Reliant Labs

import { describe, expect, it, vi } from "vitest";

import type { Workload } from "@/services/controlPlane/observability/types";
import type { ForgeHostedWorkload } from "@/services/forge/topology";

import { ENV_ALL } from "../../Console/envScope";
import { databaseEntries } from "../DatabaseHome";

// DatabaseHome draws its CPU and memory lines with UPlotChart; uPlot needs a canvas jsdom lacks.
vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());

const hosted = new Map<string, Map<string, ForgeHostedWorkload>>([
  ["staging", new Map([["db", { name: "db", tier: "database", verdict: "converging" } as ForgeHostedWorkload]])],
  [
    "prod",
    new Map([
      ["db", { name: "db", tier: "database", verdict: "converged" } as ForgeHostedWorkload],
      ["api", { name: "api", tier: "backend", verdict: "converged" } as ForgeHostedWorkload],
    ]),
  ],
]);
const envs = [
  { id: "env-prod", name: "prod" },
  { id: "env-staging", name: "staging" },
];
const instances = { workload: "db", envName: "prod", containers: [] } as unknown as Workload;

describe("databaseEntries (Data's database home)", () => {
  it("lists every environment's managed database, prod first, and says the deploy's word", () => {
    const entries = databaseEntries(hosted, [], envs, ENV_ALL);
    expect(entries.map((e) => `${e.envName}/${e.name}`)).toEqual(["prod/db", "staging/db"]);
    expect(entries[0]).toMatchObject({ envId: "env-prod", verdict: { label: "Provisioned" }, instances: null });
    expect(entries[1]!.verdict.label).toBe("Deploying");
  });

  it("keeps the scoped environment's only, and finds its instances by the database's name", () => {
    const entries = databaseEntries(hosted, [instances], envs, "prod");
    expect(entries).toHaveLength(1);
    expect(entries[0]!.instances).toBe(instances);
    expect(databaseEntries(hosted, [instances], envs, "staging")[0]!.instances).toBeNull();
  });
});
