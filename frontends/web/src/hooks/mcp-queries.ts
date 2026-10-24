// Copyright (c) 2025 Reliant Labs

/**
 * A project's MCP servers — MCPService — on the generated hooks and keys.
 *
 * The cache holds what the generated hooks fetch (ListServersResponse,
 * ListRecommendedResponse) under `mcpServiceKeys`; the settings page reads
 * the plain frontend types mapped from it. Every MCPService write is a
 * generated mutation, which invalidates `mcpServiceKeys.all`, so the page
 * refetches what the server recorded instead of patching its own copy. The
 * stream's `config_health` refetch (a server connecting, failing, or its
 * config changing on disk) invalidates the same scope (invalidateMcpQueries).
 */

import { useMemo } from "react";
import type { QueryClient } from "@tanstack/react-query";

import { ConfigScope } from "../gen/reliant/v1/common_pb";
import {
  MCPServerStatus,
  MCPService,
  type ConfigField as ProtoConfigField,
  type MCPServer as ProtoMCPServer,
  type MCPServerConfig as ProtoMCPServerConfig,
  type MCPTool as ProtoMCPTool,
  type RecommendedServer as ProtoRecommendedServer,
} from "../gen/reliant/v1/mcp_pb";
import { connectClient } from "../lib/connect";
import { mcpServiceKeys, useListRecommended, useListServers } from "./mcp-service-hooks_gen";

export { ConfigScope, MCPServerStatus };

const client = connectClient(MCPService);

// ── Frontend types ──────────────────────────────────────────────────────────

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

// ── Proto mapping ───────────────────────────────────────────────────────────

const EMPTY_CONFIG: MCPServerConfig = { command: "", type: "stdio" };

function configFromProto(proto: ProtoMCPServerConfig | undefined): MCPServerConfig {
  if (!proto) return { ...EMPTY_CONFIG };
  return {
    command: proto.command,
    args: proto.args.length > 0 ? proto.args : undefined,
    env: proto.env.length > 0 ? proto.env : undefined,
    headers: Object.keys(proto.headers ?? {}).length > 0 ? proto.headers : undefined,
    type: proto.type,
    url: proto.url || undefined,
  };
}

function toolFromProto(proto: ProtoMCPTool): MCPTool {
  return { name: proto.name, description: proto.description || undefined };
}

function serverFromProto(proto: ProtoMCPServer): MCPServer {
  return {
    name: proto.name,
    status: proto.status,
    connected: proto.connected,
    healthy: proto.healthy,
    enabled: proto.enabled,
    scope: proto.scope,
    config: configFromProto(proto.config),
    serverInfo: proto.serverInfo ? { name: proto.serverInfo.name, version: proto.serverInfo.version } : undefined,
    toolCount: proto.toolCount,
    resourcesEnabled: proto.resourcesEnabled,
    promptsEnabled: proto.promptsEnabled,
    lastError: proto.lastError || undefined,
  };
}

function configFieldFromProto(proto: ProtoConfigField): ConfigField {
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

function recommendedFromProto(proto: ProtoRecommendedServer): RecommendedServer {
  return {
    name: proto.name,
    displayName: proto.displayName,
    description: proto.description,
    category: proto.category,
    setupRequired: proto.setupRequired || undefined,
    setupInstructions: proto.setupInstructions || undefined,
    configTemplate: proto.configTemplate || undefined,
    configFields: proto.configFields.length > 0 ? proto.configFields.map(configFieldFromProto) : undefined,
    docsUrl: proto.docsUrl || undefined,
    config: configFromProto(proto.config),
    installed: proto.installed,
  };
}

/** The wire form of a server config, for InstallServer. */
export function configToProto(config: MCPServerConfig) {
  return {
    command: config.command,
    args: config.args ?? [],
    env: config.env ?? [],
    headers: config.headers ?? {},
    type: config.type,
    url: config.url,
  };
}

// ── Reads ───────────────────────────────────────────────────────────────────

/** Server status changes under the page (the stream's config_health refetch), so 30s is only the cold-cache bound. */
export const MCP_STALE_MS = 30_000;

/** A project's installed servers, with their live status. */
export function useMcpServers(projectId: string | null) {
  const query = useListServers({ projectId: projectId ?? "" }, { enabled: !!projectId, staleTime: MCP_STALE_MS });
  const servers = query.data?.servers;
  const data = useMemo(() => servers?.map(serverFromProto), [servers]);
  return { ...query, data };
}

/** The recommended servers for a project, each marked installed or not. */
export function useMcpRecommended(projectId: string | null) {
  const query = useListRecommended({ projectId: projectId ?? "" }, { enabled: !!projectId, staleTime: MCP_STALE_MS });
  const recommended = query.data?.recommended;
  const data = useMemo(() => recommended?.map(recommendedFromProto), [recommended]);
  return { ...query, data };
}

/** A project's installed servers, from the cache when fresh (the onboarding checklist's probe). */
export async function fetchMcpServers(queryClient: QueryClient, projectId: string): Promise<MCPServer[]> {
  const response = await queryClient.fetchQuery({
    queryKey: mcpServiceKeys.listServers({ projectId }),
    queryFn: () => client.listServers({ projectId }),
    staleTime: MCP_STALE_MS,
  });
  return response.servers.map(serverFromProto);
}

/**
 * The tools a server exposes, fetched when asked for (the Tools button), so
 * the list is current even when the server reconnected since.
 */
export async function fetchMcpServerTools(queryClient: QueryClient, projectId: string, name: string): Promise<MCPTool[]> {
  const response = await queryClient.fetchQuery({
    queryKey: mcpServiceKeys.getServerTools({ projectId, name }),
    queryFn: () => client.getServerTools({ projectId, name }),
    staleTime: 0,
  });
  return response.tools.map(toolFromProto);
}

// ── Stream ──────────────────────────────────────────────────────────────────

/**
 * The stream's `config_health` refetch: a server connected, failed or had its
 * config change. It carries no payload, so every MCPService read is marked
 * stale; only the mounted ones refetch.
 */
export function invalidateMcpQueries(queryClient: QueryClient): void {
  void queryClient.invalidateQueries({ queryKey: mcpServiceKeys.all });
}
