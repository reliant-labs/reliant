// Copyright (c) 2025 Reliant Labs

/**
 * Projects: ProjectService on the generated hooks and keys.
 *
 * Every project read in the app is a cache entry under `projectServiceKeys`
 * holding exactly what the generated hook fetches — the project list as a
 * `ListProjectsResponse` under `projectsKey()`, one project as a
 * `GetProjectResponse`, its git info, prompts, and the project_daemons rows —
 * converted to the plain frontend types below on read. Components read
 * through the hooks; imperative callers (the project store's selection
 * flow, app start-up, onboarding) read through the SAME entries with
 * `fetchProjects` / `getCachedProjects`, so a mounted list and a store
 * action never disagree.
 *
 * Every write here waits for its RPC and then invalidates
 * `projectServiceKeys.all`, so every mounted project read refetches. The
 * stream invalidates the same scope when a queued clone settles on a daemon
 * (the `file_tree` refetch, globalUpdatesStore) — that is when a project's
 * checkout and its forge name appear.
 *
 * The project store (store/projectStore.ts) keeps only the selection: which
 * project is current. Its `currentProject` is reconciled from these entries
 * whenever one of them changes, so it is never a second copy that can go
 * stale.
 */

import { useCallback, useMemo } from "react";
import { ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { partialMatchKey, useQueryClient, type QueryClient } from "@tanstack/react-query";

import {
  GetProjectPromptsResponseSchema,
  ProjectService,
  ProjectInstallState,
  type FileChange as ProtoFileChange,
  type GetProjectGitInfoResponse,
  type GetProjectPromptsResponse,
  type GetProjectResponse,
  type ListProjectDaemonsResponse,
  type ListProjectsResponse,
  type Project as ProtoProject,
  type ProjectDaemon as ProtoProjectDaemon,
  type Prompt as ProtoPrompt,
} from "../gen/reliant/v1/project_pb";
import type { FileChangeStatus } from "../gen/reliant/v1/common_pb";
import { trackEvent } from "../lib/analytics";
import { connectClient } from "../lib/connect";
import { logger } from "../lib/logger";
import { queryClient } from "../lib/query-client";
import { toast } from "../lib/toast-manager";
import { useAuthStore } from "../store/authStore";
import {
  projectServiceKeys,
  projectServiceQueries,
  useGetProjectGitInfo,
  useGetProjectPrompts,
  useListProjectDaemons,
  useListProjects,
  useSaveProjectPrompts as useSaveProjectPromptsRpc,
} from "./project-service-hooks_gen";

export { ProjectInstallState };

const client = connectClient(ProjectService);

// ── Frontend types ──────────────────────────────────────────────────────────

export interface Project {
  id: string;
  name: string;
  path: string;
  description?: string;
  is_git_repo: boolean;
  default_branch?: string;
  last_active: string;
  created_at: string;
  updated_at: string;
  // Canonical git remote URL — present for git-backed projects whose root
  // repo's remote has been resolved. NULL for non-git / local-only
  // projects. The picker uses this to know whether a project can be
  // re-cloned to another daemon.
  remote_url?: string;
  // True when the project's repo root contains a forge.yaml. Populated at
  // clone / project-create time by the server.
  is_forge: boolean;
  // forge's name for the project (forge.yaml `name`) — the key its
  // control-plane environments are filed under. Absent until a daemon has
  // read it; see services/forge/environments.ts resolveForgeProjectName.
  forge_project_name?: string;
}

// ProjectDaemonInfo mirrors the proto ProjectDaemon row — one record per
// (project, daemon) pair where the daemon has a local clone of the project.
export interface ProjectDaemonInfo {
  project_id: string;
  daemon_id: string;
  path: string;
  default_branch?: string;
  cloned_at: string;
  /** A row can be mid-clone or failed; only INSTALLED means the checkout is there. */
  install_state: ProjectInstallState;
}

export interface FileChange {
  path: string;
  status: FileChangeStatus;
  diff: string;
  content: string;
  original_content: string;
  is_new: boolean;
}

export interface ProjectChanges {
  branch: string;
  files: FileChange[];
  total_files: number;
}

export interface Prompt {
  id: string;
  name: string;
  content: string;
  description: string;
  created_at: string;
  updated_at: string;
}

// ── Mappers ─────────────────────────────────────────────────────────────────

export function projectFromProto(proto: ProtoProject): Project {
  return {
    id: proto.id,
    name: proto.name,
    path: proto.path,
    description: proto.description || undefined,
    is_git_repo: proto.isGitRepo,
    default_branch: proto.defaultBranch || undefined,
    // A row the server never touched has no last_active; order it by its
    // last write instead of as the oldest project there is.
    last_active: proto.lastActive || proto.updatedAt || proto.createdAt,
    created_at: proto.createdAt,
    updated_at: proto.updatedAt,
    remote_url: proto.remoteUrl || undefined,
    is_forge: proto.isForge,
    forge_project_name: proto.forgeProjectName || undefined,
  };
}

function projectDaemonFromProto(proto: ProtoProjectDaemon): ProjectDaemonInfo {
  return {
    project_id: proto.projectId,
    daemon_id: proto.daemonId,
    path: proto.path,
    default_branch: proto.defaultBranch || undefined,
    cloned_at: proto.clonedAt,
    install_state: proto.installState,
  };
}

function promptFromProto(proto: ProtoPrompt): Prompt {
  return {
    id: proto.id,
    name: proto.name,
    content: proto.content,
    description: proto.description,
    created_at: proto.createdAt,
    updated_at: proto.updatedAt,
  };
}

function fileChangeFromProto(proto: ProtoFileChange): FileChange {
  return {
    path: proto.path,
    status: proto.status,
    diff: proto.diff,
    content: proto.content,
    original_content: proto.originalContent,
    is_new: proto.isNew,
  };
}

/**
 * Whether two rows of one project differ in anything a reader acts on.
 * `last_active` is left out on purpose: every selection touches it, and a
 * new object for that alone re-ran every effect keyed on the current project.
 */
export function sameProject(a: Project, b: Project): boolean {
  return (
    a.id === b.id &&
    a.name === b.name &&
    a.path === b.path &&
    a.description === b.description &&
    a.is_git_repo === b.is_git_repo &&
    a.default_branch === b.default_branch &&
    a.created_at === b.created_at &&
    a.updated_at === b.updated_at &&
    a.remote_url === b.remote_url &&
    a.is_forge === b.is_forge &&
    a.forge_project_name === b.forge_project_name
  );
}

// ── Session gate ────────────────────────────────────────────────────────────

/**
 * ProjectService is authenticated. Reads mounted on the sign-in screen (the
 * shell's pickers render before a session lands) wait for the session rather
 * than going out without a credential and coming back 401.
 */
function useHasSession(): boolean {
  return useAuthStore((state) => !!state.session);
}

// ── The project list ────────────────────────────────────────────────────────

/** The one project-list read the app makes; every list caller shares its entry. */
export const LIST_PROJECTS_REQUEST = { limit: 100, offset: 0 } as const;

export const projectsKey = () => projectServiceKeys.listProjects(LIST_PROJECTS_REQUEST);

/**
 * How long a project list is fresh. Writes made here invalidate it, and a
 * settled clone invalidates it from the stream; this only bounds how long a
 * project added from another device goes unnoticed.
 */
export const PROJECTS_STALE_TIME_MS = 30_000;

const EMPTY_PROJECTS: Project[] = [];

// One converted array per response, so every reader of one cache entry gets
// the same array (and the same row objects) until the entry changes.
const projectsByResponse = new WeakMap<ListProjectsResponse, Project[]>();

export function projectsOf(response: ListProjectsResponse | undefined): Project[] {
  if (!response) return EMPTY_PROJECTS;
  let projects = projectsByResponse.get(response);
  if (!projects) {
    projects = response.projects.map(projectFromProto);
    projectsByResponse.set(response, projects);
  }
  return projects;
}

export interface ProjectListView {
  projects: Project[];
  /** True until the first answer (or while signed out, never). */
  isLoading: boolean;
  error: Error | null;
  /** Refetch the list now (a Retry button). */
  refetch: () => void;
}

/** Every project the caller can open, from the shared list entry. */
export function useProjects(): ProjectListView {
  const hasSession = useHasSession();
  const query = useListProjects(LIST_PROJECTS_REQUEST, {
    enabled: hasSession,
    staleTime: PROJECTS_STALE_TIME_MS,
    select: projectsOf,
  });
  const { refetch } = query;
  const refetchList = useCallback(() => {
    void refetch();
  }, [refetch]);
  return {
    projects: query.data ?? EMPTY_PROJECTS,
    isLoading: hasSession && query.isPending,
    error: query.error,
    refetch: refetchList,
  };
}

/** One project by id from the shared list entry; undefined until it is there. */
export function useProject(projectId: string | null | undefined): Project | undefined {
  const { projects } = useProjects();
  return useMemo(
    () => (projectId ? projects.find((project) => project.id === projectId) : undefined),
    [projects, projectId],
  );
}

/**
 * The project list for an imperative caller, through the hook's entry:
 * concurrent callers share one RPC, and the answer lands where every mounted
 * list reads it. `fresh` refetches even a fresh entry — for a caller that
 * knows a project appeared server-side (an AlreadyExists answer, a project
 * opened from another device).
 */
export async function fetchProjects(options: { fresh?: boolean } = {}): Promise<Project[]> {
  const response = await queryClient.fetchQuery(
    projectServiceQueries.listProjects(LIST_PROJECTS_REQUEST, {
      staleTime: options.fresh ? 0 : PROJECTS_STALE_TIME_MS,
    }),
  );
  return projectsOf(response);
}

/** The project list as the cache holds it now; [] before the first answer. */
export function getCachedProjects(): Project[] {
  return projectsOf(queryClient.getQueryData<ListProjectsResponse>(projectsKey()));
}

/** Patch one row of the cached list in place (no refetch). */
function patchCachedProject(id: string, patch: (row: ProtoProject) => ProtoProject): void {
  queryClient.setQueryData<ListProjectsResponse>(projectsKey(), (prev) => {
    if (!prev || !prev.projects.some((row) => row.id === id)) return prev;
    return { ...prev, projects: prev.projects.map((row) => (row.id === id ? patch(row) : row)) };
  });
}

function invalidateProjects(client: QueryClient = queryClient): Promise<void> {
  return client.invalidateQueries({ queryKey: projectServiceKeys.all });
}

/**
 * Re-read one project (GetProject) and put the answer in the list entry, for
 * a caller that just changed it by another route (git init, a new
 * workspace). The project store reconciles its selection from the result.
 */
export async function refreshProject(projectId: string): Promise<Project | undefined> {
  const response = await queryClient.fetchQuery(projectServiceQueries.getProject({ projectId }, { staleTime: 0 }));
  const fresh = response.project;
  if (!fresh) return undefined;
  patchCachedProject(projectId, () => fresh);
  return projectFromProto(fresh);
}

/**
 * Call `listener` with the project rows of every list or GetProject entry
 * written to the cache — a refetch, an invalidation after a write, a patch.
 * How the project store keeps its selected row in step with the server.
 */
export function subscribeToProjectRows(listener: (rows: Project[]) => void): () => void {
  return queryClient.getQueryCache().subscribe((event) => {
    if (event.type !== "updated" || event.action.type !== "success") return;
    const key = event.query.queryKey;
    if (partialMatchKey(key, projectServiceKeys.listProjects.all)) {
      listener(projectsOf(event.query.state.data as ListProjectsResponse | undefined));
    } else if (partialMatchKey(key, projectServiceKeys.getProject.all)) {
      const row = (event.query.state.data as GetProjectResponse | undefined)?.project;
      if (row) listener([projectFromProto(row)]);
    }
  });
}

// ── Project writes ──────────────────────────────────────────────────────────

export interface CreateProjectInput {
  name: string;
  path: string;
  description?: string;
  default_branch?: string;
}

/**
 * Create a project. Toasts the outcome, except AlreadyExists: opening the
 * existing project is a normal path every caller handles itself.
 */
export async function createProject(input: CreateProjectInput): Promise<Project> {
  try {
    const response = await client.createProject({
      name: input.name,
      path: input.path,
      description: input.description,
      defaultBranch: input.default_branch,
    });
    if (!response.project) throw new Error("No project in response");
    const created = response.project;
    queryClient.setQueryData<ListProjectsResponse>(projectsKey(), (prev) =>
      prev && !prev.projects.some((row) => row.id === created.id)
        ? { ...prev, projects: [...prev.projects, created], total: prev.total + 1 }
        : prev,
    );
    await invalidateProjects();
    const project = projectFromProto(created);
    trackEvent("project_created", {
      is_forge: project.is_forge,
      is_git_repo: project.is_git_repo,
      has_remote: Boolean(project.remote_url),
    });
    toast.success(`Added ${project.name}`);
    return project;
  } catch (error) {
    if (!(error instanceof ConnectError && error.code === Code.AlreadyExists)) {
      toast.error(error);
    }
    throw error;
  }
}

export interface UpdateProjectInput {
  name?: string;
  description?: string;
  default_branch?: string;
}

/** Rename / redescribe a project. Errors propagate to the caller, which surfaces them. */
export async function updateProject(projectId: string, input: UpdateProjectInput): Promise<Project> {
  const response = await client.updateProject({
    projectId,
    name: input.name,
    description: input.description,
    defaultBranch: input.default_branch,
  });
  if (!response.project) throw new Error("No project in response");
  const updated = response.project;
  patchCachedProject(projectId, () => updated);
  await invalidateProjects();
  toast.success("Project updated successfully");
  return projectFromProto(updated);
}

export async function deleteProject(projectId: string): Promise<void> {
  await client.deleteProject({ projectId });
  queryClient.setQueryData<ListProjectsResponse>(projectsKey(), (prev) =>
    prev && prev.projects.some((row) => row.id === projectId)
      ? { ...prev, projects: prev.projects.filter((row) => row.id !== projectId), total: Math.max(0, prev.total - 1) }
      : prev,
  );
  await invalidateProjects();
  toast.success("Project deleted successfully");
}

/**
 * Record that the project was opened (last_active), without blocking the
 * caller: the cached row is patched at once and the RPC is fire-and-forget.
 * No refetch — the server's answer is the same timestamp.
 */
export function touchProject(projectId: string): void {
  const now = new Date().toISOString();
  patchCachedProject(projectId, (row) => ({ ...row, lastActive: now }));
  client.touchProject({ projectId }).catch((error: unknown) => {
    logger.error("[project-queries] Failed to touch project on backend", { projectId, error });
  });
}

export interface CreateProjectFromRepoInput {
  cloneUrl: string;
  daemonId: string;
  name?: string;
  branch?: string;
  path?: string;
}

export interface CreateProjectFromRepoResult {
  project: Project | undefined;
  projectDaemon: ProjectDaemonInfo | undefined;
  /** The clone has NOT run yet: it is durably enqueued, and the machine may be asleep. */
  queued: boolean;
  daemonName: string;
}

/**
 * Add a GitHub repo as a project on a daemon: start the clone, create the
 * project, and record where the checkout will live — in ONE call.
 *
 * `queued: true` means the command is durably enqueued; render that as
 * queued. The real outcome arrives later over the updates stream (the
 * `file_tree` refetch on success, a `daemon_command_failed` notification on
 * failure), which invalidates these entries again.
 */
export async function createProjectFromRepo(input: CreateProjectFromRepoInput): Promise<CreateProjectFromRepoResult> {
  const response = await client.createProjectFromRepo(input);
  await invalidateProjects();
  return {
    project: response.project ? projectFromProto(response.project) : undefined,
    projectDaemon: response.projectDaemon ? projectDaemonFromProto(response.projectDaemon) : undefined,
    queued: response.queued,
    daemonName: response.daemonName,
  };
}

export interface InitializeGitRepoInput {
  initial_branch?: string;
  gitignore_patterns?: string[];
  initial_commit?: boolean;
}

/** `git init` a project's checkout. Every project read refetches: is_git_repo and the branches changed. */
export async function initializeGitRepo(projectId: string, input: InitializeGitRepoInput): Promise<void> {
  await client.initializeGitRepo({
    projectId,
    initialBranch: input.initial_branch || "main",
    gitignorePatterns: input.gitignore_patterns ?? [],
    initialCommit: input.initial_commit ?? true,
  });
  await invalidateProjects();
}

// ── Git state ───────────────────────────────────────────────────────────────

/** A project checkout's git status (branch, dirty files, ahead/behind). */
export function useProjectGitInfo(project: Pick<Project, "id" | "is_git_repo">) {
  return useGetProjectGitInfo(
    { projectId: project.id },
    { enabled: project.is_git_repo, staleTime: 10_000, retry: false },
  );
}

export type ProjectGitInfo = GetProjectGitInfoResponse;

/** The main checkout's uncommitted changes, always fresh (the Changes tab keeps its own short cache). */
export async function fetchProjectChanges(projectId: string): Promise<ProjectChanges> {
  const response = await queryClient.fetchQuery(
    projectServiceQueries.getProjectChanges({ projectId }, { staleTime: 0 }),
  );
  return {
    branch: response.branch,
    files: response.files.map(fileChangeFromProto),
    total_files: response.totalFiles,
  };
}

// ── Prompts ─────────────────────────────────────────────────────────────────

const EMPTY_PROMPTS: Prompt[] = [];

function promptsOf(response: GetProjectPromptsResponse): Prompt[] {
  return response.prompts.map(promptFromProto);
}

/** A project's saved prompts (Settings → Prompts, the composer's prompt picker). */
export function useProjectPrompts(projectId: string | undefined, options: { enabled?: boolean } = {}) {
  const query = useGetProjectPrompts(
    { projectId: projectId ?? "" },
    { enabled: !!projectId && (options.enabled ?? true), select: promptsOf },
  );
  return { ...query, prompts: query.data ?? EMPTY_PROMPTS };
}

/**
 * Save a project's prompts (the whole set). The saved set becomes the
 * project's prompts entry at once, and every project read refetches. Errors
 * are the caller's to render inline, so they do not also toast.
 */
export function useSaveProjectPrompts() {
  const client = useQueryClient();
  return useSaveProjectPromptsRpc({
    meta: { silenceErrorToast: true },
    onSuccess: (response, request) => {
      client.setQueryData(
        projectServiceKeys.getProjectPrompts({ projectId: request.projectId ?? "" }),
        create(GetProjectPromptsResponseSchema, { prompts: response.prompts }),
      );
    },
  });
}

// ── Project ↔ daemon installs ───────────────────────────────────────────────

function installsOf(response: ListProjectDaemonsResponse): ProjectDaemonInfo[] {
  return response.projectDaemons.map(projectDaemonFromProto);
}

/**
 * Every project_daemons row the caller owns — which daemon has which project
 * installed (and which clones are still in flight). The same rows the server
 * checks a trigger's daemon against.
 */
export function useProjectDaemonInstalls(enabled = true) {
  return useListProjectDaemons({}, { enabled, staleTime: 30_000, select: installsOf });
}
