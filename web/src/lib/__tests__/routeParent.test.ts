import { describe, it, expect } from "vitest";
import { getParentRouteNavigateOptions } from "../routeParent";

describe("getParentRouteNavigateOptions", () => {
  it("returns /workflow as parent of /workflow/$workflowName", () => {
    expect(getParentRouteNavigateOptions("/workflow/my-flow")).toEqual({
      to: "/workflow",
    });
  });

  it("handles URL-encoded workflow names (e.g. builtin://...)", () => {
    expect(
      getParentRouteNavigateOptions("/workflow/builtin%3A%2F%2Fchat"),
    ).toEqual({ to: "/workflow" });
  });

  it("returns / as parent of /workflow", () => {
    expect(getParentRouteNavigateOptions("/workflow")).toEqual({
      to: "/",
      search: {},
    });
  });

  // /settings and /settings/$section render the same SettingsPage, so there is
  // no intermediate view to step back to — closing any section must leave
  // settings entirely rather than bounce through the default (account) tab.
  it("returns / as parent of /settings/$section", () => {
    expect(getParentRouteNavigateOptions("/settings/mcp")).toEqual({
      to: "/",
      search: {},
    });
  });

  it("returns / as parent of the default section, not /settings", () => {
    expect(getParentRouteNavigateOptions("/settings/account")).toEqual({
      to: "/",
      search: {},
    });
  });

  it("returns / as parent of /settings", () => {
    expect(getParentRouteNavigateOptions("/settings")).toEqual({
      to: "/",
      search: {},
    });
  });

  // The three forge paths are peer tabs of one surface, not a hub and its
  // children, so closing any of them leaves forge entirely. Pinned explicitly:
  // the fallback would give the same answer today, and a future /forge hub route
  // must not silently turn "close" into a redirect back into forge.
  it("returns / as parent of every forge tab", () => {
    expect(getParentRouteNavigateOptions("/forge/topology")).toEqual({
      to: "/",
      search: {},
    });
    expect(getParentRouteNavigateOptions("/forge/status")).toEqual({
      to: "/",
      search: {},
    });
    expect(getParentRouteNavigateOptions("/forge/secrets")).toEqual({
      to: "/",
      search: {},
    });
  });

  it("returns / as parent of a bare /forge", () => {
    expect(getParentRouteNavigateOptions("/forge")).toEqual({
      to: "/",
      search: {},
    });
  });

  it("returns / for unknown routes", () => {
    expect(getParentRouteNavigateOptions("/")).toEqual({ to: "/", search: {} });
    expect(getParentRouteNavigateOptions("/anything/else")).toEqual({
      to: "/",
      search: {},
    });
  });
});
