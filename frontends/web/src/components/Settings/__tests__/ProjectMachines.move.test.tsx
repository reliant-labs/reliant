/**
 * Moving a project from Settings: the clone dialog says what stays on the
 * machine it is on now, the rows show each nested repository's clone, and
 * once the new checkout is installed the old machine is offered for detach,
 * never detached for the user.
 */
import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";

import { ProjectInstallState, ProjectMachineMoveCheck } from "@/gen/reliant/v1/project_pb";
import type { Project } from "@/hooks/project-queries";

type Row = {
  projectId: string;
  daemonId: string;
  daemonName: string;
  path: string;
  installState: ProjectInstallState;
  installError?: string;
  nestedRepos?: Array<{ name: string; relativePath: string; installState: ProjectInstallState; installError: string }>;
};
const state = vi.hoisted(() => ({ rows: [] as Row[] }));
const rpc = vi.hoisted(() => ({ detach: vi.fn(async () => ({})), clone: vi.fn(async () => ({})) }));

// DaemonStatus ACTIVE = 1.
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    daemons: [
      { daemonId: "laptop", hostname: "laptop", status: 1 },
      { daemonId: "cicd", hostname: "cicd", status: 1 },
    ],
  }),
}));
vi.mock("@/hooks/project-queries", () => ({ createProjectFromRepo: rpc.clone }));
vi.mock("@/components/Projects/AttachProjectFolderDialog", () => ({ AttachProjectFolderDialog: () => null }));
vi.mock("@/components/Projects/MachineSelect", () => ({ MachineSelect: () => null }));
vi.mock("@/hooks/project-service-hooks_gen", () => ({
  useListProjectDaemons: () => ({ data: { projectDaemons: state.rows } }),
  useDetachProjectFromDaemon: () => ({ mutateAsync: rpc.detach }),
  usePullProjectOnDaemon: () => ({ mutateAsync: vi.fn() }),
  usePreviewProjectMachineMove: (req: { daemonId: string }) => ({
    isLoading: false,
    error: null,
    data: {
      daemonId: req.daemonId,
      daemonName: "Laptop",
      path: "/code/api",
      check: ProjectMachineMoveCheck.CHECKED,
      checkError: "",
      repos: [{ repoId: "r", name: "api", relativePath: "", clonable: true, changedFileCount: 2, changedFiles: ["a.go", "b.go"], unpushedBranches: [], stashCount: 0, otherWorktrees: [], checkError: "" }],
      workspaces: [],
    },
  }),
}));

import { ProjectMachines } from "../projects/ProjectMachines";

const project = { id: "p", name: "API", remote_url: "https://example.com/api.git" } as Project;
const laptopRow: Row = { projectId: "p", daemonId: "laptop", daemonName: "Laptop", path: "/code/api", installState: ProjectInstallState.INSTALLED };

beforeEach(() => {
  rpc.detach.mockClear();
  rpc.clone.mockClear();
  state.rows = [laptopRow];
});

it("lists what stays on the old machine, then offers its detach only once the clone is installed", async () => {
  const view = render(<ProjectMachines project={project} />);
  fireEvent.click(screen.getByText("Clone onto a machine…"));
  expect(screen.getByTestId("stays-on-machine")).toHaveTextContent("These stay on Laptop:");
  expect(screen.getByTestId("stays-on-machine")).toHaveTextContent("api: 2 uncommitted files (a.go, b.go)");

  await act(async () => {
    fireEvent.click(screen.getByText("Queue clone"));
  });
  expect(rpc.clone).toHaveBeenCalledWith({ cloneUrl: project.remote_url, daemonId: "cicd", name: "API" });

  // Landing: each nested repository's progress, and no offer yet.
  state.rows = [
    laptopRow,
    {
      projectId: "p",
      daemonId: "cicd",
      daemonName: "cicd",
      path: "/home/workspace/api",
      installState: ProjectInstallState.INSTALLING,
      nestedRepos: [{ name: "forge", relativePath: "forge", installState: ProjectInstallState.INSTALLING, installError: "" }],
    },
  ];
  view.rerender(<ProjectMachines project={project} />);
  expect(screen.getByTestId("nested-repo-status")).toHaveTextContent("Cloning forge/…");
  expect(screen.queryByTestId("detach-old-machine-offer")).toBeNull();

  state.rows = [laptopRow, { ...state.rows[1], installState: ProjectInstallState.INSTALLED, nestedRepos: [] }];
  view.rerender(<ProjectMachines project={project} />);
  const offer = screen.getByRole("button", { name: "Detach from Laptop (keep files)" });
  expect(rpc.detach).not.toHaveBeenCalled();
  await act(async () => {
    fireEvent.click(offer);
  });
  expect(rpc.detach).toHaveBeenCalledWith({ projectId: "p", daemonId: "laptop" });
});

it("shows a failed nested clone with the repository and git's words", () => {
  state.rows = [
    laptopRow,
    {
      projectId: "p",
      daemonId: "cicd",
      daemonName: "cicd",
      path: "/home/workspace/api",
      installState: ProjectInstallState.FAILED,
      installError: "the project cloned, but its nested repository forge (forge/): git clone failed: exit status 128",
      nestedRepos: [{ name: "forge", relativePath: "forge", installState: ProjectInstallState.FAILED, installError: "git clone failed: exit status 128: Permission denied (publickey)." }],
    },
  ];
  render(<ProjectMachines project={project} />);
  expect(screen.getByText(/the project cloned, but its nested repository forge/)).toBeInTheDocument();
  expect(screen.getByTestId("nested-repo-status")).toHaveTextContent(
    "forge/: git clone failed: exit status 128: Permission denied (publickey).",
  );
});
