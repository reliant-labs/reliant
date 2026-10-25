/**
 * reliant.v1 service clients for the hand-written `api/*-grpc.ts` wrappers.
 *
 * Every client here is `connectClient(<Service>)` from `src/lib/connect.ts`,
 * the same seam forge's generated hooks call, so a wrapper and a hook for the
 * same service reach the same backend through the same interceptor chain.
 * Which backend that is — reliant's api-server for every service below,
 * StreamingService on the binary wire format — is decided there, by
 * `routeFor`, not here. This module is a set of names kept for the wrappers
 * that import them; the per-service hook waves (R4) delete it piece by piece.
 *
 * ForgeService, DaemonRegistryService and TokenService are reliant.v1 and are
 * served by RELIANT's api-server (ForgeService proxies onto the user's daemon;
 * the registry and the token authority are reliant's). None of them may route
 * at control-plane's admin-server — see the header of src/lib/connect.ts and
 * `lib/__tests__/connect.routing.test.ts`.
 */
import type { Client, Transport } from "@connectrpc/connect";
import { CatalogService } from "../gen/reliant/v1/catalog_pb";
import { ProjectService } from "../gen/reliant/v1/project_pb";
import { ChatService } from "../gen/reliant/v1/chat_pb";
import { MessageService } from "../gen/reliant/v1/message_pb";
import { SettingsService } from "../gen/reliant/v1/settings_pb";
import { MCPService } from "../gen/reliant/v1/mcp_pb";
import { WorkflowService } from "../gen/reliant/v1/workflow_pb";
import { ScenarioService } from "../gen/reliant/v1/scenario_pb";
import { FileSystemService } from "../gen/reliant/v1/filesystem_pb";
import { BackgroundService } from "../gen/reliant/v1/background_pb";
import { PackageCommandsService } from "../gen/reliant/v1/package_commands_pb";
import { StreamingService } from "../gen/reliant/v1/streaming_pb";
import { ToolCallService } from "../gen/reliant/v1/tool_call_pb";
import { DaemonRegistryService } from "../gen/reliant/v1/daemon_registry_pb";
import { TokenService } from "../gen/reliant/v1/token_pb";
import { ConnectorService } from "../gen/reliant/v1/connector_pb";
import { AccountService } from "../gen/reliant/v1/account_pb";
import { ForgeService } from "../gen/reliant/v1/forge_pb";
import { TriggerService } from "../gen/reliant/v1/trigger_pb";
import { InboxService } from "../gen/reliant/v1/inbox_pb";
import { ConnectionService } from "../gen/reliant/v1/connection_pb";
import { connectClient, isApiReady, reliantApiBaseUrl, transport } from "../lib/connect";
import { setDaemonLastSeen as _setDaemonLastSeenInTransport } from "./transport";

// globalUpdatesStore imports it from this module.
export const setDaemonLastSeen = _setDaemonLastSeenInTransport;

/** The reliant api-server's base URL, or null before it is known. */
export const getGRPCBaseURLPublic = (): string | null => reliantApiBaseUrl();

/** True once the reliant api-server's URL is known (packaged Electron learns it late). */
export const isGrpcReady = (): boolean => isApiReady();

/**
 * The app's routed transport, for code that builds its own client with
 * `createClient(Service, getTransport())`. Same transport connectClient uses:
 * the route is picked per call from the service's name.
 */
export const getTransport = (): Transport => transport;

/** StreamingService's binary route is chosen by connect.ts's routeFor. */
export const getStreamingTransport = (): Transport => transport;

export const createCatalogClient = (): Client<typeof CatalogService> => connectClient(CatalogService);
export const createProjectClient = (): Client<typeof ProjectService> => connectClient(ProjectService);
export const createChatClient = (): Client<typeof ChatService> => connectClient(ChatService);
export const createMessageClient = (): Client<typeof MessageService> => connectClient(MessageService);
export const createSettingsClient = (): Client<typeof SettingsService> => connectClient(SettingsService);
export const createMCPClient = (): Client<typeof MCPService> => connectClient(MCPService);
export const createWorkflowClient = (): Client<typeof WorkflowService> => connectClient(WorkflowService);
export const createFileSystemClient = (): Client<typeof FileSystemService> =>
  connectClient(FileSystemService);
export const createDaemonFileSystemClient = createFileSystemClient;
export const createBackgroundClient = (): Client<typeof BackgroundService> =>
  connectClient(BackgroundService);
export const createPackageCommandsClient = (): Client<typeof PackageCommandsService> =>
  connectClient(PackageCommandsService);
export const createStreamingClient = (): Client<typeof StreamingService> =>
  connectClient(StreamingService);
export const createToolCallClient = (): Client<typeof ToolCallService> => connectClient(ToolCallService);
export const createScenarioClient = (): Client<typeof ScenarioService> => connectClient(ScenarioService);
export const createAccountClient = (): Client<typeof AccountService> => connectClient(AccountService);
export const createForgeClient = (): Client<typeof ForgeService> => connectClient(ForgeService);
export const createDaemonRegistryClient = (): Client<typeof DaemonRegistryService> =>
  connectClient(DaemonRegistryService);
export const createTokenClient = (): Client<typeof TokenService> => connectClient(TokenService);
export const createConnectorClient = (): Client<typeof ConnectorService> =>
  connectClient(ConnectorService);
export const createTriggerClient = (): Client<typeof TriggerService> => connectClient(TriggerService);
export const createConnectionClient = (): Client<typeof ConnectionService> =>
  connectClient(ConnectionService);
export const createInboxClient = (): Client<typeof InboxService> => connectClient(InboxService);

// connectClient memoizes per service, so the "get" and "create" spellings
// return the same client.
export const getCatalogClient = createCatalogClient;
export const getProjectClient = createProjectClient;
export const getChatClient = createChatClient;
export const getMessageClient = createMessageClient;
export const getSettingsClient = createSettingsClient;
export const getMCPClient = createMCPClient;
export const getWorkflowClient = createWorkflowClient;
export const getFileSystemClient = createFileSystemClient;
export const getDaemonFileSystemClient = createDaemonFileSystemClient;
export const getBackgroundClient = createBackgroundClient;
export const getPackageCommandsClient = createPackageCommandsClient;
export const getStreamingClient = createStreamingClient;
export const getToolCallClient = createToolCallClient;
export const getScenarioClient = createScenarioClient;
export const getDaemonRegistryClient = createDaemonRegistryClient;
export const getTokenClient = createTokenClient;
export const getConnectorClient = createConnectorClient;
export const getTriggerClient = createTriggerClient;
export const getInboxClient = createInboxClient;
export const getConnectionClient = createConnectionClient;

export const grpcClient = {
  catalog: getCatalogClient,
  project: getProjectClient,
  chat: getChatClient,
  message: getMessageClient,
  settings: getSettingsClient,
  mcp: getMCPClient,
  workflow: getWorkflowClient,
  filesystem: getFileSystemClient,
  daemonFilesystem: getDaemonFileSystemClient,
  background: getBackgroundClient,
  packageCommands: getPackageCommandsClient,
  streaming: getStreamingClient,
  toolCall: getToolCallClient,
  scenario: getScenarioClient,
  daemonRegistry: getDaemonRegistryClient,
  token: getTokenClient,
  connector: getConnectorClient,
  trigger: getTriggerClient,
  inbox: getInboxClient,
  connection: getConnectionClient,
};
