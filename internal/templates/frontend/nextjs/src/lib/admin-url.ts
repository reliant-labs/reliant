/**
 * adminUrl — prefix an in-app path with the Next.js basePath so links
 * survive being mounted under a sub-route.
 *
 * Thin convenience wrapper over the generated `src/lib/basepath_gen.ts`
 * (Tier-1, regenerated every `forge generate`) — that file is the single
 * source of truth for the prefix, sourced from forge.yaml
 * `frontends[].base_path` with NEXT_PUBLIC_BASE_PATH as the only env
 * override.
 *
 * The basePath leaks into every link rendered by Next.js's `<Link>` for
 * free — but it does NOT leak into raw strings passed to APIs that
 * ultimately generate URLs (Stripe Checkout `success_url`, OAuth
 * `redirect_uri`, shareable links emailed to users, etc.). Those need
 * the prefix applied by hand, and the resulting boilerplate is the kind
 * of thing that silently breaks deploys when the basePath later changes.
 *
 * Use `adminUrl(path)` (or `joinBasePath` directly) for any string
 * passed to an external system that will round-trip back to this
 * frontend:
 *
 *   const successUrl = absoluteAdminUrl("/billing/success");
 *   await stripe.checkout.sessions.create({ success_url: successUrl, ... });
 */
import { joinBasePath } from "@/lib/basepath_gen";

export const adminUrl = (path: string): string => joinBasePath(path);

/**
 * absoluteAdminUrl — adminUrl plus the current window's origin, for
 * cases where an external service needs a fully-qualified URL
 * (Stripe Checkout, OAuth callbacks, magic links, share URLs).
 *
 * Client-only — `window.location.origin` is unavailable during SSR.
 * If you need an absolute URL during a server render, build it from
 * the incoming request's host header instead and pass the assembled
 * string in as a prop.
 */
export const absoluteAdminUrl = (path: string): string => {
  return `${window.location.origin}${adminUrl(path)}`;
};
