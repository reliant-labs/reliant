import { useEffect } from 'react'
import { createRootRoute, createRoute, createRouter, lazyRouteComponent, Outlet, Navigate, useRouterState, useNavigate } from '@tanstack/react-router'
import { SurfaceProvider } from './lib/surfaceContext'
import { surfaceForPath } from './lib/surface'
import { shouldRedirectToMobileNow } from './lib/mobileRedirect'
import {
  authSearchSchema,
  githubOAuthCallbackSearchSchema,
  indexSearchSchema,
  mobileNewChatSearchSchema,
  oauthCallbackSearchSchema,
  onboardingSearchSchema,
  proxyAuthSearchSchema,
  settingsParamsSchema,
  upgradeSearchSchema,
  workflowSearchSchema,
} from './routeSchemas'
import { ErrorFallbackUI } from './components/ErrorBoundary'
import { AuthGuard } from './components/AuthGuard'
import { ModalLayer } from './components/Modals/ModalLayer'
import { AnonSignInNudge } from './components/AnonSignInNudge'
import { Toaster } from './lib/toast'
import { ContextualTipsLayer, OnboardingWizard } from './components/Onboarding'
import { GitHubSyncStatus } from './components/Layout/GitHubSyncBanner'

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
const AuthScreen = lazyRouteComponent(
  () => import('./components/AuthScreen'), 'AuthScreen')
const OAuthCallback = lazyRouteComponent(
  () => import('./components/OAuthCallback'), 'OAuthCallback')
const GitHubOAuthCallback = lazyRouteComponent(
  () => import('./components/GitHubOAuthCallback'), 'GitHubOAuthCallback')
const ProxyAuth = lazyRouteComponent(
  () => import('./components/ProxyAuth'), 'ProxyAuth')
const ResetPasswordScreen = lazyRouteComponent(
  () => import('./components/ResetPasswordScreen'), 'ResetPasswordScreen')
const EmailVerification = lazyRouteComponent(
  () => import('./components/EmailVerification'), 'EmailVerification')
const UpgradeAccount = lazyRouteComponent(
  () => import('./components/UpgradeAccount'), 'UpgradeAccount')
const DesignSandboxPage = lazyRouteComponent(
  () => import('./components/DesignSandbox/DesignSandboxPage'), 'DesignSandboxPage')
const SettingsPage = lazyRouteComponent(
  () => import('./components/Settings/SettingsPage'), 'SettingsPage')
const ConnectorConsentPage = lazyRouteComponent(
  () => import('./components/Settings/ConnectorConsentPage'), 'ConnectorConsentPage')
const WorkflowPage = lazyRouteComponent(
  () => import('./components/workflow/WorkflowPage'), 'WorkflowPage')
const OnboardingRoute = lazyRouteComponent(
  () => import('./components/OnboardingFlow/OnboardingRoute'), 'OnboardingRoute')
const MobileShell = lazyRouteComponent(
  () => import('./components/Mobile/MobileShell'), 'MobileShell')
const MobileChatList = lazyRouteComponent(
  () => import('./components/Mobile/MobileChatList'), 'MobileChatList')
const MobileChatScreen = lazyRouteComponent(
  () => import('./components/Mobile/MobileChatScreen'), 'MobileChatScreen')
const MobileNewChat = lazyRouteComponent(
  () => import('./components/Mobile/MobileNewChat'), 'MobileNewChat')
const MobileDaemonList = lazyRouteComponent(
  () => import('./components/Mobile/MobileDaemonList'), 'MobileDaemonList')
const MobileDaemonScreen = lazyRouteComponent(
  () => import('./components/Mobile/MobileDaemonScreen'), 'MobileDaemonScreen')
const MobileAccountScreen = lazyRouteComponent(
  () => import('./components/Mobile/MobileAccountScreen'), 'MobileAccountScreen')
const MobileProjectList = lazyRouteComponent(
  () => import('./components/Mobile/MobileProjectList'), 'MobileProjectList')
const MobileSearchScreen = lazyRouteComponent(
  () => import('./components/Mobile/MobileSearchScreen'), 'MobileSearchScreen')
const MobileWorkflowCatalog = lazyRouteComponent(
  () => import('./components/Mobile/MobileWorkflowCatalog'), 'MobileWorkflowCatalog')
const MobileSettingsScreen = lazyRouteComponent(
  () => import('./components/Mobile/MobileSettingsScreen'), 'MobileSettingsScreen')
const MobileWorkflowDetailRoute = lazyRouteComponent(
  () => import('./components/Mobile/MobileWorkflowDetailRoute'), 'MobileWorkflowDetailRoute')
const MobileChatWorkflowRoute = lazyRouteComponent(
  () => import('./components/Mobile/MobileWorkflowDetailRoute'), 'MobileChatWorkflowRoute')
const App = lazyRouteComponent(() => import('./App'), 'default')

// Search schemas live in ./routeSchemas (kept dependency-free so tests can
// import them without dragging in the route-tree's component graph).

// Truly app-global overlays live at the root so they render on every route —
// including /settings, /workflow/*, and the unauthenticated /auth routes. They
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
    if (shouldRedirectToMobileNow({ pathname, search: searchString })) {
      navigate({ to: "/m/chats", replace: true });
    }
  }, [pathname, search, navigate]);

  return (
    <SurfaceProvider surface={surfaceForPath(pathname)}>
      <Outlet />
      <ModalLayer />
      <AnonSignInNudge />
      <Toaster />
      <ContextualTipsLayer />
      <GitHubSyncStatus />
      <OnboardingWizard />
    </SurfaceProvider>
  );
}

const rootRoute = createRootRoute({
  component: RootShell,
  notFoundComponent: () => <Navigate to="/" search={{}} />,
  errorComponent: ({ error }) => (
    <ErrorFallbackUI
      error={error}
      onReload={() => window.location.reload()}
    />
  ),
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
  component: lazyRouteComponent(
    () => import('./components/Auth/OAuthConsent'),
    'OAuthConsent',
  ),
})

const proxyAuthRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/proxy',
  validateSearch: proxyAuthSearchSchema,
  component: ProxyAuth,
})

// Pathless layout route that gates the settings + workflow group on auth +
// email verification. Avoids re-declaring <AuthGuard requireAuth requireEmailVerification>
// on every one of those routes. The `_app` layout below has its own AuthGuard
// (kept separate so its full route id remains '/_app' — production code uses
// useSearch({ from: '/_app' }) and that id collapses if we nest it here).
// Reset-password and verify-email have different gating, so they stay as
// direct children of rootRoute.
const authenticatedLayoutRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: '_authenticated',
  component: () => (
    <AuthGuard requireAuth={true} requireEmailVerification={true}>
      <Outlet />
    </AuthGuard>
  ),
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

// Pathless layout route that owns the App shell + its shared search schema.
// Children `/` and `/project/$projectId` both render <App />; placing the
// validateSearch + AuthGuard on the parent means components inside App can
// always read search via useSearch({ from: '/_app' }) regardless of which
// child the user is on. Without this, useSearch({ from: '/' }) would throw
// "Could not find an active match from /" the moment we navigate to a project.
//
// Kept as a direct child of rootRoute (not nested under _authenticated) so its
// full id remains '/_app' — call sites use useSearch({ from: '/_app' }).
const appLayoutRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: '_app',
  validateSearch: indexSearchSchema,
  component: () => (
    <AuthGuard requireAuth={true} requireEmailVerification={true}>
      <Outlet />
    </AuthGuard>
  ),
})

const indexRoute = createRoute({
  getParentRoute: () => appLayoutRoute,
  path: '/',
  component: App,
})

// Project routes — the URL is the source of truth for the currently-selected
// project. `/project/$projectId` deep-links into a specific project; the App
// component reads the param via useParams and calls selectProject. `/project`
// (no id) redirects to `/` (the project picker view inside App).
const projectRoute = createRoute({
  getParentRoute: () => appLayoutRoute,
  path: 'project/$projectId',
  component: App,
})

const projectPickerRedirectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/project',
  component: () => <Navigate to="/" search={{}} />,
})

const designSandboxRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/design-sandbox',
  component: DesignSandboxPage,
})

// Settings routes — replace the old viewerStore.isSettingsMode + settingsSection
// flags. `/settings` is the bare entry (defaults to the page's first section).
// `/settings/$section` deep-links to a specific section.
const settingsRoute = createRoute({
  getParentRoute: () => authenticatedLayoutRoute,
  path: '/settings',
  component: SettingsPage,
})

// OAuth consent: where a third-party application's user chooses which
// connector it may act through. Under the authenticated layout, so the page
// already knows who the user is from the existing Supabase session — no new
// browser-auth path is needed.
const connectorConsentRoute = createRoute({
  getParentRoute: () => authenticatedLayoutRoute,
  path: '/settings/connectors/authorize',
  component: ConnectorConsentPage,
})

const settingsSectionRoute = createRoute({
  getParentRoute: () => authenticatedLayoutRoute,
  path: '/settings/$section',
  // Validate $section against the known settings ids. parseParams throws on a
  // bad value; the parent rootRoute's notFoundComponent then redirects to /.
  // SettingsPage also defensively coerces unknown values, so even if the
  // schema were relaxed the UI wouldn't break — this just keeps bad URLs from
  // silently rendering a default section.
  parseParams: (params) => settingsParamsSchema.parse(params),
  stringifyParams: (params) => ({ section: params.section }),
  component: SettingsPage,
})

// Workflow routes — replace viewerStore.isWorkflowMode + workflowToOpen.
// /workflow                    → hub view (browse templates, list saved)
// /workflow/new                → new blank workflow (static segment, takes
//                                precedence over the dynamic one below)
// /workflow/$workflowName      → opens a named workflow. workflowName is the
//                                full identifier — e.g. `builtin://get-it-right`
//                                or a user workflow's name. URL-encoding is
//                                handled by tanstack-router.
const workflowHubRoute = createRoute({
  getParentRoute: () => authenticatedLayoutRoute,
  path: '/workflow',
  validateSearch: workflowSearchSchema,
  component: () => <WorkflowPage />,
})

const workflowNewRoute = createRoute({
  getParentRoute: () => authenticatedLayoutRoute,
  path: '/workflow/new',
  validateSearch: workflowSearchSchema,
  component: () => <WorkflowPage isNew />,
})

const workflowBuilderRoute = createRoute({
  getParentRoute: () => authenticatedLayoutRoute,
  path: '/workflow/$workflowName',
  validateSearch: workflowSearchSchema,
  component: () => <WorkflowPage />,
})

// Onboarding lives at its own URL now. Previously the OnboardingPage was a
// branch inside ModernApp's render based on currentUser.onboardingCompleted —
// no URL, no deep-link, no way to navigate between onboarding and the app
// without the conditional. /onboarding owns: reset-onboarding signal, GitHub
// OAuth return at the onboarding context, and the "done — leave for /" hop.
// See OnboardingRoute.tsx.
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
// onboarding is redirected to `/onboarding` (see MobileShell), which is the
// existing responsive flow — the guided *tour* is what mobile skips, because
// it spotlights desktop chrome by DOM selector.
const mobileLayoutRoute = createRoute({
  getParentRoute: () => authenticatedLayoutRoute,
  id: '_mobile',
  component: MobileShell,
})

const mobileChatsRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/chats',
  component: MobileChatList,
})

const mobileChatRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
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

// Daemons are view + Resume only on this surface (daemonManage: false); the
// screens enforce that, the routes just have to exist under the shell so they
// inherit the surface provider.
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

const mobileSettingsRoute = createRoute({
  getParentRoute: () => mobileLayoutRoute,
  path: '/m/settings',
  component: MobileSettingsScreen,
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
  component: () => <Navigate to="/m/chats" />,
})

const routeTree = rootRoute.addChildren([
  authRoute,
  oauthCallbackRoute,
  githubOAuthCallbackRoute,
  oauthConsentRoute,
  proxyAuthRoute,
  resetPasswordRoute,
  verifyEmailRoute,
  upgradeRoute,
  designSandboxRoute,
  projectPickerRedirectRoute,
  mobileIndexRoute,
  authenticatedLayoutRoute.addChildren([
    mobileLayoutRoute.addChildren([
      mobileChatsRoute,
      mobileChatRoute,
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
    ]),
    onboardingRoute,
    settingsRoute,
    connectorConsentRoute,
    settingsSectionRoute,
    workflowHubRoute,
    workflowNewRoute,
    workflowBuilderRoute,
  ]),
  appLayoutRoute.addChildren([indexRoute, projectRoute]),
])

export const router = createRouter({ routeTree })

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