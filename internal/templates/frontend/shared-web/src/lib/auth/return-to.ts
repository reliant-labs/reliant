// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Where to send someone after they sign in: the `returnTo` the route guard
// put in the sign-in URL, if — and only if — it is a page of THIS app.
//
// The value is attacker-supplied: anyone can mail a victim a sign-in link
// whose returnTo points at a look-alike site. A prefix check ("starts with /,
// not //") is not enough, because the browser's URL parser is more liberal
// than the check: `/\evil.com` (a backslash, `%5C` in the link) and
// `/<tab>/evil.com` (`%09`; tabs and newlines are stripped) both become
// `//evil.com`, another site. So the value is parsed the way the browser will
// parse it, and kept only when the result is on this page's own origin AND
// its path does not itself start with `//`: dot segments and a same-origin
// absolute URL (`/.//evil.com`, `/a/..//evil.com`, `<this origin>//evil.com`)
// resolve to the path `//evil.com`, which location.assign() reads as another
// site.

/**
 * The same-origin path (with its query and fragment) to land on, or "/".
 * Outside a browser (server rendering) it is always "/".
 */
export function safeReturnTo(requested: string | null | undefined): string {
  if (!requested || typeof window === "undefined") return "/";
  let url: URL;
  try {
    url = new URL(requested, window.location.origin);
  } catch {
    return "/";
  }
  if (url.origin !== window.location.origin || url.pathname.startsWith("//")) return "/";
  return url.pathname + url.search + url.hash;
}
