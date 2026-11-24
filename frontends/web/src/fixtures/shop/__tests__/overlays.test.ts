// Copyright (c) 2025 Reliant Labs

/**
 * The dev previews' named overlays over the shop pack (../overlays.ts): each
 * composes over the app layer into a transport, names only real rpcs, and
 * turns the one fact it is named for while the rest stays the story's.
 */

import { Code, ConnectError, createClient, type Transport } from "@connectrpc/connect";
import { beforeEach, describe, expect, it } from "vitest";

import { DeployHoldKind, DeployRolloutPhase } from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { AppDatabaseService, DataAccessPolicy } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { ObservabilityService, WorkloadHealth } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { StorageAccessPolicy, StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { ForgeService } from "@/gen/reliant/v1/forge_pb";

import { withOverlay } from "../../preview";
import { createFixtureTransport, fixturePackAnswers, type FixturePack } from "../../transport";
import { shopAppPack } from "../app";
import { SHOP_SERVE } from "../index";
import { browserOverlays, consoleOverlays, deliveryOverlays } from "../overlays";
import { resetDemoNowForTests } from "../shopStory";

const FIXED_NOW = Date.UTC(2026, 10, 18, 15, 30);

const serve = (overlay: FixturePack | readonly FixturePack[] | null): Transport =>
  createFixtureTransport({ ...SHOP_SERVE, pack: withOverlay(shopAppPack({ now: FIXED_NOW }), overlay) });

async function codeOf(call: Promise<unknown>): Promise<Code | "ok"> {
  try {
    await call;
    return "ok";
  } catch (err) {
    return ConnectError.from(err).code;
  }
}

const ALL: [string, FixturePack | readonly FixturePack[]][] = Object.entries({ ...browserOverlays, ...deliveryOverlays, console: consoleOverlays }).flatMap(
  ([area, overlays]) => Object.entries(overlays as Record<string, FixturePack | readonly FixturePack[]>).map(([name, overlay]) => [`${area}.${name}`, overlay] as [string, FixturePack | readonly FixturePack[]])
);

beforeEach(() => resetDemoNowForTests());

describe("the shop overlays", () => {
  it.each(ALL)("%s composes over the app layer, naming real rpcs", (_name, overlay) => {
    expect(() => serve(overlay)).not.toThrow();
    // Every write an overlay lets past the read-only guard is one it answers itself.
    for (const key of fixturePackAnswers(withOverlay(shopAppPack({ now: FIXED_NOW }), overlay))) expect(key).toMatch(/^[a-z0-9_.]+\.[A-Za-z]+Service\/[A-Za-z]+$/);
  });

  it("holds prod's newest deploy on billing, and only prod's", async () => {
    const view = await createClient(DeployService, serve(consoleOverlays.queued)).getLiveView({ project: "shop" });
    const prod = view.environments.find((live) => live.environment?.name === "prod")!;
    expect(prod.phase).toBe(DeployRolloutPhase.HELD);
    expect(prod.environment?.holds.map((hold) => [hold.kind, hold.callerCanResolve])).toEqual([[DeployHoldKind.BILLING, true]]);
    expect(prod.currentPromotion?.releaseVersion).toBe("v1.8.4");
    const member = await createClient(DeployService, serve(consoleOverlays["queued-member"])).getLiveView({ project: "shop" });
    expect(member.environments.find((live) => live.environment?.name === "prod")?.environment?.holds[0]?.callerCanResolve).toBe(false);
    for (const live of view.environments.filter((candidate) => candidate.environment?.name !== "prod")) expect(live.phase).not.toBe(DeployRolloutPhase.HELD);
  });

  it("answers forge's reports from the app layer, and fails them when the daemon is away", async () => {
    const topology = await createClient(ForgeService, serve(null)).getTopology({ projectId: "shop-proj-shop" });
    expect(JSON.parse(topology.reportJson).environments.map((env: { env: string }) => env.env)).toEqual(["dev", "staging", "prod"]);
    expect(await codeOf(createClient(ForgeService, serve(consoleOverlays["daemon-offline"])).getTopology({ projectId: "shop-proj-shop" }))).toBe(Code.Unavailable);
  });

  it("turns one policy to deny, and refuses the reads it guards", async () => {
    const data = createClient(AppDatabaseService, serve(browserOverlays.data.deny));
    const { databases } = await data.listBrowsableDatabases({ environmentId: "env-staging" });
    expect(databases.map((db) => [db.name, db.policy])).toEqual([["shop", DataAccessPolicy.DENY]]);
    expect(await codeOf(data.describeDatabase({ environmentId: "env-staging", database: "shop" }))).toBe(Code.PermissionDenied);
    const storage = await createClient(StorageBrowserService, serve(browserOverlays.storage.deny)).listBrowsableBuckets({ environmentId: "env-staging" });
    expect([storage.policy, storage.buckets.map((bucket) => bucket.name)]).toEqual([StorageAccessPolicy.DENY, ["uploads"]]);
  });

  it("carries forge's error reason beside the code", async () => {
    const storage = createClient(StorageBrowserService, serve(browserOverlays.storage.shape));
    const err = await storage.listBrowsableBuckets({ environmentId: "env-staging" }).catch((e: unknown) => ConnectError.from(e));
    expect(err).toBeInstanceOf(ConnectError);
    expect([(err as ConnectError).code, (err as ConnectError).metadata.get("x-forge-error-reason")]).toEqual([Code.FailedPrecondition, "shape_unknown"]);
  });

  it("crash-loops the worker and leaves the other workloads healthy", async () => {
    const list = await createClient(ObservabilityService, serve(deliveryOverlays.workloads["crash-looping"])).listWorkloads({ scope: { envIds: ["env-prod"] } });
    const health = new Map(list.workloads.map((row) => [row.workload, row.health]));
    expect(health.get("worker")).toBe(WorkloadHealth.CRASH_LOOPING);
    expect(health.get("api")).toBe(WorkloadHealth.HEALTHY);
  });

  it("fails the ledger's reads, and lets a refused write reach the overlay's answer", async () => {
    const deploy = createClient(DeployService, serve(deliveryOverlays.releases.error));
    expect(await codeOf(deploy.listReleases({ project: "shop" }))).toBe(Code.Unavailable);
    // RestartWorkload is a write: the pack refuses it with PermissionDenied and the demo's message; the overlay's own refusal says why.
    const refused = await createClient(DeployService, serve(deliveryOverlays.workloads["action-fails"])).restartWorkload({ environmentId: "env-prod", workload: "api" }).catch((e: unknown) => ConnectError.from(e));
    expect((refused as ConnectError).rawMessage).toBe("the caller may not write deployments in this organization");
  });
});
