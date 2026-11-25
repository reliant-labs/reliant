/**
 * The org routes through the PRODUCTION route tree, on both hosts — the app
 * host and the forge host mount the same tree, routes.tsx (U6), the host
 * deciding only the brand (test/orgRouteHarness.ts): where each URL lands with the flag
 * `web.org_routes` off and on, what a member, a non-member and a signed-out
 * visitor get, and which org the URL, the query keys and the wire agree on
 * while a navigation into another org loads, commits, or is superseded.
 * (control-plane docs/design/unified-shell-and-projects.md §3, §12; the
 * 2026-11-19 review of reliant#2935, R4–R7.)
 */

import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
// Pages are not rendered (the harness is headless), and loading their chunks
// would only slow the router down: every lazy page is a stub here.
vi.mock("@/lib/lazyRoute", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/lazyRoute")>()),
  lazyRoute: () => () => null,
}));

import { homeTarget, routeRequiresOnboarding } from "@/components/OnboardingFlow/onboardingGatePolicy";
import { projectsKey } from "@/hooks/project-queries";
import { projectsPath } from "@/lib/paths";
import { mobileHandoffAllowed } from "@/lib/mobileRedirect";
import { getOrgScope, requestOrgOfKey } from "@/lib/orgScope";
import { getRouteOrgScope, isMemberOnly, ORG_ROUTE_ID } from "@/lib/routeOrg";
import { setTransport } from "@/lib/connect";
import { resetShellForTests } from "@/lib/shell";
import { routeTree } from "@/routes";
import { inOrg } from "@/lib/paths/org";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { createOrgRouteHarness, type OrgRouteWorld } from "@/test/orgRouteHarness";

const ACME = { id: "org-a", slug: "acme", name: "Acme" };
const SIDE = { id: "org-b", slug: "side", name: "Side" };
const HOSTS = ["reliant", "forge"] as const;
const FLAGS = [false, true] as const;

function world(host: "reliant" | "forge", flag: boolean, over: Partial<OrgRouteWorld> = {}): OrgRouteWorld {
  return { host, routeTree, flag, orgs: [ACME, SIDE], ...over };
}

afterEach(() => {
  setTransport(null);
  resetShellForTests();
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

/**
 * Each canonical org URL, where it lands, by the leaf route it commits: the
 * same on both hosts, which mount one tree. (The home sends a person who
 * cannot chat yet to their projects from the page, not the route:
 * onboardingGatePolicy.ts, homeTarget; __tests__/daemonlessShell.routes.test.tsx.)
 */
const DIRECT: { url: string; lands: [path: string, leaf: RegExp] }[] = [
  { url: "/o/acme", lands: ["/o/acme", /\/_app\/$/] },
  { url: "/o/acme/inbox", lands: ["/o/acme/inbox", /_authenticated\/inbox$/] },
  { url: "/o/acme/project/p-1", lands: ["/o/acme/project/p-1", /\/project\/\$projectId$/] },
  { url: "/o/acme/workflows", lands: ["/o/acme/workflows/library", /\/workflows\/library$/] },
  { url: "/o/acme/workflow/new", lands: ["/o/acme/workflow/new", /\/workflow\/new$/] },
  { url: "/o/acme/projects", lands: ["/o/acme/projects", /_authenticated\/projects$/] },
  { url: "/o/acme/apps/shop/releases", lands: ["/o/acme/apps/shop/releases", /\/releases$/] },
  { url: "/o/acme/observability/logs", lands: ["/o/acme/observability/logs", /\/observability\/\$view$/] },
  { url: "/o/acme/usage", lands: ["/o/acme/usage", /^\/o\/\$org\/_authenticated\/usage$/] },
  { url: "/o/org-a/inbox", lands: ["/o/acme/inbox", /_authenticated\/inbox$/] },
];

describe.each(HOSTS)("the %s host", (host) => {
  describe.each(FLAGS)("web.org_routes %s", (flag) => {
    it.each(DIRECT)("serves $url to a member, in its org", async (row) => {
      const h = createOrgRouteHarness(world(host, flag));
      await h.open(row.url);
      const [path, leaf] = row.lands;
      expect(h.pathname()).toBe(path);
      expect(h.leafRouteId()).toMatch(leaf);
      expect(h.notFound()).toBe(false);
      expect(h.orgRoute()).toEqual({ kind: "member", org: { id: "org-a", slug: "acme" } });
      // Committed: the scope, the tab's org and the wire all name it.
      expect(getRouteOrgScope()).toEqual({ kind: "member", org: { id: "org-a", slug: "acme" } });
      expect(useActiveOrgStore.getState().chosenId).toBe("org-a");
      expect(await h.sendImplicit()).toBe("org-a");
    });

    it.each([
      "/o/stranger",
      "/o/stranger/workflows",
      "/o/stranger/workflows/automations",
      "/o/stranger/apps/shop/settings",
      "/o/stranger/apps/shop/observability",
      "/o/stranger/apps/shop/monitoring",
      "/o/stranger/observability/not-a-view",
      "/o/stranger/sites",
      "/o/stranger/inbox",
    ])("keeps %s on the no-access page: no child redirects, nothing named", async (url) => {
      const h = createOrgRouteHarness(world(host, flag, { tabOrg: "org-b" }));
      await h.open(url);
      expect(h.pathname()).toBe(url);
      expect(h.orgRoute()?.kind).toBe("not-member");
      expect(getRouteOrgScope()).toEqual({ kind: "denied", reason: "not-member" });
      // The tab's previous org is neither moved nor used.
      expect(useActiveOrgStore.getState().chosenId).toBe("org-b");
      expect(getOrgScope().ready).toBe(false);
      await expect(h.sendImplicit()).rejects.toThrow(/not one of yours/);
      expect(h.wireOrgs).toEqual([]);
      // The phone hand-off does not run from it.
      expect(mobileHandoffAllowed({ pathname: url, committedPathname: url, routeOrgScope: getRouteOrgScope() })).toBe(false);
    });

    it("keeps the destination org when app settings redirects to usage", async () => {
      const h = createOrgRouteHarness(world(host, flag, { tabOrg: "org-a" }));
      await h.open("/o/acme/apps/shop/releases");
      expect(await h.sendImplicit()).toBe("org-a");

      await h.navigate("/o/side/apps/shop/settings");
      expect(h.pathname()).toBe("/o/side/usage");
      expect(getOrgScope().orgId).toBe("org-b");
      expect(await h.sendImplicit()).toBe("org-b");
      expect(await h.sendExplicit(getOrgScope().orgId)).toBe("org-b");
    });

    it("keeps an org URL in place for a visitor who is not signed in", async () => {
      const h = createOrgRouteHarness(world(host, flag, { user: null }));
      await h.open("/o/acme/workflows");
      expect(h.pathname()).toBe("/o/acme/workflows");
      expect(h.orgRoute()).toEqual({ kind: "signed-out" });
      expect(getRouteOrgScope()).toEqual({ kind: "denied", reason: "signed-out" });
    });

    it("keeps an org URL in place when the membership list fails", async () => {
      const h = createOrgRouteHarness(world(host, flag, { membershipError: new Error("offline"), tabOrg: "org-b" }));
      await h.open("/o/acme/apps/shop/settings");
      expect(h.pathname()).toBe("/o/acme/apps/shop/settings");
      expect(h.orgRoute()?.kind).toBe("error");
      expect(useActiveOrgStore.getState().chosenId).toBe("org-b");
    });
  });
});

describe("a navigation from org A to org B", () => {
  /** What every consumer names right now. */
  async function named(h: ReturnType<typeof createOrgRouteHarness>) {
    return {
      scope: getRouteOrgScope().kind === "member" ? (getRouteOrgScope() as { org: { id: string } }).org.id : getRouteOrgScope().kind,
      queryKey: requestOrgOfKey(projectsKey()),
      explicit: await h.sendExplicit(getOrgScope().orgId),
      implicit: await h.sendImplicit(),
      tab: useActiveOrgStore.getState().chosenId,
    };
  }
  const allNamed = (org: string) => ({ scope: org, queryKey: org, explicit: org, implicit: org, tab: org });

  it.each(FLAGS)("names A everywhere while B loads, and B everywhere once it commits (flag %s)", async (flag) => {
    const h = createOrgRouteHarness(world("reliant", flag));
    await h.open("/o/acme/inbox");
    expect(await named(h)).toEqual(allNamed("org-a"));

    const release = h.holdMemberships();
    const loading = h.navigate("/o/side/inbox");
    await vi.waitFor(() => expect(h.membershipCalls()).toBeGreaterThan(1));
    // B is loading: the URL on screen, the keys and the wire are all still A.
    expect(h.router.state.matches.at(-1)?.pathname).toBe("/o/acme/inbox");
    expect(await named(h)).toEqual(allNamed("org-a"));

    release();
    await loading;
    expect(h.pathname()).toBe("/o/side/inbox");
    expect(await named(h)).toEqual(allNamed("org-b"));
  });

  it("leaves nothing of B behind when the load is superseded before it commits", async () => {
    const h = createOrgRouteHarness(world("reliant", true));
    await h.open("/o/acme/inbox");
    const release = h.holdMemberships();
    const abandoned = h.navigate("/o/side/inbox");
    await vi.waitFor(() => expect(h.membershipCalls()).toBeGreaterThan(1));
    // The person goes elsewhere before B resolves: a page outside the org tree.
    await h.navigate("/settings/account");
    expect(h.pathname()).toBe("/settings/account");
    expect(getRouteOrgScope()).toEqual({ kind: "legacy" });
    release();
    await abandoned;
    // B never committed: the tab is still in A, and so is everything that reads it.
    expect(h.pathname()).toBe("/settings/account");
    expect(useActiveOrgStore.getState().chosenId).toBe("org-a");
    expect(getRouteOrgScope()).toEqual({ kind: "legacy" });
    expect(await h.sendImplicit()).toBe("org-a");
  });

  it("holds an implicit request from a freshly loaded org page until the org resolves", async () => {
    const h = createOrgRouteHarness(world("reliant", true, { tabOrg: "org-b" }));
    const release = h.holdMemberships();
    const loading = h.open("/o/acme/inbox");
    await vi.waitFor(() => expect(h.membershipCalls()).toBe(1));
    expect(getRouteOrgScope()).toEqual({ kind: "pending" });
    expect(getOrgScope().ready).toBe(false);
    const request = h.sendImplicit();
    release();
    await loading;
    // Not the tab's previous org: the one the URL named, once it was known.
    expect(await request).toBe("org-a");
  });
});

describe("org-less paths with the flag on (the Reliant host forwards; R5, R7)", () => {
  it.each([
    ["/", "/o/side"],
    ["/inbox", "/o/side/inbox"],
    ["/workflows/library", "/o/side/workflows/library"],
    ["/workflows/library/builtin%3A%2F%2Fget-it-right", "/o/side/workflows/library/builtin%3A%2F%2Fget-it-right"],
    ["/workflow/new", "/o/side/workflow/new"],
    ["/forge/observability/logs", "/o/side/observability/logs"],
    // An app's console by name (U18): the tab's org, which its reads already used.
    ["/forge/p/shop", "/o/side/apps/shop"],
    ["/forge/p/shop/releases", "/o/side/apps/shop/releases"],
    ["/apps/shop", "/o/side/apps/shop"],
  ])("forwards %s into the tab's org: %s", async (from, to) => {
    const h = createOrgRouteHarness(world("reliant", true, { tabOrg: "org-b" }));
    await h.open(from);
    expect(h.pathname()).toBe(to);
    expect(h.notFound()).toBe(false);
  });

  it("keeps the query of an app's console link, `?project=` (the checkout) included", async () => {
    const h = createOrgRouteHarness(world("reliant", true, { tabOrg: "org-b" }));
    await h.open("/forge/p/shop/releases?project=p-1&env=prod");
    expect(h.pathname()).toBe("/o/side/apps/shop/releases");
    expect(h.search()).toMatchObject({ project: "p-1", env: "prod" });
  });

  it.each(["/workflows/library/my-saved-flow", "/workflow/my-saved-flow", "/project/p-1", "/workflows/runs/c-1"])(
    "leaves %s, which names a resource of its own org, where it is",
    async (path) => {
      const h = createOrgRouteHarness(world("reliant", true, { tabOrg: "org-b" }));
      await h.open(path);
      expect(h.pathname()).toBe(path);
    },
  );

  it.each([
    ["/sites", "/o/side/projects"],
    ["/usage", "/o/side/usage"],
    ["/apps", "/o/side/projects"],
  ])("forwards on the forge host too, which mounts the same tree (U6): %s → %s", async (from, to) => {
    const h = createOrgRouteHarness(world("forge", true, { tabOrg: "org-b" }));
    await h.open(from);
    expect(h.pathname()).toBe(to);
    expect(h.notFound()).toBe(false);
  });

  it("does nothing with the flag off", async () => {
    const h = createOrgRouteHarness(world("reliant", false, { tabOrg: "org-b" }));
    await h.open("/inbox");
    expect(h.pathname()).toBe("/inbox");
    expect(getRouteOrgScope()).toEqual({ kind: "legacy" });
    expect(await h.sendImplicit()).toBe("org-b");
  });
});

it("reads an org id before a slug, as the control plane does (#2938)", async () => {
  // A historical slug equal to another org's id: the id wins on both sides.
  const h = createOrgRouteHarness(world("reliant", true, { orgs: [{ id: "org-a", slug: "org-b" }, { id: "org-b", slug: "side" }] }));
  await h.open("/o/org-b/inbox");
  expect(h.pathname()).toBe("/o/side/inbox");
  expect(h.orgRoute()).toEqual({ kind: "member", org: { id: "org-b", slug: "side" } });
});

describe.each(HOSTS)("every redirect under the %s host's org layout", (host) => {
  it("runs only in a member org (memberOnly)", () => {
    const visit = (route: { id: string; options: { beforeLoad?: unknown }; children?: unknown }, out: string[]) => {
      if (route.id.startsWith(`${ORG_ROUTE_ID}/`) && route.options.beforeLoad && !isMemberOnly(route.options.beforeLoad)) out.push(route.id);
      const children = route.children as Record<string, typeof route> | (typeof route)[] | undefined;
      for (const child of Array.isArray(children) ? children : Object.values(children ?? {})) visit(child, out);
      return out;
    };
    const unguarded = visit(world(host, false).routeTree as never, []);
    expect(unguarded).toEqual([]);
    // …and the walk saw the org tree's redirects at all.
    const all: string[] = [];
    const count = (route: { id: string; options: { beforeLoad?: unknown }; children?: unknown }) => {
      if (route.id.startsWith(`${ORG_ROUTE_ID}/`) && route.options.beforeLoad) all.push(route.id);
      const children = route.children as Record<string, typeof route> | (typeof route)[] | undefined;
      for (const child of Array.isArray(children) ? children : Object.values(children ?? {})) count(child);
    };
    count(world(host, false).routeTree as never);
    expect(all.length).toBeGreaterThan(3);
  });
});

/**
 * The one shell's pages, on both hosts × the flag × a member of the URL's org
 * or not (U6): every page every host serves, in the form a link or a bookmark
 * names it. "Not a member" here is a person in Side only: an org-less page
 * forwards into THEIR org with the flag on, and Acme's URLs show the one
 * no-access page.
 */
describe("the one shell's pages", () => {
  /** A page: whether its org-less form forwards with the flag on, and its leaf (either form). */
  const PAGES: { url: string; forwards: boolean; leaf: RegExp }[] = [
    { url: "/", forwards: true, leaf: /\/_app\/$/ },
    { url: "/projects", forwards: true, leaf: /_authenticated\/projects$/ },
    { url: "/usage", forwards: true, leaf: /_authenticated\/usage$/ },
    // An app by name: into the tab's org (U18), until the resolver (U4) names the app's own.
    { url: "/forge/p/shop", forwards: true, leaf: /\/(forge\/p|apps)\/\$project\/$/ },
    { url: "/o/acme/apps/shop", forwards: false, leaf: /\/apps\/\$project\/$/ },
    { url: "/o/acme/usage", forwards: false, leaf: /_authenticated\/usage$/ },
    // Not org-scoped yet (U8).
    { url: "/settings/tokens", forwards: false, leaf: /_authenticated\/settings\/\$section$/ },
  ];
  const MEMBERSHIP = [
    ["a member of acme", [ACME, SIDE], "org-a", "acme"],
    ["not a member of acme", [SIDE], "org-b", "side"],
  ] as const;

  describe.each(HOSTS)("the %s host", (host) => {
    describe.each(FLAGS)("web.org_routes %s", (flag) => {
      describe.each(MEMBERSHIP)("%s", (_who, orgs, tabOrg, segment) => {
        it.each(PAGES)("$url", async ({ url, forwards, leaf }) => {
          const h = createOrgRouteHarness(world(host, flag, { orgs: [...orgs], tabOrg }));
          await h.open(url);
          expect(h.notFound()).toBe(false);
          if (url.startsWith("/o/acme") && segment !== "acme") {
            // Someone else's org: its one no-access page, in place.
            expect(h.pathname()).toBe(url);
            expect(h.orgRoute()?.kind).toBe("not-member");
            return;
          }
          expect(h.pathname()).toBe(forwards && flag ? inOrg(segment, url) : url);
          expect(h.leafRouteId()).toMatch(leaf);
          // A daemonless person (U6): only the home is a chat route, and only
          // the home sends them to Projects; every other page works in place.
          const home = url === "/";
          expect(routeRequiresOnboarding(h.pathname())).toBe(home);
          for (const setup of ["onboard", "connect-machine"] as const) {
            expect(homeTarget(h.pathname(), setup, { forgeBrand: host === "forge" })).toBe(home ? projectsPath() : null);
          }
        });
      });
    });
  });
});
