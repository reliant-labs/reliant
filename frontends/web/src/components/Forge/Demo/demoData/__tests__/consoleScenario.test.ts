// Copyright (c) 2025 Reliant Labs

/**
 * The sample app's data for every console tab the demo shows, read the way
 * the console reads it: through connect.ts's transport (the demo's mock
 * transport, installShellTransport) and the console's own service mappers.
 *
 *  - Environments (the roster, each env's status, shape and promotions),
 *    Releases (list, one release, a rollout), Secrets, Domains and Activity
 *    each come back with believable rows for shop's dev/staging/prod.
 *  - Secrets are names only: nothing that looks like a value is on the wire.
 *  - The story matches the observability fixtures: prod runs v1.8.3, the
 *    release the CreateOrder issue first appeared in.
 *  - Nothing outside shop: a read naming another project or environment gets an
 *    empty answer, from the story's handlers and from the generated defaults.
 *  - Every read of every console service is answered (the story, or forge's
 *    generated defaults), and every write is refused before any handler.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { create, equals, type DescMessage, type DescService } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient } from "@connectrpc/connect";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { SecretStoreService } from "@/gen/controlplane/services/secret_store/v1/secret_store_pb";
import { installShellTransport, setTransport } from "@/lib/connect";
import { listActivity } from "@/services/controlPlane/observability/activityApi";
import { getEnvironmentStatus, listEnvironmentPromotions, listProjectEnvironments } from "@/services/forge/cloudEnvs";
import { listDomains } from "@/services/forge/domains";
import { getEnvironmentShape, getLiveView } from "@/services/forge/live";
import { getProjectRelease, getPromotionRollout, listProjectReleases } from "@/services/forge/releases";
import { listSecrets } from "@/services/forge/secretStore";

import { DEMO_DOMAINS, DEMO_ENVS, DEMO_PROJECT, DEMO_RELEASES, DEMO_SECRETS, DEMO_WORKLOADS, consoleScenarioHandlers } from "../consoleScenario";
import { DEMO_DEFAULT_SERVICES } from "../demoServiceDefaults";
import { outsideDemo } from "../demoScope";
import { createDemoTransport, isDemoRead } from "../demoTransport";
import { FIXTURE_ENVS, FIXTURE_PROJECT } from "../../../Observability/__fixtures__/fixtureApi";

beforeEach(() => {
  setTransport(null);
  installShellTransport(createDemoTransport({ source: "fixtures", url: "" }));
});

afterEach(() => {
  installShellTransport(null as never);
});

describe("the console's sample data, on the demo's transport", () => {
  it("is the observability fixtures' app: same project, same environments", () => {
    expect(DEMO_PROJECT).toBe(FIXTURE_PROJECT);
    expect(DEMO_ENVS.map((e) => [e.id, e.name])).toEqual(FIXTURE_ENVS.map((e) => [e.id, e.name]));
  });

  it("Environments: three deployed envs, prod and staging on v1.8.3, dev on the fix", async () => {
    const live = await getLiveView(DEMO_PROJECT);
    expect(live.map((e) => e.name)).toEqual(["dev", "prod", "staging"]);
    expect(Object.fromEntries(live.map((e) => [e.name, e.release]))).toEqual({ dev: "v1.8.4", staging: "v1.8.3", prod: "v1.8.3" });
    expect(live.every((e) => e.declared && e.phase === "succeeded")).toBe(true);
    expect(await getLiveView("someone-else")).toEqual([]);

    const envs = await listProjectEnvironments(DEMO_PROJECT);
    expect(envs.map((e) => e.name)).toEqual(["dev", "prod", "staging"]);

    const status = await getEnvironmentStatus("env-prod");
    expect(status.verdict).toBe("converged");
    expect(status.workloads.map((w) => w.name)).toEqual(DEMO_WORKLOADS.map((w) => w.name));
    expect(status.workloads.every((w) => w.observed_state === "ready")).toBe(true);

    const shape = await getEnvironmentShape("env-prod");
    expect(shape?.workloads.map((w) => w.name)).toEqual(DEMO_WORKLOADS.map((w) => w.name));
    expect(shape?.domains).toContain("shop.example.com");
    expect(shape?.alerts?.length ?? 0).toBeGreaterThan(0);

    const promotions = await listEnvironmentPromotions("env-prod", 10);
    expect(promotions.length).toBeGreaterThanOrEqual(3);
  });

  it("Releases: newest first with commits, one release, and its rollout", async () => {
    const releases = await listProjectReleases(DEMO_PROJECT);
    expect(releases.map((r) => r.version)).toEqual(DEMO_RELEASES.map((r) => r.version));
    expect(releases[0]).toMatchObject({ subject: DEMO_RELEASES[0].subject, author: "maya", branch: "main" });

    const one = await getProjectRelease(DEMO_PROJECT, "v1.8.3");
    expect(one?.release.version).toBe("v1.8.3");
    expect([...(one?.currentEnvironmentIds ?? [])].sort()).toEqual(["env-prod", "env-staging"]);

    const rollout = await getPromotionRollout("env-prod", "promo-prod-v183");
    expect(rollout?.workloads.length).toBe(DEMO_WORKLOADS.length);
  });

  it("Secrets: names per environment, never a value", async () => {
    const prod = await listSecrets("env-prod");
    expect(prod.secrets.map((s) => s.name)).toEqual([...DEMO_SECRETS.map((s) => s.name)].sort());
    expect(prod.protected).toBe(true);
    // dev is missing the webhook secret and the mail key: the matrix shows the gap.
    const dev = await listSecrets("env-dev");
    expect(dev.secrets.map((s) => s.name)).not.toContain("PAYMENTS_WEBHOOK_SECRET");

    const client = createClient(SecretStoreService, (await import("@/lib/connect")).transport);
    const wire = JSON.stringify(await client.getSecretVersions({ environmentId: "env-prod", name: "DATABASE_URL" }), (_k, v) => (typeof v === "bigint" ? String(v) : v));
    expect(wire).not.toMatch(/value|postgres:|sk_|password/i);
    const listed = JSON.stringify(await client.listSecrets({ environmentId: "env-prod" }), (_k, v) => (typeof v === "bigint" ? String(v) : v));
    expect(listed).not.toMatch(/value|postgres:|sk_|password/i);
    expect(Object.keys(consoleScenarioHandlers())).toContain("controlplane.v1.SecretStoreService/ListSecrets");
  });

  it("Domains: live on prod, one still verifying on staging", async () => {
    const domains = await listDomains();
    expect(domains.map((d) => d.hostname)).toEqual(DEMO_DOMAINS.map((d) => d.hostname));
    expect(domains.find((d) => d.hostname === "shop.example.com")?.state).toBe("live");
    expect(domains.find((d) => d.hostname === "staging.shop.example.com")?.state).toBe("verifying");
  });

  it("Activity: promotions, a release cut, a triage and a rotation, newest first", async () => {
    const { events } = await listActivity({ orgId: "org-demo", projectId: DEMO_PROJECT, envIds: [] });
    expect(events.map((e) => e.verb)).toEqual(["promote", "cut", "assign", "promote", "promote", "rotate"]);
    expect(events.every((e, i) => i === 0 || events[i - 1].atMs >= e.atMs)).toBe(true);
  });

  it("refuses a write before any handler sees it", async () => {
    const client = createClient(DeployService, (await import("@/lib/connect")).transport);
    await expect(client.promote({ environmentId: "env-prod" } as never)).rejects.toSatisfy(
      (err: unknown) => ConnectError.from(err).code === Code.PermissionDenied,
    );
  });
});

describe("every console read is answered; every write is refused", () => {
  /** What the console's tabs read on first render (the bundle's smoke over / and its 12 tabs). */
  const FIRST_RENDER = [
    "DeployService/GetLiveView",
    "DeployService/ListEnvironments",
    "DeployService/GetEnvironmentShape",
    "DeployService/ListConvergences",
    "DeployService/GetStatus",
    "DeployService/ListReleases",
    "DeployService/GetRelease",
    "DeployService/CompareReleases",
    "DeployService/ListPromotions",
    "DeployService/GetRollout",
    "DeployService/ListDeployAttempts",
    "SecretStoreService/ListSecrets",
    "SecretStoreService/GetSecretVersions",
    "DomainService/ListDomains",
    "DomainService/ListDomainBindings",
    "ActivityService/ListActivity",
    "ObservabilityService/ListWorkloads",
    "ObservabilityService/GetServiceOverview",
    "IssuesService/ListIssues",
    "AlertingService/ListAlerts",
    "DashboardService/ListDashboards",
  ];

  const methodsOf = (service: DescService) => service.methods.filter((m) => m.methodKind === "unary");

  it("names real RPCs, each one a read", () => {
    for (const key of FIRST_RENDER) {
      const [svc, name] = key.split("/");
      const service = DEMO_DEFAULT_SERVICES.find((s) => s.name === svc);
      const method = service?.methods.find((m) => m.name === name);
      expect(method, key).toBeDefined();
      expect(isDemoRead(method!), key).toBe(true);
    }
  });

  it("answers every read of every console service, story or default, with no Unimplemented", async () => {
    const t = (await import("@/lib/connect")).transport;
    let reads = 0;
    for (const service of DEMO_DEFAULT_SERVICES) {
      const client = createClient(service, t) as unknown as Record<string, (req: object) => Promise<unknown>>;
      for (const method of methodsOf(service).filter((m) => isDemoRead(m))) {
        await expect(client[method.localName]({}), `${service.typeName}/${method.name}`).resolves.toBeDefined();
        reads++;
      }
    }
    expect(reads).toBeGreaterThan(70);
  });

  it("refuses every write of every console service, defaults or not", async () => {
    const t = (await import("@/lib/connect")).transport;
    let writes = 0;
    for (const service of DEMO_DEFAULT_SERVICES) {
      const client = createClient(service, t) as unknown as Record<string, (req: object) => Promise<unknown>>;
      for (const method of methodsOf(service).filter((m) => !isDemoRead(m))) {
        await expect(client[method.localName]({}), `${service.typeName}/${method.name}`).rejects.toSatisfy(
          (err: unknown) => ConnectError.from(err).code === Code.PermissionDenied,
        );
        writes++;
      }
    }
    expect(writes).toBeGreaterThan(20);
  });
});

describe("nothing outside shop", () => {
  const client = async <S extends DescService>(service: S) => createClient(service, (await import("@/lib/connect")).transport);
  const OTHER_ENV = "env-other-prod";

  it("the story's environment handlers answer nothing for another project's environment", async () => {
    const deploy = await client(DeployService);
    expect((await deploy.getEnvironment({ environmentId: OTHER_ENV })).environment).toBeUndefined();
    expect((await deploy.getEnvironmentShape({ environmentId: OTHER_ENV })).declaredShape).toBeUndefined();
    expect((await deploy.getStatus({ environmentId: OTHER_ENV })).deployments).toEqual([]);
    expect((await deploy.getStatus({ environmentId: "env-prod", deploymentId: "dep-other-api" })).deployments).toEqual([]);
    expect((await deploy.getStatus({ environmentId: "env-prod", deploymentId: "dep-prod-api" })).deployments.map((d) => d.deployment?.name)).toEqual(["api"]);
    expect((await deploy.listPromotions({ environmentId: OTHER_ENV })).promotions).toEqual([]);
    expect((await deploy.getRollout({ environmentId: OTHER_ENV, promotionId: "promo-prod-v183" })).rollout).toBeUndefined();
    expect((await deploy.listConvergences({ environmentId: OTHER_ENV })).convergences).toEqual([]);
    const secrets = await client(SecretStoreService);
    expect((await secrets.listSecrets({ environmentId: OTHER_ENV })).secrets).toEqual([]);
    expect((await secrets.getSecretVersions({ environmentId: OTHER_ENV, name: "DATABASE_URL" })).summary).toBeUndefined();
    const { DomainService } = await import("@/gen/controlplane/services/domain/v1/domain_pb");
    expect((await (await client(DomainService)).listDomainBindings({ environmentId: OTHER_ENV })).bindings).toEqual([]);
  });

  it("the story's project handlers answer nothing for another project", async () => {
    const deploy = await client(DeployService);
    expect((await deploy.getLiveView({ project: "other" })).environments).toEqual([]);
    expect((await deploy.listEnvironments({ project: "other" })).environments).toEqual([]);
    expect((await deploy.listReleases({ project: "other" })).releases).toEqual([]);
    expect((await deploy.listReleases({ project: DEMO_PROJECT, promotedToEnvironmentId: OTHER_ENV })).releases).toEqual([]);
    expect((await deploy.getRelease({ project: "other", version: "v1.8.3" })).release).toBeUndefined();
    expect((await deploy.compareReleases({ project: "other", fromVersion: "v1.8.2", toVersion: "v1.8.3" })).to).toBeUndefined();
    expect((await deploy.compareReleases({ project: DEMO_PROJECT, environmentId: OTHER_ENV, fromVersion: "v1.8.2", toVersion: "v1.8.3" })).to).toBeUndefined();
    expect((await listActivity({ orgId: "org-demo", projectId: "other", envIds: [] })).events).toEqual([]);
    expect((await listActivity({ orgId: "org-demo", projectId: DEMO_PROJECT, envIds: [OTHER_ENV] })).events).toEqual([]);
    // And shop's, named: still answered.
    expect((await deploy.getRelease({ project: DEMO_PROJECT, version: "v1.8.3" })).release?.version).toBe("v1.8.3");
    const prodOnly = await listActivity({ orgId: "org-demo", projectId: DEMO_PROJECT, envIds: ["env-prod"] });
    expect(prodOnly.events.length).toBeGreaterThan(0);
    expect(prodOnly.events.every((e) => e.env?.id === "env-prod")).toBe(true);
  });

  it("every read of every console service, story or default, is empty when it names another project or environment", async () => {
    const t = (await import("@/lib/connect")).transport;
    /** The request with every project / environment field it has (top level or one message down) set to another app's. */
    function foreign(input: DescMessage, depth = 0): Record<string, unknown> | null {
      const out: Record<string, unknown> = {};
      let named = false;
      for (const f of input.fields) {
        if (f.oneof) continue;
        if (f.fieldKind === "scalar" && /^project(_id)?$/.test(f.name)) [out[f.localName], named] = ["other-app", true];
        else if (f.fieldKind === "scalar" && /(^|_)(environment_id|env_id)$/.test(f.name)) [out[f.localName], named] = [OTHER_ENV, true];
        else if (f.fieldKind === "list" && /(^|_)(environment_ids|env_ids)$/.test(f.name)) [out[f.localName], named] = [[OTHER_ENV], true];
        else if (f.fieldKind === "message" && depth < 1) {
          const inner = foreign(f.message, depth + 1);
          if (inner) [out[f.localName], named] = [inner, true];
        }
      }
      return named ? out : null;
    }
    let scoped = 0;
    for (const service of DEMO_DEFAULT_SERVICES) {
      const c = createClient(service, t) as unknown as Record<string, (req: object) => Promise<object>>;
      for (const method of service.methods.filter((m) => m.methodKind === "unary" && isDemoRead(m))) {
        const req = foreign(method.input);
        if (!req) continue;
        expect(outsideDemo(method.input, create(method.input, req as never)), `${service.typeName}/${method.name}`).toBe(true);
        const res = await c[method.localName](req);
        expect(equals(method.output, res as never, create(method.output)), `${service.typeName}/${method.name}`).toBe(true);
        scoped++;
      }
    }
    expect(scoped).toBeGreaterThan(30);
  });

  it("a secret's versions only in an environment that holds it: PAYMENTS_WEBHOOK_SECRET is not in dev", async () => {
    expect(DEMO_SECRETS.find((s) => s.name === "PAYMENTS_WEBHOOK_SECRET")?.set).not.toContain("env-dev");
    const secrets = await client(SecretStoreService);
    const dev = await secrets.getSecretVersions({ environmentId: "env-dev", name: "PAYMENTS_WEBHOOK_SECRET" });
    expect(dev.summary).toBeUndefined();
    expect(dev.versions).toEqual([]);
    const prod = await secrets.getSecretVersions({ environmentId: "env-prod", name: "PAYMENTS_WEBHOOK_SECRET" });
    expect(prod.summary?.name).toBe("PAYMENTS_WEBHOOK_SECRET");
    expect(prod.versions.length).toBe(1);
    // And the listing agrees with the versions, per environment.
    for (const env of DEMO_ENVS) {
      const listed = new Set((await secrets.listSecrets({ environmentId: env.id })).secrets.map((s) => s.name));
      for (const s of DEMO_SECRETS) {
        const versions = await secrets.getSecretVersions({ environmentId: env.id, name: s.name });
        expect(!!versions.summary, `${env.name} ${s.name}`).toBe(listed.has(s.name));
      }
    }
  });
});
