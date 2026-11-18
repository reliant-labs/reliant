import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MobilePackagesPanel } from "../MobilePackagesPanel";
import { BackgroundProcessStatus } from "../../../hooks/background-queries";

const usePackageCommands = vi.fn();
const usePackageProcesses = vi.fn();

const useChatBackgroundProcesses = vi.fn(() => ({ running: [], recent: [], isLoading: false }));

vi.mock("../../../hooks/background-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/background-queries")>()),
  useChatBackgroundProcesses: (...args: unknown[]) => useChatBackgroundProcesses(...(args as [])),
}));

vi.mock("../../../hooks/package-queries", () => ({
  usePackageCommands: (...args: unknown[]) => usePackageCommands(...args),
  usePackageProcesses: (...args: unknown[]) => usePackageProcesses(...args),
}));

describe("MobilePackagesPanel", () => {
  it("lists commands grouped by package type", () => {
    usePackageCommands.mockReturnValue({
      isLoading: false,
      data: {
        commands: {
          npm: [
            { name: "dev", command: "npm run dev", package_type: "npm", working_dir: "/" },
          ],
        },
      },
    });
    usePackageProcesses.mockReturnValue({ isLoading: false, data: [] });

    render(<MobilePackagesPanel worktreeId="wt-1" />);

    expect(screen.getByText("dev")).toBeInTheDocument();
    expect(screen.getByText("npm run dev")).toBeInTheDocument();
  });

  it("shows running processes with status", () => {
    usePackageCommands.mockReturnValue({ isLoading: false, data: { commands: {} } });
    usePackageProcesses.mockReturnValue({
      isLoading: false,
      data: [
        {
          id: "p1",
          command: "npm run dev",
          status: BackgroundProcessStatus.RUNNING,
          working_dir: "/",
          start_time: new Date().toISOString(),
        },
      ],
    });

    render(<MobilePackagesPanel worktreeId="wt-1" />);

    expect(screen.getByText("Running")).toBeInTheDocument();
  });

  it("shows an empty state when no commands are detected", () => {
    usePackageCommands.mockReturnValue({ isLoading: false, data: { commands: {} } });
    usePackageProcesses.mockReturnValue({ isLoading: false, data: [] });

    render(<MobilePackagesPanel worktreeId="wt-1" />);

    expect(
      screen.getByText("No package commands detected in this workspace."),
    ).toBeInTheDocument();
  });

  it("does not render run or kill controls", () => {
    usePackageCommands.mockReturnValue({
      isLoading: false,
      data: {
        commands: {
          npm: [
            { name: "dev", command: "npm run dev", package_type: "npm", working_dir: "/" },
          ],
        },
      },
    });
    usePackageProcesses.mockReturnValue({ isLoading: false, data: [] });

    render(<MobilePackagesPanel worktreeId="wt-1" />);

    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  // An agent's background command belongs to no workspace, so the workspace
  // list never shows it: the open chat's get a section of their own.
  it("lists the processes the open chat's agents started", () => {
    usePackageCommands.mockReturnValue({ isLoading: false, data: { commands: {} } });
    usePackageProcesses.mockReturnValue({ isLoading: false, data: [] });
    useChatBackgroundProcesses.mockReturnValueOnce({
      running: [
        { id: "a1", command: "go test ./...", status: BackgroundProcessStatus.RUNNING, start_time: "", working_dir: "/", session_id: "" },
      ],
      recent: [
        { id: "a2", command: "npm run build", status: BackgroundProcessStatus.FAILED, exit_code: 1, start_time: "", working_dir: "/", session_id: "" },
      ],
      isLoading: false,
    } as never);

    render(<MobilePackagesPanel worktreeId="wt-1" />);

    expect(screen.getByText("This chat")).toBeInTheDocument();
    expect(screen.getByText("go test ./...")).toBeInTheDocument();
    expect(screen.getByText("Running")).toBeInTheDocument();
    expect(screen.getByText("Failed (exit 1)")).toBeInTheDocument();
  });
});
