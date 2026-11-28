// Copyright (c) 2026 Reliant Labs

/**
 * The phone's nav (U6e, owner: "the mobile left nav, on chats, is the old
 * nav"): the shell's own nav column (ShellNavColumn) with the app's hybrid
 * config, drawn flat, inside the phone's drawer (Mobile/MobileNavDrawer),
 * exactly as the narrow window's drawer draws it (Layout/Sidebar.tsx): the org
 * row, Find, the project switcher, then the rows.
 *
 * The phone's own screens (`/m/*`) are read as the desktop page they stand
 * for (mobile chats are the chat home, `/m/inbox` the Inbox, …), so a row
 * lights and Agents drills in there just as on the desktop, its chat list
 * inline (Mobile/MobileNavChatList). Every row links to the desktop page; on
 * a phone the router hands a page with a phone twin to that twin
 * (lib/mobileRedirect), and the rest (an app's console, Projects) are one
 * responsive page, so there is one nav and no legacy one to fall back to.
 *
 * What the phone adds is only what `/m/*` lacks: no keyboard host (Find opens
 * the phone's Search screen), its own drawer to close (`onClose`), and Back
 * from a drill-in shown in place (reliant #3264).
 */

import { useCallback, useMemo, useState } from "react";
import { SquarePen } from "lucide-react";

import {
  chatPath,
  encodedSegment,
  homePath,
  inboxPath,
  mobileChatPath,
  mobileChatsPath,
  mobileChatWorkflowPath,
  mobileHomePath,
  mobileInboxPath,
  mobileMachinesPath,
  mobileNewChatPath,
  mobileRunsPath,
  mobileSearchPath,
  mobileSettingsPath,
  mobileWorkflowPath,
  mobileWorkflowsPath,
  newChatPath,
  runPath,
  runsPath,
  sandboxesPath,
  settingsPath,
  workflowDetailPath,
  workflowLibraryPath,
} from "@/lib/paths";
import { orgFreePath } from "@/lib/paths/org";

import { InboxNavItem, SandboxesHousekeepingDot } from "../../inbox/InboxNavItem";
import { MobileNavChatList } from "../../Mobile/MobileNavChatList";
import { hybridNav } from "./navConfig";
import { releaseNavDrawerHold } from "./navDrawerHold";
import { isAtOrBelow } from "./navLocation";
import type { NavigateOptions, NavSlots } from "./NavRenderer";
import { SidebarSubRow } from "./primitives";
import { ShellNavColumn } from "./ShellNavColumn";
import { useShellProject } from "./shellProject";

const atOrBelow = (path: string, base: string) => path === base || path.startsWith(`${base}/`);

/** The desktop page a phone screen stands for, so the one nav model reads it; any other path as it is. */
export function desktopPathOfMobile(pathname: string): string {
  const path = orgFreePath(pathname).replace(/\/+$/, "") || "/";
  const chatPrefix = mobileChatPath("");
  if (path.startsWith(chatPrefix)) {
    const id = path.slice(chatPrefix.length).split("/")[0];
    if (!id) return homePath();
    // A workflow chat's run (`/m/chats/<id>/workflow`) is the phone's run page: the desktop's run of that chat.
    if (path === mobileChatWorkflowPath(encodedSegment(id))) return runPath(encodedSegment(id));
    return chatPath(encodedSegment(id));
  }
  if (path === mobileHomePath() || path === mobileChatsPath()) return homePath();
  if (path === mobileNewChatPath()) return newChatPath();
  if (atOrBelow(path, mobileInboxPath())) return inboxPath();
  if (atOrBelow(path, mobileRunsPath())) return runsPath();
  // One workflow's screen is that workflow's page in the Library; the catalog is the Library.
  const workflowPrefix = mobileWorkflowPath("");
  if (path.startsWith(workflowPrefix)) {
    const name = path.slice(workflowPrefix.length).split("/")[0];
    if (name) return workflowDetailPath(encodedSegment(name));
  }
  if (atOrBelow(path, mobileWorkflowsPath())) return workflowLibraryPath();
  if (atOrBelow(path, mobileMachinesPath())) return sandboxesPath();
  if (atOrBelow(path, mobileSettingsPath())) return settingsPath();
  return pathname;
}

export function MobileHybridNav({
  pathname: phonePathname,
  search,
  onNavigate,
  onClose,
}: {
  /** The page on screen (a phone screen or a desktop page). */
  pathname: string;
  search: Record<string, unknown>;
  /** Opens a page; `drill` keeps the drawer open to show the area's rows. */
  onNavigate: (href: string, options?: NavigateOptions) => void;
  /** Closes the drawer: after a pick in the header, which may leave the route as it is. */
  onClose: () => void;
}) {
  const pathname = desktopPathOfMobile(phonePathname);
  // Back from a drill-in shows the top level here, in place (reliant #3264):
  // the desktop nav's Back goes to a desktop page (the Overview, Projects),
  // which on a phone left the phone shell and took the drawer with it. It is
  // kept for the screen it was pressed on; any row the person then opens,
  // the area they left included, drills in again as before.
  const [topLevelAt, setTopLevelAt] = useState<string | null>(null);
  const topLevel = topLevelAt === phonePathname;
  const showTopLevel = useCallback(() => setTopLevelAt(phonePathname), [phonePathname]);
  const navigate = useCallback(
    (...args: Parameters<typeof onNavigate>) => {
      setTopLevelAt(null);
      onNavigate(...args);
    },
    [onNavigate],
  );
  // A pick in the header (a project, All projects) closes the drawer even when the route stays.
  const closeAfterPick = useCallback(() => {
    releaseNavDrawerHold();
    onClose();
  }, [onClose]);
  // Another org: the open chat, if any, is the old org's, so go to the new org's chat list.
  const leaveForOrg = useCallback(() => navigate(mobileChatsPath()), [navigate]);
  const openSearch = useCallback(() => navigate(mobileSearchPath()), [navigate]);
  const shell = useShellProject(pathname, search);
  const slots = useMemo<NavSlots>(
    () => ({
      "new-chat": ({ depth }) => (
        <SidebarSubRow
          label="New chat"
          depth={depth}
          icon={<SquarePen className="size-4" />}
          href={newChatPath()}
          onClick={() => navigate(newChatPath())}
          testId="mobile-nav-new-chat"
        />
      ),
      // The chats inline, as the shell's drawer lists them under Agents (the phone's own list: Mobile/MobileNavChatList).
      "chat-list": (layout) => <MobileNavChatList {...layout} onOpen={(href) => navigate(href)} />,
      inbox: () => <InboxNavItem href={inboxPath()} onOpen={() => navigate(inboxPath())} current={isAtOrBelow(pathname, inboxPath())} />,
      "sandboxes-housekeeping": () => <SandboxesHousekeepingDot />,
    }),
    [navigate, pathname],
  );
  return (
    <ShellNavColumn
      config={hybridNav}
      slots={slots}
      shell={shell}
      pathname={pathname}
      search={search}
      onNavigate={navigate}
      // A drill-in replaces the list here, so its rows are drawn as top-level rows (owner, 2026-11-24).
      flat
      onFind={openSearch}
      onProjectPicked={closeAfterPick}
      onOrgSwitched={leaveForOrg}
      onBack={showTopLevel}
      topLevel={topLevel}
    />
  );
}
