// Copyright (c) 2025 Reliant Labs

/**
 * `forge env status <env> --json` nests the stack under `runtime` (forge
 * v0.1.42+). The fixture below is control-plane's real `dev` answer, trimmed:
 * every reader of ForgeEnvStatusReport looked for services/checks at the top
 * level and so saw an empty dev stack for an environment that was running.
 */

import { describe, expect, it } from "vitest";

import { checksOf, isLocalLifecycle, normalizeEnvStatus } from "../status";
import { devStackRunsHere } from "../environments";

const CONTROL_PLANE_DEV = {
  ok: true,
  exit_code: 0,
  env: "dev",
  bound: false,
  images: [],
  detail: "no release binding — the environment has never been promoted",
  runtime: {
    env: "dev",
    lifecycle: "local",
    destination: "mixed",
    services: [
      { name: "admin-server", kind: "host", url: "http://localhost:8090", port: 8090, listening: true, pid: 57674, owned: true },
      { name: "admin-api", kind: "host", url: "http://localhost:8091", port: 8091, listening: true, pid: 57666, owned: true },
    ],
    checks: [
      { name: "Compose Infra", status: "pass", message: "9/9 services healthy/running" },
      { name: "App Health", status: "pass", message: "admin-server localhost:8090: healthz=ok readyz=ok" },
    ],
  },
};

describe("normalizeEnvStatus", () => {
  it("lifts runtime.services, checks and lifecycle to where the panels read them", () => {
    const report = normalizeEnvStatus(CONTROL_PLANE_DEV);
    expect(report.services?.map((service) => service.name)).toEqual(["admin-server", "admin-api"]);
    expect(checksOf(report)).toHaveLength(2);
    expect(report.destination).toBe("mixed");
    expect(isLocalLifecycle(report)).toBe(true);
    // The dev stack is recognised as running HERE — it was invisible before.
    expect(devStackRunsHere(report)).toBe(true);
    // The release half stays where it was.
    expect(report.env).toBe("dev");
    expect((report as Record<string, unknown>).runtime).toBeUndefined();
  });

  it("leaves an older forge's flat document untouched", () => {
    const flat = { env: "dev", services: [{ name: "api", owned: true }], checks: [] };
    expect(normalizeEnvStatus(flat)).toBe(flat);
  });

  it("lets a top-level key win over the nested copy", () => {
    const report = normalizeEnvStatus({ env: "prod", runtime: { env: "dev", lifecycle: "local" } });
    expect(report.env).toBe("prod");
    expect(report.lifecycle).toBe("local");
  });

  it("does not call a deployed env local", () => {
    expect(isLocalLifecycle(normalizeEnvStatus({ env: "prod", runtime: { destination: "cluster" } }))).toBe(false);
    expect(isLocalLifecycle(null)).toBe(false);
  });
});
