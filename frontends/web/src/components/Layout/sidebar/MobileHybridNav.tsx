// Copyright (c) 2026 Reliant Labs

/**
 * The phone's nav (U6e, owner: "drilling in brings back the old nav"): the
 * SAME hybrid config the desktop nav and the narrow window's drawer draw
 * (navConfig/hybrid.ts), drill-ins and all, inside the phone's drawer
 * (Mobile/MobileNavDrawer).
 *
 * The phone's own screens (`/m/*`) are read as the desktop page they stand
 * for (mobile chats are the chat home, `/m/inbox` the Inbox, …), so a row
 * lights and Agents drills in there just as on the desktop. Every row links to
 * the desktop page; on a phone the router hands a page with a phone twin to
 * that twin (lib/mobileRedirect), and the rest (an app's console, Projects)
 * are one responsive page, so there is one nav and no legacy one to fall back to.
 */

import { useMemo } from "react";
import { MessagesSquare, Search, SquarePen } from "lucide-react";

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
  settingsPath,
  workflowDetailPath,
  workflowLibraryPath,
} from "@/lib/paths";
import { orgFreePath } from "@/lib/paths/org";

import { InboxNavItem } from "../../inbox/InboxNavItem";
import { isAtOrBelow } from "./navLocation";
import type { NavigateOptions, NavSlots } from "./NavRenderer";
import { SidebarRow, SidebarSubRow } from "./primitives";
import { ShellNavBody } from "./ShellNavBody";
import { isChatPage, useShellProject } from "./shellProject";

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
  if (atOrBelow(path, mobileMachinesPath())) return settingsPath("machines");
  if (atOrBelow(path, mobileSettingsPath())) return settingsPath();
  return pathname;
}

export function MobileHybridNav({
  pathname: phonePathname,
  search,
  onNavigate,
}: {
  /** The page on screen (a phone screen or a desktop page). */
  pathname: string;
  search: Record<string, unknown>;
  /** Opens a page; `drill` keeps the drawer open to show the area's rows. */
  onNavigate: (href: string, options?: NavigateOptions) => void;
}) {
  const pathname = desktopPathOfMobile(phonePathname);
  const shell = useShellProject(pathname, search);
  const slots = useMemo<NavSlots>(
    () => ({
      "new-chat": ({ depth }) => (
        <SidebarSubRow
          label="New chat"
          depth={depth}
          icon={<SquarePen className="size-4" />}
          href={newChatPath()}
          onClick={() => onNavigate(newChatPath())}
          testId="mobile-nav-new-chat"
        />
      ),
      // The phone's chat list is a screen of its own (the drawer is too narrow for it), one row away.
      "chat-list": ({ depth }) => (
        <SidebarSubRow
          label="Chats"
          depth={depth}
          icon={<MessagesSquare className="size-4" />}
          href={homePath()}
          onClick={() => onNavigate(homePath())}
          current={isChatPage(pathname) && !atOrBelow(orgFreePath(pathname), newChatPath())}
          testId="mobile-nav-chats"
        />
      ),
      inbox: () => <InboxNavItem href={inboxPath()} onOpen={() => onNavigate(inboxPath())} current={isAtOrBelow(pathname, inboxPath())} />,
    }),
    [onNavigate, pathname],
  );
  return (
    <div className="flex min-h-0 flex-1 flex-col" data-testid="mobile-hybrid-nav">
      {/* The phone's Search screen in the desktop's Find place. */}
      <div className="shrink-0 px-2 pb-1">
        <SidebarRow
          icon={<Search className="size-4" />}
          label="Find"
          href={mobileSearchPath()}
          onClick={() => onNavigate(mobileSearchPath())}
          testId="mobile-nav-find"
        />
      </div>
      <ShellNavBody shell={shell} pathname={pathname} search={search} slots={slots} onNavigate={onNavigate} />
    </div>
  );
}
