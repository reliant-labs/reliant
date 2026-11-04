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

import { useMemo } from "react";
import { create } from "@bufbuild/protobuf";

import {
  BackgroundProcessSchema,
  type BackgroundProcess as ProtoBackgroundProcess,
  type ListBackgroundProcessesResponse,
} from "../gen/reliant/v1/background_pb";
import { BackgroundProcessStatus } from "../gen/reliant/v1/common_pb";
import { queryClient } from "../lib/query-client";
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
  chat_id?: string;
  ports?: PortInfo[];
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
    ports: proto.ports.map((port) => ({ port: port.port, protocol: port.protocol, state: port.state, address: port.address })),
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
    ports: process.ports ?? [],
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

/** A process's captured stdout/stderr, fetched while `processId` is set. */
export function useBackgroundProcessOutput(processId: string | null) {
  const query = useGetProcessOutput({ processId: processId ?? "" }, { enabled: !!processId, staleTime: 0 });
  const response = query.data;
  const data = useMemo<ProcessOutput | null>(
    () => (response ? { stdout: response.stdout, stderr: response.stderr } : null),
    [response],
  );
  return { ...query, data };
}

/** Stop a process (`mutateAsync({ processId })`); refetches the process list. */
export const useKillBackgroundProcess = useKillProcess;

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
