// Copyright (c) 2026 Reliant Labs

/**
 * fixtures/scope.tsx: which apps read sample data (only the forge tour's
 * sample), the one shared scope, the roster row and the tour flag; and the
 * shop pack it serves, told for the sample (its name, any org, `sample_` ids)
 * while the public demo's pack stays exactly as it was.
 */

import { afterEach, beforeAll, afterAll, describe, expect, it } from "vitest";
import { createClient, createRouterTransport } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";

import { ActivityService } from "@/gen/controlplane/services/activity/v1/activity_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { TestsService } from "@/gen/controlplane/services/tests/v1/tests_pb";
import { useAskAgentMode } from "@/components/Forge/Observability/AskReliant/askAgentMode";
import { useObsReadOnly } from "@/components/Forge/Observability/obsReadOnly";
import { SAMPLE_APP, SAMPLE_APP_LABEL } from "@/components/Forge/Sample/sampleApp";
import { useTransportScope } from "@/lib/transportScope";
import { setTransport } from "@/lib/connect";
import { ProjectService } from "@/gen/reliant/v1/project_pb";
import type { ProjectCatalog } from "@/services/projects/catalog";

import { appScope, AppScopeProvider, resetSampleScopeForTests, SAMPLE_ROW, setSampleTourActive, useSampleTour, withSampleApp } from "../scope";
import { createShopTransport, SHOP_PACK_SERVICES, shopScenario } from "../shop";
import { resetDemoNowForTests } from "../shop/shopStory";

beforeAll(() => resetDemoNowForTests(Date.UTC(2026, 10, 10, 15, 0, 0)));
afterAll(() => resetDemoNowForTests());
afterEach(() => resetSampleScopeForTests());

describe("appScope", () => {
  it("is null for every real app, and one shared, read-only scope for the sample", () => {
    expect(appScope("shop")).toBeNull();
    expect(appScope("shopfront")).toBeNull();
    expect(appScope("")).toBeNull();
    expect(appScope(null)).toBeNull();
    const sample = appScope(SAMPLE_APP);
    expect(sample).not.toBeNull();
    expect(appScope(SAMPLE_APP)).toBe(sample);
    expect(sample!.readOnly).toBe(true);
    expect(sample!.id).toBe("sample");
  });

  it("answers the sample's console from the shop pack, told for the sample", async () => {
    const deploy = createClient(DeployService, appScope(SAMPLE_APP)!.transport);
    const view = await deploy.getLiveView({ project: SAMPLE_APP, orgId: "org-anyones" } as never);
    expect(view.environments.map((env) => [env.environment?.id, env.environment?.project, env.environment?.name])).toEqual([
      ["sample_env-dev", SAMPLE_APP, "dev"],
      ["sample_env-staging", SAMPLE_APP, "staging"],
      ["sample_env-prod", SAMPLE_APP, "prod"],
    ]);
  });

  // Generated hooks follow the scope (scopedClient), the shell's among them: a
  // service the pack does not own is the person's, and stays on the app's transport.
  it("passes every call to a service the shop pack does not own to the app's transport", async () => {
    expect(SHOP_PACK_SERVICES.map((service) => service.typeName)).not.toContain(ProjectService.typeName);
    const seen: string[] = [];
    const live = createRouterTransport((router) =>
      router.service(ProjectService, {
        listProjects: () => {
          seen.push("ListProjects");
          return {};
        },
      } as never)
    );
    const restore = setTransport(live);
    try {
      await createClient(ProjectService, appScope(SAMPLE_APP)!.transport).listProjects({});
    } finally {
      restore();
    }
    expect(seen).toEqual(["ListProjects"]);
  });
});

describe("AppScopeProvider", () => {
  function Probe() {
    const scope = useTransportScope();
    const { readOnly, reason } = useObsReadOnly();
    const { mode, demo } = useAskAgentMode();
    return <output data-testid="probe">{JSON.stringify({ scope: scope?.id ?? null, readOnly, reason: reason ?? null, mode, demo })}</output>;
  }

  it("mounts the sample's scope with read-only controls and Ask in handoff mode", () => {
    render(<AppScopeProvider app={SAMPLE_APP}><Probe /></AppScopeProvider>);
    expect(JSON.parse(screen.getByTestId("probe").textContent!)).toEqual({ scope: "sample", readOnly: true, reason: "Sample app: changes are turned off", mode: "handoff", demo: true });
  });

  it("leaves a real app's console alone", () => {
    render(<AppScopeProvider app="shop"><Probe /></AppScopeProvider>);
    expect(JSON.parse(screen.getByTestId("probe").textContent!)).toEqual({ scope: null, readOnly: false, reason: null, mode: "chat", demo: false });
  });
});

describe("the roster row and the tour flag", () => {
  const catalog: ProjectCatalog = { orgId: "org-1", rows: [], hostedAppsState: "ok" };

  it("adds the sample's row once, labelled, and never changes the catalog it was given", () => {
    const decorated = withSampleApp(catalog);
    expect(decorated.rows).toEqual([SAMPLE_ROW]);
    expect(SAMPLE_ROW).toMatchObject({ kind: "app", key: `app:${SAMPLE_APP}`, name: SAMPLE_APP_LABEL, app: { name: SAMPLE_APP, state: "live" } });
    expect(withSampleApp(decorated)).toBe(decorated);
    expect(catalog.rows).toEqual([]);
  });

  it("clears the sample's cache when the tour ends, and only then", () => {
    const { queryClient } = appScope(SAMPLE_APP)!;
    queryClient.setQueryData(["sample-read"], 1);
    setSampleTourActive(true);
    expect(useSampleTour.getState().active).toBe(true);
    expect(queryClient.getQueryData(["sample-read"])).toBe(1);
    setSampleTourActive(false);
    expect(queryClient.getQueryData(["sample-read"])).toBeUndefined();
  });
});

describe("the shop pack, told for an app", () => {
  it("mints ids for every service it answers", () => {
    const known = new Set(SHOP_PACK_SERVICES.flatMap((service) => service.methods.map((method) => `${service.typeName}/${method.name}`)));
    expect(Object.keys(shopScenario().handlers as object).filter((rpc) => !known.has(rpc))).toEqual([]);
  });

  it("leaves the public demo's pack exactly as it was: shop, its own ids, its own org", async () => {
    const deploy = createClient(DeployService, createShopTransport());
    const view = await deploy.getLiveView({ project: "shop" } as never);
    expect(view.environments.map((env) => [env.environment?.id, env.environment?.project])).toEqual([
      ["env-dev", "shop"],
      ["env-staging", "shop"],
      ["env-prod", "shop"],
    ]);
    expect((await deploy.getLiveView({ project: SAMPLE_APP } as never)).environments).toEqual([]);
    const activity = createClient(ActivityService, createShopTransport());
    expect((await activity.listActivity({ scope: { orgId: "org-someone-else" } })).events).toEqual([]);
  });

  it("tells the sample's story for its name, in any org, every id prefixed, and reads its ids back", async () => {
    const transport = createShopTransport({ app: SAMPLE_APP, org: null, idPrefix: "sample_" });
    const deploy = createClient(DeployService, transport);
    // Another app is still answered empty.
    expect((await deploy.getLiveView({ project: "shop" } as never)).environments).toEqual([]);
    const releases = await deploy.listReleases({ project: SAMPLE_APP, promotedToEnvironmentId: "sample_env-prod" } as never);
    expect(releases.releases.length).toBeGreaterThan(0);
    expect(releases.releases.every((release) => release.id.startsWith("sample_") && release.project === SAMPLE_APP)).toBe(true);
    // An unprefixed id the sample never minted is not the sample's.
    const runs = await createClient(TestsService, transport).listTestRuns({ project: SAMPLE_APP, orgId: "org-anyones" } as never);
    expect(runs.runs.length).toBeGreaterThan(0);
    expect(runs.runs.every((run) => run.runId.startsWith("sample_") && run.project === SAMPLE_APP)).toBe(true);
    const run = await createClient(TestsService, transport).getTestRun({ runId: runs.runs[0].runId } as never);
    expect(run.run?.runId).toBe(runs.runs[0].runId);
    const activity = await createClient(ActivityService, transport).listActivity({ scope: { orgId: "org-anyones", projectId: SAMPLE_APP } });
    expect(activity.events.length).toBeGreaterThan(0);
    expect(activity.events.every((event) => event.project === SAMPLE_APP && event.id.startsWith("sample_"))).toBe(true);
  });
});
