// Copyright (c) 2026 Reliant Labs

/**
 * The findings fixture (fixtures/shop/model/ml/findings.ts) implements every
 * world.ts FINDINGS id exactly once, completely, and every subject it names
 * is well formed and points at an entity of the world.
 */

import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { ListMlFindingsResponseSchema, MlFindingState, MlFixKind, MlSubjectKind, type MlSubject } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { findingsAnswers } from "@/fixtures/shop/model/ml/findings";
import { allDevices, fleetNodes } from "@/fixtures/shop/model/ml/fleet";
import { ALL_POOLS, CLUSTERS, EVAL_RUNS, FINDINGS, periodicEvalRunId, QUEUES, REVISIONS, RUNS, SERVICES, V3_ATTEMPTS, VERSIONS } from "@/fixtures/shop/model/ml/world";

const now = () => Date.UTC(2026, 10, 26, 15, 0);
const list = (includeClosed: boolean) => create(ListMlFindingsResponseSchema, findingsAnswers.listMlFindings!({ orgId: "o", includeClosed } as never, now) as never);

const runIds = new Set<string>(Object.values(RUNS).map((r) => r.id));
const phases = new Set(["pretrain", "sft", "eval-periodic", "rl", "dpo", "quantize"]);

function resolves(s: MlSubject): boolean {
  const p = s.id.split("/");
  switch (s.kind) {
    case MlSubjectKind.NODE:
      return fleetNodes().some((n) => n.id === s.id);
    case MlSubjectKind.DEVICE:
      return allDevices().some((d) => d.uuid === s.id);
    case MlSubjectKind.POOL:
      return ALL_POOLS.some((x) => x.id === s.id);
    case MlSubjectKind.CLUSTER:
      return Object.values(CLUSTERS).some((c) => c.id === s.id);
    case MlSubjectKind.RUN:
      return runIds.has(s.id);
    case MlSubjectKind.PHASE:
      return p.length === 2 && runIds.has(p[0]) && phases.has(p[1]);
    case MlSubjectKind.ATTEMPT:
      return p.length === 3 && V3_ATTEMPTS.some((a) => `${RUNS.v3.id}/${a.phase}/${a.n}` === s.id);
    case MlSubjectKind.RANK:
      return p.length === 4 && V3_ATTEMPTS.some((a) => `${RUNS.v3.id}/${a.phase}/${a.n}` === p.slice(0, 3).join("/")) && Number(p[3]) >= 0 && Number(p[3]) < 64;
    case MlSubjectKind.CHECKPOINT:
      return p.length === 3 && runIds.has(p[0]) && Number.isInteger(Number(p[2]));
    case MlSubjectKind.INFERENCE_SERVICE:
      return Object.values(SERVICES).some((x) => x.id === s.id);
    case MlSubjectKind.REVISION:
      return (Object.values(REVISIONS) as string[]).includes(s.id);
    case MlSubjectKind.REPLICA:
      return Object.values(SERVICES).some((x) => s.id.startsWith(`${x.id}/`));
    case MlSubjectKind.MODEL_VERSION:
      return Object.values(VERSIONS).some((v) => v.id === s.id);
    case MlSubjectKind.EVAL_RUN:
      return (Object.values(EVAL_RUNS) as string[]).includes(s.id) || /^eval_01JDVPERIODIC\d+$/.test(s.id) || s.id === periodicEvalRunId(0);
    case MlSubjectKind.QUEUE:
      return Object.values(QUEUES).some((q) => q.id === s.id);
    case MlSubjectKind.WORKLOAD:
    case MlSubjectKind.POD:
      return s.id.length > 0;
    default:
      return false;
  }
}

describe("findings fixture", () => {
  it("implements every world.ts finding id exactly once", () => {
    const ids = list(true).findings.map((f) => f.id);
    expect(ids.sort()).toEqual(Object.values(FINDINGS).sort());
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("hides fixed and dismissed findings unless asked", () => {
    const open = list(false).findings;
    expect(open.some((f) => f.state === MlFindingState.FIXED || f.state === MlFindingState.DISMISSED)).toBe(false);
    expect(open.length).toBe(Object.values(FINDINGS).length - 2);
    const all = list(true).findings;
    expect(all.filter((f) => f.state === MlFindingState.ASSIGNED).map((f) => f.assignee)).toContain("maya");
    expect(all.find((f) => f.state === MlFindingState.FIX_PROPOSED)?.prUrl).toBe("https://github.com/acme/shop/pull/412");
    expect(all.some((f) => f.state === MlFindingState.SNOOZED)).toBe(true);
  });

  it("every finding is complete and every subject resolves to the world", () => {
    for (const f of list(true).findings) {
      expect(f.title, f.id).toBeTruthy();
      expect(f.why, f.id).toBeTruthy();
      expect(f.rule, f.id).toBeTruthy();
      expect(f.impact?.text, f.id).toBeTruthy();
      expect(f.subjects.length, f.id).toBeGreaterThan(0);
      expect(f.clusterIds.length, f.id).toBeGreaterThan(0);
      expect(f.fix?.summary, f.id).toBeTruthy();
      if (f.fix?.kind === MlFixKind.KCL_PR) {
        expect(f.fix.kclPath, f.id).toBeTruthy();
        expect(f.fix.kclDiff, f.id).toMatch(/^--- a\/.+\n\+\+\+ b\/.+\n@@/);
      } else expect(f.fix?.command, f.id).toBeTruthy();
      for (const s of [...f.subjects, ...(f.evidenceTable?.rows.flatMap((r) => (r.subject ? [r.subject] : [])) ?? [])]) expect(resolves(s), `${f.id}: ${MlSubjectKind[s.kind]} ${s.id}`).toBe(true);
    }
  });

  it("rolls telemetry coverage up", () => {
    const r = list(false);
    expect(r.coverageRatio).toBeGreaterThan(0.5);
    expect(r.coverageRatio).toBeLessThan(1);
  });
});
