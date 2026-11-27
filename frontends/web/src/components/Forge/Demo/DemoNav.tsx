// Copyright (c) 2026 Reliant Labs

/**
 * The public demo's nav body, in the app's own frame (demoShellRoutes.tsx
 * mounts AppShellFrame with `nav={DemoNav}`): the shell's one nav column
 * (Layout/sidebar/ShellNavColumn.tsx) with the demo's config (demoNavConfig.ts)
 * and the demo's slots. The renderer, drill-in and phone drawer are the
 * app's; the scope header is static, so no live account/project controls
 * (or their token-dependent hooks) mount on this public host.
 *
 *  - No Find: the demo's keyboard host has no command palette to open.
 *  - One app: every page of the demo is the sample shop's (a link to another
 *    app lands on shop's page), so the nav always draws shop's sections and
 *    the switcher names shop, on the demo's own pages (/forge/demo) too, where
 *    the URL names no app: the scope is read there as shop's Overview's.
 *  - On a phone the drawer ends with the tour, Install Forge and the
 *    telemetry notice (DemoDrawerActions), which the page's action row only
 *    has room for from a tablet up.
 */

import { useMemo } from "react";

import type { ShellNavProps } from "@/components/Layout/shell/AppShell";
import type { NavSlots } from "@/components/Layout/sidebar/NavRenderer";
import { ShellNavColumn } from "@/components/Layout/sidebar/ShellNavColumn";
import { useShellProject, type ShellProject } from "@/components/Layout/sidebar/shellProject";
import { useNarrowViewport } from "@/hooks/useNarrowViewport";

import { consolePageOf, consolePath } from "../Console/paths";
import { FIXTURE_PROJECT } from "../Observability/__fixtures__/fixtureApi";
import { DemoDrawerActions } from "./DemoScreenActions";
import { demoNav } from "./demoNavConfig";
import { SidebarOrgRow, SidebarProjectLabel } from "@/components/Layout/sidebar/primitives";
import { ForgeBrand } from "@/components/ForgeShell/ForgeBrand";

/** Where the shell reads its scope: a console page's own path, else (a page of the demo's own) shop's Overview. */
const SHOP_HOME = consolePath(FIXTURE_PROJECT, { kind: "overview" });

export function DemoNav({ pathname, search, onNavigate }: ShellNavProps) {
  const resolved = useShellProject(consolePageOf(pathname) ? pathname : SHOP_HOME, search);
  const shell = useMemo<ShellProject>(() => ({ ...resolved, mode: "one", ids: resolved.projectId ? [resolved.projectId] : [], app: FIXTURE_PROJECT, deployed: true }), [resolved]);
  const narrow = useNarrowViewport();
  const slots = useMemo<NavSlots>(() => ({ "host-actions": () => (narrow ? <DemoDrawerActions /> : null) }), [narrow]);
  return (
    <ShellNavColumn config={demoNav} slots={slots} shell={shell} pathname={pathname} search={search} onNavigate={onNavigate} flat={narrow} find={false}
      header={<><SidebarOrgRow><ForgeBrand /></SidebarOrgRow><SidebarProjectLabel name={FIXTURE_PROJECT} testId="demo-project-label" /></>} />
  );
}

export default DemoNav;
