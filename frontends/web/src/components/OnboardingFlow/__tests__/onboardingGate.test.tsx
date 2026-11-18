/**
 * OnboardingGate — a user who has not finished onboarding goes through it
 * before an app route, and comes back to the page they asked for.
 *
 * THE BYPASS THIS CLOSES: the redirect to /onboarding lived only in ModernApp
 * (`/`, `/project/$id`) and MobileShell (`/m/*`). Every other route under the
 * `_authenticated` layout was gated on sign-in alone, so a first-time user who
 * came from forge — "Open in Reliant" lands on `/forge/p/<project>/…` — got
 * the forge console with no machine and no project, and never saw the wizard.
 *
 * These tests drive a real memory router and assert where the user ENDS UP,
 * not that something asked to navigate.
 */
import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
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
  CLAIM_LANDING_KEY,
  CLAIM_LANDING_TTL_MS,
  claimLandingExcuses,
  FORGE_SHELL_ONBOARDING_EXEMPT_PREFIXES,
  isOnboardingReturnTo,
  markClaimLanding,
  onboardingGateDecision,
  onboardingGateTarget,
  routeRequiresOnboarding,
} from "../onboardingGate";
import { onboardingSearchSchema } from "@/routeSchemas";

function Where() {
  const href = useRouterState({ select: (s) => s.location.href });
  return <div data-testid="where">{href}</div>;
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
    component: Where,
  });
  const router = createRouter({
    routeTree: root.addChildren([
      layout.addChildren([
        onboarding,
        page("/forge/p/$project/releases"),
        page("/settings/$section"),
        page("/apps"),
        page("/inbox"),
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
  sessionStorage.removeItem(CLAIM_LANDING_KEY);
});

describe("OnboardingGate", () => {
  it("sends a not-yet-onboarded user from a forge console deep link to onboarding, remembering the link", async () => {
    mockUseCurrentUser.mockReturnValue(notOnboarded);
    const router = renderAt("/forge/p/simplebutton/releases?env=prod");

    await waitFor(() => expect(router.state.location.pathname).toBe("/onboarding"));
    expect((router.state.location.search as { returnTo?: string }).returnTo).toBe(
      "/forge/p/simplebutton/releases?env=prod",
    );
  });

  it("lets an onboarded user through to the page", async () => {
    mockUseCurrentUser.mockReturnValue(onboarded);
    renderAt("/forge/p/simplebutton/releases?env=prod");

    expect(await screen.findByTestId("where")).toHaveTextContent("/forge/p/simplebutton/releases");
  });

  it("shows nothing of the page while it does not yet know", async () => {
    mockUseCurrentUser.mockReturnValue({ data: undefined, isLoading: true });
    renderAt("/inbox");

    // The spinner, not the page: rendering it would fire its queries for a
    // user about to be sent away.
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByTestId("where")).toBeNull();
  });

  it.each(["/settings/billing", "/apps", "/onboarding"])("leaves %s alone mid-onboarding", async (url) => {
    mockUseCurrentUser.mockReturnValue(notOnboarded);
    const router = renderAt(url);

    expect(await screen.findByTestId("where")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe(url);
  });

  it("lets the user through when their record could not be loaded", async () => {
    // A control-plane hiccup must not trap someone on a spinner; `/` asks again.
    mockUseCurrentUser.mockReturnValue({ data: undefined, isLoading: false });
    renderAt("/inbox");

    expect(await screen.findByTestId("where")).toHaveTextContent("/inbox");
  });
});

describe("a claim excuses only the claimed app's console", () => {
  it("lets the claimant through on the claimed app's pages", async () => {
    mockUseCurrentUser.mockReturnValue(notOnboarded);
    // The gate reads the signed-in user from the auth store.
    const { useAuthStore } = await import("@/store/authStore");
    useAuthStore.setState({ user: { id: "auth-1" } as never });
    markClaimLanding("auth-1", "simplebutton");
    const router = renderAt("/forge/p/simplebutton/releases?env=prod");

    expect(await screen.findByTestId("where")).toHaveTextContent("/forge/p/simplebutton/releases");
    expect(router.state.location.pathname).toBe("/forge/p/simplebutton/releases");
    useAuthStore.setState({ user: null });
  });

  it("gates another app's console, /inbox and /apps-like routes", () => {
    markClaimLanding("auth-1", "simplebutton", 1_000);
    expect(claimLandingExcuses("auth-1", "/forge/p/simplebutton", 2_000)).toBe(true);
    expect(claimLandingExcuses("auth-1", "/forge/p/simplebutton/observability/errors", 2_000)).toBe(true);
    expect(claimLandingExcuses("auth-1", "/forge/p/simplebuttonx", 2_000)).toBe(false);
    expect(claimLandingExcuses("auth-1", "/forge/p/other-app", 2_000)).toBe(false);
    expect(claimLandingExcuses("auth-1", "/apps", 2_000)).toBe(false);
    expect(claimLandingExcuses("auth-1", "/inbox", 2_000)).toBe(false);
  });

  it("does not apply to another user", () => {
    markClaimLanding("auth-1", "simplebutton", 1_000);
    expect(claimLandingExcuses("auth-2", "/forge/p/simplebutton", 2_000)).toBe(false);
    expect(claimLandingExcuses(undefined, "/forge/p/simplebutton", 2_000)).toBe(false);
  });

  it("expires, and is removed when it has", () => {
    markClaimLanding("auth-1", "simplebutton", 1_000);
    expect(claimLandingExcuses("auth-1", "/forge/p/simplebutton", 1_000 + CLAIM_LANDING_TTL_MS)).toBe(true);
    expect(claimLandingExcuses("auth-1", "/forge/p/simplebutton", 1_001 + CLAIM_LANDING_TTL_MS)).toBe(false);
    expect(sessionStorage.getItem(CLAIM_LANDING_KEY)).toBeNull();
  });

  it("ignores a malformed mark", () => {
    sessionStorage.setItem(CLAIM_LANDING_KEY, "auth-1");
    expect(claimLandingExcuses("auth-1", "/forge/p/simplebutton")).toBe(false);
  });
});

describe("routeRequiresOnboarding", () => {
  it.each([
    ["/forge", true],
    ["/forge/p/x", true],
    ["/forge/observability/errors", true],
    ["/inbox", true],
    ["/workflows", true],
    ["/workflow/new", true],
    ["/onboarding", false],
    ["/settings", false],
    ["/settings/billing", false],
    ["/m/chats", false],
    ["/forge/demo", false],
    ["/apps", false],
    // A prefix is a path segment, not a string prefix.
    ["/mcp", true],
    ["/settingsx", true],
  ])("%s → %s", (pathname, expected) => {
    expect(routeRequiresOnboarding(pathname)).toBe(expected);
  });
});

describe("the forge site's exemptions", () => {
  it.each([
    ["/apps", true],
    ["/forge/p/x/releases", true],
    ["/settings/billing", false],
    ["/onboarding", false],
  ])("%s → %s", (pathname, expected) => {
    expect(routeRequiresOnboarding(pathname, FORGE_SHELL_ONBOARDING_EXEMPT_PREFIXES)).toBe(expected);
  });

  it("lets an excused user through a gated route", () => {
    const user = { onboardingCompleted: false };
    expect(onboardingGateDecision({ pathname: "/inbox", userLoading: false, user, excused: true })).toBe("allow");
    expect(onboardingGateDecision({ pathname: "/inbox", userLoading: false, user })).toBe("onboard");
  });
});

describe("onboardingGateDecision", () => {
  it("waits for the user on a gated route, and only there", () => {
    expect(onboardingGateDecision({ pathname: "/inbox", userLoading: true, user: undefined })).toBe("wait");
    expect(onboardingGateDecision({ pathname: "/settings", userLoading: true, user: undefined })).toBe("allow");
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
