import { createClient } from "@connectrpc/connect";
import type { Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { SystemService } from "../gen/reliant/v1/system_pb";
import { PlanService } from "../gen/reliant/v1/plan_pb";
import { TaskService } from "../gen/reliant/v1/task_pb";
import { CatalogService } from "../gen/reliant/v1/catalog_pb";
import { ProjectService } from "../gen/reliant/v1/project_pb";
import { RepoService } from "../gen/reliant/v1/repo_pb";
import { WorktreeService } from "../gen/reliant/v1/worktree_pb";
import { ApprovalService } from "../gen/reliant/v1/approval_pb";
import { ChatService } from "../gen/reliant/v1/chat_pb";
import { MessageService } from "../gen/reliant/v1/message_pb";
import { SettingsService } from "../gen/reliant/v1/settings_pb";
import { MCPService } from "../gen/reliant/v1/mcp_pb";
import { WorkflowService, ScenarioService } from "../gen/reliant/v1/workflow_pb";
import { FileSystemService } from "../gen/reliant/v1/filesystem_pb";
import { BackgroundService } from "../gen/reliant/v1/background_pb";
import { PackageCommandsService } from "../gen/reliant/v1/package_commands_pb";
import { StreamingService } from "../gen/reliant/v1/streaming_pb";
import { TerminalService } from "../gen/reliant/v1/terminal_pb";
import { AttachmentService } from "../gen/reliant/v1/attachment_pb";
import { ToolCallService } from "../gen/reliant/v1/tool_call_pb";
import { PresetService } from "../gen/reliant/v1/preset_pb";
import { DaemonRegistryService } from "../gen/reliant/v1/daemon_registry_pb";
import { DaemonTokenService } from "../gen/reliant/v1/daemon_token_pb";
import { QuestionService } from "../gen/reliant/v1/question_pb";
import { logger } from "../lib/logger";
import { buildLocalhostUrl, useSameOriginTransport } from "../lib/protocol";
import {
  buildInterceptors,
  setCurrentBaseURL,
  setDaemonLastSeen as _setDaemonLastSeenInTransport,
} from "./transport";

// Re-export setDaemonLastSeen for backwards compatibility — globalUpdatesStore
// imports it from this module today.
export const setDaemonLastSeen = _setDaemonLastSeenInTransport;

// Detect if running in Electron and get gRPC URL
// Returns null if config not yet available (Electron loading)
const getGRPCBaseURL = (): string | null => {
  // Same-origin (Vite-proxy) path — see useSameOriginTransport. Whenever the
  // renderer is served over http(s) (web-dev AND electron-dev), reliant.v1.*
  // RPCs go to the document origin and Vite's `/reliant.v1.*` proxy forwards
  // them to reliant-api. This is first-party ⇒ ZERO CORS, and it short-circuits
  // BEFORE RELIANT_CONFIG.grpcUrl / the absolute VITE_* fallbacks below so
  // electron-dev never dials a cross-origin backend port. Packaged Electron
  // (file://) falls through to the daemon URL.
  if (useSameOriginTransport()) {
    return window.location.origin;
  }

  // Check if running in Electron with config available
  if (
    typeof window !== "undefined" &&
    window.RELIANT_CONFIG?.isElectron
  ) {
    if (window.RELIANT_CONFIG?.grpcUrl) {
      return window.RELIANT_CONFIG.grpcUrl;
    }
  }

  // If we're in a file:// protocol (Electron but config not loaded yet),
  // we need to wait for the config - return null to indicate not ready
  if (typeof window !== "undefined" && window.location.protocol === "file:") {
    logger.warn(
      "[gRPC Client] Electron detected but backend config not yet available"
    );
    return null;
  }

  // Use port from env, protocol based on VITE_DISABLE_TLS
  const grpcPort = import.meta.env.VITE_GRPC_PORT || "9090";
  const fallbackUrl =
    import.meta.env.VITE_GRPC_URL ||
    import.meta.env.VITE_API_URL ||
    buildLocalhostUrl(grpcPort);
  logger.info("[gRPC Client] Using direct gRPC connection:", {
    fallbackUrl,
    grpcPort,
  });
  return fallbackUrl;
};

// Create the Connect transport
let _transport: ReturnType<typeof createConnectTransport> | null = null;
let _currentBaseURL: string | null = null;

// Clear all cached clients (called when transport changes)
const clearClientCache = () => {
  _systemClient = null;
  _planClient = null;
  _taskClient = null;
  _catalogClient = null;
  _projectClient = null;
  _repoClient = null;
  _worktreeClient = null;
  _approvalClient = null;
  _chatClient = null;
  _messageClient = null;
  _settingsClient = null;
  _mcpClient = null;
  _workflowClient = null;
  _filesystemClient = null;
  _backgroundClient = null;
  _packageCommandsClient = null;
  _streamingClient = null;
  _terminalClient = null;
  _attachmentClient = null;
  _toolCallClient = null;
  _presetClient = null;
  _scenarioClient = null;
  _daemonRegistryClient = null;
  _questionClient = null;
};

export const getGRPCBaseURLPublic = (): string | null => getGRPCBaseURL();

// Daemon registry/token RPCs are owned by the control-plane admin-server in
// cloud mode (it hosts the compat adapter that translates reliant.v1 →
// controlplane.v1). When VITE_CONTROL_PLANE_API_URL is set, daemon-registry
// clients use this transport so they see cloud-managed daemons; otherwise
// they fall through to the regular reliant api-server transport (local /
// self-hosted daemons).
let _controlPlaneTransport: ReturnType<typeof createConnectTransport> | null = null;
export const getControlPlaneTransport = () => {
  // Same-origin (Vite-proxy) path — see useSameOriginTransport. When the
  // renderer is served over http(s) (web-dev AND electron-dev), return null so
  // DaemonRegistry/DaemonToken fall through to the same-origin getTransport().
  // Their RPCs are `reliant.v1.*` paths, so the Vite `/reliant.v1.*` proxy
  // forwards them to reliant-api (which serves DaemonRegistryService against the
  // shared dev DB) — first-party, ZERO CORS, no absolute admin-server port.
  // Only packaged Electron (file://) needs the absolute control-plane URL to
  // reach the hosted admin-server for cloud-managed daemons.
  if (useSameOriginTransport()) return null;

  const cpURL = import.meta.env.VITE_CONTROL_PLANE_API_URL;
  if (!cpURL) return null;
  if (!_controlPlaneTransport) {
    // interceptors via buildInterceptors — see api/transport.ts
    _controlPlaneTransport = createConnectTransport({
      baseUrl: cpURL,
      interceptors: buildInterceptors({ withAuth: true }),
      useBinaryFormat: false,
    });
  }
  return _controlPlaneTransport;
};

export const getTransport = () => {
  const currentBaseURL = getGRPCBaseURL();

  // If base URL is not available yet (Electron loading), throw error
  if (currentBaseURL === null) {
    logger.error("[gRPC Client] gRPC client not ready - no backend URL available");
    throw new Error("gRPC client not ready - waiting for backend configuration");
  }

  // Recreate transport if URL changed
  if (!_transport || _currentBaseURL !== currentBaseURL) {
    logger.info("[gRPC Client] Creating transport", { 
      baseUrl: currentBaseURL,
      previousUrl: _currentBaseURL,
      isElectron: typeof window !== "undefined" && window.RELIANT_CONFIG?.isElectron,
      hasGrpcUrl: typeof window !== "undefined" && !!window.RELIANT_CONFIG?.grpcUrl,
      grpcUrl: typeof window !== "undefined" && window.RELIANT_CONFIG?.grpcUrl,
    });
    
    // Clear cached clients since they hold the old transport
    if (_currentBaseURL !== null) {
      logger.info("[gRPC Client] URL changed, clearing client cache");
      clearClientCache();
    }
    
    _currentBaseURL = currentBaseURL;
    // Mirror the URL into transport.ts so errorInterceptor can include it
    // in Sentry/log context.
    setCurrentBaseURL(currentBaseURL);
    // interceptors via buildInterceptors — see api/transport.ts
    _transport = createConnectTransport({
      baseUrl: currentBaseURL,
      interceptors: buildInterceptors({ withAuth: true }),
      // Use JSON for easier debugging during migration
      // Can switch to binary later for performance
      useBinaryFormat: false,
    });
  }

  return _transport;
};

// Check if gRPC client is ready (config available)
export const isGrpcReady = (): boolean => {
  return getGRPCBaseURL() !== null;
};

// Create typed clients for each service
export const createSystemClient = (): Client<typeof SystemService> => {
  return createClient(SystemService, getTransport());
};

// Create typed clients for Plans and Tasks
export const createPlanClient = (): Client<typeof PlanService> => {
  return createClient(PlanService, getTransport());
};

export const createTaskClient = (): Client<typeof TaskService> => {
  return createClient(TaskService, getTransport());
};

export const createCatalogClient = (): Client<typeof CatalogService> => {
  return createClient(CatalogService, getTransport());
};


export const createProjectClient = (): Client<typeof ProjectService> => {
  return createClient(ProjectService, getTransport());
};

export const createWorktreeClient = (): Client<typeof WorktreeService> => {
  return createClient(WorktreeService, getTransport());
};

export const createRepoClient = (): Client<typeof RepoService> => {
  return createClient(RepoService, getTransport());
};

export const createApprovalClient = (): Client<typeof ApprovalService> => {
  return createClient(ApprovalService, getTransport());
};

export const createChatClient = (): Client<typeof ChatService> => {
  return createClient(ChatService, getTransport());
};

export const createMessageClient = (): Client<typeof MessageService> => {
  return createClient(MessageService, getTransport());
};

export const createSettingsClient = (): Client<typeof SettingsService> => {
  return createClient(SettingsService, getTransport());
};

export const createMCPClient = (): Client<typeof MCPService> => {
  return createClient(MCPService, getTransport());
};

export const createWorkflowClient = (): Client<typeof WorkflowService> => {
  return createClient(WorkflowService, getTransport());
};

export const createFileSystemClient = (): Client<typeof FileSystemService> => {
  return createClient(FileSystemService, getTransport());
};

export const createDaemonFileSystemClient = (): Client<typeof FileSystemService> => {
  return createClient(FileSystemService, getTransport());
};

export const createBackgroundClient = (): Client<typeof BackgroundService> => {
  return createClient(BackgroundService, getTransport());
};

export const createPackageCommandsClient = (): Client<typeof PackageCommandsService> => {
  return createClient(PackageCommandsService, getTransport());
};

export const createStreamingClient = (): Client<typeof StreamingService> => {
  return createClient(StreamingService, getTransport());
};

export const createTerminalClient = (): Client<typeof TerminalService> => {
  return createClient(TerminalService, getTransport());
};

export const createAttachmentClient = (): Client<typeof AttachmentService> => {
  return createClient(AttachmentService, getTransport());
};

export const createToolCallClient = (): Client<typeof ToolCallService> => {
  return createClient(ToolCallService, getTransport());
};

export const createPresetClient = (): Client<typeof PresetService> => {
  return createClient(PresetService, getTransport());
};

export const createScenarioClient = (): Client<typeof ScenarioService> => {
  return createClient(ScenarioService, getTransport());
};

export const createDaemonRegistryClient = (): Client<typeof DaemonRegistryService> => {
  return createClient(DaemonRegistryService, getControlPlaneTransport() ?? getTransport());
};

export const createDaemonTokenClient = (): Client<typeof DaemonTokenService> => {
  return createClient(DaemonTokenService, getControlPlaneTransport() ?? getTransport());
};

export const createQuestionClient = (): Client<typeof QuestionService> => {
  return createClient(QuestionService, getTransport());
};

// Singleton instances (lazy-initialized)
let _systemClient: Client<typeof SystemService> | null = null;
let _planClient: Client<typeof PlanService> | null = null;
let _taskClient: Client<typeof TaskService> | null = null;
let _catalogClient: Client<typeof CatalogService> | null = null;
let _projectClient: Client<typeof ProjectService> | null = null;
let _repoClient: Client<typeof RepoService> | null = null;
let _worktreeClient: Client<typeof WorktreeService> | null = null;
let _approvalClient: Client<typeof ApprovalService> | null = null;
let _chatClient: Client<typeof ChatService> | null = null;
let _messageClient: Client<typeof MessageService> | null = null;
let _settingsClient: Client<typeof SettingsService> | null = null;
let _mcpClient: Client<typeof MCPService> | null = null;
let _workflowClient: Client<typeof WorkflowService> | null = null;
let _filesystemClient: Client<typeof FileSystemService> | null = null;
let _backgroundClient: Client<typeof BackgroundService> | null = null;
let _packageCommandsClient: Client<typeof PackageCommandsService> | null = null;
let _streamingClient: Client<typeof StreamingService> | null = null;
let _terminalClient: Client<typeof TerminalService> | null = null;
let _attachmentClient: Client<typeof AttachmentService> | null = null;
let _toolCallClient: Client<typeof ToolCallService> | null = null;
let _presetClient: Client<typeof PresetService> | null = null;
let _scenarioClient: Client<typeof ScenarioService> | null = null;
let _daemonRegistryClient: Client<typeof DaemonRegistryService> | null = null;
let _daemonTokenClient: Client<typeof DaemonTokenService> | null = null;
let _questionClient: Client<typeof QuestionService> | null = null;

export const getSystemClient = (): Client<typeof SystemService> => {
  if (!_systemClient) {
    _systemClient = createSystemClient();
  }
  return _systemClient;
};

export const getPlanClient = (): Client<typeof PlanService> => {
  if (!_planClient) {
    _planClient = createPlanClient();
  }
  return _planClient;
};

export const getTaskClient = (): Client<typeof TaskService> => {
  if (!_taskClient) {
    _taskClient = createTaskClient();
  }
  return _taskClient;
};

export const getCatalogClient = (): Client<typeof CatalogService> => {
  if (!_catalogClient) {
    _catalogClient = createCatalogClient();
  }
  return _catalogClient;
};


export const getProjectClient = (): Client<typeof ProjectService> => {
  if (!_projectClient) {
    _projectClient = createProjectClient();
  }
  return _projectClient;
};

export const getWorktreeClient = (): Client<typeof WorktreeService> => {
  if (!_worktreeClient) {
    _worktreeClient = createWorktreeClient();
  }
  return _worktreeClient;
};

export const getRepoClient = (): Client<typeof RepoService> => {
  if (!_repoClient) {
    _repoClient = createRepoClient();
  }
  return _repoClient;
};

export const getApprovalClient = (): Client<typeof ApprovalService> => {
  if (!_approvalClient) {
    _approvalClient = createApprovalClient();
  }
  return _approvalClient;
};


export const getChatClient = (): Client<typeof ChatService> => {
  if (!_chatClient) {
    _chatClient = createChatClient();
  }
  return _chatClient;
};

export const getMessageClient = (): Client<typeof MessageService> => {
  if (!_messageClient) {
    _messageClient = createMessageClient();
  }
  return _messageClient;
};

export const getSettingsClient = (): Client<typeof SettingsService> => {
  if (!_settingsClient) {
    _settingsClient = createSettingsClient();
  }
  return _settingsClient;
};

export const getMCPClient = (): Client<typeof MCPService> => {
  if (!_mcpClient) {
    _mcpClient = createMCPClient();
  }
  return _mcpClient;
};

export const getWorkflowClient = (): Client<typeof WorkflowService> => {
  if (!_workflowClient) {
    _workflowClient = createWorkflowClient();
  }
  return _workflowClient;
};

export const getFileSystemClient = (): Client<typeof FileSystemService> => {
  if (!_filesystemClient) {
    _filesystemClient = createFileSystemClient();
  }
  return _filesystemClient;
};

export const getDaemonFileSystemClient = (): Client<typeof FileSystemService> => {
  if (!_filesystemClient) {
    _filesystemClient = createDaemonFileSystemClient();
  }
  return _filesystemClient;
};

export const getBackgroundClient = (): Client<typeof BackgroundService> => {
  if (!_backgroundClient) {
    _backgroundClient = createBackgroundClient();
  }
  return _backgroundClient;
};

export const getPackageCommandsClient = (): Client<typeof PackageCommandsService> => {
  if (!_packageCommandsClient) {
    _packageCommandsClient = createPackageCommandsClient();
  }
  return _packageCommandsClient;
};

export const getStreamingClient = (): Client<typeof StreamingService> => {
  if (!_streamingClient) {
    _streamingClient = createStreamingClient();
  }
  return _streamingClient;
};

export const getTerminalClient = (): Client<typeof TerminalService> => {
  if (!_terminalClient) {
    _terminalClient = createTerminalClient();
  }
  return _terminalClient;
};

export const getAttachmentClient = (): Client<typeof AttachmentService> => {
  if (!_attachmentClient) {
    _attachmentClient = createAttachmentClient();
  }
  return _attachmentClient;
};

export const getToolCallClient = (): Client<typeof ToolCallService> => {
  if (!_toolCallClient) {
    _toolCallClient = createToolCallClient();
  }
  return _toolCallClient;
};

export const getPresetClient = (): Client<typeof PresetService> => {
  if (!_presetClient) {
    _presetClient = createPresetClient();
  }
  return _presetClient;
};

export const getScenarioClient = (): Client<typeof ScenarioService> => {
  if (!_scenarioClient) {
    _scenarioClient = createScenarioClient();
  }
  return _scenarioClient;
};

export const getDaemonRegistryClient = (): Client<typeof DaemonRegistryService> => {
  if (!_daemonRegistryClient) {
    _daemonRegistryClient = createDaemonRegistryClient();
  }
  return _daemonRegistryClient;
};

export const getDaemonTokenClient = (): Client<typeof DaemonTokenService> => {
  if (!_daemonTokenClient) {
    _daemonTokenClient = createDaemonTokenClient();
  }
  return _daemonTokenClient;
};

export const getQuestionClient = (): Client<typeof QuestionService> => {
  if (!_questionClient) {
    _questionClient = createQuestionClient();
  }
  return _questionClient;
};

// Export for convenience
export const grpcClient = {
  system: () => getSystemClient(),
  plan: () => getPlanClient(),
  task: () => getTaskClient(),
  catalog: () => getCatalogClient(),
  project: () => getProjectClient(),
  repo: () => getRepoClient(),
  worktree: () => getWorktreeClient(),
  approval: () => getApprovalClient(),
  chat: () => getChatClient(),
  message: () => getMessageClient(),
  settings: () => getSettingsClient(),
  mcp: () => getMCPClient(),
  workflow: () => getWorkflowClient(),
  filesystem: () => getFileSystemClient(),
  daemonFilesystem: () => getDaemonFileSystemClient(),
  background: () => getBackgroundClient(),
  packageCommands: () => getPackageCommandsClient(),
  streaming: () => getStreamingClient(),
  terminal: () => getTerminalClient(),
  attachment: () => getAttachmentClient(),
  toolCall: () => getToolCallClient(),
  preset: () => getPresetClient(),
  scenario: () => getScenarioClient(),
  daemonRegistry: () => getDaemonRegistryClient(),
  daemonToken: () => getDaemonTokenClient(),
  question: () => getQuestionClient(),
};