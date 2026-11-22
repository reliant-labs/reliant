// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { Project } from "@/hooks/project-queries";

import { patchPlan, vulnPatchAccess, vulnPatchInputs, type PatchRepo, type VulnPatchAccessInput } from "../vulnPatchModel";

const PROJECT: Project = {
  id: "proj-1",
  name: "shop-monorepo",
  path: "/home/me/shop",
  is_git_repo: true,
  last_active: "",
  created_at: "",
  updated_at: "",
  is_forge: false,
  remote_url: "https://github.com/acme/shop.git",
};

const REPO: PatchRepo = {
  name: "services",
  relativePath: "services",
  remoteUrl: "git@github.com:acme/services.git",
  forgeApps: [
    { name: "billing", subpath: "billing" },
    { name: "shop", subpath: "apps/shop" },
  ],
};

const input = (patch: Partial<VulnPatchAccessInput> = {}): VulnPatchAccessInput => ({
  forgeProject: "shop",
  projectsListed: true,
  project: PROJECT,
  repos: { data: [REPO], isLoading: false, isError: false },
  github: { connected: true, isLoading: false },
  ...patch,
});

describe("vulnPatchAccess", () => {
  it("is ready in the repository whose forge apps hold the project, at the app's path in the project", () => {
    expect(vulnPatchAccess(input())).toEqual({ state: "ready", projectId: "proj-1", projectPath: "services/apps/shop" });
  });

  it("patches the root when the project itself declares the forge project and no repository was scanned to hold it", () => {
    const access = vulnPatchAccess(input({ project: { ...PROJECT, forge_project_name: "shop" }, repos: { data: [], isLoading: false, isError: false } }));
    expect(access).toEqual({ state: "ready", projectId: "proj-1", projectPath: "" });
  });

  it("checks rather than blocks while a read is out", () => {
    expect(vulnPatchAccess(input({ projectsListed: false, project: null }))).toEqual({ state: "checking" });
    expect(vulnPatchAccess(input({ repos: { data: undefined, isLoading: true, isError: false } }))).toEqual({ state: "checking" });
    expect(vulnPatchAccess(input({ github: { connected: false, isLoading: true } }))).toEqual({ state: "checking" });
  });

  it.each([
    ["no linked repository", input({ project: null }), /No repository is linked to shop/],
    ["read-only access", input({ project: { ...PROJECT, viewer_access: true } }), /shared with you to read: only its owner can start a patch/],
    ["no repository holding the app", input({ repos: { data: [{ ...REPO, forgeApps: [] }], isLoading: false, isError: false } }), /No repository of shop-monorepo holds the forge project shop/],
    ["a repository with no GitHub origin", input({ repos: { data: [{ ...REPO, remoteUrl: "https://gitlab.com/acme/services.git" }], isLoading: false, isError: false } }), /origin is not a GitHub repository/],
    ["an unreadable repository list", input({ repos: { data: undefined, isLoading: false, isError: true } }), /Could not read the repositories of shop-monorepo/],
    ["no GitHub connection", input({ github: { connected: false, isLoading: false } }), /Connect GitHub to patch/],
  ])("is blocked with a stated reason: %s", (_, given, reason) => {
    const access = vulnPatchAccess(given);
    expect(access.state).toBe("blocked");
    expect(access.state === "blocked" && access.reason).toMatch(reason);
  });
});

describe("patchPlan", () => {
  it("sends each fixable advisory once and counts what has no fix", () => {
    const plan = patchPlan("prod", [
      { advisoryId: "CVE-1", fixState: "fixed" },
      { advisoryId: "CVE-1", fixState: "fixed" },
      { advisoryId: "CVE-2", fixState: "not-fixed" },
      { advisoryId: "GHSA-3", fixState: "fixed" },
    ]);
    expect(plan).toEqual({ ok: true, env: "prod", advisories: ["CVE-1", "GHSA-3"], withoutFix: 1 });
  });

  it("says why it cannot patch: every environment, nothing selected, nothing fixable", () => {
    expect(patchPlan(null, [{ advisoryId: "CVE-1", fixState: "fixed" }])).toEqual({ ok: false, reason: expect.stringMatching(/Choose one environment/) });
    expect(patchPlan("prod", [])).toEqual({ ok: false, reason: "Select the findings to patch." });
    expect(patchPlan("prod", [{ advisoryId: "CVE-2", fixState: "wont-fix" }])).toEqual({ ok: false, reason: expect.stringMatching(/None of the selected findings has a fixed version/) });
    expect(patchPlan("prod", [], "advisory")).toEqual({ ok: false, reason: "It is not found in what prod runs." });
  });
});

describe("vulnPatchInputs", () => {
  it("names vuln-patch's inputs exactly", () => {
    expect(vulnPatchInputs("", "prod", ["CVE-1"], "agent")).toEqual({ project: "", env: "prod", advisories: ["CVE-1"], patch_mode: "agent" });
  });
});
