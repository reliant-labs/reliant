import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { expect, it } from "vitest";
import { MACHINE_BURST, MACHINE_SPECS } from "../machineSpecs";

// Public consumer contract of CP config.DaemonTierResources. Do not add sizes or
// fractional requests here; migrate to public hardware fields when available.
const guaranteed = { s1: [1, 2], s2: [1, 4], m: [2, 4], l: [4, 8], xl: [8, 16], "2xl": [15, 32], "3xl": [30, 64] } as const;
it("shows the seven Guaranteed guest sizes without fractional/burst promises", () => {
  expect(Object.keys(MACHINE_SPECS)).toEqual(Object.keys(guaranteed));
  for (const [size, [cpu, memory]] of Object.entries(guaranteed)) {
    expect(MACHINE_SPECS[size as keyof typeof guaranteed]).toEqual({ cpuReserved: cpu, cpuBurst: cpu, memoryReservedGb: memory, memoryBurstGb: memory });
  }
  expect(MACHINE_BURST).toBeUndefined();
});
it("matches actual sibling CP canonical requests and limits when available", () => {
  const source = resolve(process.cwd(), "../../..", "control-plane/internal/config/workspace_config.go");
  if (!existsSync(source)) return; // Public consumer CI has no private sibling.
  const text = readFileSync(source, "utf8");
  for (const [size, [cpu, memory]] of Object.entries(guaranteed)) {
    const row = new RegExp(`"${size}":\\s*\\{CPURequest: "([^"]+)", CPULimit: "([^"]+)", MemoryRequest: "([^"]+)", MemoryLimit: "([^"]+)"`).exec(text);
    expect(row?.slice(1), `${size}: CP canonical guest changed; update public hardware contract`).toEqual([String(cpu), String(cpu), `${memory}Gi`, `${memory}Gi`]);
  }
});
