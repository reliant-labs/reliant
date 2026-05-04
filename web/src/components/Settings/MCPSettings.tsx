import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import * as Sentry from "@sentry/react";
import {
  AlertCircle,
  CheckCircle2,
  Compass,
  Plus,
  RefreshCw,
  Server,
} from "lucide-react";
import {
  mcpGrpc,
  type MCPServer,
  type MCPTool,
  type MCPServerConfig,
  type RecommendedServer,
  ConfigScope,
} from "../../api/mcp-grpc";
import { useProjectStore } from "../../store/projectStore";
import { usePreferencesStore } from "../../store/preferencesStore";
import { toast } from "sonner";
import { Button } from "../ui/Button";
import { MCPCustomServerModal } from "./MCPCustomServerModal";
import { MCPTabs } from "./MCPTabs";
import { MCPInstalledTab } from "./MCPInstalledTab";
import { MCPDiscoverTab } from "./MCPDiscoverTab";
import { MCPSetupWizard } from "./MCPSetupWizard";
import { Modal } from "../ui/Modal";
import { subscribeToRefetch } from "../../store/refetchStore";

type MainTab = "installed" | "discover";
type WizardMode = "install" | "edit";

type MCPSettingsCacheEntry = {
  installedServers: MCPServer[];
  recommendedServers: RecommendedServer[];
  updatedAt: number;
};

const MCP_SETTINGS_CACHE_TTL_MS = 30_000;
const mcpSettingsCache = new Map<string, MCPSettingsCacheEntry>();

const getCachedMcpSettings = (projectId: string | null) => {
  if (!projectId) return null;
  return mcpSettingsCache.get(projectId) ?? null;
};

const isMcpSettingsCacheFresh = (cacheEntry: MCPSettingsCacheEntry) =>
  Date.now() - cacheEntry.updatedAt < MCP_SETTINGS_CACHE_TTL_MS;

const getErrorMessage = (error: unknown): string => {
  if (error instanceof Error) return error.message;
  return String(error);
};

export function resetMCPSettingsCacheForTests() {
  mcpSettingsCache.clear();
}

function MCPInitialLoadingState() {
  return (
    <div
      className="flex min-h-[560px] items-center justify-center"
      role="status"
      aria-live="polite"
      aria-label="Loading MCP server settings"
    >
      <div className="relative w-full max-w-md overflow-hidden rounded-2xl border border-border/70 bg-card/80 p-5 shadow-sm">
        <div className="pointer-events-none absolute inset-x-0 top-0 h-px bg-gradient-to-r from-transparent via-primary/40 to-transparent" />
        <div className="flex items-start gap-4">
          <div className="relative flex h-12 w-12 shrink-0 items-center justify-center rounded-2xl border border-border/70 bg-background shadow-sm">
            <RefreshCw className="h-5 w-5 animate-spin text-primary" />
            <span className="absolute -right-1 -top-1 h-3 w-3 rounded-full border-2 border-card bg-primary" />
          </div>
          <div className="min-w-0 flex-1">
            <h2 className="text-base font-semibold text-foreground">
              Loading MCP servers
            </h2>
            <p className="mt-1 text-sm text-muted-foreground">
              Checking installed tools and available recommendations for this
              project.
            </p>
            <div className="mt-5 space-y-3">
              {[0, 1, 2].map((item) => (
                <div
                  key={item}
                  className="flex items-center gap-3 rounded-xl border border-border/50 bg-background/60 p-3"
                >
                  <div className="h-9 w-9 rounded-lg bg-muted animate-pulse" />
                  <div className="min-w-0 flex-1 space-y-2">
                    <div className="h-2.5 w-2/3 rounded-full bg-muted animate-pulse" />
                    <div className="h-2 w-full rounded-full bg-muted/70 animate-pulse" />
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function MCPRefreshStatus({
  isRefreshing,
  loadError,
  lastLoadedAt,
  onRetry,
}: {
  isRefreshing: boolean;
  loadError: string | null;
  lastLoadedAt: number | null;
  onRetry: () => void;
}) {
  if (loadError) {
    return (
      <div className="flex flex-col gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive sm:flex-row sm:items-center sm:justify-between">
        <div className="flex min-w-0 items-center gap-2">
          <AlertCircle className="h-4 w-4 shrink-0" />
          <span className="truncate">
            Couldn’t refresh MCP servers: {loadError}
          </span>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="xs"
          className="self-start text-destructive hover:bg-destructive/10 hover:text-destructive sm:self-auto"
          onClick={onRetry}
        >
          Retry
        </Button>
      </div>
    );
  }

  if (isRefreshing) {
    return (
      <div className="inline-flex items-center gap-2 self-start rounded-full border border-border/60 bg-muted/50 px-3 py-1 text-xs text-muted-foreground sm:self-auto">
        <RefreshCw className="h-3.5 w-3.5 animate-spin" />
        Refreshing server status
      </div>
    );
  }

  if (!lastLoadedAt) return null;

  return (
    <div className="inline-flex items-center gap-2 self-start rounded-full border border-border/50 bg-background/70 px-3 py-1 text-xs text-muted-foreground sm:self-auto">
      <CheckCircle2 className="h-3.5 w-3.5 text-primary" />
      Status up to date
    </div>
  );
}

export function MCPSettings() {
  const currentProject = useProjectStore((state) => state.currentProject);
  const currentProjectId = currentProject?.id ?? null;
  const cachedMcpData = getCachedMcpSettings(currentProjectId);

  const [activeTab, setActiveTab] = useState<MainTab>("installed");
  const [installedServers, setInstalledServers] = useState<MCPServer[]>(
    () => cachedMcpData?.installedServers ?? [],
  );
  const [recommendedServers, setRecommendedServers] = useState<
    RecommendedServer[]
  >(() => cachedMcpData?.recommendedServers ?? []);
  const [isInitialLoading, setIsInitialLoading] = useState(
    () => !cachedMcpData,
  );
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [lastLoadedAt, setLastLoadedAt] = useState<number | null>(
    () => cachedMcpData?.updatedAt ?? null,
  );
  const [loadError, setLoadError] = useState<string | null>(null);
  const latestLoadRequestRef = useRef(0);
  const activeProjectRef = useRef(currentProjectId);
  const [installingServers, setInstallingServers] = useState<Set<string>>(
    new Set(),
  );
  const [serverActions, setServerActions] = useState<
    Record<
      string,
      "configure" | "restart" | "remove" | "toggle" | "scope" | "tools" | null
    >
  >({});
  const [showCustomServerModal, setShowCustomServerModal] = useState(false);
  const [toolsModalServer, setToolsModalServer] = useState<string | null>(null);
  const [toolsModalTools, setToolsModalTools] = useState<MCPTool[]>([]);

  const [wizardServer, setWizardServer] = useState<RecommendedServer | null>(
    null,
  );
  const [wizardMode, setWizardMode] = useState<WizardMode>("install");
  const [wizardInitialConfig, setWizardInitialConfig] = useState<
    Record<string, string>
  >({});

  const {
    preferences,
    updatePreferences,
    loadPreferences,
    isLoading: preferencesLoading,
  } = usePreferencesStore();
  const defaultMcpScope = preferences?.defaultMcpScope ?? ConfigScope.PROJECT;

  useEffect(() => {
    if (!preferences && !preferencesLoading) {
      loadPreferences();
    }
  }, [preferences, preferencesLoading, loadPreferences]);

  const recommendedByName = useMemo(() => {
    return new Map(recommendedServers.map((server) => [server.name, server]));
  }, [recommendedServers]);

  const applyMcpData = useCallback(
    (
      projectId: string,
      servers: MCPServer[],
      recommended: RecommendedServer[],
      updatedAt = Date.now(),
    ) => {
      mcpSettingsCache.set(projectId, {
        installedServers: servers,
        recommendedServers: recommended,
        updatedAt,
      });
      setInstalledServers(servers);
      setRecommendedServers(recommended);
      setLastLoadedAt(updatedAt);
      setLoadError(null);
      setActiveTab((currentTab) =>
        currentTab === "installed" && servers.length === 0
          ? "discover"
          : currentTab,
      );
    },
    [],
  );

  const loadData = useCallback(
    async (options: { force?: boolean; showRefreshIndicator?: boolean } = {}) => {
      const projectId = currentProjectId;
      if (!projectId) return;

      activeProjectRef.current = projectId;
      const cachedData = getCachedMcpSettings(projectId);
      if (cachedData && !options.force && isMcpSettingsCacheFresh(cachedData)) {
        applyMcpData(
          projectId,
          cachedData.installedServers,
          cachedData.recommendedServers,
          cachedData.updatedAt,
        );
        setIsInitialLoading(false);
        setIsRefreshing(false);
        return;
      }

      const requestId = latestLoadRequestRef.current + 1;
      latestLoadRequestRef.current = requestId;
      setLoadError(null);

      if (cachedData) {
        setIsInitialLoading(false);
        setIsRefreshing(options.showRefreshIndicator !== false);
      } else {
        setIsInitialLoading(true);
      }

      try {
        const [serversData, recommendedData] = await Promise.all([
          mcpGrpc.listServers(projectId),
          mcpGrpc.listRecommended(projectId),
        ]);

        if (
          latestLoadRequestRef.current !== requestId ||
          activeProjectRef.current !== projectId
        ) {
          return;
        }

        applyMcpData(
          projectId,
          serversData.servers,
          recommendedData.recommended,
        );
      } catch (error) {
        if (
          latestLoadRequestRef.current !== requestId ||
          activeProjectRef.current !== projectId
        ) {
          return;
        }

        console.error("Failed to load MCP data:", error);
        const message = getErrorMessage(error);
        setLoadError(message);
        toast.error("Failed to load MCP servers");
      } finally {
        if (
          latestLoadRequestRef.current === requestId &&
          activeProjectRef.current === projectId
        ) {
          setIsInitialLoading(false);
          setIsRefreshing(false);
        }
      }
    },
    [applyMcpData, currentProjectId],
  );

  useEffect(() => {
    activeProjectRef.current = currentProjectId;

    if (!currentProjectId) {
      setInstalledServers([]);
      setRecommendedServers([]);
      setLastLoadedAt(null);
      setLoadError(null);
      setIsInitialLoading(false);
      setIsRefreshing(false);
      return;
    }

    const cachedData = getCachedMcpSettings(currentProjectId);
    if (cachedData) {
      applyMcpData(
        currentProjectId,
        cachedData.installedServers,
        cachedData.recommendedServers,
        cachedData.updatedAt,
      );
      setIsInitialLoading(false);
      setIsRefreshing(false);
    } else {
      setInstalledServers([]);
      setRecommendedServers([]);
      setLastLoadedAt(null);
      setLoadError(null);
      setIsInitialLoading(true);
    }

    if (!cachedData || !isMcpSettingsCacheFresh(cachedData)) {
      void loadData({ force: true, showRefreshIndicator: Boolean(cachedData) });
    }

    const unsubscribe = subscribeToRefetch("config_health", () => {
      void loadData({ force: true });
    });

    return () => unsubscribe();
  }, [applyMcpData, currentProjectId, loadData]);

  const rememberDefaultScope = async (scope: ConfigScope) => {
    try {
      await updatePreferences({ defaultMcpScope: scope });
    } catch (error) {
      console.error("Failed to persist default MCP scope:", error);
      toast.info(
        "Installed successfully, but failed to remember default scope.",
      );
    }
  };

  const doInstall = async (
    server: RecommendedServer,
    envConfig: Record<string, string>,
    scope: ConfigScope = defaultMcpScope,
    rememberScope: boolean = false,
    options?: { configOverrides?: Partial<MCPServerConfig> },
  ) => {
    if (!currentProjectId) return;

    setInstallingServers((prev) => new Set(prev).add(server.name));
    try {
      const config: MCPServerConfig = { ...server.config };

      const mergedEnv: Record<string, string> = {};
      if (server.config.env) {
        server.config.env.forEach((envVar) => {
          const [key, ...valueParts] = envVar.split("=");
          mergedEnv[key] = valueParts.join("=");
        });
      }

      Object.entries(envConfig).forEach(([key, value]) => {
        if (value && value.trim()) {
          mergedEnv[key] = value;
        }
      });

      if (Object.keys(mergedEnv).length > 0) {
        config.env = Object.entries(mergedEnv).map(([k, v]) => `${k}=${v}`);
      }

      if (options?.configOverrides) {
        const overrides = options.configOverrides;
        if (overrides.command !== undefined) config.command = overrides.command;
        if (overrides.args !== undefined) config.args = overrides.args;
        if (overrides.type !== undefined) config.type = overrides.type;
        if (overrides.url !== undefined) config.url = overrides.url;
        if (overrides.headers !== undefined) config.headers = overrides.headers;
      }

      const response = await mcpGrpc.installServer(
        currentProjectId,
        server.name,
        config,
        scope,
      );

      if (!response.success) {
        throw new Error(
          response.message ||
            "MCP server failed to start after saving configuration",
        );
      }

      if (rememberScope) {
        await rememberDefaultScope(scope);
      }

      toast.success(`${server.displayName} installed successfully`);

      await loadData({ force: true });
    } catch (error) {
      console.error("Failed to install server:", error);
      Sentry.captureException(error, {
        tags: { component: 'mcp_settings', operation: 'install' },
        extra: { serverName: server.displayName },
      });
      toast.error(
        `Failed to install ${server.displayName}: ${getErrorMessage(error)}`,
      );
      throw error;
    } finally {
      setInstallingServers((prev) => {
        const next = new Set(prev);
        next.delete(server.name);
        return next;
      });
    }
  };

  const handleRestart = async (serverName: string) => {
    if (!currentProjectId) return;

    setServerActions((prev) => ({ ...prev, [serverName]: "restart" }));
    try {
      await mcpGrpc.restartServer(currentProjectId, serverName);
      toast.success("Server restarted successfully");
      await loadData({ force: true });
    } catch (error) {
      console.error("Failed to restart server:", error);
      Sentry.captureException(error, {
        tags: { component: 'mcp_settings', operation: 'restart' },
        extra: { serverName },
      });
      toast.error(`Failed to restart server: ${getErrorMessage(error)}`);
    } finally {
      setServerActions((prev) => {
        const next = { ...prev };
        delete next[serverName];
        return next;
      });
    }
  };

  const handleUninstall = async (serverName: string) => {
    if (!currentProjectId) return;

    setServerActions((prev) => ({ ...prev, [serverName]: "remove" }));
    try {
      await mcpGrpc.uninstallServer(currentProjectId, serverName);
      toast.success("Server removed successfully");
      await loadData({ force: true });
    } catch (error) {
      console.error("Failed to uninstall server:", error);
      Sentry.captureException(error, {
        tags: { component: 'mcp_settings', operation: 'uninstall' },
        extra: { serverName },
      });
      toast.error(`Failed to uninstall server: ${getErrorMessage(error)}`);
    } finally {
      setServerActions((prev) => {
        const next = { ...prev };
        delete next[serverName];
        return next;
      });
    }
  };

  const handleToggleServerEnabled = async (
    serverName: string,
    enabled: boolean,
  ) => {
    if (!currentProjectId) return;

    setServerActions((prev) => ({ ...prev, [serverName]: "toggle" }));
    try {
      const response = await mcpGrpc.setServerEnabled(
        currentProjectId,
        serverName,
        enabled,
      );
      if (!response.success) {
        throw new Error(response.message || "Failed to update server state");
      }

      toast.success(enabled ? "Server enabled" : "Server disabled");
      await loadData({ force: true });
    } catch (error) {
      console.error("Failed to toggle server enabled state:", error);
      Sentry.captureException(error, {
        tags: { component: 'mcp_settings', operation: 'toggle_enabled' },
        extra: { serverName },
      });
      toast.error(
        `Failed to ${enabled ? "enable" : "disable"} server: ${getErrorMessage(error)}`,
      );
    } finally {
      setServerActions((prev) => {
        const next = { ...prev };
        delete next[serverName];
        return next;
      });
    }
  };

  const handleMoveServerScope = async (
    serverName: string,
    scope: ConfigScope,
  ) => {
    if (!currentProjectId) return;

    const current = installedServers.find(
      (server) => server.name === serverName,
    );
    if (current?.scope === scope) {
      return;
    }

    setServerActions((prev) => ({ ...prev, [serverName]: "scope" }));
    try {
      const response = await mcpGrpc.moveServerScope(
        currentProjectId,
        serverName,
        scope,
      );
      if (!response.success) {
        throw new Error(response.message || "Failed to move server scope");
      }

      toast.success("Install location updated");
      await loadData({ force: true });
    } catch (error) {
      console.error("Failed to move server scope:", error);
      Sentry.captureException(error, {
        tags: { component: 'mcp_settings', operation: 'move_scope' },
        extra: { serverName },
      });
      toast.error(
        `Failed to change install location: ${getErrorMessage(error)}`,
      );
    } finally {
      setServerActions((prev) => {
        const next = { ...prev };
        delete next[serverName];
        return next;
      });
    }
  };

  const handleViewServerTools = async (serverName: string) => {
    if (!currentProjectId) return;

    setServerActions((prev) => ({ ...prev, [serverName]: "tools" }));
    try {
      const response = await mcpGrpc.getServerTools(
        currentProjectId,
        serverName,
      );
      setToolsModalServer(serverName);
      setToolsModalTools(response.tools);
    } catch (error) {
      console.error("Failed to load server tools:", error);
      toast.error(`Failed to load tools: ${getErrorMessage(error)}`);
    } finally {
      setServerActions((prev) => {
        const next = { ...prev };
        delete next[serverName];
        return next;
      });
    }
  };

  const openInstallWizard = (server: RecommendedServer) => {
    setWizardMode("install");
    setWizardInitialConfig({});
    setWizardServer(server);
  };

  const openEditWizard = (server: MCPServer) => {
    const recommended = recommendedByName.get(server.name);
    if (!recommended) {
      toast.info("This server does not expose guided configuration fields yet");
      return;
    }

    const currentEnv: Record<string, string> = {};
    if (server.config.env) {
      server.config.env.forEach((envVar) => {
        const [key, ...valueParts] = envVar.split("=");
        const value = valueParts.join("=");
        if (!value.includes("_reliant_shared_demo_")) {
          currentEnv[key] = value;
        }
      });
    }

    setWizardMode("edit");
    setWizardInitialConfig(currentEnv);
    setWizardServer(recommended);
  };


  const handleWizardInstall = async (
    envConfig: Record<string, string>,
    scope: ConfigScope,
    rememberScope: boolean,
    options?: { configOverrides?: Partial<MCPServerConfig> },
  ) => {
    if (!wizardServer) return;
    await doInstall(wizardServer, envConfig, scope, rememberScope, options);
    setWizardServer(null);
  };

  const handleWizardUpdate = async (envConfig: Record<string, string>) => {
    if (!currentProjectId || !wizardServer) return;

    const filteredConfig: Record<string, string> = {};
    Object.entries(envConfig).forEach(([key, value]) => {
      if (value && value.trim()) {
        filteredConfig[key] = value;
      }
    });

    try {
      await mcpGrpc.updateServerConfig(
        currentProjectId,
        wizardServer.name,
        filteredConfig,
      );
      toast.success("Configuration updated successfully");
      await loadData({ force: true });
      setWizardServer(null);
    } catch (error) {
      console.error("Failed to update configuration:", error);
      Sentry.captureException(error, {
        tags: { component: 'mcp_settings', operation: 'wizard_update' },
        extra: { serverName: wizardServer.name },
      });
      toast.error(`Failed to update configuration: ${getErrorMessage(error)}`);
      throw error;
    }
  };

  const handleInstallCustomServer = async (
    name: string,
    config: {
      command: string;
      args?: string[];
      env?: string[];
      headers?: Record<string, string>;
      type: string;
      url?: string;
    },
    scope: ConfigScope,
    rememberScope: boolean,
  ) => {
    if (!currentProjectId) return;

    const response = await mcpGrpc.installServer(
      currentProjectId,
      name,
      config,
      scope,
    );
    if (!response.success) {
      throw new Error(
        response.message ||
          "MCP server failed to start after saving configuration",
      );
    }

    if (rememberScope) {
      await rememberDefaultScope(scope);
    }

    toast.success(`Custom MCP server "${name}" installed`);
    await loadData({ force: true });
  };

  const hasLoadedData = lastLoadedAt !== null;

  if (isInitialLoading && !hasLoadedData) {
    return <MCPInitialLoadingState />;
  }

  const topTabs = [
    {
      id: "installed",
      label: "Installed",
      count: installedServers.length,
      icon: <Server className="h-3.5 w-3.5" />,
    },
    {
      id: "discover",
      label: "Discover",
      count: recommendedServers.filter((server) => !server.installed).length,
      icon: <Compass className="h-3.5 w-3.5" />,
    },
  ];

  return (
    <div className="space-y-5" data-onboarding="mcp-server">
      <div className="flex flex-col gap-4">
        <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
          <div>
            <h2 className="mb-1 text-lg font-semibold text-foreground">
              MCP Servers
            </h2>
            <p className="text-sm text-muted-foreground">
              Install and manage tool servers for this project
            </p>
          </div>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <MCPRefreshStatus
              isRefreshing={isRefreshing}
              loadError={loadError}
              lastLoadedAt={lastLoadedAt}
              onRetry={() => void loadData({ force: true })}
            />
            <Button
              size="sm"
              onClick={() => setShowCustomServerModal(true)}
              leftIcon={<Plus className="h-4 w-4" />}
            >
              Add Custom Server
            </Button>
          </div>
        </div>
      </div>

      <MCPTabs
        tabs={topTabs}
        activeTab={activeTab}
        onTabChange={(tabId) => setActiveTab(tabId as MainTab)}
      />

      <div
        role="tabpanel"
        id={`mcp-tabpanel-${activeTab}`}
        aria-labelledby={`mcp-tab-${activeTab}`}
      >
        {activeTab === "installed" && (
          <MCPInstalledTab
            installedServers={installedServers}
            recommendedServers={recommendedServers}
            onConfigure={openEditWizard}
            onRestart={handleRestart}
            onToggleEnabled={handleToggleServerEnabled}
            onMoveScope={handleMoveServerScope}
            onViewTools={handleViewServerTools}
            onRemove={handleUninstall}
            onGoToDiscover={() => setActiveTab("discover")}
            loadingActions={serverActions}
          />
        )}

        {activeTab === "discover" && (
          <MCPDiscoverTab
            recommendedServers={recommendedServers}
            installingServers={installingServers}
            onInstall={openInstallWizard}
          />
        )}

      </div>

      <div className="rounded-lg border border-border/40 bg-muted/40 p-3 shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]">
        <div className="flex items-start gap-2">
          <AlertCircle className="h-4 w-4 mt-0.5 text-muted-foreground" />
          <div className="text-sm text-muted-foreground">
            <p className="mb-1">
              MCP servers extend Reliant with additional tools and capabilities.
            </p>
            <ul className="list-disc list-inside space-y-1 text-xs">
              <li>
                Connection status refreshes automatically every 30 seconds
              </li>
              <li>
                Configurations merge by scope: Global &lt; Project &lt; Project
                Local
              </li>
              <li>Restart disconnected servers after updating credentials</li>
              <li>Disable servers you want to keep installed but inactive</li>
            </ul>
          </div>
        </div>
      </div>

      {wizardServer && (
        <MCPSetupWizard
          mode={wizardMode}
          server={wizardServer}
          currentConfig={wizardInitialConfig}
          defaultScope={defaultMcpScope}
          onInstall={handleWizardInstall}
          onUpdate={handleWizardUpdate}
          onClose={() => setWizardServer(null)}
        />
      )}

      {showCustomServerModal && (
        <MCPCustomServerModal
          defaultScope={defaultMcpScope}
          onClose={() => setShowCustomServerModal(false)}
          onSave={async (name, config, scope, rememberScope) => {
            await handleInstallCustomServer(name, config, scope, rememberScope);
            setShowCustomServerModal(false);
          }}
        />
      )}

      {toolsModalServer && (
        <Modal
          isOpen={true}
          onClose={() => {
            setToolsModalServer(null);
            setToolsModalTools([]);
          }}
          size="md"
          title={`${toolsModalServer} tools`}
        >
          {toolsModalTools.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No tools are currently available for this server.
            </p>
          ) : (
            <div className="space-y-2">
              {toolsModalTools.map((tool) => (
                <div
                  key={tool.name}
                  className="rounded-md border border-border/70 p-3"
                >
                  <p className="text-sm font-semibold text-foreground">
                    {tool.name}
                  </p>
                  {tool.description ? (
                    <p className="mt-1 text-xs text-muted-foreground">
                      {tool.description}
                    </p>
                  ) : (
                    <p className="mt-1 text-xs text-muted-foreground italic">
                      No description provided.
                    </p>
                  )}
                </div>
              ))}
            </div>
          )}
        </Modal>
      )}
    </div>
  );
}