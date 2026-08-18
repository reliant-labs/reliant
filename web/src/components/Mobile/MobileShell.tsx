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
 * restored workspace. Mobile has no project route yet, so it falls back to the
 * user's first project — enough for iteration 1, where the list is scoped to a
 * single project.
 *
 * ## 3. Top-level navigation
 *
 * `MobileNavDrawer` is mounted here, once, rather than inside each screen —
 * it portals to `document.body` so it can slide in above whichever screen is
 * routed, and a single instance means there is exactly one source of truth
 * for open/closed state (`mobileDrawerStore`). Each screen's own header opts
 * in with `<MobileMenuButton />`, which just calls `store.open()`.
 */

import { useEffect } from "react";
import { Outlet, useNavigate } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";
import { MobileLayout } from "./MobileLayout";
import { MobileNavDrawer } from "./MobileNavDrawer";
import { useCurrentUser } from "@/hooks/useOnboardingQueries";
import { useProjectStore } from "../../store/projectStore";
import { useMobileDrawerStore } from "../../store/mobileDrawerStore";

export function MobileShell() {
  const navigate = useNavigate();
  const { data: currentUser, isLoading: isUserLoading } = useCurrentUser();

  const currentProject = useProjectStore((s) => s.currentProject);
  const projects = useProjectStore((s) => s.projects);
  const selectProject = useProjectStore((s) => s.selectProject);
  const loadProjects = useProjectStore((s) => s.loadProjects);

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

  useEffect(() => {
    if (needsOnboarding) return;
    if (projects.length === 0) void loadProjects();
  }, [needsOnboarding, projects.length, loadProjects]);

  // Fall back to the first project so the chat list has something to query
  // on first load. Guarded on `currentProject` being unset, so an explicit
  // selection via `/m/projects` (which sets it before this effect re-runs)
  // always sticks rather than being overwritten back to projects[0].
  useEffect(() => {
    if (currentProject || projects.length === 0) return;
    void selectProject(projects[0]);
  }, [currentProject, projects, selectProject]);

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
    </MobileLayout>
  );
}
