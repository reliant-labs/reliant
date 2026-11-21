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
  chatRouteAction,
  chatSetup,
  chatSetupPath,
  homeTarget,
  isOnboardingReturnTo,
  onboardingGateDecision,
  onboardingGateTarget,
  routeRequiresOnboarding,
  type ChatSetup,
} from "../onboardingGate";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
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
        page("/projects"),
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

  it("sends a not-yet-onboarded user on the forge brand from the phone's chat list to their projects, not the wizard", async () => {
    resetShellForTests("forge");
    mockUseCurrentUser.mockReturnValue(notOnboarded);
    const router = renderAt("/m/chats");

    await waitFor(() => expect(router.state.location.pathname).toBe("/projects"));
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
    ["/c/c-1", true],
    ["/c/c-1/t/t-2", true],
    ["/o/acme/c/c-1", true],
    ["/o/acme/c/c-1/t/t-2", true],
    ["/new", true],
    ["/o/acme/new", true],
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
    ["/connections", false],
    ["/news", false],
    ["/o/acme/connections", false],
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
    ["/c/c-1", true],
    ["/o/acme/c/c-1/t/t-2", true],
    ["/new", true],
    ["/o/acme/new", true],
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

/**
 * The daemonless home (U6, owner 2026-11-19): what chats need from a person,
 * and where each home and chat route sends them, on both brands, for every
 * persona — onboarded or not × a usable machine, none, or a list still
 * loading or failed.
 */
const ACTIVE = [{ status: DaemonStatus.ACTIVE }];
const STALE = [{ status: DaemonStatus.DISCONNECTED }, { status: DaemonStatus.FAILED }];
const DONE = { onboardingCompleted: true };
const NOT_DONE = { onboardingCompleted: false };

/** [persona, chatSetup facts, the ChatSetup they make] */
const SETUP_PERSONAS = [
  ["user loading", { userLoading: true, user: undefined, daemonsLoading: true, daemons: undefined }, "checking"],
  ["user record failed", { userLoading: false, user: null, daemonsLoading: false, daemons: undefined }, "unknown"],
  ["not onboarded, no machine", { userLoading: false, user: NOT_DONE, daemonsLoading: false, daemons: [] }, "onboard"],
  ["not onboarded, a machine", { userLoading: false, user: NOT_DONE, daemonsLoading: false, daemons: ACTIVE }, "onboard"],
  ["not onboarded, machines loading", { userLoading: false, user: NOT_DONE, daemonsLoading: true, daemons: undefined }, "onboard"],
  ["not onboarded, machines failed", { userLoading: false, user: NOT_DONE, daemonsLoading: false, daemons: undefined }, "onboard"],
  ["onboarded, a machine", { userLoading: false, user: DONE, daemonsLoading: false, daemons: ACTIVE }, "ready"],
  ["onboarded, no machine", { userLoading: false, user: DONE, daemonsLoading: false, daemons: [] }, "connect-machine"],
  ["onboarded, only unusable machines", { userLoading: false, user: DONE, daemonsLoading: false, daemons: STALE }, "connect-machine"],
  ["onboarded, machines loading", { userLoading: false, user: DONE, daemonsLoading: true, daemons: undefined }, "checking"],
  ["onboarded, machines failed", { userLoading: false, user: DONE, daemonsLoading: false, daemons: undefined }, "unknown"],
] as const;

describe("chatSetup", () => {
  it.each(SETUP_PERSONAS)("%s → %s", (_who, facts, expected) => {
    expect(chatSetup(facts)).toBe(expected);
  });

  it("keeps a settled answer through a failed refetch (no flapping)", () => {
    // TanStack keeps the last data on a refetch error; the decision reads the data.
    expect(chatSetup({ userLoading: false, user: DONE, daemonsLoading: false, daemons: [] })).toBe("connect-machine");
    expect(chatSetup({ userLoading: false, user: DONE, daemonsLoading: false, daemons: ACTIVE })).toBe("ready");
  });
});

describe("chatSetupPath", () => {
  it("sends a person who has not onboarded to the wizard, and one who has to Settings › Machines", () => {
    expect(chatSetupPath("onboard")).toBe("/onboarding");
    expect(chatSetupPath("onboard", "/project/p-1")).toBe("/onboarding?returnTo=%2Fproject%2Fp-1");
    // Never a returnTo that would loop.
    expect(chatSetupPath("onboard", "/")).toBe("/onboarding");
    // The wizard leaves at once for an onboarded person (useArrivedComplete).
    expect(chatSetupPath("connect-machine", "/project/p-1")).toBe("/settings/machines");
  });
});

describe.each([false, true])("homeTarget (forge brand %s)", (forgeBrand) => {
  /** Where each home sends each ChatSetup: one brand-independent rule on the desktop. */
  const DESKTOP: [setup: ChatSetup, expected: string | null][] = [
    ["onboard", "/projects"],
    ["connect-machine", "/projects"],
    ["ready", null],
    // No redirect on a guess: loading or failed stays put.
    ["checking", null],
    ["unknown", null],
  ];
  describe.each(["/", "/o/acme", "/o/acme/"])("at %s", (home) => {
    it.each(DESKTOP)("%s → %s", (setup, expected) => {
      expect(homeTarget(home, setup, { forgeBrand })).toBe(expected);
    });
  });

  it("onboards rather than redirects a home that asked for a chat (?plan, ?start)", () => {
    expect(homeTarget("/", "onboard", { forgeBrand, chatIntent: true })).toBeNull();
    expect(homeTarget("/", "connect-machine", { forgeBrand, chatIntent: true })).toBeNull();
  });

  it("leaves every other chat route alone: a chat the person asked for", () => {
    for (const path of ["/project/p-1", "/o/acme/project/p-1", "/m/chats/c-1", "/m/new"]) {
      expect(homeTarget(path, "onboard", { forgeBrand })).toBeNull();
      expect(homeTarget(path, "connect-machine", { forgeBrand })).toBeNull();
    }
  });

  it("keeps the phone's rule: the forge brand's chat list sends a person who has not onboarded to their projects", () => {
    for (const path of ["/m", "/m/chats"]) {
      expect(homeTarget(path, "onboard", { forgeBrand })).toBe(forgeBrand ? "/projects" : null);
      expect(homeTarget(path, "connect-machine", { forgeBrand })).toBeNull();
    }
  });

  it("is what the gate's target is, on the homes", () => {
    expect(onboardingGateTarget("/m/chats?x=1", forgeBrand)).toEqual(
      forgeBrand
        ? { to: "/projects", search: {}, replace: true }
        : { to: "/onboarding", search: { returnTo: "/m/chats?x=1" }, replace: true },
    );
    expect(onboardingGateTarget("/m/chats/c-1", forgeBrand).to).toBe("/onboarding");
    expect(onboardingGateTarget("/", forgeBrand)).toEqual({ to: "/projects", search: {}, replace: true });
  });
});

describe.each([false, true])("chatRouteAction (forge brand %s)", (forgeBrand) => {
  const act = (pathname: string, persona: (typeof SETUP_PERSONAS)[number], chatIntent = false) => {
    const [, facts] = persona;
    return chatRouteAction({
      pathname,
      href: pathname,
      chatIntent,
      userLoading: facts.userLoading,
      user: facts.user,
      setup: chatSetup(facts),
      forgeBrand,
    });
  };
  const persona = (who: string) => SETUP_PERSONAS.find(([name]) => name === who)!;
  const projects = { kind: "go", to: "/projects", search: {} };
  const stay = { kind: "stay" };
  const wait = { kind: "wait" };

  const onboard = (returnTo: string) => ({ kind: "go", to: "/onboarding", search: { returnTo } });
  const machines = { kind: "go", to: "/settings/machines", search: {} };
  /** The same answer at the org form of a URL: the wizard remembers that URL. */
  const inOrgForm = (expected: object, url: string) => {
    const search = (expected as { search?: { returnTo?: string } }).search;
    return typeof search === "object" && search.returnTo ? onboard(url) : expected;
  };

  /**
   * [persona, at the home, at a project's chats, at a chat by its URL (U5's
   * /c/…), at the new-chat view (U5's /new)]. A chat — a project's, or one by
   * its URL — is a chat the person asked for: the wizard for a person who has
   * not onboarded, and otherwise the chat's own Connect a machine. The
   * new-chat view is New chat: while chats cannot run it goes where New chat
   * goes (chatSetupPath).
   */
  const TABLE = [
    ["user loading", wait, wait, wait, wait],
    // A failed record: the wizard asks again, as it always did.
    ["user record failed", { kind: "go", to: "/onboarding", search: "keep" }, onboard("/project/p-1"), onboard("/c/c-1"), onboard("/new")],
    ["not onboarded, no machine", projects, onboard("/project/p-1"), onboard("/c/c-1"), onboard("/new")],
    ["not onboarded, a machine", projects, onboard("/project/p-1"), onboard("/c/c-1"), onboard("/new")],
    ["not onboarded, machines loading", projects, onboard("/project/p-1"), onboard("/c/c-1"), onboard("/new")],
    ["not onboarded, machines failed", projects, onboard("/project/p-1"), onboard("/c/c-1"), onboard("/new")],
    ["onboarded, a machine", stay, stay, stay, stay],
    // The chat has its own Connect a machine; the wizard would bounce them straight back.
    ["onboarded, no machine", projects, stay, stay, machines],
    ["onboarded, only unusable machines", projects, stay, stay, machines],
    // Checking: hold the home and the new-chat view rather than flash it and leave.
    ["onboarded, machines loading", wait, stay, stay, wait],
    ["onboarded, machines failed", stay, stay, stay, stay],
  ] as const;

  describe.each(TABLE)("%s", (who, atHome, atProject, atChat, atNew) => {
    it("at the home (/, /o/acme)", () => {
      expect(act("/", persona(who))).toEqual(atHome);
      expect(act("/o/acme", persona(who))).toEqual(atHome);
    });
    it("at a project's chats", () => {
      expect(act("/project/p-1", persona(who))).toEqual(atProject);
    });
    it("at a chat by its URL (/c/…, /o/acme/c/…)", () => {
      expect(act("/c/c-1", persona(who))).toEqual(atChat);
      expect(act("/o/acme/c/c-1/t/t-2", persona(who))).toEqual(inOrgForm(atChat, "/o/acme/c/c-1/t/t-2"));
    });
    it("at the new-chat view (/new, /o/acme/new)", () => {
      expect(act("/new", persona(who))).toEqual(atNew);
      expect(act("/o/acme/new", persona(who))).toEqual(inOrgForm(atNew, "/o/acme/new"));
    });
  });

  it("sends a home that asked for a chat where New chat goes: the wizard, search and all, or Settings › Machines", () => {
    expect(act("/", persona("not onboarded, no machine"), true)).toEqual({ kind: "go", to: "/onboarding", search: "keep" });
    // Onboarded with no usable machine: never the new-chat view with nothing to run it on.
    expect(act("/", persona("onboarded, no machine"), true)).toEqual(machines);
    expect(act("/o/acme", persona("onboarded, only unusable machines"), true)).toEqual(machines);
    expect(act("/", persona("onboarded, machines loading"), true)).toEqual(wait);
    expect(act("/", persona("onboarded, a machine"), true)).toEqual(stay);
    expect(act("/", persona("onboarded, machines failed"), true)).toEqual(stay);
  });

  it("does nothing off the chat routes", () => {
    for (const path of ["/projects", "/apps", "/forge/p/x", "/inbox", "/workflows", "/usage", "/settings"]) {
      expect(act(path, persona("not onboarded, no machine"))).toEqual(stay);
      expect(act(path, persona("onboarded, no machine"))).toEqual(stay);
    }
  });
});

describe("isOnboardingReturnTo / onboardingGateTarget", () => {
  it.each([
    ["/forge/p/x?env=prod", true],
    ["/inbox", true],
    ["/c/c-1/t/t-2?panel=files", true],
    ["/o/acme/new", true],
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
