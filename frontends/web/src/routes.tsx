import { useEffect } from 'react'
// The workflows journey's content and Reliant's end-of-tour landing (the demo bundle leaves them out).
import './components/Onboarding/journeys/catalog'
import { ActiveOrgBinding } from './components/Orgs/ActiveOrgBinding'
import { followActiveOrg } from './components/Orgs/followActiveOrg'
import { createRootRoute, createRoute, createRouter, Outlet, Navigate, useRouterState, useNavigate, type AnyRoute, type RouteComponent } from '@tanstack/react-router'
import { SurfaceProvider } from './lib/surfaceContext'
import { surfaceForPath } from './lib/surface'
import { mobilePathFor, shouldRedirectToMobileNow } from './lib/mobileRedirect'
import { isForgeUIEnabled } from './lib/forgeFeature'
import { getIsDev } from './lib/constants'
import { createWorkflowsAreaRoutes } from './workflowsAreaRoutes'
import { retainTourParam } from './components/Onboarding/tourParam'
import { createForgeDemoRedirectRoute, createForgeDemoRoute } from './components/Forge/Demo/demoRoute'
import { createForgeConsoleRoutes } from './forgeConsoleRoutes'
import { createSettingsConnectorRoutes } from './settingsConnectorRoutes'
import { createSettingsSectionRoutes } from './settingsSectionRoutes'
import { createOrgLayoutRoute } from './orgRoutes'
import { routeOrgScopeHooks, useRouteOrgScope } from './lib/routeOrg'
import { mobileHandoffAllowed } from './lib/mobileRedirect'
import { forwardIntoOrg, type LocationLike } from './lib/orgRouting'
import type { OrgRouteState } from './lib/routeOrg'
import {
  appsSearchSchema,
  authSearchSchema,
  chatViewSearchSchema,
  newChatSearchSchema,
  connectionOAuthCallbackSearchSchema,
  githubOAuthCallbackSearchSchema,
  indexSearchSchema,
  mobileNewChatSearchSchema,
  oauthCallbackSearchSchema,
  onboardingSearchSchema,
  proxyAuthSearchSchema,
  settingsSearchSchema,
  upgradeSearchSchema,
  workflowSearchSchema,
} from './routeSchemas'
import { reportCaughtError, RouteErrorFallback } from './components/ErrorBoundary'
import { lazyRoute } from './lib/lazyRoute'
import { watchRoutedChatOpens } from './lib/routedChatOpen'
import { listenForOpenRouteLinks } from './lib/openRouteLink'
import { AuthGuard } from './components/AuthGuard'
import { OnboardingGate } from './components/OnboardingFlow/OnboardingGate'
import { ModalLayer } from './components/Modals/ModalLayer'
import { AddProjectDialogHost } from './components/Projects/AddProjectDialog'
import { AutomationDialogHost } from './components/Automations/dialog/AutomationDialogHost'
import { AnonSignInNudge } from './components/AnonSignInNudge'
import { Toaster } from './lib/toast'
import { NewBuildPrompt } from './components/NewBuildPrompt'
import { ClientMinimumWatch } from './components/ClientMinimumWatch'
import { GlobalUpdateHandler } from './components/Settings/GlobalUpdateHandler'
import { ContextualTipsLayer, OnboardingWizard } from './components/Onboarding'
import { GitHubSyncStatus } from './components/Layout/GitHubSyncBanner'
import { QueuedDeployWatch } from './components/Forge/QueuedDeployWatch'
import { ShortcutHost } from './components/Shortcuts/ShortcutHost'
import { PageVisitTracker } from './components/PageVisitTracker'
import { JourneyTour } from './components/Onboarding/JourneyTour'
import { ForgeLandingJourney } from './components/ForgeShell/ForgeLandingJourney'
import { captureForgeEntry } from './components/ForgeShell/forgeEntry'
import { bootForgeTour } from './components/ForgeShell/forgeTourBoot'
import { isForgeShell } from './lib/shell'
import { homePath, mobileChatsPath } from '@/lib/paths'

// ─── One router, two brands ─────────────────────────────────────────────────
//
// Both hosts mount THIS tree (components/App.tsx): app.reliantlabs.io and
// forge.reliantlabs.io serve the same routes, guards and pages. The host is
// only a brand (lib/shell.ts, getShell / isForgeShell), and the forge brand
// differs in exactly these places, all in this file unless named:
//
//   - the root carries `data-shell="forge"` (RootShell);
//   - the first page_visited event carries the landing handoff (forgeEntry,
//     taken below, before the router exists, because the sign-in redirect
//     replaces the URL it arrived on);
//   - the tour runs as the forge shell's, with progress in localStorage
//     (bootForgeTour, below), and the root mounts JourneyTour and
//     ForgeLandingJourney in place of Reliant's OnboardingWizard;
//   - AuthGuard holds sign-in for a visitor the landing journey is taking to
//     the public demo (components/AuthGuard.tsx);
//   - the home sends a person who has not onboarded to their apps rather
//     than to the wizard (OnboardingFlow/onboardingGate.ts,
//     forgeBrandHomeTarget);
//   - telemetry reports as `forge-web` (lib/telemetry.ts), and the brand mark
//     is ForgeBrand (the nav, AuthLayout).
//
// Brand is fixed for the page load, so it is read once, here.
const forgeBrand = isForgeShell()
const forgeEntry = forgeBrand ? captureForgeEntry() : undefined
if (forgeBrand) bootForgeTour()

// ─── Code-split route components ────────────────────────────────────────────
//
// Every screen below is loaded on demand. Statically importing them here is
// what made the entry chunk the whole application: this module is imported by
// `main.tsx`, so any component named at the top level lands in `index.js`
// whether or not the visited route renders it. That is why /oauth/consent —
// a lazy route with a 9.7 kB chunk of its own — still pulled ~5 MB of
// JavaScript: the cost was in the route tree, not the route.
//
// The unauthenticated entry screens (AuthScreen, the OAuth callbacks,
// ProxyAuth, ResetPassword, EmailVerification, UpgradeAccount) are split for a
// second reason: they are frequently someone's first page load, so they should
// not pay for the authenticated app at all.
//
// `AuthGuard`, the overlay components and `lib/toast` stay static — they mount
// on every route, so deferring them would only add a waterfall.
//
// Each goes through `lazyRoute` (lib/lazyRoute.tsx), never TanStack's
// lazyRouteComponent directly: after a deploy a tab can ask for a chunk that
// no longer exists, and lazyRoute reloads from an effect where TanStack would
// reload during render.
const AuthScreen = lazyRoute(
  () => import('./components/AuthScreen'), 'AuthScreen')
const OAuthCallback = lazyRoute(
  () => import('./components/OAuthCallback'), 'OAuthCallback')
const GitHubOAuthCallback = lazyRoute(
  () => import('./components/GitHubOAuthCallback'), 'GitHubOAuthCallback')
const ConnectionOAuthCallback = lazyRoute(
  () => import('./components/ConnectionOAuthCallback'), 'ConnectionOAuthCallback')
const ProxyAuth = lazyRoute(
  () => import('./components/ProxyAuth'), 'ProxyAuth')
const ResetPasswordScreen = lazyRoute(
  () => import('./components/ResetPasswordScreen'), 'ResetPasswordScreen')
const EmailVerification = lazyRoute(
  () => import('./components/EmailVerification'), 'EmailVerification')
const UpgradeAccount = lazyRoute(
  () => import('./components/UpgradeAccount'), 'UpgradeAccount')
const ClaimPage = lazyRoute(
  () => import('./components/Claim/ClaimPage'), 'ClaimPage')
const AcceptInvitePage = lazyRoute(
  () => import('./components/Orgs/AcceptInvitePage'), 'AcceptInvitePage')
const DesignSandboxPage = lazyRoute(
  () => import('./components/DesignSandbox/DesignSandboxPage'), 'DesignSandboxPage')
// Proving harness for the forge → reliant token bridge (see index.css).
const ForgeTokenSandbox = lazyRoute(
  () => import('./components/DesignSandbox/forgeTokens/ForgeTokenSandbox'), 'default')
// The download / connect / buy-a-machine surfaces against a fake network.
const SetupSurfacesPreview = lazyRoute(
  () => import('./components/OnboardingFlow/__preview__/SetupSurfacesPreview'), 'default')
const SecretsPreview = lazyRoute(
  () => import('./components/Forge/Secrets/__preview__/SecretsPreview'), 'default')
// The whole Forge pane against a fake network (control-plane's real state as
// fixtures) — see ForgePanePreview for why the fake sits at fetch.
const ForgePanePreview = lazyRoute(
  () => import('./components/Forge/__preview__/ForgePanePreview'), 'ForgePanePreview')
// The Observability tab against in-memory fixtures — every view and state,
// no backend. Also what the Playwright smoke drives (e2e/observability.spec.ts).
const ObservabilityPreview = lazyRoute(
  () => import('./components/Forge/Observability/__preview__/ObservabilityPreview'), 'default')
const ObservabilityBillingPreview = lazyRoute(
  () => import('./components/Forge/Observability/__preview__/ObservabilityBillingPreview'), 'default')
const TraceLatencyPreview = lazyRoute(
  () => import('./components/Forge/Observability/traceScatter/__preview__/TraceLatencyPreview'), 'default')
// The console v3 forge-ui primitives, composed on fabricated data.
const ConsolePrimitivesPreview = lazyRoute(
  () => import('./components/Forge/__preview__/ConsolePrimitivesPreview'), 'default')
const DashboardsPreview = lazyRoute(
  () => import('./components/Forge/Observability/Dashboards/__preview__/DashboardsPreview'), 'default')
const MetricsExplorerPreview = lazyRoute(
  () => import('./components/Forge/Observability/Metrics/__preview__/MetricsExplorerPreview'), 'default')
const ReleasesPreview = lazyRoute(
  () => import('./components/Forge/Releases/__preview__/ReleasesPreview'), 'default')
const DataPreview = lazyRoute(
  () => import('./components/Forge/Data/__preview__/DataPreview'), 'default')
const WorkloadsPreview = lazyRoute(
  () => import('./components/Forge/Workloads/__preview__/WorkloadsPreview'), 'default')
const StoragePreview = lazyRoute(
  () => import('./components/Forge/Storage/__preview__/StoragePreview'), 'default')
const SettingsPage = lazyRoute(
  () => import('./components/Settings/SettingsPage'), 'SettingsPage')
const ConnectorConsentPage = lazyRoute(
  () => import('./components/Settings/ConnectorConsentPage'), 'ConnectorConsentPage')
const WorkflowPage = lazyRoute(
  () => import('./components/workflow/WorkflowPage'), 'WorkflowPage')
const AutomationRedirectPage = lazyRoute(
  () => import('./components/workflows/AutomationRedirectPage'), 'AutomationRedirectPage')
const RunsPage = lazyRoute(
  () => import('./components/runs/RunsPage'), 'RunsPage')
const RunDetailPage = lazyRoute(
  () => import('./components/runs/RunDetailPage'), 'RunDetailPage')
const WorkflowsLayout = lazyRoute(
  () => import('./components/workflows/WorkflowsShell'), 'WorkflowsLayout')
const WorkflowsListPage = lazyRoute(
  () => import('./components/workflows/list/WorkflowsListPage'), 'WorkflowsListPage')
const WorkflowDetailPage = lazyRoute(
  () => import('./components/workflows/detail/WorkflowDetailPage'), 'WorkflowDetailPage')
const InboxPage = lazyRoute(
  () => import('./components/inbox/InboxPage'), 'InboxPage')
const AppsPage = lazyRoute(
  () => import('./components/Apps/AppsPage'), 'AppsPage')
const SitesPage = lazyRoute(
  () => import('./components/ForgeShell/ForgeSitesPage'), 'SitesPage')
const UsagePage = lazyRoute(
  () => import('./components/ForgeShell/ForgeUsagePage'), 'UsagePage')
const OnboardingRoute = lazyRoute(
  () => import('./components/OnboardingFlow/OnboardingRoute'), 'OnboardingRoute')
const MobileShell = lazyRoute(
  () => import('./components/Mobile/MobileShell'), 'MobileShell')
const MobileSplitLayout = lazyRoute(
  () => import('./components/Mobile/MobileSplitLayout'), 'MobileSplitLayout')
const MobileChatsEmptyDetail = lazyRoute(
  () => import('./components/Mobile/MobileSplitLayout'), 'MobileChatsEmptyDetail')
const MobileChatScreen = lazyRoute(
  () => import('./components/Mobile/MobileChatScreen'), 'MobileChatScreen')
const MobileNewChat = lazyRoute(
  () => import('./components/Mobile/MobileNewChat'), 'MobileNewChat')
const MobileDaemonList = lazyRoute(
  () => import('./components/Mobile/MobileDaemonList'), 'MobileDaemonList')
const MobileDaemonScreen = lazyRoute(
  () => import('./components/Mobile/MobileDaemonScreen'), 'MobileDaemonScreen')
const MobileAccountScreen = lazyRoute(
  () => import('./components/Mobile/MobileAccountScreen'), 'MobileAccountScreen')
const MobileProjectList = lazyRoute(
  () => import('./components/Mobile/MobileProjectList'), 'MobileProjectList')
const MobileSearchScreen = lazyRoute(
  () => import('./components/Mobile/MobileSearchScreen'), 'MobileSearchScreen')
const MobileWorkflowCatalog = lazyRoute(
  () => import('./components/Mobile/MobileWorkflowCatalog'), 'MobileWorkflowCatalog')
const MobileInboxScreen = lazyRoute(
  () => import('./components/Mobile/MobileInboxScreen'), 'MobileInboxScreen')
const MobileRunsScreen = lazyRoute(
  () => import('./components/Mobile/MobileRunsScreen'), 'MobileRunsScreen')
const MobileAutomationRedirect = lazyRoute(
  () => import('./components/Mobile/MobileAutomationRedirect'), 'MobileAutomationRedirect')
const MobileDiagnosticsRoute = lazyRoute(
  () => import('./components/Mobile/MobileDiagnosticsRoute'), 'MobileDiagnosticsRoute')
const MobileSettingsScreen = lazyRoute(
  () => import('./components/Mobile/MobileSettingsScreen'), 'MobileSettingsScreen')
const MobileGitHubScreen = lazyRoute(
  () => import('./components/Mobile/MobileGitHubScreen'), 'MobileGitHubScreen')
const MobileWorkflowDetailRoute = lazyRoute(
  () => import('./components/Mobile/MobileWorkflowDetailRoute'), 'MobileWorkflowDetailRoute')
const MobileChatWorkflowRoute = lazyRoute(
  () => import('./components/Mobile/MobileWorkflowDetailRoute'), 'MobileChatWorkflowRoute')
const ForgeLayout = lazyRoute(
  () => import('./components/Forge/ForgeLayout'), 'ForgeLayout')
const ConsoleLayout = lazyRoute(
  () => import('./components/Forge/Console/ConsoleLayout'), 'ConsoleLayout')
const ForgeConsoleOverviewPage = lazyRoute(
  () => import('./components/Forge/Workloads/WorkloadPages'), 'OverviewPage')
const ForgeConsoleWorkloadPage = lazyRoute(
  () => import('./components/Forge/Workloads/WorkloadPages'), 'WorkloadPage')
const ForgeConsoleReleasesPage = lazyRoute(
  () => import('./components/Forge/Releases/ReleasesPages'), 'ReleasesPage')
const ForgeConsoleReleasePage = lazyRoute(
  () => import('./components/Forge/Releases/ReleasesPages'), 'ReleasePage')
const ForgeConsoleObservabilityViewPage = lazyRoute(
  () => import('./components/Forge/Console/ConsolePages'), 'ObservabilityViewPage')
const ForgeConsoleDashboardsPage = lazyRoute(
  () => import('./components/Forge/Observability/Dashboards/DashboardsPages'), 'DashboardsPage')
const ForgeConsoleDashboardPage = lazyRoute(
  () => import('./components/Forge/Observability/Dashboards/DashboardsPages'), 'DashboardPage')
const ForgeConsoleStoragePage = lazyRoute(
  () => import('./components/Forge/Storage/StoragePage'), 'StoragePage')
const ForgeConsoleDataPage = lazyRoute(
  () => import('./components/Forge/Data/DataPage'), 'DataPage')
const ForgeConsoleUsersPage = lazyRoute(
  () => import('./components/Forge/Users/UsersPage'), 'UsersPage')
const ForgeConsoleExperimentsPage = lazyRoute(
  () => import('./components/Forge/Experiments/ExperimentsPage'), 'ExperimentsPage')
const ForgeConsoleExperimentPage = lazyRoute(
  () => import('./components/Forge/Experiments/ExperimentsPage'), 'ExperimentPage')
const ForgeConsoleVulnerabilitiesPage = lazyRoute(
  () => import('./components/Forge/Security/VulnerabilitiesPage'), 'VulnerabilitiesPage')
const ForgeConsoleVulnerabilityPage = lazyRoute(
  () => import('./components/Forge/Security/VulnerabilityPage'), 'VulnerabilityPage')
const ForgeConsoleSbomsPage = lazyRoute(
  () => import('./components/Forge/Security/SbomsPage'), 'SbomsPage')
const ForgeConsoleSecretsPage = lazyRoute(
  () => import('./components/Forge/Console/SecretsPage'), 'SecretsPage')
const ForgeConsoleActivityPage = lazyRoute(
  () => import('./components/Forge/Console/ActivityPage'), 'ActivityPage')
const ForgeEnvironmentsPage = lazyRoute(
  () => import('./components/Forge/Overview/EnvironmentsPage'), 'EnvironmentsPage')
const ForgeEnvPage = lazyRoute(
  () => import('./components/Forge/EnvPage/ForgeEnvPage'), 'ForgeEnvPage')
const ForgeDomainsPage = lazyRoute(
  () => import('./components/Forge/Domains/ForgeDomainsPage'), 'ForgeDomainsPage')
const ForgeLandingRoute = lazyRoute(
  () => import('./components/Forge/Console/ForgeEntryRoutes'), 'ForgeLandingRoute')
const ForgeLegacyEnvRoute = lazyRoute(
  () => import('./components/Forge/Console/ForgeEntryRoutes'), 'LegacyEnvRoute')
const ForgeOrgObservabilityRoute = lazyRoute(
  () => import('./components/Forge/Console/ForgeEntryRoutes'), 'OrgObservabilityRoute')
const ForgeOrgLayout = lazyRoute(
  () => import('./components/Forge/Org'), 'OrgConsoleLayout')
const ForgeLegacyDomainsRoute = lazyRoute(
  () => import('./components/Forge/Console/ForgeEntryRoutes'), 'LegacyDomainsRoute')
const ForgeLegacyCommandRoute = lazyRoute(
  () => import('./components/Forge/Console/ForgeEntryRoutes'), 'LegacyCommandRoute')
const App = lazyRoute(() => import('./App'), 'default')

// Search schemas live in ./routeSchemas (kept dependency-free so tests can
// import them without dragging in the route-tree's component graph).

// Truly app-global overlays live at the root so they render on every route —
// including /settings, /workflows/*, /workflow/*, and the unauthenticated /auth routes. They
// were previously mounted inside ModernApp (which only mounts on `/` and
// `/project/$projectId`), so users on any other route silently got no toasts,
// no modals registered via ModalLayer (incl. ApiKeySetupModal), no contextual
// tips, and no GitHub sync feedback. Mounting here fixes that.
//
// Mounting on unauthenticated routes is intentional and safe: <Toaster /> is
// just a portal that renders queued toasts, <ModalLayer /> renders nothing
// without a registered modal, <ContextualTipsLayer /> renders nothing without
// a relevant tip, and <GitHubSyncStatus /> only emits toasts on sync state
// transitions which don't fire pre-auth.
//
// OnboardingWizard also lives here. It is fully URL-decoupled — it does not
// push routes anywhere. Steps that need to spotlight an element on a specific
// page check `pathname` themselves and either render the spotlight (right
// page) or a small modal asking the user to navigate (wrong page). The wizard
// gates its own visibility internally via `isWizardActive`, so mounting it
// globally is safe on every route.
// The surface is resolved HERE, at the root, rather than only inside
// MobileLayout. These overlays are siblings of <Outlet/>, so a provider
// rendered inside the outlet sits *below* them in the tree and they read the
// context default ("desktop") — which silently disabled the mobile guards in
// OnboardingWizard / ContextualTipsLayer / AnonSignInNudge and left the
// onboarding-checklist FAB floating over the phone UI, intercepting taps.
// Deriving from the pathname keeps the overlays and the routed screen in
// agreement. MobileLayout still provides the surface for its subtree, which is
// harmless (same value) and keeps it self-contained for tests and embeds.
function RootShell() {
  const { pathname, search } = useRouterState({ select: (s) => s.location });
  // The path of the page on screen (the committed matches) and its org scope,
  // which move together (lib/routeOrg.ts): the phone hand-off below reads them.
  const committedPathname = useRouterState({ select: (s) => s.matches[s.matches.length - 1]?.pathname ?? null });
  const routeOrgScope = useRouteOrgScope((s) => s.scope);
  const navigate = useNavigate();

  // Phones get the mobile surface. Without this the `/m/*` routes are
  // unreachable except by typing the URL, and every phone loads the full
  // desktop ADE — resizable sidebars, file tree, terminal tabs — at 390px.
  //
  // Runs on pathname change rather than once on mount so an in-app navigation
  // out of `/m/*` (a stray link into a desktop-only route) lands back here
  // instead of stranding the user in the desktop shell.
  // The router's location is passed in deliberately: it commits ~17ms ahead of
  // `window.location`, so reading the live URL here evaluated the /auth → /
  // sign-in hop against the stale `/auth` (a preserved path) and left phones
  // stranded on the desktop shell.
  useEffect(() => {
    const searchString =
      typeof search === "string"
        ? search
        : new URLSearchParams(
            search as Record<string, string>,
          ).toString();
    // An org page hands off only once a member org's page has committed (its
    // org is then the tab's, which the mobile tree reads until U15): never
    // from a "no access" page, and never before the org is known.
    if (!mobileHandoffAllowed({ pathname, committedPathname, routeOrgScope })) return;
    if (shouldRedirectToMobileNow({ pathname, search: searchString })) {
      // To the mobile twin of what was asked for (the Inbox, a run's chat,
      // settings), not always the chat list — see mobilePathFor.
      navigate({ href: mobilePathFor(pathname, searchString), replace: true });
    }
  }, [pathname, search, navigate, committedPathname, routeOrgScope]);

  // Keyboard shortcuts are app-global too, for the same reason: the host here
  // owns the one dispatcher, the desktop menu bridge and the command palette
  // for every authenticated desktop route. It decides where it is live itself
  // (signed in, desktop surface, an app route — never /auth, /onboarding or
  // /m/*); ModernApp and the workflow builder contribute their handlers to it.
  const shell = (
    <>
      <ActiveOrgBinding onSwitch={followActiveOrg} />
      <PageVisitTracker firstVisitProps={forgeEntry} />
      <ShortcutHost>
        <Outlet />
      </ShortcutHost>
      <ModalLayer />
      {/* Add project opens over any route (the sidebar, Settings, the
          new-chat project chip, the shortcut, the tray). */}
      <AddProjectDialogHost />
      {/* openAutomationDialog: one automation dialog for every page, chat card and builder panel. */}
      <AutomationDialogHost />
      <AnonSignInNudge />
      <Toaster />
      <NewBuildPrompt />
      {/* The forced upgrade when this build is below the reliant API's
          published minimum (lib/clientMinimum.ts). */}
      <ClientMinimumWatch />
      {/* The desktop app's update dialog: the sidebar button, Settings and
          the app menu's "Check for Updates…" open it from any route. */}
      <GlobalUpdateHandler />
      <ContextualTipsLayer />
      <GitHubSyncStatus />
      {forgeBrand ? (
        // The journeys (Onboarding/JOURNEYS.md) at the root, signed in or not,
        // so a step follows the visitor between the public demo and the
        // signed-in pages; and the journey the landing page carried. The
        // OnboardingWizard's checklist is Reliant's chat setup, and it shows
        // no tour to a signed-out visitor.
        <>
          <JourneyTour />
          <ForgeLandingJourney />
        </>
      ) : (
        <OnboardingWizard />
      )}
      {/* A deploy this session saw queued on billing goes out while the user
          is usually elsewhere (billing, a chat): the watch lives here, and
          loads its forge code only while it has something to watch. */}
      <QueuedDeployWatch pathname={pathname} />
    </>
  );

  return (
    <SurfaceProvider surface={surfaceForPath(pathname)}>
      {forgeBrand ? (
        <div data-shell="forge" data-pathname={pathname} className="contents">
          {shell}
        </div>
      ) : (
        shell
      )}
    </SurfaceProvider>
  );
}

const rootRoute = createRootRoute({
  component: RootShell,
  // The org scope moves when a navigation commits (lib/routeOrg.ts).
  ...routeOrgScopeHooks,
  // With the flag web.org_routes on, an org-scoped page's org-less path (`/`,
  // `/inbox`, an old bookmark) redirects into the tab's org (lib/orgRouting.ts,
  // the table in lib/paths/legacy.ts). Off: a no-op.
  beforeLoad: ({ location }) => forwardIntoOrg(location),
  // Keep `?tour=` across navigations that don't name it (retainTourParam).
  // The root has no search schema, so its middleware type is `{}`-shaped.
  search: { middlewares: [retainTourParam as never] },
  notFoundComponent: () => <Navigate to={homePath()} search={{}} />,
  errorComponent: RouteErrorFallback,
})

const authRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth',
  validateSearch: authSearchSchema,
  component: () => (
    <AuthGuard requireAuth={false}>
      <AuthScreen />
    </AuthGuard>
  ),
})

const oauthCallbackRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/callback',
  validateSearch: oauthCallbackSearchSchema,
  component: OAuthCallback,
})

// App-owned GitHub OAuth connect callback. GITHUB_REDIRECT_URI points GitHub at
// this app route (localhost:3000 dev / app.reliantlabs.io prod) so the flow works
// on Firebase, whose SPA-rewrites can't proxy the callback to the GKE backend.
// The component exchanges the code via the ExchangeGithubOAuthCode RPC. Public:
// state carries identity, and the user may not have an app session in this tab.
const githubOAuthCallbackRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/github/callback',
  validateSearch: githubOAuthCallbackSearchSchema,
  component: GitHubOAuthCallback,
})

// Where the API relays an integration connection's OAuth flow (Slack, Gmail;
// Go: connections.AppCallbackPath). Firebase serves the SPA for it like any
// route. Signed in: finishing the flow as its own user is the CSRF check.
const connectionOAuthCallbackRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/connections/oauth/callback',
  validateSearch: connectionOAuthCallbackSearchSchema,
  component: () => (
    <AuthGuard requireAuth={true}>
      <ConnectionOAuthCallback />
    </AuthGuard>
  ),
})

// Supabase OAuth 2.1 consent screen. Supabase's OAuth server validates the
// authorization request and then redirects the user HERE with an
// authorization_id, waiting for this page to approve or deny — it hosts no
// consent UI of its own. The path must match the Authorization Path configured
// in the Supabase dashboard (Authentication → OAuth Server), combined with the
// project's Site URL.
//
// Root-level and NOT under the authenticated layout: the component checks the
// session itself and bounces to /auth carrying the authorization_id, because a
// layout-level redirect would drop that id and return the user to a page that
// no longer knows what it was approving.
const oauthConsentRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/oauth/consent',
  // This page is reached mid-OAuth from a third-party client, often on a
  // phone, by someone who has not otherwise opened the app — so its
  // time-to-interactive is a step in someone else's flow rather than
  // navigation within ours. It is also why the Monaco preload is gated off
  // this path (see lib/monacoPreload).
  component: lazyRoute(
    () => import('./components/Auth/OAuthConsent'),
    'OAuthConsent',
  ),
})

// CLI login consent (`forge cloud login`, `reliant auth login`). The control plane's
// /oauth/authorize validates the CLI's PKCE request and sends the browser here,
// because this app holds the end-user session. Root-level and unguarded for the
// same reason as /oauth/consent: the page signs the user in itself and must
// keep the whole query string across that detour.
const cliLoginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/oauth/cli',
  component: lazyRoute(() => import('./components/Auth/CliLogin'), 'CliLogin'),
})

// Self-hosted machine pairing (`reliant daemon start` prints this link with
// its code). Root-level and unguarded like /oauth/cli: the page signs the user
// in itself and keeps the code across that detour.
const devicePairingRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/oauth/device',
  component: lazyRoute(() => import('./components/Auth/DevicePairing'), 'DevicePairing'),
})

const proxyAuthRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/proxy',
  validateSearch: proxyAuthSearchSchema,
  component: ProxyAuth,
})

// Pathless layout route that gates the settings + workflow group on auth +
// email verification, and its chat routes (the phone's, `/m/chats…`) on
// finished onboarding (OnboardingGate; which routes is
// OnboardingFlow/onboardingGate.ts — every other page here works for a person
// with no machine). A signed-out deep link keeps where it was going across
// sign-in (returnHere), and the gate across the wizard.
// Avoids re-declaring <AuthGuard requireAuth requireEmailVerification>
// on every one of those routes. The `_app` layout below has its own AuthGuard
// (kept separate so its full route id remains '/_app' — production code uses
// useSearch({ from: '/_app' }) and that id collapses if we nest it here).
// Reset-password and verify-email have different gating, so they stay as
// direct children of rootRoute.
function AuthenticatedLayout() {
  return (
    <AuthGuard requireAuth={true} requireEmailVerification={true} returnHere>
      <OnboardingGate>
        <Outlet />
      </OnboardingGate>
    </AuthGuard>
  )
}

const authenticatedLayoutRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: '_authenticated',
  component: AuthenticatedLayout,
})

const resetPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/reset-password',
  component: () => (
    <AuthGuard requireAuth={true}>
      <ResetPasswordScreen />
    </AuthGuard>
  ),
})

const verifyEmailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/verify-email',
  component: () => (
    <AuthGuard requireAuth={true} requireEmailVerification={false}>
      <EmailVerification />
    </AuthGuard>
  ),
})

// Account-upgrade flow. An anonymous user who needs a real identity *with an
// email* (e.g. blocked on the admin billing page) lands here. requireAuth is
// true because the anon user already has a Supabase session — a plain /auth
// bounce would just redirect them away. requireEmailVerification is false by
// design: the whole point is that they have no email yet. UpgradeAccount owns
// its own returnTo redirect once an email is attached.
const upgradeRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/upgrade',
  validateSearch: upgradeSearchSchema,
  component: () => (
    <AuthGuard requireAuth={true} requireEmailVerification={false}>
      <UpgradeAccount />
    </AuthGuard>
  ),
})

// Claim a no-signup deploy (`/claim#rcl_<token>`, handed over by an agent).
// Root-level and unguarded: the page shows what is being claimed to someone
// with no session, and a layout redirect to /auth would lose the claim. The
// token never reaches the router: lib/claimLinkBoot moves it out of the
// fragment before the router is built. See components/Claim/ClaimPage.tsx.
const claimRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/claim',
  component: ClaimPage,
})

// Accept an org invitation (`/accept-invite#rinv_<token>`, from an invitation
// email or a link an admin copied). Root-level and unguarded like /claim: a
// signed-out invitee is sent through sign-in from the page, which returns
// here. lib/inviteLinkBoot moves the token out of the fragment first.
const acceptInviteRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/accept-invite',
  component: AcceptInvitePage,
})

// Pathless layout route that owns the App shell + its shared search schema.
// Children `/` and `/project/$projectId` both render <App />; placing the
// validateSearch + AuthGuard on the parent means components inside App can
// always read search via useSearch({ from: '/_app' }) regardless of which
// child the user is on. Without this, useSearch({ from: '/' }) would throw
// "Could not find an active match from /" the moment we navigate to a project.
//
// Kept as a direct child of rootRoute (not nested under _authenticated) so its
// full id remains '/_app' — call sites use useSearch({ from: '/_app' }).
function AppLayout() {
  // returnHere: a signed-out chat link (`/c/<id>`) comes back to its chat after sign-in.
  return (
    <AuthGuard requireAuth={true} requireEmailVerification={true} returnHere>
      <Outlet />
    </AuthGuard>
  )
}

const appLayoutRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: '_app',
  validateSearch: indexSearchSchema,
  component: AppLayout,
})

const projectRootRedirectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/project',
  component: () => <Navigate to={homePath()} search={{}} />,
})

const designSandboxRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/design-sandbox',
  component: DesignSandboxPage,
})

// Both forge preview routes below are UNAUTHENTICATED but DEV-BUILD ONLY.
//
// Unauthenticated because the bugs these screens fix were VISUAL: they have to
// be lookable-at in every color scheme without standing up a session, which is
// exactly what made them useful during the rebuild.
//
// Dev-only because the workloads preview embeds a CAPTURED PRODUCTION REPORT —
// real workload names, the real GKE context and the real namespace
// (gke_reliant-labs-475814_us-central1_prod / control-plane-prod). No secrets
// and no credentials, but it is our infrastructure topology, and an
// unauthenticated route on rootRoute is reachable in a packaged build by
// anyone who types the path. A harness that only ever runs on a developer's
// machine costs nothing; shipping our cluster coordinates to every install is
// not a trade worth making for a debugging convenience.
//
// getIsDev() is read at render, never snapshot at module scope, matching
// ForgeGate. In a packaged build these fall through to rootRoute's
// notFoundComponent and behave exactly as if they were never written.
function DevOnlyRoute({ children }: { children: React.ReactNode }) {
  if (!getIsDev()) return <Navigate to={homePath()} search={{}} />
  return <>{children}</>
}

const forgeTokenSandboxRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forge-token-sandbox',
  component: () => <DevOnlyRoute><ForgeTokenSandbox /></DevOnlyRoute>,
})

// The secrets surface, against fabricated store states. Same reasoning as the
// workloads preview: the complaint it answers was visual, so it has to be
// LOOKED at in every scheme, and the states that are easiest to get wrong
// (destroyed, soft-deleted, declared-but-never-set) are the hardest to
// reproduce on demand against a live OpenBao.
const setupSurfacesPreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/setup-surfaces-preview',
  component: () => <DevOnlyRoute><SetupSurfacesPreview /></DevOnlyRoute>,
})

const forgeSecretsPreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forge-secrets-preview',
  component: () => <DevOnlyRoute><SecretsPreview /></DevOnlyRoute>,
})

const forgePanePreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forge-pane-preview',
  component: () => <DevOnlyRoute><ForgePanePreview /></DevOnlyRoute>,
})

const forgeObservabilityPreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forge-observability-preview',
  component: () => <DevOnlyRoute>{new URLSearchParams(window.location.search).has('billingCase') ? <ObservabilityBillingPreview /> : <ObservabilityPreview />}</DevOnlyRoute>,
})

// The traces page's latency chart (scatter + histogram) in every state, on
// seeded synthetic traces — a busy hour, the newest page of many, a brushed
// region, loading, error, empty — in every scheme, no backend.
const forgeTraceLatencyPreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forge-trace-latency-preview',
  component: () => <DevOnlyRoute><TraceLatencyPreview /></DevOnlyRoute>,
})

const consolePrimitivesPreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forge-ui-preview',
  component: () => <DevOnlyRoute><ConsolePrimitivesPreview /></DevOnlyRoute>,
})

// The Releases list and a release, inside the real console chrome, against the
// fixture ledger — every state (data, empty, error, loading, a ledger that
// failed) in every scheme, no backend.
const forgeReleasesPreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forge-releases-preview',
  component: () => <DevOnlyRoute><ReleasesPreview /></DevOnlyRoute>,
})

// The Observability home, Dashboards and a dashboard, inside the real console
// chrome, against the fixture app and an in-memory DashboardService — every
// state in every scheme, no backend.
// The Overview (with the workloads) and a workload, inside the real console
// chrome, against the workload fixtures — every state in every scheme, no
// backend.
const forgeWorkloadsPreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forge-workloads-preview',
  component: () => <DevOnlyRoute><WorkloadsPreview /></DevOnlyRoute>,
})

// The storage browser (buckets, a level, an object's preview and download),
// inside the real console chrome, against the fixture buckets — every state
// in every scheme, no backend.
const forgeStoragePreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forge-storage-preview',
  component: () => <DevOnlyRoute><StoragePreview /></DevOnlyRoute>,
})

// The data browser (databases, tables, a grid, a row and its Related), inside
// the real console chrome, against the fixture shop — every state in every
// scheme, no backend.
const forgeDataPreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forge-data-preview',
  component: () => <DevOnlyRoute><DataPreview /></DevOnlyRoute>,
})

const forgeDashboardsPreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forge-dashboards-preview',
  component: () => <DevOnlyRoute><DashboardsPreview /></DevOnlyRoute>,
})

// The metrics explorer, inside the real console chrome, against the explorer's
// fixture app — every state in every scheme, no backend.
const forgeMetricsPreviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forge-metrics-preview',
  component: () => <DevOnlyRoute><MetricsExplorerPreview /></DevOnlyRoute>,
})

// (`/checkout/embed` removed with the embedded-checkout path. It existed to
// host Stripe's hosted checkout page in a bare Electron BrowserWindow, because
// payment-method domains are registered by HOSTNAME and app://bundle cannot be
// one. Both checkouts now mount Stripe Elements inline on the ordinary app
// routes, so there is nothing left to give its own window.)

// Settings routes — replace the old viewerStore.isSettingsMode + settingsSection
// flags. `/settings` is the bare entry (defaults to the page's first section).
// `/settings/$section` deep-links to a specific section.
const settingsRoute = createRoute({
  getParentRoute: () => authenticatedLayoutRoute,
  path: '/settings',
  validateSearch: settingsSearchSchema,
  component: SettingsPage,
})

// /settings/connectors/authorize (the OAuth consent page third parties
// redirect to — its path is a contract) and the retired /settings/connectors
// section, which redirects to Machines. Defined in settingsConnectorRoutes.tsx
// so the route tests mount the same definitions.
const settingsConnectorRoutes = createSettingsConnectorRoutes(() => authenticatedLayoutRoute, {
  consent: ConnectorConsentPage,
})

// /settings/$section and /settings/machines/$machineId. Defined in
// settingsSectionRoutes.tsx so the route tests mount the same definitions; an
// unknown slug redirects to /settings with a notice rather than throwing.
const settingsSectionRoutes = createSettingsSectionRoutes(() => authenticatedLayoutRoute, {
  page: SettingsPage,
})

// ── forge UI (experimental, gated) ───────────────────────────────────────────
//
// Every forge route goes through ForgeGate. Hiding the sidebar entry is NOT enough
// on its own: a route left registered stays reachable by pasting its URL, and
// two of these screens front write paths (promote, and a deploy that applies to
// a live cluster). So the GATE IS ON THE ROUTE, and the nav entry is a second,
// independent check — the feature cannot be half-off.
//
// A disabled route redirects to / rather than rendering an explanation, which is
// how the root already treats a route that does not exist
// (rootRoute.notFoundComponent). To a user who has not opted in, these URLs
// behave exactly as they did before the feature was written.
function ForgeGate({ children }: { children: React.ReactNode }) {
  // Read at render, never at module scope: the Developer settings toggle
  // changes this within a session and a snapshot would need a reload.
  if (!isForgeUIEnabled()) return <Navigate to={homePath()} search={{}} />
  return <>{children}</>
}

// The forge console: /forge/p/$project/…
// for every page, addressed by the FORGE project name, plus /forge and the
// retired URLs, which resolve a project and redirect so bookmarks and
// notification deep links keep working. Defined in forgeConsoleRoutes.tsx so
// the route tests mount the same definitions.
//
// Every route is gated on its own (ForgeGate above), the redirects included —
// a redirect that skipped the gate would be a way past it. The pathless
// layout carries the Escape binding and stays ungated: it renders nothing of
// its own.
function gated(Component: RouteComponent): RouteComponent {
  return function Gated() {
    return <ForgeGate><Component /></ForgeGate>
  }
}



const forgeConsolePageComponents = {
  layout: ForgeLayout,
  console: gated(ConsoleLayout),
  overview: gated(ForgeConsoleOverviewPage),
  workload: gated(ForgeConsoleWorkloadPage),
  releases: gated(ForgeConsoleReleasesPage),
  release: gated(ForgeConsoleReleasePage),
  observabilityView: gated(ForgeConsoleObservabilityViewPage),
  dashboards: gated(ForgeConsoleDashboardsPage),
  dashboard: gated(ForgeConsoleDashboardPage),
  storage: gated(ForgeConsoleStoragePage),
  data: gated(ForgeConsoleDataPage),
  users: gated(ForgeConsoleUsersPage),
  vulnerabilities: gated(ForgeConsoleVulnerabilitiesPage),
  vulnerability: gated(ForgeConsoleVulnerabilityPage),
  sboms: gated(ForgeConsoleSbomsPage),
  environments: gated(ForgeEnvironmentsPage),
  environment: gated(ForgeEnvPage),
  secrets: gated(ForgeConsoleSecretsPage),
  domains: gated(ForgeDomainsPage),
  activity: gated(ForgeConsoleActivityPage),
  experiments: gated(ForgeConsoleExperimentsPage),
  experiment: gated(ForgeConsoleExperimentPage),
  orgLayout: gated(ForgeOrgLayout),
  orgObservability: gated(ForgeOrgObservabilityRoute),
  landing: gated(ForgeLandingRoute),
  legacyEnv: gated(ForgeLegacyEnvRoute),
  legacyDomains: gated(ForgeLegacyDomainsRoute),
  legacyCommand: gated(ForgeLegacyCommandRoute),
}

// ── The org-scoped pages, mounted twice ─────────────────────────────────────
//
// Every org-scoped page is mounted at its org-less path (today's) AND under
// the `/o/$org` layout (control-plane docs/design/unified-shell-and-projects.md
// §3), from this one factory so the two can never drift. The org tree is
// mounted whatever the flag `web.org_routes` says (D12): the flag only picks
// which form the links name (lib/paths/org.ts) and whether an org-less path
// redirects into the tab's org (the root's beforeLoad). Pages that read their
// params or search do so without a route id (`strict: false`), so the same
// page serves both trees.
//
//   form 'legacy'   /, /project/$projectId, /c/$chatId[/t/$threadId], /new,
//                   /workflow/…, /workflows/… (and their retired paths),
//                   /inbox, /apps, /forge/…
//   form 'org'      /o/$org, /o/$org/project/$projectId,
//                   /o/$org/c/$chatId[/t/$threadId], /o/$org/new, /o/$org/workflow/…,
//                   /o/$org/workflows/…, /o/$org/inbox, /o/$org/apps,
//                   /o/$org/sites, /o/$org/usage,
//                   /o/$org/apps/$project/… (the console),
//                   /o/$org/observability/…, /o/$org/forge (the console's entry)
function createOrgScopedRoutes<TApp extends AnyRoute, TAuth extends AnyRoute>(
  appParent: () => TApp,
  authParent: () => TAuth,
  form: 'legacy' | 'org',
) {
  // The chat home: every project's chats.
  const indexRoute = createRoute({
    getParentRoute: appParent,
    path: '/',
    component: App,
  })

  // Project routes — the URL carries the sidebar's project filter.
  // `/project/$projectId` shows one project (App reads the param via useParams
  // and calls setProjectFilter); `/` shows every project. `/project` (no id)
  // redirects to `/`.
  const projectRoute = createRoute({
    getParentRoute: appParent,
    path: 'project/$projectId',
    component: App,
  })

  // A chat, one of its threads in full view, and the new-chat view (R1–R3 of
  // the design's §9): the chat shell open on what the URL names — the stores
  // follow it (hooks/useChatRoute.ts). The same component as the home above,
  // with no options that change how the router wraps it, so moving between
  // chats (or from the home to a chat) never remounts the shell. The loader
  // finds the chat and its org: a chat in another org redirects there, an
  // unknown one is the shell's not-found page (lib/chatRouteResolve.ts).
  // Loaded on demand like the screens above: the entry chunk stays free of the chat client.
  const chatLoader = async ({ params, location, context }: { params: { chatId: string }; location: LocationLike; context: unknown }) => {
    const { resolveChatRoute } = await import('./lib/chatRouteResolve')
    return resolveChatRoute(params.chatId, location, { orgRoute: (context as { orgRoute?: OrgRouteState }).orgRoute })
  }
  const chatRoute = createRoute({
    getParentRoute: appParent,
    path: 'c/$chatId',
    validateSearch: chatViewSearchSchema,
    loader: chatLoader,
    component: App,
  })
  const threadRoute = createRoute({
    getParentRoute: appParent,
    path: 'c/$chatId/t/$threadId',
    validateSearch: chatViewSearchSchema,
    loader: chatLoader,
    component: App,
  })
  const newChatRoute = createRoute({
    getParentRoute: appParent,
    path: 'new',
    validateSearch: newChatSearchSchema,
    component: App,
  })

  // The workflow BUILDER keeps its own full-screen chrome (WorkflowHeader):
  // /workflow/new                → the New workflow dialog; Create makes the
  //                                draft (static segment, takes precedence over
  //                                the dynamic one below)
  // /workflow/$workflowName      → opens a named workflow. workflowName is the
  //                                full identifier — e.g. `builtin://get-it-right`
  //                                or a user workflow's name. URL-encoding is
  //                                handled by tanstack-router.
  // The bare /workflow (the old hub) is a redirect (workflowsAreaRoutes.tsx).
  const workflowNewRoute = createRoute({
    getParentRoute: authParent,
    path: '/workflow/new',
    validateSearch: workflowSearchSchema,
    component: () => <WorkflowPage isNew />,
  })

  const workflowBuilderRoute = createRoute({
    getParentRoute: authParent,
    path: '/workflow/$workflowName',
    validateSearch: workflowSearchSchema,
    component: () => <WorkflowPage />,
  })

  // The Workflows area — Workflows and Runs — and, in today's form, the
  // redirects from the paths it replaced. Defined in workflowsAreaRoutes.tsx so
  // the route tests mount the same definitions.
  const workflowsAreaRoutes = createWorkflowsAreaRoutes(authParent, {
    layout: WorkflowsLayout,
    library: WorkflowsListPage,
    workflowDetail: WorkflowDetailPage,
    runs: RunsPage,
    runDetail: RunDetailPage,
    automationRedirect: AutomationRedirectPage,
  }, { retiredPaths: form === 'legacy' })

  // Inbox: everything waiting on the user, across every chat and project
  // (research/WORKFLOW_UI.md §8). Top-level, not under Workflows: interactive
  // chats awaiting an answer belong in it too (§14.1 decisions 1 and 2).
  const inboxRoute = createRoute({
    getParentRoute: authParent,
    path: '/inbox',
    component: InboxPage,
  })

  // Apps: everything the user has built and deployed, with its live links
  // (components/Apps/AppsPage.tsx). The sidebar's Apps entry, and the bare
  // /forge, land here. Behind ForgeGate like every forge surface: it is the way
  // into the console, and the Developer off-switch has to turn it off too.
  const appsRoute = createRoute({
    getParentRoute: authParent,
    path: '/apps',
    validateSearch: appsSearchSchema,
    component: gated(AppsPage),
  })

  // Sites: every static site the org hosts, and its domains
  // (ForgeShell/ForgeSitesPage.tsx). Usage: the org's spend on what forge
  // provisions (ForgeShell/ForgeUsagePage.tsx). Both were the forge host's
  // own pages until one router served both hosts (U6); now every host serves
  // them, in both forms. Not behind ForgeGate: neither reaches a write path.
  const sitesRoute = createRoute({
    getParentRoute: authParent,
    path: '/sites',
    component: SitesPage,
  })

  const usageRoute = createRoute({
    getParentRoute: authParent,
    path: '/usage',
    component: UsagePage,
  })

  const forgeConsoleRoutes = createForgeConsoleRoutes(authParent, forgeConsolePageComponents, { form })

  return {
    app: [indexRoute, projectRoute, chatRoute, threadRoute, newChatRoute] as const,
    authenticated: [forgeConsoleRoutes, workflowNewRoute, workflowBuilderRoute, ...workflowsAreaRoutes, inboxRoute, appsRoute, sitesRoute, usageRoute] as const,
  }
}

const legacyOrgScopedRoutes = createOrgScopedRoutes(() => appLayoutRoute, () => authenticatedLayoutRoute, 'legacy')

// The org tree: `/o/$org` resolves the org before any page in it renders
// (orgRoutes.tsx), then the same two layouts as the org-less tree, with the
// same guards, and the same pages.
const orgLayoutRoute = createOrgLayoutRoute(() => rootRoute, {
  Guard: ({ children }) => (
    <AuthGuard requireAuth={true} requireEmailVerification={true} returnHere>
      {children}
    </AuthGuard>
  ),
})

const orgAuthenticatedLayoutRoute = createRoute({
  getParentRoute: () => orgLayoutRoute,
  id: '_authenticated',
  component: AuthenticatedLayout,
})

const orgAppLayoutRoute = createRoute({
  getParentRoute: () => orgLayoutRoute,
  id: '_app',
  validateSearch: indexSearchSchema,
  component: AppLayout,
})

const orgOrgScopedRoutes = createOrgScopedRoutes(() => orgAppLayoutRoute, () => orgAuthenticatedLayoutRoute, 'org')

// Onboarding lives at its own URL now. Previously the OnboardingPage was a
// branch inside ModernApp's render based on currentUser.onboardingCompleted —
// no URL, no deep-link, no way to navigate between onboarding and the app
// without the conditional. /onboarding owns: reset-onboarding signal, GitHub
// OAuth return at the onboarding context, and the "done — leave for /" hop.
// See OnboardingRoute.tsx.

// The forge demo console: the console and its Observability views on fixture
// data, with no daemon, chat or control-plane call (components/Forge/Demo). The
// forge journey walks it. Public, at the root and outside every guard: it
// reads nothing of the person's, the journey picker offers it to everyone, and
// the landing page's `?journey=forge` takes a signed-out visitor straight to
// it (ForgeShell/ForgeLandingJourney). `/demo`, a landing link's short form,
// points at it. Not behind ForgeGate for the same reason.
const forgeDemoRoute = createForgeDemoRoute(() => rootRoute)
const forgeDemoRedirectRoute = createForgeDemoRedirectRoute(() => rootRoute)

const onboardingRoute = createRoute({
  getParentRoute: () => authenticatedLayoutRoute,
  path: '/onboarding',
  validateSearch: onboardingSearchSchema,
  component: OnboardingRoute,
})

// ─── Mobile surface (`/m/*`) ────────────────────────────────────────────────
//
// A phone-shaped surface over the same data layer, NOT a separate app: the
// chat screen renders the very same ChatContainer the desktop uses, so every
// subscription, stream-resume and message-merge rule is shared. What differs
// is set by MobileLayout's <SurfaceProvider surface="mobile">, which drives
// the capability map in lib/surface.ts.
//
// Gated by `authenticatedLayoutRoute` for the same reason the desktop app is.
// Note there is no mobile *setup* onboarding: a user who has not completed
// onboarding is redirected from the chat screens to `/onboarding` (see
// MobileShell, OnboardingFlow/onboardingGate.ts), which is the existing
// responsive flow — the guided *tour* is what mobile skips, because it
// spotlights desktop chrome by DOM selector.
const mobileLayoutRoute = createRoute({
  getParentRoute: () => authenticatedLayoutRoute,
  id: '_mobile',
  component: MobileShell,
})

// The chats routes share one layout: single pane on a phone, list–detail at
// 840px and up, where the list stays mounted beside the routed chat (see
// MobileSplitLayout). Pathless, so the chat routes keep their paths and the
// layout owns the list: `/m/chats` itself is only the empty detail pane.
const mobileChatsLayoutRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  id: '_chats',
  component: MobileSplitLayout,
})

const mobileChatsRoute = createRoute({
  getParentRoute: () => mobileChatsLayoutRoute,
  path: '/m/chats',
  component: MobileChatsEmptyDetail,
})

const mobileChatRoute = createRoute({
  getParentRoute: () => mobileChatsLayoutRoute,
  path: '/m/chats/$chatId',
  component: MobileChatScreen,
})

// `/m/new` is registered BEFORE `/m/chats/$chatId` has a chance to matter —
// they don't overlap, but keeping creation off the `/m/chats/*` prefix means a
// future `/m/chats/new` chat can never be shadowed by this screen.
const mobileNewChatRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/new',
  validateSearch: mobileNewChatSearchSchema,
  component: MobileNewChat,
})

// Machine management is mobile-native now: list/detail routes live under the
// shell so they inherit the mobile surface provider and its lifecycle gates.
const mobileDaemonsRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/daemons',
  component: MobileDaemonList,
})

const mobileDaemonRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/daemons/$daemonId',
  component: MobileDaemonScreen,
})

// Identity only — sign out, and the theme. `settings` is false on this surface
// and stays false; `mobileAccount` is the separate, narrower capability that
// keeps a phone user from being stranded in an account they can't leave.
const mobileAccountRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/account',
  component: MobileAccountScreen,
})

const mobileProjectsRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/projects',
  component: MobileProjectList,
})

const mobileSearchRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/search',
  component: MobileSearchScreen,
})

// The catalog is the route-level workflow surface; MobileWorkflowScreen is the
// per-workflow detail view, reached from here and from a running chat, and it
// takes a resolved Workflow rather than an id.
const mobileWorkflowsRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/workflows',
  component: MobileWorkflowCatalog,
})

// The Inbox: the desktop InboxView in the mobile chrome. The desktop `/inbox`
// redirects here on a phone (lib/mobileRedirect.ts, mobilePathFor).
const mobileInboxRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/inbox',
  component: MobileInboxScreen,
})

// The run log, and an automation link resolved to its workflow: so a desktop
// /workflows/runs or automation link opened on a phone lands on its target
// (lib/mobileRedirect.ts, mobilePathFor) instead of the chat list.
const mobileRunsRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/runs',
  component: MobileRunsScreen,
})

const mobileAutomationRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/automations/$triggerId',
  component: MobileAutomationRedirect,
})

// Settings → Diagnostics at its own URL, for a support link.
const mobileDiagnosticsRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/diagnostics',
  component: MobileDiagnosticsRoute,
})

const mobileSettingsRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/settings',
  validateSearch: (search: Record<string, unknown>): { section?: string; returnTo?: string } => ({
    section: typeof search.section === 'string' ? search.section : undefined,
    returnTo: typeof search.returnTo === 'string' ? search.returnTo : undefined,
  }),
  component: MobileSettingsScreen,
})

const mobileGitHubRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/github',
  component: MobileGitHubScreen,
})

// Both of these existed as links before they existed as routes — every
// workflow card and the chat header's execution pill rendered "Not Found".
const mobileWorkflowDetailRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/workflows/$workflowName',
  component: MobileWorkflowDetailRoute,
})

const mobileChatWorkflowRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/chats/$chatId/workflow',
  component: MobileChatWorkflowRoute,
})

// `/m` is an alias for the chat list — the list is the mobile home screen.
const mobileIndexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/m',
  component: () => <Navigate to={mobileChatsPath()} />,
})

/**
 * The production route tree, on both hosts (the forge host's brand is decided
 * at the top of this file); tests mount it with a router of their own
 * (test/orgRouteHarness.ts).
 */
export const routeTree = rootRoute.addChildren([
  authRoute,
  oauthCallbackRoute,
  githubOAuthCallbackRoute,
  connectionOAuthCallbackRoute,
  oauthConsentRoute,
  cliLoginRoute,
  devicePairingRoute,
  proxyAuthRoute,
  resetPasswordRoute,
  verifyEmailRoute,
  upgradeRoute,
  claimRoute,
  acceptInviteRoute,
  designSandboxRoute,
  forgeTokenSandboxRoute,
  setupSurfacesPreviewRoute,
  forgeSecretsPreviewRoute,
  forgePanePreviewRoute,
  forgeObservabilityPreviewRoute,
  forgeTraceLatencyPreviewRoute,
  consolePrimitivesPreviewRoute,
  forgeReleasesPreviewRoute,
  forgeWorkloadsPreviewRoute,
  forgeStoragePreviewRoute,
  forgeDataPreviewRoute,
  forgeDashboardsPreviewRoute,
  forgeMetricsPreviewRoute,
  projectRootRedirectRoute,
  mobileIndexRoute,
  forgeDemoRoute,
  forgeDemoRedirectRoute,
  authenticatedLayoutRoute.addChildren([
    mobileLayoutRoute.addChildren([
      mobileChatsLayoutRoute.addChildren([mobileChatsRoute, mobileChatRoute]),
      mobileNewChatRoute,
      mobileDaemonsRoute,
      mobileDaemonRoute,
      mobileAccountRoute,
      mobileProjectsRoute,
      mobileSearchRoute,
      mobileWorkflowsRoute,
      mobileWorkflowDetailRoute,
      mobileChatWorkflowRoute,
      mobileSettingsRoute,
      mobileGitHubRoute,
      mobileInboxRoute,
      mobileRunsRoute,
      mobileAutomationRoute,
      mobileDiagnosticsRoute,
    ]),
    onboardingRoute,
    settingsRoute,
    ...settingsConnectorRoutes,
    ...settingsSectionRoutes,
    ...legacyOrgScopedRoutes.authenticated,
  ]),
  appLayoutRoute.addChildren([...legacyOrgScopedRoutes.app]),
  orgLayoutRoute.addChildren([
    orgAuthenticatedLayoutRoute.addChildren([...orgOrgScopedRoutes.authenticated]),
    orgAppLayoutRoute.addChildren([...orgOrgScopedRoutes.app]),
  ]),
])

export const router = createRouter({
  routeTree,
  // Every route is its own failure domain. Without a default, only the root
  // route had a boundary, so a screen that threw replaced the whole app —
  // the overlays beside the outlet (toasts, modals, onboarding) included —
  // rather than just itself.
  defaultErrorComponent: RouteErrorFallback,
  defaultOnCatch: (error, errorInfo) => reportCaughtError(error, errorInfo.componentStack),
})

// The mobile chat screen opens its chat from the URL, not through
// chatStore.selectChat: time each of those opens from the navigation that
// starts it (lib/routedChatOpen). Subscribed here, with the router, so the
// app's first navigation (a reload, a deep link) is seen too.
watchRoutedChatOpens(router, (pathname) => {
  const { foundRoute, routeParams } = router.getMatchedRoutes(pathname)
  return foundRoute?.id === mobileChatRoute.id ? routeParams.chatId : undefined
})

// The desktop app's `reliant://open?url=` links (a chat, from the web's "Open
// in desktop") navigate this router (lib/openRouteLink).
listenForOpenRouteLinks(router)

// Register the router on globalThis so non-React modules (e.g. projectStore)
// can drive URL changes without importing this file. Static-importing the
// router from a store creates a cycle (routes.tsx → App → store → routes.tsx)
// that reorders module init and exposes latent TDZ bugs elsewhere. The
// global-registry pattern keeps the dependency one-way: only routes.tsx
// imports the route components; stores read this at call time.
;(globalThis as { __RELIANT_ROUTER?: typeof router }).__RELIANT_ROUTER = router;

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}