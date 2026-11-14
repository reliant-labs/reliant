// Copyright (c) 2026 Reliant Labs

/**
 * The led Releases page's one sentence (Console/ledConsole.tsx), from the
 * release model alone: what the environment in scope runs (prod when every
 * environment is in scope), who promoted it, and what its checks said. Null
 * while there is no environment to speak of.
 */

import type { Summary } from "../Observability/Leads/leadStory";
import { referenceEnv, shortVersion, type ReleaseModel } from "./releaseModel";

export function releasesSummary(model: ReleaseModel, env: string | null): Summary | null {
  const target = env ? model.pipeline.find((cell) => cell.env.name === env) : model.pipeline.find((cell) => cell.env.id === referenceEnv(model.longEnvs)?.id);
  if (!target) {
    if (!model.rows.length) return { tone: "good", text: "No release has been cut yet." };
    return null;
  }
  const name = target.env.name;
  if (!target.base) {
    const cut = model.rows.length;
    return { tone: "good", text: `Nothing is deployed to ${name} yet. ${cut ? `${cut === 1 ? "One release is" : `${cut} releases are`} ready to promote.` : ""}`.trim() };
  }
  const row = model.rows.find((candidate) => candidate.release.version === target.base);
  const by = target.by ? `, promoted by ${target.by}` : "";
  const checks = row?.checks;
  const verdict = !checks || !checks.total
    ? ""
    : checks.failed
      ? ` ${checks.failed === 1 ? "One of its checks" : `${checks.failed} of its ${checks.total} checks`} failed.`
      : ` Its ${checks.total === 1 ? "check" : `${checks.total} checks`} passed.`;
  const newer = target.relation.kind === "newer" ? ` ${target.relation.count === 1 ? "A newer release is" : `${target.relation.count} newer releases are`} ready to promote.` : "";
  return { tone: checks?.failed ? "bad" : "good", text: `${name} runs ${shortVersion(target.base)}${by}.${verdict}${newer}` };
}
