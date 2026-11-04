/**
 * Shell rendered for every `/m/*` route.
 *
 * Owns the two things that must happen before any mobile screen renders:
 *
 * ## 1. Setup onboarding still applies
 *
 * There are two separate systems both called "onboarding", and mobile treats
 * them differently:
 *
 *   - **Setup** (`/onboarding`, `components/OnboardingFlow/`) — pick compute,
 *     pick a model, connect GitHub, choose a project. This is a *prerequisite*:
 *     without a daemon and a project there is nothing for the mobile surface to
 *     show, so a mobile user who hasn't completed it is redirected here exactly
 *     like a desktop user. The flow is a centered card that already reflows, so
 *     it works on a phone today.
 *   - **Tour** (`?tour=<step>`, `components/Onboarding/`) — the guided
 *     spotlight walkthrough. It anchors to desktop chrome by DOM selector
 *     (`left-sidebar`, `right-sidebar`, `workflow-canvas`), none of which
 *     exists on mobile. `OnboardingWizard` suppresses itself on this surface.
 *
 * So: mobile users DO get onboarding; they just don't get the tour. Skipping
 * the redirect would drop a brand-new user onto an empty chat list with no
 * daemon and no project for the mobile surface to show.
 *
 * ## 2. A project must be selected
 *
 * The desktop app selects a project from the URL (`/project/$projectId`) or a
 * restored workspace. Mobile has no project route, so it restores the last
 * project (`restoreLastProject`, the desktop's own path) and falls back to the
 * user's first. A chat screen then moves to its chat's own project if that
 * differs (see MobileChatScreen).
 *
 * ## 3. Top-level navigation
 *
 * `MobileNavDrawer` is mounted here, once, rather than inside each screen —
 * it portals to `document.body` so it can slide in above whichever screen is
 * routed, and a single instance means there is exactly one source of truth
 * for open/closed state (`mobileDrawerStore`). Each screen's own header opts
 * in with `<MobileMenuButton />`, which just calls `store.open()`.
 *
 * ## 4. The live update stream
 *
 * Connected here, as ModernApp connects it on desktop (see
 * `useUpdateStreamConnection`), once the selected project's chats have
 * loaded. Screens only subscribe to the chat they show. Before, nothing on
 * `/m/*` connected it: the chat list never went live, and a chat or its
 * workflow view opened directly by URL subscribed before the chats loaded,
 * was deferred, and never connected at all.
 *
 * The shell also reads the Inbox counts once (`MobileInboxBadgeSync`) for the
 * badge every screen's menu button and the drawer's Inbox row show.
 *
 * ## 5. Navigation feedback
 *
 * `MobileNavigationFeedback` warms every screen's lazy chunk and shows a
 * progress bar while a navigation is pending. Without it, the first tap on
 * any destination did nothing visible until that chunk had downloaded. The
 * module comment there has the measurements.
 */

import { useEffect, useRef } from "react";
import { Outlet, useNavigate } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";
import { MobileLayout } from "./MobileLayout";
import { MobileNavDrawer } from "./MobileNavDrawer";
import { MobileNavigationFeedback } from "./MobileNavigationFeedback";
import { MobileInboxBadgeSync } from "./MobileInboxBadgeSync";
import { useCurrentUser } from "@/hooks/useOnboardingQueries";
import { useUpdateStreamConnection } from "@/hooks/useUpdateStreamConnection";
import { useProjectStore } from "../../store/projectStore";
import { useProjects } from "../../hooks/project-queries";
import { useChatStore } from "../../store/chatStore";
import { useMobileDrawerStore } from "../../store/mobileDrawerStore";

export function MobileShell() {
  const navigate = useNavigate();
  const { data: currentUser, isLoading: isUserLoading } = useCurrentUser();

  const currentProject = useProjectStore((s) => s.currentProject);
  const { projects } = useProjects();

  const needsOnboarding =
    !isUserLoading && (!currentUser || !currentUser.onboardingCompleted);

  // Same gate the desktop shell applies (see ModernApp) — a user without a
  // completed setup has no daemon and no project, so every mobile screen would
  // render empty. Wait for `isUserLoading` to settle first, otherwise the
  // one-frame `currentUser === undefined` reads as "needs onboarding" and we
  // bounce a fully-onboarded user.
  useEffect(() => {
    if (isUserLoading) return;
    if (!needsOnboarding) return;
    navigate({ to: "/onboarding", search: (prev) => prev, replace: true });
  }, [isUserLoading, needsOnboarding, navigate]);

  // Pick the context project the way the desktop shell does: the last one the
  // user had open, else the most recently used (restoreLastProject). Phone
  // browsers discard and reload tabs constantly, so "no project selected yet"
  // is the normal state on mobile. Guarded on `currentProject` being unset, so
  // a project opened since always sticks, and attempted once so a failed
  // restore cannot loop.
  //
  // Only the PROJECT is restored, never the desktop workspace inside it
  // (restoreWorkspace: false): that would select the desktop's last active
  // chat on every mobile load — marking it read and subscribing the update
  // stream to it — while the route shows a different chat or none.
  const restoreLastProject = useProjectStore((s) => s.restoreLastProject);
  const initialSelectionAttempted = useRef(false);
  useEffect(() => {
    if (currentProject || projects.length === 0) return;
    if (initialSelectionAttempted.current) return;
    initialSelectionAttempted.current = true;
    void restoreLastProject({ restoreWorkspace: false });
  }, [currentProject, projects, restoreLastProject]);

  // The chat list spans every project (or the /m/projects filter's one) and
  // does not follow the context project: load it once, then keep the stream
  // on it.
  const chatListRequested = useRef(false);
  useEffect(() => {
    if (isUserLoading || needsOnboarding || chatListRequested.current) return;
    chatListRequested.current = true;
    void useChatStore.getState().loadChats();
  }, [isUserLoading, needsOnboarding]);
  const chatsLoaded = useChatStore((s) => s.hasLoaded);
  useUpdateStreamConnection(chatsLoaded);

  const isDrawerOpen = useMobileDrawerStore((s) => s.isOpen);
  const closeDrawer = useMobileDrawerStore((s) => s.close);

  if (isUserLoading || needsOnboarding) {
    return (
      <MobileLayout>
        <div className="flex flex-1 items-center justify-center">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        </div>
      </MobileLayout>
    );
  }

  return (
    <MobileLayout>
      {/* The routed screens size themselves with `h-full`, so they need a
          parent whose height flex has already resolved — without this wrapper
          they'd claim the full viewport past the shell's `overflow-hidden`
          edge. `min-h-0` lets it shrink below the virtualized lists' content
          height. No sibling bar takes space below it anymore — the drawer is
          a portal overlay, not a layout participant. */}
      <div className="min-h-0 flex-1">
        <Outlet />
      </div>
      <MobileNavDrawer isOpen={isDrawerOpen} onClose={closeDrawer} />
      <MobileInboxBadgeSync />
      <MobileNavigationFeedback />
    </MobileLayout>
  );
}
