// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import type {
  BackgroundProcess as ProtoBackgroundProcess,
  PortInfo as ProtoPortInfo,
} from "../gen/reliant/v1/background_pb";
import {
  ListBackgroundProcessesRequestSchema,
  GetProcessOutputRequestSchema,
  KillProcessRequestSchema,
} from "../gen/reliant/v1/background_pb";
import { BackgroundProcessStatus } from "../gen/reliant/v1/common_pb";

// ============================================
// Frontend Type Definitions
// ============================================

export interface PortInfo {
  port: number;
  protocol: string;
  state: string;
  address: string;
}

export { BackgroundProcessStatus };

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

export interface ListProcessesFilters {
  worktree_id?: string;
  session_id?: string;
  chat_id?: string;
}

// ============================================
// Conversion Functions: Proto -> Frontend
// ============================================

function protoPortInfoToFrontend(proto: ProtoPortInfo): PortInfo {
  return {
    port: proto.port,
    protocol: proto.protocol,
    state: proto.state,
    address: proto.address,
  };
}

function protoProcessToFrontend(proto: ProtoBackgroundProcess): BackgroundProcess {
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
    ports: proto.ports?.map(protoPortInfoToFrontend),
  };
}

// ============================================
// Background gRPC Client
// ============================================

export const backgroundGrpc = {
  /**
   * List all background processes, optionally filtered
   */
  async listProcesses(filters?: ListProcessesFilters): Promise<BackgroundProcess[]> {
    const client = await grpcClient.background();
    const request = create(ListBackgroundProcessesRequestSchema, {
      worktreeId: filters?.worktree_id,
      sessionId: filters?.session_id,
      chatId: filters?.chat_id,
    });
    const response = await client.listProcesses(request);
    return response.processes.map(protoProcessToFrontend);
  },

  /**
   * Get the output (stdout/stderr) of a process
   */
  async getProcessOutput(processId: string): Promise<ProcessOutput> {
    const client = await grpcClient.background();
    const request = create(GetProcessOutputRequestSchema, {
      processId,
    });
    const response = await client.getProcessOutput(request);
    return {
      stdout: response.stdout,
      stderr: response.stderr,
    };
  },

  /**
   * Kill a background process
   */
  async killProcess(processId: string): Promise<void> {
    const client = await grpcClient.background();
    const request = create(KillProcessRequestSchema, {
      processId,
    });
    await client.killProcess(request);
  },
};
