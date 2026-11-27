// @vitest-environment node
// Copyright (c) 2025 Reliant Labs

/**
 * EVERY LINK INTO THE APP IS BUILT BY lib/paths.
 *
 * The unified-shell design (control-plane docs/design/unified-shell-and-projects.md)
 * puts the org in the URL of every org-scoped page (U3: `/o/<org>/…`). That
 * is a change in one place — `withOrg` in lib/paths/org.ts and the builders
 * beside it — only while no link spells its path as a string. So, outside the
 * builders, no source file:
 *
 *   1. navigates to a path literal: a `to`/`href`, `navigate(…)`,
 *      `redirect(…)`, `window.location`, `history.pushState(…)`, … given a
 *      string starting with `/` (`navigation` sites);
 *   2. spells one of the app's routes anywhere else — a constant, a nav item,
 *      a return value — except to MATCH a pathname against it (`route` sites).
 *
 * The analysis is static (test/navPathAnalysis.ts), and its header says what
 * it cannot see: a passing guard is not a proof, and the real-router tests
 * (lib/paths/__tests__, test/orgRouteHarness.ts) are what show destinations
 * resolve. A deliberate exception goes in ALLOWED with the reason it is safe
 * and, for a route literal, its exact text, so a file allowed to MATCH paths
 * still cannot spell a new destination.
 */

import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { findPathSites, scanSource, type PathSite } from "@/test/navPathAnalysis";

const SRC = join(__dirname, "..");

/** The builder modules: the only files that spell destinations. */
const BUILDERS = new Set([
  "lib/paths/index.ts",
  "lib/paths/org.ts",
  "lib/paths/search.ts",
  // The org-less paths that forward into an org (U3): a table of today's paths, one place.
  "lib/paths/legacy.ts",
  "components/Forge/Console/paths.ts",
]);

/**
 * Route literals allowed outside a navigation, by file: why, and the exact
 * texts. Each file is held to its list, so a NEW destination spelled there
 * (an outbound path that should go through a builder) fails, even in a file
 * that legitimately matches paths. Every one listed must still be used.
 */
const ALLOWED_ROUTE: Record<string, { why: string; texts: readonly string[] }> = {
  "lib/workflowsArea.ts": {
    why: "Matchers: the area's paths as a pathname is tested against them (which tab, is this a workflow page). Its destinations, the retired paths' included, go through lib/paths.",
    texts: ["/workflows", "/workflows/library", "/workflows/runs", "/workflow", "/workflows/automations"],
  },
  "lib/mobileRedirect.ts": {
    why: "Matchers: the desktop paths a phone is never handed off from. Each desktop path's mobile twin is built by lib/paths.",
    texts: ["/auth", "/oauth", "/connections/oauth", "/onboarding", "/reset-password", "/verify-email", "/upgrade", "/claim", "/accept-invite", "/design-sandbox", "/forge", "/projects", "/apps", "/sites", "/connectors", "/usage", "/gtm"],
  },
  "lib/monacoPreload.ts": {
    why: "Matchers: the entry pages the editor preload skips.",
    texts: ["/auth", "/oauth", "/reset-password", "/verify-email", "/upgrade", "/claim"],
  },
  "components/OnboardingFlow/onboardingGatePolicy.ts": {
    why: "Matchers: the chat routes the onboarding gate guards, and the homes the forge brand sends to Apps.",
    texts: ["/project", "/m/chats", "/m/new", "/m"],
  },
  "components/Onboarding/journeys/workflows.tsx": {
    why: "Tour step route prefixes, matched against the pathname (useTourNavigation isOnRoute, org-less). The step's link is built by its `open`.",
    texts: ["/workflow/", "/project/"],
  },
  "services/controlPlane/pairing.ts": {
    why: "Control-plane HTTP endpoints (`/oauth/device/lookup`), not pages of this app.",
    texts: ["/oauth/device/lookup", "/oauth/device/approve"],
  },
  "lib/auth/native-login.ts": {
    why: "reliant-api login-broker HTTP endpoints (`POST /auth/login`, reached at reliantForgeBase()), not pages of this app. The SPA pages that share a few of these paths (`/auth/reset`, `/auth/verify`, `/auth/social/callback`) are built by lib/paths.",
    texts: [
      "/auth/login",
      "/auth/register",
      "/auth/logout",
      "/auth/forgot",
      "/auth/reset",
      "/auth/passkey/begin",
      "/auth/passkey/finish",
      "/auth/passkey/register/begin",
      "/auth/passkey/register/finish",
      "/auth/magic/request",
      "/auth/magic/consume",
      "/auth/social/start",
      "/auth/social/callback",
      "/auth/invite/accept",
      "/auth/verify",
      "/auth/verify/resend",
    ],
  },
  "lib/auth/account.ts": {
    why: "reliant-api login-broker account-settings HTTP endpoints (`GET /auth/account`, forge#2663), not pages of this app. The page a confirmation email opens (`/auth/account/email/confirm`) is built by lib/paths.",
    texts: [
      "/auth/account",
      "/auth/reauth",
      "/auth/account/email",
      "/auth/account/email/confirm",
      "/auth/account/password",
      "/auth/account/links/start",
      "/auth/account/links/${segment(provider)}",
      "/auth/account/passkeys/${segment(id)}",
      "/auth/account/sessions",
      "/auth/account/sessions/${segment(handle)}",
      "/auth/account/sessions/revoke-others",
    ],
  },
};

/** Files allowed to navigate to a path literal, by why. */
const ALLOWED_NAVIGATION: Record<string, string> = {
  "components/DesignSandbox/forgeTokens/ForgeTokenSandbox.tsx": "A dev-only harness whose links point at its own dev-only route; never org-scoped.",
};

const { sites, roots } = findPathSites(SRC);
const outside = sites.filter((site) => !BUILDERS.has(site.file));

const describeSite = (site: PathSite) => `  ${site.where}: ${site.text}`;

describe("app paths", () => {
  it("knows the app's routes", () => {
    // A guard that silently stopped seeing routes would pass forever.
    for (const root of ["settings", "workflows", "workflow", "forge", "m", "project", "inbox", "apps", "onboarding", "auth"]) {
      expect(roots).toContain(root);
    }
  });

  it("sees the builders' own paths", () => {
    // …and one that stopped seeing literals would too: the builders spell every route.
    const inBuilders = sites.filter((site) => site.file === "lib/paths/index.ts");
    expect(inBuilders.length).toBeGreaterThan(40);
  });

  it("catches each way of spelling a destination, and spares matching", () => {
    const kinds = (code: string) => scanSource("probe.tsx", code, new Set(["settings", "inbox", "auth"])).map((site) => site.kind);
    for (const nav of [
      `navigate({ to: "/settings/$section", params: { section: "ai" } })`,
      `const a = <Link to="/inbox" />`,
      `const a = <a href={"/x"} />`,
      `const a = { href: cond ? "/inbox" : "/" }`,
      `navigate({ href: \`/settings/\${id}\` })`,
      `throw redirect({ href: "/inbox", replace: true })`,
      `window.location.href = "/auth"`,
      `window.location.assign("/auth")`,
      `history.replaceState(null, "", "/inbox")`,
      `hardNavigate("/auth?redirect=%2Fclaim")`,
      `navigate({ to: "/auth", search: { redirect: "/inbox" } })`,
      // A route interpolated into a template-string destination (review of #2931).
      `navigate({ href: \`\${"/inbox"}\` })`,
      `navigate({ href: \`x\${"/inbox"}y\` })`,
      `const a = <Link to={\`\${cond ? "/inbox" : "/auth"}?q=1\`} />`,
      `window.location.href = \`\${encodeURIComponent("/inbox")}\``,
    ]) {
      expect(kinds(nav), nav).toContain("navigation");
    }
    // The interpolated route itself is the site, not only the template around it.
    for (const nav of [`navigate({ href: \`/settings/\${"/inbox"}\` })`, `navigate({ href: \`x\${"/inbox"}\` })`]) {
      const sites = scanSource("probe.tsx", nav, new Set(["settings", "inbox", "auth"]));
      expect(sites.filter((site) => site.text === "/inbox").map((site) => site.kind), nav).toEqual(["navigation"]);
    }
    for (const route of [
      `const BILLING = "/settings/billing"`,
      `const items = [{ path: x, target: "/inbox" }]`,
      `return "/inbox"`,
      // A \`path\` that is not a route's own declaration (review of #2931, R1).
      `const items = [{ path: "/inbox" }]`,
      `const nav = { id: "/inbox", path: x }`,
    ]) {
      expect(kinds(route), route).toEqual(["route"]);
    }
    // Navigations the guard used to miss (review of #2931, R1).
    for (const nav of [
      // A constant holding a path, navigated to by name.
      `const HOME = "/"; navigate({ to: HOME })`,
      `const INBOX = "/inbox"; const a = <Link to={INBOX} />`,
      // An absolute URL of this app's own origin.
      `window.location.href = \`\${window.location.origin}/inbox\``,
      `navigate({ href: \`\${location.origin}/settings/ai\` })`,
    ]) {
      expect(kinds(nav), nav).toContain("navigation");
    }
    for (const fine of [
      `if (pathname === "/inbox") go()`,
      `pathname.startsWith("/settings/")`,
      `switch (p) { case "/inbox": break }`,
      `createRoute({ path: "/settings/$section" })`,
      `createRoute({ getParentRoute, id: "/inbox" })`,
      `legacyRoute("/settings")`,
      `const SEP = "/"; parts.join(SEP)`,
      `const url = \`\${forgeAppOrigin()}/apps\``,
      `useSearch({ from: "/_app" })`,
      `isAtOrBelow(pathname, "/settings")`,
      `type T = { to: "/inbox" }`,
      `const api = "/v1/projects"`,
      `navigate({ to: inboxPath() })`,
      `const url = "https://example.com/settings"`,
    ]) {
      expect(kinds(fine), fine).toEqual([]);
    }
  });

  it("navigates only to built paths", () => {
    const offenders = outside.filter((site) => site.kind === "navigation" && !(site.file in ALLOWED_NAVIGATION));
    expect(
      offenders,
      `${offenders.length} navigation(s) to a path literal: build the path with lib/paths (or Console/paths for the console)\n${offenders.map(describeSite).join("\n")}`,
    ).toEqual([]);
  });

  it("spells a route only to match a pathname", () => {
    const offenders = outside.filter((site) => site.kind === "route" && !ALLOWED_ROUTE[site.file]?.texts.includes(site.text));
    expect(
      offenders,
      `${offenders.length} route path literal(s) outside lib/paths: build it with a builder, or (to match a pathname) compare it directly\n${offenders.map(describeSite).join("\n")}`,
    ).toEqual([]);
  });

  it("allows nothing it no longer needs", () => {
    const used = new Set(outside.map((site) => site.file));
    const stale = [...Object.keys(ALLOWED_ROUTE), ...Object.keys(ALLOWED_NAVIGATION)].filter((file) => !used.has(file));
    expect(stale, "allowlist entries with nothing left to allow").toEqual([]);
    const staleTexts = Object.entries(ALLOWED_ROUTE).flatMap(([file, { texts }]) =>
      texts.filter((text) => !outside.some((site) => site.file === file && site.kind === "route" && site.text === text)).map((text) => `${file}: ${text}`),
    );
    expect(staleTexts, "allowed route literals no longer spelled").toEqual([]);
  });
});
