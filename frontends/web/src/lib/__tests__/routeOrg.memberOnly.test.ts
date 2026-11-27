// Copyright (c) 2025 Reliant Labs

/**
 * memberOnly (lib/routeOrg.ts): a route's `beforeLoad` runs outside the org
 * tree and in a member org of the current account, and nowhere else; what it
 * returns or throws reaches the router unchanged. And its typing leaves the
 * route's own params as the path declares them.
 */

import { createRootRoute, createRoute, redirect, type ParsedLocation, type ResolveParams } from "@tanstack/react-router";
import { afterEach, describe, expect, expectTypeOf, it, vi } from "vitest";

import { beginSessionTransition, resetSessionOwnerForTests } from "../auth/sessionOwner";
import { currentRouteOrgEpoch, isMemberOnly, memberOnly, type OrgRouteState } from "../routeOrg";

afterEach(() => resetSessionOwnerForTests());

const location = { pathname: "/o/acme/workflows", searchStr: "?project=p-1", hash: "", search: { project: "p-1" } } as unknown as ParsedLocation;
const MEMBER: OrgRouteState = { kind: "member", org: { id: "org-a", slug: "acme" } };
/** Whether A and B are assignable both ways: the same params. */
type Same<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false;
const inOrg = (orgRoute: OrgRouteState, orgRouteEpoch = currentRouteOrgEpoch()) => ({ orgRoute, orgRouteEpoch });

describe("memberOnly", () => {
  it.each([
    ["outside the org tree (no orgRoute)", () => ({})],
    ["with no context at all", () => undefined],
    ["in a member org", () => inOrg(MEMBER)],
  ])("runs %s, with the router's own options, and hands on what it returns", (_where, context) => {
    const handed = { extra: "context" };
    const beforeLoad = vi.fn(() => handed);
    const options = { context: context(), location, params: { view: "logs" } };
    expect(memberOnly<{ view: string }>(beforeLoad)(options)).toBe(handed);
    expect(beforeLoad).toHaveBeenCalledTimes(1);
    expect(beforeLoad.mock.calls[0]).toEqual([options]);
    expect((beforeLoad.mock.calls[0] as unknown[])[0]).toBe(options);
  });

  it.each([
    ["not a member", () => inOrg({ kind: "not-member", orgs: [] })],
    ["signed out", () => inOrg({ kind: "signed-out" })],
    ["the membership list failed", () => inOrg({ kind: "error", error: new Error("down") })],
    [
      "a member org resolved for an earlier account",
      () => {
        const context = inOrg(MEMBER);
        beginSessionTransition();
        return context;
      },
    ],
  ])("skips, returning no context, when %s", (_why, context) => {
    const beforeLoad = vi.fn(() => {
      throw redirect({ href: "/elsewhere", replace: true });
    });
    expect(memberOnly(beforeLoad)({ context: context(), location })).toBeUndefined();
    expect(beforeLoad).not.toHaveBeenCalled();
  });

  it("lets a redirect through in a member org", () => {
    const wrapped = memberOnly(({ location }) => {
      throw redirect({ href: `/o/acme/workflows/library${location.searchStr}`, replace: true });
    });
    let thrown: unknown;
    try {
      wrapped({ context: inOrg(MEMBER), location });
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toMatchObject({ options: { href: "/o/acme/workflows/library?project=p-1", replace: true } });
  });

  it("marks what it made, and only that", () => {
    const raw = () => undefined;
    expect(isMemberOnly(memberOnly(raw))).toBe(true);
    expect(isMemberOnly(raw)).toBe(false);
    expect(isMemberOnly(undefined)).toBe(false);
  });

  it("leaves a route's params as its path declares them", () => {
    const root = createRootRoute();
    const named = createRoute({
      getParentRoute: () => root,
      path: "/d/$kind/$key",
      beforeLoad: memberOnly<{ kind: string; key: string }>(({ params }) => void params.kind),
    });
    const locationOnly = createRoute({
      getParentRoute: () => root,
      path: "/t/$tab",
      beforeLoad: memberOnly(({ location }) => void location.pathname),
    });
    // `types` exists only in the type: the check is the compiler's (tsconfig.test.json).
    // A subset (`memberOnly<{ kind: string }>` here) would drop `key` from the route.
    expectTypeOf<Same<(typeof named)["types"]["params"], ResolveParams<"/d/$kind/$key">>>().toEqualTypeOf<true>();
    expectTypeOf<Same<(typeof locationOnly)["types"]["params"], ResolveParams<"/t/$tab">>>().toEqualTypeOf<true>();
    expect(isMemberOnly(locationOnly.options.beforeLoad)).toBe(true);
    expect(isMemberOnly(named.options.beforeLoad)).toBe(true);
  });
});
