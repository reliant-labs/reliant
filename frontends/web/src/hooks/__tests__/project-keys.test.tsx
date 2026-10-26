/**
 * ProjectService: one cache entry per read, whoever reads it.
 *
 *  - a project write (rename, delete, create-from-repo, git init) refetches
 *    every mounted project read — the list, the project_daemons rows;
 *  - the project store holds only the selection, and its `currentProject`
 *    follows the cache: a rename or a GetProject refresh reaches it without
 *    the store fetching anything itself, and a delete moves it to the
 *    most recent project left;
 *  - saving prompts refetches a mounted prompt read;
 *  - the stream's `file_tree` refetch (a queued clone settled on a daemon)
 *    invalidates `projectServiceKeys.all`.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import { EntityType, UserUpdateType } from "@/gen/reliant/v1/streaming_pb";
import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import type { UserUpdate } from "@/types/streaming";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { projectRow, type ProjectFixture } from "@/test/projectList";

vi.mock("@/api/streaming-grpc", () => ({
  UserStreamingService: vi.fn().mockImplementation(() => ({
    isConnected: () => false,
    disconnect: vi.fn(),
    subscribeToChatDetails: vi.fn(),
    unsubscribeFromChatDetails: vi.fn(),
  })),
}));

import { queryClient } from "@/lib/query-client";
import { useAuthStore } from "@/store/authStore";
import { useGlobalUpdatesStore } from "@/store/globalUpdatesStore";
import { useProjectStore } from "@/store/projectStore";
import {
  createProjectFromRepo,
  projectsKey,
  useProjectDaemonInstalls,
  useProjectPrompts,
  useProjects,
  useSaveProjectPrompts,
} from "../project-queries";
import { projectServiceKeys } from "../project-service-hooks_gen";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

/** A fake ProjectService over an in-memory project table. */
function projectServer(initial: ProjectFixture[]) {
  const projects = new Map(initial.map((p) => [p.id, { ...p }]));
  const installs: { projectId: string; daemonId: string; installState: ProjectInstallState }[] = [];
  const prompts = new Map<string, { id: string; name: string; content: string; description: string }[]>();
  const calls: Record<string, number> = {};
  const count = (rpc: string) => (calls[rpc] = (calls[rpc] ?? 0) + 1);
  const transport = mockTransport({
    "ProjectService.ListProjects": () => {
      count("ListProjects");
      return { projects: [...projects.values()].map(projectRow), total: projects.size };
    },
    "ProjectService.GetProject": (req) => {
      count("GetProject");
      const project = projects.get((req as { projectId: string }).projectId);
      return { project: project ? projectRow(project) : undefined };
    },
    "ProjectService.UpdateProject": (req) => {
      const { projectId, name } = req as { projectId: string; name?: string };
      const project = projects.get(projectId)!;
      if (name) project.name = name;
      return { project: projectRow(project) };
    },
    "ProjectService.DeleteProject": (req) => {
      projects.delete((req as { projectId: string }).projectId);
      return { success: true };
    },
    "ProjectService.CreateProjectFromRepo": (req) => {
      const { daemonId, name } = req as { daemonId: string; name: string };
      const project = { id: `p-${name}`, name, path: `/home/workspace/projects/${name}` };
      projects.set(project.id, project);
      installs.push({ projectId: project.id, daemonId, installState: ProjectInstallState.INSTALLING });
      return { project: projectRow(project), queued: true, daemonName: "box" };
    },
    "ProjectService.ListProjectDaemons": () => {
      count("ListProjectDaemons");
      return { projectDaemons: installs.map((row) => ({ ...row })) };
    },
    "ProjectService.GetProjectPrompts": (req) => {
      count("GetProjectPrompts");
      return { prompts: prompts.get((req as { projectId: string }).projectId) ?? [] };
    },
    "ProjectService.SaveProjectPrompts": (req) => {
      const { projectId, prompts: saved } = req as {
        projectId: string;
        prompts: { id: string; name: string; content: string; description: string }[];
      };
      prompts.set(projectId, saved);
      return { prompts: saved };
    },
    "ProjectService.TouchProject": () => ({ success: true }),
  });
  return {
    transport,
    projects,
    installs,
    calls: (rpc: string) => calls[rpc] ?? 0,
  };
}

const ALPHA: ProjectFixture = { id: "p-alpha", name: "alpha", path: "/src/alpha", is_git_repo: true };
const BETA: ProjectFixture = { id: "p-beta", name: "beta", path: "/src/beta" };

function refetch(type: string, seq: number): UserUpdate {
  return {
    id: `u-${seq}`,
    user_id: "user-1",
    sequence_number: seq,
    project_id: "p-alpha",
    update_type: UserUpdateType.REFETCH,
    entity_type: EntityType.PROJECT,
    entity_id: "p-alpha",
    data: { type },
    created_at: new Date().toISOString(),
  };
}

beforeEach(() => {
  useAuthStore.setState({ session: { access_token: "t" } as never });
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
  useAuthStore.setState({ session: null });
  useProjectStore.setState({ currentProject: null });
});

describe("ProjectService keys", () => {
  it("renaming a project refetches the mounted list, and the selection follows it", async () => {
    const server = projectServer([ALPHA, BETA]);
    setTransport(server.transport);

    const list = renderHook(() => useProjects(), { wrapper });
    await waitFor(() => expect(list.result.current.projects.map((p) => p.name)).toEqual(["alpha", "beta"]));
    useProjectStore.setState({ currentProject: list.result.current.projects[0]! });

    await act(() => useProjectStore.getState().updateProject("p-alpha", { name: "alpha-renamed" }));

    await waitFor(() =>
      expect(list.result.current.projects.map((p) => p.name)).toEqual(["alpha-renamed", "beta"]),
    );
    expect(server.calls("ListProjects")).toBe(2);
    // The store fetched nothing itself: its row is the cache's.
    expect(useProjectStore.getState().currentProject?.name).toBe("alpha-renamed");
  });

  it("refreshing the current project reaches the selection through GetProject's entry", async () => {
    const server = projectServer([ALPHA]);
    setTransport(server.transport);
    useProjectStore.setState({
      currentProject: { ...ALPHA, is_git_repo: false, is_forge: false, last_active: "", created_at: "", updated_at: "" } as never,
    });

    await act(() => useProjectStore.getState().refreshCurrentProject());

    expect(server.calls("GetProject")).toBe(1);
    expect(useProjectStore.getState().currentProject?.is_git_repo).toBe(true);
  });

  it("deleting the current project drops it from the mounted list and moves to the one left", async () => {
    const server = projectServer([ALPHA, BETA]);
    setTransport(server.transport);

    const list = renderHook(() => useProjects(), { wrapper });
    await waitFor(() => expect(list.result.current.projects).toHaveLength(2));
    useProjectStore.setState({ currentProject: list.result.current.projects[1]! });

    await act(() => useProjectStore.getState().deleteProject("p-beta"));

    await waitFor(() => expect(list.result.current.projects.map((p) => p.id)).toEqual(["p-alpha"]));
    // No project picker to fall back to: the context moves to the most
    // recent project left.
    expect(useProjectStore.getState().currentProject?.id).toBe("p-alpha");
  });

  it("adding a repo refetches the mounted project list and the project_daemons rows", async () => {
    const server = projectServer([ALPHA]);
    setTransport(server.transport);

    const list = renderHook(() => useProjects(), { wrapper });
    const installs = renderHook(() => useProjectDaemonInstalls(), { wrapper });
    await waitFor(() => {
      expect(list.result.current.projects).toHaveLength(1);
      expect(installs.result.current.data).toEqual([]);
    });

    const result = await createProjectFromRepo({
      cloneUrl: "https://github.com/acme/widgets.git",
      daemonId: "d-1",
      name: "widgets",
    });
    expect(result.queued).toBe(true);
    expect(result.project?.id).toBe("p-widgets");

    await waitFor(() => {
      expect(list.result.current.projects.map((p) => p.id)).toEqual(["p-alpha", "p-widgets"]);
      expect(installs.result.current.data).toEqual([
        expect.objectContaining({ project_id: "p-widgets", daemon_id: "d-1", install_state: ProjectInstallState.INSTALLING }),
      ]);
    });
  });

  it("saving prompts refetches a mounted prompt read", async () => {
    const server = projectServer([ALPHA]);
    setTransport(server.transport);

    const picker = renderHook(() => useProjectPrompts("p-alpha"), { wrapper });
    await waitFor(() => expect(picker.result.current.isSuccess).toBe(true));
    expect(picker.result.current.prompts).toEqual([]);

    const save = renderHook(() => useSaveProjectPrompts(), { wrapper });
    await act(() =>
      save.result.current.mutateAsync({
        projectId: "p-alpha",
        prompts: [{ id: "review", name: "Review", content: "Review this diff", description: "" }],
      }),
    );

    await waitFor(() => expect(picker.result.current.prompts.map((p) => p.name)).toEqual(["Review"]));
    expect(server.calls("GetProjectPrompts")).toBe(2);
  });

  it("the stream's file_tree refetch (a clone settled) invalidates projectServiceKeys.all", () => {
    const daemonsKey = projectServiceKeys.listProjectDaemons({});
    queryClient.setQueryData(projectsKey(), { projects: [], total: 0 });
    queryClient.setQueryData(daemonsKey, { projectDaemons: [] });

    useGlobalUpdatesStore.getState().handleUpdate([refetch("file_tree", 1)]);

    expect(queryClient.getQueryState(projectsKey())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(daemonsKey)?.isInvalidated).toBe(true);
  });

  it("another refetch type leaves project reads alone", () => {
    queryClient.setQueryData(projectsKey(), { projects: [], total: 0 });

    useGlobalUpdatesStore.getState().handleUpdate([refetch("worktree_changes", 2)]);

    expect(queryClient.getQueryState(projectsKey())?.isInvalidated).toBe(false);
  });
});
