// Copyright (c) 2026 Reliant Labs

/**
 * The public demo's nav body, in the app's own frame (DemoShellFrame mounts
 * AppShellFrame with `nav={DemoNav}`): the app's nav column with its chats
 * (Layout/ChatShellNav.tsx, the code the signed-in app's Sidebar draws) and
 * the demo's config (demoNavConfig.ts). The header is the app's too: the org
 * row (the sample org, hooks/useOrgs.demo.ts), Find (the command palette,
 * DemoShellFrame's overlays) and the project switcher over the sample
 * projects. The chat list, New chat, the Inbox count and the Workflows pill
 * read the sample shop's data over the demo's transport; what writes is
 * refused there.
 *
 *  - The shop's sections: every console page of the demo is the sample
 *    shop's (a link to another app lands on shop's page), so the nav's
 *    sections always open shop's, whichever project the switcher names, and
 *    always for one project. On the demo's own pages (/forge/demo), where the
 *    URL names no project, the scope is read as shop's Overview's.
 *  - On a phone the drawer ends with the tour, Install Forge and the
 *    telemetry notice (DemoDrawerActions), which the page's action row only
 *    has room for from a tablet up.
 */

import { useMemo } from "react";

import { ChatShellNav } from "@/components/Layout/ChatShellNav";
import type { ShellNavProps } from "@/components/Layout/shell/AppShell";
import type { NavSlots } from "@/components/Layout/sidebar/NavRenderer";
import type { ShellProject } from "@/components/Layout/sidebar/shellProject";
import { useNarrowViewport } from "@/hooks/useNarrowViewport";

import { consolePath } from "../Console/paths";
import { FIXTURE_PROJECT } from "../Observability/__fixtures__/fixtureApi";
import { DemoDrawerActions } from "./DemoScreenActions";
import { demoNav } from "./demoNavConfig";
import { FORGE_DEMO_PATH } from "./demoPath";

/** Where the shell reads its scope on the demo's own pages: shop's Overview. */
const SHOP_HOME = consolePath(FIXTURE_PROJECT, { kind: "overview" });

/** One project, and shop's sections, whatever the route or the switcher picked. */
const shopSections = (shell: ShellProject): ShellProject => ({
  ...shell,
  mode: "one",
  ids: shell.projectId ? [shell.projectId] : [],
  app: FIXTURE_PROJECT,
  deployed: true,
});

export function DemoNav({ pathname, search, onNavigate, onOpenRuns, onOpenChatSearch }: ShellNavProps) {
  const narrow = useNarrowViewport();
  const hostSlots = useMemo<NavSlots>(() => ({ "host-actions": () => (narrow ? <DemoDrawerActions /> : null) }), [narrow]);
  const scope = useMemo(() => ({ pathname: pathname === FORGE_DEMO_PATH ? SHOP_HOME : pathname, adjust: shopSections }), [pathname]);
  return (
    <ChatShellNav
      config={demoNav}
      hostSlots={hostSlots}
      scope={scope}
      pathname={pathname}
      search={search}
      onNavigate={onNavigate}
      onOpenRuns={onOpenRuns}
      onOpenChatSearch={onOpenChatSearch}
    />
  );
}

export default DemoNav;
