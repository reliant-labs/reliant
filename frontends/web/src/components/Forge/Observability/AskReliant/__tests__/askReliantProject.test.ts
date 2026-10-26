// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { Project } from "@/store/projectStore";

import { resolveAskProject } from "../askReliantProject";

const project = (id: string, forge?: string): Project => ({
  id,
  name: `Project ${id}`,
  path: `/work/${id}`,
  is_git_repo: true,
  last_active: "",
  created_at: "",
  updated_at: "",
  is_forge: !!forge,
  forge_project_name: forge,
});

describe("resolveAskProject", () => {
  it("is the one project that declares the forge project", () => {
    const shop = project("a", "shop");
    expect(resolveAskProject({ forgeProject: "shop", projects: [project("b", "blog"), shop], currentProject: project("b", "blog") })).toEqual({ kind: "project", project: shop });
  });

  it("prefers the current project when several checkouts declare it", () => {
    const [one, two] = [project("a", "shop"), project("b", "shop")];
    expect(resolveAskProject({ forgeProject: "shop", projects: [one, two], currentProject: two })).toEqual({ kind: "project", project: two });
  });

  it("asks when several declare it and none is current, preselecting the one the console resolved", () => {
    const [one, two] = [project("a", "shop"), project("b", "shop")];
    expect(resolveAskProject({ forgeProject: "shop", consoleProjectId: "b", projects: [one, two, project("c", "blog")], currentProject: project("c", "blog") })).toEqual({
      kind: "choose",
      reason: "ambiguous",
      candidates: [one, two],
      preselected: "b",
    });
  });

  it("asks, with every project and the current one preselected, when none declares it", () => {
    const [one, two] = [project("a", "blog"), project("b")];
    expect(resolveAskProject({ forgeProject: "shop", projects: [one, two], currentProject: one })).toEqual({ kind: "choose", reason: "unmapped", candidates: [one, two], preselected: "a" });
  });

  it("asks rather than falling to the current project when the console named nothing", () => {
    const one = project("a", "blog");
    expect(resolveAskProject({ projects: [one], currentProject: one }).kind).toBe("choose");
  });

  it("trusts a project the console resolved even when its persisted name is not in the list", () => {
    const resolved = project("a");
    expect(resolveAskProject({ forgeProject: "shop", consoleProjectId: "a", projects: [resolved], currentProject: null })).toEqual({ kind: "project", project: resolved });
  });
});
