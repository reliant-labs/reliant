// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Pins which routes src/lib/auth/route-guard.tsx lets a signed-out visitor
// open, and that links reading useCanVisit() get the same answer.
//
// The bug this guards: the sidebar offered a signed-out visitor every admin
// route, and each one bounced them to sign in. The guard is the one place
// that decides what is public, so the links ask it rather than keep a list of
// their own.
//
// The session is stubbed (./use-auth), so these tests are about the guard's
// decisions, not about how the session is fetched (context.test.tsx).

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const session = vi.hoisted(() => ({ isAuthenticated: false, isLoading: false }));
vi.mock("./use-auth", () => ({ useAuth: () => session }));

import { useCanVisit } from "./route-access_gen";
import { RouteGuard } from "./route-guard";

/** Prints the route access the guard hands down, one line per path. */
function Links({ paths }: { paths: string[] }) {
  const canVisit = useCanVisit();
  return (
    <ul>
      {paths.map((p) => (
        <li key={p}>{`${p}: ${canVisit(p) ? "open" : "hidden"}`}</li>
      ))}
    </ul>
  );
}

type PublicRoutes = { exact: string[]; prefixes: string[] };

function renderAt(pathname: string, paths: string[] = [], publicRoutes?: PublicRoutes) {
  const navigate = vi.fn();
  render(
    <RouteGuard pathname={pathname} navigate={navigate} publicRoutes={publicRoutes}>
      <p>page</p>
      <Links paths={paths} />
    </RouteGuard>,
  );
  return navigate;
}

afterEach(() => {
  cleanup();
  session.isAuthenticated = false;
  session.isLoading = false;
});

describe("RouteGuard", () => {
  it("renders the sign-in screens for a signed-out visitor, with or without a trailing slash", () => {
    for (const path of ["/auth/sign-in", "/auth/sign-in/", "/auth/reset"]) {
      const navigate = renderAt(path);
      expect(screen.getByText("page")).toBeTruthy();
      expect(navigate).not.toHaveBeenCalled();
      cleanup();
    }
  });

  it("sends a signed-out visitor on a gated page to sign in, with where they were going", () => {
    const navigate = renderAt("/orders");
    expect(screen.queryByText("page")).toBeNull();
    expect(navigate).toHaveBeenCalledWith(
      `/auth/sign-in?returnTo=${encodeURIComponent("/orders")}`,
    );
  });

  it("treats a public section as a prefix only at a path boundary", () => {
    // /auth/sign-in is a section: what is under it is public, a sibling that
    // merely starts with the same letters is not.
    renderAt("/auth/sign-in", ["/auth/sign-in/sso", "/auth/sign-inx", "/"]);
    expect(screen.getByText("/auth/sign-in/sso: open")).toBeTruthy();
    expect(screen.getByText("/auth/sign-inx: hidden")).toBeTruthy();
    // Not listed by default: the scaffolded home page calls RPCs that need
    // a session. Add "/" to publicExactPaths to make it public.
    expect(screen.getByText("/: hidden")).toBeTruthy();
  });

  it("offers a signed-in visitor every route", () => {
    session.isAuthenticated = true;
    renderAt("/orders", ["/", "/orders", "/admin"]);
    for (const p of ["/", "/orders", "/admin"]) {
      expect(screen.getByText(`${p}: open`)).toBeTruthy();
    }
  });

  it("offers only public routes until the session is known", () => {
    // A signed-in visitor's links arrive a moment late; a signed-out
    // visitor's admin links never flash up.
    session.isLoading = true;
    renderAt("/auth/sign-in", ["/orders", "/auth/forgot"]);
    expect(screen.getByText("/orders: hidden")).toBeTruthy();
    expect(screen.getByText("/auth/forgot: open")).toBeTruthy();
  });
});

/**
 * Mounts the guard signed out at `pathname` and reports whether it rendered
 * the page (true) or sent the visitor to sign in (false).
 */
function opensSignedOut(pathname: string, publicRoutes: PublicRoutes): boolean {
  const navigate = renderAt(pathname, [], publicRoutes);
  const rendered = screen.queryByText("page") !== null;
  // Rendering and redirecting are one decision, never both or neither.
  expect(navigate.mock.calls.length > 0).toBe(!rendered);
  cleanup();
  return rendered;
}

/** What the guard tells links (useCanVisit) about each of `paths`, signed out. */
function linksSignedOut(from: string, paths: string[], publicRoutes: PublicRoutes) {
  renderAt(from, paths, publicRoutes);
  const seen = Object.fromEntries(
    paths.map((p) => [p, screen.getByText(new RegExp(`^${p}: `)).textContent?.endsWith("open")]),
  );
  cleanup();
  return seen;
}

describe("RouteGuard public-route policy", () => {
  // The lists the scaffold ships are the edit point; these pass their own,
  // so the matching rules are pinned whatever the app lists.

  it("opens an exact page, not what is under it, with or without a trailing slash", () => {
    const routes = { exact: ["/pricing"], prefixes: [] };
    expect(opensSignedOut("/pricing", routes)).toBe(true);
    expect(opensSignedOut("/pricing/", routes)).toBe(true);
    expect(opensSignedOut("/pricing/admin", routes)).toBe(false);
    expect(opensSignedOut("/pricing/admin/", routes)).toBe(false);

    expect(
      linksSignedOut(
        "/pricing",
        ["/pricing", "/pricing/", "/pricing/admin", "/pricing/admin/"],
        routes,
      ),
    ).toEqual({
      "/pricing": true,
      "/pricing/": true,
      "/pricing/admin": false,
      "/pricing/admin/": false,
    });
  });

  it("normalizes a listed path's own trailing slash", () => {
    const routes = { exact: ["/pricing/"], prefixes: ["/docs/"] };
    expect(opensSignedOut("/pricing", routes)).toBe(true);
    expect(opensSignedOut("/docs/intro", routes)).toBe(true);
    expect(opensSignedOut("/pricing/admin", routes)).toBe(false);
  });

  for (const list of ["exact", "prefixes"] as const) {
    it(`opens only the home page when "/" is in ${list}, never every route`, () => {
      const routes = { exact: [], prefixes: [], [list]: ["/"] } as PublicRoutes;
      expect(opensSignedOut("/", routes)).toBe(true);
      expect(opensSignedOut("/orders", routes)).toBe(false);
      expect(opensSignedOut("/orders/", routes)).toBe(false);
      expect(opensSignedOut("/orders/view", routes)).toBe(false);

      expect(linksSignedOut("/", ["/", "/orders", "/orders/", "/orders/view"], routes)).toEqual({
        "/": true,
        "/orders": false,
        "/orders/": false,
        "/orders/view": false,
      });
    });
  }
});

describe("useCanVisit without a guard", () => {
  it("allows every route: nothing is gated", () => {
    render(<Links paths={["/", "/orders"]} />);
    expect(screen.getByText("/: open")).toBeTruthy();
    expect(screen.getByText("/orders: open")).toBeTruthy();
  });
});
