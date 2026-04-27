import { createRootRoute, createRoute, createRouter, Outlet, Navigate } from '@tanstack/react-router'
import { ErrorFallbackUI } from './components/ErrorBoundary'
import { AuthScreen } from './components/AuthScreen'
import { OAuthCallback } from './components/OAuthCallback'
import { ProxyAuth } from './components/ProxyAuth'
import { ResetPasswordScreen } from './components/ResetPasswordScreen'
import { EmailVerification } from './components/EmailVerification'
import { AuthGuard } from './components/AuthGuard'
import App from './App'

const rootRoute = createRootRoute({
  component: () => <Outlet />,
  notFoundComponent: () => <Navigate to="/" />,
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

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  component: () => (
    <AuthGuard requireAuth={true} requireEmailVerification={true}>
      <App />
    </AuthGuard>
  ),
})

const routeTree = rootRoute.addChildren([authRoute, oauthCallbackRoute, proxyAuthRoute, resetPasswordRoute, verifyEmailRoute, indexRoute])

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}