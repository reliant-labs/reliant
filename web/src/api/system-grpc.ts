/**
 * System API using gRPC/Connect
 * This replaces the HTTP REST endpoints for system operations
 * 
 * NOTE: DevAuth methods are in grpc-unauth.ts to avoid circular dependencies
 * (supabase.ts needs devAuth, grpc-client.ts needs supabase for auth interceptor)
 */

import { grpcClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import { 
  HealthRequestSchema, 
  InfoRequestSchema, 
  ReadyRequestSchema, 
  VersionRequestSchema,
} from "../gen/reliant/v1/system_pb";

export const systemGrpc = {
  /**
   * Health check - returns service health status
   */
  async health() {
    const client = grpcClient.system();
    const response = await client.health(create(HealthRequestSchema));
    return {
      status: response.status,
      version: response.version,
    };
  },

  /**
   * Readiness check - returns service readiness
   */
  async ready() {
    const client = grpcClient.system();
    const response = await client.ready(create(ReadyRequestSchema));
    return {
      status: response.status,
      timestamp: Number(response.timestamp),
    };
  },

  /**
   * Get API info - returns version, build info, and worktree
   */
  async info() {
    const client = grpcClient.system();
    const response = await client.info(create(InfoRequestSchema));
    return {
      version: response.version,
      commit: response.commit,
      date: response.date,
      branch: response.branch,
      api: response.api,
      timestamp: Number(response.timestamp),
      worktree_name: response.worktreeName,
    };
  },

  /**
   * Get version info - returns detailed version information
   */
  async version() {
    const client = grpcClient.system();
    const response = await client.version(create(VersionRequestSchema));
    return {
      version: response.version,
      commit: response.commit,
      date: response.date,
      branch: response.branch,
    };
  },
};
