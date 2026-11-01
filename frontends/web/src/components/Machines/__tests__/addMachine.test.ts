import { describe, expect, it } from "vitest";

import type { ComputePlanOption } from "@/components/Billing/PlanTiles";
import { cloudMachineRows, defaultMachineName, defaultRow, MACHINE_NAME_RULE } from "../addMachine";

const option = (size: "m" | "l" | "xl", cents: number): ComputePlanOption => ({
  planId: `plan-${size}`,
  label: size[0]!.toUpperCase() + size.slice(1),
  detail: `${size} spec`,
  size,
  monthlyPriceCents: cents,
});
const catalog = [option("m", 1900), option("l", 3900), option("xl", 7900)];

describe("cloudMachineRows", () => {
  it("offers sizes above the plan as upgrades instead of hiding them", () => {
    const rows = cloudMachineRows(catalog, ["m"], true);
    expect(rows.map((r) => [r.size, r.terms])).toEqual([
      ["m", "included"],
      ["l", "upgrade"],
      ["xl", "upgrade"],
    ]);
  });

  it("makes every size a first purchase with no plan", () => {
    expect(cloudMachineRows(catalog, [], false).every((r) => r.terms === "purchase")).toBe(true);
  });

  it("keeps a size the account may run even when the catalog did not price it", () => {
    const rows = cloudMachineRows([], ["m"], false);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ size: "m", terms: "included" });
    expect(rows[0]!.plan).toBeUndefined();
  });
});

describe("defaultRow", () => {
  it("prefers the caller's suggestion, then Small", () => {
    const rows = cloudMachineRows(catalog, ["m", "l"], true);
    expect(defaultRow(rows, "l")?.size).toBe("l");
    expect(defaultRow(rows)?.size).toBe("m");
  });
});

describe("defaultMachineName", () => {
  it("is unique on the account", () => {
    expect(defaultMachineName("m", ["cloud-m"])).toBe("cloud-m-2");
  });

  it("is derived from the repository when there is one", () => {
    expect(defaultMachineName("m", [], "https://github.com/acme/api-server.git")).toBe("api-server");
  });

  it("always satisfies the server's name rule", () => {
    for (const name of [defaultMachineName("3xl", []), defaultMachineName("m", [], "https://github.com/a/My_Repo.git")]) {
      expect(MACHINE_NAME_RULE.test(name)).toBe(true);
    }
  });
});
