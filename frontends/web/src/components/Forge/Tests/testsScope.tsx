// Copyright (c) 2026 Reliant Labs

/**
 * What a Tests page is about, from the console: the forge project, and the
 * top bar's environment scope resolved to the control plane's environment id
 * (none for All). With one environment in scope, the pages read only the runs
 * recorded against it (a preview's e2e lane, say); All reads every run.
 *
 * Unlike the Security pages, a Tests page never waits for a deploy: CI
 * records runs before any release, so an environment that is not live only
 * narrows the read to nothing, and the page says so in its own words.
 */

import type { ReactNode } from "react";

import Card from "@/components/forge-ui/card";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";

import { Header, ScopedEnvNote } from "../Console/consolePageFrame";
import { useForgeConsole } from "../Console/consoleContext";
import { ENV_ALL } from "../Console/envScope";

export interface TestsScope {
  project: string;
  /** The scoped environment's control-plane id; undefined for All. */
  environmentId: string | undefined;
}

/** The scope, or the note (or loading state) the page shows instead while it cannot be resolved. */
export function useTestsScope(what: string): { scope: TestsScope } | { blocked: ReactNode } {
  const { forgeProject, env, roster } = useForgeConsole();
  if (env === ENV_ALL) return { scope: { project: forgeProject, environmentId: undefined } };
  if (roster.isLoading || roster.resolvingName) {
    return {
      blocked: (
        <Card padding="none" aria-label="Loading" aria-busy="true">
          <SkeletonLoader variant="table-row" count={4} />
        </Card>
      ),
    };
  }
  const scoped = roster.envs.find((candidate) => candidate.name === env) ?? null;
  if (!scoped?.live) return { blocked: <ScopedEnvNote scoped={scoped} what={what} /> };
  return { scope: { project: forgeProject, environmentId: scoped.live.id } };
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
