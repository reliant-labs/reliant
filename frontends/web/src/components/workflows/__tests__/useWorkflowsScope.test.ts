// Copyright (c) 2025 Reliant Labs

/** The area's one project scope, read the same way on every page. */

import { describe, expect, it } from "vitest";

import { workflowsScope } from "../useWorkflowsScope";

describe("workflowsScope", () => {
  it("is the current project unless the URL widens it", () => {
    expect(workflowsScope(undefined, "p1")).toEqual({ currentProjectId: "p1", allProjects: false, projectId: "p1" });
    expect(workflowsScope(true, "p1")).toEqual({ currentProjectId: "p1", allProjects: true, projectId: undefined });
  });

  it("is every project when there is no current project, whatever the URL says", () => {
    expect(workflowsScope(undefined, undefined)).toEqual({ currentProjectId: undefined, allProjects: true, projectId: undefined });
  });
});
