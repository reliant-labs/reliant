/**
 * The open-redirect guard every `returnTo` in the app passes through.
 *
 * A prefix check (`startsWith('/') && !startsWith('//')`) is not enough: the
 * browser's URL parser reads a backslash as a slash and drops tabs and
 * newlines, so `/\evil.com` and `/\t/evil.com` both pass it and then
 * navigate to `//evil.com`, a protocol-relative URL on another origin. Dot
 * segments can resolve to one too (`/a/..//evil.com`). So the value is parsed
 * the way the browser will parse it, and admitted only when it stays on this
 * origin with a path that does not start with `//` (forge's v1.1.0
 * `safeReturnTo`, migrations/v1.1.0 section 3).
 */
export const isSafeReturnTo = (
  returnTo: string | undefined | null,
): returnTo is string => {
  if (!returnTo || !returnTo.startsWith('/') || returnTo.startsWith('//')) return false
  const origin = typeof window === 'undefined' ? 'http://app.invalid' : window.location.origin
  let url: URL
  try {
    url = new URL(returnTo, origin)
  } catch {
    return false
  }
  return url.origin === origin && !url.pathname.startsWith('//')
}
