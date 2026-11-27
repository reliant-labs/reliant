// Copyright (c) 2026 Reliant Labs

/** The Models and lineage view-models over the fixture world's answers. */

import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { GetMlEvalMatrixResponseSchema, GetMlLineageResponseSchema, GetMlModelResponseSchema, MlSubjectKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { evalsAnswers } from "@/fixtures/shop/model/ml/evals";
import { lineageAnswers } from "@/fixtures/shop/model/ml/lineage";
import { modelsAnswers } from "@/fixtures/shop/model/ml/models";
import { EVAL_SUITES, MODELS, phaseId, RUNS, V3_CANDIDATE_CHECKPOINT, VERSIONS } from "@/fixtures/shop/model/ml/world";

import { focusIds, layoutLineage, NODE_W } from "../lineageModel";
import { aliasOf, candidateRow, checkpointLink, fmtGB, fmtParams, provenance, scorecard, shareIn, shortDigest, versionOf } from "../modelsModel";

const now = () => Date.UTC(2026, 10, 26, 15, 0);
const model = create(GetMlModelResponseSchema, modelsAnswers.getMlModel!({ modelId: MODELS.support.id } as never, now) as never);
const matrix = create(GetMlEvalMatrixResponseSchema, evalsAnswers.getMlEvalMatrix!({ suiteId: EVAL_SUITES.gate.id, modelId: MODELS.support.id } as never, now) as never);
const lineage = (kind: MlSubjectKind, id: string) => create(GetMlLineageResponseSchema, lineageAnswers.getMlLineage!({ subject: { kind, id } } as never, now) as never);

describe("models", () => {
  it("formats registry numbers", () => {
    expect(fmtGB(16_060_556_376)).toBe("16.1 GB");
    expect(fmtGB(8_497_515_280)).toBe("8.5 GB");
    expect(fmtGB(826)).toBe("826 B");
    expect(fmtParams(8_030_261_248n)).toBe("8.03B");
    expect(fmtParams(567_754_752n)).toBe("568M");
    expect(shortDigest(VERSIONS.v2.digest)).toBe("56ad1e9f8c7b");
    expect(versionOf(VERSIONS.v21fp8.id)).toBe("v2.1-fp8");
  });

  it("reads aliases, the candidate and the scorecard", () => {
    expect(aliasOf(model.model!, "production")?.versionId).toBe(VERSIONS.v2.id);
    expect(candidateRow(matrix)?.id).toBe(V3_CANDIDATE_CHECKPOINT);
    const card = scorecard(matrix);
    expect(card.production?.id).toBe(VERSIONS.v2.id);
    expect(card.lines).toHaveLength(8);
    const gsm = card.lines.find((l) => l.task.key === "gsm8k")!;
    expect(gsm.candidate).toMatchObject({ significant: true, regression: true, gateFail: true });
  });

  it("links each version to where it came from", () => {
    const v2 = model.versions.find((v) => v.id === VERSIONS.v2.id)!;
    const from = provenance(v2, { [RUNS.v2.id]: RUNS.v2.name });
    expect(from[0]).toEqual({ label: RUNS.v2.name, href: expect.stringContaining(`/runs/${RUNS.v2.name}`) });
    expect(from[1].href).toContain(`/runs/${RUNS.v2.id}/phases/sft`);
    expect(checkpointLink(v2)?.label).toBe("sft@60,000");
    const fp8 = model.versions.find((v) => v.id === VERSIONS.v21fp8.id)!;
    expect(shareIn(fp8, "prod")).toBeCloseTo(0.1, 10);
    expect(shareIn(fp8, "staging")).toBe(1);
    expect(provenance(model.versions.find((v) => v.id === VERSIONS.v1.id)!, {})[0].label).toBe("pushed by jon");
  });
});

describe("lineage layout", () => {
  it("runs every edge left to right and keeps nodes apart", () => {
    for (const [kind, id] of [
      [MlSubjectKind.MODEL, MODELS.support.id],
      [MlSubjectKind.RUN, RUNS.v3.id],
      [MlSubjectKind.MODEL_VERSION, VERSIONS.v2.id],
    ] as const) {
      const g = lineage(kind, id);
      const layout = layoutLineage(g.nodes, g.edges);
      const at = new Map(layout.nodes.map((n) => [n.id, n]));
      expect(layout.edges).toHaveLength(g.edges.length);
      for (const e of layout.edges) expect(at.get(e.edge.to)!.x).toBeGreaterThan(at.get(e.edge.from)!.x);
      // No two nodes overlap.
      for (const a of layout.nodes) for (const b of layout.nodes) if (a !== b && a.x === b.x) expect(Math.abs(a.y - b.y)).toBeGreaterThanOrEqual(46);
      expect(layout.width).toBeGreaterThanOrEqual(NODE_W);
    }
  });

  it("spreads chained phases into sub-columns and names the columns", () => {
    const g = lineage(MlSubjectKind.RUN, RUNS.v3.id);
    const layout = layoutLineage(g.nodes, g.edges);
    const x = (id: string) => layout.nodes.find((n) => n.id === id)!.x;
    expect(x(phaseId(RUNS.v3.id, "sft"))).toBeGreaterThan(x(phaseId(RUNS.v3.id, "pretrain")));
    expect(layout.headers.map((h) => h.label)).toEqual(["Inputs", "Phases", "Checkpoints", "Versions"]);
  });

  it("focuses the subject, or the phase that holds it", () => {
    const g = lineage(MlSubjectKind.RUN, RUNS.v3.id);
    expect([...focusIds({ kind: MlSubjectKind.ATTEMPT, id: `${RUNS.v3.id}/sft/3` }, g.nodes)]).toEqual([phaseId(RUNS.v3.id, "sft")]);
    expect(focusIds({ kind: MlSubjectKind.RUN, id: RUNS.v3.id }, g.nodes).size).toBeGreaterThan(2);
    expect([...focusIds({ kind: MlSubjectKind.CHECKPOINT, id: V3_CANDIDATE_CHECKPOINT }, g.nodes)]).toEqual([V3_CANDIDATE_CHECKPOINT]);
  });
});
