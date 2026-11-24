/**
 * A chat's projects (hooks/useChatProjects): read, attach, detach; a project
 * the server says is not on the chat's machine becomes an offer to set it up
 * there, and is attached once it lands; an older server (Unimplemented)
 * leaves the primary alone, with no toast; the new-chat draft keeps its own.
 */
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import { ProjectCheckoutMissingSchema, ProjectCheckoutState } from "@/gen/reliant/v1/filesystem_pb";
import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

const mocks = vi.hoisted(() => ({
  installs: [] as Array<{ project_id: string; daemon_id: string; install_state: number; path?: string; install_error?: string }>,
  createProjectFromRepo: vi.fn(async () => ({ queued: true })),
  toastError: vi.fn(),
  projects: [] as Array<{ id: string; name: string; path: string; remote_url?: string }>,
}));

vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  createProjectFromRepo: mocks.createProjectFromRepo,
  useProjectDaemonInstalls: () => ({ data: mocks.installs, refetch: async () => ({}) }),
  useProjects: () => ({ projects: mocks.projects, isLoading: false }),
}));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: [{ daemonId: "d-cicd", name: "cicd", platform: "linux" }] }),
}));
vi.mock("sonner", () => ({ toast: { error: mocks.toastError } }));

import { resetChatProjectsSupportForTests, useChatProjects, useDraftChatProjects } from "../useChatProjects";
import { getNewChatAttachedProjects, setNewChatAttachedProjects } from "@/store/newChatProjectsStore";

const chat = { id: "c1", projectId: "p-api", activeDaemonId: "d-cicd", noMachine: false, viewerAccess: false };
const primary = { projectId: "p-api", name: "api", primary: true, daemonId: "d-cicd", path: "/w/api", attachedAt: "" };
const web = { projectId: "p-web", name: "web", primary: false, daemonId: "d-cicd", path: "/w/web", attachedAt: "2026-11-24T00:00:00Z" };
const docs = { id: "p-docs", name: "docs", path: "/w/docs", remote_url: "https://github.com/acme/docs" } as never;

let queryClient: QueryClient;

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  mocks.installs = [];
  mocks.projects = [];
  mocks.createProjectFromRepo.mockClear();
  mocks.toastError.mockClear();
  resetChatProjectsSupportForTests();
  setNewChatAttachedProjects(null, []);
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

describe("useChatProjects", () => {
  it("lists the chat's projects, its own first", async () => {
    setTransport(mockTransport({ "ChatService.ListChatProjects": { response: { projects: [primary, web] } } }));
    const { result } = renderHook(() => useChatProjects(chat), { wrapper });
    await waitFor(() => expect(result.current.supported).toBe(true));
    expect(result.current.projects).toEqual([
      { projectId: "p-api", name: "api", primary: true },
      { projectId: "p-web", name: "web", primary: false },
    ]);
    expect(result.current.machine).toEqual({ daemonId: "d-cicd", name: "cicd" });
  });

  it("shows the primary only, with no toast and no add, on a server older than the rpcs", async () => {
    setTransport(
      mockTransport({ "ChatService.ListChatProjects": { error: new ConnectError("HTTP 404", Code.Unimplemented) } }),
    );
    const { result } = renderHook(() => useChatProjects(chat, "api"), { wrapper });
    await waitFor(() => expect(queryClient.getQueryCache().getAll()[0]?.state.status).toBe("error"));
    expect(result.current.supported).toBe(false);
    expect(result.current.projects).toEqual([{ projectId: "p-api", name: "api", primary: true }]);
    expect(mocks.toastError).not.toHaveBeenCalled();
    // The draft learns it too: a new chat offers no attached projects.
    const draft = renderHook(() => useDraftChatProjects({ primaryId: "p-api", machineDaemonId: "d-cicd" }), { wrapper });
    expect(draft.result.current.supported).toBe(false);
  });

  it("reads nothing for a viewer of a shared chat, or a chat with no machine", () => {
    const list = vi.fn(() => ({ projects: [primary] }));
    setTransport(mockTransport({ "ChatService.ListChatProjects": list }));
    const viewer = renderHook(() => useChatProjects({ ...chat, viewerAccess: true }), { wrapper });
    const noMachine = renderHook(() => useChatProjects({ ...chat, noMachine: true }), { wrapper });
    expect(viewer.result.current.supported).toBe(false);
    expect(noMachine.result.current.supported).toBe(false);
    expect(list).not.toHaveBeenCalled();
  });

  it("attaches a project and detaches it again", async () => {
    const attach = vi.fn((req: unknown) => ({
      projects: [primary, { ...web, projectId: (req as { projectId: string }).projectId, name: "docs" }],
    }));
    const detach = vi.fn(() => ({ projects: [primary] }));
    setTransport(
      mockTransport({
        "ChatService.ListChatProjects": { response: { projects: [primary] } },
        "ChatService.AttachProjectToChat": attach,
        "ChatService.DetachProjectFromChat": detach,
      }),
    );
    const { result } = renderHook(() => useChatProjects(chat), { wrapper });
    await waitFor(() => expect(result.current.supported).toBe(true));

    act(() => result.current.add(docs));
    await waitFor(() => expect(result.current.projects.map((p) => p.projectId)).toEqual(["p-api", "p-docs"]));
    expect(attach).toHaveBeenCalledWith(expect.objectContaining({ chatId: "c1", projectId: "p-docs" }));

    act(() => result.current.remove("p-docs"));
    await waitFor(() => expect(result.current.projects.map((p) => p.projectId)).toEqual(["p-api"]));
    expect(detach).toHaveBeenCalledWith(expect.objectContaining({ chatId: "c1", projectId: "p-docs" }));
  });

  it("offers to set up a project the server says is not on the machine, then attaches it once it lands", async () => {
    let refuse = true;
    const missing = create(ProjectCheckoutMissingSchema, {
      projectId: "p-docs",
      daemonId: "d-cicd",
      state: ProjectCheckoutState.ABSENT,
      remoteUrl: "https://github.com/acme/docs",
      path: "/w/docs",
    });
    const attach = vi.fn(() => {
      if (refuse) {
        throw new ConnectError("not on that machine", Code.FailedPrecondition, undefined, [
          { desc: ProjectCheckoutMissingSchema, value: missing },
        ]);
      }
      return { projects: [primary, { ...web, projectId: "p-docs", name: "docs" }] };
    });
    setTransport(
      mockTransport({
        "ChatService.ListChatProjects": { response: { projects: [primary] } },
        "ChatService.AttachProjectToChat": attach,
      }),
    );
    const { result, rerender } = renderHook(() => useChatProjects(chat), { wrapper });
    await waitFor(() => expect(result.current.supported).toBe(true));

    act(() => result.current.add(docs));
    await waitFor(() => expect(result.current.setUp?.state).toBe("offer"));
    expect(result.current.setUp).toMatchObject({ projectName: "docs", machine: { daemonId: "d-cicd", name: "cicd" }, canClone: true });
    expect(mocks.toastError).not.toHaveBeenCalled();

    act(() => result.current.confirmSetUp());
    await waitFor(() => expect(mocks.createProjectFromRepo).toHaveBeenCalled());
    expect(mocks.createProjectFromRepo).toHaveBeenCalledWith(
      expect.objectContaining({ cloneUrl: "https://github.com/acme/docs", daemonId: "d-cicd", name: "docs" }),
    );
    expect(result.current.setUp?.state).toBe("setting_up");
    expect(result.current.pending.has("p-docs")).toBe(true);

    // The clone lands: the attach goes again, and this time the server takes it.
    refuse = false;
    mocks.installs = [{ project_id: "p-docs", daemon_id: "d-cicd", install_state: ProjectInstallState.INSTALLED }];
    rerender();
    await waitFor(() => expect(result.current.projects.map((p) => p.projectId)).toEqual(["p-api", "p-docs"]));
    expect(result.current.setUp).toBeNull();
    expect(attach).toHaveBeenCalledTimes(2);
  });

  it("sets up a picked project the machine lacks before attaching it, with no refusal first", async () => {
    const attach = vi.fn(() => ({ projects: [primary] }));
    setTransport(
      mockTransport({
        "ChatService.ListChatProjects": { response: { projects: [primary] } },
        "ChatService.AttachProjectToChat": attach,
      }),
    );
    // docs is on another machine only.
    mocks.installs = [{ project_id: "p-docs", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLED }];
    const { result } = renderHook(() => useChatProjects(chat), { wrapper });
    await waitFor(() => expect(result.current.supported).toBe(true));
    act(() => result.current.add(docs));
    await waitFor(() => expect(mocks.createProjectFromRepo).toHaveBeenCalled());
    expect(attach).not.toHaveBeenCalled();
    expect(result.current.setUp?.state).toBe("setting_up");
  });
});

describe("setting a project up again after a clone failed there", () => {
  it("waits for the new clone rather than reading the old failure as its answer", async () => {
    setTransport(mockTransport({ "ChatService.ListChatProjects": { response: { projects: [primary] } } }));
    mocks.installs = [
      { project_id: "p-docs", daemon_id: "d-cicd", install_state: ProjectInstallState.FAILED, install_error: "disk full" },
    ];
    // The new clone is queued: the row reads INSTALLING once it is re-read.
    mocks.createProjectFromRepo.mockImplementationOnce(async () => {
      mocks.installs = [{ project_id: "p-docs", daemon_id: "d-cicd", install_state: ProjectInstallState.INSTALLING }];
      return { queued: true };
    });
    const { result } = renderHook(() => useChatProjects(chat), { wrapper });
    await waitFor(() => expect(result.current.supported).toBe(true));
    act(() => result.current.add(docs));
    expect(result.current.setUp?.state).toBe("setting_up");
    await waitFor(() => expect(mocks.createProjectFromRepo).toHaveBeenCalled());
    // Past the re-read, still waiting: the old "disk full" was not this clone's.
    await act(async () => {});
    expect(result.current.setUp?.state).toBe("setting_up");
  });
});

describe("useDraftChatProjects", () => {
  it("adds and removes the draft's attached projects, for its primary only", () => {
    mocks.projects = [
      { id: "p-api", name: "api", path: "/w/api" },
      { id: "p-docs", name: "docs", path: "/w/docs" },
    ];
    const { result } = renderHook(() => useDraftChatProjects({ primaryId: "p-api" }), { wrapper });
    act(() => result.current.add(docs));
    expect(getNewChatAttachedProjects("p-api")).toEqual(["p-docs"]);
    expect(result.current.projects).toEqual([
      { projectId: "p-api", name: "api", primary: true },
      { projectId: "p-docs", name: "docs", primary: false },
    ]);
    // Another project's draft is not this one's.
    expect(getNewChatAttachedProjects("p-other")).toEqual([]);
    act(() => result.current.remove("p-docs"));
    expect(getNewChatAttachedProjects("p-api")).toEqual([]);
  });

  it("offers nothing for a chat with no machine", () => {
    const { result } = renderHook(() => useDraftChatProjects({ primaryId: "p-api", noMachine: true }), { wrapper });
    expect(result.current.supported).toBe(false);
  });
});
