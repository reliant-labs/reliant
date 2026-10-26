import { describe, expect, it } from "vitest";

import type { ComputePlanOption } from "@/components/Billing/PlanTiles";
import { cloudMachineRows, defaultMachineName, defaultRow, MACHINE_NAME_RULE } from "../addMachine";

const option = (size: "small" | "medium" | "large", cents: number): ComputePlanOption => ({
  planId: `plan-${size}`,
  label: size[0]!.toUpperCase() + size.slice(1),
  detail: `${size} spec`,
  size,
  monthlyPriceCents: cents,
});
const catalog = [option("small", 1900), option("medium", 3900), option("large", 7900)];

describe("cloudMachineRows", () => {
  it("offers sizes above the plan as upgrades instead of hiding them", () => {
    const rows = cloudMachineRows(catalog, ["small"], true);
    expect(rows.map((r) => [r.size, r.terms])).toEqual([
      ["small", "included"],
      ["medium", "upgrade"],
      ["large", "upgrade"],
    ]);
  });

  it("makes every size a first purchase with no plan", () => {
    expect(cloudMachineRows(catalog, [], false).every((r) => r.terms === "purchase")).toBe(true);
  });

  it("keeps a size the account may run even when the catalog did not price it", () => {
    const rows = cloudMachineRows([], ["small"], false);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ size: "small", terms: "included" });
    expect(rows[0]!.plan).toBeUndefined();
  });
});

describe("defaultRow", () => {
  it("prefers the caller's suggestion, then Small", () => {
    const rows = cloudMachineRows(catalog, ["small", "medium"], true);
    expect(defaultRow(rows, "medium")?.size).toBe("medium");
    expect(defaultRow(rows)?.size).toBe("small");
  });
});

describe("defaultMachineName", () => {
  it("is unique on the account", () => {
    expect(defaultMachineName("small", ["cloud-small"])).toBe("cloud-small-2");
  });

  it("is derived from the repository when there is one", () => {
    expect(defaultMachineName("small", [], "https://github.com/acme/api-server.git")).toBe("api-server");
  });

  it("always satisfies the server's name rule", () => {
    for (const name of [defaultMachineName("2xl", []), defaultMachineName("small", [], "https://github.com/a/My_Repo.git")]) {
      expect(MACHINE_NAME_RULE.test(name)).toBe(true);
    }
  });
});
