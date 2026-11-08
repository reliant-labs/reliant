import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useAddProject } from "../useAddProject";
import { useCurrentMachineStore } from "@/store/currentMachineStore";
const create = vi.hoisted(() => vi.fn());
vi.mock("@/hooks/project-queries", () => ({ createProjectWithResult: create, fetchProjects: vi.fn() }));
vi.mock("@/hooks/project-service-hooks_gen", () => ({ useListProjectDaemons: () => ({ refetch: vi.fn() }) }));
vi.mock("@/hooks/useDaemonStatus", () => ({ useDaemonStatus: () => ({ daemons: [{ daemonId: "default", isDefault: true }, { daemonId: "chosen" }], loading: false }) }));
vi.mock("@/hooks/useGitHubCredential", () => ({ useGitHubCredential: () => ({ hasToken: false }) }));
vi.mock("@/services/controlPlane/capabilities", () => ({ capabilities: { cloudDaemons: false } }));
const openNewChat = vi.hoisted(() => vi.fn());
vi.mock("@/store/openChat", () => ({ openNewChat }));
vi.mock("@/store/projectStore", () => ({ useProjectStore: { getState: () => ({ setProjectFilter: vi.fn() }) } }));
vi.mock("@/lib/toast-manager", () => ({ toast: { success: vi.fn(), error: vi.fn(), loading: vi.fn(), dismiss: vi.fn() } }));
beforeEach(() => { create.mockReset(); openNewChat.mockReset(); create.mockResolvedValue({ project: { id: "p" }, attached: false }); useCurrentMachineStore.setState({ daemonId: "chosen" }); });
afterEach(() => { vi.unstubAllGlobals(); useCurrentMachineStore.setState({ daemonId: null }); });
it("defaults to the chat's selected machine, not the default, and sends the browsed machine", async () => {
  const client = new QueryClient();
  const { result } = renderHook(() => useAddProject({ onChooseClone: vi.fn(), onChooseFolderInBrowser: vi.fn() }), { wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> });
  expect(result.current.folderDaemonId).toBe("chosen");
  await act(() => result.current.openFolderPath("/code", "other"));
  expect(create).toHaveBeenCalledWith(expect.objectContaining({ path: "/code", daemon_id: "other" }));
});
it("opens the machine-aware browser even in Electron when local identity isn't known", () => {
  const browser = vi.fn(); const native = vi.fn();
  vi.stubGlobal("electronAPI", { selectDirectory: native });
  const { result } = renderHook(() => useAddProject({ onChooseClone: vi.fn(), onChooseFolderInBrowser: browser }), { wrapper: ({ children }) => <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider> });
  result.current.actions.find((a) => a.kind === "open")!.onClick();
  expect(browser).toHaveBeenCalledOnce(); expect(native).not.toHaveBeenCalled();
});
// The folder the user picked on "other" attached to a project already
// installed on the default machine: the new chat must run on "other", where
// the folder is, not wherever the project's default resolution lands.
it("opens the new chat on the machine of the folder, also when it joined an existing project", async () => {
  create.mockResolvedValue({ project: { id: "existing" }, attached: true, projectDaemon: { daemon_id: "other", project_id: "existing", path: "/code" } });
  const { result } = renderHook(() => useAddProject({ onChooseClone: vi.fn(), onChooseFolderInBrowser: vi.fn() }), { wrapper: ({ children }) => <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider> });
  await act(() => result.current.openFolderPath("/code", "other"));
  expect(openNewChat).toHaveBeenCalledWith({ projectId: "existing", daemonId: "other" });
});
