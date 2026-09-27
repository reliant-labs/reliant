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

  // Close on any forge page leaves forge entirely — including an environment's
  // page, whose close must not turn into a step back to the Overview (that is
  // what the sidebar and the page's own back link are for). Pinned explicitly:
  // the fallback gives the same answer today, and must keep giving it.
  it("returns / as parent of every forge page", () => {
    expect(getParentRouteNavigateOptions("/forge/env/prod")).toEqual({
      to: "/",
      search: {},
    });
    expect(getParentRouteNavigateOptions("/forge/env/dev")).toEqual({
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
