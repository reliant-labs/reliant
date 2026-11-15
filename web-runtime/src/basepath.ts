// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The mount-prefix primitives: normalising a raw `base_path` value, and
// prefixing an app-internal path with it.
//
// WHY THIS IS A LIBRARY AND NOT A GENERATED FILE. The prefix ITSELF is
// project-specific — it comes from forge.yaml's `frontends[].base_path`, with
// NEXT_PUBLIC_BASE_PATH as the build-time override. The RULES for handling it
// are not: leading slash, no trailing slash, idempotent joins, absolute URLs
// untouched. Those rules are identical in every project, and every one of the
// edge cases below is one somebody has shipped a broken deploy over. So the
// project keeps the value and this module keeps the behaviour.
//
// WHEN AN APP NEEDS THIS AT ALL. Next.js prepends the configured basePath to
// `<Link href>` and `useRouter().push()` on its own — keep passing
// app-relative paths there. This is for the URLs Next.js never sees:
//
//   - window.location-built absolute URLs handed to external systems that
//     round-trip back into the app: payment-provider redirect / return URLs
//     (Stripe success_url / cancel_url), OAuth redirect_uri, share links,
//     magic-link emails;
//   - raw fetch() / EventSource / <a> / <img> paths built by string
//     concatenation rather than rendered through Next's router.
//
// The anti-pattern this exists to kill is the hand-baked prefix literal
// (`"/admin" + path`) and the bare `/route` string in a hand-built URL — they
// bypass the prefix, or double it, the day the mount point moves.
//
// This module is in the BARREL, not behind a subpath: it is three string
// functions with no imports at all, so there is no optional dependency for a
// subpath to fence off and nothing for a bundler to fail to shake out.

/**
 * normalizeBasePath canonicalises a raw base-path value: ensure a leading
 * "/", drop any trailing "/". "" and "/" both mean "served from the host
 * root" and both normalise to "".
 *
 *   normalizeBasePath("admin")   -> "/admin"
 *   normalizeBasePath("/admin/") -> "/admin"
 *   normalizeBasePath("/")       -> ""
 */
export function normalizeBasePath(raw: string): string {
  if (!raw || raw === "/") return "";
  const withLeading = raw.startsWith("/") ? raw : `/${raw}`;
  return withLeading.endsWith("/") ? withLeading.slice(0, -1) : withLeading;
}

/**
 * joinBasePath prefixes an app-internal path with an ALREADY-NORMALISED base
 * path (see normalizeBasePath).
 *
 * Contract:
 *   - Idempotent: a path already under basePath is returned unchanged, so
 *     double-wrapping never double-prefixes.
 *   - Absolute http(s) URLs pass through untouched.
 *   - Accepts paths with or without a leading "/".
 *
 * Examples (basePath "/admin"):
 *   joinBasePath("/admin", "/billing/success") -> "/admin/billing/success"
 *   joinBasePath("/admin", "billing/success")  -> "/admin/billing/success"
 *   joinBasePath("/admin", "/admin/billing")   -> "/admin/billing"  (idempotent)
 *   joinBasePath("/admin", "/")                -> "/admin"
 *   joinBasePath("/admin", "https://x.test/y") -> "https://x.test/y"
 *
 * Most callers want the bound one-argument form from createBasePath() rather
 * than repeating the prefix at every call site.
 */
export function joinBasePath(basePath: string, path: string): string {
  if (/^https?:\/\//i.test(path)) return path;
  const withLeading = path.startsWith("/") ? path : `/${path}`;
  if (!basePath) return withLeading;
  if (withLeading === basePath || withLeading.startsWith(`${basePath}/`)) {
    return withLeading;
  }
  if (withLeading === "/") return basePath;
  return `${basePath}${withLeading}`;
}

/** What createBasePath() hands back: the normalised prefix and a joiner bound to it. */
export interface BasePathHelpers {
  /**
   * The effective mount prefix, normalised. "" means the frontend is served
   * from the host root.
   */
  BASE_PATH: string;
  /** joinBasePath, bound to BASE_PATH — the one-argument form apps call. */
  joinBasePath: (path: string) => string;
}

/**
 * createBasePath binds the prefix once, at module scope, and returns the pair
 * an app actually imports.
 *
 * This is the seam forge's generated `src/lib/basepath_gen.ts` is reduced to:
 *
 *   export const { BASE_PATH, joinBasePath } = createBasePath(
 *     process.env.NEXT_PUBLIC_BASE_PATH ?? "/admin",
 *   );
 *
 * The `process.env.NEXT_PUBLIC_*` read STAYS in project code and cannot move
 * here: Next.js inlines those literals by substituting them into the sources
 * it compiles, so a library reading process.env at runtime finds nothing in a
 * browser bundle. The value is the project's; the behaviour is this package's.
 */
export function createBasePath(raw: string): BasePathHelpers {
  const BASE_PATH = normalizeBasePath(raw);
  return {
    BASE_PATH,
    joinBasePath: (path: string) => joinBasePath(BASE_PATH, path),
  };
}
