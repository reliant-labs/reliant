// Copyright (c) 2025 Reliant Labs

/**
 * The console's parsers against REAL forge output for a hosted env.
 *
 * The fixtures under ./fixtures/hosted-*.json were produced by forge itself,
 * not hand-written: forge's `TestHostedCLIEndToEnd` flow (real cobra, real KCL
 * render, hosted ledger + hosted provider, only the control plane's HTTP faked)
 * run as `release cut → promote → deploy --dry-run --json → deploy --json →
 * status --json → topology --json`, with each stdout saved verbatim. Only the
 * temp-dir path inside `ledger`/check evidence was replaced by a stable one.
 *
 * So a field rename on either side fails HERE, against the bytes forge really
 * emits — which a hand-written fixture that shares the author's assumptions
 * cannot do.
 */

import { describe, expect, it } from "vitest";

import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

import {
  classifyForgeResponse,
  destinationOf,
  environments,
  hostedWorkloadsOf,
  workloadLastError,
  type ForgeTopologyReport,
} from "../topology";
import { hostedWorkloadsOfStatus, type ForgeEnvStatusReport } from "../status";
import {
  confirmPhrase,
  deployBlockers,
  deployTokenFor,
  isHostedPlan,
  targetContexts,
  type ForgeDeployReport,
} from "../deploy";
import { managedStoreTarget } from "../secretStore";

import topologyJson from "./fixtures/hosted-topology.json?raw";
import statusJson from "./fixtures/hosted-status.json?raw";
import dryRunJson from "./fixtures/hosted-deploy-dry-run.json?raw";
import applyJson from "./fixtures/hosted-deploy.json?raw";

const ENDPOINT = "http://127.0.0.1:56171";
const ENV_ID = "env-hosted-id";

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

function parse<T>(raw: string): T {
  const outcome = classifyForgeResponse<T>(meta(), raw);
  if (outcome.kind !== "report") throw new Error(`fixture classified as ${outcome.kind}`);
  return outcome.report;
}

describe("forge env topology --json (hosted)", () => {
  const env = environments(parse<ForgeTopologyReport>(topologyJson))[0];

  it("reads destination, endpoint and environment_id off the env row", () => {
    expect(destinationOf(env)).toBe("hosted");
    expect(env.endpoint).toBe(ENDPOINT);
    expect(env.environment_id).toBe(ENV_ID);
    expect(env.kube_context ?? "").toBe("");
  });

  it("reads every per-workload field PROVIDER shipped", () => {
    const [api] = hostedWorkloadsOf(env);
    expect(api).toMatchObject({
      name: "api",
      tier: "backend",
      url: "https://api-acme.reliantapps.dev",
      hostname: "api-acme.reliantapps.dev",
      verdict: "converging",
      verdict_reason: "within the stability window",
      observed_state: "ready",
    });
    expect(api.observed_digest).toMatch(/^sha256:/);
    expect(api.desired_digest).toBe(api.observed_digest);
    // omitempty on the forge side: absent means "not drifted, no error".
    expect(api.drifted ?? false).toBe(false);
    expect(workloadLastError(api)).toBe("");
  });

  it("feeds the managed secret store the environment_id — and only on the matching control plane", () => {
    expect(managedStoreTarget(env, ENDPOINT)).toEqual({ kind: "lookup", environmentId: ENV_ID, endpoint: ENDPOINT });
    expect(managedStoreTarget(env, "https://api.reliant.dev")).toEqual({
      kind: "none",
      availability: "other-control-plane",
    });
  });
});

describe("forge env status --json (hosted)", () => {
  const report = parse<ForgeEnvStatusReport>(statusJson);

  it("reads hosted_workloads — not the cluster inventory under `workloads`", () => {
    const rows = hostedWorkloadsOfStatus(report);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ name: "api", url: "https://api-acme.reliantapps.dev", verdict: "converging" });
    // The same document's `workloads` is the (skipped) cluster inventory, a
    // different shape; it must not be what the hosted rows come from.
    expect(Array.isArray(report.workloads)).toBe(false);
  });

  it("carries the same env identity as topology, so secrets resolve from either", () => {
    expect(destinationOf(report)).toBe("hosted");
    expect(report.verdict).toBe("converging");
    expect(managedStoreTarget(report, ENDPOINT)).toEqual({ kind: "lookup", environmentId: ENV_ID, endpoint: ENDPOINT });
  });
});

describe("forge env deploy --json (hosted)", () => {
  const plan = parse<ForgeDeployReport>(dryRunJson);

  it("is a hosted plan that names no kube context and has no blockers", () => {
    expect(isHostedPlan(plan)).toBe(true);
    expect(plan.guard?.reason).toBe("control_plane_declared");
    expect(targetContexts(plan)).toEqual([]);
    expect(deployBlockers(plan)).toEqual([]);
  });

  it("derives a token whose staleness identity is the endpoint (guard.declared_context)", () => {
    const token = deployTokenFor(plan);
    expect(token).toEqual({
      expectedDeclaredContext: ENDPOINT,
      expectedCurrentRelease: "v1",
      hosted: { environmentId: ENV_ID },
    });
    expect(confirmPhrase(token!)).toBe("127.0.0.1:56171");
  });

  it("never derives a token from the APPLY report", () => {
    expect(deployTokenFor(parse<ForgeDeployReport>(applyJson))).toBeNull();
  });
});
