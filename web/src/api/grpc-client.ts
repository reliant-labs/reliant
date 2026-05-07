import { createClient, ConnectError, Code } from "@connectrpc/connect";
import type { Interceptor, Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { trace, context, SpanStatusCode, SpanKind, propagation, type Span } from '@opentelemetry/api';
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
import { DaemonRegistryService } from "../gen/reliant/v1/tools_daemon_pb";
import { QuestionService } from "../gen/reliant/v1/question_pb";
import { supabase } from "../lib/supabase";
import { logger } from "../lib/logger";
import { getIsDev } from "../lib/constants";
import * as Sentry from "@sentry/react";
import { buildLocalhostUrl } from "../lib/protocol";
import {
  DEFAULT_GRPC_TIMEOUT_MS,
  FILE_OPERATION_TIMEOUT_MS,
  CHAT_OPERATION_TIMEOUT_MS,
  MCP_OPERATION_TIMEOUT_MS,
  UPLOAD_TIMEOUT_MS,
  WORKTREE_OPERATION_TIMEOUT_MS,
  OAUTH_TIMEOUT_MS,
  OAUTH_EXCHANGE_TIMEOUT_MS,
  PROVIDER_VALIDATION_TIMEOUT_MS,
} from "../lib/constants";

// Detect if running in Electron and get gRPC URL
// Returns null if config not yet available (Electron loading)
const getGRPCBaseURL = (): string | null => {
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

// ---- Daemon last-seen header ----
// Module-level state set by globalUpdatesStore to avoid circular imports.
let _daemonLastSeen: number | null = null;
/** Called by globalUpdatesStore when a DAEMON_HEARTBEAT event arrives. */
export function setDaemonLastSeen(unixSeconds: number): void {
  _daemonLastSeen = unixSeconds;
}

// Attaches x-daemon-last-seen header so the server can skip the
// IsDaemonOnline DB query when the daemon was recently seen.
const daemonLastSeenInterceptor: Interceptor = (next) => async (req) => {
  if (_daemonLastSeen !== null) {
    req.header.set("x-daemon-last-seen", String(_daemonLastSeen));
  }
  return await next(req);
};

// Auth interceptor to add JWT token to requests
const authInterceptor: Interceptor = (next) => async (req) => {
  // In dev mode, skip auth - backend will use DevUser
  if (getIsDev()) {
    logger.info("[gRPC Client] Dev mode - skipping auth:", {
      method: req.method.name,
    });
    return await next(req);
  }

  // Check for API key auth first (stored by ApiKeyLogin)
  const apiKey = localStorage.getItem('reliant-api-key');
  if (apiKey) {
    req.header.set("Authorization", `Bearer ${apiKey}`);
    logger.info("[gRPC Client] API key auth set for request:", {
      method: req.method.name,
    });
    return await next(req);
  }

  try {
    const {
      data: { session },
    } = await supabase.auth.getSession();

    if (session?.access_token) {
      req.header.set("Authorization", `Bearer ${session.access_token}`);
      logger.info("[gRPC Client] Auth token set for request:", {
        method: req.method.name,
        tokenLength: session.access_token.length,
      });
    } else {
      logger.warn("[gRPC Client] No auth token available for request:", {
        method: req.method.name,
        hasSession: !!session,
        isElectron: !!window.electronAPI,
      });
    }
  } catch (error) {
    logger.error("[gRPC Client] Error getting session in interceptor:", {
      method: req.method.name,
      error: error instanceof Error ? error.message : String(error),
    });
  }

  return await next(req);
};


// Methods that need longer timeouts (0 = no timeout, handled by streaming layer)
const LONG_TIMEOUT_METHODS: Record<string, number> = {
  // File operations - may involve large files
  "ReadFile": FILE_OPERATION_TIMEOUT_MS,
  "WriteFile": FILE_OPERATION_TIMEOUT_MS,
  "ListFiles": FILE_OPERATION_TIMEOUT_MS,
  // Chat operations that involve workflows - initial setup can take time
  "CreateChat": CHAT_OPERATION_TIMEOUT_MS,
  "SendMessage": CHAT_OPERATION_TIMEOUT_MS,
  // MCP operations - external process startup can be slow
  "StartServer": MCP_OPERATION_TIMEOUT_MS,
  "InstallServer": MCP_OPERATION_TIMEOUT_MS,
  "RestartServer": MCP_OPERATION_TIMEOUT_MS,
  "UpdateServerConfig": MCP_OPERATION_TIMEOUT_MS,
  "UninstallServer": MCP_OPERATION_TIMEOUT_MS,
  "CallTool": MCP_OPERATION_TIMEOUT_MS,
  // Attachment uploads - depends on file size
  "Upload": UPLOAD_TIMEOUT_MS,
  // Worktree operations - involve git commands that can take 10-30s
  "CreateWorktree": WORKTREE_OPERATION_TIMEOUT_MS,
  "DeleteWorktree": WORKTREE_OPERATION_TIMEOUT_MS,
  "ArchiveWorktree": WORKTREE_OPERATION_TIMEOUT_MS,
  "UnarchiveWorktree": WORKTREE_OPERATION_TIMEOUT_MS,
  "ImportWorktree": WORKTREE_OPERATION_TIMEOUT_MS,
  "DiscoverWorktrees": WORKTREE_OPERATION_TIMEOUT_MS,
  "RecreateWorktree": WORKTREE_OPERATION_TIMEOUT_MS,
  "GetWorktreeChanges": WORKTREE_OPERATION_TIMEOUT_MS,
  "GetWorktreeGitStatus": WORKTREE_OPERATION_TIMEOUT_MS,
  "GetWorktreeCommits": WORKTREE_OPERATION_TIMEOUT_MS,
  "StageFiles": WORKTREE_OPERATION_TIMEOUT_MS,
  "UnstageFiles": WORKTREE_OPERATION_TIMEOUT_MS,
  "CommitWorktree": WORKTREE_OPERATION_TIMEOUT_MS,
  "PushWorktree": WORKTREE_OPERATION_TIMEOUT_MS,
  "PullWorktree": WORKTREE_OPERATION_TIMEOUT_MS,
  "GetWorktreePR": WORKTREE_OPERATION_TIMEOUT_MS,
  "CreateWorktreePR": WORKTREE_OPERATION_TIMEOUT_MS,
  "RevertFiles": WORKTREE_OPERATION_TIMEOUT_MS,
  // OAuth flows — no timeout, user can take as long as needed (cancelled via AbortController)
  "StartOAuthFlow": OAUTH_TIMEOUT_MS,
  // OAuth token exchange - external network call
  "CompleteClaudeOAuth": OAUTH_EXCHANGE_TIMEOUT_MS,
  "CompleteCodexOAuth": OAUTH_EXCHANGE_TIMEOUT_MS,
  // Provider API key validation - external network call
  "ValidateProviderAPIKey": PROVIDER_VALIDATION_TIMEOUT_MS,
  "UpdateProviderAPIKey": PROVIDER_VALIDATION_TIMEOUT_MS,
  // Streaming methods should not have client-side timeout
  // These are server-streaming RPCs that manage their own lifecycle via AbortController
  "StreamUserUpdates": 0,
  "StreamProcessOutput": 0,
};

// Timeout interceptor to prevent requests from hanging indefinitely.
// Races the RPC against a timer since req.signal is readonly and timeoutMs
// is consumed by the transport before interceptors run.
const timeoutInterceptor: Interceptor = (next) => async (req) => {
  const methodName = req.method.name;
  const timeoutMs = LONG_TIMEOUT_METHODS[methodName] ?? DEFAULT_GRPC_TIMEOUT_MS;

  // Skip timeout for streaming methods (timeout = 0)
  if (timeoutMs === 0) {
    return next(req);
  }

  const controller = new AbortController();
  const timer = setTimeout(() => {
    controller.abort(new ConnectError(`${methodName} timed out after ${timeoutMs}ms`, Code.DeadlineExceeded));
  }, timeoutMs);

  // Abort our timer if the request's own signal fires first
  const onUpstreamAbort = () => {
    clearTimeout(timer);
    controller.abort(req.signal.reason);
  };
  if (req.signal.aborted) {
    clearTimeout(timer);
    throw ConnectError.from(req.signal.reason);
  }
  req.signal.addEventListener("abort", onUpstreamAbort);

  try {
    return await Promise.race([
      next(req),
      new Promise<never>((_, reject) => {
        controller.signal.addEventListener("abort", () => {
          reject(ConnectError.from(controller.signal.reason));
        });
      }),
    ]);
  } finally {
    clearTimeout(timer);
    req.signal.removeEventListener("abort", onUpstreamAbort);
  }
};

// OTel tracing interceptor — creates a span per RPC and injects W3C traceparent/tracestate headers
const tracingInterceptor: Interceptor = (next) => async (req) => {
  const tracer = trace.getTracer('reliant-frontend');
  const spanName = `grpc.${req.service.typeName}/${req.method.name}`;

  return tracer.startActiveSpan(spanName, { kind: SpanKind.CLIENT }, async (span: Span) => {
    try {
      // Inject W3C trace context into request headers
      const carrier: Record<string, string> = {};
      propagation.inject(context.active(), carrier);
      for (const [key, value] of Object.entries(carrier)) {
        req.header.set(key, value);
      }

      span.setAttribute('rpc.system', 'connect');
      span.setAttribute('rpc.service', req.service.typeName);
      span.setAttribute('rpc.method', req.method.name);

      const result = await next(req);
      span.setStatus({ code: SpanStatusCode.OK });
      return result;
    } catch (error) {
      span.setStatus({
        code: SpanStatusCode.ERROR,
        message: error instanceof Error ? error.message : String(error),
      });
      span.recordException(error instanceof Error ? error : new Error(String(error)));
      throw error;
    } finally {
      span.end();
    }
  });
};

// Connect error codes that are client-side / expected and should NOT be reported to Sentry.
const SENTRY_SKIP_CODES = new Set([
  Code.Canceled,
  Code.InvalidArgument,
  Code.NotFound,
  Code.AlreadyExists,
  Code.PermissionDenied,
  Code.Unauthenticated,
  Code.FailedPrecondition,
  Code.Aborted,
  Code.OutOfRange,
  Code.ResourceExhausted,
]);

// Error logging interceptor
const errorInterceptor: Interceptor = (next) => async (req) => {
  const startTime = Date.now();
  try {
    logger.info("[gRPC Client] Request starting:", {
      service: req.service.typeName,
      method: req.method.name,
      baseUrl: _currentBaseURL,
      isElectron: !!window.electronAPI,
      protocol: window.location.protocol,
      hasReliantConfig: !!window.RELIANT_CONFIG,
      configGrpcUrl: window.RELIANT_CONFIG?.grpcUrl,
    });
    const result = await next(req);
    const duration = Date.now() - startTime;
    logger.debug("[gRPC Client] Request succeeded:", {
      service: req.service.typeName,
      method: req.method.name,
      durationMs: duration,
    });
    return result;
  } catch (error) {
    const duration = Date.now() - startTime;
    // Log detailed error info
    logger.error("[gRPC Client] Request failed:", {
      service: req.service.typeName,
      method: req.method.name,
      baseUrl: _currentBaseURL,
      isElectron: !!window.electronAPI,
      protocol: window.location.protocol,
      durationMs: duration,
      error,
      errorMessage: error instanceof Error ? error.message : String(error),
      errorCode: (error as any)?.code,
      errorName: (error as any)?.name,
      errorCause: (error as any)?.cause,
    });

    // Report non-trivial errors to Sentry
    const shouldReport =
      !(error instanceof ConnectError) ||
      !SENTRY_SKIP_CODES.has(error.code);

    if (shouldReport) {
      Sentry.captureException(error, {
        tags: {
          grpc_service: req.service.typeName,
          grpc_method: req.method.name,
          grpc_code: error instanceof ConnectError ? Code[error.code] : "unknown",
        },
        extra: {
          baseUrl: _currentBaseURL,
          durationMs: duration,
        },
      });
    }

    throw error;
  }
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
    _transport = createConnectTransport({
      baseUrl: currentBaseURL,
      // Order: timeout -> auth -> error logging
      // Timeout is outermost so it applies to the full request lifecycle
      interceptors: [timeoutInterceptor, authInterceptor, daemonLastSeenInterceptor, tracingInterceptor, errorInterceptor],
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
  return createClient(DaemonRegistryService, getTransport());
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
  question: () => getQuestionClient(),
};