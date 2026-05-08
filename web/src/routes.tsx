import { createRootRoute, createRoute, createRouter, Outlet, Navigate } from '@tanstack/react-router'
import { ErrorFallbackUI } from './components/ErrorBoundary'
import { AuthScreen } from './components/AuthScreen'
import { OAuthCallback } from './components/OAuthCallback'
import { ProxyAuth } from './components/ProxyAuth'
import { ResetPasswordScreen } from './components/ResetPasswordScreen'
import { EmailVerification } from './components/EmailVerification'
import { AuthGuard } from './components/AuthGuard'
import { DesignSandboxPage } from './components/DesignSandbox/DesignSandboxPage'
import App from './App'

const rootRoute = createRootRoute({
  component: () => <Outlet />,
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
  validateSearch: (search: Record<string, unknown>) => ({
    redirect: (search.redirect as string) || undefined,
  }),
  component: () => (
    <AuthGuard requireAuth={false}>
      <AuthScreen />
    </AuthGuard>
  ),
})

const oauthCallbackRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/callback',
  component: OAuthCallback,
})

const proxyAuthRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/proxy',
  validateSearch: (search: Record<string, unknown>) => ({
    return: (search.return as string) || undefined,
  }),
  component: ProxyAuth,
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

type IndexSearch = {
  step?: string;
  'reset-onboarding'?: boolean;
};

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  validateSearch: (search: Record<string, unknown>): IndexSearch => {
    const result: IndexSearch = {};
    if (search.step) result.step = search.step as string;
    if (search['reset-onboarding'] !== undefined) result['reset-onboarding'] = true;
    return result;
  },
  component: () => (
    <AuthGuard requireAuth={true} requireEmailVerification={true}>
      <App />
    </AuthGuard>
  ),
})

const designSandboxRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/design-sandbox',
  component: DesignSandboxPage,
})

const routeTree = rootRoute.addChildren([
  authRoute,
  oauthCallbackRoute,
  proxyAuthRoute,
  resetPasswordRoute,
  verifyEmailRoute,
  designSandboxRoute,
  indexRoute,
])

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}