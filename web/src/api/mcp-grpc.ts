// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import type {
  MCPServer as ProtoMCPServer,
  MCPServerConfig as ProtoMCPServerConfig,
  MCPServerInfo as ProtoMCPServerInfo,
  MCPTool as ProtoMCPTool,
  ConfigField as ProtoConfigField,
  RecommendedServer as ProtoRecommendedServer,
} from "../gen/reliant/v1/mcp_pb";
import { MCPServerStatus } from "../gen/reliant/v1/mcp_pb";
import {
  ListServersRequestSchema,
  GetServerRequestSchema,
  GetServerToolsRequestSchema,
  InstallServerRequestSchema,
  UninstallServerRequestSchema,
  RestartServerRequestSchema,
  ListRecommendedRequestSchema,
  UpdateServerConfigRequestSchema,
  SetServerEnabledRequestSchema,
  MoveServerScopeRequestSchema,
  MCPServerConfigSchema,
} from "../gen/reliant/v1/mcp_pb";
import { ConfigScope } from "../gen/reliant/v1/common_pb";

// ============================================
// Frontend Type Definitions
// ============================================

export interface MCPServerConfig {
  command: string;
  args?: string[];
  env?: string[];
  headers?: Record<string, string>;
  type: string;
  url?: string;
}

export interface MCPServerInfo {
  name: string;
  version: string;
}

export interface MCPTool {
  name: string;
  description?: string;
}

export { MCPServerStatus };

export interface MCPServer {
  name: string;
  status: MCPServerStatus;
  connected: boolean;
  healthy: boolean;
  enabled: boolean;
  scope: ConfigScope;
  config: MCPServerConfig;
  serverInfo?: MCPServerInfo;
  toolCount: number;
  resourcesEnabled: boolean;
  promptsEnabled: boolean;
  lastError?: string;
}

export interface ConfigField {
  key: string;
  label: string;
  type: string;
  required: boolean;
  placeholder?: string;
  helpText?: string;
  validationRegex?: string;
  validationMessage?: string;
}

export interface RecommendedServer {
  name: string;
  displayName: string;
  description: string;
  category: string;
  setupRequired?: boolean;
  setupInstructions?: string;
  configTemplate?: string;
  configFields?: ConfigField[];
  docsUrl?: string;
  config: MCPServerConfig;
  installed: boolean;
}

// ============================================
// Conversion Functions
// ============================================

function protoConfigToFrontend(proto: ProtoMCPServerConfig): MCPServerConfig {
  return {
    command: proto.command,
    args: proto.args.length > 0 ? proto.args : undefined,
    env: proto.env.length > 0 ? proto.env : undefined,
    headers:
      Object.keys(proto.headers ?? {}).length > 0 ? proto.headers : undefined,
    type: proto.type,
    url: proto.url || undefined,
  };
}

function protoServerInfoToFrontend(proto: ProtoMCPServerInfo): MCPServerInfo {
  return {
    name: proto.name,
    version: proto.version,
  };
}

function protoToolToFrontend(proto: ProtoMCPTool): MCPTool {
  return {
    name: proto.name,
    description: proto.description || undefined,
  };
}

function protoServerToFrontend(proto: ProtoMCPServer): MCPServer {
  return {
    name: proto.name,
    status: proto.status,
    connected: proto.connected,
    healthy: proto.healthy,
    enabled: proto.enabled,
    scope: proto.scope,
    config: proto.config
      ? protoConfigToFrontend(proto.config)
      : {
          command: "",
          type: "stdio",
        },
    serverInfo: proto.serverInfo
      ? protoServerInfoToFrontend(proto.serverInfo)
      : undefined,
    toolCount: proto.toolCount,
    resourcesEnabled: proto.resourcesEnabled,
    promptsEnabled: proto.promptsEnabled,
    lastError: proto.lastError || undefined,
  };
}

function protoConfigFieldToFrontend(proto: ProtoConfigField): ConfigField {
  return {
    key: proto.key,
    label: proto.label,
    type: proto.type,
    required: proto.required,
    placeholder: proto.placeholder || undefined,
    helpText: proto.helpText || undefined,
    validationRegex: proto.validationRegex || undefined,
    validationMessage: proto.validationMessage || undefined,
  };
}

function protoRecommendedToFrontend(
  proto: ProtoRecommendedServer,
): RecommendedServer {
  return {
    name: proto.name,
    displayName: proto.displayName,
    description: proto.description,
    category: proto.category,
    setupRequired: proto.setupRequired || undefined,
    setupInstructions: proto.setupInstructions || undefined,
    configTemplate: proto.configTemplate || undefined,
    configFields:
      proto.configFields.length > 0
        ? proto.configFields.map(protoConfigFieldToFrontend)
        : undefined,
    docsUrl: proto.docsUrl || undefined,
    config: proto.config
      ? protoConfigToFrontend(proto.config)
      : {
          command: "",
          type: "stdio",
        },
    installed: proto.installed,
  };
}

// ============================================
// MCP gRPC Client
// ============================================

export const mcpGrpc = {
  /**
   * List all configured MCP servers with their status
   */
  async listServers(projectId: string): Promise<{
    servers: MCPServer[];
    total: number;
  }> {
    const client = grpcClient.mcp();
    const request = create(ListServersRequestSchema, { projectId });
    const response = await client.listServers(request);
    return {
      servers: response.servers.map(protoServerToFrontend),
      total: response.total,
    };
  },

  /**
   * Get detailed information about a specific MCP server
   */
  async getServer(projectId: string, name: string): Promise<MCPServer> {
    const client = grpcClient.mcp();
    const request = create(GetServerRequestSchema, { projectId, name });
    const response = await client.getServer(request);
    if (!response.server) {
      throw new Error("Server not found");
    }
    return protoServerToFrontend(response.server);
  },

  /**
   * Get tools exposed by an MCP server.
   */
  async getServerTools(
    projectId: string,
    name: string,
  ): Promise<{ tools: MCPTool[]; total: number }> {
    const client = grpcClient.mcp();
    const request = create(GetServerToolsRequestSchema, { projectId, name });
    const response = await client.getServerTools(request);
    return {
      tools: response.tools.map(protoToolToFrontend),
      total: response.total,
    };
  },

  /**
   * Install a new MCP server
   * @param scope - Where to save the configuration (default: PROJECT)
   */
  async installServer(
    projectId: string,
    name: string,
    config: MCPServerConfig,
    scope: ConfigScope = ConfigScope.PROJECT,
  ): Promise<{ success: boolean; message: string; name: string }> {
    const client = grpcClient.mcp();
    const protoConfig = create(MCPServerConfigSchema, {
      command: config.command,
      args: config.args || [],
      env: config.env || [],
      headers: config.headers || {},
      type: config.type,
      url: config.url,
    });
    const request = create(InstallServerRequestSchema, {
      projectId,
      name,
      config: protoConfig,
      scope,
    });
    const response = await client.installServer(request);
    return {
      success: response.success,
      message: response.message,
      name: response.name,
    };
  },

  /**
   * Uninstall an MCP server
   */
  async uninstallServer(
    projectId: string,
    name: string,
  ): Promise<{ success: boolean; message: string; name: string }> {
    const client = grpcClient.mcp();
    const request = create(UninstallServerRequestSchema, { projectId, name });
    const response = await client.uninstallServer(request);
    return {
      success: response.success,
      message: response.message,
      name: response.name,
    };
  },

  /**
   * Restart an MCP server
   */
  async restartServer(
    projectId: string,
    name: string,
  ): Promise<{ success: boolean; message: string; name: string }> {
    const client = grpcClient.mcp();
    const request = create(RestartServerRequestSchema, { projectId, name });
    const response = await client.restartServer(request);
    return {
      success: response.success,
      message: response.message,
      name: response.name,
    };
  },

  /**
   * List recommended MCP servers
   */
  async listRecommended(projectId: string): Promise<{
    recommended: RecommendedServer[];
    total: number;
  }> {
    const client = grpcClient.mcp();
    const request = create(ListRecommendedRequestSchema, { projectId });
    const response = await client.listRecommended(request);
    return {
      recommended: response.recommended.map(protoRecommendedToFrontend),
      total: response.total,
    };
  },

  /**
   * Update the configuration of an MCP server
   */
  async updateServerConfig(
    projectId: string,
    name: string,
    env: Record<string, string>,
  ): Promise<{ success: boolean; message: string }> {
    const client = grpcClient.mcp();
    const request = create(UpdateServerConfigRequestSchema, {
      projectId,
      name,
      env,
    });
    const response = await client.updateServerConfig(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  /**
   * Enable or disable an installed MCP server without uninstalling.
   */
  async setServerEnabled(
    projectId: string,
    name: string,
    enabled: boolean,
  ): Promise<{
    success: boolean;
    message: string;
    name: string;
    enabled: boolean;
  }> {
    const client = grpcClient.mcp();
    const request = create(SetServerEnabledRequestSchema, {
      projectId,
      name,
      enabled,
    });
    const response = await client.setServerEnabled(request);
    return {
      success: response.success,
      message: response.message,
      name: response.name,
      enabled: response.enabled,
    };
  },

  /**
   * Move where an MCP server config is stored (global/project/project-local).
   */
  async moveServerScope(
    projectId: string,
    name: string,
    scope: ConfigScope,
  ): Promise<{
    success: boolean;
    message: string;
    name: string;
    scope: ConfigScope;
  }> {
    const client = grpcClient.mcp();
    const request = create(MoveServerScopeRequestSchema, {
      projectId,
      name,
      scope,
    });
    const response = await client.moveServerScope(request);
    return {
      success: response.success,
      message: response.message,
      name: response.name,
      scope: response.scope,
    };
  },
};

// Re-export ConfigScope for convenience
export { ConfigScope };
