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
import { ScalarType, create, createFileRegistry, equals, type DescField, type DescMessage, type DescService } from "@bufbuild/protobuf";
import { FieldDescriptorProto_Label, FieldDescriptorProto_Type, FileDescriptorSetSchema } from "@bufbuild/protobuf/wkt";
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

const { OPTIONAL: LABEL_OPTIONAL, REPEATED: LABEL_REPEATED } = FieldDescriptorProto_Label;
const { MESSAGE: TYPE_MESSAGE, STRING: TYPE_STRING } = FieldDescriptorProto_Type;

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

  it("Deploys: one per promotion, newest first, and a staging build that failed before it cut a release", async () => {
    const { listDeployRuns, getDeployRun } = await import("@/services/forge/deployAttempts");
    const runs = await listDeployRuns(DEMO_PROJECT);
    expect(runs.length).toBe(10);
    expect(runs.every((r, i) => i === 0 || Date.parse(runs[i - 1].startedAt!) >= Date.parse(r.startedAt!))).toBe(true);
    expect(runs.slice(0, 2).map((r) => [r.env, r.release, r.state])).toEqual([
      ["dev", "v1.8.4", "succeeded"],
      ["prod", "v1.8.3", "succeeded"],
    ]);
    const failed = runs.filter((r) => r.state === "failed");
    expect(failed.map((r) => [r.env, r.release, r.failedPhase])).toEqual([["staging", "", "build"]]);
    expect((await getDeployRun(failed[0].id)).log).toMatch(/nothing was cut or promoted/);
    // prod's from a terminal: its output stayed there.
    expect((await listDeployRuns(DEMO_PROJECT, "prod")).every((r) => r.source === "terminal" && !r.hasLog)).toBe(true);
    expect(await listDeployRuns("someone-else")).toEqual([]);
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
    // An environment named by name, not id (ListDeployAttemptsRequest.environment_name).
    expect((await deploy.listDeployAttempts({ project: DEMO_PROJECT, environmentName: "other-prod" })).attempts).toEqual([]);
    expect((await deploy.listDeployAttempts({ project: DEMO_PROJECT, environmentName: "prod" })).attempts.length).toBeGreaterThan(0);
    // And shop's, named: still answered.
    expect((await deploy.getRelease({ project: DEMO_PROJECT, version: "v1.8.3" })).release?.version).toBe("v1.8.3");
    const prodOnly = await listActivity({ orgId: "org-demo", projectId: DEMO_PROJECT, envIds: ["env-prod"] });
    expect(prodOnly.events.length).toBeGreaterThan(0);
    expect(prodOnly.events.every((e) => e.env?.id === "env-prod")).toBe(true);
  });

  it("every read of every console service, story or default, is empty when it names another project or environment", async () => {
    const t = (await import("@/lib/connect")).transport;
    /**
     * The request with every project / environment selector it has, at any depth (nested,
     * repeated and oneof messages too), set to another app's, under every name demoScope.ts reads.
     */
    function foreign(input: DescMessage, seen: ReadonlySet<string> = new Set()): Record<string, unknown> | null {
      if (seen.has(input.typeName)) return null;
      const inside = new Set([...seen, input.typeName]);
      const out: Record<string, unknown> = {};
      let named = false;
      const set = (f: DescField, value: unknown) => {
        if (f.oneof) {
          if (out[f.oneof.localName]) return;
          out[f.oneof.localName] = { case: f.localName, value };
        } else out[f.localName] = value;
        named = true;
      };
      for (const f of input.fields) {
        const selector = /(^|_)project(_id|_name|_slug)?s?$/.test(f.name)
          ? "other-app"
          : /(^|_)(environment|env)_ids?$/.test(f.name)
            ? OTHER_ENV
            : /(^|_)(environment|env)(_name)?s?$/.test(f.name)
              ? "other-prod"
              : undefined;
        if (f.fieldKind === "scalar" && f.scalar === ScalarType.STRING && selector) set(f, selector);
        else if (f.fieldKind === "list" && f.listKind === "scalar" && f.scalar === ScalarType.STRING && selector) set(f, [selector]);
        else if (f.fieldKind === "message" || (f.fieldKind === "list" && f.listKind === "message")) {
          const inner = foreign(f.message, inside);
          if (inner) set(f, f.fieldKind === "list" ? [inner] : inner);
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

  it("a selector at any depth, under any selector name, is judged: nested, repeated, mapped and oneof messages", () => {
    // Shaped like no console request on purpose: the guard must not depend on today's contracts' shapes.
    const registry = createFileRegistry(
      create(FileDescriptorSetSchema, {
        file: [
          {
            name: "demo_scope_probe.proto",
            package: "demo.scope.probe",
            syntax: "proto3",
            messageType: [
              {
                name: "Req",
                field: [
                  { name: "outer", number: 1, label: LABEL_OPTIONAL, type: TYPE_MESSAGE, typeName: ".demo.scope.probe.Outer" },
                  { name: "outers", number: 2, label: LABEL_REPEATED, type: TYPE_MESSAGE, typeName: ".demo.scope.probe.Outer" },
                  { name: "by_key", number: 3, label: LABEL_REPEATED, type: TYPE_MESSAGE, typeName: ".demo.scope.probe.Req.ByKeyEntry" },
                  { name: "self", number: 4, label: LABEL_OPTIONAL, type: TYPE_MESSAGE, typeName: ".demo.scope.probe.Req" },
                ],
                nestedType: [
                  {
                    name: "ByKeyEntry",
                    options: { mapEntry: true },
                    field: [
                      { name: "key", number: 1, label: LABEL_OPTIONAL, type: TYPE_STRING },
                      { name: "value", number: 2, label: LABEL_OPTIONAL, type: TYPE_MESSAGE, typeName: ".demo.scope.probe.Outer" },
                    ],
                  },
                ],
              },
              { name: "Outer", field: [{ name: "middle", number: 1, label: LABEL_OPTIONAL, type: TYPE_MESSAGE, typeName: ".demo.scope.probe.Middle" }] },
              {
                name: "Middle",
                field: [
                  { name: "by_id", number: 1, label: LABEL_OPTIONAL, type: TYPE_MESSAGE, typeName: ".demo.scope.probe.Leaf", oneofIndex: 0 },
                  { name: "other", number: 2, label: LABEL_OPTIONAL, type: TYPE_STRING, oneofIndex: 0 },
                ],
                oneofDecl: [{ name: "pick" }],
              },
              {
                name: "Leaf",
                field: [
                  { name: "parent_project_id", number: 1, label: LABEL_OPTIONAL, type: TYPE_STRING },
                  { name: "project_name", number: 2, label: LABEL_OPTIONAL, type: TYPE_STRING },
                  { name: "target_env_id", number: 3, label: LABEL_OPTIONAL, type: TYPE_STRING },
                  { name: "from_environment", number: 4, label: LABEL_OPTIONAL, type: TYPE_STRING },
                  { name: "env_names", number: 5, label: LABEL_REPEATED, type: TYPE_STRING },
                  { name: "note", number: 6, label: LABEL_OPTIONAL, type: TYPE_STRING },
                ],
              },
            ],
          },
        ],
      }),
    );
    const Req = registry.getMessage("demo.scope.probe.Req")!;
    const leaf = (l: Record<string, unknown>) => ({ middle: { pick: { case: "byId", value: l } } });
    const outside = (r: Record<string, unknown>) => outsideDemo(Req, create(Req, r as never));
    for (const l of [{ parentProjectId: "other-app" }, { projectName: "other-app" }, { targetEnvId: OTHER_ENV }, { fromEnvironment: "other-prod" }, { envNames: ["prod", "other-prod"] }]) {
      expect(outside({ outer: leaf(l) }), JSON.stringify(l)).toBe(true);
      expect(outside({ outers: [leaf({}), leaf(l)] }), JSON.stringify(l)).toBe(true);
      expect(outside({ byKey: { a: leaf(l) } }), JSON.stringify(l)).toBe(true);
      expect(outside({ self: { self: { self: { outer: leaf(l) } } } }), JSON.stringify(l)).toBe(true);
    }
    // Shop's, by id or by name, and a field that selects nothing: in scope.
    const shop = { parentProjectId: DEMO_PROJECT, projectName: DEMO_PROJECT, targetEnvId: "env-prod", fromEnvironment: "staging", envNames: ["dev", "prod"], note: "other-app" };
    expect(outside({ outer: leaf(shop), outers: [leaf(shop)], byKey: { a: leaf(shop) }, self: { outer: leaf(shop) } })).toBe(false);
    expect(outside({})).toBe(false);
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
