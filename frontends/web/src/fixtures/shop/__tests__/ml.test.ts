// Copyright (c) 2026 Reliant Labs

/**
 * The demo's ML pages' answers (model/ml.ts, served by handlers/ml.ts) and
 * the org's ML entitlement (handlers/organization.ts), asked through a real
 * Connect client over the shop pack's transport, read-only guard included:
 * the story's key facts hold, every answer names the same clusters and the
 * same model digests, nothing happens after the page load, every answer
 * survives protobuf binary, and a read outside the pack is answered empty.
 * Requesting access is refused, as every demo write is.
 */

import { toBinary, type DescMessage, type Message } from "@bufbuild/protobuf";
import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Code, createClient, type Client, type Transport } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import {
  GetMlMachinesResponseSchema,
  GetMlStackResponseSchema,
  ListMlFindingsResponseSchema,
  ListMlInferenceServicesResponseSchema,
  ListMlModelsResponseSchema,
  ListMlRunsResponseSchema,
  MlComponentState,
  MlEarlyAccessService,
  MlFindingSeverity,
  MlNodeHealth,
  MlReleaseVerdict,
  MlRunState,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";

import { createShopTransport, SHOP_READ_ONLY_MESSAGE } from "..";
import {
  ML_BAD_DEVICE,
  ML_BAD_NODE,
  ML_H100,
  ML_IDLE_NODE,
  ML_MODEL_DIGESTS,
  ML_MODEL_NAME,
  ML_RELEASE_SUMMARY,
  ML_RUN_IDS,
  ML_SERVE_CLUSTER,
  ML_TRAIN_CLUSTER,
  ML_VLLM_RUNTIME,
} from "../model/ml";
import { DEMO_ORG, resetDemoNowForTests } from "../shopStory";

const NOW = Date.UTC(2026, 10, 10, 15, 0, 0);
const req = { orgId: DEMO_ORG };

/** Every timestamp in `value` (a message), with its path. */
function timestamps(value: unknown, out: { path: string; ms: number }[] = [], path = ""): { path: string; ms: number }[] {
  if (Array.isArray(value)) value.forEach((item, i) => timestamps(item, out, `${path}[${i}]`));
  else if (value && typeof value === "object") {
    if ((value as { $typeName?: string }).$typeName === "google.protobuf.Timestamp") out.push({ path, ms: timestampMs(value as Timestamp) });
    else for (const [key, item] of Object.entries(value)) timestamps(item, out, `${path}.${key}`);
  }
  return out;
}

let transport: Transport;
let ml: Client<typeof MlEarlyAccessService>;
let org: Client<typeof OrganizationService>;

beforeAll(() => {
  resetDemoNowForTests(NOW);
  transport = createShopTransport();
  ml = createClient(MlEarlyAccessService, transport);
  org = createClient(OrganizationService, transport);
});

afterAll(() => resetDemoNowForTests());

describe("the demo's ML story", () => {
  it("has ML enabled for shop's org, granted before now", async () => {
    const { features } = await org.getMyOrgEntitlements(req);
    expect(features).toHaveLength(1);
    expect(features[0]).toMatchObject({ feature: "ml", enabled: true });
    expect(timestampMs(features[0].grantedAt!)).toBeLessThan(NOW);
    expect(timestampMs(features[0].requestedAt!)).toBeLessThan(timestampMs(features[0].grantedAt!));
  });

  it("refuses a request for access, as every demo write", async () => {
    await expect(org.requestOrgEntitlement({ orgId: DEMO_ORG, feature: "ml" })).rejects.toMatchObject({ code: Code.PermissionDenied });
    await expect(org.requestOrgEntitlement({ orgId: DEMO_ORG, feature: "ml" })).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
  });

  it("shows two clusters, an H100 pool with ONE bad node (Xid 79 on GPU-5), and an L4 pool with 2 idle GPUs", async () => {
    const machines = await ml.getMlMachines(req);
    expect(machines.clusters.map((c) => [c.clusterId, c.provider])).toEqual([
      [ML_TRAIN_CLUSTER, "gke"],
      [ML_SERVE_CLUSTER, "eks"],
    ]);
    const [h100, l4] = machines.pools;
    expect(h100).toMatchObject({ clusterId: ML_TRAIN_CLUSTER, acceleratorProduct: ML_H100, acceleratorsTotal: 32 });
    expect(h100.nodes).toHaveLength(4);
    expect(h100.nodes.every((n) => n.acceleratorCount === 8 && n.sharing === "none" && n.driverVersion.startsWith("550."))).toBe(true);

    const bad = machines.pools.flatMap((p) => p.nodes).filter((n) => n.health === MlNodeHealth.BAD);
    expect(bad.map((n) => n.name)).toEqual([ML_BAD_NODE]);
    expect(bad[0]).toMatchObject({ acceleratorsAllocatable: 7, cordoned: false });
    expect(bad[0].flags.find((f) => f.kind === "xid")).toMatchObject({ code: "79", device: ML_BAD_DEVICE, message: "GPU has fallen off the bus" });

    expect(l4).toMatchObject({ clusterId: ML_SERVE_CLUSTER, acceleratorProduct: "NVIDIA L4", acceleratorsIdle: 2 });
    expect(l4.nodes.map((n) => n.name)).toContain(ML_IDLE_NODE);
    expect(machines.pools.reduce((a, p) => a + p.nodes.reduce((b, n) => b + n.acceleratorsAllocated, 0), 0)).toBe(h100.acceleratorsAllocated + l4.acceleratorsAllocated);

    for (const pool of machines.pools) {
      expect(pool.utilization).toHaveLength(24);
      const times = pool.utilization.map((p) => timestampMs(p.time!));
      expect(times).toEqual([...times].sort((a, b) => a - b));
      expect(pool.utilization.every((p) => p.utilizationPercent >= 0 && p.utilizationPercent <= 100 && p.utilizationPercent <= p.allocatedPercent + 1e-9)).toBe(true);
    }
  });

  it("narrows to one cluster", async () => {
    const machines = await ml.getMlMachines({ ...req, clusterId: ML_SERVE_CLUSTER });
    expect(machines.clusters.map((c) => c.clusterId)).toEqual([ML_SERVE_CLUSTER]);
    expect(machines.pools.every((p) => p.clusterId === ML_SERVE_CLUSTER)).toBe(true);
    expect((await ml.getMlStack({ ...req, clusterId: ML_TRAIN_CLUSTER })).components.every((c) => c.clusterId === ML_TRAIN_CLUSTER)).toBe(true);
    expect((await ml.listMlFindings({ ...req, clusterId: ML_TRAIN_CLUSTER })).findings.map((f) => f.ruleId)).toEqual(["bad-accelerator", "shm-too-small", "dcgm-profiling-off"]);
  });

  it("finds the GPU Operator's DCGM exporter and Kueue adopted, installs JobSet and node-problem-detector, and has no Trainer on serve", async () => {
    const { components } = await ml.getMlStack(req);
    const on = (cluster: string, name: string) => components.find((c) => c.clusterId === cluster && c.name === name);
    expect(on(ML_TRAIN_CLUSTER, "dcgm-exporter")).toMatchObject({ state: MlComponentState.ADOPTED, version: "3.3.9", via: "GPU Operator v24.9.0", namespace: "gpu-operator" });
    expect(on(ML_TRAIN_CLUSTER, "dcgm-exporter")!.declaration).toBe('forge.Adopt { component = "dcgm-exporter", namespace = "gpu-operator" }');
    expect(on(ML_TRAIN_CLUSTER, "nvidia-device-plugin")).toMatchObject({ state: MlComponentState.ADOPTED, via: "GPU Operator v24.9.0" });
    expect(on(ML_TRAIN_CLUSTER, "gpu-feature-discovery")?.state).toBe(MlComponentState.ADOPTED);
    expect(on(ML_TRAIN_CLUSTER, "kueue")).toMatchObject({ state: MlComponentState.ADOPTED, namespace: "kueue-system" });
    expect(on(ML_TRAIN_CLUSTER, "jobset")?.state).toBe(MlComponentState.INSTALLED);
    expect(on(ML_TRAIN_CLUSTER, "node-problem-detector")?.state).toBe(MlComponentState.INSTALLED);
    expect(on(ML_TRAIN_CLUSTER, "node-problem-detector")?.note).toMatch(/GPU log rule/);
    expect(on(ML_SERVE_CLUSTER, "kubeflow-trainer")?.state).toBe(MlComponentState.MISSING);
    expect(on(ML_SERVE_CLUSTER, "kserve")?.state).toBe(MlComponentState.MISSING);
    expect(components.every((c) => c.declaration.startsWith("forge.Adopt {") || c.declaration.startsWith("forge.Install {"))).toBe(true);
  });

  it("lists findings worst first: the Xid-79 node, then the pod-start breakdown, kubelet tracing among them", async () => {
    const { findings } = await ml.listMlFindings(req);
    expect(findings.map((f) => f.ruleId)).toEqual(["bad-accelerator", "accel-idle", "pod-start-slow", "shm-too-small", "dcgm-profiling-off", "kubelet-tracing-off"]);
    const severities = findings.map((f) => f.severity);
    expect(severities).toEqual([...severities].sort((a, b) => a - b));
    expect(findings[0]).toMatchObject({ severity: MlFindingSeverity.CRITICAL, subject: ML_BAD_NODE, clusterId: ML_TRAIN_CLUSTER });
    expect(findings[0].fixCommand).toBe(`kubectl cordon ${ML_BAD_NODE} && kubectl drain ${ML_BAD_NODE} --ignore-daemonsets --delete-emptydir-data`);
    expect(findings[0].evidence.some((e) => e.value.includes("79"))).toBe(true);
    expect(findings.find((f) => f.ruleId === "accel-idle")).toMatchObject({ severity: MlFindingSeverity.WARNING, subject: ML_IDLE_NODE });

    const start = findings.find((f) => f.ruleId === "pod-start-slow")!.podStart!;
    expect(start.stages.map((s) => [s.name, s.p50Seconds, s.p95Seconds])).toEqual([
      ["scheduling", 4, 41],
      ["image pull", 96, 210],
      ["container start", 3, 6],
      ["model load", 58, 80],
      ["ready", 2, 3],
    ]);
    const tracing = findings.find((f) => f.ruleId === "kubelet-tracing-off")!;
    expect(tracing.severity).toBe(MlFindingSeverity.INFO);
    expect(tracing.fixKcl + tracing.fixCommand).toMatch(/[Kk]ubelet.?[Tt]racing|kubelet_tracing/);
    expect(findings.find((f) => f.ruleId === "shm-too-small")?.severity).toBe(MlFindingSeverity.WARNING);

    // Every fix is one that exists today: no forge KCL (forge declares no ML resources yet).
    expect(findings.filter((f) => /forge\.[A-Z]/.test(f.fixKcl + f.fixCommand)).map((f) => f.ruleId)).toEqual([]);
    const fix = (rule: string) => findings.find((f) => f.ruleId === rule)!.fixKcl;
    expect(fix("pod-start-slow")).toMatch(/^kind: DaemonSet$/m);
    expect(fix("pod-start-slow")).toContain("image: vllm/vllm-openai:v0.6.3");
    expect(fix("pod-start-slow")).toContain("eks.amazonaws.com/nodegroup: l4-pool");
    expect(fix("shm-too-small")).toMatch(/medium: Memory\n\s+sizeLimit: 16Gi/);
    expect(fix("shm-too-small")).toContain("mountPath: /dev/shm");
    expect(fix("kubelet-tracing-off")).toMatch(/^kind: KubeletConfiguration$/m);
    expect(fix("kubelet-tracing-off")).toMatch(/featureGates:\n\s+KubeletTracing: true/);
    expect(fix("kubelet-tracing-off")).toContain("samplingRatePerMillion: 10000");
  });

  it("runs: one running fine-tune on 2×8 H100s, one that produced the served model, one that failed", async () => {
    const { runs } = await ml.listMlRuns(req);
    expect(runs.map((r) => [r.id, r.state])).toEqual([
      [ML_RUN_IDS.running, MlRunState.RUNNING],
      [ML_RUN_IDS.succeeded, MlRunState.SUCCEEDED],
      [ML_RUN_IDS.failed, MlRunState.FAILED],
    ]);
    const started = runs.map((r) => timestampMs(r.startedAt!));
    expect(started).toEqual([...started].sort((a, b) => b - a));
    expect(runs[0]).toMatchObject({ orchestrator: "TrainJob", acceleratorCount: 16, acceleratorProduct: ML_H100, project: "shop", clusterId: ML_TRAIN_CLUSTER });
    expect(runs[0].name).toMatch(/llama-3\.1-8b/);
    expect(runs[0].finishedAt).toBeUndefined();
    expect(runs[0].metrics.map((m) => m.name)).toEqual(expect.arrayContaining(["loss", "eval/loss"]));
    const loss = runs[0].metrics.find((m) => m.name === "loss")!.points;
    expect(loss.length).toBeGreaterThan(10);
    expect(loss.at(-1)!.value).toBeLessThan(loss[0].value);
    expect(runs[1].outputModelDigest).toBe(ML_MODEL_DIGESTS.v2);
    expect(runs[2].restarts).toBeGreaterThan(0);
    expect(runs[2].preemptions).toBeGreaterThan(0);
  });

  it("models: two versions by sha256 digest, the newer from the succeeded run and serving in prod", async () => {
    const { models } = await ml.listMlModels(req);
    expect(models).toHaveLength(1);
    expect(models[0]).toMatchObject({ name: ML_MODEL_NAME, project: "shop", repository: "registry.reliantlabs.io/acme/models/support-assistant" });
    const [v2, v1] = models[0].versions;
    expect([v2.digest, v1.digest]).toEqual([ML_MODEL_DIGESTS.v2, ML_MODEL_DIGESTS.v1]);
    expect(models[0].versions.every((v) => /^sha256:[0-9a-f]{64}$/.test(v.digest))).toBe(true);
    expect(v2.digest).not.toBe(v1.digest);
    expect(timestampMs(v2.createdAt!)).toBeGreaterThan(timestampMs(v1.createdAt!));
    expect(v2.runId).toBe(ML_RUN_IDS.succeeded);
    expect(v2.servingIn).toEqual(["shop/prod/support-assistant"]);
    expect(Object.keys(v2.evalMetrics).length).toBeGreaterThan(0);
  });

  it("inference: vLLM serving the newer digest on the L4 pool, its release check PASSED", async () => {
    const { services } = await ml.listMlInferenceServices(req);
    expect(services).toHaveLength(1);
    const svc = services[0];
    expect(svc).toMatchObject({ runtime: ML_VLLM_RUNTIME, clusterId: ML_SERVE_CLUSTER, modelDigest: ML_MODEL_DIGESTS.v2, replicas: 3, releaseVerdict: MlReleaseVerdict.PASS, releaseVerdictSummary: ML_RELEASE_SUMMARY, environment: "prod" });
    expect(svc.ttftP95Ms).toBeGreaterThan(0);
    expect(svc.queueWaitP95Ms).toBeGreaterThan(0);
    expect(svc.tokensPerSecond).toBeGreaterThan(0);
    expect(svc.kvCacheUsagePercent).toBeGreaterThan(0);
    expect(svc.coldStart?.stages.map((s) => s.name)).toEqual(["scheduling", "image pull", "container start", "model load", "ready"]);
    expect(timestampMs(svc.releaseCheckedAt!)).toBeLessThan(NOW);
  });

  it("names only its own clusters and digests, tells nothing after now, and every answer survives the wire", async () => {
    const answers: [DescMessage, Message][] = [
      [GetMlMachinesResponseSchema, await ml.getMlMachines(req)],
      [GetMlStackResponseSchema, await ml.getMlStack(req)],
      [ListMlFindingsResponseSchema, await ml.listMlFindings(req)],
      [ListMlRunsResponseSchema, await ml.listMlRuns(req)],
      [ListMlModelsResponseSchema, await ml.listMlModels(req)],
      [ListMlInferenceServicesResponseSchema, await ml.listMlInferenceServices(req)],
    ];
    const clusters = new Set<string>();
    JSON.stringify(answers.map(([, m]) => m), (k, v) => {
      if (k === "clusterId") clusters.add(v as string);
      return typeof v === "bigint" ? String(v) : v;
    });
    expect([...clusters].sort()).toEqual([ML_SERVE_CLUSTER, ML_TRAIN_CLUSTER].sort());
    for (const [schema, message] of answers) {
      expect(() => toBinary(schema, message)).not.toThrow();
      expect(timestamps(message).filter((t) => t.ms > NOW)).toEqual([]);
    }
  });

  it("answers another org's or another app's read empty", async () => {
    const foreign = { orgId: "org-someone-else" };
    expect((await ml.getMlMachines(foreign)).pools).toEqual([]);
    expect((await ml.listMlFindings(foreign)).findings).toEqual([]);
    expect((await org.getMyOrgEntitlements(foreign)).features).toEqual([]);
    expect((await ml.listMlRuns({ ...req, project: "not-shop" })).runs).toEqual([]);
    expect((await ml.listMlModels({ ...req, project: "not-shop" })).models).toEqual([]);
    expect((await ml.listMlRuns({ ...req, project: "shop" })).runs).toHaveLength(3);
  });

  it("tells the same story for the forge tour's sample, every id prefixed", async () => {
    const sample = createClient(MlEarlyAccessService, createShopTransport({ app: "sample_shop", org: null, idPrefix: "sample_" }));
    const { runs } = await sample.listMlRuns({ orgId: "org-anyones", project: "sample_shop" });
    expect(runs.every((r) => r.project === "sample_shop" && r.id.startsWith("sample_") && r.clusterId.startsWith("sample_"))).toBe(true);
    const machines = await sample.getMlMachines({ orgId: "org-anyones", clusterId: `sample_${ML_TRAIN_CLUSTER}` });
    expect(machines.clusters.map((c) => c.clusterId)).toEqual([`sample_${ML_TRAIN_CLUSTER}`]);
  });
});
