// @vitest-environment node
import { describe, expect, it } from "vitest";

import {
  ProjectMachineMoveCheck,
  type PreviewProjectMachineMoveResponse,
  type ProjectMachineMoveRepo,
} from "@/gen/reliant/v1/project_pb";
import { stayBehind } from "../machineMove";

const repo = (over: Partial<ProjectMachineMoveRepo> = {}) =>
  ({
    repoId: "root",
    name: "api",
    relativePath: "",
    clonable: true,
    changedFileCount: 0,
    changedFiles: [],
    unpushedBranches: [],
    stashCount: 0,
    otherWorktrees: [],
    checkError: "",
    ...over,
  }) as ProjectMachineMoveRepo;

const preview = (over: Partial<PreviewProjectMachineMoveResponse>) =>
  ({
    daemonId: "d",
    daemonName: "",
    path: "/code/api",
    check: ProjectMachineMoveCheck.CHECKED,
    checkError: "",
    repos: [repo()],
    workspaces: [],
    ...over,
  }) as PreviewProjectMachineMoveResponse;

describe("stayBehind", () => {
  it("names the machine from the caller when the server gave no name", () => {
    expect(stayBehind(preview({}), "laptop").summary).toBe(
      "Nothing is left behind on laptop: every repository is committed and pushed.",
    );
  });

  it("words each kind of left-behind work, per repository", () => {
    const { items } = stayBehind(
      preview({
        daemonName: "laptop",
        repos: [
          repo({ name: "forge", relativePath: "forge", changedFileCount: 1, changedFiles: ["go.mod"] }),
          repo({
            name: "api",
            unpushedBranches: [{ name: "wip", unpushedCommits: 0 } as never, { name: "fix", unpushedCommits: 1 } as never],
            stashCount: 3,
            checkError: "git: not a repository.",
          }),
        ],
      }),
      "x",
    );
    expect(items.map((i) => i.text)).toEqual([
      "forge/: 1 uncommitted file (go.mod)",
      "api: couldn't be checked (git: not a repository)",
      "api: branch wip is not pushed",
      "api: branch fix has 1 commit not pushed",
      "api: 3 stashes",
    ]);
  });

  it("skips change counts of a repository with no remote: the whole repository stays", () => {
    const { items } = stayBehind(
      preview({ repos: [repo({ name: "notes", relativePath: "notes", clonable: false, changedFileCount: 4 })] }),
      "laptop",
    );
    expect(items.map((i) => i.text)).toEqual(["notes/: no remote, so it isn't cloned"]);
  });

  it("renders nothing for a project not on that machine, or an unset check", () => {
    expect(stayBehind(preview({ check: ProjectMachineMoveCheck.NOT_ON_MACHINE }), "laptop")).toEqual({ summary: null, items: [] });
    expect(stayBehind(preview({ check: ProjectMachineMoveCheck.UNSPECIFIED }), "laptop").summary).toBeNull();
  });
});
