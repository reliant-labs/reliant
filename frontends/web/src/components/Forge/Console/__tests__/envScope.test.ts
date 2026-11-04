// Copyright (c) 2025 Reliant Labs

/** The scope's refinements: remembered per project, default prod (not All), never scoped to nothing. */

import { beforeEach, describe, expect, it } from "vitest";

import type { LiveEnv } from "@/services/forge/live";
import type { RosterEnv } from "@/services/forge/roster";

import { envsInScope, rememberedEnv, rememberEnv, resolveEnvScope, scopeOptions } from "../envScope";

const KNOWN = ["dev", "e2e", "prod"];

describe("resolveEnvScope", () => {
  it("defaults to prod, not All", () => {
    expect(resolveEnvScope(undefined, null, KNOWN)).toBe("prod");
  });

  it("prefers the URL, then the remembered scope", () => {
    expect(resolveEnvScope("e2e", "dev", KNOWN)).toBe("e2e");
    expect(resolveEnvScope(undefined, "dev", KNOWN)).toBe("dev");
    expect(resolveEnvScope("all", "dev", KNOWN)).toBe("all");
  });

  it("lets the candidate stand while the environments are still arriving", () => {
    expect(resolveEnvScope("pr-12", null, null)).toBe("pr-12");
  });

  it("falls back to prod, then All, for an environment that does not exist", () => {
    expect(resolveEnvScope("pr-12", null, KNOWN)).toBe("prod");
    expect(resolveEnvScope(undefined, "gone", ["dev", "staging"])).toBe("all");
  });
});

describe("the remembered scope", () => {
  beforeEach(() => window.localStorage.clear());

  it("is per project", () => {
    rememberEnv("shop", "e2e");
    expect(rememberedEnv("shop")).toBe("e2e");
    expect(rememberedEnv("other")).toBeNull();
  });
});

function env(name: string, overrides: Partial<RosterEnv> = {}, kind: LiveEnv["kind"] = "persistent"): RosterEnv {
  return {
    name,
    source: "backend",
    lifecycle: kind === "local" ? "local" : "deployed",
    live: { id: `id-${name}`, name, kind } as LiveEnv,
    forge: null,
    ...overrides,
  };
}

describe("scopeOptions", () => {
  it("puts prod first, then what Reliant runs, local, declared-only; previews in the menu", () => {
    const { long, previews } = scopeOptions([
      env("dev", {}, "local"),
      env("pr-9", {}, "preview"),
      env("e2e"),
      env("staging-new", { source: "checkout", live: null }),
      env("prod", {}, "self_managed"),
      env("pr-10", {}, "preview"),
    ]);
    expect(long.map((option) => option.name)).toEqual(["prod", "e2e", "dev", "staging-new"]);
    expect(long.find((option) => option.name === "staging-new")?.recorded).toBe(false);
    expect(previews.map((option) => option.name)).toEqual(["pr-10", "pr-9"]);
  });
});

describe("envsInScope", () => {
  it("is one environment, or every one", () => {
    const envs = [{ name: "dev" }, { name: "prod" }];
    expect(envsInScope("prod", envs)).toEqual([{ name: "prod" }]);
    expect(envsInScope("all", envs)).toEqual(envs);
    expect(envsInScope("gone", envs)).toEqual([]);
  });
});
