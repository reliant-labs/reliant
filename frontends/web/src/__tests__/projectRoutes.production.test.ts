/**
 * A project's URLs and every URL that moves the tab's org, through the
 * PRODUCTION route tree (test/orgRouteHarness.ts), with the flag
 * `web.org_routes` off and on (control-plane
 * docs/design/unified-shell-and-projects.md §3.4, D8: old URLs redirect to
 * their `/o/<org>/…` form; a deep link into another org switches the org with
 * a notice; a resource is never shown under an org it is not in).
 *
 *   - `/project/<id>` and `/projects/<id>[/settings|/workspaces]` go to the
 *     project's org (lib/projectRouteResolve.ts), keeping the query and hash;
 *   - a project URL naming the wrong org goes to the project's, with a
 *     notice; one the person cannot read here is not found, in place;
 *   - an arrival that moves the tab's org says so exactly once
 *     (lib/orgArrivalNotice.ts), and the person's own switch says nothing.
 */

import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
// Pages are not rendered (the harness is headless): every lazy page is a stub.
vi.mock("@/lib/lazyRoute", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/lazyRoute")>()),
  lazyRoute: () => () => null,
}));

import { setTransport } from "@/lib/connect";
import { switchTabOrg } from "@/lib/orgSwitch";
import type { ProjectRouteData } from "@/lib/projectRouteResolve";
import { resetShellForTests } from "@/lib/shell";
import { routeTree } from "@/routes";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { createOrgRouteHarness, type OrgRouteWorld } from "@/test/orgRouteHarness";

const ACME = { id: "org-a", slug: "acme", name: "Acme" };
const SIDE = { id: "org-b", slug: "side", name: "Side" };
const FLAGS = [false, true] as const;

const PROJECTS = [
  { id: "pa", orgId: "org-a" },
  { id: "pb", orgId: "org-b" },
  // Readable (shared with the person) but in an org that is not theirs.
  { id: "px", orgId: "org-x" },
  // From before orgs: its owner's default org's (acme, the first).
  { id: "old", orgId: "" },
];

/** A project's URL, by the page it opens (org-less). */
const PAGES = ["/project/$id", "/projects/$id", "/projects/$id/settings", "/projects/$id/workspaces"];
const page = (template: string, id: string) => template.replace("$id", id);

function world(flag: boolean, over: Partial<OrgRouteWorld> = {}): OrgRouteWorld {
  return { host: "reliant", routeTree, flag, orgs: [ACME, SIDE], projects: PROJECTS, ...over };
}

const data = (h: ReturnType<typeof createOrgRouteHarness>) => h.leafData() as ProjectRouteData;

afterEach(() => {
  setTransport(null);
  resetShellForTests();
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

describe("an org-less project URL", () => {
  describe.each(PAGES)("%s", (template) => {
    it("opens where it is with the flag off, asking nothing", async () => {
      const h = createOrgRouteHarness(world(false, { tabOrg: "org-a" }));
      const url = page(template, "pb");
      await h.open(url);
      expect(h.pathname()).toBe(url);
      expect(data(h)).toEqual({ kind: "unresolved" });
      expect(h.projectReads).toEqual([]);
      expect(h.notices).toEqual([]);
    });

    it("redirects into the tab's org with the flag on, keeping the query and hash, and says nothing", async () => {
      const h = createOrgRouteHarness(world(true, { tabOrg: "org-a" }));
      await h.open(`${page(template, "pa")}?panel=files#top`);
      expect(h.pathname()).toBe(`/o/acme${page(template, "pa")}`);
      expect(h.router.state.location.searchStr).toBe("?panel=files");
      expect(h.router.state.location.hash).toBe("top");
      expect(h.orgRoute()).toEqual({ kind: "member", org: { id: "org-a", slug: "acme" } });
      expect(data(h)).toEqual({ kind: "project", projectId: "pa" });
      // Found in the tab's org list: no GetProject.
      expect(h.projectReads).toEqual([]);
      expect(h.notices).toEqual([]);
    });

    it("redirects into the project's org, not the tab's, with the flag on: one notice", async () => {
      const h = createOrgRouteHarness(world(true, { tabOrg: "org-a" }));
      await h.open(`${page(template, "pb")}?panel=files`);
      expect(h.pathname()).toBe(`/o/side${page(template, "pb")}`);
      expect(h.router.state.location.searchStr).toBe("?panel=files");
      expect(useActiveOrgStore.getState().chosenId).toBe("org-b");
      expect(h.notices).toEqual(["This project is in Side, so it opened there."]);
    });
  });

  it("puts a project from before orgs in its owner's default org", async () => {
    const h = createOrgRouteHarness(world(true, { tabOrg: "org-b" }));
    await h.open("/projects/old");
    expect(h.pathname()).toBe("/o/acme/projects/old");
  });

  it.each(PAGES)("does not expose %s outside the person's memberships, even when shared", async (template) => {
    const h = createOrgRouteHarness(world(true, { tabOrg: "org-a" }));
    const url = page(template, "px");
    await h.open(url);
    expect(h.pathname()).toBe(url);
    expect(data(h)).toEqual({ kind: "missing", projectId: "px" });
    expect(useActiveOrgStore.getState().chosenId).toBe("org-a");
    expect(h.notices).toEqual([]);
  });

  it("shows an unknown project as not found, in place", async () => {
    const h = createOrgRouteHarness(world(true, { tabOrg: "org-a" }));
    await h.open("/project/nope");
    expect(h.pathname()).toBe("/project/nope");
    expect(data(h)).toEqual({ kind: "missing", projectId: "nope" });
  });

  it("opens where it is for a visitor who is not signed in", async () => {
    const h = createOrgRouteHarness(world(true, { user: null }));
    await h.open("/projects/pb");
    expect(h.pathname()).toBe("/projects/pb");
    expect(h.projectReads).toEqual([]);
  });
});

describe.each(FLAGS)("a project's org URL (flag %s)", (flag) => {
  it("opens a project in the URL's org as it is", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/acme/projects/pa/settings");
    expect(h.pathname()).toBe("/o/acme/projects/pa/settings");
    expect(data(h)).toEqual({ kind: "project", projectId: "pa" });
    expect(h.notices).toEqual([]);
  });

  it.each(PAGES)("sends %s in the wrong org to the project's own, with one notice", async (template) => {
    const h = createOrgRouteHarness(world(flag, { tabOrg: "org-a" }));
    await h.open(`/o/acme${page(template, "pb")}?panel=files`);
    expect(h.pathname()).toBe(`/o/side${page(template, "pb")}`);
    expect(h.router.state.location.searchStr).toBe("?panel=files");
    expect(h.orgRoute()).toEqual({ kind: "member", org: { id: "org-b", slug: "side" } });
    expect(useActiveOrgStore.getState().chosenId).toBe("org-b");
    expect(h.notices).toEqual(["This project is in Side, so it opened there."]);
  });

  it("says so even when the tab is in the project's org already: the URL named another", async () => {
    const h = createOrgRouteHarness(world(flag, { tabOrg: "org-b" }));
    await h.open("/o/acme/projects/pb");
    expect(h.pathname()).toBe("/o/side/projects/pb");
    expect(h.notices).toEqual(["This project is in Side, so it opened there."]);
  });

  it("never shows a project under an org it is not in: one whose org is not the person's is not found, in place", async () => {
    const h = createOrgRouteHarness(world(flag, { tabOrg: "org-a" }));
    await h.open("/o/acme/projects/px");
    expect(h.pathname()).toBe("/o/acme/projects/px");
    expect(data(h)).toEqual({ kind: "missing", projectId: "px" });
    expect(h.notFound()).toBe(false);
  });

  it("shows an unknown project as not found, in place", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/acme/project/nope");
    expect(h.pathname()).toBe("/o/acme/project/nope");
    expect(data(h)).toEqual({ kind: "missing", projectId: "nope" });
  });

  it("asks nothing about a project under an org the person is not in", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/stranger/projects/pb");
    expect(h.pathname()).toBe("/o/stranger/projects/pb");
    expect(h.orgRoute()?.kind).toBe("not-member");
    expect(h.projectReads).toEqual([]);
  });
});

describe.each(FLAGS)("an arrival in another org (flag %s)", (flag) => {
  it("switches the tab's org with exactly one notice for a deep link into another org", async () => {
    const h = createOrgRouteHarness(world(flag, { tabOrg: "org-a" }));
    await h.open("/o/side/inbox?x=1");
    expect(h.pathname()).toBe("/o/side/inbox");
    expect(useActiveOrgStore.getState().chosenId).toBe("org-b");
    expect(h.notices).toEqual(["Switched to Side to open this link."]);
    // Moving within the org says nothing more.
    await h.navigate("/o/side/projects");
    expect(h.notices).toEqual(["Switched to Side to open this link."]);
  });

  it("says so for a link followed in the app, and for Back into the other org", async () => {
    const h = createOrgRouteHarness(world(flag, { tabOrg: "org-a" }));
    await h.open("/o/acme/inbox");
    await h.navigate("/o/side/projects");
    expect(h.notices).toEqual(["Switched to Side to open this link."]);
    await h.back();
    expect(h.pathname()).toBe("/o/acme/inbox");
    expect(h.notices).toEqual(["Switched to Side to open this link.", "Switched to Acme to open this link."]);
  });

  it("says nothing when the URL's org is the tab's", async () => {
    const h = createOrgRouteHarness(world(flag, { tabOrg: "org-b" }));
    await h.open("/o/side/inbox");
    expect(h.notices).toEqual([]);
  });

  it("says nothing for a first visit in the person's default org", async () => {
    const h = createOrgRouteHarness(world(flag));
    await h.open("/o/acme/inbox");
    expect(h.notices).toEqual([]);
  });

  it("says nothing when the person switches org themselves", async () => {
    const h = createOrgRouteHarness(world(flag, { tabOrg: "org-a" }));
    await h.open("/o/acme/projects/pa");
    switchTabOrg({ id: "org-b", slug: "side" });
    // The switch is a navigation: done once Side's page has committed.
    await vi.waitFor(() => expect(useActiveOrgStore.getState().chosenId).toBe("org-b"));
    expect(h.pathname()).toBe("/o/side/projects");
    expect(h.notices).toEqual([]);
  });

  it("says nothing for an org page that is not the person's, and leaves the tab's org", async () => {
    const h = createOrgRouteHarness(world(flag, { tabOrg: "org-a" }));
    await h.open("/o/stranger/inbox");
    expect(useActiveOrgStore.getState().chosenId).toBe("org-a");
    expect(h.notices).toEqual([]);
  });
});

describe("old URLs with the flag on land on their org form", () => {
  it.each([
    ["/", "/o/side"],
    ["/projects?show=apps", "/o/side/projects"],
  ])("%s → %s in the tab's org, keeping the query, with no notice", async (from, to) => {
    const h = createOrgRouteHarness(world(true, { tabOrg: "org-b" }));
    await h.open(from);
    expect(h.pathname()).toBe(to);
    expect(h.router.state.location.searchStr).toBe(from.includes("?") ? `?${from.split("?")[1]}` : "");
    expect(h.notices).toEqual([]);
  });

  it.each(["/", "/projects"])("leaves %s where it is with the flag off", async (url) => {
    const h = createOrgRouteHarness(world(false, { tabOrg: "org-b" }));
    await h.open(url);
    expect(h.pathname()).toBe(url);
  });
});
