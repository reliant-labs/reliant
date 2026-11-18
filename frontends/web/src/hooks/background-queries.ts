// Copyright (c) 2025 Reliant Labs

/**
 * The daemon's background processes — BackgroundService — on the generated
 * hooks and keys, kept live by the user stream.
 *
 * The React Query cache is the single source of truth: it holds exactly what
 * the generated hooks fetch (ListBackgroundProcessesResponse,
 * GetProcessOutputResponse) under `backgroundServiceKeys`. The stream's
 * process_started / _completed / _failed / _port_changed events patch the
 * same cached list a component reads (upsertBackgroundProcess,
 * patchBackgroundProcessPorts), and KillProcess, a generated mutation,
 * invalidates `backgroundServiceKeys.all`. Components read BackgroundProcess,
 * a view of the proto mapped from that entry.
 *
 * Every reader lists every process (no worktree filter) and filters on the
 * client, so there is one list to keep live.
 */

import { useCallback, useMemo } from "react";
import { create } from "@bufbuild/protobuf";

import {
  BackgroundProcessSchema,
  type BackgroundProcess as ProtoBackgroundProcess,
  type ListBackgroundProcessesResponse,
} from "../gen/reliant/v1/background_pb";
import { BackgroundProcessStatus } from "../gen/reliant/v1/common_pb";
import { queryClient } from "../lib/query-client";
import { hasErrorReason } from "../lib/errorReasons";
import {
  backgroundServiceKeys,
  useGetProcessOutput,
  useKillProcess,
  useListProcesses,
} from "./background-service-hooks_gen";

export { BackgroundProcessStatus };

// ── Frontend types ──────────────────────────────────────────────────────────

export interface PortInfo {
  port: number;
  protocol: string;
  state: string;
  address: string;
}

export interface BackgroundProcess {
  id: string;
  command: string;
  status: BackgroundProcessStatus;
  start_time: string;
  end_time?: string;
  exit_code?: number;
  working_dir: string;
  worktree_id?: string;
  session_id: string;
  /** The chat whose agent started it; unset for a Commands-panel process. */
  chat_id?: string;
  /** The thread (main or a sub-agent's) whose tool call started it. */
  thread_id?: string;
  /** The machine it runs on; output, stream and stop requests go there. */
  daemon_id?: string;
  ports?: PortInfo[];
  /** The agent asked for it to outlive the idle reaper. */
  keep_alive?: boolean;
  /** When the machine's idle reaper stops it if nothing touches it. */
  stops_after?: string;
  /** Why the reaper never will, when stops_after is unset. */
  stop_exempt?: string;
}

export interface ProcessOutput {
  stdout: string;
  stderr: string;
}

// ── Proto mapping ───────────────────────────────────────────────────────────

export function backgroundProcessFromProto(proto: ProtoBackgroundProcess): BackgroundProcess {
  return {
    id: proto.id,
    command: proto.command,
    status: proto.status,
    start_time: proto.startTime,
    end_time: proto.endTime,
    exit_code: proto.exitCode,
    working_dir: proto.workingDir,
    worktree_id: proto.worktreeId,
    session_id: proto.sessionId,
    chat_id: proto.chatId,
    thread_id: proto.threadId,
    daemon_id: proto.daemonId,
    ports: proto.ports.map((port) => ({ port: port.port, protocol: port.protocol, state: port.state, address: port.address })),
    keep_alive: proto.keepAlive,
    stops_after: proto.stopsAfter,
    stop_exempt: proto.stopExempt,
  };
}

function backgroundProcessToProto(process: BackgroundProcess): ProtoBackgroundProcess {
  return create(BackgroundProcessSchema, {
    id: process.id,
    command: process.command,
    status: process.status,
    startTime: process.start_time,
    endTime: process.end_time,
    exitCode: process.exit_code,
    workingDir: process.working_dir,
    worktreeId: process.worktree_id,
    sessionId: process.session_id,
    chatId: process.chat_id,
    threadId: process.thread_id,
    daemonId: process.daemon_id,
    ports: process.ports ?? [],
    keepAlive: process.keep_alive ?? false,
    stopsAfter: process.stops_after,
    stopExempt: process.stop_exempt,
  });
}

// ── Reads ───────────────────────────────────────────────────────────────────

/** The one list every reader shares: all processes, every worktree. */
const ALL_PROCESSES = {} as const;
const allProcessesKey = () => backgroundServiceKeys.listProcesses(ALL_PROCESSES);

const NO_PROCESSES: BackgroundProcess[] = [];

/**
 * Every background process the daemon is tracking, across worktrees. The
 * fetch is the cold-start seed; the stream keeps it live.
 */
export function useBackgroundProcesses() {
  const query = useListProcesses(ALL_PROCESSES, { select: backgroundProcessesOf });
  return { ...query, data: query.data ?? NO_PROCESSES };
}

// Module-level, so React Query re-maps only when the response changes.
const backgroundProcessesOf = (response: ListBackgroundProcessesResponse) => response.processes.map(backgroundProcessFromProto);

/**
 * How often a chat's process list is re-read while one of its processes is
 * still running. Lifecycle events from the machine keep the list live where
 * the machine sends them; this is the floor for one that does not (an older
 * daemon), so a finished process never reads "Running" for long.
 */
export const CHAT_PROCESS_REFRESH_MS = 10_000;

/** How many of a chat's finished processes are kept beside its running ones. */
export const CHAT_RECENT_PROCESS_LIMIT = 8;

export interface ChatProcesses {
  /** Still running, newest first. */
  running: BackgroundProcess[];
  /** Finished, newest first, at most CHAT_RECENT_PROCESS_LIMIT. */
  recent: BackgroundProcess[];
  isLoading: boolean;
}

function startedAt(process: BackgroundProcess): number {
  const t = new Date(process.start_time).getTime();
  return Number.isNaN(t) ? 0 : t;
}

/**
 * The background processes a chat's agents started — its main thread and
 * every sub-agent — read from the one shared list. A process carries the chat
 * that started it (chat_id); a Commands-panel process carries none and is
 * never a chat's.
 */
export function useChatBackgroundProcesses(chatId: string | null | undefined): ChatProcesses {
  const query = useListProcesses(ALL_PROCESSES, {
    select: backgroundProcessesOf,
    // React Query takes the shortest interval among a query's observers, so
    // only a mounted chat view with a running process makes the list poll.
    refetchInterval: (q) =>
      chatId && q.state.data?.processes.some((p) => p.chatId === chatId && p.status === BackgroundProcessStatus.RUNNING)
        ? CHAT_PROCESS_REFRESH_MS
        : false,
  });
  const data = query.data;
  return useMemo(() => {
    const mine = chatId && data ? data.filter((p) => p.chat_id === chatId) : [];
    mine.sort((a, b) => startedAt(b) - startedAt(a));
    return {
      running: mine.filter((p) => p.status === BackgroundProcessStatus.RUNNING),
      recent: mine.filter((p) => p.status !== BackgroundProcessStatus.RUNNING).slice(0, CHAT_RECENT_PROCESS_LIMIT),
      isLoading: query.isLoading,
    };
  }, [chatId, data, query.isLoading]);
}

/** One process from the shared list, or undefined while it is not in it. */
export function useBackgroundProcess(processId: string | null | undefined) {
  const query = useBackgroundProcesses();
  const process = useMemo(
    () => (processId ? query.data.find((p) => p.id === processId) : undefined),
    [processId, query.data],
  );
  return { process, isLoading: query.isLoading, isFetched: query.isFetched };
}

/** Re-read the process list now (a process this client did not see start). */
export function refreshBackgroundProcesses(): Promise<void> {
  return queryClient.invalidateQueries({ queryKey: backgroundServiceKeys.all });
}

/** A process's captured stdout/stderr, fetched while `processId` is set. */
export function useBackgroundProcessOutput(processId: string | null, daemonId?: string) {
  const query = useGetProcessOutput({ processId: processId ?? "", daemonId }, { enabled: !!processId, staleTime: 0 });
  const response = query.data;
  const data = useMemo<ProcessOutput | null>(
    () => (response ? { stdout: response.stdout, stderr: response.stderr } : null),
    [response],
  );
  return { ...query, data };
}

/** Stop a process (`mutateAsync({ processId, daemonId })`); refetches the process list. */
export const useKillBackgroundProcess = useKillProcess;

/**
 * Stop a background process on the machine it runs on. One that had already
 * exited is not a failure: the list is re-read so it says so.
 */
export function useStopBackgroundProcess() {
  const kill = useKillProcess();
  const stop = useCallback(
    async (process: Pick<BackgroundProcess, "id" | "daemon_id">) => {
      try {
        await kill.mutateAsync({ processId: process.id, daemonId: process.daemon_id });
      } catch (error) {
        if (hasErrorReason(error, "process_not_running")) {
          await refreshBackgroundProcesses();
          return;
        }
        throw error;
      }
    },
    [kill],
  );
  return { stop, isPending: kill.isPending };
}

// ── Stream ──────────────────────────────────────────────────────────────────

/** A message's fields without its type tag, so create() builds a new message from them. */
function fieldsOf({ $typeName: _typeName, ...fields }: ProtoBackgroundProcess) {
  return fields;
}

function patchAll(update: (processes: ProtoBackgroundProcess[]) => ProtoBackgroundProcess[]): void {
  queryClient.setQueryData<ListBackgroundProcessesResponse>(allProcessesKey(), (prev) =>
    prev ? { ...prev, processes: update(prev.processes) } : prev,
  );
}

/**
 * A process started, finished or failed (the stream event carries the whole
 * process). One the list already holds keeps what the event does not say
 * (its ports, unless the event names them); an unknown one is added. A list
 * that was never fetched is left alone: its first fetch is current.
 */
export function upsertBackgroundProcess(process: BackgroundProcess): void {
  patchAll((processes) => {
    const index = processes.findIndex((p) => p.id === process.id);
    if (index < 0) return [backgroundProcessToProto(process), ...processes];
    const existing = processes[index]!;
    const next = [...processes];
    next[index] = create(BackgroundProcessSchema, {
      ...fieldsOf(existing),
      status: process.status,
      exitCode: process.exit_code,
      endTime: process.end_time,
      ports: process.ports ?? existing.ports,
      daemonId: existing.daemonId ?? process.daemon_id,
      threadId: existing.threadId ?? process.thread_id,
    });
    return next;
  });
}

/** A running process opened or closed a port. */
export function patchBackgroundProcessPorts(processId: string, ports: PortInfo[] | undefined): void {
  patchAll((processes) =>
    processes.map((p) => (p.id === processId ? create(BackgroundProcessSchema, { ...fieldsOf(p), ports: ports ?? [] }) : p)),
  );
}
