// Copyright (c) 2026 Reliant Labs

/**
 * What a run detail tab is handed: the run as GetMlRun answered it, and the
 * scope the page is in — the whole run, one phase (the URL's
 * `/phases/<phase>`), or one attempt of it (`?attempt=n`); and a rank when
 * `?rank=` names one. Every tab reads its own data for that scope
 * (`subject`), so a tab can be mounted on its own.
 */

import { MlSubjectKind, type GetMlRunResponse, type MlAttempt, type MlPhase, type MlRun, type MlSubject } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { attemptId, phaseId, rankId } from "../kit/ids";

export interface RunScope {
  detail: GetMlRunResponse;
  run: MlRun;
  /** The phase in scope: the URL's, else the run's current phase. */
  phase: MlPhase | null;
  /** The attempt in scope (`?attempt=`); null for all attempts of the phase overlaid. */
  attempt: MlAttempt | null;
  rank: number | null;
  /** Whether the URL scoped the page to the phase (`/phases/<phase>`), not just defaulted to it. */
  phaseScoped: boolean;
  /** The subject every tab reads: the rank, attempt, phase, or run. */
  subject: Pick<MlSubject, "kind" | "id" | "label">;
}

/** The scope a run page is in, from the run and the URL's phase, attempt and rank. */
export function runScopeOf(detail: GetMlRunResponse, phaseKey: string | undefined, attemptN: number | undefined, rank: number | undefined): RunScope {
  const run = detail.run!;
  const phase = run.phases.find((p) => p.key === phaseKey) ?? run.phases.find((p) => p.key === run.currentPhaseKey) ?? run.phases[0] ?? null;
  const attempt = phase && attemptN ? (phase.attempts.find((a) => a.n === attemptN) ?? null) : null;
  const r = rank ?? null;
  let subject: RunScope["subject"] = { kind: MlSubjectKind.RUN, id: run.id, label: run.name };
  if (phase && phaseKey) subject = { kind: MlSubjectKind.PHASE, id: phaseId(run.id, phase.key), label: phase.label || phase.key };
  if (phase && attempt) subject = { kind: MlSubjectKind.ATTEMPT, id: attemptId(run.id, phase.key, attempt.n), label: `${phase.key} attempt ${attempt.n}` };
  if (phase && attempt && r !== null) subject = { kind: MlSubjectKind.RANK, id: rankId(run.id, phase.key, attempt.n, r), label: `rank ${r}` };
  return { detail, run, phase, attempt, rank: r, phaseScoped: !!phaseKey && phase?.key === phaseKey, subject };
}
