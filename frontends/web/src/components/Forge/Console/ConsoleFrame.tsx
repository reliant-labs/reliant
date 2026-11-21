// Copyright (c) 2026 Reliant Labs

/**
 * The console's frame INSIDE THE ONE SHELL (U6): no nav column — the app's
 * sections are rows of the shell's left nav, nested under the open app
 * (Layout/sidebar/AppsNavSection.tsx) — just a slim bar with the breadcrumb
 * and the ONE environment selector, over the page.
 *
 * `forge-ui` is REQUIRED, not cosmetic: inside it forge's `accent` token
 * resolves to reliant's primary action colour; outside it, to reliant's muted
 * hover tint (src/index.css). `forge-console` raises the control edge to 3:1
 * so filters and toggles read as controls beside the charts.
 *
 * ConsoleShell stays the frame of the public demo and the dev previews, which
 * have no shell around them.
 */

import type { ReactNode } from "react";

import { PageMain, ShellPageHeader } from "@/components/Layout/shell/shellFrame";
import { AppPageProjectPicker } from "@/components/Layout/sidebar/AppPageProjectPicker";

import { useConsoleNavExtras } from "./consoleNavExtras";
import { ConsoleBreadcrumb, consoleCrumbs } from "./ConsoleShell";
import { EnvScopeControl } from "./EnvScopeControl";
import type { ScopeOption } from "./envScope";
import { pinnedEnvOf, type ConsolePage } from "./paths";
import { useRouteUsesEnvScope } from "./useConsoleNavModel";

export interface ConsoleFrameProps {
  project: string;
  page: ConsolePage;
  env: string;
  dashboardTitle?: string;
  scope: { long: ScopeOption[]; previews: ScopeOption[] };
  platformWide?: boolean;
  onEnvChange: (env: string) => void;
  children: ReactNode;
}

export function ConsoleFrame({ project, page, env, dashboardTitle, scope, platformWide, onEnvChange, children }: ConsoleFrameProps) {
  const { crumbs: crumbsOverride, envOrder } = useConsoleNavExtras();
  const rank = (name: string) => (envOrder?.includes(name) ? envOrder.indexOf(name) : (envOrder?.length ?? 0));
  const long = envOrder ? [...scope.long].sort((a, b) => rank(a.name) - rank(b.name)) : scope.long;
  const own = consoleCrumbs(project, page, env, dashboardTitle);
  const crumbs = crumbsOverride?.(page, own) ?? own;
  // A page that opted out of the env scope (useConsoleNavModel.ts) gets no selector.
  const envScoped = useRouteUsesEnvScope();

  return (
    <div className="forge-ui forge-console flex h-full min-h-0 w-full flex-col" data-testid="forge-console-frame">
      <ShellPageHeader
        testId="forge-console-header"
        actions={
          <>
          {envScoped && (
            <EnvScopeControl
              value={env}
              long={long}
              previews={scope.previews}
              onChange={onEnvChange}
              pinnedEnv={pinnedEnvOf(page)}
              platformWide={platformWide}
            />
          )}
          </>
        }
      >
        {/* Several projects in the shell: which one's app this page shows. */}
        <AppPageProjectPicker app={project} page={page} />
        <ConsoleBreadcrumb project={project} env={env} crumbs={crumbs} />
      </ShellPageHeader>
      <PageMain className="relative min-h-0 flex-1 overflow-y-auto px-8 pb-12 pt-6 max-md:px-4 max-md:pt-4">{children}</PageMain>
    </div>
  );
}
