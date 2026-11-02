// Copyright (c) 2025 Reliant Labs

/**
 * What an observability page shows when it has no environment to scope to,
 * and the two reasons are not the same answer:
 *
 *   read failed   the roster's control-plane read did not answer (rosterReadFailed).
 *                 The page's CloudNotice says why; an unreachable read offers
 *                 Try again. Nothing is claimed about the project.
 *   none          the read answered, and the project has no environment
 *                 Reliant recorded: the getting-started steps.
 *
 * Rendering "No environments to observe yet" for the first was the 2026-11-01
 * report: control-plane's two prod environments, an aborted GetLiveView, and a
 * page telling its owner to deploy something.
 */

import { ObsEnvironmentsUnread, ObsNoEnvironments, type ObsTopic } from "../Observability/ObsStates";
import { rosterReadFailed, useForgeConsole } from "./consoleContext";

export function NoEnvironmentsState({ topic, testId = "observability-no-envs" }: { topic: ObsTopic; testId?: string }) {
  const { roster } = useForgeConsole();
  if (rosterReadFailed(roster)) {
    return roster.live.data?.availability === "unreachable" ? (
      <ObsEnvironmentsUnread onRetry={() => void roster.live.refetch()} retrying={roster.live.isFetching} />
    ) : null;
  }
  return (
    <div data-testid={testId}>
      <ObsNoEnvironments topic={topic} />
    </div>
  );
}
