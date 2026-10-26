// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows area's paths (lib/workflowsArea.ts): two tabs, every
 * automation link landing on its trigger's row on its workflow's page, and
 * the retired Automations paths mapped to their new homes.
 */

import { describe, expect, it } from "vitest";

import {
  ON_WORKFLOWS_HREF,
  WORKFLOWS_TABS,
  automationHref,
  automationRedirectPath,
  legacyWorkflowsPath,
  workflowPageHref,
  workflowsTabForPath,
} from "../workflowsArea";

describe("workflowsArea", () => {
  it("has two tabs: Workflows and Runs", () => {
    expect(WORKFLOWS_TABS.map((tab) => tab.label)).toEqual(["Workflows", "Runs"]);
    expect(workflowsTabForPath("/workflows/library/triage")).toBe("workflows");
    expect(workflowsTabForPath("/workflows/runs/chat-1")).toBe("runs");
    // The retired redirect belongs to no tab.
    expect(workflowsTabForPath("/workflows/automations/t1")).toBeNull();
  });

  it("opens every automation — declared or hand-made — on its trigger's row of its workflow's page", () => {
    expect(automationHref({ id: "t1", workflow: "builtin://agent", projectId: "p1" })).toBe(
      "/workflows/library/builtin%3A%2F%2Fagent?project=p1#trigger-t1",
    );
    // Knowing only the id, the link resolves through the redirect that looks it up.
    expect(automationHref({ id: "t1" })).toBe("/workflows/automations/t1");
    expect(automationRedirectPath("a b")).toBe("/workflows/automations/a%20b");
  });

  it("scrolls a workflow page to a project's triggers or to one trigger", () => {
    expect(workflowPageHref("triage", "p1", { automation: true })).toBe("/workflows/library/triage?project=p1#automation-p1");
    expect(workflowPageHref("triage", "p1", { triggerId: "t9" })).toBe("/workflows/library/triage?project=p1#trigger-t9");
    expect(workflowPageHref("triage")).toBe("/workflows/library/triage");
  });

  it("maps the retired paths", () => {
    expect(legacyWorkflowsPath("/workflow")).toBe("/workflows/library");
    expect(legacyWorkflowsPath("/runs/c1")).toBe("/workflows/runs/c1");
    // The Automations tab was the Workflows list narrowed to what is on, everywhere.
    expect(legacyWorkflowsPath("/automations")).toBe(ON_WORKFLOWS_HREF);
    expect(legacyWorkflowsPath("/workflows/automations")).toBe(ON_WORKFLOWS_HREF);
    expect(ON_WORKFLOWS_HREF).toBe("/workflows/library?show=on&allProjects=true");
    expect(legacyWorkflowsPath("/automations/t1")).toBe("/workflows/automations/t1");
    // The resolving redirect itself is a route, not a mapping.
    expect(legacyWorkflowsPath("/workflows/automations/t1")).toBeNull();
    expect(legacyWorkflowsPath("/workflows/library")).toBeNull();
  });
});
