/**
 * OnboardingGate — a user who has not finished onboarding goes through it
 * before a CHAT route, and comes back to the page they asked for. Every other
 * page works without a machine, so it never sends anyone to the wizard (U6:
 * the gate is scoped to the chat routes; onboardingGate.ts).
 *
 * These tests drive a real memory router and assert where the user ENDS UP,
 * not that something asked to navigate.
 */
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  useRouterState,
} from "@tanstack/react-router";

const mockUseCurrentUser = vi.fn();
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCurrentUser: () => mockUseCurrentUser(),
}));

import { OnboardingGate } from "../OnboardingGate";
import {
  forgeBrandHomeTarget,
  isOnboardingReturnTo,
  onboardingGateDecision,
  onboardingGateTarget,
  routeRequiresOnboarding,
} from "../onboardingGate";
import { resetShellForTests } from "@/lib/shell";
import { onboardingSearchSchema } from "@/routeSchemas";

function Where() {
  const href = useRouterState({ select: (s) => s.location.href });
  return <div data-testid="where">{href}</div>;
}

/** The wizard's route, standing in for OnboardingRoute: mounting it is what the gate must not do off the chat routes. */
function Wizard() {
  return <div data-testid="wizard" />;
}

function renderAt(url: string) {
  const root = createRootRoute({ component: Outlet });
  const layout = createRoute({
    getParentRoute: () => root,
    id: "_authenticated",
    component: () => (
      <OnboardingGate>
        <Outlet />
      </OnboardingGate>
    ),
  });
  const page = (path: string) => createRoute({ getParentRoute: () => layout, path, component: Where });
  const onboarding = createRoute({
    getParentRoute: () => layout,
    path: "/onboarding",
    validateSearch: onboardingSearchSchema,
    component: Wizard,
  });
  const router = createRouter({
    routeTree: root.addChildren([
      layout.addChildren([
        onboarding,
        page("/forge/p/$project"),
        page("/forge/p/$project/releases"),
        page("/settings/$section"),
        page("/apps"),
        page("/sites"),
        page("/usage"),
        page("/inbox"),
        page("/workflows"),
        page("/m/chats"),
        page("/m/chats/$chatId"),
      ]),
    ]),
    history: createMemoryHistory({ initialEntries: [url] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

const notOnboarded = { data: { id: "u1", onboardingCompleted: false }, isLoading: false };
const onboarded = { data: { id: "u1", onboardingCompleted: true }, isLoading: false };

beforeEach(() => {
  vi.clearAllMocks();
  resetShellForTests("reliant");
});

afterEach(() => {
  resetShellForTests();
});

describe("OnboardingGate", () => {
  it("sends a not-yet-onboarded user from a chat to onboarding, remembering the chat", async () => {
    mockUseCurrentUser.mockReturnValue(notOnboarded);
    const router = renderAt("/m/chats/c-1?focus=1");

    await waitFor(() => expect(router.state.location.pathname).toBe("/onboarding"));
    expect((router.state.location.search as { returnTo?: string }).returnTo).toBe("/m/chats/c-1?focus=1");
    expect(await screen.findByTestId("wizard")).toBeInTheDocument();
  });

  // A forge signup, or anyone without a machine, opening their apps or an
  // app's console: the page works, and the wizard never mounts (so neither
  // does OnboardingRoute's returning-user heal, which only runs inside it).
  it.each(["/apps", "/forge/p/x", "/forge/p/simplebutton/releases?env=prod", "/sites", "/usage", "/inbox", "/workflows", "/settings/billing"])(
    "lets a not-yet-onboarded user open %s, and never mounts the wizard",
    async (url) => {
      mockUseCurrentUser.mockReturnValue(notOnboarded);
      const router = renderAt(url);

      expect(await screen.findByTestId("where")).toBeInTheDocument();
      await new Promise((r) => setTimeout(r, 20));
      expect(router.state.location.pathname).toBe(url.split("?")[0]);
      expect(screen.queryByTestId("wizard")).toBeNull();
    },
  );

  it("lets an onboarded user through to a chat", async () => {
    mockUseCurrentUser.mockReturnValue(onboarded);
    renderAt("/m/chats/c-1");

    expect(await screen.findByTestId("where")).toHaveTextContent("/m/chats/c-1");
  });

  it("shows nothing of a chat while it does not yet know", async () => {
    mockUseCurrentUser.mockReturnValue({ data: undefined, isLoading: true });
    renderAt("/m/chats");

    // The spinner, not the page: rendering it would fire its queries for a
    // user about to be sent away.
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByTestId("where")).toBeNull();
  });

  it("does not wait for the user off the chat routes", async () => {
    mockUseCurrentUser.mockReturnValue({ data: undefined, isLoading: true });
    renderAt("/apps");

    expect(await screen.findByTestId("where")).toHaveTextContent("/apps");
  });

  it("lets the user through when their record could not be loaded", async () => {
    // A control-plane hiccup must not trap someone on a spinner.
    mockUseCurrentUser.mockReturnValue({ data: undefined, isLoading: false });
    renderAt("/m/chats");

    expect(await screen.findByTestId("where")).toHaveTextContent("/m/chats");
  });

  it("sends a not-yet-onboarded user on the forge brand from the phone's chat list to their apps, not the wizard", async () => {
    resetShellForTests("forge");
    mockUseCurrentUser.mockReturnValue(notOnboarded);
    const router = renderAt("/m/chats");

    await waitFor(() => expect(router.state.location.pathname).toBe("/apps"));
    expect(screen.queryByTestId("wizard")).toBeNull();
  });

  it("still onboards a not-yet-onboarded user on the forge brand who opens a chat", async () => {
    resetShellForTests("forge");
    mockUseCurrentUser.mockReturnValue(notOnboarded);
    const router = renderAt("/m/chats/c-1");

    await waitFor(() => expect(router.state.location.pathname).toBe("/onboarding"));
  });
});

describe("routeRequiresOnboarding", () => {
  it.each([
    // The chat routes, in either form.
    ["/", true],
    ["/o/acme", true],
    ["/o/acme/", true],
    ["/project/1", true],
    ["/o/acme/project/1", true],
    ["/m", true],
    ["/m/chats", true],
    ["/m/chats/c-1", true],
    ["/m/chats/c-1/workflow", true],
    ["/m/new", true],
    // Everything else works without a machine.
    ["/forge", false],
    ["/forge/p/x", false],
    ["/forge/observability/errors", false],
    ["/forge/demo", false],
    ["/inbox", false],
    ["/o/acme/inbox", false],
    ["/workflows", false],
    ["/workflow/new", false],
    ["/apps", false],
    ["/o/acme/apps/x", false],
    ["/o/acme/apps", false],
    ["/sites", false],
    ["/o/acme/sites", false],
    ["/usage", false],
    ["/o/acme/usage", false],
    ["/settings", false],
    ["/settings/billing", false],
    ["/onboarding", false],
    ["/m/daemons", false],
    ["/m/projects", false],
    ["/m/settings", false],
    // A prefix is a path segment, not a string prefix.
    ["/projects", false],
    ["/m/newer", false],
    ["/mcp", false],
  ])("%s → %s", (pathname, expected) => {
    expect(routeRequiresOnboarding(pathname)).toBe(expected);
  });
});

describe("onboardingGateDecision", () => {
  it("waits for the user on a chat route, and only there", () => {
    expect(onboardingGateDecision({ pathname: "/", userLoading: true, user: undefined })).toBe("wait");
    expect(onboardingGateDecision({ pathname: "/inbox", userLoading: true, user: undefined })).toBe("allow");
    expect(onboardingGateDecision({ pathname: "/settings", userLoading: true, user: undefined })).toBe("allow");
  });

  /**
   * The decision for each persona on each kind of route. "Daemonless" is the
   * person forge signs up: onboarding not completed (they never went through
   * it), no machine. The gate reads only the onboarding flag, so a daemonless
   * person who did finish onboarding (they skipped the machine) is "onboarded".
   */
  const PERSONAS = [
    ["onboarded", { onboardingCompleted: true }, "allow"],
    ["daemonless, onboarding not completed", { onboardingCompleted: false }, "onboard"],
    ["record failed to load", null, "allow"],
  ] as const;
  const ROUTES = [
    ["/", true],
    ["/o/acme/", true],
    ["/project/1", true],
    ["/m/chats", true],
    ["/forge/p/x", false],
    ["/o/acme/apps/x", false],
    ["/o/acme/inbox", false],
    ["/inbox", false],
    ["/workflows", false],
    ["/apps", false],
    ["/sites", false],
    ["/usage", false],
    ["/settings", false],
  ] as const;
  describe.each(PERSONAS)("%s", (_who, user, onChat) => {
    it.each(ROUTES)("%s", (pathname, chat) => {
      expect(onboardingGateDecision({ pathname, userLoading: false, user })).toBe(chat ? onChat : "allow");
    });
  });
});

describe("forgeBrandHomeTarget", () => {
  it.each([
    ["/", true, "/apps"],
    ["/o/acme", true, "/apps"],
    ["/m", true, "/apps"],
    ["/m/chats", true, "/apps"],
    // A chat they asked for still onboards them.
    ["/project/1", true, null],
    ["/m/chats/c-1", true, null],
    // Reliant onboards everyone.
    ["/", false, null],
    ["/m/chats", false, null],
  ] as const)("%s (forge brand %s) → %s", (pathname, forgeBrand, expected) => {
    expect(forgeBrandHomeTarget(pathname, forgeBrand)).toBe(expected);
  });

  it("is what the gate's target is, on the forge brand's homes", () => {
    expect(onboardingGateTarget("/m/chats?x=1", true)).toEqual({ to: "/apps", search: {}, replace: true });
    expect(onboardingGateTarget("/m/chats/c-1", true).to).toBe("/onboarding");
    expect(onboardingGateTarget("/m/chats", false).to).toBe("/onboarding");
  });
});

describe("isOnboardingReturnTo / onboardingGateTarget", () => {
  it.each([
    ["/forge/p/x?env=prod", true],
    ["/inbox", true],
    ["/", false],
    ["/onboarding?plan=%7B%7D", false],
    ["/auth?redirect=%2Fx", false],
    ["//evil.com", false],
    ["https://evil.com/x", false],
    [undefined, false],
  ])("%s → %s", (returnTo, expected) => {
    expect(isOnboardingReturnTo(returnTo)).toBe(expected);
  });

  it("carries only a usable returnTo", () => {
    expect(onboardingGateTarget("/inbox").search).toEqual({ returnTo: "/inbox" });
    expect(onboardingGateTarget("/").search).toEqual({});
  });
});
