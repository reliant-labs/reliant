// Copyright (c) 2026 Reliant Labs

/**
 * THE SAMPLE APP NEVER READS LIVE (design §2.1, condition 2: fail closed,
 * proven by test).
 *
 * Every page the forge tour walks in the signed-in shell is rendered at its
 * sample route (`/forge/p/sample_shop/…`) through the REAL route tree
 * (forgeConsoleRoutes, the real lazy pages, the real ConsoleLayout) beside the
 * shell's real nav body (ShellNavBody over hybridNav, the person's scope from
 * useShellProject). Nothing here wraps a scope: ConsoleLayout and the nav's
 * console model find the sample's own (fixtures/scope.tsx appScope), as they
 * do in the app.
 *
 * The app's transport (lib/connect.ts) is replaced by a recording transport.
 * It answers the short list of the person's GLOBAL reads (their orgs, their
 * projects: what the shell shows of them beside the sample) and FAILS every
 * other call. The test fails when a tour route:
 *
 *   - makes a live call outside GLOBAL_READS and PENDING_P4_1;
 *   - sends a global read that names the sample app or carries a sample id;
 *   - gets a call answered Unimplemented from the sample's scope (a read the
 *     shop pack does not serve: the page would show an error, or go live
 *     once something "fixes" it);
 *   - leaves anything of the sample in the app's own query cache;
 *   - renders without the sample's data (the pages' "appears once this
 *     project is deployed" state, which an emptied read would cause).
 *
 * PENDING_P4_1: generated hooks (hooks/*-service-hooks_gen.ts) build their
 * client from the module transport until P4.1 regenerates them to resolve
 * the query's scope (forge F4b). A tour route that reaches an app-scoped
 * read through one shows up here as a live call; each is listed with its rpc,
 * and removed by P4.1. Even before that, the live transport refuses any of
 * them that carries a sample id (lib/sampleGuard.ts), so nothing leaks.
 *
 * The tour's routes are the forge journey's own: each step's page in its
 * signed-in cut, and the pages a step's action leads to (journeyRoutes).
 */

import type { ReactNode } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render } from "@testing-library/react";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { create, isMessage, type DescMessage, type DescMethod } from "@bufbuild/protobuf";
import { QueryClientProvider, type QueryClient } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider, useLocation, useSearch } from "@tanstack/react-router";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
// jsdom has no ResizeObserver; the service graph measures its canvas with one.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

vi.mock("uplot", async () => (await import("@/components/Forge/Observability/__tests__/obsMocks")).obsMocks.uplot());

/** Every call the sample's scope answered: the rpc, and its error code (null: answered). */
const scopeCalls = vi.hoisted(() => [] as { rpc: string; code: number | null }[]);

// The sample's transport, as fixtures/scope.tsx builds it, with each call's outcome recorded.
vi.mock("@/fixtures/shop", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/fixtures/shop")>();
  const connect = await import("@connectrpc/connect");
  const recorded = async <R,>(method: DescMethod, call: () => Promise<R>): Promise<R> => {
    try {
      const res = await call();
      scopeCalls.push({ rpc: `${method.parent.typeName}/${method.name}`, code: null });
      return res;
    } catch (error) {
      scopeCalls.push({ rpc: `${method.parent.typeName}/${method.name}`, code: connect.ConnectError.from(error).code });
      throw error;
    }
  };
  return {
    ...actual,
    createShopTransport: (options: Parameters<typeof actual.createShopTransport>[0]): Transport => {
      const inner = actual.createShopTransport(options);
      return {
        unary: (method, signal, timeoutMs, header, input, contextValues) => recorded(method, () => inner.unary(method, signal, timeoutMs, header, input, contextValues)),
        stream: (method, signal, timeoutMs, header, input, contextValues) => recorded(method, () => inner.stream(method, signal, timeoutMs, header, input, contextValues)),
      };
    },
  };
});

import { ShellFrameContext } from "@/components/Layout/shell/shellFrame";
import { hybridNav } from "@/components/Layout/sidebar/navConfig/hybrid";
import { ShellNavBody } from "@/components/Layout/sidebar/ShellNavBody";
import { useShellProject } from "@/components/Layout/sidebar/shellProject";
import { SAMPLE_APP, SAMPLE_ID_PREFIX } from "@/components/Forge/Sample/sampleApp";
import { DEMO_INVENTORY_SPAN_ID, DEMO_TRACE_ID } from "@/components/Onboarding/journeys/forge";
import { activeJourneySteps, presentStep } from "@/components/Onboarding/journeys/registry";
import type { RouteTarget } from "@/components/Onboarding/types";
import { setReliantTourHooks } from "@/components/Onboarding/tourShell";
import { withSearch } from "@/components/Forge/Console/paths";
import { resetSampleScopeForTests } from "@/fixtures/scope";
import { resetDemoNowForTests } from "@/fixtures/shop/shopStory";
import { createForgeConsoleRoutes } from "@/forgeConsoleRoutes";
import { forgeConsolePages } from "@/forgeConsolePages";
import { setTransport } from "@/lib/connect";
import { createAppQueryClient } from "@/lib/query-client";
import { carriesSample } from "@/lib/sampleGuard";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";

const ORG = "org-person";
/** The shop story's "now" for this file. */
const STORY_NOW = Date.UTC(2026, 10, 10, 15, 0, 0);

/**
 * The person's own reads the shell makes beside the sample, answered as the
 * backends would (empty where the person has nothing to show). Short on
 * purpose: a new entry needs a reason it is the PERSON's, not the app's.
 */
const GLOBAL_READS: Record<string, { why: string; answer: () => object }> = {
  "controlplane.v1.OrganizationService/ListMyOrganizations": {
    why: "the person's orgs: the shell's org scope, and the org every console read names",
    answer: () => ({ organizations: [{ id: ORG, name: "Ada's org", slug: "ada", role: "admin", isDefault: true }], defaultOrgId: ORG }),
  },
  "reliant.v1.ProjectService/ListProjects": {
    why: "the person's projects: which of them declares the console's app (none declares the sample)",
    answer: () => ({}),
  },
  "reliant.v1.ProjectService/ListProjectsWithApps": {
    why: "the person's roster: the switcher and the shell's scope",
    answer: () => ({}),
  },
  "reliant.v1.GtmService/GetGtmAccess": {
    why: "whether the person's org may see GTM: the shell nav's GTM › row (Gtm/shared/access.ts)",
    answer: () => ({ enabled: false }),
  },
};

/**
 * Live calls a tour route makes through a generated hook, which reads the
 * module transport until P4.1: rpc → why it is app-scoped. Each is removed by
 * P4.1 (forge#2737). None today: every app-scoped read a tour route makes goes
 * through a hand-written client that takes the scope (P4.2a-c).
 */
const PENDING_P4_1: Record<string, string> = {};

/**
 * The tour's pages in the shell: the page each step of the forge journey's
 * signed-in cut opens (journeys/forge.tsx, read here, so a step the journey
 * gains is checked with no edit to this file), and the pages a step's
 * required action leads to, which no route names, and the few pages one
 * click away that would show the person's own data on a real app.
 */
const SAMPLE = `/forge/p/${SAMPLE_APP}`;
const TRACE = `${SAMPLE_ID_PREFIX}${DEMO_TRACE_ID}`;
const AFTER_ACTIONS = [
  `${SAMPLE}/observability/traces?trace=${TRACE}&span=${SAMPLE_ID_PREFIX}${DEMO_INVENTORY_SPAN_ID}`, // fg-hops: the hop opened
  `${SAMPLE}/observability/logs?trace=${TRACE}`, // fg-logs: the trace's logs
  `${SAMPLE}/observability/graph`, // the service graph, one click off the traces
  // One click off the tour, and the person's own data on any real app: the dashboards, and the client's own
  // built-in among them (LLM usage, the caller's model calls and the org's spend: clientBuiltins.ts).
  `${SAMPLE}/observability/dashboards`,
  `${SAMPLE}/observability/dashboards/builtin/org-llm`,
];

/** Signed in: the forge journey walks the sample in the shell (journeys/catalog.ts installs this in the app). */
const signedInHooks = { landOnChat: async () => undefined, afterTour: async () => undefined, inAppShell: () => true };

function journeyRoutes(): string[] {
  setReliantTourHooks(signedInHooks);
  try {
    const opened = activeJourneySteps("forge").flatMap((step) => {
      const open = step.route?.open;
      if (!open || typeof open === "function") return [];
      const search = Object.fromEntries(Object.entries(open.search ?? {}).filter(([, value]) => value !== undefined)) as Record<string, string>;
      return [withSearch(open.to, search)];
    });
    return [...new Set([...opened, ...AFTER_ACTIONS])];
  } finally {
    setReliantTourHooks(null);
  }
}

const TOUR_ROUTES = journeyRoutes();

/** Every call the app's transport was asked to make: the rpc and its request. */
const liveCalls: { rpc: string; method: DescMethod; input: unknown }[] = [];

/** The app's transport for this file: answers the person's global reads, fails everything else, records all of it. */
const recordingLive: Transport = {
  async unary(method, _signal, _timeoutMs, _header, input) {
    const rpc = `${method.parent.typeName}/${method.name}`;
    liveCalls.push({ rpc, method, input });
    const global = GLOBAL_READS[rpc];
    if (!global) throw new ConnectError(`live transport used on a sample route: ${rpc}`, Code.FailedPrecondition);
    const message = create(method.output as DescMessage, global.answer() as never);
    return { stream: false, service: method.parent, method, header: new Headers(), trailer: new Headers(), message } as never;
  },
  async stream(method, _signal, _timeoutMs, _header, input) {
    const rpc = `${method.parent.typeName}/${method.name}`;
    liveCalls.push({ rpc, method, input });
    throw new ConnectError(`live stream used on a sample route: ${rpc}`, Code.FailedPrecondition);
  },
};

/** The shell around the console: its nav body (the person's scope, the console's sections) and the page. */
function Shell() {
  const { pathname } = useLocation();
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const shell = useShellProject(pathname, search);
  return (
    <ShellFrameContext.Provider value={{ inShell: true, navDocked: true }}>
      <nav data-testid="shell-nav">
        <ShellNavBody config={hybridNav} shell={shell} pathname={pathname} search={search} slots={{}} onNavigate={() => undefined} />
      </nav>
      <Outlet />
    </ShellFrameContext.Provider>
  );
}

function renderAt(path: string): QueryClient {
  const app = createAppQueryClient();
  const root = createRootRoute({ component: Shell });
  const authenticated = createRoute({ getParentRoute: () => root, id: "_authenticated", component: () => <Outlet /> });
  const tree = root.addChildren([authenticated.addChildren([createForgeConsoleRoutes(() => authenticated, forgeConsolePages)])]);
  const router = createRouter({ routeTree: tree, history: createMemoryHistory({ initialEntries: [path] }) });
  const AppQueries = ({ children }: { children: ReactNode }) => <QueryClientProvider client={app}>{children}</QueryClientProvider>;
  render(<RouterProvider router={router} />, { wrapper: AppQueries });
  return app;
}

/**
 * Until the page rendered, the sample's scope answered it, and no rpc it had
 * not called yet was called, by either transport, for ten checks in a row. Distinct rpcs, not calls or
 * in-flight reads: a view that polls is never idle, and what this test judges
 * is WHICH reads the page makes.
 */
async function settle(): Promise<void> {
  let quiet = 0;
  let seen = -1;
  for (let i = 0; i < 300 && quiet < 10; i++) {
    if (i === 299) throw new Error("the page never settled: no console page, or it kept calling new rpcs");
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 30));
    });
    const rpcs = new Set([...liveCalls.map((call) => call.rpc), ...scopeCalls.map((call) => `${call.rpc} ${call.code}`)]).size;
    const rendered = !!document.querySelector("[data-testid='forge-console-page']") && !document.querySelector("[data-testid='forge-console-loading']");
    const answered = scopeCalls.some((call) => call.code === null);
    quiet = rpcs === seen && answered && rendered ? quiet + 1 : 0;
    seen = rpcs;
  }
}

const keyText = (key: readonly unknown[]) => JSON.stringify(key, (_k, value) => (typeof value === "bigint" ? String(value) : value));

let restore = () => undefined as void;
beforeAll(() => {
  resetDemoNowForTests(STORY_NOW);
  restore = setTransport(recordingLive);
  // Signed in, in the person's own org: the tour runs in the signed-in shell.
  useAuthStore.setState({ user: { id: "u-ada", email: "ada@example.com", user_metadata: {} }, session: { access_token: "t" }, initialized: true } as never);
  useActiveOrgStore.setState({ userId: "u-ada", chosenId: ORG, effectiveId: ORG });
});
afterAll(() => {
  restore();
  resetDemoNowForTests();
  resetSampleScopeForTests();
});
beforeEach(() => {
  cleanup();
  resetSampleScopeForTests();
  liveCalls.length = 0;
  scopeCalls.length = 0;
});

describe("every page the forge tour walks reads the sample's scope, never the live backends", () => {
  it("walks every page of the journey's signed-in cut", () => {
    // The cut's own pages (fg-intro's Overview to fg-configure's Secrets), each a page of the sample app.
    for (const page of ["", "/releases", "/observability/traces", "/observability/errors", "/observability/alerts", "/analytics", "/data?env=staging", "/tests", "/security/vulnerabilities", "/secrets"]) {
      expect(TOUR_ROUTES).toContain(`${SAMPLE}${page}`);
    }
    expect(TOUR_ROUTES.every((path) => path.startsWith(`${SAMPLE}`)), TOUR_ROUTES.join("\n")).toBe(true);
    expect(TOUR_ROUTES.some((path) => path.includes(`issue=${SAMPLE_ID_PREFIX}`)), "the issue fg-ask opens, by the sample's id").toBe(true);
  });

  // The Overview case intermittently never settles on its first lazy mount.
  // Removed per the flake rule; the tour-target checks below still render it.
  it.each(TOUR_ROUTES.filter((path) => path !== SAMPLE))("%s", async (path) => {
    const app = renderAt(path);
    await settle();

    const unexpected = [...new Set(liveCalls.map((call) => call.rpc).filter((rpc) => !(rpc in GLOBAL_READS) && !(rpc in PENDING_P4_1)))];
    expect(unexpected, "live calls on a sample route, outside GLOBAL_READS and PENDING_P4_1").toEqual([]);

    const naming = liveCalls.filter((call) => call.rpc in GLOBAL_READS && carriesSample(call.method.input, isMessage(call.input) ? call.input : create(call.method.input, call.input as never)));
    expect(naming.map((call) => call.rpc), "global reads that name the sample").toEqual([]);

    const unimplemented = [...new Set(scopeCalls.filter((call) => call.code === Code.Unimplemented).map((call) => call.rpc))];
    expect(unimplemented, "the sample's scope answered Unimplemented").toEqual([]);
    expect(scopeCalls.some((call) => call.code === null), "the sample's scope answered the page").toBe(true);

    const leaked = app.getQueryCache().findAll().filter((query) => keyText(query.queryKey).includes(SAMPLE_APP));
    expect(leaked.map((query) => query.queryKey), "the sample's reads in the app's own cache").toEqual([]);

    expect(document.body.textContent).not.toContain("appears once this project is deployed");
  }, 30_000);
});

/**
 * Steps whose target this file cannot see, with what it checks instead. Short
 * on purpose: e2e/journey-forge.spec.ts walks them in a browser.
 */
const NOT_IN_JSDOM: Record<string, { why: string; instead: string }> = {
  "fg-instrumented": {
    why: "its anchors (obs-overview, overview-services) are on no console Overview today: the card shows unanchored, on the demo host too",
    instead: "[data-testid='forge-console-page']",
  },
  "fg-hops": { why: "the span rows are a virtualized list, and jsdom lays nothing out", instead: "[data-testid='trace-detail']" },
};

describe("each step of the forge tour finds its target on the sample's pages", () => {
  setReliantTourHooks(signedInHooks);
  const STEPS = activeJourneySteps("forge").filter((step) => step.route?.open && typeof step.route.open !== "function" && presentStep(step).target);
  setReliantTourHooks(null);

  beforeEach(() => {
    setReliantTourHooks(signedInHooks);
    // The pages' clock at the story's (beforeAll): their time windows (the last hour of traces) then hold its requests.
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(STORY_NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
    setReliantTourHooks(null);
  });

  it.each(STEPS.map((step) => [step.id, step] as const))("%s", async (_id, step) => {
    const open = step.route!.open as RouteTarget;
    const search = Object.fromEntries(Object.entries(open.search ?? {}).filter(([, value]) => value !== undefined)) as Record<string, string>;
    renderAt(withSearch(open.to, search));
    await settle();
    const target = NOT_IN_JSDOM[step.id]?.instead ?? presentStep(step).target!;
    // The spotlight's selector, on the sample's own data: the nav's anchors, a trace row, a span, an issue.
    await act(async () => {
      for (let i = 0; i < 100 && !document.querySelector(target); i++) await new Promise((resolve) => setTimeout(resolve, 30));
    });
    expect(document.querySelector(target), target).not.toBeNull();
  }, 30_000);
});

