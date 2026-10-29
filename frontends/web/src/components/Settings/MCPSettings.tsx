import { useEffect, useMemo, useState } from "react";
import { reportError } from "@/lib/errorReporting";
import { useQueryClient } from "@tanstack/react-query";
import {
  AlertCircle,
  CheckCircle2,
  Compass,
  Plus,
  RefreshCw,
  Server,
} from "lucide-react";
import {
  configToProto,
  fetchMcpServerTools,
  useMcpRecommended,
  useMcpServers,
  type MCPServer,
  type MCPTool,
  type MCPServerConfig,
  type RecommendedServer,
  ConfigScope,
} from "../../hooks/mcp-queries";
import {
  useInstallServer,
  useMoveServerScope,
  useRestartServer,
  useSetServerEnabled,
  useUninstallServer,
  useUpdateServerConfig,
} from "../../hooks/mcp-service-hooks_gen";
import { useProjectStore } from "../../store/projectStore";
import { useScopePreferences, useUpdateScopePreferences } from "../../hooks/settings-queries";
import { toast } from "sonner";
import Button from "@/components/forge-ui/button";
import { MCPCustomServerModal } from "./MCPCustomServerModal";
import { MCPTabs } from "./MCPTabs";
import { MCPInstalledTab } from "./MCPInstalledTab";
import { MCPDiscoverTab } from "./MCPDiscoverTab";
import { MCPSetupWizard } from "./MCPSetupWizard";
import Modal from "../forge-ui/modal";
import { SettingsPageHeader } from "./SettingsPageHeader";

type MainTab = "installed" | "discover";
type WizardMode = "install" | "edit";

const NO_SERVERS: MCPServer[] = [];
const NO_RECOMMENDED: RecommendedServer[] = [];

const getErrorMessage = (error: unknown): string => {
  if (error instanceof Error) return error.message;
  return String(error);
};

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
            <h2 className="text-sm font-semibold text-foreground">
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
      <div className="flex flex-col gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive-ink sm:flex-row sm:items-center sm:justify-between">
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
          className="self-start text-destructive-ink! not-disabled:hover:bg-destructive/10! sm:self-auto"
          onClick={onRetry}
        >
          Retry
        </Button>
      </div>
    );
  }

  if (isRefreshing) {
    return (
      <div className="inline-flex items-center gap-2 self-start rounded-full border border-border/60 bg-background px-3 py-1 text-xs text-muted-foreground sm:self-auto">
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
  const queryClient = useQueryClient();

  // The servers and the recommendations are generated MCPService reads: the
  // cache is React Query's, every write below invalidates mcpServiceKeys.all,
  // and the stream's config_health refetch does too (invalidateMcpQueries).
  const serversQuery = useMcpServers(currentProjectId);
  const recommendedQuery = useMcpRecommended(currentProjectId);
  const installedServers = serversQuery.data ?? NO_SERVERS;
  const recommendedServers = recommendedQuery.data ?? NO_RECOMMENDED;
  const hasLoadedData = serversQuery.isSuccess && recommendedQuery.isSuccess;
  const isFetching = serversQuery.isFetching || recommendedQuery.isFetching;
  const isInitialLoading = !!currentProjectId && !hasLoadedData && (serversQuery.isPending || recommendedQuery.isPending) && isFetching;
  const isRefreshing = hasLoadedData && isFetching;
  const queryError = serversQuery.error ?? recommendedQuery.error;
  const loadError = queryError && !isFetching ? getErrorMessage(queryError) : null;
  const lastLoadedAt = hasLoadedData ? Math.max(serversQuery.dataUpdatedAt, recommendedQuery.dataUpdatedAt) : null;
  const refetchAll = () => {
    void serversQuery.refetch();
    void recommendedQuery.refetch();
  };

  const installServer = useInstallServer();
  const restartServer = useRestartServer();
  const uninstallServer = useUninstallServer();
  const setServerEnabled = useSetServerEnabled();
  const moveServerScope = useMoveServerScope();
  const updateServerConfig = useUpdateServerConfig();

  const [activeTab, setActiveTab] = useState<MainTab>("installed");
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

  const { defaultMcpScope } = useScopePreferences().data;
  const { mutateAsync: updatePreferences } = useUpdateScopePreferences();

  const recommendedByName = useMemo(() => {
    return new Map(recommendedServers.map((server) => [server.name, server]));
  }, [recommendedServers]);

  // A project with nothing installed opens on Discover.
  const noServersInstalled = serversQuery.isSuccess && installedServers.length === 0;
  useEffect(() => {
    if (noServersInstalled) {
      setActiveTab((currentTab) => (currentTab === "installed" ? "discover" : currentTab));
    }
  }, [noServersInstalled, serversQuery.dataUpdatedAt]);

  // Say so once per failed load (a retry that fails again says so again).
  const errorUpdatedAt = Math.max(serversQuery.errorUpdatedAt, recommendedQuery.errorUpdatedAt);
  useEffect(() => {
    if (!queryError) return;
    console.error("Failed to load MCP data:", queryError);
    toast.error("Failed to load MCP servers");
    // eslint-disable-next-line react-hooks/exhaustive-deps -- keyed on when the error happened, not its identity
  }, [errorUpdatedAt]);

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

      const response = await installServer.mutateAsync({
        projectId: currentProjectId,
        name: server.name,
        config: configToProto(config),
        scope,
      });

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
    } catch (error) {
      console.error("Failed to install server:", error);
      reportError(error, { component: 'mcp_settings', operation: 'install', server_name: server.displayName });
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
      await restartServer.mutateAsync({ projectId: currentProjectId, name: serverName });
      toast.success("Server restarted successfully");
    } catch (error) {
      console.error("Failed to restart server:", error);
      reportError(error, { component: 'mcp_settings', operation: 'restart', server_name: serverName });
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
      await uninstallServer.mutateAsync({ projectId: currentProjectId, name: serverName });
      toast.success("Server removed successfully");
    } catch (error) {
      console.error("Failed to uninstall server:", error);
      reportError(error, { component: 'mcp_settings', operation: 'uninstall', server_name: serverName });
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
      const response = await setServerEnabled.mutateAsync({
        projectId: currentProjectId,
        name: serverName,
        enabled,
      });
      if (!response.success) {
        throw new Error(response.message || "Failed to update server state");
      }

      toast.success(enabled ? "Server enabled" : "Server disabled");
    } catch (error) {
      console.error("Failed to toggle server enabled state:", error);
      reportError(error, { component: 'mcp_settings', operation: 'toggle_enabled', server_name: serverName });
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
      const response = await moveServerScope.mutateAsync({
        projectId: currentProjectId,
        name: serverName,
        scope,
      });
      if (!response.success) {
        throw new Error(response.message || "Failed to move server scope");
      }

      toast.success("Install location updated");
    } catch (error) {
      console.error("Failed to move server scope:", error);
      reportError(error, { component: 'mcp_settings', operation: 'move_scope', server_name: serverName });
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
      const tools = await fetchMcpServerTools(queryClient, currentProjectId, serverName);
      setToolsModalServer(serverName);
      setToolsModalTools(tools);
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
      await updateServerConfig.mutateAsync({
        projectId: currentProjectId,
        name: wizardServer.name,
        env: filteredConfig,
      });
      toast.success("Configuration updated successfully");
      setWizardServer(null);
    } catch (error) {
      console.error("Failed to update configuration:", error);
      reportError(error, { component: 'mcp_settings', operation: 'wizard_update', server_name: wizardServer.name });
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

    const response = await installServer.mutateAsync({
      projectId: currentProjectId,
      name,
      config: configToProto(config),
      scope,
    });
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
  };

  if (isInitialLoading) {
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
        <SettingsPageHeader
          title="MCP Servers"
          description="Give agents extra tools by connecting Model Context Protocol (MCP) servers. Install one from the Discover tab, or add your own custom server; installed servers are available to this project's agents."
          className="mb-0"
          actions={
            <>
              <MCPRefreshStatus
                isRefreshing={isRefreshing}
                loadError={loadError}
                lastLoadedAt={lastLoadedAt}
                onRetry={refetchAll}
              />
              <Button
                size="sm"
                onClick={() => setShowCustomServerModal(true)}
                leftIcon={<Plus className="h-4 w-4" />}
              >
                Add Custom Server
              </Button>
            </>
          }
        />
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

      <div className="rounded-lg border border-border/60 bg-background p-3">
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
          open={true}
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