// Copyright (c) 2026 Reliant Labs

/**
 * The nav under the switcher (U6d, prototype): a nav config (the app's is
 * navConfig/hybrid.ts), drawn by the one renderer against the route and the
 * shell's scope, with the open app's console model (alerts, the analytics
 * hub, its env scope, the rows other modules register) when there is an app.
 *
 * The config is the caller's, never a default here: the app's config carries
 * Settings' sections (navConfig/settings.tsx), and a nav that is not the app's
 * (the public demo's) must not import them by reaching this file.
 */

import { memo, useLayoutEffect, useMemo, useState } from "react";

import { appScope, AppScopeProvider } from "@/fixtures/scope";

import { useGtmAccess } from "../../Gtm/shared/access";
import { consolePageOf, orgPageOf } from "../../Forge/Console/paths";
import type { GroupedNavInput } from "../../Forge/Console/consoleNavModel";
import { useConsoleNavModel, type ConsoleNavModel } from "../../Forge/Console/useConsoleNavModel";
import type { NavConfig, NavCtx } from "./navConfig/types";
import { NavRenderer, type NavigateOptions, type NavSlots } from "./NavRenderer";
import type { ShellProject } from "./shellProject";

export interface ShellNavBodyProps {
  /** The nav to draw: the app's `hybridNav`, or a host's own (navConfig/types.ts). */
  config: NavConfig;
  shell: ShellProject;
  pathname: string;
  search: Record<string, unknown>;
  slots: NavSlots;
  onNavigate: (href: string, options?: NavigateOptions) => void;
  /** Every row drawn as a top-level row (a phone's drawer; NavRenderer `flat`). */
  flat?: boolean;
  /** Leaves a drill-in in place instead of navigating (NavRenderer `onBack`). */
  onBack?: () => void;
  /** Draws the top level on an area's route too (NavRenderer `topLevel`). */
  topLevel?: boolean;
}

export function ShellNavBody(props: ShellNavBodyProps) {
  return props.shell.app ? <NavWithConsole app={props.shell.app} {...props} /> : <NavBody {...props} console={null} />;
}

function NavWithConsole({ app, ...props }: ShellNavBodyProps & { app: string }) {
  if (appScope(app)) return <NavWithScopedConsole app={app} {...props} />;
  return <NavWithLiveConsole app={app} {...props} />;
}

function NavWithLiveConsole({ app, ...props }: ShellNavBodyProps & { app: string }) {
  const model = useConsoleNavModel(app, props.pathname, props.search);
  return <NavBody {...props} console={model} />;
}

/**
 * An app whose console reads its own scope (the forge tour's sample,
 * fixtures/scope.tsx): its console model is read INSIDE that scope, so the
 * nav's sections and the page under them share one cache and never read
 * live, while the rest of the nav (its slots: chats, inbox) stays outside it,
 * on the app's transport. The model is lifted out before paint.
 */
function NavWithScopedConsole({ app, ...props }: ShellNavBodyProps & { app: string }) {
  const [model, setModel] = useState<ConsoleNavModel | null>(null);
  return (
    <>
      <AppScopeProvider app={app}>
        <ScopedConsoleModel app={app} pathname={props.pathname} search={props.search} onModel={setModel} />
      </AppScopeProvider>
      <NavBody {...props} console={model} />
    </>
  );
}

const ScopedConsoleModel = memo(function ScopedConsoleModel({
  app,
  pathname,
  search,
  onModel,
}: {
  app: string;
  pathname: string;
  search: Record<string, unknown>;
  onModel: (model: ConsoleNavModel) => void;
}) {
  const model = useConsoleNavModel(app, pathname, search);
  useLayoutEffect(() => onModel(model), [model, onModel]);
  return null;
});

function NavBody({ config, shell, pathname, search, slots, onNavigate, flat, onBack, topLevel, console }: ShellNavBodyProps & { console: GroupedNavInput | null }) {
  // GTM is org-gated by the server (Gtm/shared/access.ts): asked only by a nav
  // that has the area, and hidden until the answer is yes.
  const gtm = useGtmAccess({ ask: config.items.some((item) => item.id === "gtm") }).enabled;
  const ctx = useMemo<NavCtx>(
    () => ({
      pathname,
      search,
      mode: shell.mode,
      projectId: shell.projectId,
      projectName: shell.name,
      app: shell.app,
      console,
      page: consolePageOf(pathname)?.page ?? null,
      orgPage: orgPageOf(pathname),
      features: { gtm },
    }),
    [pathname, search, shell.mode, shell.projectId, shell.name, shell.app, console, gtm],
  );
  return <NavRenderer config={config} ctx={ctx} slots={slots} onNavigate={onNavigate} flat={flat} onBack={onBack} topLevel={topLevel} />;
}
