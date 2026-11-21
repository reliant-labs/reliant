// Copyright (c) 2026 Reliant Labs

/**
 * What a Tests page is about: the forge project, and the environment its
 * reads are narrowed to — none unless the reader chose one.
 *
 * THE CONSOLE'S ENVIRONMENT SCOPE DOES NOT APPLY HERE. Most runs belong to
 * no environment: CI and a local checkout record against a commit, and only
 * a lane run against a deployed environment (a preview's e2e, say) carries
 * one. Narrowing to the top bar's scope (prod, by default) would hide nearly
 * every run behind "no runs recorded against prod". So a Tests page reads
 * every run, and narrows to one environment only when the reader picks it:
 * the `environment` key of the page's own URL (the Runs page's Environment
 * filter), never the console's `?env=`.
 *
 * A chosen environment resolves through the project's roster to the control
 * plane's environment id; one that does not exist, or that Reliant has no
 * record of, says so and offers every environment instead.
 */

import type { ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";

import { Header } from "../Console/consolePageFrame";
import { useForgeConsole } from "../Console/consoleContext";
import { StateFrame } from "../Observability/ObsStates";

/** The Tests pages' own environment key (`?environment=<name>`): separate from the console's `?env=` scope. */
export const TESTS_ENV_KEY = "environment";

export interface TestsScope {
  project: string;
  /** The chosen environment's control-plane id; undefined (the default) reads every run. */
  environmentId: string | undefined;
  /** The chosen environment's name, as the URL holds it; undefined for every environment. */
  environment?: string;
}

/** The environment the page's URL chose, or undefined: every environment. */
export function chosenTestsEnv(raw: Record<string, unknown>): string | undefined {
  const value = raw[TESTS_ENV_KEY];
  return typeof value === "string" && value ? value : undefined;
}

/** The scope, or the note (or loading state) the page shows instead while a chosen environment cannot be resolved. */
export function useTestsScope(what: string): { scope: TestsScope } | { blocked: ReactNode } {
  const { forgeProject, roster } = useForgeConsole();
  const environment = chosenTestsEnv(useSearch({ strict: false }) as Record<string, unknown>);
  if (!environment) return { scope: { project: forgeProject, environmentId: undefined } };
  if (roster.isLoading || roster.resolvingName) {
    return {
      blocked: (
        <Card padding="none" aria-label="Loading" aria-busy="true">
          <SkeletonLoader variant="table-row" count={4} />
        </Card>
      ),
    };
  }
  const chosen = roster.envs.find((candidate) => candidate.name === environment) ?? null;
  if (!chosen?.live) return { blocked: <TestsEnvNote environment={environment} known={!!chosen} what={what} /> };
  return { scope: { project: forgeProject, environmentId: chosen.live.id, environment } };
}

/** A chosen environment the page cannot read: why, and the way back to every environment. */
function TestsEnvNote({ environment, known, what }: { environment: string; known: boolean; what: string }) {
  const navigate = useNavigate();
  const showAll = () =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, [TESTS_ENV_KEY]: undefined }), replace: true } as never);
  return (
    <StateFrame
      testId="tests-env-note"
      icon={null}
      title={known ? `Reliant has no record of ${environment} yet` : `There is no environment named ${environment}`}
      actions={
        <Button variant="outline" size="sm" onClick={showAll} data-testid="tests-env-note-all">
          Show every environment
        </Button>
      }
    >
      <p>
        {known
          ? `So no ${what} are recorded against it here. Register it from its environment page, or read every run.`
          : `This link names an environment the project does not have. Every run is still here.`}
      </p>
    </StateFrame>
  );
}

/** A Tests page: its header, then the body. The console frame puts the section's Learn link beside it (consoleLearn.ts). */
export function TestsFrame({ title, subtitle, meta, testId, children }: { title: string; subtitle?: ReactNode; meta?: ReactNode; testId: string; children: ReactNode }) {
  return (
    <div className="min-w-0 space-y-4" data-testid={testId}>
      <Header title={title} subtitle={subtitle} meta={meta} />
      {children}
    </div>
  );
}
