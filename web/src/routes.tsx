import { createRootRoute, createRoute, createRouter, Outlet, Navigate } from '@tanstack/react-router'
import {
  authSearchSchema,
  indexSearchSchema,
  oauthCallbackSearchSchema,
  onboardingSearchSchema,
  proxyAuthSearchSchema,
  settingsParamsSchema,
  upgradeSearchSchema,
  workflowSearchSchema,
} from './routeSchemas'
import { ErrorFallbackUI } from './components/ErrorBoundary'
import { AuthScreen } from './components/AuthScreen'
import { OAuthCallback } from './components/OAuthCallback'
import { ProxyAuth } from './components/ProxyAuth'
import { ResetPasswordScreen } from './components/ResetPasswordScreen'
import { EmailVerification } from './components/EmailVerification'
import { UpgradeAccount } from './components/UpgradeAccount'
import { AuthGuard } from './components/AuthGuard'
import { DesignSandboxPage } from './components/DesignSandbox/DesignSandboxPage'
import { SettingsPage } from './components/Settings/SettingsPage'
import { WorkflowPage } from './components/workflow/WorkflowPage'
import { OnboardingRoute } from './components/OnboardingFlow/OnboardingRoute'
import { ModalLayer } from './components/Modals/ModalLayer'
import { Toaster } from './lib/toast'
import { ContextualTipsLayer, OnboardingWizard } from './components/Onboarding'
import { GitHubSyncStatus } from './components/Layout/GitHubSyncBanner'
import App from './App'

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
const rootRoute = createRootRoute({
  component: () => (
    <>
      <Outlet />
      <ModalLayer />
      <Toaster />
      <ContextualTipsLayer />
      <GitHubSyncStatus />
      <OnboardingWizard />
    </>
  ),
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

const routeTree = rootRoute.addChildren([
  authRoute,
  oauthCallbackRoute,
  proxyAuthRoute,
  resetPasswordRoute,
  verifyEmailRoute,
  upgradeRoute,
  designSandboxRoute,
  projectPickerRedirectRoute,
  authenticatedLayoutRoute.addChildren([
    onboardingRoute,
    settingsRoute,
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