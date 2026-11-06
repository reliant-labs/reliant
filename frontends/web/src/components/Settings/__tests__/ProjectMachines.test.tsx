import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { ProjectMachines } from "../projects/ProjectMachines";
import type { Project } from "@/hooks/project-queries";
const rpc = vi.hoisted(() => ({ pull: vi.fn(), detach: vi.fn(), remove: vi.fn(), reclone: vi.fn() }));
vi.mock("@/hooks/useDaemonStatus", () => ({ useDaemonStatus: () => ({ daemons: [] }) }));
vi.mock("@/hooks/project-queries", () => ({ createProjectFromRepo: vi.fn() }));
vi.mock("@/components/Projects/AttachProjectFolderDialog", () => ({ AttachProjectFolderDialog: () => <div>Attach a folder</div> }));
vi.mock("@/hooks/project-service-hooks_gen", () => ({
  useListProjectDaemons: () => ({ data: { projectDaemons: [{ projectId: "p", daemonId: "a", daemonName: "Laptop", path: "/code", installState: 0, verifiedAt: "2026-11-06T12:00:00Z" }, { projectId: "p", daemonId: "b", daemonName: "Server", path: "/repo", installState: 0 }] } }),
  useDetachProjectFromDaemon: () => ({ mutateAsync: rpc.detach }), usePullProjectOnDaemon: () => ({ mutateAsync: rpc.pull }),
  useRemoveProjectFromDaemon: () => ({ mutateAsync: rpc.remove }), useRecloneProjectOnDaemon: () => ({ mutateAsync: rpc.reclone }),
}));
const project = { id: "p", name: "API", remote_url: "https://example.com/api.git" } as Project;
beforeEach(() => { Object.values(rpc).forEach((fn) => fn.mockReset()); });
it("shows machine paths and keeps detach separate from deleting files", async () => {
  render(<ProjectMachines project={project} />);
  expect(screen.getByText("/code")).toBeInTheDocument();
  fireEvent.click(screen.getAllByText("Detach (keep files)")[0]);
  await waitFor(() => expect(rpc.detach).toHaveBeenCalledWith({ projectId: "p", daemonId: "a" }));
  expect(rpc.remove).not.toHaveBeenCalled();
});
it("requires explicit destructive confirmation before deleting a machine's folder", async () => {
  render(<ProjectMachines project={project} />);
  fireEvent.click(screen.getAllByText("Remove from machine (delete files)…")[0]);
  expect(rpc.remove).not.toHaveBeenCalled();
  expect(screen.getByText(/including local changes/)).toHaveTextContent("/code on Laptop");
  fireEvent.click(screen.getByText("Delete folder"));
  await waitFor(() => expect(rpc.remove).toHaveBeenCalledWith({ projectId: "p", daemonId: "a" }));
});
it("shows server refusals instead of silently dropping a machine", async () => {
  rpc.detach.mockRejectedValue(new Error("This machine has branch workspaces; remove them first."));
  render(<ProjectMachines project={project} />);
  fireEvent.click(screen.getAllByText("Detach (keep files)")[0]);
  await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("branch workspaces"));
  expect(screen.getByText("/code")).toBeInTheDocument();
});
it("provides both folder attachment and cloning for remote-backed projects", () => {
  render(<ProjectMachines project={project} />);
  expect(screen.getByText("Clone onto a machine…")).toBeInTheDocument();
  fireEvent.click(screen.getByText("Add machine · use a folder"));
  expect(screen.getByText("Attach a folder")).toBeInTheDocument();
});
