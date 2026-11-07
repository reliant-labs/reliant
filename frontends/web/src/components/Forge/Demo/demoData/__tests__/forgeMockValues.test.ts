// Copyright (c) 2025 Reliant Labs

/**
 * The vendored mock engine (forgeMockValues/, forge#1874 + forge#1878) on the
 * console's own contracts, plus a real compiled descriptor fixture for
 * response constraints and requested oneof cases. No upstream test assumed:
 *
 *  - every string a console message's pattern constrains gets a value that
 *    matches it (a value that did not would be one control-plane rejects);
 *  - a value the request carries is echoed only when the response field's
 *    rules accept it, and a requested oneof case is kept.
 */

import { describe, expect, it, beforeAll, afterAll } from "vitest";
import { execFileSync } from "node:child_process";
import { mkdtempSync, cpSync, readFileSync, rmSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { create, createFileRegistry, fromBinary, type DescService } from "@bufbuild/protobuf";
import { FileDescriptorSetSchema } from "@bufbuild/protobuf/wkt";

import { CreateReceiverRequestSchema, MatcherSchema } from "@/gen/controlplane/services/alerting/v1/alerting_pb";
import { SaveAsCopyRequestSchema, SaveDashboardRequestSchema } from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";

import { mockValue, serviceDefaults } from "../forgeMockValues/mock-values";

const NOW = Date.UTC(2026, 10, 6, 12, 0, 0);

/** Runtime descriptors from the proto compiler, including real encoded buf.validate options. */
describe("serviceDefaults descriptor regressions (R4.B1)", () => {
  let scratch: string;
  let service: DescService;
  beforeAll(() => {
    scratch = mkdtempSync(path.join(tmpdir(), "demo-mock-descriptors-"));
    mkdirSync(path.join(scratch, "buf/validate"), { recursive: true });
    cpSync(path.resolve("../../proto-vendor/controlplane/buf/validate/validate.proto"), path.join(scratch, "buf/validate/validate.proto"));
    cpSync(path.resolve("src/components/Forge/Demo/demoData/__tests__/fixtures/mock_echo.proto"), path.join(scratch, "mock_echo.proto"));
    writeFileSync(path.join(scratch, "buf.yaml"), "version: v2\nmodules:\n  - path: .\n");
    execFileSync("buf", ["build", "--as-file-descriptor-set", "--exclude-source-info", "-o", "descriptor.binpb"], { cwd: scratch, timeout: 30_000 });
    const registry = createFileRegistry(fromBinary(FileDescriptorSetSchema, readFileSync(path.join(scratch, "descriptor.binpb"))));
    service = registry.getService("demo.mock.echo.EchoService")!;
    expect(service.kind).toBe("service");
  });
  afterAll(() => { if (scratch) rmSync(scratch, { recursive: true, force: true }); });

  type Result = { fixed: string; allowed: string; count: number; selection: { case: "alpha" | "beta"; value: string } };
  const read = (request: Record<string, unknown>) => serviceDefaults(service, { now: NOW }).read(create(service.methods[0].input, request)) as unknown as Result;

  it("rejects an echoed string incompatible with the response const", () => {
    expect(read({ fixed: "not-required" }).fixed).toBe("required");
  });
  it("rejects an echoed string outside the response in set", () => {
    expect(["red", "blue"]).toContain(read({ allowed: "green" }).allowed);
  });
  it.each([-100, 21, 500])("rejects echoed %i outside the response numeric range", (count) => {
    const response = read({ count });
    expect(response.count).toBeGreaterThanOrEqual(10);
    expect(response.count).toBeLessThanOrEqual(20);
  });
  it("still echoes compatible constrained scalars", () => {
    const response = read({ fixed: "required", allowed: "blue", count: 17 });
    expect(response).toMatchObject({ fixed: "required", allowed: "blue", count: 17 });
  });
  it.each(["alpha", "beta"] as const)("preserves the request-selected %s oneof case and value across request seeds", (selected) => {
    for (let i = 0; i < 12; i++) {
      const value = `chosen-${selected}-${i}`;
      const response = read({ selection: { case: selected, value }, count: i });
      expect.soft(response.selection.case).toBe(selected);
      expect.soft(response.selection.value).toBe(value);
    }
  });
});

describe("the vendored mock engine on the console's contracts", () => {
  it("meets every pattern a console message declares", () => {
    for (let i = 0; i < 5; i++) {
      const seed = `seed-${i}`;
      expect(mockValue(MatcherSchema, { now: NOW, seed }).name).toMatch(/^[a-zA-Z_][a-zA-Z0-9_]*$/);
      expect(mockValue(CreateReceiverRequestSchema, { now: NOW, seed }).name).toMatch(/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$/);
      expect(mockValue(SaveDashboardRequestSchema, { now: NOW, seed }).slug).toMatch(/^[a-z0-9][a-z0-9-]{0,62}$/);
      expect(mockValue(SaveAsCopyRequestSchema, { now: NOW, seed }).slug).toMatch(/^$|^[a-z0-9][a-z0-9-]{0,62}$/);
    }
  });

  it("is deterministic: the same seed, the same message", () => {
    expect(mockValue(MatcherSchema, { now: NOW, seed: "s" })).toEqual(mockValue(MatcherSchema, { now: NOW, seed: "s" }));
  });
});
