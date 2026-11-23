// Copyright (c) 2026 Reliant Labs

/**
 * The daemonless home through the PRODUCTION route tree, rendered (U6): a
 * person with no usable machine — onboarding not completed, or completed with
 * an empty daemon list — on both brands, with `web.org_routes` off and on.
 *
 *   - Every page that works without a machine (Projects, Apps and an app's
 *     console, the Inbox, Workflows, Usage, Settings) stays put, and never
 *     mounts OnboardingRoute — whose returning-user heal is the one thing
 *     that can complete onboarding with no user action (onboarding-flow
 *     skill) — and nothing calls CompleteOnboarding.
 *   - The home (`/`, `/o/acme`) sends them to Projects, the same way.
 *   - A chat they asked for — a project's chats, a chat by its URL (U5's
 *     `/c/…`), the new-chat view (`/new`) — walks a person who has not
 *     onboarded through the wizard and back (`returnTo`); those are the only
 *     routes that mount it.
 *   - An onboarded person with no machine: `/new` goes where New chat goes
 *     (Settings › Machines); a chat by its URL stays, as a project's chats
 *     do (the chat has its own Connect a machine).
 *   - A person with a machine keeps the chat home.
 *
 * The tree, guards and redirects are the real ones (test/orgRouteHarness.ts
 * builds the router); each lazy page is a stub naming itself, except that the
 * workspace (`App`, ModernApp) runs the redirect ModernApp runs
 * (useChatRouteRedirect) and nothing else. The shell frame is a passthrough:
 * its Set up chats card is covered in Sidebar.setUpChats.test.tsx.
 */

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const world = vi.hoisted(() => ({
  user: { id: "alice", onboardingCompleted: false, createdAtMs: 0 } as { id: string; onboardingCompleted: boolean; createdAtMs: number },
  daemons: [] as { status: number }[] | undefined,
  daemonsLoading: false,
  completeOnboarding: vi.fn(),
  mounted: [] as string[],
}));

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
vi.mock("@/lib/lazyRoute", async (importOriginal) => {
  const { useChatRouteRedirect } = await import("@/components/OnboardingFlow/useChatSetup");
  /** The workspace's redirect, exactly as ModernApp runs it, and no workspace. */
  function App() {
    const { holding } = useChatRouteRedirect();
    world.mounted.push("App");
    return <div data-testid="page-App" data-holding={String(holding)} />;
  }
  return {
    ...(await importOriginal<typeof import("@/lib/lazyRoute")>()),
    lazyRoute: (load: () => unknown, name: string) => {
      // routes.tsx's `lazyRoute(() => import('./App'), 'default')`, by the module it loads.
      if (name === "default" && /\/src\/App\.tsx|["']\.\/App["']/.test(String(load))) return App;
      return function Page() {
        world.mounted.push(name);
        return <div data-testid={`page-${name}`} />;
      };
    },
  };
});
vi.mock("@/hooks/useOnboardingQueries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useOnboardingQueries")>()),
  useCurrentUser: () => ({ data: world.user, isLoading: false }),
  useDaemonList: () => ({ data: world.daemons, isLoading: world.daemonsLoading }),
  useCompleteOnboarding: () => ({ mutate: world.completeOnboarding, mutateAsync: world.completeOnboarding }),
}));
// And the service under the hook: nothing may complete onboarding by any path.
vi.mock("@/services/controlPlane/onboarding", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/controlPlane/onboarding")>();
  return { ...actual, onboardingService: { ...actual.onboardingService, completeOnboarding: world.completeOnboarding } };
});

// The root's other residents read stores and the network; they are not what this tests.
vi.mock("@/components/Layout/shell/AppShell", () => ({ AppShellFrame: ({ children }: { children: ReactNode }) => <>{children}</> }));
vi.mock("@/components/Orgs/ActiveOrgBinding", () => ({ ActiveOrgBinding: () => null }));
vi.mock("@/components/PageVisitTracker", () => ({ PageVisitTracker: () => null }));
vi.mock("@/components/Shortcuts/ShortcutHost", () => ({ ShortcutHost: ({ children }: { children: ReactNode }) => <>{children}</> }));
vi.mock("@/components/Modals/ModalLayer", () => ({ ModalLayer: () => null }));
vi.mock("@/components/Projects/AddProjectDialog", () => ({ AddProjectDialogHost: () => null }));
vi.mock("@/components/Automations/dialog/AutomationDialogHost", () => ({ AutomationDialogHost: () => null }));
vi.mock("@/components/AnonSignInNudge", () => ({ AnonSignInNudge: () => null }));
vi.mock("@/lib/toast", async (importOriginal) => ({ ...(await importOriginal<typeof import("@/lib/toast")>()), Toaster: () => null }));
vi.mock("@/components/NewBuildPrompt", () => ({ NewBuildPrompt: () => null }));
vi.mock("@/components/Settings/GlobalUpdateHandler", () => ({ GlobalUpdateHandler: () => null }));
vi.mock("@/components/Onboarding", () => ({ ContextualTipsLayer: () => null, OnboardingWizard: () => null }));
vi.mock("@/components/Onboarding/JourneyTour", () => ({ JourneyTour: () => null }));
vi.mock("@/components/ForgeShell/ForgeLandingJourney", () => ({ ForgeLandingJourney: () => null }));
vi.mock("@/components/Layout/GitHubSyncBanner", () => ({ GitHubSyncStatus: () => null }));
vi.mock("@/components/Forge/QueuedDeployWatch", () => ({ QueuedDeployWatch: () => null }));
vi.mock("@/hooks/useAuthMode", () => ({ useAuthMode: () => ({ authMode: "supabase", loading: false }) }));

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { setTransport } from "@/lib/connect";
import { inOrg } from "@/lib/paths/org";
import { queryClient } from "@/lib/query-client";
import { resetShellForTests } from "@/lib/shell";
import { routeTree } from "@/routes";
import { useAuthStore } from "@/store/authStore";
import { createOrgRouteHarness } from "@/test/orgRouteHarness";

const ACME = { id: "org-a", slug: "acme", name: "Acme" };
const HOSTS = ["reliant", "forge"] as const;
const FLAGS = [false, true] as const;

/** [persona, onboarding completed, the daemon list] — both have no usable machine. */
const DAEMONLESS = [
  ["onboarding not completed", false, []],
  ["onboarded, the daemon list settled empty", true, []],
  ["onboarded, only a dead machine", true, [{ status: DaemonStatus.FAILED }]],
] as const;

/** Pages that work without a machine: the URL, and the page it mounts. */
const NO_MACHINE_PAGES = [
  ["/projects", "ProjectsPage"],
  // The retired /apps: Projects' apps (lib/paths/legacy.ts RETIRED_PAGES).
  ["/apps", "ProjectsPage"],
  ["/forge/p/x", null],
  ["/inbox", "InboxPage"],
  ["/workflows", null],
  ["/usage", "UsageBillingPage"],
  ["/settings/account", "SettingsPage"],
] as const;

/** A chat in acme, for the chat-by-URL routes (U5's loader resolves it). */
const CHATS = [{ id: "c-1", orgId: "org-a" }];

async function open(host: (typeof HOSTS)[number], flag: boolean, url: string) {
  const h = createOrgRouteHarness({ host, routeTree, flag, orgs: [ACME], chats: CHATS });
  useAuthStore.setState({
    user: { id: "alice", email: "alice@example.com", email_confirmed_at: "2026-01-01T00:00:00Z" } as never,
  });
  await h.open(url);
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={h.router} />
    </QueryClientProvider>,
  );
  return h;
}

/** Long enough for every effect a mount schedules to have run and navigated. */
const settle = () => new Promise((resolve) => setTimeout(resolve, 50));

afterEach(() => {
  cleanup();
  setTransport(null);
  resetShellForTests();
  world.mounted.length = 0;
  world.completeOnboarding.mockReset();
  world.daemonsLoading = false;
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

describe.each(HOSTS)("the %s brand", (host) => {
  describe.each(FLAGS)("web.org_routes %s", (flag) => {
    describe.each(DAEMONLESS)("a person with no usable machine: %s", (_who, onboarded, daemons) => {
      const persona = () => {
        world.user = { id: "alice", onboardingCompleted: onboarded, createdAtMs: 0 };
        world.daemons = [...daemons];
      };

      it.each(NO_MACHINE_PAGES)("opens %s in place, and never mounts the wizard or completes onboarding", async (url, page) => {
        persona();
        const h = await open(host, flag, url);
        if (page) expect(await screen.findByTestId(`page-${page}`)).toBeInTheDocument();
        await settle();
        // Where it was opened (or that page's canonical, org form): not the wizard, not a home.
        expect(h.pathname()).toContain(url === "/apps" ? "projects" : url.split("/")[1]);
        expect(h.pathname()).not.toMatch(/\/onboarding$/);
        expect(world.mounted).not.toContain("OnboardingRoute");
        expect(world.completeOnboarding).not.toHaveBeenCalled();
      });

      it.each(["/", "/o/acme"])("is sent from the home %s to Projects, never through the wizard", async (url) => {
        persona();
        const h = await open(host, flag, url);
        await waitFor(() => expect(h.pathname()).toMatch(/\/projects$/));
        // Built like every nav link (projectsPath): the org form when org links are on.
        expect(h.pathname()).toBe(flag ? inOrg("acme", "/projects") : "/projects");
        expect(await screen.findByTestId("page-ProjectsPage")).toBeInTheDocument();
        await settle();
        expect(world.mounted).not.toContain("OnboardingRoute");
        expect(world.completeOnboarding).not.toHaveBeenCalled();
      });
    });

    it("keeps a daemonless person who is not a member of the URL's org on its no-access page", async () => {
      world.user = { id: "alice", onboardingCompleted: false, createdAtMs: 0 };
      world.daemons = [];
      const h = createOrgRouteHarness({ host, routeTree, flag, orgs: [{ id: "org-b", slug: "side", name: "Side" }], tabOrg: "org-b" });
      useAuthStore.setState({
        user: { id: "alice", email: "alice@example.com", email_confirmed_at: "2026-01-01T00:00:00Z" } as never,
      });
      await h.open("/o/acme");
      render(
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={h.router} />
        </QueryClientProvider>,
      );
      await settle();
      expect(h.pathname()).toBe("/o/acme");
      expect(h.orgRoute()?.kind).toBe("not-member");
      expect(world.mounted).not.toContain("App");
      expect(world.mounted).not.toContain("OnboardingRoute");
    });

    it("walks a person who has not onboarded through the wizard from a project's chats, and back", async () => {
      world.user = { id: "alice", onboardingCompleted: false, createdAtMs: 0 };
      world.daemons = [];
      const h = await open(host, flag, "/project/p-1");
      await waitFor(() => expect(h.pathname()).toBe("/onboarding"));
      expect((h.router.state.location.search as { returnTo?: string }).returnTo).toBe("/project/p-1");
      expect(await screen.findByTestId("page-OnboardingRoute")).toBeInTheDocument();
    });

    // U5's chat URLs are chats the person asked for.
    it.each([
      // With the flag on, an org-less chat URL forwards into its org first.
      ["/c/c-1", flag ? "/o/acme/c/c-1" : "/c/c-1"],
      ["/o/acme/c/c-1/t/t-2?panel=files", "/o/acme/c/c-1/t/t-2?panel=files"],
      ["/new", flag ? "/o/acme/new" : "/new"],
      ["/o/acme/new", "/o/acme/new"],
    ])("walks a person who has not onboarded through the wizard from %s, and back", async (url, returnTo) => {
      world.user = { id: "alice", onboardingCompleted: false, createdAtMs: 0 };
      world.daemons = [];
      const h = await open(host, flag, url);
      await waitFor(() => expect(h.pathname()).toBe("/onboarding"));
      expect((h.router.state.location.search as { returnTo?: string }).returnTo).toBe(returnTo);
      expect(await screen.findByTestId("page-OnboardingRoute")).toBeInTheDocument();
      expect(world.completeOnboarding).not.toHaveBeenCalled();
    });

    it.each(["/new", "/o/acme/new"])("sends an onboarded person with no machine from %s where New chat goes: Settings › Machines", async (url) => {
      world.user = { id: "alice", onboardingCompleted: true, createdAtMs: 0 };
      world.daemons = [];
      const h = await open(host, flag, url);
      await waitFor(() => expect(h.pathname()).toMatch(/\/settings\/machines$/));
      await settle();
      expect(world.mounted).not.toContain("OnboardingRoute");
      expect(world.completeOnboarding).not.toHaveBeenCalled();
    });

    it.each(["/c/c-1", "/o/acme/c/c-1"])("keeps an onboarded person with no machine on a chat they opened by its URL, %s", async (url) => {
      world.user = { id: "alice", onboardingCompleted: true, createdAtMs: 0 };
      world.daemons = [];
      const h = await open(host, flag, url);
      expect(await screen.findByTestId("page-App")).toHaveAttribute("data-holding", "false");
      await settle();
      expect(h.pathname()).toMatch(/\/c\/c-1$/);
      expect(world.mounted).not.toContain("OnboardingRoute");
    });

    it("keeps the chat home for a person with a machine", async () => {
      world.user = { id: "alice", onboardingCompleted: true, createdAtMs: 0 };
      world.daemons = [{ status: DaemonStatus.ACTIVE }];
      const h = await open(host, flag, "/");
      expect(await screen.findByTestId("page-App")).toHaveAttribute("data-holding", "false");
      await settle();
      expect(h.pathname()).toBe(flag ? "/o/acme" : "/");
    });

    it("does not move an onboarded person while the daemon list loads, or after it fails", async () => {
      world.user = { id: "alice", onboardingCompleted: true, createdAtMs: 0 };
      world.daemons = undefined;
      world.daemonsLoading = true;
      const loading = await open(host, flag, "/");
      expect(await screen.findByTestId("page-App")).toHaveAttribute("data-holding", "true");
      await settle();
      expect(loading.pathname()).toBe(flag ? "/o/acme" : "/");
      cleanup();

      world.daemonsLoading = false;
      const failed = await open(host, flag, "/");
      expect(await screen.findByTestId("page-App")).toHaveAttribute("data-holding", "false");
      await settle();
      expect(failed.pathname()).toBe(flag ? "/o/acme" : "/");
    });
  });
});
