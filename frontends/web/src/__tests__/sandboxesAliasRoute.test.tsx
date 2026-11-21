/**
 * `/machines` and `/o/<org>/machines`: aliases for Agent sandboxes, Settings'
 * machines page (sandboxesAliasRoute.tsx, U6 12/16), through the PRODUCTION
 * route tree on both hosts and with `web.org_routes` off and on
 * (test/orgRouteHarness.ts).
 */

import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
// Pages are not rendered (the harness is headless): every lazy page is a stub.
vi.mock("@/lib/lazyRoute", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/lazyRoute")>()),
  lazyRoute: () => () => null,
}));

import { setTransport } from "@/lib/connect";
import { sandboxesPath } from "@/lib/paths";
import { getRouteOrgScope } from "@/lib/routeOrg";
import { resetShellForTests } from "@/lib/shell";
import { routeTree } from "@/routes";
import { createOrgRouteHarness, type OrgRouteHarness } from "@/test/orgRouteHarness";

const ACME = { id: "org-a", slug: "acme", name: "Acme" };
const SIDE = { id: "org-b", slug: "side", name: "Side" };
const HOSTS = ["reliant", "forge"] as const;
const FLAGS = [false, true] as const;

afterEach(() => {
  setTransport(null);
  resetShellForTests();
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

/** Where the router is, path + query + hash, as a URL names it. */
const where = (h: OrgRouteHarness) => h.router.state.location.href;

it("the alias's target is Settings' machines section", () => {
  expect(sandboxesPath()).toBe("/settings/machines");
});

describe.each(HOSTS)("the %s host", (host) => {
  describe.each(FLAGS)("web.org_routes %s", (flag) => {
    const open = async (url: string, orgs = [ACME, SIDE], tabOrg = "org-a") => {
      const h = createOrgRouteHarness({ host, routeTree, flag, orgs, tabOrg });
      await h.open(url);
      return h;
    };

    it("redirects /machines to /settings/machines", async () => {
      const h = await open("/machines");
      expect(h.pathname()).toBe("/settings/machines");
      expect(h.leafRouteId()).toMatch(/_authenticated\/settings\/\$section$/);
      expect(h.notFound()).toBe(false);
    });

    it("keeps the query and the hash", async () => {
      const h = await open("/machines?tab=cloud#add");
      expect(h.pathname()).toBe("/settings/machines");
      expect(where(h)).toBe("/settings/machines?tab=cloud#add");
    });

    it("replaces the alias in history, so Back does not bounce through it", async () => {
      const h = await open("/inbox");
      await h.navigate("/machines");
      expect(h.pathname()).toBe("/settings/machines");
      await h.back();
      expect(h.pathname()).toMatch(/\/inbox$/);
    });

    it("redirects /o/<member>/machines to the same page, keeping the query and hash", async () => {
      const h = await open("/o/acme/machines?tab=cloud#add");
      expect(h.pathname()).toBe("/settings/machines");
      expect(where(h)).toBe("/settings/machines?tab=cloud#add");
      expect(h.notFound()).toBe(false);
    });

    it("keeps /o/<non-member>/machines on the one no-access page", async () => {
      const h = await open("/o/acme/machines", [SIDE], "org-b");
      expect(h.pathname()).toBe("/o/acme/machines");
      expect(h.orgRoute()?.kind).toBe("not-member");
      expect(getRouteOrgScope()).toEqual({ kind: "denied", reason: "not-member" });
    });
  });
});
