// Copyright (c) 2026 Reliant Labs

/**
 * The nav under the switcher (U6d, prototype): the owner's hybrid config
 * (navConfig/hybrid.ts), drawn by the one renderer against the route and the
 * shell's scope, with the open app's console model (alerts, the analytics
 * hub, its env scope, the rows other modules register) when there is an app.
 */

import { useMemo } from "react";

import { consolePageOf, orgPageOf } from "../../Forge/Console/paths";
import type { GroupedNavInput } from "../../Forge/Console/groupedNav";
import { useConsoleNavModel } from "../../Forge/Console/useConsoleNavModel";
import { isForgeUIEnabled } from "../../../lib/forgeFeature";
import { hybridNav, type NavCtx } from "./navConfig";
import { forgeOffNav } from "./navConfig/hybrid";
import { NavRenderer, type NavigateOptions, type NavSlots } from "./NavRenderer";
import type { ShellProject } from "./shellProject";

export interface ShellNavBodyProps {
  shell: ShellProject;
  pathname: string;
  search: Record<string, unknown>;
  slots: NavSlots;
  onNavigate: (href: string, options?: NavigateOptions) => void;
}

export function ShellNavBody(props: ShellNavBodyProps) {
  // The forge UI's off-switch (lib/forgeFeature.ts) takes the app pages out of the nav.
  if (!isForgeUIEnabled()) return <NavBody {...props} shell={{ ...props.shell, app: null }} console={null} forgeUI={false} />;
  return props.shell.app ? <NavWithConsole app={props.shell.app} {...props} /> : <NavBody {...props} console={null} />;
}

function NavWithConsole({ app, ...props }: ShellNavBodyProps & { app: string }) {
  const model = useConsoleNavModel(app, props.pathname, props.search);
  return <NavBody {...props} console={model} />;
}

function NavBody({ shell, pathname, search, slots, onNavigate, console, forgeUI = true }: ShellNavBodyProps & { console: GroupedNavInput | null; forgeUI?: boolean }) {
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
    }),
    [pathname, search, shell.mode, shell.projectId, shell.name, shell.app, console],
  );
  return <NavRenderer config={forgeUI ? hybridNav : forgeOffNav} ctx={ctx} slots={slots} onNavigate={onNavigate} />;
}
