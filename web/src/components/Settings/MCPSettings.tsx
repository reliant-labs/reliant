import { useEffect, useMemo, useState } from "react";
import * as Sentry from "@sentry/react";
import { AlertCircle, RefreshCw, Plus } from "lucide-react";
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

export function MCPSettings() {
  const [activeTab, setActiveTab] = useState<MainTab>("installed");
  const [installedServers, setInstalledServers] = useState<MCPServer[]>([]);
  const [recommendedServers, setRecommendedServers] = useState<
    RecommendedServer[]
  >([]);
  const [loading, setLoading] = useState(true);
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

  const currentProject = useProjectStore((state) => state.currentProject);
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

  useEffect(() => {
    if (currentProject?.id) {
      loadData();
      const unsubscribe = subscribeToRefetch("config_health", loadData);
      return () => unsubscribe();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentProject?.id]);

  const recommendedByName = useMemo(() => {
    return new Map(recommendedServers.map((server) => [server.name, server]));
  }, [recommendedServers]);


  const getErrorMessage = (error: unknown): string => {
    if (error instanceof Error) return error.message;
    return String(error);
  };

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

  const loadData = async () => {
    if (!currentProject?.id) return;

    try {
      const [serversData, recommendedData] = await Promise.all([
        mcpGrpc.listServers(currentProject.id),
        mcpGrpc.listRecommended(currentProject.id),
      ]);

      setInstalledServers(serversData.servers);
      setRecommendedServers(recommendedData.recommended);
      setActiveTab((currentTab) =>
        currentTab === "installed" && serversData.servers.length === 0
          ? "discover"
          : currentTab,
      );
    } catch (error) {
      console.error("Failed to load MCP data:", error);
      toast.error("Failed to load MCP servers");
    } finally {
      setLoading(false);
    }
  };

  const doInstall = async (
    server: RecommendedServer,
    envConfig: Record<string, string>,
    scope: ConfigScope = defaultMcpScope,
    rememberScope: boolean = false,
    options?: { configOverrides?: Partial<MCPServerConfig> },
  ) => {
    if (!currentProject?.id) return;

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
        currentProject.id,
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

      await loadData();
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
    if (!currentProject?.id) return;

    setServerActions((prev) => ({ ...prev, [serverName]: "restart" }));
    try {
      await mcpGrpc.restartServer(currentProject.id, serverName);
      toast.success("Server restarted successfully");
      await loadData();
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
    if (!currentProject?.id) return;

    setServerActions((prev) => ({ ...prev, [serverName]: "remove" }));
    try {
      await mcpGrpc.uninstallServer(currentProject.id, serverName);
      toast.success("Server removed successfully");
      await loadData();
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
    if (!currentProject?.id) return;

    setServerActions((prev) => ({ ...prev, [serverName]: "toggle" }));
    try {
      const response = await mcpGrpc.setServerEnabled(
        currentProject.id,
        serverName,
        enabled,
      );
      if (!response.success) {
        throw new Error(response.message || "Failed to update server state");
      }

      toast.success(enabled ? "Server enabled" : "Server disabled");
      await loadData();
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
    if (!currentProject?.id) return;

    const current = installedServers.find(
      (server) => server.name === serverName,
    );
    if (current?.scope === scope) {
      return;
    }

    setServerActions((prev) => ({ ...prev, [serverName]: "scope" }));
    try {
      const response = await mcpGrpc.moveServerScope(
        currentProject.id,
        serverName,
        scope,
      );
      if (!response.success) {
        throw new Error(response.message || "Failed to move server scope");
      }

      toast.success("Install location updated");
      await loadData();
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
    if (!currentProject?.id) return;

    setServerActions((prev) => ({ ...prev, [serverName]: "tools" }));
    try {
      const response = await mcpGrpc.getServerTools(
        currentProject.id,
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
    if (!currentProject?.id || !wizardServer) return;

    const filteredConfig: Record<string, string> = {};
    Object.entries(envConfig).forEach(([key, value]) => {
      if (value && value.trim()) {
        filteredConfig[key] = value;
      }
    });

    try {
      await mcpGrpc.updateServerConfig(
        currentProject.id,
        wizardServer.name,
        filteredConfig,
      );
      toast.success("Configuration updated successfully");
      await loadData();
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
    if (!currentProject?.id) return;

    const response = await mcpGrpc.installServer(
      currentProject.id,
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
    await loadData();
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center h-64">
        <RefreshCw className="w-8 h-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  const topTabs = [
    { id: "installed", label: "Installed", count: installedServers.length },
    {
      id: "discover",
      label: "Discover",
      count: recommendedServers.filter((server) => !server.installed).length,
    },
  ];

  return (
    <div className="space-y-6" data-onboarding="mcp-server">
      <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <h2 className="text-base font-semibold mb-1">MCP Servers</h2>
          <p className="text-sm text-muted-foreground">
            Install and manage tool servers for this project
          </p>
        </div>
        <Button
          size="sm"
          onClick={() => setShowCustomServerModal(true)}
          leftIcon={<Plus className="w-4 h-4" />}
        >
          Add Custom Server
        </Button>
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

      <div className="rounded-lg border border-border bg-muted/40 p-3">
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