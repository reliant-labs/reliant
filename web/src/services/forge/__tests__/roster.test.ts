// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { LiveEnv } from "../live";
import { forgeEnvRoster, lifecycleOf } from "../roster";
import type { ForgeTopologyEnv } from "../topology";

function live(name: string, kind: LiveEnv["kind"]): LiveEnv {
  return {
    id: `cp-${name}`,
    name,
    project: "control-plane",
    kind,
    declaredShape: null,
    declaredBy: null,
    release: "",
    releaseProvenance: null,
    promotedByActor: "",
    promotedByUserId: "",
    phase: "unspecified",
    observed: { state: "not-reported" },
    drift: { state: "not-reported" },
    driftDetail: "",
    provenance: "",
    holds: [],
  };
}

// control-plane's real topology today: forge's file ledger, no backend rows.
const CONTROL_PLANE_TOPOLOGY: ForgeTopologyEnv[] = [
  { env: "dev", declared: true, destination: "mixed" },
  { env: "dev-k8s", declared: true, destination: "cluster" },
  { env: "e2e", declared: true, destination: "cluster", release: "20261004.090833-65c4c4ea711d" },
  { env: "prod", declared: true, destination: "mixed", release: "v1.7.15" },
];

describe("forgeEnvRoster", () => {
  it("lists backend environments first and labels checkout-only ones by source", () => {
    const roster = forgeEnvRoster([live("prod", "self_managed")], CONTROL_PLANE_TOPOLOGY);
    expect(roster.map((env) => [env.name, env.source])).toEqual([
      ["prod", "backend"],
      ["dev", "checkout"],
      ["dev-k8s", "checkout"],
      ["e2e", "checkout"],
    ]);
    expect(roster[0]?.forge?.release).toBe("v1.7.15");
  });

  it("renders the backend list with the daemon asleep — no checkout entries, nothing lost", () => {
    const roster = forgeEnvRoster([live("prod", "persistent"), live("dev", "local")], []);
    expect(roster.map((env) => env.name)).toEqual(["dev", "prod"]);
    expect(roster.every((env) => env.source === "backend")).toBe(true);
  });

  it("keeps a backend env the checkout no longer declares", () => {
    const roster = forgeEnvRoster([live("staging", "persistent")], [
      { env: "staging", declared: false },
    ]);
    expect(roster).toHaveLength(1);
    expect(roster[0]?.forge).toBeNull();
  });

  it("is empty when neither side knows anything", () => {
    expect(forgeEnvRoster([], [])).toEqual([]);
  });
});

describe("lifecycleOf", () => {
  it("trusts the control plane's local kind", () => {
    expect(lifecycleOf(live("dev", "local"), null)).toBe("local");
    expect(lifecycleOf(live("prod", "persistent"), { destination: "host" })).toBe("deployed");
  });

  it("takes forge's runtime word for an env the backend has no row for", () => {
    // control-plane's dev: destination mixed, lifecycle local per forge.
    expect(lifecycleOf(null, { destination: "mixed" }, true)).toBe("local");
  });

  it("does not guess local from a mixed destination — prod is mixed too", () => {
    expect(lifecycleOf(null, { destination: "mixed" })).toBe("deployed");
    expect(lifecycleOf(null, { destination: "compose" })).toBe("local");
    expect(lifecycleOf(null, { destination: "host" })).toBe("local");
  });

  it("reads forge's declared lifecycle off the topology row on first load", () => {
    // No env status read, no backend row: the label comes from the topology alone.
    expect(lifecycleOf(null, { destination: "mixed", lifecycle: "local" })).toBe("local");
    expect(lifecycleOf(null, { destination: "mixed", lifecycle: "" })).toBe("deployed");
  });

  it("treats a non-local declared lifecycle as deployed even on a host destination", () => {
    expect(lifecycleOf(null, { destination: "host", lifecycle: "ephemeral" })).toBe("deployed");
  });

  it("the backend's kind still outranks forge's declaration", () => {
    expect(lifecycleOf(live("prod", "persistent"), { lifecycle: "local" })).toBe("deployed");
  });
});

describe("forgeEnvRoster lifecycle labelling", () => {
  it("files unregistered envs under their declared lifecycle without any status read", () => {
    const roster = forgeEnvRoster([], [
      { env: "dev", declared: true, destination: "mixed", lifecycle: "local" },
      { env: "prod", declared: true, destination: "mixed", lifecycle: "" },
    ]);
    expect(roster.map((env) => [env.name, env.source, env.lifecycle])).toEqual([
      ["dev", "checkout", "local"],
      ["prod", "checkout", "deployed"],
    ]);
  });
});
