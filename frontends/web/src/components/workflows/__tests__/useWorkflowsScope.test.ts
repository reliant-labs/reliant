// Copyright (c) 2025 Reliant Labs

/** The area's one project scope, read the same way on every page. */

import { describe, expect, it } from "vitest";

import { inWorkflowsScope, workflowsScope } from "../useWorkflowsScope";

describe("workflowsScope", () => {
  it("is the current project unless the URL widens it", () => {
    expect(workflowsScope(undefined, "p1")).toEqual({ currentProjectId: "p1", allProjects: false, projectId: "p1" });
    expect(workflowsScope(true, "p1")).toEqual({ currentProjectId: "p1", allProjects: true, projectId: undefined });
  });

  it("is every project when there is no current project, whatever the URL says", () => {
    expect(workflowsScope(undefined, undefined)).toEqual({ currentProjectId: undefined, allProjects: true, projectId: undefined });
  });

  // Inside the one shell the area shows what the nav beside it shows, never
  // a project (or every project) the nav does not.
  describe("inside the one shell", () => {
    it("is the nav's one project, whatever the current project", () => {
      expect(workflowsScope(undefined, "p-other", { mode: "one", projectId: "p1" })).toEqual({
        currentProjectId: "p1",
        allProjects: false,
        projectId: "p1",
      });
    });

    it("is the nav's several projects, acting in the current one when it is among them", () => {
      expect(workflowsScope(undefined, "p2", { mode: "many", projectIds: ["p1", "p2"] })).toEqual({
        currentProjectId: "p2",
        allProjects: false,
        projectIds: ["p1", "p2"],
      });
      expect(workflowsScope(undefined, "p-other", { mode: "many", projectIds: ["p1", "p2"] }).currentProjectId).toBe("p1");
    });

    it("is every project when the nav shows every project, or the URL widens it", () => {
      expect(workflowsScope(undefined, "p1", { mode: "all" })).toEqual({ currentProjectId: "p1", allProjects: true });
      expect(workflowsScope(true, "p1", { mode: "many", projectIds: ["p1", "p2"] }).allProjects).toBe(true);
    });
  });
});

describe("inWorkflowsScope", () => {
  it("answers for one, several and every project", () => {
    expect(inWorkflowsScope(workflowsScope(undefined, "p1"), "p1")).toBe(true);
    expect(inWorkflowsScope(workflowsScope(undefined, "p1"), "p2")).toBe(false);
    const several = workflowsScope(undefined, "p1", { mode: "many", projectIds: ["p1", "p2"] });
    expect(inWorkflowsScope(several, "p2")).toBe(true);
    expect(inWorkflowsScope(several, "p3")).toBe(false);
    expect(inWorkflowsScope(workflowsScope(true, "p1"), "p3")).toBe(true);
  });
});
