// Copyright (c) 2025 Reliant Labs

/**
 * What a Security page is about, from the console: the forge project, and the
 * top bar's environment scope resolved to the control plane's environment id
 * (none for All). The scope is the pages' environment filter: the console has
 * one environment selector, and a second one here would be a second home for
 * the same choice.
 */

import type { ReactNode } from "react";

import Card from "@/components/forge-ui/card";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";

import { Header, ScopedEnvNote } from "../Console/consolePageFrame";
import { useForgeConsole } from "../Console/consoleContext";
import { ENV_ALL } from "../Console/envScope";
import { CloudNotice } from "../SourceNotices";

export interface SecurityScope {
  project: string;
  /** The scoped environment's control-plane id; undefined for All. */
  environmentId: string | undefined;
}

/** The scope, or the note (or loading state) the page shows instead while it cannot be resolved. */
export function useSecurityScope(what: string): { scope: SecurityScope } | { blocked: ReactNode } {
  const { forgeProject, env, roster } = useForgeConsole();
  if (roster.isLoading || roster.resolvingName) {
    return {
      blocked: (
        <Card padding="none" aria-label="Loading" aria-busy="true">
          <SkeletonLoader variant="table-row" count={4} />
        </Card>
      ),
    };
  }
  if (env === ENV_ALL) return { scope: { project: forgeProject, environmentId: undefined } };
  const scoped = roster.envs.find((candidate) => candidate.name === env) ?? null;
  if (!scoped?.live) {
    return {
      blocked: (
        <>
          <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />
          <ScopedEnvNote scoped={scoped} what={what} />
        </>
      ),
    };
  }
  return { scope: { project: forgeProject, environmentId: scoped.live.id } };
}

export function SecurityFrame({ title, subtitle, testId, children }: { title: string; subtitle?: ReactNode; testId: string; children: ReactNode }) {
  return (
    <div className="min-w-0 space-y-4" data-testid={testId}>
      <Header title={title} subtitle={subtitle} />
      {children}
    </div>
  );
}
