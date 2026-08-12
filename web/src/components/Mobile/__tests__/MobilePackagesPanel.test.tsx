import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MobilePackagesPanel } from "../MobilePackagesPanel";
import { BackgroundProcessStatus } from "../../../api/background-grpc";

const usePackageCommands = vi.fn();
const usePackageProcesses = vi.fn();

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
});
