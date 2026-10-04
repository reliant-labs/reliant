import { describe, it, expect } from "vitest";
import { getParentRouteNavigateOptions } from "../routeParent";

describe("getParentRouteNavigateOptions", () => {
  // The builder's parent is the Library — the hub it used to step back to is
  // gone (WORKFLOW_UI.md §1.3).
  it("returns the Library as parent of /workflow/$workflowName", () => {
    expect(getParentRouteNavigateOptions("/workflow/my-flow")).toEqual({
      to: "/workflows/library",
    });
  });

  it("handles URL-encoded workflow names (e.g. builtin://...)", () => {
    expect(
      getParentRouteNavigateOptions("/workflow/builtin%3A%2F%2Fchat"),
    ).toEqual({ to: "/workflows/library" });
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

  // The Workflows area (WORKFLOW_UI.md §1.3): a detail page steps back to its
  // tab's list; a tab's list exits to the app. A workflow's detail page is a
  // step into the Library, and the builder opened from it returns there too.
  it("a workflow's detail page steps back to the Library", () => {
    expect(getParentRouteNavigateOptions("/workflows/library/builtin%3A%2F%2Fagent")).toEqual({
      to: "/workflows/library",
    });
  });

  it("an automation's page steps back to the Automations tab", () => {
    expect(getParentRouteNavigateOptions("/workflows/automations/trig-1")).toEqual({
      to: "/workflows/automations",
    });
  });

  it("a run's page steps back to the Runs tab", () => {
    expect(getParentRouteNavigateOptions("/workflows/runs/chat-1")).toEqual({ to: "/workflows/runs" });
  });

  it("every tab's list exits to the app", () => {
    for (const path of ["/workflows", "/workflows/library", "/workflows/runs", "/workflows/automations"]) {
      expect(getParentRouteNavigateOptions(path), path).toEqual({ to: "/", search: {} });
    }
  });

  it("the Inbox is a single page; its exit returns to the app", () => {
    expect(getParentRouteNavigateOptions("/inbox")).toEqual({ to: "/", search: {} });
  });

  it("returns / for unknown routes", () => {
    expect(getParentRouteNavigateOptions("/")).toEqual({ to: "/", search: {} });
    expect(getParentRouteNavigateOptions("/anything/else")).toEqual({
      to: "/",
      search: {},
    });
  });
});
