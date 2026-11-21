/**
 * EVERY OLD URL STILL LANDS (U6, one shell; reliant is semver 1: old links,
 * bookmarks, notifications and older Electron builds never 404).
 *
 * One row per URL a link ever named — the pages the one shell retired or
 * folded together (lib/paths/legacy.ts RETIRED_PAGES), the forge host's own
 * pages, the console's retired paths (forgeConsoleRoutes.tsx), the Workflows
 * area's (workflowsAreaRoutes.tsx), the forge Settings slugs
 * (routeSchemas.ts SETTINGS_SECTION_ALIASES) — through the PRODUCTION route
 * tree (test/orgRouteHarness.ts), on both hosts, with `web.org_routes` off and
 * on. Each resolves: a redirect the router follows to the page that replaced
 * it, or a page (some, like `/forge?project=`, resolve their target on the
 * page, which this headless harness does not render), and never not-found.
 *
 * `lands` is the org-less path and query it ends on. With the flag off that is
 * the URL itself; with it on, a page the forwarding table names (lib/paths/
 * legacy.ts FORWARDED) moves into the person's org (`/o/acme/…`), and the rest
 * stay org-less.
 */

import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
// Pages are not rendered (the harness is headless): every lazy page is a stub.
vi.mock("@/lib/lazyRoute", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/lazyRoute")>()),
  lazyRoute: () => () => null,
}));

import { setTransport } from "@/lib/connect";
import { orgFreePath, orgOfPath } from "@/lib/paths/org";
import { resetShellForTests } from "@/lib/shell";
import { routeTree } from "@/routes";
import { createOrgRouteHarness } from "@/test/orgRouteHarness";

const ACME = { id: "org-a", slug: "acme", name: "Acme" };
const HOSTS = ["reliant", "forge"] as const;
const FLAGS = [false, true] as const;

interface LegacyRow {
  url: string;
  /** Where it ends, org-less: path and query. */
  lands: string;
  /** The committed leaf route, when the row pins the page. */
  leaf?: RegExp;
}

const ROWS: LegacyRow[] = [
  // The chat home and a project's chats.
  { url: "/", lands: "/", leaf: /\/_app\/$/ },
  { url: "/project", lands: "/", leaf: /\/_app\/$/ },
  { url: "/project/p-1", lands: "/project/p-1", leaf: /\/project\/\$projectId$/ },
  { url: "/inbox", lands: "/inbox", leaf: /_authenticated\/inbox$/ },
  // Apps and Sites are Projects' filters now; the query a claim carried rides along.
  { url: "/apps", lands: "/projects?show=apps", leaf: /_authenticated\/projects$/ },
  { url: "/apps?claimed=shop&kept=claimed", lands: "/projects?claimed=shop&kept=claimed&show=apps", leaf: /_authenticated\/projects$/ },
  { url: "/sites", lands: "/projects?show=sites", leaf: /_authenticated\/projects$/ },
  { url: "/o/acme/apps", lands: "/projects?show=apps", leaf: /^\/o\/\$org\/_authenticated\/projects$/ },
  { url: "/o/acme/sites", lands: "/projects?show=sites", leaf: /^\/o\/\$org\/_authenticated\/projects$/ },
  { url: "/usage", lands: "/usage", leaf: /_authenticated\/usage$/ },
  { url: "/projects", lands: "/projects", leaf: /_authenticated\/projects$/ },
  // The console's entries and the app by name.
  { url: "/forge", lands: "/forge", leaf: /\/forge$/ },
  { url: "/forge?project=x", lands: "/forge?project=x", leaf: /\/forge$/ },
  { url: "/forge/p/shop", lands: "/forge/p/shop", leaf: /\/forge\/p\/\$project\/$/ },
  { url: "/forge/p/shop/releases", lands: "/forge/p/shop/releases", leaf: /\/releases$/ },
  { url: "/forge/p/shop/observability/logs", lands: "/forge/p/shop/observability/logs", leaf: /\/observability\/\$view$/ },
  // Observability's own Overview (U6e): a page now, no longer a redirect to the project Overview.
  { url: "/forge/p/shop/observability", lands: "/forge/p/shop/observability", leaf: /\/observability$/ },
  { url: "/forge/observability", lands: "/forge/observability" },
  { url: "/forge/observability/logs", lands: "/forge/observability/logs" },
  { url: "/forge/env/prod?forgeProject=x", lands: "/forge/env/prod?forgeProject=x", leaf: /\/forge\/env\/\$env$/ },
  // The console's retired URLs (Console/paths.ts, forgeConsoleRoutes.tsx).
  { url: "/forge/domains", lands: "/forge/domains" },
  { url: "/forge/topology", lands: "/forge/topology" },
  { url: "/forge/environments", lands: "/forge/environments" },
  { url: "/forge/status", lands: "/forge/status" },
  { url: "/forge/secrets", lands: "/forge/secrets" },
  { url: "/forge/p/shop/monitoring", lands: "/forge/p/shop/observability/alerts" },
  { url: "/forge/p/shop/settings", lands: "/settings/billing" },
  { url: "/forge/observability/not-a-view", lands: "/forge/observability" },
  // The Workflows area's retired paths.
  { url: "/workflows", lands: "/workflows/library" },
  { url: "/workflows/automations", lands: "/workflows/library?show=on&allProjects=true" },
  { url: "/workflow", lands: "/workflows/library" },
  { url: "/runs", lands: "/workflows/runs" },
  { url: "/runs/c-1", lands: "/workflows/runs/c-1" },
  { url: "/automations", lands: "/workflows/library?show=on&allProjects=true" },
  { url: "/automations/t-1", lands: "/workflows/automations/t-1" },
  // The forge host's Settings sections, and the one Settings gained from it.
  { url: "/settings/profile", lands: "/settings/account" },
  { url: "/settings/organization", lands: "/settings/member-permissions" },
  { url: "/settings/members", lands: "/settings/member-permissions" },
  { url: "/settings/tokens", lands: "/settings/tokens", leaf: /_authenticated\/settings\/\$section$/ },
  // The forge shell's dev preview, and the phone's project filter.
  { url: "/forge-shell-preview", lands: "/projects", leaf: /_authenticated\/projects$/ },
  { url: "/m/projects", lands: "/m/projects", leaf: /\/m\/projects$/ },
];

afterEach(() => {
  setTransport(null);
  resetShellForTests();
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

describe.each(HOSTS)("the %s host", (host) => {
  describe.each(FLAGS)("web.org_routes %s", (flag) => {
    it.each(ROWS)("$url lands on $lands", async ({ url, lands, leaf }) => {
      const h = createOrgRouteHarness({ host, routeTree, flag, orgs: [ACME] });
      await h.open(url);
      expect(h.notFound()).toBe(false);
      const { pathname, searchStr } = h.router.state.location as { pathname: string; searchStr: string };
      // Sorted, so the row does not depend on the order a redirect wrote the keys in.
      const query = [...new URLSearchParams(searchStr).entries()].sort(([a], [b]) => a.localeCompare(b));
      const [landsPath, landsQuery = ""] = lands.split("?");
      expect(orgFreePath(pathname)).toBe(landsPath);
      expect(query).toEqual([...new URLSearchParams(landsQuery).entries()].sort(([a], [b]) => a.localeCompare(b)));
      // An org URL stays in its org; an org-less one moves into the person's only where the flag forwards it.
      if (url.startsWith("/o/")) expect(orgOfPath(pathname)).toBe("acme");
      else if (!flag) expect(orgOfPath(pathname)).toBeNull();
      if (leaf) expect(h.leafRouteId()).toMatch(leaf);
    });
  });
});

it("leaves a retired page in someone else's org on its no-access page, never redirected", async () => {
  // A non-member's /o/acme/apps stays put: the org layout shows its one no-access page.
  const h = createOrgRouteHarness({ host: "reliant", routeTree, flag: true, orgs: [{ id: "org-b", slug: "side" }] });
  await h.open("/o/acme/apps");
  expect(h.notFound()).toBe(false);
  expect(h.pathname()).toBe("/o/acme/apps");
  expect(h.orgRoute()?.kind).toBe("not-member");
});
