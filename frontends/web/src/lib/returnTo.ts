/**
 * The open-redirect guard every `returnTo` in the app passes through.
 *
 * `startsWith('/')` alone is not enough: `//evil.com/x` is a protocol-relative
 * URL, so a browser reads it as an absolute address on another origin. Both
 * halves of the predicate are load-bearing.
 */
export const isSafeReturnTo = (
  returnTo: string | undefined | null,
): returnTo is string =>
  !!returnTo && returnTo.startsWith('/') && !returnTo.startsWith('//')
