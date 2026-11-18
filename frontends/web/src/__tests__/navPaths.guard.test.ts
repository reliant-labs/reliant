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
 * The analysis is static (test/navPathAnalysis.ts). A deliberate exception
 * goes in ALLOWED with the reason it is safe; the matchers there are the
 * pathname tests U3 has to teach the org segment as well.
 */

import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { findPathSites, scanSource, type PathSite } from "@/test/navPathAnalysis";

const SRC = join(__dirname, "..");

/** The builder modules: the only files that spell destinations. */
const BUILDERS = new Set(["lib/paths/index.ts", "lib/paths/org.ts", "lib/paths/search.ts", "components/Forge/Console/paths.ts"]);

/** Files allowed to spell a route outside a navigation, by why. */
const ALLOWED_ROUTE: Record<string, string> = {
  "lib/workflowsArea.ts":
    "Matchers: the area's paths as a pathname is tested against them (which tab, is this a workflow page), and the retired paths' translation to the new ones. Its links go through lib/paths.",
  "lib/mobileRedirect.ts":
    "Matchers and a translation: which desktop paths stay desktop on a phone, and each desktop path's mobile twin. Rewritten with the mobile tree's move under /o/<org>/m (U15).",
  "lib/monacoPreload.ts": "Matchers: the entry pages the editor preload skips.",
  "components/OnboardingFlow/onboardingGate.ts": "Matchers: the path prefixes the onboarding gate lets through.",
  "services/controlPlane/pairing.ts": "Control-plane HTTP endpoints (`/oauth/device/lookup`), not pages of this app.",
};

/** Files allowed to navigate to a path literal, by why. */
const ALLOWED_NAVIGATION: Record<string, string> = {
  "components/forge-ui/ForgeTokenSandbox.tsx": "A dev-only harness whose links point at its own dev-only route; never org-scoped.",
};

/**
 * Files U1's codemod PRs ("refactor(web): typed paths for every destination
 * (codemod N/M)") move onto the builders. Each PR deletes its lines; the last
 * deletes the list. Never add to it.
 */
const PENDING_CODEMOD = new Set<string>([
  "ModernApp.tsx",
  "api/transport.ts",
  "components/AnonSignInNudge.tsx",
  "components/AppOutOfDateModal.tsx",
  "components/Apps/AppsPage.tsx",
  "components/Apps/DeployedAppCard.tsx",
  "components/Apps/SidebarApps.tsx",
  "components/Apps/startBuilding.ts",
  "components/Auth/CliLogin.tsx",
  "components/Auth/DevicePairing.tsx",
  "components/Auth/OAuthConsent.tsx",
  "components/AuthGuard.tsx",
  "components/AuthScreen.tsx",
  "components/Automations/AutomationCta.tsx",
  "components/Billing/LowCreditIndicator.tsx",
  "components/Billing/MachinePlanChange.tsx",
  "components/Chat/OomKillBanner.tsx",
  "components/Chat/PersonalFundingPrompt.tsx",
  "components/Chat/WorkflowErrorMessage.tsx",
  "components/Chat/settings/ModelSettingsPage.tsx",
  "components/Chat/tool-renderers/RunToolRenderer.tsx",
  "components/Claim/ExistingAccount.tsx",
  "components/Claim/claimCopy.ts",
  "components/Claim/useClaimFlow.ts",
  "components/DaemonWaitState.tsx",
  "components/EmailVerification.tsx",
  "components/Forge/Console/ForgeEntryRoutes.tsx",
  "components/Forge/Demo/DemoBanner.tsx",
  "components/Forge/Demo/demoEntry.ts",
  "components/Forge/Demo/demoPath.ts",
  "components/Forge/Observability/AskReliant/AskReliant.tsx",
  "components/Forge/Observability/Dashboards/LLMUsageView.tsx",
  "components/Forge/Observability/Dashboards/OrganizationBillingView.tsx",
  "components/Forge/Observability/Dashboards/OrganizationUsageView.tsx",
  "components/Forge/Observability/ObsStates.tsx",
  "components/Forge/Observability/UsageView.tsx",
  "components/Forge/Org/OrgConsoleLayout.tsx",
  "components/ForgeShell/ForgeAuthGuard.tsx",
  "components/ForgeShell/ForgeSettingsPage.tsx",
  "components/ForgeShell/ForgeShellLayout.tsx",
  "components/ForgotPassword.tsx",
  "components/GitHubOAuthCallback.tsx",
  "components/Layout/DaemonStatusDot.tsx",
  "components/Layout/Header.tsx",
  "components/Layout/LoadingSpinner.tsx",
  "components/Mobile/MobileAccountScreen.tsx",
  "components/Mobile/MobileAutomationRedirect.tsx",
  "components/Mobile/MobileBillingScreen.tsx",
  "components/Mobile/MobileChatList.tsx",
  "components/Mobile/MobileChatSearchTab.tsx",
  "components/Mobile/MobileChatSheet.tsx",
  "components/Mobile/MobileChatTopBar.tsx",
  "components/Mobile/MobileComingSoon.tsx",
  "components/Mobile/MobileDaemonList.tsx",
  "components/Mobile/MobileDaemonScreen.tsx",
  "components/Mobile/MobileDiagnosticsRoute.tsx",
  "components/Mobile/MobileNavDrawer.tsx",
  "components/Mobile/MobileNewChat.tsx",
  "components/Mobile/MobileProjectList.tsx",
  "components/Mobile/MobileRunsScreen.tsx",
  "components/Mobile/MobileSearchScreen.tsx",
  "components/Mobile/MobileSettingsScreen.tsx",
  "components/Mobile/MobileShell.tsx",
  "components/Mobile/MobileSplitLayout.tsx",
  "components/Mobile/MobileWorkflowCatalog.tsx",
  "components/Mobile/MobileWorkflowDetailRoute.tsx",
  "components/Mobile/MobileWorkflowScreen.tsx",
  "components/OAuthCallback.tsx",
  "components/Onboarding/OnboardingChecklist.tsx",
  "components/Onboarding/journeys/forge.tsx",
  "components/Onboarding/journeys/workflows.tsx",
  "components/OnboardingFlow/DaemonConnectingGate.tsx",
  "components/OnboardingFlow/OnboardingEscapeHatch.tsx",
  "components/OnboardingFlow/OnboardingRoute.tsx",
  "components/OnboardingFlow/leaveOnboarding.ts",
  "components/OnboardingFlow/onboardingGate.ts",
  "components/OnboardingFlow/useOnboardingComplete.ts",
  "components/OnboardingFlow/useOnboardingPlan.ts",
  "components/Orgs/AcceptInvitePage.tsx",
  "components/Orgs/useAcceptInvite.ts",
  "components/ProxyAuth.tsx",
  "components/ResetPasswordScreen.tsx",
  "components/Settings/AboutSection.tsx",
  "components/Settings/AccountSettings.tsx",
  "components/Settings/IntegrationsSettings.tsx",
  "components/Settings/ProjectsSection.tsx",
  "components/Settings/SettingsPage.tsx",
  "components/Settings/SettingsProjectScope.tsx",
  "components/Settings/cloud/billing.tsx",
  "components/Settings/cloud/machines.tsx",
  "components/Settings/cloud/usage/usageBillingCheckout.ts",
  "components/Settings/providers/ProvidersSection.tsx",
  "components/Shortcuts/ShortcutHost.tsx",
  "components/UpgradeAccount.tsx",
  "components/Worktrees/DeleteWorktreeModal.tsx",
  "components/inbox/InboxItem.tsx",
  "components/inbox/InboxPage.tsx",
  "components/runs/ChildRunsStrip.tsx",
  "components/runs/RunDetailPage.tsx",
  "components/runs/RunHeader.tsx",
  "components/runs/RunRow.tsx",
  "components/runs/TriggerCard.tsx",
  "components/runs/useOpenRun.ts",
  "components/workflow/WorkflowBuilderPage.tsx",
  "components/workflow/WorkflowPage.tsx",
  "components/workflow/WorkflowParamInput.tsx",
  "components/workflow/WorkflowViewer.tsx",
  "components/workflow/connections/ConnectionPicker.tsx",
  "components/workflow/run/BuilderTestRunPanel.tsx",
  "components/workflow/run/MissingConnectionLink.tsx",
  "components/workflow/run/RunFormError.tsx",
  "components/workflow/run/RunNowDialog.tsx",
  "components/workflow/run/RunWorkflowDialog.tsx",
  "components/workflow/run/StepRunPanel.tsx",
  "components/workflows/WorkflowsShell.tsx",
  "components/workflows/detail/TriggersCard.tsx",
  "components/workflows/detail/WorkflowDetailPage.tsx",
  "components/workflows/list/ReadyMadeStrip.tsx",
  "components/workflows/list/WorkflowRow.tsx",
  "components/workflows/list/WorkflowsListPage.tsx",
  "forgeShellRoutes.tsx",
  "hooks/useGoToBilling.ts",
  "lib/claimLink.ts",
  "lib/connection-oauth.ts",
  "lib/inviteLink.ts",
  "lib/oauthCallbackCredentials.ts",
  "lib/routeParent.ts",
  "routes.tsx",
  "settingsConnectorRoutes.tsx",
  "settingsSectionRoutes.tsx",
  "store/projectStore.ts",
]);

const { sites, roots } = findPathSites(SRC);
const outside = sites.filter((site) => !BUILDERS.has(site.file) && !PENDING_CODEMOD.has(site.file));

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
    ]) {
      expect(kinds(nav), nav).toContain("navigation");
    }
    for (const route of [`const BILLING = "/settings/billing"`, `const items = [{ path: x, target: "/inbox" }]`, `return "/inbox"`]) {
      expect(kinds(route), route).toEqual(["route"]);
    }
    for (const fine of [
      `if (pathname === "/inbox") go()`,
      `pathname.startsWith("/settings/")`,
      `switch (p) { case "/inbox": break }`,
      `createRoute({ path: "/settings/$section" })`,
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
    const offenders = outside.filter((site) => site.kind === "route" && !(site.file in ALLOWED_ROUTE));
    expect(
      offenders,
      `${offenders.length} route path literal(s) outside lib/paths: build it with a builder, or (to match a pathname) compare it directly\n${offenders.map(describeSite).join("\n")}`,
    ).toEqual([]);
  });

  it("allows nothing it no longer needs", () => {
    const used = new Set(sites.map((site) => site.file));
    const stale = [...Object.keys(ALLOWED_ROUTE), ...Object.keys(ALLOWED_NAVIGATION), ...PENDING_CODEMOD].filter((file) => !used.has(file));
    expect(stale, "allowlist entries with nothing left to allow").toEqual([]);
  });
});
