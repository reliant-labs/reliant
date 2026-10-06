// Copyright (c) 2025 Reliant Labs

/**
 * THE HEADER'S ONE STATUS LINE: the glance, visible from every tab.
 *
 * The Overview's state panel (LiveState) is the explanation — intent, who
 * promoted it, the drift verdict, the provenance. This is the summary a
 * reader takes in before choosing a tab, so it says exactly one thing.
 *
 * It is derived from the same fields LiveState reads and follows the same
 * rule: ABSENCE IS NEVER AGREEMENT. Only `converged` produces "Running";
 * "not confirmed yet" and "can't confirm" stay in the quiet register, and only
 * an actual reported failure is a problem. Freshness is the control plane's
 * call — this does not second-guess an `unknown` into something friendlier.
 *
 * The OWNER'S run state outranks the rollout reading: an environment its
 * owner stopped is "Stopped by you" whatever its last convergence said.
 *
 * No branch on environment kind, for the reason LiveState has none: the
 * reading is the platform's, for every kind. The caller passes null for an
 * environment that has nothing to read (unregistered, local).
 */

import { formatRelativeTime } from "@/lib/relativeTime";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import { declaredNotBuilt, neverBuilt, type LiveEnv } from "@/services/forge/live";

import { environmentStopped, runStateOf } from "../runStateVocabulary";

/** ok = confirmed good; progress = moving; problem = needs a look; quiet = nothing to say yet. */
export type HeadlineTone = "ok" | "progress" | "problem" | "quiet";

export interface EnvHeadline {
  tone: HeadlineTone;
  text: string;
  /** Why, in the platform's own words. Empty when the line says it all. */
  detail: string;
}

export function envHeadline(
  live: LiveEnv,
  status: CloudEnvStatus | undefined,
  now: number = Date.now()
): EnvHeadline {
  const workloads = status?.workloads ?? [];
  const runStates = workloads.map(runStateOf);

  if (environmentStopped(workloads)) {
    return runStates.some((state) => state.kind === "stopping")
      ? { tone: "progress", text: "Stopping…", detail: "" }
      : {
          tone: "quiet",
          text: "Stopped by you",
          detail: "Compute is off. Data and URLs are kept. Start it to bring it back.",
        };
  }
  const billing = runStates.find((state) => state.kind === "billing");
  if (billing) return { tone: "problem", text: billing.label, detail: billing.detail };

  if (declaredNotBuilt(live)) return { tone: "quiet", text: "Declared, not built yet", detail: "" };
  if (neverBuilt(live)) return { tone: "quiet", text: "Nothing released yet", detail: "" };

  switch (live.observed.state) {
    case "converged": {
      const when = live.observed.observedAt ? formatRelativeTime(live.observed.observedAt, now) : "";
      return { tone: "ok", text: when ? `Running · confirmed ${when}` : "Running · confirmed", detail: "" };
    }
    case "converging":
      return { tone: "progress", text: "Rolling out…", detail: "" };
    case "failed":
      return { tone: "problem", text: "Couldn't finish rolling out", detail: live.driftDetail };
    case "unknown":
      return { tone: "quiet", text: "Can't confirm what's running", detail: live.driftDetail };
    default:
      return { tone: "quiet", text: "Not confirmed running yet", detail: "" };
  }
}
