// `reliant://` deep links, parsed. main.js's handleDeepLink acts on the result;
// it lives here, apart from main.js, so it can be tested.
//
//   reliant://open?path=<dir>      open the project at that directory (the CLI's
//                                  `reliant <path>`): "open-project"
//   reliant://open?url=<app path>  open a page of the app: a chat
//                                  (`/o/<org>/c/<id>`, `/c/<id>`, a thread of
//                                  one) or the new-chat view, the web app's
//                                  "Open in desktop" (control-plane
//                                  docs/design/unified-shell-and-projects.md
//                                  §10.3): "open-route"
//
// `url` is additive: a release before it ignores the parameter and the link
// does nothing, as it did. When both are given, a valid `url` wins.

// The pages a deep link may open: a chat or a thread of it (`/c/…`, and every
// page under an org, `/o/…`) and the new-chat view (`/new`). An allowlist,
// widened by the page that needs it: a link from outside the app is untrusted
// input, and each of these pages resolves what it names itself (a chat that is
// not the person's is not found).
const ROUTE_PREFIXES = ["/c/", "/o/"];
const ROUTE_PATHS = ["/new"];

/**
 * The app path `value` names, or null when it is not one a link may open:
 * absolute within the app (`/…`, never `//host` or a scheme), no backslash or
 * control character, and under one of ROUTE_PREFIXES. Returns the path with
 * its search and hash, normalized by the URL parser.
 */
function appRouteOf(value) {
  if (typeof value !== "string" || value === "") return null;
  if (!value.startsWith("/") || value.startsWith("//")) return null;
  // eslint-disable-next-line no-control-regex
  if (/[\\\u0000-\u001f\u007f]/.test(value)) return null;
  let parsed;
  try {
    parsed = new URL(value, "app://bundle/");
  } catch {
    return null;
  }
  if (parsed.protocol !== "app:" || parsed.host !== "bundle") return null;
  const route = `${parsed.pathname}${parsed.search}${parsed.hash}`;
  const allowed = ROUTE_PATHS.includes(parsed.pathname) || ROUTE_PREFIXES.some((prefix) => parsed.pathname.startsWith(prefix));
  return allowed ? route : null;
}

/**
 * What a `reliant://` link asks for:
 *   { kind: "route", route }     open an app page ("open-route")
 *   { kind: "project", path }    open a project directory ("open-project")
 *   { kind: "ignored", reason }  nothing to do (logged by the caller)
 */
function parseDeepLink(url) {
  let parsed;
  try {
    parsed = new URL(url);
  } catch {
    return { kind: "ignored", reason: "unparseable" };
  }
  if (parsed.protocol !== "reliant:") return { kind: "ignored", reason: "not a reliant:// link" };
  if (parsed.hostname !== "open") return { kind: "ignored", reason: `unknown host ${parsed.hostname}` };
  // searchParams values are already decoded.
  const route = appRouteOf(parsed.searchParams.get("url"));
  if (route) return { kind: "route", route };
  // Decoded once more, as this link has always been read (`reliant <path>` writes it so).
  const rawPath = parsed.searchParams.get("path");
  if (rawPath) {
    try {
      const projectPath = decodeURIComponent(rawPath);
      if (projectPath) return { kind: "project", path: projectPath };
    } catch {
      return { kind: "ignored", reason: "undecodable path" };
    }
  }
  return { kind: "ignored", reason: "no url or path" };
}

module.exports = { appRouteOf, parseDeepLink };
