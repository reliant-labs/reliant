// RouteGuard: a signed-out visitor is sent to sign in, not shown a wall of 401s.
// `auth_required: false` publishes an RPC, NOT a page: list public pages in publicExactPaths or
// publicPaths below; links ask useCanVisit(). Depth: `forge skill load auth/public-pages`.
//
// reliant: copied (owned) from forge's scaffold, shared-web/src/lib/auth/route-guard.tsx.tmpl
// (forge 3b80ee2f5), and used only with forge sign-in (AUTH_PROVIDER "forge"):
// components/AuthGuard.tsx mounts it for every route that needs a session. What
// differs from forge's file:
//   - who is signed in comes from reliant's facade (lib/auth/use-auth.ts), whose store
//     asked the login broker (GET /auth/session; the cookie is HttpOnly);
//   - the sign-in page is reliant's `/auth`, and the way back is its `redirect` param
//     (lib/returnTo signInRedirectFor: none for `/` or an auth page);
//   - the public pages are reliant's: `/auth` itself plus forge's auth pages;
//   - the pending state is reliant's LoadingSpinner.

import { useCallback, useEffect, useSyncExternalStore } from "react";

import { LoadingSpinner } from "@/components/Layout/LoadingSpinner";
import {
  authPath,
  confirmEmailChangePath,
  forgeForgotPasswordPath,
  forgeMagicLinkPath,
  forgeResetPasswordPath,
  forgeSignInPath,
  forgeSocialCallbackPath,
  forgeVerifyEmailPath,
} from "@/lib/paths";
import { signInRedirectFor } from "@/lib/returnTo";
import { RouteAccessContext } from "./route-access_gen";
import { useAuth } from "./use-auth";

/**
 * A store that never changes, for the hydration flag below.
 *
 * Module scope, not inline: `useSyncExternalStore` re-subscribes whenever
 * `subscribe` changes identity, so a fresh arrow per render would tear down
 * and re-establish the subscription on every render.
 */
const subscribeToNothing = () => () => {};

/** Where an anonymous visitor is sent: reliant's sign-in screen. */
const SIGN_IN_PATH = authPath();

/**
 * forge's own sign-in path. The broker sends a failed Google or GitHub
 * round trip there (devidp.SocialFailurePath); the SPA renders the same
 * screen at it.
 */
const FORGE_SIGN_IN_PATH = forgeSignInPath();

/**
 * The password-reset screens. Public for the same reason sign-in is: the
 * person using them has no session — that is why they are resetting.
 */
const FORGOT_PATH = forgeForgotPasswordPath();
const RESET_PATH = forgeResetPasswordPath();

/**
 * The pages an emailed link opens (a magic link, an address confirmation)
 * and the page a Google or GitHub sign-in returns to. Public for the same
 * reason: whoever opens them has no session yet.
 *
 * reliant: every path comes from lib/paths, the only place a page's path is
 * spelled. forge's `/auth/invite` is not here because reliant has no such
 * page yet; it joins with its builder when one exists.
 */
const EMAIL_LINK_PATHS = [
  forgeMagicLinkPath(),
  forgeVerifyEmailPath(),
  forgeSocialCallbackPath(),
  // reliant: a changed address's confirmation link (forge account settings).
  confirmEmailChangePath(),
];

/**
 * Paths that render without a session.
 *
 * The sign-in route MUST be public: sending an anonymous visitor from the
 * sign-in page to the sign-in page is a redirect loop.
 *
 * There is no `/auth/callback` in this list because forge sign-in has no OIDC
 * callback in the browser: it posts credentials to reliant-api's broker and
 * the server does the exchange. (reliant's `/auth/callback` is Supabase's,
 * mounted without a guard; /auth/social/callback receives no code, only a
 * handle the broker checks against the sign-in this browser started.)
 */
const publicPaths = [FORGE_SIGN_IN_PATH, FORGOT_PATH, RESET_PATH, ...EMAIL_LINK_PATHS];

/**
 * Single pages that render without a session, matched EXACTLY: "/pricing"
 * opens /pricing and not /pricing/admin.
 *
 * `/auth` is exact, not a prefix: `/auth/proxy`, `/auth/github/callback` and
 * the rest are their own routes with their own gating.
 */
const publicExactPaths: string[] = [SIGN_IN_PATH];

/** The path without a trailing slash ("/docs/" is "/docs"); the root stays "/". */
function normalize(pathname: string): string {
  const trimmed = pathname.replace(/\/+$/, "");
  return trimmed === "" ? "/" : trimmed;
}

/** The two lists above, as the guard reads them. */
interface PublicRoutes {
  exact: readonly string[];
  prefixes: readonly string[];
}

const PUBLIC_ROUTES: PublicRoutes = { exact: publicExactPaths, prefixes: publicPaths };

/**
 * Whether `pathname` renders for a visitor with no session.
 *
 * Not exported: a module that exports anything but components loses React
 * Fast Refresh. Links ask through useCanVisit() (route-access_gen.ts).
 */
function isPublicPath(pathname: string, routes: PublicRoutes = PUBLIC_ROUTES): boolean {
  const path = normalize(pathname);
  if (routes.exact.some((p) => normalize(p) === path)) return true;
  return routes.prefixes.some((raw) => {
    const p = normalize(raw);
    // "/" is the home page, never a prefix: that would be every path.
    if (p === "/") return path === "/";
    return path === p || path.startsWith(`${p}/`);
  });
}

/**
 * Gates its children on a session, sending anonymous visitors to sign in.
 *
 * `navigate` and `pathname` are injected rather than imported from a router:
 * the caller (AuthGuard) owns routing, and this component can be tested with
 * no router mounted at all. `navigate` receives the whole sign-in href
 * (`/auth?redirect=…`).
 *
 * `publicRoutes` replaces the two lists above for one mount. The app never
 * passes it — edit the lists — but a test may, to pin the matching rules.
 */
export function RouteGuard({
  pathname,
  navigate,
  children,
  publicRoutes = PUBLIC_ROUTES,
}: {
  pathname: string;
  navigate: (path: string) => void;
  children: React.ReactNode;
  publicRoutes?: PublicRoutes;
}) {
  const { signedIn, loading, initialized } = useAuth();
  const isAuthenticated = signedIn;
  // Restoring a session (the first /auth/session) or switching identities.
  const isLoading = !initialized || loading;

  // Native sign-in is ALWAYS enforced: the browser holds no issuer config to
  // ask about; the server answers "who is signed in?" over reliant-api.
  const enforcing = true;
  const blocked =
    enforcing && !isLoading && !isAuthenticated && !isPublicPath(pathname, publicRoutes);

  // Redirect from an effect, not during render: navigating while rendering is
  // a side effect in the render phase, which React is entitled to run twice
  // (and does, under StrictMode in development).
  useEffect(() => {
    if (!blocked) return;
    // Where to come back to, query string included: a deep link's state lives
    // there. The sign-in screen reads `redirect` and lands the person on the
    // page they asked for instead of home.
    const redirect = signInRedirectFor(pathname + window.location.search);
    navigate(redirect ? `${SIGN_IN_PATH}?redirect=${encodeURIComponent(redirect)}` : SIGN_IN_PATH);
  }, [blocked, navigate, pathname]);

  // forge's file renders a placeholder on the first (hydrating) render so a
  // server render and the client agree; reliant's web is a client-only SPA,
  // where `useSyncExternalStore` returns the client snapshot at once, so this
  // costs nothing and keeps the file close to forge's.
  const hydrated = useSyncExternalStore(
    subscribeToNothing,
    () => true,
    () => false,
  );

  // What every link under the guard may offer (route-access_gen.ts). Until
  // the session is KNOWN, only public routes, so a signed-out visitor's
  // links never flash up and vanish.
  const signedInKnown = hydrated && !isLoading && isAuthenticated;
  const canVisit = useCallback(
    (path: string) => !enforcing || signedInKnown || isPublicPath(path, publicRoutes),
    [enforcing, signedInKnown, publicRoutes],
  );
  const render = (tree: React.ReactNode) => (
    <RouteAccessContext.Provider value={canVisit}>{tree}</RouteAccessContext.Provider>
  );

  // A PUBLIC path is never gated, so it renders immediately — without
  // waiting on a session it does not need. This check comes FIRST: a spinner
  // on the sign-in route would hide the one form that ends the signed-out
  // state behind a wait nothing on that route can resolve.
  if (isPublicPath(pathname, publicRoutes)) return render(children);

  if (!hydrated) return <LoadingSpinner />;

  // While the store is restoring a session, show the pending state rather
  // than the signed-out view: the sign-in screen flashed at a person who IS
  // signed in is a worse flicker.
  if (enforcing && isLoading) return <LoadingSpinner />;

  // Already redirecting — do not render the protected tree, or its queries
  // fire the 401s this component exists to prevent.
  if (blocked) return <LoadingSpinner />;

  return render(children);
}
