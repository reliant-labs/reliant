// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import type {
  PackageCommand as ProtoPackageCommand,
  PackageProcess as ProtoPackageProcess,
  CommandsByType as ProtoCommandsByType,
} from "../gen/reliant/v1/package_commands_pb";
import {
  ListPackageCommandsRequestSchema,
  RunPackageCommandRequestSchema,
  ListPackageProcessesRequestSchema,
  GetPackageProcessRequestSchema,
  GetPackageProcessLogsRequestSchema,
  KillPackageProcessRequestSchema,
  GetCommandFavoritesRequestSchema,
  SetCommandFavoriteRequestSchema,
} from "../gen/reliant/v1/package_commands_pb";
import { PackageType as ProtoPackageType } from "../gen/reliant/v1/package_commands_pb";
import { BackgroundProcessStatus, OutputStreamType } from "../gen/reliant/v1/common_pb";

// ============================================
// Frontend Type Definitions
// ============================================

export { ProtoPackageType };
export type PackageType = "makefile" | "npm" | "taskfile";

const PACKAGE_TYPE_TO_STRING: Record<number, PackageType> = {
  [ProtoPackageType.MAKEFILE]: "makefile",
  [ProtoPackageType.NPM]: "npm",
  [ProtoPackageType.TASKFILE]: "taskfile",
};

const STRING_TO_PACKAGE_TYPE: Record<string, ProtoPackageType> = {
  "makefile": ProtoPackageType.MAKEFILE,
  "npm": ProtoPackageType.NPM,
  "taskfile": ProtoPackageType.TASKFILE,
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

export interface RunCommandRequest {
  worktree_id?: string;
  path?: string;
  command_name: string;
  package_type: PackageType;
  env?: Record<string, string>;
  working_dir?: string;  // Directory to run command from (from command.working_dir)
}

export interface RunCommandResponse {
  process_id: string;
  command: string;
  start_time: string;
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

// ============================================
// Conversion Functions: Proto -> Frontend
// ============================================

function protoCommandToFrontend(proto: ProtoPackageCommand): PackageCommand {
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

function protoProcessToFrontend(proto: ProtoPackageProcess): PackageProcess {
  return {
    id: proto.id,
    command: proto.command,
    status: proto.status,
    worktree_id: proto.worktreeId,
    working_dir: proto.workingDir,
    start_time: proto.startTime,
    end_time: proto.endTime,
    exit_code: proto.exitCode,
  };
}

function protoCommandsByTypeToFrontend(
  protoCommands: { [key: string]: ProtoCommandsByType }
): Record<PackageType, PackageCommand[]> {
  const result: Record<string, PackageCommand[]> = {};
  
  for (const [pkgType, cmdsByType] of Object.entries(protoCommands)) {
    result[pkgType] = cmdsByType.commands.map(protoCommandToFrontend);
  }
  
  return result as Record<PackageType, PackageCommand[]>;
}

// ============================================
// PackageCommands gRPC Client
// ============================================

export const packageCommandsGrpc = {
  /**
   * List available commands for a worktree or directory
   */
  async listCommands(options: { worktreeId?: string; path?: string; customDirectories?: string[] }): Promise<ListCommandsResponse> {
    const client = grpcClient.packageCommands();
    const request = create(ListPackageCommandsRequestSchema, {
      worktreeId: options.worktreeId,
      path: options.path,
      customDirectories: options.customDirectories || [],
    });
    const response = await client.listCommands(request);
    return {
      commands: protoCommandsByTypeToFrontend(response.commands),
      detected_types: response.detectedTypes as PackageType[],
    };
  },

  /**
   * Run a package command
   */
  async runCommand(request: RunCommandRequest): Promise<RunCommandResponse> {
    const client = grpcClient.packageCommands();
    const grpcRequest = create(RunPackageCommandRequestSchema, {
      worktreeId: request.worktree_id,
      path: request.path,
      commandName: request.command_name,
      packageType: STRING_TO_PACKAGE_TYPE[request.package_type] || ProtoPackageType.MAKEFILE,
      env: request.env || {},
      workingDir: request.working_dir,
    });
    const response = await client.runCommand(grpcRequest);
    return {
      process_id: response.processId,
      command: response.command,
      start_time: response.startTime,
    };
  },

  /**
   * List processes for a worktree, or all processes if no worktreeId is provided
   */
  async listProcesses(worktreeId?: string): Promise<PackageProcess[]> {
    const client = grpcClient.packageCommands();
    const request = create(ListPackageProcessesRequestSchema, {
      worktreeId: worktreeId || "",
    });
    const response = await client.listProcesses(request);
    return response.processes.map(protoProcessToFrontend);
  },

  /**
   * Get a specific process
   */
  async getProcess(processId: string): Promise<PackageProcess> {
    const client = grpcClient.packageCommands();
    const request = create(GetPackageProcessRequestSchema, {
      processId,
    });
    const response = await client.getProcess(request);
    if (!response.process) {
      throw new Error("Process not found");
    }
    return protoProcessToFrontend(response.process);
  },

  /**
   * Get process logs
   */
  async getProcessLogs(processId: string): Promise<ProcessLogsResponse> {
    const client = grpcClient.packageCommands();
    const request = create(GetPackageProcessLogsRequestSchema, {
      processId,
    });
    const response = await client.getProcessLogs(request);
    return {
      stdout: response.stdout,
      stderr: response.stderr,
      combined: response.combined.map(line => ({
        type: line.type === OutputStreamType.STDERR ? "stderr" : "stdout",
        text: line.text,
      })),
    };
  },

  /**
   * Kill a process
   */
  async killProcess(processId: string): Promise<void> {
    const client = grpcClient.packageCommands();
    const request = create(KillPackageProcessRequestSchema, {
      processId,
    });
    await client.killProcess(request);
  },

  /**
   * Get command favorites for a project
   */
  async getCommandFavorites(projectId: string): Promise<string[]> {
    const client = grpcClient.packageCommands();
    const request = create(GetCommandFavoritesRequestSchema, {
      projectId,
    });
    const response = await client.getCommandFavorites(request);
    return response.commandKeys;
  },

  /**
   * Set a command as favorite or unfavorite
   */
  async setCommandFavorite(projectId: string, commandKey: string, isFavorite: boolean): Promise<void> {
    const client = grpcClient.packageCommands();
    const request = create(SetCommandFavoriteRequestSchema, {
      projectId,
      commandKey,
      isFavorite,
    });
    await client.setCommandFavorite(request);
  },
};
