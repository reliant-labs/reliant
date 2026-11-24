/**
 * "These stay on <machine>": what a set-up on another machine leaves where
 * the project is now. Each state the server's preview can be in, and the
 * older server that has no preview at all.
 */
import { render, screen } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  ProjectMachineMoveCheck,
  type PreviewProjectMachineMoveResponse,
  type ProjectMachineMoveRepo,
} from "@/gen/reliant/v1/project_pb";

const preview = vi.hoisted(() => ({
  current: { isLoading: false, error: null as unknown, data: undefined as unknown },
}));
vi.mock("@/hooks/project-service-hooks_gen", () => ({
  usePreviewProjectMachineMove: () => preview.current,
}));

import { StaysOnMachineList } from "../StaysOnMachineList";

function repo(over: Partial<ProjectMachineMoveRepo> = {}): ProjectMachineMoveRepo {
  return {
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
  } as ProjectMachineMoveRepo;
}

function answer(over: Partial<PreviewProjectMachineMoveResponse>): void {
  preview.current = {
    isLoading: false,
    error: null,
    data: {
      daemonId: "d-laptop",
      daemonName: "laptop",
      path: "/code/api",
      check: ProjectMachineMoveCheck.CHECKED,
      checkError: "",
      repos: [repo()],
      workspaces: [],
      ...over,
    },
  };
}

const renderList = () => render(<StaysOnMachineList projectId="p" daemonId="d-laptop" machineName="laptop" />);

beforeEach(() => answer({}));

describe("StaysOnMachineList", () => {
  it("lists uncommitted files, unpushed branches, stashes, worktrees, repos with no remote and workspaces", () => {
    answer({
      repos: [
        repo({
          changedFileCount: 5,
          changedFiles: ["a.go", "b.go", "c.go", "d.go", "e.go"],
          unpushedBranches: [{ name: "feature", unpushedCommits: 2 } as never],
          stashCount: 1,
          otherWorktrees: [{ path: "/code/api-hotfix", branch: "hotfix" } as never],
        }),
        repo({ repoId: "forge", name: "forge", relativePath: "forge", clonable: false }),
      ],
      workspaces: [{ worktreeId: "w1", name: "fix-login", branch: "fix/login" } as never],
    });
    renderList();
    const list = screen.getByTestId("stays-on-machine");
    expect(list).toHaveTextContent("The clone takes what's pushed. These stay on laptop:");
    expect(list).toHaveTextContent("api: 5 uncommitted files (a.go, b.go, c.go and 2 more)");
    expect(list).toHaveTextContent("api: branch feature has 2 commits not pushed");
    expect(list).toHaveTextContent("api: 1 stash");
    expect(list).toHaveTextContent("api: git worktree /code/api-hotfix (hotfix)");
    expect(list).toHaveTextContent("forge/: no remote, so it isn't cloned");
    expect(list).toHaveTextContent("Branch workspace fix-login (fix/login) stays here with its chats");
  });

  it("says nothing is left behind when every repository is committed and pushed", () => {
    renderList();
    expect(screen.getByTestId("stays-on-machine")).toHaveTextContent(
      "Nothing is left behind on laptop: every repository is committed and pushed.",
    );
    expect(screen.queryByRole("list")).toBeNull();
  });

  it("still lists the workspaces when nothing else stays", () => {
    answer({ workspaces: [{ worktreeId: "w1", name: "spike", branch: "spike" } as never] });
    renderList();
    expect(screen.getByTestId("stays-on-machine")).toHaveTextContent("Nothing is left behind on laptop");
    expect(screen.getByRole("list")).toHaveTextContent("Branch workspace spike stays here with its chats");
  });

  it("says an offline machine can't be checked, and still shows what Reliant knows", () => {
    answer({
      check: ProjectMachineMoveCheck.MACHINE_OFFLINE,
      // Change counts mean nothing unchecked: never shown.
      repos: [repo({ changedFileCount: 3 })],
      workspaces: [{ worktreeId: "w1", name: "spike", branch: "spike" } as never],
    });
    renderList();
    const list = screen.getByTestId("stays-on-machine");
    expect(list).toHaveTextContent("laptop is offline, so its uncommitted changes and unpushed branches can't be checked.");
    expect(list).toHaveTextContent("Branch workspace spike");
    expect(list).not.toHaveTextContent("uncommitted file");
  });

  it("asks for an update on a machine too old to check", () => {
    answer({ check: ProjectMachineMoveCheck.MACHINE_OUTDATED });
    renderList();
    expect(screen.getByTestId("stays-on-machine")).toHaveTextContent("Update Reliant on laptop to check what stays behind.");
  });

  it("shows the machine's reason when the check failed", () => {
    answer({ check: ProjectMachineMoveCheck.FAILED, checkError: "git status: exit status 128." });
    renderList();
    expect(screen.getByTestId("stays-on-machine")).toHaveTextContent(
      "Couldn't check what stays on laptop: git status: exit status 128.",
    );
  });

  it("renders nothing when the project isn't on that machine", () => {
    answer({ check: ProjectMachineMoveCheck.NOT_ON_MACHINE });
    const { container } = renderList();
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing on a server without the preview (Unimplemented)", () => {
    preview.current = {
      isLoading: false,
      error: new ConnectError("procedure not implemented", Code.Unimplemented),
      data: undefined,
    };
    const { container } = renderList();
    expect(container).toBeEmptyDOMElement();
  });

  it("says it is checking while the preview loads", () => {
    preview.current = { isLoading: true, error: null, data: undefined };
    renderList();
    expect(screen.getByRole("status")).toHaveTextContent("Checking what stays on laptop…");
  });
});
