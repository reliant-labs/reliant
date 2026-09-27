// Copyright (c) 2025 Reliant Labs

/**
 * The join of forge's environments (daemon) and the control plane's, and the
 * facts each row derives from whichever sources answered.
 *
 * The property everything here protects: a missing daemon never blanks an
 * environment the control plane can describe, and nothing is invented to
 * fill the gap it leaves.
 */

import { describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
  hasControlPlane: true,
}));

import type { CloudEnv, CloudEnvStatus } from "../cloudEnvs";
import {
  cloudRunIdOf,
  daemonSideOf,
  devStackRunsHere,
  envFacts,
  joinEnvironments,
  managedTargetFor,
  offersShipping,
  resolveForgeProjectName,
  whereOf,
} from "../environments";
import type { ForgeOutcome, ForgeTopologyEnv, ForgeTopologyReport } from "../topology";
import { ForgeReachability, type ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

function meta(): ForgeReportMeta {
  return {
    isForgeProject: true,
    supported: true,
    forgeVersion: "v0.1.18",
    unsupportedReason: "",
    exitCode: 0,
    reachability: ForgeReachability.UNSPECIFIED,
    unreachableReason: "",
  } as ForgeReportMeta;
}

const cloud = (name: string, kind: CloudEnv["kind"], id = `env-${name}`): CloudEnv => ({
  id,
  name,
  project: "barksocial",
  kind,
});

const DEV_LOCAL: ForgeTopologyEnv = { env: "dev", declared: true, bound: false, destination: "host" };
const PROD_HOSTED: ForgeTopologyEnv = {
  env: "prod",
  declared: true,
  bound: true,
  release: "v3",
  destination: "hosted",
  endpoint: "http://127.0.0.1:8090",
  environment_id: "env-prod",
};

describe("joinEnvironments", () => {
  it("lists a control-plane env the daemon never reported — the daemon-asleep case", () => {
    const envs = joinEnvironments([], [cloud("prod", "persistent")]);
    expect(envs.map((e) => [e.name, e.where])).toEqual([["prod", "cloud"]]);
    expect(envs[0].forge).toBeNull();
  });

  it("joins the two sides by name, forge's declared order first", () => {
    const envs = joinEnvironments(
      [PROD_HOSTED, DEV_LOCAL],
      [cloud("dev", "local"), cloud("prod", "persistent"), cloud("preview-7", "preview")]
    );
    expect(envs.map((e) => e.name)).toEqual(["prod", "dev", "preview-7"]);
    expect(envs[0].forge).toBe(PROD_HOSTED);
    expect(envs[0].cloud?.id).toBe("env-prod");
    expect(envs[1].cloud?.kind).toBe("local");
  });
});

describe("whereOf", () => {
  it("lets the control plane's immutable kind win over a re-derived destination", () => {
    // A LOCAL env declares control_plane, so an older forge still calls it hosted.
    expect(whereOf({ destination: "hosted" }, { kind: "local" })).toBe("local");
    expect(whereOf({ destination: "host" }, { kind: "persistent" })).toBe("cloud");
  });

  it("never turns an unknown destination into a cluster", () => {
    expect(whereOf({ destination: "moon-base" }, null)).toBe("unknown");
    expect(whereOf(null, null)).toBe("unknown");
    expect(whereOf(null, { kind: "unknown" })).toBe("unknown");
  });
});

describe("envFacts", () => {
  const status: CloudEnvStatus = {
    verdict: "converged",
    workloads: [{ name: "api", verdict: "converged", url: "https://api.example" }],
    currentPromotion: {
      id: "p1",
      releaseVersion: "v4",
      kind: "rollback",
      fromEnvironmentId: "",
      promotedByUserId: "u",
      promotedByActor: "",
      note: "",
      createdAt: "2026-09-26T10:00:00.000Z",
      artifacts: [],
    },
  };

  it("reads a cloud env's release and health from the control plane — not from forge", () => {
    const [summary] = joinEnvironments([PROD_HOSTED], [cloud("prod", "persistent")]);
    const facts = envFacts(summary, status);
    expect(facts.release).toBe("v4"); // forge said v3; the control plane's ledger wins
    expect(facts.rolledBack).toBe(true);
    expect(facts.health).toEqual({ kind: "hosted", verdict: "converged", loading: false });
    expect(facts.workloads).toHaveLength(1);
  });

  it("renders a cloud env with NO daemon at all", () => {
    const [summary] = joinEnvironments([], [cloud("prod", "persistent")]);
    const facts = envFacts(summary, status);
    expect(facts.release).toBe("v4");
    expect(facts.health.kind).toBe("hosted");
  });

  it("does not claim a verdict while the control plane has not answered", () => {
    const [summary] = joinEnvironments([], [cloud("prod", "persistent")]);
    expect(envFacts(summary, undefined, true).health).toEqual({
      kind: "hosted",
      verdict: "unknown",
      loading: true,
    });
  });

  it("gives a LOCAL env no release and no health — it runs the working tree", () => {
    const [summary] = joinEnvironments([DEV_LOCAL], [cloud("dev", "local")]);
    const facts = envFacts(summary, undefined);
    expect(facts.binding).toBe("local");
    expect(facts.release).toBeNull();
    expect(facts.health).toEqual({ kind: "none" });
  });

  it("says a hosted env forge never ensured is not deployed", () => {
    const [summary] = joinEnvironments([{ ...PROD_HOSTED, environment_id: "" }], []);
    expect(envFacts(summary, undefined).health).toEqual({ kind: "not-deployed" });
  });

  it("keeps a cluster binding's images unverified by default — never green", () => {
    const [summary] = joinEnvironments(
      [
        {
          env: "prod",
          declared: true,
          bound: true,
          release: "v1",
          destination: "cluster",
          images: [{ image: "api", digest: "sha256:a", state: "not_verified" }],
        },
      ],
      []
    );
    expect(envFacts(summary, undefined).health).toEqual({
      kind: "images",
      tally: { "known-good": 0, "known-bad": 0, unknown: 1 },
    });
  });
});

describe("cloudRunIdOf", () => {
  it("uses the control plane's own row first", () => {
    const [summary] = joinEnvironments([PROD_HOSTED], [cloud("prod", "persistent", "cp-id")]);
    expect(cloudRunIdOf(summary)).toBe("cp-id");
  });

  it("falls back to the id forge reported for a hosted env on THIS control plane", () => {
    // The project-scoped list can miss an env ensured before projects existed.
    const [summary] = joinEnvironments([PROD_HOSTED], []);
    expect(cloudRunIdOf(summary)).toBe("env-prod");
  });

  it("never reads a LOCAL env's status — the platform runs nothing there", () => {
    const [summary] = joinEnvironments([DEV_LOCAL], [cloud("dev", "local")]);
    expect(cloudRunIdOf(summary)).toBeNull();
  });

  it("never sends another control plane's id here", () => {
    const [summary] = joinEnvironments([{ ...PROD_HOSTED, endpoint: "https://api.elsewhere.io" }], []);
    expect(cloudRunIdOf(summary)).toBeNull();
  });
});

describe("offersShipping", () => {
  it("offers no promote/deploy for a local env — the control plane refuses both", () => {
    const [dev] = joinEnvironments([DEV_LOCAL], [cloud("dev", "local")]);
    const [prod] = joinEnvironments([PROD_HOSTED], [cloud("prod", "persistent")]);
    expect(offersShipping(dev)).toBe(false);
    expect(offersShipping(prod)).toBe(true);
  });
});

describe("managedTargetFor", () => {
  it("keys a LOCAL env's secrets on the control plane's id — no daemon, no forge report", () => {
    const [summary] = joinEnvironments([], [cloud("dev", "local", "cp-dev")]);
    expect(managedTargetFor(summary)).toMatchObject({ kind: "lookup", environmentId: "cp-dev" });
  });

  it("says a non-hosted env with no control-plane row is not in the managed store", () => {
    const [summary] = joinEnvironments([{ env: "staging", declared: true, destination: "cluster" }], []);
    expect(managedTargetFor(summary)).toEqual({ kind: "none", availability: "not-hosted" });
  });
});

describe("devStackRunsHere", () => {
  it("is true only when forge owns a running host service for the env", () => {
    // control-plane dev, as `forge env status dev --json` reported it.
    expect(
      devStackRunsHere({
        services: [
          { name: "admin-server", kind: "host", listening: true, owned: true },
          { name: "reliant-web", kind: "frontend", listening: true, owned: true },
        ],
      })
    ).toBe(true);
    // control-plane prod: the same array lists what env up WOULD launch — nothing owned.
    expect(
      devStackRunsHere({
        services: [
          { name: "reliant-web", kind: "frontend", listening: false },
          { name: "internal-console", kind: "frontend", listening: true },
        ],
      })
    ).toBe(false);
    expect(devStackRunsHere(null)).toBe(false);
  });
});

describe("daemonSideOf", () => {
  it("is offline only on a transport failure, never on forge's own answers", () => {
    expect(daemonSideOf(undefined, new Error("no daemon connected"))).toBe("offline");
    expect(daemonSideOf(undefined, null)).toBe("loading");
    expect(daemonSideOf({ kind: "not-forge-project", meta: meta() }, null)).toBe("not-forge-project");
    expect(
      daemonSideOf({ kind: "report", meta: meta(), report: {} } as ForgeOutcome<ForgeTopologyReport>, null)
    ).toBe("ok");
  });
});

describe("the forge project name — the join key", () => {
  const report = (project: string): ForgeOutcome<ForgeTopologyReport> => ({
    kind: "report",
    meta: meta(),
    report: { project },
  });

  it("comes from forge when the daemon answers, even over a stale persisted name", () => {
    expect(resolveForgeProjectName("hounders", report("barksocial"))).toEqual({
      name: "barksocial",
      source: "daemon",
    });
  });

  it("comes from the project row when the daemon is offline", () => {
    expect(resolveForgeProjectName("hounders", undefined)).toEqual({ name: "hounders", source: "project" });
    // A daemon that answered without naming the project does not erase the row's name.
    expect(resolveForgeProjectName("hounders", { kind: "not-forge-project", meta: meta() })).toEqual({
      name: "hounders",
      source: "project",
    });
  });

  it("guesses nothing for a project no daemon has ever named", () => {
    expect(resolveForgeProjectName(undefined, undefined)).toEqual({ name: null, source: null });
    expect(resolveForgeProjectName("  ", undefined)).toEqual({ name: null, source: null });
  });
});
