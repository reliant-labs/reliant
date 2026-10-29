// Copyright (c) 2025 Reliant Labs

/**
 * Package commands (Makefile / npm / Taskfile targets) and the processes they
 * start — PackageCommandsService — on the generated hooks and keys, kept live
 * by the user stream.
 *
 * The cache holds what the generated hooks fetch (ListPackageCommandsResponse,
 * ListPackageProcessesResponse, GetCommandFavoritesResponse) under
 * `packageCommandsServiceKeys`. RunCommand / KillProcess / SetCommandFavorite
 * are generated mutations, which invalidate `packageCommandsServiceKeys.all`;
 * the stream's process events patch the cached process lists
 * (upsertPackageProcess). Components read the frontend types mapped from
 * those entries.
 *
 * BackgroundService also has a ListProcesses and a KillProcess: the two
 * generated files export the same hook names, so this module imports this
 * service's under aliases and background-queries.ts the other's.
 */

import { useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";

import { BackgroundProcessStatus, OutputStreamType } from "../gen/reliant/v1/common_pb";
import {
  GetCommandFavoritesResponseSchema,
  PackageProcessSchema,
  PackageType as ProtoPackageType,
  type CommandsByType as ProtoCommandsByType,
  type GetCommandFavoritesResponse,
  type ListPackageProcessesResponse,
  type PackageCommand as ProtoPackageCommand,
  type PackageProcess as ProtoPackageProcess,
} from "../gen/reliant/v1/package_commands_pb";
import { queryClient } from "../lib/query-client";
import {
  packageCommandsServiceKeys,
  useGetCommandFavorites,
  useGetProcessLogs as useGetPackageProcessLogs,
  useKillProcess as useKillPackageProcessRpc,
  useListCommands,
  useListProcesses as useListPackageProcesses,
  useRunCommand as useRunCommandRpc,
  useSetCommandFavorite,
} from "./package-commands-service-hooks_gen";

// ── Frontend types ──────────────────────────────────────────────────────────

export type PackageType = "makefile" | "npm" | "taskfile";

const PACKAGE_TYPE_TO_STRING: Record<number, PackageType> = {
  [ProtoPackageType.MAKEFILE]: "makefile",
  [ProtoPackageType.NPM]: "npm",
  [ProtoPackageType.TASKFILE]: "taskfile",
};

const STRING_TO_PACKAGE_TYPE: Record<PackageType, ProtoPackageType> = {
  makefile: ProtoPackageType.MAKEFILE,
  npm: ProtoPackageType.NPM,
  taskfile: ProtoPackageType.TASKFILE,
};

export interface PackageCommand {
  name: string;
  description?: string;
  command: string;
  package_type: PackageType;
  source: string;
  category?: string;
  dependencies?: string[];
  working_dir: string;
  relative_path?: string;
}

export interface ListCommandsResponse {
  commands: Record<PackageType, PackageCommand[]>;
  detected_types: PackageType[];
}

export interface PackageProcess {
  id: string;
  command: string;
  status: BackgroundProcessStatus;
  worktree_id?: string;
  working_dir: string;
  start_time: string;
  end_time?: string;
  exit_code?: number;
  /**
   * The machine the process runs on — the one holding its workspace. Its
   * logs, output stream and kill go there. Absent for a process the default
   * machine runs, and for one known only from a stream event.
   */
  daemon_id?: string;
}

export interface OutputLine {
  type: "stdout" | "stderr";
  text: string;
}

export interface ProcessLogsResponse {
  stdout: string;
  stderr: string;
  combined: OutputLine[];
}

// ── Proto mapping ───────────────────────────────────────────────────────────

function commandFromProto(proto: ProtoPackageCommand): PackageCommand {
  return {
    name: proto.name,
    description: proto.description || undefined,
    command: proto.command,
    package_type: PACKAGE_TYPE_TO_STRING[proto.packageType] || "makefile",
    source: proto.source,
    category: proto.category || undefined,
    dependencies: proto.dependencies?.length > 0 ? proto.dependencies : undefined,
    working_dir: proto.workingDir,
    relative_path: proto.relativePath || undefined,
  };
}

function commandsByTypeFromProto(protoCommands: { [key: string]: ProtoCommandsByType }): Record<PackageType, PackageCommand[]> {
  const result: Record<string, PackageCommand[]> = {};
  for (const [pkgType, cmdsByType] of Object.entries(protoCommands)) {
    result[pkgType] = cmdsByType.commands.map(commandFromProto);
  }
  return result as Record<PackageType, PackageCommand[]>;
}

export function packageProcessFromProto(proto: ProtoPackageProcess): PackageProcess {
  return {
    id: proto.id,
    command: proto.command,
    status: proto.status,
    worktree_id: proto.worktreeId,
    working_dir: proto.workingDir,
    start_time: proto.startTime,
    end_time: proto.endTime,
    exit_code: proto.exitCode,
    daemon_id: proto.daemonId || undefined,
  };
}

function packageProcessToProto(process: PackageProcess): ProtoPackageProcess {
  return create(PackageProcessSchema, {
    id: process.id,
    command: process.command,
    status: process.status,
    worktreeId: process.worktree_id,
    workingDir: process.working_dir,
    startTime: process.start_time,
    endTime: process.end_time,
    exitCode: process.exit_code,
    daemonId: process.daemon_id ?? "",
  });
}

// ── Reads ───────────────────────────────────────────────────────────────────

/**
 * The commands a worktree (or, with none, a project directory) offers.
 * `chatId` is the chat the panel is beside: for the main checkout, the server
 * asks that chat's machine, as the Files tab and terminal do.
 */
export function usePackageCommands(worktreeId?: string, path?: string, chatId?: string) {
  const query = useListCommands({ worktreeId, path, chatId: chatId || undefined }, { enabled: !!(worktreeId || path) });
  const response = query.data;
  const data = useMemo<ListCommandsResponse | undefined>(
    () =>
      response && {
        commands: commandsByTypeFromProto(response.commands),
        detected_types: response.detectedTypes as PackageType[],
      },
    [response],
  );
  return { ...query, data };
}

const NO_PROCESSES: PackageProcess[] = [];

/**
 * Processes package commands started: one worktree's, or every worktree's
 * without one. The fetch is the cold-start seed; the stream keeps it live.
 */
export function usePackageProcesses(worktreeId?: string) {
  const query = useListPackageProcesses({ worktreeId: worktreeId || undefined }, { select: packageProcessesOf });
  return { ...query, data: query.data ?? NO_PROCESSES };
}

// Module-level, so React Query re-maps only when the response changes.
const packageProcessesOf = (response: ListPackageProcessesResponse) => response.processes.map(packageProcessFromProto);

/** A process's captured logs, read from its machine, fetched while `processId` is set. */
export function useProcessLogs(processId: string | null, daemonId?: string) {
  const query = useGetPackageProcessLogs({ processId: processId ?? "", daemonId }, { enabled: !!processId });
  const response = query.data;
  const data = useMemo<ProcessLogsResponse | undefined>(
    () =>
      response && {
        stdout: response.stdout,
        stderr: response.stderr,
        combined: response.combined.map((line) => ({
          type: line.type === OutputStreamType.STDERR ? "stderr" : "stdout",
          text: line.text,
        })),
      },
    [response],
  );
  return { ...query, data };
}

const NO_FAVORITES: ReadonlySet<string> = new Set();

/** A project's favorite command keys (getCommandKey). */
export function useCommandFavorites(projectId: string | undefined) {
  const query = useGetCommandFavorites({ projectId: projectId ?? "" }, { enabled: !!projectId });
  const keys = query.data?.commandKeys;
  const data = useMemo<ReadonlySet<string>>(() => (keys ? new Set(keys) : NO_FAVORITES), [keys]);
  return { ...query, data };
}

// ── Writes ──────────────────────────────────────────────────────────────────

export interface RunCommandInput {
  worktreeId?: string;
  path?: string;
  /** The chat the panel is beside; picks the machine for the main checkout. */
  chatId?: string;
  commandName: string;
  packageType: PackageType;
  /** The directory to run from (the command's working_dir). */
  workingDir?: string;
  env?: Record<string, string>;
}

/** The RunCommand request for a package command. */
export function runCommandRequest(input: RunCommandInput) {
  return {
    worktreeId: input.worktreeId,
    path: input.path,
    chatId: input.chatId || undefined,
    commandName: input.commandName,
    packageType: STRING_TO_PACKAGE_TYPE[input.packageType] ?? ProtoPackageType.MAKEFILE,
    env: input.env ?? {},
    workingDir: input.workingDir,
  };
}

/** Run a command (`mutateAsync(runCommandRequest(...))`); refetches the process lists. */
export const useRunCommand = useRunCommandRpc;

/**
 * Stop a process a command started (`mutateAsync({ processId, daemonId })`,
 * daemonId being the process's machine); refetches the process lists.
 */
export const useKillPackageProcess = useKillPackageProcessRpc;

/**
 * Mark or unmark a favorite, shown at once: the cached favorites are patched
 * before the call and restored if it fails; the generated mutation then
 * refetches them on success.
 */
export function useToggleCommandFavorite() {
  const client = useQueryClient();
  return useSetCommandFavorite({
    onMutate: async ({ projectId = "", commandKey = "", isFavorite = false }) => {
      const key = packageCommandsServiceKeys.getCommandFavorites({ projectId });
      await client.cancelQueries({ queryKey: key });
      const previous = client.getQueryData<GetCommandFavoritesResponse>(key);
      const keys = new Set(previous?.commandKeys ?? []);
      if (isFavorite) keys.add(commandKey);
      else keys.delete(commandKey);
      client.setQueryData(key, create(GetCommandFavoritesResponseSchema, { commandKeys: [...keys] }));
      return { key, previous };
    },
    // The generated hook infers onMutate's return (TanStack's TContext), so
    // the rollback arrives typed.
    onError: (_error, _vars, rollback) => {
      if (rollback) client.setQueryData(rollback.key, rollback.previous);
    },
  });
}

// ── Stream ──────────────────────────────────────────────────────────────────

/** A message's fields without its type tag, so create() builds a new message from them. */
function fieldsOf({ $typeName: _typeName, ...fields }: ProtoPackageProcess) {
  return fields;
}

function patchList(worktreeId: string | undefined, update: (processes: ProtoPackageProcess[]) => ProtoPackageProcess[]): void {
  const patch = (prev: ListPackageProcessesResponse | undefined) => (prev ? { ...prev, processes: update(prev.processes) } : prev);
  queryClient.setQueryData<ListPackageProcessesResponse>(packageCommandsServiceKeys.listProcesses({}), patch);
  if (worktreeId) {
    queryClient.setQueryData<ListPackageProcessesResponse>(packageCommandsServiceKeys.listProcesses({ worktreeId }), patch);
  }
}

/**
 * A process started, finished or failed. One a list already holds takes the
 * event's status, exit code and end time; an unknown one is added. Lists that
 * were never fetched are left alone: their first fetch is current.
 */
export function upsertPackageProcess(process: PackageProcess): void {
  patchList(process.worktree_id, (processes) => {
    const index = processes.findIndex((p) => p.id === process.id);
    if (index < 0) return [packageProcessToProto(process), ...processes];
    const existing = processes[index]!;
    const next = [...processes];
    next[index] = create(PackageProcessSchema, {
      ...fieldsOf(existing),
      status: process.status,
      exitCode: process.exit_code,
      endTime: process.end_time,
    });
    return next;
  });
}
