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

  it("returns /settings as parent of /settings/$section", () => {
    expect(getParentRouteNavigateOptions("/settings/account")).toEqual({
      to: "/settings",
    });
  });

  it("returns / as parent of /settings", () => {
    expect(getParentRouteNavigateOptions("/settings")).toEqual({
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
