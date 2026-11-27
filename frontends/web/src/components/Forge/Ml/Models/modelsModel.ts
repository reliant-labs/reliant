// Copyright (c) 2026 Reliant Labs

/**
 * The Models pages' view-models: registry rows (with the candidate from the
 * eval matrix — a checkpoint is a candidate before it is a version), the
 * scorecard of candidate against production, versions' provenance and the
 * numbers model registries write their own way (decimal GB, "8.03B").
 */

import {
  MlSubjectKind,
  MlVersionSource,
  type GetMlEvalMatrixResponse,
  type MlAlias,
  type MlEvalCell,
  type MlEvalRow,
  type MlEvalTask,
  type MlModel,
  type MlModelVersion,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import type { MlTone } from "../kit/labels";
import { mlHref, subjectHref } from "../kit/links";

/** A digest's first 12 hex characters: "56ad1e9f8c7b". */
export const shortDigest = (digest: string) => digest.replace(/^sha256:/, "").slice(0, 12);

/** Bytes as a registry shows them (decimal): "16.1 GB", "826 B". */
export function fmtGB(bytes: bigint | number): string {
  const n = Number(bytes);
  if (!Number.isFinite(n)) return "—";
  if (n >= 1e9) return `${Number((n / 1e9).toFixed(n >= 1e11 ? 0 : n >= 1e10 ? 1 : 2))} GB`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)} MB`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)} kB`;
  return `${n} B`;
}

/** Parameters: 8,030,261,248 → "8.03B", 567,754,752 → "568M". */
export function fmtParams(n: bigint | number): string {
  const v = Number(n);
  if (!v) return "—";
  if (v >= 1e9) return `${(v / 1e9).toFixed(2)}B`;
  if (v >= 1e6) return `${Math.round(v / 1e6)}M`;
  return `${v}`;
}

/** The version part of "<model>/<version>". */
export const versionOf = (versionId: string) => versionId.split("/").slice(1).join("/") || versionId;

export const gateTone = (status: string): MlTone => (status === "pass" ? "success" : status === "fail" ? "danger" : status === "pending" ? "warning" : "neutral");

export const gateWord = (status: string) => (status === "" ? "no gate" : status);

export const SOURCE_LABEL: Record<number, string> = {
  [MlVersionSource.TRAINED]: "trained",
  [MlVersionSource.PUSHED]: "pushed by hand",
  [MlVersionSource.IMPORTED]: "imported",
};

/** An alias of a model by name. */
export const aliasOf = (model: Pick<MlModel, "aliases">, name: string): MlAlias | undefined => model.aliases.find((a) => a.name === name);

/** The matrix row a model's candidate is (aliased "candidate"), newest first. */
export function candidateRow(matrix: GetMlEvalMatrixResponse | undefined): MlEvalRow | undefined {
  return matrix?.rows.filter((r) => r.alias === "candidate").sort((a, b) => Number(b.evaluatedAt?.seconds ?? 0n) - Number(a.evaluatedAt?.seconds ?? 0n))[0];
}

export interface ScoreLine {
  task: MlEvalTask;
  production: MlEvalCell | undefined;
  candidate: MlEvalCell | undefined;
}

/**
 * The scorecard (02-views §5.2): each task's production score and the
 * candidate's, whose cell already carries its delta and significance
 * against the matrix baseline (production by default).
 */
export function scorecard(matrix: GetMlEvalMatrixResponse | undefined): { candidate: MlEvalRow | undefined; production: MlEvalRow | undefined; lines: ScoreLine[] } {
  const candidate = candidateRow(matrix);
  const production = matrix?.rows.find((r) => r.id === matrix.baselineRowId);
  const cell = (row: string | undefined, task: string) => matrix?.cells.find((c) => c.rowId === row && c.taskKey === task);
  return {
    candidate,
    production,
    lines: (matrix?.suite?.tasks ?? []).map((task) => ({ task, production: cell(production?.id, task.key), candidate: cell(candidate?.id, task.key) })),
  };
}

/** Where a version came from: "llama-8b-support-v2 · sft attempt 1", with links. */
export function provenance(v: MlModelVersion, runNames: Readonly<Record<string, string>>): { label: string; href: string | null }[] {
  if (v.source === MlVersionSource.TRAINED && v.runId) {
    const run = runNames[v.runId] ?? v.runId;
    return [
      { label: run, href: mlHref("runs", [run]) },
      { label: `${v.phaseKey} a${v.attempt}`, href: mlHref("runs", [v.runId, "phases", v.phaseKey], { attempt: v.attempt }) },
    ];
  }
  if (v.source === MlVersionSource.IMPORTED) return [{ label: `${v.baseModel} (Hugging Face)`, href: null }];
  return [{ label: `pushed by ${v.createdBy}`, href: null }];
}

/** The checkpoint a version was registered from, as a link. */
export function checkpointLink(v: MlModelVersion): { label: string; href: string | null } | null {
  if (!v.checkpointId) return null;
  const [, phase, step] = v.checkpointId.split("/");
  return { label: `${phase}@${Number(step).toLocaleString("en-US")}`, href: subjectHref({ kind: MlSubjectKind.CHECKPOINT, id: v.checkpointId }) };
}

/** A version's share of traffic in an env (0 when it does not serve there). */
export const shareIn = (v: Pick<MlModelVersion, "serving">, env: string) => v.serving.filter((s) => s.env === env).reduce((sum, s) => sum + s.trafficShare, 0);
