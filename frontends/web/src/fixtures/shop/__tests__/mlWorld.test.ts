// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world's referential integrity: every subject id any answer
 * names resolves, through the same rpc a page would make, to an entity of
 * the world. A page's link never lands on NotFound.
 *
 * It walks every list and entity answer (fleet, pools, nodes, devices, runs,
 * models, versions, eval runs, services, findings, queues, lineage), collects
 * every MlSubject (and the plain id fields that name one), and resolves each
 * by kind. It also asks the whole pack over a real Connect client, read-only
 * guard included, so each answer survives the wire, and that nothing is in
 * the future.
 */

import { toBinary, type DescMessage, type Message } from "@bufbuild/protobuf";
import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, createClient, type Client } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { MlEarlyAccessService, MlSubjectKind, type MlSubject } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { createShopTransport } from "..";
import { RUNS } from "../model/ml";
import { DEMO_ORG, resetDemoNowForTests } from "../shopStory";

const NOW = Date.UTC(2026, 10, 26, 15, 0, 0);
const org = { orgId: DEMO_ORG };
type Ml = Client<typeof MlEarlyAccessService>;

let ml: Ml;
beforeAll(() => {
  resetDemoNowForTests(NOW);
  ml = createClient(MlEarlyAccessService, createShopTransport());
});
afterAll(() => resetDemoNowForTests(null));

/** Every MlSubject inside `value`, with where it was found. */
function subjects(value: unknown, out: { at: string; s: MlSubject }[] = [], path = ""): { at: string; s: MlSubject }[] {
  if (Array.isArray(value)) value.forEach((v, i) => subjects(v, out, `${path}[${i}]`));
  else if (value && typeof value === "object") {
    const t = (value as { $typeName?: string }).$typeName;
    if (t === "controlplane.v1.MlSubject") {
      const s = value as MlSubject;
      if (s.kind !== MlSubjectKind.UNSPECIFIED) out.push({ at: path, s });
      return out;
    }
    for (const [k, v] of Object.entries(value)) subjects(v, out, `${path}.${k}`);
  }
  return out;
}

/** Every timestamp inside `value`, but the ones that are about the future by definition (a snooze's end). */
const FUTURE_FIELDS = new Set(["snoozedUntil"]);
function timestamps(value: unknown, out: number[] = []): number[] {
  if (Array.isArray(value)) value.forEach((v) => timestamps(v, out));
  else if (value && typeof value === "object") {
    if ((value as { $typeName?: string }).$typeName === "google.protobuf.Timestamp") out.push(timestampMs(value as Timestamp));
    else for (const [k, v] of Object.entries(value)) if (!FUTURE_FIELDS.has(k)) timestamps(v, out);
  }
  return out;
}

/** Resolves a subject through the rpc its page reads; true when it exists. */
async function resolves(s: MlSubject): Promise<boolean> {
  const parts = s.id.split("/");
  const ok = async (p: Promise<unknown>) => {
    try {
      await p;
      return true;
    } catch (e) {
      if (e instanceof ConnectError && e.code === Code.NotFound) return false;
      throw e;
    }
  };
  switch (s.kind) {
    case MlSubjectKind.ORG:
      return true;
    case MlSubjectKind.CLUSTER:
      return (await ml.getMlFleet(org)).clusters.some((c) => c.id === s.id);
    case MlSubjectKind.POOL:
      return ok(ml.getMlPool({ ...org, poolId: s.id }));
    case MlSubjectKind.NODE:
      return ok(ml.getMlNode({ ...org, nodeId: s.id }));
    case MlSubjectKind.DEVICE:
      return ok(ml.getMlDevice({ ...org, uuid: s.id }));
    case MlSubjectKind.EXPERIMENT:
      return (await ml.listMlExperiments(org)).experiments.some((e) => e.id === s.id);
    case MlSubjectKind.RUN:
      return ok(ml.getMlRun({ ...org, run: s.id }));
    case MlSubjectKind.PHASE: {
      const run = (await ml.getMlRun({ ...org, run: parts[0] })).run!;
      return run.phases.some((p) => p.key === parts[1]);
    }
    case MlSubjectKind.ATTEMPT:
    case MlSubjectKind.RANK: {
      const run = (await ml.getMlRun({ ...org, run: parts[0] })).run!;
      const attempt = run.phases.find((p) => p.key === parts[1])?.attempts.find((a) => a.n === Number(parts[2]));
      if (!attempt) return false;
      return s.kind === MlSubjectKind.ATTEMPT || attempt.ranks.length === 0 || attempt.ranks.some((r) => r.rank === Number(parts[3]));
    }
    case MlSubjectKind.CHECKPOINT: {
      const detail = await ml.getMlRun({ ...org, run: parts[0] });
      return detail.checkpoints.some((c) => c.id === s.id);
    }
    case MlSubjectKind.MODEL:
      // A Hugging Face repo id (a base model) opens on Hugging Face, not the registry.
      return s.id.includes("/") || ok(ml.getMlModel({ ...org, modelId: s.id }));
    case MlSubjectKind.MODEL_VERSION:
      return ok(ml.getMlModelVersion({ ...org, versionId: s.id }));
    case MlSubjectKind.EVAL_RUN:
      return ok(ml.getMlEvalRun({ ...org, evalRunId: s.id }));
    case MlSubjectKind.INFERENCE_SERVICE:
      return ok(ml.getMlInferenceService({ ...org, serviceId: s.id }));
    case MlSubjectKind.REVISION: {
      const svc = await ml.getMlInferenceService({ ...org, serviceId: parts[0] });
      return svc.revisions.some((r) => r.id === s.id);
    }
    case MlSubjectKind.REPLICA: {
      const svc = await ml.getMlInferenceService({ ...org, serviceId: parts[0] });
      return svc.replicas.some((r) => r.id === s.id);
    }
    case MlSubjectKind.QUEUE:
      return (await ml.getMlQueues(org)).queues.some((q) => q.id === s.id);
    case MlSubjectKind.FINDING:
      return (await ml.listMlFindings({ ...org, includeClosed: true })).findings.some((f) => f.id === s.id);
    // Not pages of their own: named, never opened.
    case MlSubjectKind.WORKLOAD:
    case MlSubjectKind.POD:
    case MlSubjectKind.DATASET:
    case MlSubjectKind.RELEASE:
      return true;
    default:
      return false;
  }
}

/** Every answer a page can make, as [name, message]. */
async function everyAnswer(): Promise<[string, Message][]> {
  const out: [string, Message][] = [];
  const add = (name: string, m: Message) => out.push([name, m]);
  const fleet = await ml.getMlFleet(org);
  add("fleet", fleet);
  for (const p of fleet.pools) {
    const pool = await ml.getMlPool({ ...org, poolId: p.id });
    add(`pool ${p.id}`, pool);
    for (const n of pool.nodes) {
      add(`node ${n.id}`, await ml.getMlNode({ ...org, nodeId: n.id }));
      for (const d of n.devices.slice(0, 2)) add(`device ${d.uuid}`, await ml.getMlDevice({ ...org, uuid: d.uuid }));
    }
  }
  add("stack", await ml.getMlStack(org));
  add("queues", await ml.getMlQueues(org));
  add("findings", await ml.listMlFindings({ ...org, includeClosed: true }));
  add("experiments", await ml.listMlExperiments(org));
  const runs = await ml.listMlRuns(org);
  add("runs", runs);
  for (const r of runs.runs) {
    add(`run ${r.name}`, await ml.getMlRun({ ...org, run: r.id }));
    const subject = { kind: MlSubjectKind.RUN, id: r.id, label: r.name };
    add(`events ${r.name}`, await ml.listMlEvents({ ...org, subject }));
    add(`lineage ${r.name}`, await ml.getMlLineage({ ...org, subject }));
  }
  add("suites", await ml.listMlEvalSuites(org));
  add("eval runs", await ml.listMlEvalRuns(org));
  const models = await ml.listMlModels(org);
  add("models", models);
  for (const m of models.models) {
    const model = await ml.getMlModel({ ...org, modelId: m.id });
    add(`model ${m.id}`, model);
    for (const v of model.versions) add(`version ${v.id}`, await ml.getMlModelVersion({ ...org, versionId: v.id }));
  }
  const services = await ml.listMlInferenceServices(org);
  add("services", services);
  for (const s of services.services) add(`service ${s.id}`, await ml.getMlInferenceService({ ...org, serviceId: s.id }));
  add("org events", await ml.listMlEvents({ ...org, subject: { kind: MlSubjectKind.ORG, id: "" } }));
  add("org alerts", await ml.listMlAlerts({ ...org, subject: { kind: MlSubjectKind.ORG, id: "" } }));
  return out;
}

describe("the ML fixture world", () => {
  it("answers every list read with rows", async () => {
    expect((await ml.getMlFleet(org)).pools.length).toBeGreaterThan(0);
    expect((await ml.listMlRuns(org)).runs.length).toBeGreaterThan(5);
    expect((await ml.listMlFindings(org)).findings.length).toBeGreaterThan(10);
    expect((await ml.listMlModels(org)).models.length).toBeGreaterThan(0);
    expect((await ml.listMlInferenceServices(org)).services.length).toBeGreaterThan(0);
    expect((await ml.getMlStack(org)).clusters.length).toBe(2);
  });

  it("finds the hero run by its id and by its name", async () => {
    const byId = await ml.getMlRun({ ...org, run: RUNS.v3.id });
    const byName = await ml.getMlRun({ ...org, run: RUNS.v3.name });
    expect(byName.run?.id).toBe(byId.run?.id);
  });

  it("answers an unknown entity NotFound, never a made-up row", async () => {
    await expect(ml.getMlRun({ ...org, run: "run_nope" })).rejects.toMatchObject({ code: Code.NotFound });
    await expect(ml.getMlNode({ ...org, nodeId: "nope/nope" })).rejects.toMatchObject({ code: Code.NotFound });
  });

  it("resolves every subject any answer names", { timeout: 120_000 }, async () => {
    const answers = await everyAnswer();
    const seen = new Map<string, string>();
    for (const [name, message] of answers) for (const { at, s } of subjects(message)) seen.set(`${s.kind}|${s.id}`, `${name}${at}`);
    const broken: string[] = [];
    for (const [key, where] of seen) {
      const [kind, id] = [Number(key.split("|")[0]) as MlSubjectKind, key.slice(key.indexOf("|") + 1)];
      if (!(await resolves({ kind, id } as MlSubject))) broken.push(`${MlSubjectKind[kind]} ${id} (named at ${where})`);
    }
    expect(broken).toEqual([]);
    expect(seen.size).toBeGreaterThan(50);
  });

  it("puts nothing in the future, and survives protobuf binary", { timeout: 120_000 }, async () => {
    for (const [name, message] of await everyAnswer()) {
      const late = timestamps(message).filter((ms) => ms > NOW + 1000);
      expect(late, name).toEqual([]);
      const schema = (MlEarlyAccessService.methods.find((m) => m.output.typeName === message.$typeName)?.output ?? null) as DescMessage | null;
      if (schema) expect(() => toBinary(schema, message as never), name).not.toThrow();
    }
  });
});
