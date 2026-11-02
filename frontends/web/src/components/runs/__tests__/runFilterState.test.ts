// Copyright (c) 2025 Reliant Labs

/** The Runs filter state ↔ URL mapping (runFilterState.ts). */

import { describe, expect, it } from "vitest";

import {
  applyRunFilter,
  clearRunFilters,
  hasActiveRunFilters,
  RUN_KIND_OPTIONS,
} from "../runFilterState";

describe("runFilterState", () => {
  it("writes defaults as absent, so a default view is a bare URL", () => {
    expect(applyRunFilter({}, { range: "24h" })).toEqual({});
    expect(applyRunFilter({ range: "7d" }, { range: "24h" })).toEqual({});
    expect(applyRunFilter({}, { state: [] })).toEqual({});
    expect(applyRunFilter({}, { q: "" })).toEqual({});
    expect(applyRunFilter({}, { allProjects: false })).toEqual({});
  });

  it("keeps what narrows the list", () => {
    expect(applyRunFilter({ project: "p1" }, { state: ["failed", "live"], range: "30d" })).toEqual({
      project: "p1",
      state: ["failed", "live"],
      range: "30d",
    });
    expect(applyRunFilter({}, { allProjects: true })).toEqual({ allProjects: true });
  });

  it("Started by offers every launch kind, the unattended ones included", () => {
    expect(RUN_KIND_OPTIONS.map((option) => option.value)).toEqual([
      "chat.start",
      "manual.run",
      "schedule",
      "webhook",
      "integration",
      "workflow_event",
      "agent.start_run",
      "cli.run",
      "builder.test",
    ]);
    expect(RUN_KIND_OPTIONS.map((option) => option.label)).toEqual([
      "Chat",
      "Run by hand",
      "Schedule",
      "Webhook",
      "App event",
      "Workflow event",
      "Agent",
      "CLI",
      "Tests",
    ]);
  });

  it("an active filter is anything but the defaults and view settings", () => {
    expect(hasActiveRunFilters({})).toBe(false);
    expect(hasActiveRunFilters({ range: "24h", allProjects: true, group: false, project: "p" })).toBe(false);
    expect(hasActiveRunFilters({ range: "7d" })).toBe(true);
    expect(hasActiveRunFilters({ workflow: ["builtin://agent"] })).toBe(true);
  });

  it("Clear keeps the view settings and drops every filter", () => {
    expect(
      clearRunFilters({
        state: ["failed"],
        kind: ["schedule"],
        range: "7d",
        q: "x",
        workflow: ["w"],
        trigger: "t",
        parent: "c",
        allProjects: true,
        group: false,
        project: "p",
      }),
    ).toEqual({ allProjects: true, group: false, project: "p" });
  });
});
