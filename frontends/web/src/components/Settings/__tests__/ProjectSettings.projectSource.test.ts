import { describe, expect, it } from "vitest";

import {
  describeProjectSource,
  describeRemote,
  projectMatchesQuery,
  sortProjectsForSettings,
} from "@/components/Settings/projects/projectSource";

describe("describeRemote", () => {
  it("parses scp-style GitHub remotes into a browsable link", () => {
    expect(describeRemote("git@github.com:acme/app.git")).toEqual({
      host: "github.com",
      repo: "acme/app",
      href: "https://github.com/acme/app",
    });
  });

  it("parses https remotes", () => {
    expect(describeRemote("https://gitlab.com/group/sub/app.git")).toEqual({
      host: "gitlab.com",
      repo: "group/sub/app",
      href: "https://gitlab.com/group/sub/app",
    });
  });

  it("does not guess a web URL for an unknown ssh host", () => {
    const remote = describeRemote("ssh://git@git.internal:2222/team/app.git");
    expect(remote).toMatchObject({ host: "git.internal", repo: "team/app" });
    expect(remote.href).toBeUndefined();
  });

  it("falls back to the raw string for something it cannot parse", () => {
    expect(describeRemote("/srv/git/app.git")).toEqual({ host: "", repo: "/srv/git/app.git" });
  });
});

describe("describeProjectSource", () => {
  it("names a plain folder as such", () => {
    expect(describeProjectSource({ is_git_repo: false })).toMatchObject({ primary: "Folder", mono: false });
  });

  it("shows owner/repo with host and default branch for a remote-backed repo", () => {
    expect(
      describeProjectSource({ is_git_repo: true, remote_url: "git@github.com:acme/app.git", default_branch: "main" }),
    ).toEqual({ primary: "acme/app", secondary: "github.com · main", mono: true });
  });

  it("calls a repo without a remote a local repository", () => {
    expect(describeProjectSource({ is_git_repo: true, default_branch: "trunk" })).toEqual({
      primary: "Local repository",
      secondary: "trunk",
      mono: false,
    });
  });
});

describe("sortProjectsForSettings", () => {
  const projects = [
    { id: "old", last_active: "2024-01-01T00:00:00Z" },
    { id: "new", last_active: "2025-01-01T00:00:00Z" },
    { id: "current", last_active: "2023-01-01T00:00:00Z" },
  ];

  it("puts the current project first, then most recently active", () => {
    expect(sortProjectsForSettings(projects, "current").map((p) => p.id)).toEqual(["current", "new", "old"]);
  });

  it("does not mutate its input", () => {
    sortProjectsForSettings(projects, "current");
    expect(projects.map((p) => p.id)).toEqual(["old", "new", "current"]);
  });
});

describe("projectMatchesQuery", () => {
  const project = { name: "Billing", path: "/Users/me/src/billing-svc", remote_url: "git@github.com:acme/pay.git" };

  it("matches name, path and remote case-insensitively", () => {
    expect(projectMatchesQuery(project, "BILL")).toBe(true);
    expect(projectMatchesQuery(project, "svc")).toBe(true);
    expect(projectMatchesQuery(project, "acme/pay")).toBe(true);
    expect(projectMatchesQuery(project, "nope")).toBe(false);
  });

  it("treats a blank query as matching everything", () => {
    expect(projectMatchesQuery(project, "   ")).toBe(true);
  });
});
