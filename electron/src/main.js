const {
  app,
  BrowserWindow,
  ipcMain,
  dialog,
  shell,
  Menu,
  Tray,
  globalShortcut,
  Notification,
  protocol,
} = require("electron");

// NOTE: With HTTP/2 (TLS), connection limits are not a concern.
// The backend now uses self-signed certs for HTTPS, enabling HTTP/2 multiplexing.
//
// DEV is different: the renderer loads over plain HTTP from Vite, so every
// RPC rides HTTP/1.1 and Chromium's 6-connections-per-origin limit applies.
// Long-lived Connect streams (chat, user updates, terminals) plus any slow
// unary call pin those sockets, and every other RPC sits "pending" in the
// network tab until it times out. Lift the limit for loopback origins —
// harmless for packaged builds (h2 multiplexes anyway), essential for dev.
app.commandLine.appendSwitch("ignore-connections-limit", "127.0.0.1,localhost");

const net = require("node:net");
const log = require("./logger");
const path = require("path");
const fs = require("fs");
const BackendManager = require("./backend-manager");
const {
  defaultAccelerators,
  resolveMenuAccelerators,
} = require("./menu-accelerators");
const WindowManager = require("./window-manager");
const BrowserManager = require("./browser-manager");
const windowConfig = require("./window-config");
const { shouldOpenExternally } = require("./navigation-policy");
const {
  APP_INDEX_URL,
  APP_ORIGIN,
  registerAppProtocol,
  registerSchemePrivileges,
} = require("./app-protocol");
const oauthLoopback = require("./oauth-loopback");
const { watchRendererHealth } = require("./renderer-health");
const {
  formatDiagnosticsReport,
  shouldAutoOpenDevTools,
  shouldShowDevToolsMenuItem,
} = require("./diagnostics");
const authStorage = require("./auth-storage");
const cliInstaller = require("./cli-installer");
const {
  describeAuthPrincipalChange,
  shouldRestartBackendForAuthChange,
} = require("./backend-auth");
const windowStateClient = require("./window-state-client");
const Sentry = require("@sentry/electron/main");
const { StatsigClient } = require("@statsig/js-client");
const { autoUpdater } = require("electron-updater");
const electronLog = require("electron-log");
const ChunkedDownloader = require("./chunked-downloader");
const crypto = require("crypto");

const appStartTime = Date.now();

// Configure auto-updater logging
autoUpdater.logger = electronLog;
autoUpdater.logger.transports.file.level = "info";

// Disable automatic downloads - require user confirmation
autoUpdater.autoDownload = false;
autoUpdater.autoInstallOnAppQuit = false;

// Determine update channel based on version
// RC/alpha/beta builds use 'alpha' channel, stable builds use 'latest'
const currentVersion = app.getVersion();
const isPrerelease = /-rc\.|rc[0-9]+|-beta\.|beta[0-9]+|-alpha\.|alpha[0-9]+/.test(currentVersion);
const updateChannel = isPrerelease ? 'alpha' : 'latest';

log.info(`[AutoUpdater] App version: ${currentVersion}`);
log.info(`[AutoUpdater] Update channel: ${updateChannel}`);

// Use generic provider with custom domain for downloads
// NOTE: electron-builder uses provider: s3 for publishing (with credentials),
// but we use generic provider here to download from the public custom domain
// (downloads.reliantlabs.io) which doesn't require S3 API authentication.
// This is the correct setup when using R2 with a custom domain.

// Allow overriding update URL for local testing (set RELIANT_UPDATE_URL env var)
// See electron/scripts/LOCAL_UPDATE_TESTING.md for instructions
const updateServerUrl = process.env.RELIANT_UPDATE_URL || "https://downloads.reliantlabs.io/";
if (process.env.RELIANT_UPDATE_URL) {
  log.info(`[AutoUpdater] Using custom update URL: ${updateServerUrl}`);
}

autoUpdater.setFeedURL({
  provider: "generic",
  url: updateServerUrl,
  channel: updateChannel,
});

// Optimize download settings
// Allow differential updates (blockmaps) if available
autoUpdater.allowPrerelease = isPrerelease;
// Request headers to prevent compression issues
// Accept-Encoding: identity prevents Cloudflare from compressing the response,
// ensuring Content-Length matches actual file size
// Note: We don't set Cache-Control here - let Cloudflare cache rules handle caching
autoUpdater.requestHeaders = {
  'Accept-Encoding': 'identity', // Request uncompressed content to prevent Content-Length mismatch
};

// Privacy settings helper functions
// These read from a persistent settings file in userData
function getPrivacySettingsPath() {
  return path.join(app.getPath('userData'), 'privacy-settings.json');
}

function readPrivacySettings() {
  try {
    const settingsPath = getPrivacySettingsPath();
    log.info('[Privacy] Reading settings from:', settingsPath);
    if (fs.existsSync(settingsPath)) {
      const data = fs.readFileSync(settingsPath, 'utf8');
      const settings = JSON.parse(data);
      log.info('[Privacy] Loaded settings from file:', settings);
      return settings;
    } else {
      log.info('[Privacy] No settings file found, using defaults (enabled)');
    }
  } catch (error) {
    log.warn('[Privacy] Failed to read privacy settings:', error);
  }
  // Default to true (opt-out model) - data collection enabled by default
  const defaults = {
    crashReportingEnabled: true,
    analyticsEnabled: true,
  };
  log.info('[Privacy] Using default settings:', defaults);
  return defaults;
}

function writePrivacySettings(settings) {
  try {
    const settingsPath = getPrivacySettingsPath();
    fs.writeFileSync(settingsPath, JSON.stringify(settings, null, 2), 'utf8');
    log.info('[Privacy] Privacy settings saved:', settings);
  } catch (error) {
    log.error('[Privacy] Failed to write privacy settings:', error);
  }
}

function getCrashReportingEnabled() {
  const settings = readPrivacySettings();
  return settings.crashReportingEnabled !== false; // Default to true
}

function getAnalyticsEnabled() {
  const settings = readPrivacySettings();
  return settings.analyticsEnabled !== false; // Default to true
}

// Sentry needs to be initialized before app.ready event fires
// We'll initialize it early with privacy settings check
let sentryInitialized = false;
let statsigClient = null;
const statsigEventVersion = 1;
const statsigSessionId = crypto.randomUUID();
const allowedAuthFunnelEvents = new Set([
  "app_opened",
  "app_closed",
  "auth_screen_viewed",
  "login_attempted",
  "login_failed",
  "login_succeeded",
  "signup_attempted",
  "signup_succeeded",
  "oauth_started",
  "oauth_failed",
  "oauth_succeeded",
  "workflow_failed",
  "update_available",
  "update_download_started",
  "update_download_completed",
  "update_install_started",
]);

function getAnalyticsStatePath() {
  return path.join(app.getPath("userData"), "analytics-state.json");
}

function readAnalyticsState() {
  const defaults = {
    stableId: crypto.randomUUID(),
    firstSeenAt: new Date().toISOString(),
    appOpenCount: 0,
  };

  try {
    const statePath = getAnalyticsStatePath();
    if (!fs.existsSync(statePath)) {
      fs.writeFileSync(statePath, JSON.stringify(defaults, null, 2), "utf8");
      return defaults;
    }

    const raw = fs.readFileSync(statePath, "utf8");
    const parsed = JSON.parse(raw);
    return {
      stableId: typeof parsed.stableId === "string" && parsed.stableId.length > 0 ? parsed.stableId : defaults.stableId,
      firstSeenAt: typeof parsed.firstSeenAt === "string" && parsed.firstSeenAt.length > 0 ? parsed.firstSeenAt : defaults.firstSeenAt,
      appOpenCount: Number.isInteger(parsed.appOpenCount) && parsed.appOpenCount >= 0 ? parsed.appOpenCount : 0,
    };
  } catch (error) {
    log.warn("[Statsig] Failed to read analytics state, using defaults:", error?.message || error);
    return defaults;
  }
}

function writeAnalyticsState(state) {
  try {
    fs.writeFileSync(getAnalyticsStatePath(), JSON.stringify(state, null, 2), "utf8");
  } catch (error) {
    log.warn("[Statsig] Failed to persist analytics state:", error?.message || error);
  }
}

function getAuthMethodFromSession(session) {
  if (!session?.user) return "unknown";

  const identityProvider = session.user.identities?.[0]?.provider;
  if (typeof identityProvider === "string" && identityProvider.length > 0) {
    return identityProvider;
  }

  const appMetadataProvider = session.user.app_metadata?.provider;
  if (typeof appMetadataProvider === "string" && appMetadataProvider.length > 0) {
    return appMetadataProvider;
  }

  return "password";
}

async function trackStatsigEvent(eventName, metadata = {}, options = {}) {
  if (!statsigClient || !getAnalyticsEnabled()) {
    return false;
  }

  if (!allowedAuthFunnelEvents.has(eventName)) {
    log.warn("[Statsig] Ignoring unknown event", { eventName });
    return false;
  }

  const isDev = !app.isPackaged;
  if (isDev) {
    return false;
  }

  try {
    if (typeof statsigClient.checkGate === "function") {
      const enabled = statsigClient.checkGate("auth_funnel_tracking_enabled");
      if (!enabled) {
        return false;
      }
    }
  } catch (gateError) {
    log.warn("[Statsig] Failed gate check, continuing with event send", gateError?.message || gateError);
  }

  const analyticsState = readAnalyticsState();

  let inferredUserId = "anonymous";
  try {
    const storedSession = authStorage.loadStoredAuth();
    if (storedSession?.user?.id) {
      inferredUserId = storedSession.user.id;
    }
  } catch (_) {
    // no-op: anonymous fallback is fine
  }

  const userId = typeof options.userID === "string" && options.userID.length > 0
    ? options.userID
    : inferredUserId;
  const authState = userId !== "anonymous" ? "authenticated" : "anonymous";

  const enriched = {
    ...metadata,
    event_version: statsigEventVersion,
    app_version: app.getVersion(),
    platform: process.platform,
    env_tier: app.isPackaged ? "production" : "development",
    session_id: statsigSessionId,
    auth_state: authState,
    is_first_seen: analyticsState.appOpenCount <= 1,
    stable_id: analyticsState.stableId,
    user_id: authState === "authenticated" ? userId : null,
    first_seen_at: analyticsState.firstSeenAt,
    electron_version: process.versions.electron,
    chrome_version: process.versions.chrome,
    node_version: process.versions.node,
  };

  await statsigClient.logEvent(eventName, null, enriched);
  return true;
}

function detectInstallMethod() {
  if (process.platform === "darwin") {
    // Check for Homebrew cask
    try {
      const homebrewPaths = [
        "/opt/homebrew/Caskroom/reliant",
        "/usr/local/Caskroom/reliant",
      ];
      for (const p of homebrewPaths) {
        if (fs.existsSync(p)) return "homebrew";
      }
    } catch (_) {}
    return "dmg";
  } else if (process.platform === "linux") {
    if (process.env.APPIMAGE) return "appimage";
    // Check for AUR/pacman install
    try {
      const { execSync } = require("child_process");
      execSync("pacman -Q reliant-labs-bin 2>/dev/null", { stdio: "ignore" });
      return "aur";
    } catch (_) {}
    return "linux_other";
  } else if (process.platform === "win32") {
    return "exe_installer";
  }
  return "unknown";
}

// Initialize Sentry BEFORE app.ready (required by Sentry SDK)
function initializeSentry() {
  // Only initialize in production, if crash reporting is enabled, and not disabled via env
  if (app.isPackaged && getCrashReportingEnabled() && process.env.SENTRY_ENABLED !== "false") {
    try {
      // Set environment based on release type for filtering in Sentry dashboard
      const sentryEnvironment = isPrerelease ? "prerelease" : "production";

      Sentry.init({
        dsn: process.env.SENTRY_DSN,
        environment: sentryEnvironment,
        release: `reliant@${currentVersion}`,
        beforeSend(event, hint) {
          // Double-check privacy settings before sending
          if (!getCrashReportingEnabled()) {
            return null;
          }
          // Filter out sensitive information if needed
          if (event.user) {
            delete event.user.ip_address;
          }
          return event;
        },
      });
      sentryInitialized = true;
      log.info(`[Sentry] Initialized (environment: ${sentryEnvironment}, release: reliant@${currentVersion})`);
    } catch (error) {
      log.error("[Sentry] Failed to initialize:", error);
    }
  } else {
    log.info("[Sentry] Disabled (development mode or user opted out)");
  }
}

// Initialize Statsig AFTER app.ready (can be async)
function initializeStatsig() {
  const analyticsEnabled = getAnalyticsEnabled();
  console.log('[Statsig] Analytics enabled:', analyticsEnabled);

  log.info('[Privacy] Initializing analytics with settings:', {
    analyticsEnabled,
  });

  // Initialize Statsig client for analytics
  // Disable in non-production environments
  const nodeEnv = process.env.NODE_ENV || "production";
  if (nodeEnv !== "production") {
    log.info("[Statsig] Disabled in non-production environment", { NODE_ENV: nodeEnv });
    return;
  }

  if (analyticsEnabled) {
    // Get user ID from stored auth session
    let userID = 'anonymous';
    try {
      const session = authStorage.loadStoredAuth();
      if (session?.user?.id) {
        userID = session.user.id;
        log.info('[Statsig] Using Supabase user ID:', userID);
      } else {
        log.info('[Statsig] No user session found, using anonymous');
      }
    } catch (error) {
      log.warn('[Statsig] Failed to load user ID, using anonymous:', error.message);
    }

    const statsigKey = process.env.STATSIG_CLIENT_KEY || "";
    if (!statsigKey) {
      log.info("[Statsig] No STATSIG_CLIENT_KEY configured, skipping initialization");
      return;
    }

    statsigClient = new StatsigClient(
      statsigKey,
      { userID: userID },
      {
        environment: { tier: process.env.NODE_ENV || "production" },
      }
    );

    statsigClient
      .initializeAsync()
      .then(async () => {
        log.info("[Statsig] Initialized successfully", { userID, analyticsEnabled: true });

        const analyticsState = readAnalyticsState();
        analyticsState.appOpenCount += 1;
        writeAnalyticsState(analyticsState);

        await trackStatsigEvent("app_opened", {
          app_open_count: analyticsState.appOpenCount,
          install_method: detectInstallMethod(),
        }, {
          userID,
        });
      })
      .catch((err) => {
        log.error("[Statsig] Initialization failed:", err);
      });
  } else {
    log.info("[Statsig] Disabled (user opted out)");
  }
}

const DEV_HOST = "127.0.0.1";
const DEV_PORT = Number(process.env.FRONTEND_PORT || 5173);
const DEV_URL = `http://${DEV_HOST}:${DEV_PORT}`;

function waitForPort(port, host = "127.0.0.1", timeoutMs = 20000) {
  const start = Date.now();
  return new Promise((resolve, reject) => {
    const tryOnce = () => {
      const s = net.connect(port, host);
      s.once("connect", () => {
        s.destroy();
        resolve(true);
      });
      s.once("error", () => {
        s.destroy();
        if (Date.now() - start > timeoutMs) reject(new Error("vite-timeout"));
        else setTimeout(tryOnce, 200);
      });
    };
    tryOnce();
  });
}

let mainWindow;
let backendManager;
let windowManager;
let tray = null;
let isQuitting = false;
let isInstallingUpdate = false; // Flag to prevent quit handlers from interfering with update installation
let pendingProjectPath = null;
let pendingLaunchDeepLink = null;

let trayStatus = {
  agentStatus: "idle", // idle | running | error
  activeWorkflows: 0,
  lastActivityAt: null,
  hasChats: false,
  canCreateChat: false,
  currentProjectName: null,
  currentWorktreeName: null,
  currentWorktreeId: "__main__",
  activeChatId: null,
  activeChatTitle: null,
  recentChats: [],
  workspaces: [],
  workflows: [],
};

let trayNotificationMuteUntil = null;
let trayNotificationMutePreset = "unmuted";

function getTraySettingsPath() {
  return path.join(app.getPath("userData"), "tray-settings.json");
}

function readTraySettings() {
  try {
    const settingsPath = getTraySettingsPath();
    if (!fs.existsSync(settingsPath)) {
      return {};
    }
    const raw = fs.readFileSync(settingsPath, "utf8");
    return JSON.parse(raw);
  } catch (error) {
    log.warn("[Tray] Failed to read tray settings:", error);
    return {};
  }
}

function writeTraySettings(settings) {
  try {
    const settingsPath = getTraySettingsPath();
    fs.writeFileSync(settingsPath, JSON.stringify(settings, null, 2), "utf8");
  } catch (error) {
    log.error("[Tray] Failed to write tray settings:", error);
  }
}

function loadTraySettings() {
  const settings = readTraySettings();
  if (typeof settings.notificationsMutedUntil === "number") {
    trayNotificationMuteUntil = settings.notificationsMutedUntil;
  } else {
    trayNotificationMuteUntil = null;
  }

  if (
    settings.notificationsMutePreset === "one_hour" ||
    settings.notificationsMutePreset === "until_tomorrow"
  ) {
    trayNotificationMutePreset = settings.notificationsMutePreset;
  } else {
    trayNotificationMutePreset = "unmuted";
  }
}

function getNextTomorrowTimestamp() {
  const tomorrow = new Date();
  tomorrow.setDate(tomorrow.getDate() + 1);
  tomorrow.setHours(0, 0, 0, 0);
  return tomorrow.getTime();
}

function isNotificationMuted() {
  if (typeof trayNotificationMuteUntil !== "number") {
    return false;
  }

  if (Date.now() >= trayNotificationMuteUntil) {
    trayNotificationMuteUntil = null;
    trayNotificationMutePreset = "unmuted";
    writeTraySettings({ notificationsMutedUntil: null, notificationsMutePreset: "unmuted" });
    refreshTrayMenu();
    return false;
  }

  return true;
}

function getMutePreset() {
  if (!isNotificationMuted()) {
    return "unmuted";
  }

  if (trayNotificationMutePreset === "one_hour" || trayNotificationMutePreset === "until_tomorrow") {
    return trayNotificationMutePreset;
  }

  return "custom";
}

function setNotificationMute(preset) {
  if (preset === "one_hour") {
    trayNotificationMuteUntil = Date.now() + 60 * 60 * 1000;
    trayNotificationMutePreset = "one_hour";
  } else if (preset === "until_tomorrow") {
    trayNotificationMuteUntil = getNextTomorrowTimestamp();
    trayNotificationMutePreset = "until_tomorrow";
  } else {
    trayNotificationMuteUntil = null;
    trayNotificationMutePreset = "unmuted";
  }

  writeTraySettings({
    notificationsMutedUntil: trayNotificationMuteUntil,
    notificationsMutePreset: trayNotificationMutePreset,
  });
  refreshTrayMenu();
}

function normalizeTrayStatus(status) {
  const next = {
    agentStatus: "idle",
    activeWorkflows: 0,
    lastActivityAt: null,
    hasChats: false,
    canCreateChat: false,
    currentProjectName: null,
    currentWorktreeName: null,
    currentWorktreeId: "__main__",
    activeChatId: null,
    activeChatTitle: null,
    recentChats: [],
    workspaces: [],
    workflows: [],
  };

  if (status && typeof status === "object") {
    if (["idle", "running", "error"].includes(status.agentStatus)) {
      next.agentStatus = status.agentStatus;
    }

    if (typeof status.activeWorkflows === "number" && Number.isFinite(status.activeWorkflows)) {
      next.activeWorkflows = Math.max(0, Math.floor(status.activeWorkflows));
    }

    if (typeof status.lastActivityAt === "string") {
      next.lastActivityAt = status.lastActivityAt;
    }

    if (typeof status.hasChats === "boolean") {
      next.hasChats = status.hasChats;
    }

    if (typeof status.canCreateChat === "boolean") {
      next.canCreateChat = status.canCreateChat;
    }

    if (typeof status.currentProjectName === "string") {
      next.currentProjectName = status.currentProjectName;
    }

    if (typeof status.currentWorktreeName === "string") {
      next.currentWorktreeName = status.currentWorktreeName;
    }

    if (typeof status.currentWorktreeId === "string") {
      next.currentWorktreeId = status.currentWorktreeId;
    }

    if (typeof status.activeChatId === "string") {
      next.activeChatId = status.activeChatId;
    }

    if (typeof status.activeChatTitle === "string") {
      next.activeChatTitle = status.activeChatTitle;
    }

    if (Array.isArray(status.recentChats)) {
      next.recentChats = status.recentChats
        .filter((chat) => chat && typeof chat.id === "string")
        .slice(0, 5)
        .map((chat) => ({
          id: chat.id,
          title: typeof chat.title === "string" ? chat.title : "Untitled chat",
          workflowStatus: typeof chat.workflowStatus === "string" ? chat.workflowStatus : "idle",
          needsRecovery: chat.needsRecovery === true,
        }));
    }

    if (Array.isArray(status.workspaces)) {
      next.workspaces = status.workspaces
        .filter((workspace) => workspace && typeof workspace.id === "string")
        .slice(0, 10)
        .map((workspace) => ({
          id: workspace.id,
          name: typeof workspace.name === "string" ? workspace.name : "Workspace",
          isMain: workspace.isMain === true,
        }));
    }

    if (Array.isArray(status.workflows)) {
      next.workflows = status.workflows
        .filter((workflow) => workflow && typeof workflow.name === "string")
        .slice(0, 20)
        .map((workflow) => ({
          name: workflow.name,
          source:
            workflow.source === "builtin" ||
            workflow.source === "user" ||
            workflow.source === "project"
              ? workflow.source
              : "user",
        }));
    }
  }

  return next;
}

function formatLastActivity(lastActivityAt) {
  if (!lastActivityAt) {
    return "No recent activity";
  }

  const parsed = new Date(lastActivityAt);
  if (Number.isNaN(parsed.getTime())) {
    return "No recent activity";
  }

  return `Last activity: ${parsed.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
}


function truncateLabel(value, maxLength = 40) {
  if (!value || typeof value !== "string") {
    return "";
  }

  if (value.length <= maxLength) {
    return value;
  }

  return `${value.slice(0, maxLength - 1)}…`;
}

function getChatMenuState(chat) {
  if (!chat) {
    return "Idle";
  }

  if (chat.needsRecovery) {
    return "Needs approval";
  }

  if (chat.workflowStatus === "running") {
    return "Running";
  }

  if (chat.workflowStatus === "paused") {
    return "Paused";
  }

  return "Idle";
}

function ensureMainWindowVisible() {
  if (mainWindow && !mainWindow.isDestroyed()) {
    if (mainWindow.isMinimized()) {
      mainWindow.restore();
    }
    mainWindow.show();
    mainWindow.focus();
    return Promise.resolve(mainWindow);
  }

  return createWindow().then(() => mainWindow);
}

function sendToMainWindow(channel, payload) {
  ensureMainWindowVisible()
    .then((window) => {
      if (!window || window.isDestroyed()) {
        return;
      }

      const send = () => window.webContents.send(channel, payload);
      if (window.webContents.isLoading()) {
        window.webContents.once("did-finish-load", () => {
          setTimeout(send, 150);
        });
      } else {
        send();
      }
    })
    .catch((error) => {
      log.error("[Tray] Failed to deliver event to main window:", { channel, error });
    });
}

function refreshTrayMenu() {
  if (!tray) {
    return;
  }

  const workflowLabel = `${trayStatus.activeWorkflows} workflow${trayStatus.activeWorkflows === 1 ? "" : "s"} running`;
  const mutePreset = getMutePreset();
  const isMuted = isNotificationMuted();
  const statusTitle = `Reliant • ${trayStatus.agentStatus === "running" ? "Running" : trayStatus.agentStatus === "error" ? "Error" : "Idle"}${trayStatus.activeWorkflows > 0 ? ` (${trayStatus.activeWorkflows})` : ""}`;
  const projectLabel = trayStatus.currentProjectName
    ? `Project: ${truncateLabel(trayStatus.currentProjectName, 48)}`
    : "Project: None selected";
  const workspaceLabel = trayStatus.currentWorktreeName
    ? `Workspace: ${truncateLabel(trayStatus.currentWorktreeName, 48)}`
    : "Workspace: Main";
  const activeChatLabel = trayStatus.activeChatTitle
    ? `Chat: ${truncateLabel(trayStatus.activeChatTitle, 48)}`
    : "Chat: None selected";

  const quickActions = [
    {
      label: "Show Reliant",
      click: () => {
        ensureMainWindowVisible();
      },
    },
    {
      label: "New Chat",
      enabled: trayStatus.canCreateChat,
      click: () => {
        sendToMainWindow("create-new-tab");
      },
    },
    {
      label: "Resume Last Chat",
      enabled: trayStatus.hasChats && trayStatus.canCreateChat,
      click: () => {
        sendToMainWindow("resume-last-chat");
      },
    },
  ];

  if (!trayStatus.canCreateChat) {
    quickActions.push({
      label: "Select a project to create chats",
      enabled: false,
    });
  }

  const recentChatsSubmenu = trayStatus.recentChats.length
    ? trayStatus.recentChats.map((chat) => ({
        label: `${truncateLabel(chat.title, 34)} • ${getChatMenuState(chat)}`,
        enabled: trayStatus.canCreateChat,
        click: () => sendToMainWindow("tray:go-to-chat", { chatId: chat.id }),
      }))
    : [{ label: "No recent chats", enabled: false }];

  const workspaceOptions = [
    { id: "__main__", name: "Main Workspace", isMain: true },
    ...trayStatus.workspaces.filter((workspace) => !workspace.isMain),
  ];
  const workspaceSubmenu = workspaceOptions.length
    ? workspaceOptions.map((workspace) => ({
        label: truncateLabel(workspace.name, 40),
        type: "checkbox",
        checked:
          (workspace.isMain && trayStatus.currentWorktreeId === "__main__") ||
          trayStatus.currentWorktreeId === workspace.id,
        enabled: Boolean(trayStatus.currentProjectName),
        click: () =>
          sendToMainWindow("tray:switch-workspace", {
            workspaceId: workspace.isMain ? "__main__" : workspace.id,
          }),
      }))
    : [{ label: "No workspaces", enabled: false }];

  const builtinWorkflowItems = trayStatus.workflows
    .filter((workflow) => workflow.source === "builtin")
    .map((workflow) => ({
      label: truncateLabel(workflow.name.replace("builtin://", ""), 40),
      enabled: Boolean(trayStatus.currentProjectName),
      click: () => sendToMainWindow("tray:open-workflow", { workflowName: workflow.name }),
    }));

  const createdWorkflowItems = trayStatus.workflows
    .filter((workflow) => workflow.source === "user" || workflow.source === "project")
    .map((workflow) => ({
      label: truncateLabel(workflow.name, 40),
      enabled: Boolean(trayStatus.currentProjectName),
      click: () => sendToMainWindow("tray:open-workflow", { workflowName: workflow.name }),
    }));

  const workflowsSubmenu = [
    {
      label: "Built-in",
      submenu: builtinWorkflowItems.length
        ? builtinWorkflowItems
        : [{ label: "No built-in workflows", enabled: false }],
    },
    {
      label: "Created",
      submenu: createdWorkflowItems.length
        ? createdWorkflowItems
        : [{ label: "No created workflows", enabled: false }],
    },
  ];

  const contextMenu = Menu.buildFromTemplate([
    { label: statusTitle, enabled: false },
    { label: workflowLabel, enabled: false },
    { label: formatLastActivity(trayStatus.lastActivityAt), enabled: false },
    { type: "separator" },
    { label: projectLabel, enabled: false },
    { label: workspaceLabel, enabled: false },
    { label: activeChatLabel, enabled: false },
    { type: "separator" },
    ...quickActions,
    {
      label: "Go to…",
      submenu: [
        {
          label: "Current Chat",
          enabled: Boolean(trayStatus.activeChatId) && trayStatus.canCreateChat,
          click: () =>
            sendToMainWindow("tray:go-to-chat", {
              chatId: trayStatus.activeChatId,
            }),
        },
        {
          label: "Workflow Hub",
          enabled: Boolean(trayStatus.currentProjectName),
          click: () => sendToMainWindow("tray:go-to-workflow-hub"),
        },
        {
          label: "Settings",
          enabled: true,
          click: () => sendToMainWindow("tray:go-to-settings"),
        },
        {
          label: "Project Picker",
          enabled: true,
          click: () => sendToMainWindow("tray:go-to-project-picker"),
        },
      ],
    },
    {
      label: "Recent Chats",
      submenu: recentChatsSubmenu,
    },
    {
      label: "Workspaces",
      submenu: workspaceSubmenu,
    },
    {
      label: "Workflows",
      submenu: workflowsSubmenu,
    },
    { type: "separator" },
    {
      label: "Notifications",
      submenu: [
        {
          label: "Mute for 1 Hour",
          type: "checkbox",
          checked: mutePreset === "one_hour",
          click: () => setNotificationMute("one_hour"),
        },
        {
          label: "Mute Until Tomorrow",
          type: "checkbox",
          checked: mutePreset === "until_tomorrow",
          click: () => setNotificationMute("until_tomorrow"),
        },
        { type: "separator" },
        {
          label: isMuted && trayNotificationMuteUntil
            ? `Muted until ${new Date(trayNotificationMuteUntil).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`
            : "Notifications active",
          enabled: false,
        },
        {
          label: "Unmute",
          enabled: isMuted,
          click: () => setNotificationMute("unmuted"),
        },
      ],
    },
    { type: "separator" },
    {
      label: "Quit",
      click: () => {
        isQuitting = true;
        app.quit();
      },
    },
  ]);

  tray.setContextMenu(contextMenu);
}

function updateTrayStatus(status) {
  trayStatus = normalizeTrayStatus(status);
  refreshTrayMenu();
}

async function createWindow(options = {}) {
  const { createInitialTab = false } = options;

  // In development, append directory basename and port to title for clarity
  let windowTitle = "Reliant";
  if (!app.isPackaged && process.cwd()) {
    const basename = path.basename(process.cwd());
    const frontendPort = process.env.FRONTEND_PORT || "5173";
    windowTitle = `Reliant [${basename}] - Frontend:${frontendPort}`;
  }

  mainWindow = new BrowserWindow({
    width: 1400,
    height: 900,
    title: windowTitle,
    webPreferences: windowConfig.getWebPreferences(),
    ...windowConfig.getCommonWindowOptions(),
    titleBarStyle: windowConfig.getTitleBarStyle('inset'), // Main window uses hiddenInset
    backgroundColor: "#111111", // avoids white flash if we show early
  });

  // ---- External links (register before load) ----
  mainWindow.webContents.setWindowOpenHandler(({ url }) => {
    shell.openExternal(url);
    return { action: "deny" };
  });

  // Honor the renderer's beforeunload prompt (unsaved changes in the workflow
  // builder, etc). Without this handler Electron silently allows the unload.
  // event.preventDefault() here means "allow the navigation/close to proceed".
  mainWindow.webContents.on("will-prevent-unload", (event) => {
    const choice = dialog.showMessageBoxSync(mainWindow, {
      type: "question",
      buttons: ["Leave", "Stay"],
      title: "Unsaved changes",
      message: "You have unsaved changes. Are you sure you want to leave?",
      defaultId: 1,
      cancelId: 1,
    });
    if (choice === 0) event.preventDefault();
  });

  mainWindow.webContents.on("will-navigate", (event, url) => {
    if (shouldOpenExternally(url, mainWindow.webContents.getURL())) {
      event.preventDefault();
      shell.openExternal(url);
    }
  });

  // NOTE: packaged builds no longer intercept in-app navigation. Under the old
  // file:// load this handler existed because history navigation produced
  // file:///some/route, which had no document behind it; it forced a reload of
  // index.html and threw the route away. app:// serves index.html for unknown
  // paths (the SPA fallback in app-protocol.js), so the router now handles its
  // own routes and intercepting would break deep links.

  // Disable refresh and DevTools in production (installed builds only)
  if (app.isPackaged) {
    mainWindow.webContents.on("before-input-event", (event, input) => {
      // Reload in place. Previously this reloaded index.html from disk to work
      // around file:// losing the route; app:// serves the current route
      // correctly, so an ordinary reload preserves where the user was.
      if ((input.meta && input.key.toLowerCase() === "r") ||
          (input.control && input.key.toLowerCase() === "r") ||
          input.key === "F5") {
        event.preventDefault();
        mainWindow.webContents.reload();
      }

      // DevTools keyboard shortcuts stay disabled in packaged builds so an
      // ordinary user cannot open them by accident. The View menu item is the
      // deliberate path in, and RELIANT_DEVTOOLS=1 is the one for a user we
      // are actively debugging with. Blocking the KEYS is not the same as
      // forcing DevTools closed, which is what previously made a blank window
      // impossible to inspect at all.
      if (input.control && input.shift && input.key.toLowerCase() === "i") {
        event.preventDefault();
      }
      if (input.meta && input.alt && input.key.toLowerCase() === "i") {
        event.preventDefault();
      }
      if (input.key === "F12") {
        event.preventDefault();
      }
      if (input.control && input.shift && input.key.toLowerCase() === "c") {
        event.preventDefault();
      }
      if (input.meta && input.alt && input.key.toLowerCase() === "c") {
        event.preventDefault();
      }
      if (input.control && input.shift && input.key.toLowerCase() === "j") {
        event.preventDefault();
      }
      if (input.meta && input.alt && input.key.toLowerCase() === "j") {
        event.preventDefault();
      }

      // Disable View Source and other dev shortcuts
      if (input.control && input.key.toLowerCase() === "u") {
        event.preventDefault();
      }
      if (input.meta && input.key.toLowerCase() === "u") {
        event.preventDefault();
      }
    });
  }

  // Load the app
  // ---- Visibility & load flow (register BEFORE load) ----
  let windowShown = false;

  mainWindow.once("ready-to-show", () => {
    if (!windowShown) {
      mainWindow.show();
      windowShown = true;
    }
  });

  // If ready-to-show never fires (some render paths), show on DOM construction
  mainWindow.webContents.once("dom-ready", () => {
    if (!windowShown) {
      mainWindow.show();
      windowShown = true;
    }
  });

  // Log load lifecycle
  mainWindow.webContents.on("did-start-loading", () => {});

  mainWindow.webContents.on("did-stop-loading", () => {});

  mainWindow.webContents.on("did-frame-finish-load", () => {});

  mainWindow.webContents.on("page-title-updated", (event, title) => {});

  // Backend port handshake + post-load fallback
  // Enhanced context menu with spell check, macOS features, and more
  mainWindow.webContents.on("context-menu", (event, params) => {
    const isMac = process.platform === "darwin";
    const hasSelection = params.selectionText && params.selectionText.trim().length > 0;
    const isLink = params.linkURL && params.linkURL.length > 0;
    const isEditable = params.isEditable || params.inputFieldType;

    // Build menu items dynamically based on context
    const menuTemplate = [];

    // === Spell Check Suggestions (for misspelled words) ===
    if (params.misspelledWord && params.dictionarySuggestions) {
      // Add spelling suggestions
      if (params.dictionarySuggestions.length > 0) {
        params.dictionarySuggestions.slice(0, 5).forEach((suggestion) => {
          menuTemplate.push({
            label: suggestion,
            click: () => mainWindow.webContents.replaceMisspelling(suggestion),
          });
        });
      } else {
        menuTemplate.push({
          label: "No Suggestions",
          enabled: false,
        });
      }

      menuTemplate.push({ type: "separator" });

      // Add to dictionary option
      menuTemplate.push({
        label: `Add "${params.misspelledWord}" to Dictionary`,
        click: () => {
          mainWindow.webContents.session.addWordToSpellCheckerDictionary(params.misspelledWord);
        },
      });

      menuTemplate.push({ type: "separator" });
    }

    // === Editable Field Options ===
    if (isEditable) {
      // Undo/Redo
      menuTemplate.push(
        { label: "Undo", role: "undo" },
        { label: "Redo", role: "redo" },
        { type: "separator" }
      );

      // Cut/Copy/Paste
      menuTemplate.push(
        { label: "Cut", role: "cut" },
        { label: "Copy", role: "copy" },
        { label: "Paste", role: "paste" },
        { type: "separator" },
        { label: "Select All", role: "selectAll" }
      );
    }

    // === Text Selection Options (non-editable) ===
    if (!isEditable && hasSelection) {
      menuTemplate.push(
        { label: "Copy", role: "copy" }
      );
    }

    // === Look Up & Search (when text is selected) ===
    if (hasSelection) {
      const selectedText = params.selectionText.trim();
      const truncatedText = selectedText.length > 20
        ? selectedText.substring(0, 20) + "..."
        : selectedText;

      if (menuTemplate.length > 0) {
        menuTemplate.push({ type: "separator" });
      }

      // Look Up (macOS only - uses dictionary)
      if (isMac) {
        menuTemplate.push({
          label: `Look Up "${truncatedText}"`,
          click: () => {
            mainWindow.webContents.showDefinitionForSelection();
          },
        });
      }

      // Search Google
      menuTemplate.push({
        label: `Search Google for "${truncatedText}"`,
        click: () => {
          shell.openExternal(
            `https://www.google.com/search?q=${encodeURIComponent(selectedText)}`
          );
        },
      });
    }

    // === Link Options ===
    if (isLink) {
      if (menuTemplate.length > 0) {
        menuTemplate.push({ type: "separator" });
      }

      menuTemplate.push(
        {
          label: "Open Link in Browser",
          click: () => shell.openExternal(params.linkURL),
        },
        {
          label: "Copy Link Address",
          click: () => {
            require("electron").clipboard.writeText(params.linkURL);
          },
        }
      );
    }

    // === macOS Native Features ===
    if (isMac && hasSelection) {
      if (menuTemplate.length > 0) {
        menuTemplate.push({ type: "separator" });
      }

      // Speech submenu
      menuTemplate.push({
        label: "Speech",
        submenu: [
          { label: "Start Speaking", role: "startSpeaking" },
          { label: "Stop Speaking", role: "stopSpeaking" },
        ],
      });
    }

    // === macOS Input Helpers (editable fields only) ===
    if (isMac && isEditable) {
      if (menuTemplate.length > 0) {
        menuTemplate.push({ type: "separator" });
      }

      // Start Dictation - triggers macOS native speech-to-text
      menuTemplate.push({
        label: "Start Dictation...",
        click: () => {
          Menu.sendActionToFirstResponder("startDictation:");
        },
      });

      // Emoji & Symbols picker
      menuTemplate.push({
        label: "Emoji & Symbols",
        click: () => app.showEmojiPanel(),
        accelerator: "CmdOrCtrl+Ctrl+Space",
      });
    }

    // === Developer Tools (dev mode only) ===
    if (!app.isPackaged) {
      if (menuTemplate.length > 0) {
        menuTemplate.push({ type: "separator" });
      }

      menuTemplate.push({
        label: "Inspect Element",
        click: () => {
          mainWindow.webContents.inspectElement(params.x, params.y);
        },
      });
    }

    // Only show menu if we have items
    if (menuTemplate.length > 0) {
      const contextMenu = Menu.buildFromTemplate(menuTemplate);

      // Pass frame parameter to enable macOS native features
      // (Writing Tools, Services, AutoFill on macOS 15.1+)
      contextMenu.popup({
        window: mainWindow,
        frame: params.frame,
      });
    }
  });

  mainWindow.webContents.on("did-finish-load", async () => {
    if (!backendManager) {
      log.error("Backend manager not initialized - this should not happen");
      return;
    }

    const isReady = await backendManager.isReady();
    const port = backendManager.getPort();
    log.info(
      "[did-finish-load] Backend status - ready:",
      isReady,
      "port:",
      port
    );

    if (isReady && port) {
      log.info("Backend ready, sending port to frontend:", port);
      mainWindow.webContents.send("backend-port", port);
    } else if (!isReady) {
      log.info("Backend not ready yet, waiting...");
      try {
        // waitForReady() resolves `false` (does not throw) when the daemon
        // is idling under --non-interactive awaiting sign-in — that is not
        // an error, so the catch below is reserved for a genuine failure.
        const becameReady = await backendManager.waitForReady();
        const finalPort = backendManager.getPort();
        log.info(
          becameReady ? "Backend now ready, sending port to frontend:" : "Backend still awaiting credentials, sending port anyway:",
          finalPort
        );
        mainWindow.webContents.send("backend-port", finalPort);
      } catch (error) {
        log.error("Backend failed to become ready:", error);
        const maybePort = backendManager.getPort();
        if (maybePort) {
          log.info("Sending fallback port:", maybePort);
          mainWindow.webContents.send("backend-port", maybePort);
        }
      }
    }

    // If we have a pending project path from deep link, open it now
    if (pendingProjectPath) {
      log.debug("[Window] Opening pending project:", pendingProjectPath);
      mainWindow.webContents.send("open-project", pendingProjectPath);
      pendingProjectPath = null;
    }


    // If this window was created with the intent to have an initial tab (e.g., from cmd+n with no windows)
    if (createInitialTab) {
      log.debug("[Window] Creating initial tab as requested");
      // Small delay to ensure React app is ready to receive the message
      setTimeout(() => {
        mainWindow.webContents.send("create-new-tab");
      }, 500);
    }

    // Check for updates after window is fully loaded (only in packaged app)
    if (app.isPackaged) {
      log.info("[AutoUpdater] Window loaded, scheduling update check...");
      setTimeout(() => {
        log.info("[AutoUpdater] Running update check from did-finish-load...");
        autoUpdater.checkForUpdates().then(result => {
          log.info("[AutoUpdater] Check completed:", result);
        }).catch(err => {
          log.error("[AutoUpdater] Check failed:", err);
        });
      }, 3000); // 3 seconds after window loads
    }
  });

  mainWindow.webContents.on("did-fail-load", async (_e, code, desc, url, isMainFrame) => {
    log.error("[Window] did-fail-load:", { code, desc, url, isMainFrame });

    // Only a top-level document failure is fatal. A failed subresource leaves
    // a window that is up but possibly blank; renderer-health.js reports that
    // case, and tearing the app down here would kill it before it could.
    if (!isMainFrame) {
      return;
    }

    await dialog.showErrorBox(
      "Renderer load failed",
      `${code}: ${desc}\nURL: ${url || "(file)"}`
    );
    app.exit(1);
  });

  // Catch the blank-window failure that reports success everywhere else.
  watchRendererHealth(mainWindow, log, {
    onUnhealthy: (reason) => {
      log.error(
        "[Window] renderer never mounted — see the log for the failing assets:",
        reason
      );
    },
  });

  if (shouldAutoOpenDevTools({ isPackaged: app.isPackaged, env: process.env })) {
    log.info("[Window] RELIANT_DEVTOOLS set — opening DevTools");
    mainWindow.webContents.openDevTools({ mode: "detach" });
  }

  // Crash / responsiveness diagnostics with recovery
  mainWindow.webContents.on("crashed", (event, killed) => {
    log.error("[Window] WebContents crashed, killed:", killed);
    // Attempt recovery by reloading
    if (mainWindow && !mainWindow.isDestroyed()) {
      log.info("[Window] Attempting to recover from crash by reloading...");
      setTimeout(() => {
        if (mainWindow && !mainWindow.isDestroyed()) {
          mainWindow.webContents.reload();
        }
      }, 500);
    }
  });
  app.on("render-process-gone", (_e, webContents, details) => {
    log.error("[App] render-process-gone:", details);
    // Attempt recovery for main window
    if (mainWindow && !mainWindow.isDestroyed() && mainWindow.webContents === webContents) {
      log.info("[Window] Attempting to recover from render-process-gone by reloading...");
      setTimeout(() => {
        if (mainWindow && !mainWindow.isDestroyed()) {
          mainWindow.webContents.reload();
        }
      }, 500);
    }
  });
  app.on("child-process-gone", (_e, details) => {
    log.error("[App] child-process-gone:", details);
    // If GPU process crashed, attempt to recover main window
    if (details.type === "GPU" && mainWindow && !mainWindow.isDestroyed()) {
      log.info("[Window] GPU process crashed, attempting recovery by reloading...");
      setTimeout(() => {
        if (mainWindow && !mainWindow.isDestroyed()) {
          mainWindow.webContents.reload();
        }
      }, 500);
    }
  });
  mainWindow.on("unresponsive", () => {
    log.error("[Window] Window became unresponsive");
  });
  mainWindow.on("responsive", () => {
    log.debug("[Window] Window became responsive again");
  });

  // Capture window instance in closure to avoid issues when mainWindow is reassigned
  const currentWindow = mainWindow;

  currentWindow.on("closed", () => {
    // Only clear mainWindow if this is the current main window
    if (mainWindow === currentWindow) {
      mainWindow = null;
    }
  });

  // macOS: close -> hide to tray
  // CRITICAL: Use currentWindow (captured in closure) instead of mainWindow
  // to ensure we're checking the correct window instance
  currentWindow.on("close", (event) => {
    if (!isQuitting && process.platform === "darwin") {
      event.preventDefault();
      currentWindow.hide();
    }
  });

  // ---- Load content (AFTER handlers) ----
  log.debug("[Window] Preparing to load content...");
  log.debug("[Window] app.isPackaged:", app.isPackaged);
  log.debug("[Window] process.env.USE_DEV_SERVER:", process.env.USE_DEV_SERVER);

  if (app.isPackaged) {
    // Served over app:// rather than loadFile(). The bundle is built with
    // base "/" for the web deploy, and those root-absolute asset paths
    // resolve against the filesystem root under file:// — which is why
    // v1.6.3 opened a blank window. See app-protocol.js.
    log.info("[Window] Production mode - loading from:", APP_INDEX_URL);
    await mainWindow.loadURL(APP_INDEX_URL);
  } else {
    log.debug(`[Window] Waiting for Vite at ${DEV_URL} ...`);
    await waitForPort(DEV_PORT).catch(async () => {
      await dialog.showErrorBox(
        "Vite dev server not available",
        `Could not reach ${DEV_URL}.\n\nRun scripts/dev-electron.sh (starts Vite first).`
      );
      app.exit(1);
      return;
    });

    log.debug("[Window] Loading dev server:", DEV_URL);
    await mainWindow.loadURL(DEV_URL);
    mainWindow.webContents.once("show", () =>
      mainWindow.webContents.openDevTools({ mode: "detach" })
    );
  }

  // ---- Final debug ----
  log.debug("[Window] createWindow function completed");
  log.debug(
    "[Window] Final window state - visible:",
    mainWindow.isVisible(),
    "minimized:",
    mainWindow.isMinimized()
  );

  // Debug: Check if page is already loaded...
  log.debug("[Window] Checking if page is already loaded...");
  if (mainWindow.webContents.getURL()) {
    log.debug("[Window] Page URL:", mainWindow.webContents.getURL());
  }

  // Register window with WindowManager if available
  // This ensures all windows are properly tracked, including those created from menu
  if (windowManager && currentWindow) {
    windowManager.registerWindow(currentWindow, {
      worktreeId: null,
      projectId: null,
      projectName: "Reliant",
    });
    log.debug("[Window] Registered new window with WindowManager");
  }

  // Initialize browser manager for this window
  const windowBrowserManager = new BrowserManager();
  windowBrowserManager.initialize(currentWindow);
  currentWindow.browserManager = windowBrowserManager;
  log.debug("[Window] Browser manager initialized for window");
}

function createTray() {
  try {
    const { nativeImage } = require("electron");
    const iconPath = path.join(__dirname, "../build/iconTemplate.png");

    if (!fs.existsSync(iconPath)) {
      log.debug("Tray icon not found, creating tray without icon");
      tray = new Tray(nativeImage.createEmpty());
    } else {
      const image = nativeImage.createFromPath(iconPath);
      image.setTemplateImage(true);
      tray = new Tray(image);
    }

    loadTraySettings();
    refreshTrayMenu();
    tray.setToolTip("Reliant");

    tray.on("click", () => {
      ensureMainWindowVisible();
    });
  } catch (error) {
    log.error("Failed to create tray:", error);
    // Continue without tray rather than crashing
  }
}

async function restartBackendForAuthPrincipalChange(previousSession, nextSession, reason) {
  const change = describeAuthPrincipalChange(previousSession, nextSession);
  if (!change.changed) {
    return { restarted: false, reason: 'principal_unchanged' };
  }

  if (!backendManager) {
    log.warn('[Auth] Backend restart skipped - backend manager not initialized', {
      reason,
      previousPrincipal: change.previousPrincipal,
      nextPrincipal: change.nextPrincipal,
    });
    return { restarted: false, reason: 'backend_manager_uninitialized' };
  }

  const shouldRestart = shouldRestartBackendForAuthChange(previousSession, nextSession, {
    development: process.env.NODE_ENV === 'development',
    externalBackend: Boolean(process.env.RELIANT_EXTERNAL_BACKEND),
  });

  if (!shouldRestart) {
    log.info('[Auth] Backend restart not required for auth principal change', {
      reason,
      previousPrincipal: change.previousPrincipal,
      nextPrincipal: change.nextPrincipal,
      development: process.env.NODE_ENV === 'development',
      externalBackend: Boolean(process.env.RELIANT_EXTERNAL_BACKEND),
    });
    return { restarted: false, reason: 'restart_not_required' };
  }

  log.info('[Auth] Restarting backend after auth principal change', {
    reason,
    previousPrincipal: change.previousPrincipal,
    nextPrincipal: change.nextPrincipal,
  });

  await backendManager.stop();
  const port = await backendManager.start();

  BrowserWindow.getAllWindows().forEach((window) => {
    window.webContents.send('backend-port', port);
  });

  return { restarted: true, port };
}

// IPC handlers
// Handle logs from renderer process
ipcMain.on("log-from-renderer", (event, level, ...args) => {
  // Prefix renderer logs for clarity
  const prefix = "[Renderer]";

  switch (level) {
    case "error":
      log.error(prefix, ...args);
      break;
    case "warn":
      log.warn(prefix, ...args);
      break;
    case "debug":
      log.debug(prefix, ...args);
      break;
    case "info":
    default:
      log.info(prefix, ...args);
      break;
  }
});

ipcMain.handle("get-backend-port", async () => {
  log.info("[IPC] get-backend-port called");
  if (!backendManager) {
    log.warn("[IPC] backendManager not initialized, waiting for it...");
    // Wait a bit for backend manager to be initialized
    await new Promise((resolve) => setTimeout(resolve, 100));
    if (!backendManager) {
      log.error("[IPC] backendManager still not initialized after wait");
      return null;
    }
  }

  // First check if we already have a port
  let port = backendManager.getPort();
  log.info("[IPC] Current backend port:", port, "type:", typeof port);

  // If no port yet, backend might still be starting
  if (!port) {
    log.info("[IPC] No port available yet, waiting for backend to start...");
    try {
      // Wait for backend to start and get a port (this could take up to 15 seconds)
      await backendManager.waitForReady();
      port = backendManager.getPort();
      log.info("[IPC] Backend started, port is now:", port);
    } catch (error) {
      log.error("[IPC] Failed to wait for backend to start:", error);
      return null;
    }
  }

  // Check if backend is actually ready to receive requests
  const isReady = await backendManager.isReady();
  log.info("[IPC] Backend ready check:", isReady);

  if (isReady && port) {
    log.info(
      "[IPC] Backend is ready, returning port:",
      port,
      "type:",
      typeof port
    );
    return port;
  }

  // If backend has a port but isn't ready yet, wait for it
  if (port && !isReady) {
    log.info("[IPC] Backend has port but not ready, waiting...");
    try {
      // waitForReady() resolves `false` (does not throw) when the daemon is
      // idling under --non-interactive awaiting sign-in — expected, not an
      // error. The catch below only fires for a genuine startup failure.
      const becameReady = await backendManager.waitForReady();
      const finalPort = backendManager.getPort();
      log.info(
        becameReady ? "[IPC] Backend now ready, returning port:" : "[IPC] Backend still awaiting credentials, returning port anyway:",
        finalPort,
        "type:",
        typeof finalPort
      );
      return finalPort;
    } catch (error) {
      log.error("[IPC] Backend failed to become ready:", error);

      // In development with Air (external backend), don't show error dialogs
      // Air hot reload causes brief backend unavailability which is expected
      if (!process.env.RELIANT_EXTERNAL_BACKEND) {
        dialog.showErrorBox(
          "Backend Connection Error",
          `We're having trouble connecting to the backend service. The application may not function properly.\n\nError: ${error.message}`
        );
      } else {
        log.info("[IPC] Dev mode - suppressing error dialog during Air hot reload");
      }

      // Still return the port even if not ready, frontend will handle retries
      return port;
    }
  }

  log.error("[IPC] Unexpected state - no port available");
  return null;
});

ipcMain.handle("get-backend-status", () => {
  return backendManager ? backendManager.getStatus() : { isRunning: false };
});

ipcMain.handle("get-app-info", () => {
  return {
    isPackaged: app.isPackaged,
    platform: process.platform,
  };
});

ipcMain.handle("get-version", () => {
  return app.getVersion();
});

ipcMain.handle("tray:set-status", async (_event, status) => {
  try {
    updateTrayStatus(status);
    return { success: true };
  } catch (error) {
    log.error("[Tray] Failed to set tray status:", error);
    return { success: false, error: error.message };
  }
});

ipcMain.handle("tray:get-notification-mute-status", async () => {
  const muted = isNotificationMuted();
  return {
    muted,
    mutedUntil: muted ? trayNotificationMuteUntil : null,
    preset: getMutePreset(),
  };
});

// Store active notifications to handle clicks
const activeNotifications = new Map();

// Track the most recent notification for activation-based click detection
// This helps when app is already focused (macOS limitation)
let mostRecentNotification = null;
let appWasFocusedWhenNotificationShown = false;

// Polling mechanism to detect notification clicks when app is already focused
// On macOS, clicking a notification when app is focused doesn't fire events
// So we poll for window focus changes shortly after a notification
let notificationPollInterval = null;
function startNotificationPolling() {
  if (notificationPollInterval) {
    clearInterval(notificationPollInterval);
  }

  notificationPollInterval = setInterval(() => {
    if (mostRecentNotification && appWasFocusedWhenNotificationShown) {
      const timeSinceNotification = Date.now() - mostRecentNotification.timestamp;

      // Check if any window just got focused (within last 500ms)
      const windows = BrowserWindow.getAllWindows();
      for (const window of windows) {
        if (window.isFocused()) {
          // Window is focused - check if this happened recently
          // We can't directly detect when focus changed, but we can check if
          // window is focused and notification was recent
          if (timeSinceNotification < 2000 && timeSinceNotification > 100) {
            // Window is focused and notification was shown recently
            // This might indicate a notification click
            log.info("[Notification] 🔔🔔🔔 Polling detected focused window after notification - treating as click 🔔🔔🔔", {
              tag: mostRecentNotification.tag,
              timeSinceNotification,
              windowId: window.id,
            });

            // Send IPC message to trigger navigation
            try {
              mostRecentNotification.sender.send("notification-click", mostRecentNotification.tag);
              log.info("[Notification] ✅ Polling-based IPC message sent", { tag: mostRecentNotification.tag });
              // Clear the recent notification
              mostRecentNotification = null;
              appWasFocusedWhenNotificationShown = false;
              clearInterval(notificationPollInterval);
              notificationPollInterval = null;
              return;
            } catch (error) {
              log.error("[Notification] ❌ Failed to send polling-based IPC message", { error });
            }
          }
        }
      }

      // Stop polling after 3 seconds
      if (timeSinceNotification > 3000) {
        log.info("[Notification] Stopping notification polling - timeout", { tag: mostRecentNotification.tag });
        clearInterval(notificationPollInterval);
        notificationPollInterval = null;
        mostRecentNotification = null;
        appWasFocusedWhenNotificationShown = false;
      }
    } else {
      // No recent notification, stop polling
      if (notificationPollInterval) {
        clearInterval(notificationPollInterval);
        notificationPollInterval = null;
      }
    }
  }, 100); // Poll every 100ms
}

// Function to handle notification clicks
function handleNotificationClick(tag, sender) {
  log.info("[Notification] handleNotificationClick called", { tag });

  if (tag && activeNotifications.has(tag)) {
    const notifData = activeNotifications.get(tag);
    // Focus the window first (even if already focused, this ensures it's on top)
    const window = BrowserWindow.fromWebContents(notifData.sender || sender);
    if (window) {
      if (window.isMinimized()) window.restore();
      window.show();
      window.focus();
      // Force focus on macOS - CRITICAL for when app is already focused
      if (process.platform === 'darwin') {
        app.focus({ steal: true });
        // Also try focusing the window again after app focus
        setTimeout(() => {
          window.focus();
        }, 50);
      }
    }
    // Send IPC message to renderer to trigger navigation
    // This MUST happen even if window is already focused
    log.info("[Notification] Sending notification-click IPC message to renderer", {
      tag,
      windowId: window?.id,
      isFocused: window?.isFocused(),
    });

    // Send the IPC message - this should work regardless of focus state
    try {
      (notifData.sender || sender).send("notification-click", tag);
      log.info("[Notification] ✅ IPC message sent successfully", { tag });
    } catch (error) {
      log.error("[Notification] ❌ Failed to send IPC message", { tag, error });
    }

    // Clean up
    activeNotifications.delete(tag);
    if (mostRecentNotification?.tag === tag) {
      mostRecentNotification = null;
      appWasFocusedWhenNotificationShown = false;
    }
  } else {
    log.warn("[Notification] Notification clicked but tag not found in activeNotifications", {
      tag,
      availableTags: Array.from(activeNotifications.keys()),
    });
  }
}

// Handle notification creation from renderer
ipcMain.handle("show-notification", (event, options) => {
  try {
    const { title, body, tag, icon, silent } = options;

    if (isNotificationMuted()) {
      log.info("[Notification] Suppressed due to tray mute setting", {
        tag,
        mutedUntil: trayNotificationMuteUntil,
      });
      return { success: true, muted: true };
    }

    // Check if notifications are supported
    if (!Notification.isSupported()) {
      log.warn("[Notification] Notifications not supported on this platform");
      return { success: false, error: "Notifications not supported" };
    }

    // Create native Electron notification
    // CRITICAL: On macOS, notifications might not be clickable when app is focused
    // We need to ensure the notification is created with proper settings
    const shouldBeSilent = silent === true; // Only silent if explicitly true, default to playing sound
    log.info("[Notification] Creating notification", { title, tag, silent, shouldBeSilent });
    
    const notification = new Notification({
      title,
      body,
      icon: icon || path.join(__dirname, "../build/icon.png"),
      silent: shouldBeSilent,
      // On macOS, add urgency to make notification more interactive
      urgency: 'normal', // 'low', 'normal', 'critical'
    });

    // Store notification with tag for click handling
    if (tag) {
      const window = BrowserWindow.fromWebContents(event.sender);
      const wasFocused = window && window.isFocused();

      activeNotifications.set(tag, {
        notification,
        sender: event.sender, // Store sender to send IPC message back
        timestamp: Date.now(),
        window: window,
      });

      // Track most recent notification for activation-based detection
      mostRecentNotification = { tag, timestamp: Date.now(), sender: event.sender };
      appWasFocusedWhenNotificationShown = wasFocused || false;

      log.info("[Notification] Notification stored", {
        tag,
        wasFocused,
        windowId: window?.id,
        willStartPolling: wasFocused || false,
      });

      // Note: Disabled polling mechanism - it was too aggressive and triggered navigation
      // on any window focus, not just notification clicks. Rely on actual notification click events only.
      // if (wasFocused) {
      //   log.info("[Notification] App was focused when notification shown - starting polling mechanism", { tag });
      //   startNotificationPolling();
      // }

      // Clean up after 30 seconds
      setTimeout(() => {
        activeNotifications.delete(tag);
        if (mostRecentNotification?.tag === tag) {
          mostRecentNotification = null;
        }
      }, 30000);
    }

    // Handle notification click - CRITICAL for when app is already focused
    // This MUST work even when app is already focused
    notification.on("click", () => {
      log.info("[Notification] 🔔🔔🔔 NOTIFICATION CLICKED IN MAIN PROCESS 🔔🔔🔔", { tag });
      // Stop polling if it was running
      if (notificationPollInterval) {
        clearInterval(notificationPollInterval);
        notificationPollInterval = null;
      }
      handleNotificationClick(tag, event.sender);
    });

    // Also listen for 'show' event to track when notification appears
    notification.on("show", () => {
      log.info("[Notification] Notification shown", { tag });
      
      // Bounce dock icon on macOS when notification appears (if window not focused)
      if (process.platform === 'darwin' && app.dock) {
        const window = BrowserWindow.fromWebContents(event.sender);
        if (!window || !window.isFocused()) {
          app.dock.bounce('informational');
          log.info("[Notification] Dock bounce triggered");
        }
      }
    });

    // Listen for 'close' event
    notification.on("close", () => {
      log.info("[Notification] Notification closed", { tag });
    });

    // Also handle 'action' event (some platforms use this instead of 'click')
    notification.on("action", (actionEvent, index) => {
      log.info("[Notification] Notification action triggered", { tag, index });
      if (tag && activeNotifications.has(tag)) {
        const notifData = activeNotifications.get(tag);
        const window = BrowserWindow.fromWebContents(notifData.sender);
        if (window) {
          if (window.isMinimized()) window.restore();
          window.show();
          window.focus();
        }
        notifData.sender.send("notification-click", tag);
        activeNotifications.delete(tag);
      }
    });

    // Handle 'reply' event (for notifications with reply capability)
    notification.on("reply", (replyEvent, reply) => {
      log.info("[Notification] Notification reply", { tag, reply });
      // Treat reply as click for navigation purposes
      if (tag && activeNotifications.has(tag)) {
        const notifData = activeNotifications.get(tag);
        const window = BrowserWindow.fromWebContents(notifData.sender);
        if (window) {
          if (window.isMinimized()) window.restore();
          window.show();
          window.focus();
        }
        notifData.sender.send("notification-click", tag);
        activeNotifications.delete(tag);
      }
    });

    // Show the notification
    notification.show();

    log.info("[Notification] Notification shown", { title, tag });
    return { success: true };
  } catch (error) {
    log.error("[Notification] Failed to show notification:", error);
    return { success: false, error: error.message };
  }
});

ipcMain.handle("restart-backend", async () => {
  if (backendManager) {
    try {
      await backendManager.stop();
      const port = await backendManager.start();

      // Send new port to all windows
      BrowserWindow.getAllWindows().forEach((window) => {
        window.webContents.send("backend-port", port);
      });

      return { success: true, port };
    } catch (error) {
      return { success: false, error: error.message };
    }
  }
  return { success: false, error: "Backend manager not initialized" };
});

// Mock driver configuration handlers
ipcMain.handle("get-mock-driver-config", async () => {
  try {
    const configPath = path.join(
      app.getPath("userData"),
      "mock-driver-config.json"
    );
    if (fs.existsSync(configPath)) {
      const data = fs.readFileSync(configPath, "utf8");
      return JSON.parse(data);
    }
    return null;
  } catch (error) {
    log.error("[IPC] Failed to load mock driver config:", error);
    return null;
  }
});

ipcMain.handle("set-mock-driver-config", async (event, config) => {
  try {
    const configPath = path.join(
      app.getPath("userData"),
      "mock-driver-config.json"
    );
    fs.writeFileSync(configPath, JSON.stringify(config, null, 2));

    // Store in backend manager for next restart
    if (backendManager) {
      // mockDriverConfig removed — daemon mode doesn't support mock drivers
    }

    return true;
  } catch (error) {
    log.error("[IPC] Failed to save mock driver config:", error);
    throw error;
  }
});

ipcMain.handle("browse-mock-file", async () => {
  const result = await dialog.showOpenDialog({
    properties: ["openFile"],
    title: "Select Mock Driver Replay File",
    filters: [
      { name: "JSON Files", extensions: ["json"] },
      { name: "All Files", extensions: ["*"] },
    ],
  });

  if (!result.cancelled && result.filePaths.length > 0) {
    return result.filePaths[0];
  }
  return null;
});

ipcMain.handle("select-directory", async (event) => {
  const result = await dialog.showOpenDialog({
    properties: ["openDirectory", "createDirectory"],
    title: "Select Project Directory",
  });

  if (!result.cancelled && result.filePaths.length > 0) {
    return result.filePaths[0];
  }
  return null;
});

// Handle opening project from CLI or other sources
ipcMain.handle("open-project-directory", async (event, projectPath) => {
  log.debug("[IPC] open-project-directory:", projectPath);

  // Verify the directory exists
  if (!fs.existsSync(projectPath) || !fs.statSync(projectPath).isDirectory()) {
    return { success: false, error: "Directory does not exist" };
  }

  try {
    shell.showItemInFolder(projectPath);
    return { success: true };
  } catch (error) {
    log.error("[IPC] Error opening project directory:", error);
    return { success: false, error: error.message };
  }
});

// Open terminal in directory
ipcMain.handle("open-terminal", async (event, directoryPath) => {
  log.debug("[IPC] open-terminal:", directoryPath);

  // Verify the directory exists
  if (
    !fs.existsSync(directoryPath) ||
    !fs.statSync(directoryPath).isDirectory()
  ) {
    return { success: false, error: "Directory does not exist" };
  }

  try {
    const { exec } = require("child_process");

    // Use macOS Terminal.app to open the directory
    const command = `open -a Terminal "${directoryPath}"`;

    exec(command, (error) => {
      if (error) {
        log.error("[IPC] Failed to open terminal:", error);
      }
    });

    return { success: true };
  } catch (error) {
    log.error("[IPC] Error opening terminal:", error);
    return { success: false, error: error.message };
  }
});

// Open external URL
ipcMain.handle("open-external", async (event, url) => {
  log.debug("[IPC] open-external:", url);

  try {
    await shell.openExternal(url);
    return { success: true };
  } catch (error) {
    log.error("[IPC] Error opening external URL:", error);
    return { success: false, error: error.message };
  }
});

// Privacy settings handlers
ipcMain.handle("update-privacy-settings", async (event, settings) => {
  log.info("[IPC] update-privacy-settings:", settings);

  // TODO: Save to database via backend API instead of file
  // For now, keep file-based storage for backwards compatibility
  writePrivacySettings(settings);

  // TODO: Call hosted API to update database and dynamically switch clients
  // Example: POST ${apiUrl}/api/v2/settings/privacy
  // This will allow no-restart privacy changes

  // Update environment variable for backend (temporary until DB integration)
  if (settings.analyticsEnabled === false) {
    process.env.RELIANT_ANALYTICS_DISABLED = 'true';
    log.info("[Privacy] Analytics disabled");
  } else {
    delete process.env.RELIANT_ANALYTICS_DISABLED;
    log.info("[Privacy] Analytics enabled");
  }

  // Settings saved - restart not required once DB integration is complete
  log.info("[Privacy] Settings saved.");

  return { success: true, requiresRestart: false };
});

/**
 * The renderer pushes its effective keyboard bindings whenever they change
 * (on load, and after any remap in Settings). The menu rebuilds so its
 * accelerators match what the user actually configured — see
 * updateMenuAccelerators for why hardcoding them here is a bug.
 */
ipcMain.handle("shortcuts:update", async (_event, bindings) => {
  try {
    updateMenuAccelerators(bindings);
    return { success: true };
  } catch (error) {
    log.error("[IPC] Failed to apply shortcut bindings to menu:", error);
    return { success: false };
  }
});

ipcMain.handle("get-privacy-settings", async () => {
  log.info("[IPC] get-privacy-settings");
  const settings = {
    crashReportingEnabled: getCrashReportingEnabled(),
    analyticsEnabled: getAnalyticsEnabled(),
  };
  log.info("[IPC] Returning privacy settings:", settings);
  return settings;
});

// Install CLI command. Tries a no-sudo install first (via the shared
// cli-installer module), then escalates with osascript / pkexec only if the
// user explicitly clicked the "Install CLI Command" button and the silent
// path failed.
ipcMain.handle("install-cli", async () => {
  const { exec } = require("child_process");

  // 1) Try the silent path first (no privilege prompt). On macOS/Linux this
  // covers Homebrew users (writable /usr/local/bin) and falls back to
  // ~/.local/bin. On Windows it copies to %LOCALAPPDATA%\Reliant\bin and
  // adds that dir to the user PATH via PowerShell.
  const silent = await cliInstaller.installSilently();
  if (silent.success) {
    const msg = silent.warning
      ? `CLI installed at ${silent.target}. ${silent.warning}`
      : `CLI installed at ${silent.target}. Restart your terminal to use 'reliant'.`;
    return { success: true, message: msg };
  }

  // 2) Fall back to a sudo-prompting install on Unix-like systems.
  const source = cliInstaller.getEmbeddedBinaryPath();
  if (!source) {
    return { success: false, error: "Bundled CLI binary not found. Reinstall the app." };
  }

  const symlinkPath = "/usr/local/bin/reliant";

  if (process.platform === "darwin") {
    return new Promise((resolve) => {
      const script = `do shell script "ln -sf '${source}' '${symlinkPath}'" with administrator privileges`;
      exec(`osascript -e '${script}'`, (error) => {
        if (error) {
          log.error("[IPC] Failed to install CLI with admin privileges:", error);
          resolve({
            success: false,
            error: "Installation cancelled or failed. Please try again.",
          });
        } else {
          resolve({
            success: true,
            message: `CLI installed at ${symlinkPath}. Restart your terminal to use 'reliant'.`,
          });
        }
      });
    });
  }

  if (process.platform === "linux") {
    return new Promise((resolve) => {
      exec("which pkexec", (whichError) => {
        if (!whichError) {
          exec(`pkexec ln -sf "${source}" "${symlinkPath}"`, (error) => {
            if (error) {
              log.error("[IPC] Failed to install CLI with pkexec:", error);
              resolve({
                success: false,
                error: `Installation cancelled or failed. You can manually run:\nsudo ln -sf "${source}" ${symlinkPath}`,
              });
            } else {
              resolve({
                success: true,
                message: `CLI installed at ${symlinkPath}. Restart your terminal to use 'reliant'.`,
              });
            }
          });
        } else {
          resolve({
            success: false,
            error: `Please run this command in your terminal:\nsudo ln -sf "${source}" ${symlinkPath}`,
          });
        }
      });
    });
  }

  return {
    success: false,
    error: silent.error || "CLI installation failed. Please retry from Settings → About.",
  };
});

// Returns the current CLI install state so the renderer can decide whether
// to show the "Run this terminal command" onboarding step.
//
// Shape: { installed: boolean, path: string | null, embeddedPath: string | null }
ipcMain.handle("get-cli-status", () => {
  const installedPath = cliInstaller.getInstalledCliPath();
  return {
    installed: Boolean(installedPath),
    path: installedPath,
    embeddedPath: cliInstaller.getEmbeddedBinaryPath(),
  };
});

// Tab and window management handlers
ipcMain.handle("create-new-tab", (event) => {
  // Send create new tab event to the renderer
  const window = BrowserWindow.fromWebContents(event.sender);
  if (window) {
    window.webContents.send("create-new-tab");
  }
});

ipcMain.handle("close-current-tab", (event) => {
  // Send close current tab event to the renderer
  const window = BrowserWindow.fromWebContents(event.sender);
  if (window) {
    window.webContents.send("close-current-tab");
  }
});

ipcMain.handle("close-window-if-no-tabs", (event, tabCount) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (window && tabCount === 0) {
    window.close();
  }
});

ipcMain.handle("get-window-context", (event) => {
  if (windowManager) {
    const windowId = BrowserWindow.fromWebContents(event.sender).id;
    const metadata = windowManager.windows.get(windowId);
    return metadata
      ? {
          worktreeId: metadata.worktreeId,
          projectId: metadata.projectId,
          projectName: metadata.projectName,
        }
      : { type: "main" };
  }
  return { type: "main" };
});

// Window control handlers
ipcMain.handle("minimize-window", (event) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (window) window.minimize();
});

ipcMain.handle("maximize-window", (event) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (window) {
    window.isMaximized() ? window.unmaximize() : window.maximize();
  }
});

ipcMain.handle("close-window", (event) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (window) window.close();
});

ipcMain.handle("get-fullscreen-status", (event) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (window) {
    return window.isFullScreen();
  }
  return false;
});

ipcMain.handle("toggle-fullscreen", (event) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (window) {
    window.setFullScreen(!window.isFullScreen());
    // Send fullscreen change event to renderer
    window.webContents.send("fullscreen-changed", window.isFullScreen());
  }
});

// Auth storage IPC handlers

// Loopback receiver for provider sign-in (Google / GitHub / Apple).
//
// The renderer runs the SAME sign-in code as the web app and holds the PKCE
// verifier; all it needs from the main process is a redirect URI the system
// browser can reach. See electron/src/oauth-loopback.js for why loopback
// rather than a custom scheme, and why nothing is exchanged here.
//
// The code is delivered back by navigating the window to the app's own
// /auth/callback route, so it lands in the shared component the browser build
// uses. Navigating (rather than emitting an event the renderer must be
// listening for) is deliberate: the previous design declared an
// `onOAuthCallback` bridge in electron.d.ts that was NEVER implemented, so the
// renderer's listener could not fire in any build that shipped.
ipcMain.handle("oauth:start-redirect", async () => {
  try {
    const appOrigin = app.isPackaged ? APP_ORIGIN : DEV_URL;
    const { redirectUri } = await oauthLoopback.startOAuthRedirect(appOrigin, (callbackUrl) => {
      if (!mainWindow || mainWindow.isDestroyed()) {
        log.warn("[IPC] OAuth callback arrived with no window to deliver it to");
        return;
      }
      if (mainWindow.isMinimized()) mainWindow.restore();
      mainWindow.show();
      mainWindow.focus();
      log.info("[IPC] Delivering OAuth callback into the renderer");
      mainWindow.loadURL(callbackUrl).catch((error) => {
        log.error("[IPC] Failed to load OAuth callback route:", error);
      });
    });
    return { success: true, redirectUri };
  } catch (error) {
    log.error("[IPC] Failed to start OAuth loopback listener:", error);
    return { success: false, error: error.message };
  }
});

ipcMain.handle("auth:load", async () => {
  try {
    const session = authStorage.loadStoredAuth();
    return { success: true, session };
  } catch (error) {
    log.error("[IPC] Failed to load auth:", error);
    return { success: false, error: error.message };
  }
});

ipcMain.handle("auth:save", async (event, session) => {
  try {
    const previousSession = authStorage.loadStoredAuth();
    const previousUserId = previousSession?.user?.id || "anonymous";
    const success = authStorage.saveAuth(session);

    let backendRestart = { restarted: false };
    if (success) {
      backendRestart = await restartBackendForAuthPrincipalChange(previousSession, session, 'auth:save');
    }

    // Update Statsig user when auth changes
    if (success && statsigClient && session?.user?.id) {
      try {
        await statsigClient.updateUserAsync({ userID: session.user.id });
        log.info("[Statsig] Updated user ID:", session.user.id);

        if (previousUserId === "anonymous") {
          await trackStatsigEvent("login_succeeded", {
            auth_method: getAuthMethodFromSession(session),
            source: "auth_save",
          }, {
            userID: session.user.id,
          });
        }
      } catch (err) {
        log.warn("[Statsig] Failed to update user:", err.message);
      }
    }

    return { success, backendRestarted: backendRestart.restarted === true };
  } catch (error) {
    log.error("[IPC] Failed to save auth:", error);
    return { success: false, error: error.message };
  }
});

ipcMain.handle("auth:clear", async () => {
  try {
    const previousSession = authStorage.loadStoredAuth();
    const success = authStorage.clearAuth();

    let backendRestart = { restarted: false };
    if (success) {
      // Drop the per-origin daemon.json entry (PAT + owner sub + stable
      // daemon_id) so the next login mints fresh credentials and the server
      // assigns a fresh daemon id. Logout may precede a user switch, so we
      // must not resurrect the prior user's identity. Done regardless of
      // whether the daemon restarts here (on logout it stays warm — see
      // shouldRestartBackendForAuthChange).
      if (backendManager) {
        backendManager.clearDaemonCredsForOrigin();
      }
      backendRestart = await restartBackendForAuthPrincipalChange(previousSession, null, 'auth:clear');
    }

    // Update Statsig to anonymous when user logs out
    if (success && statsigClient) {
      try {
        await statsigClient.updateUserAsync({ userID: 'anonymous' });
        log.info("[Statsig] Updated user to anonymous (logged out)");
      } catch (err) {
        log.warn("[Statsig] Failed to update user:", err.message);
      }
    }

    return { success, backendRestarted: backendRestart.restarted === true };
  } catch (error) {
    log.error("[IPC] Failed to clear auth:", error);
    return { success: false, error: error.message };
  }
});

ipcMain.handle("analytics:track", async (_event, payload) => {
  try {
    const eventName = payload?.eventName;
    const metadata = payload?.metadata;
    const userID = payload?.userID;

    if (typeof eventName !== "string" || eventName.length === 0) {
      return { success: false, error: "Invalid eventName" };
    }

    if (metadata != null && typeof metadata !== "object") {
      return { success: false, error: "metadata must be an object" };
    }

    const tracked = await trackStatsigEvent(eventName, metadata || {}, { userID });
    return { success: tracked };
  } catch (error) {
    log.warn("[Statsig] Failed to track analytics event via IPC", error?.message || error);
    return { success: false, error: error?.message || "unknown error" };
  }
});

// Browser IPC handlers
ipcMain.handle("browser:create-tab", async (event, tabId, url, paneId) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (!window) {
    return { success: false, error: "Window not found" };
  }

  const browserManager = window.browserManager;
  if (!browserManager) {
    return { success: false, error: "Browser manager not initialized" };
  }

  return await browserManager.createTab(tabId, url, paneId);
});

ipcMain.handle("browser:close-tab", async (event, tabId) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (!window) {
    return { success: false, error: "Window not found" };
  }

  const browserManager = window.browserManager;
  if (!browserManager) {
    return { success: false, error: "Browser manager not initialized" };
  }

  return await browserManager.closeTab(tabId);
});

ipcMain.handle("browser:set-active-tab", async (event, tabId) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (!window) {
    return { success: false, error: "Window not found" };
  }

  const browserManager = window.browserManager;
  if (!browserManager) {
    return { success: false, error: "Browser manager not initialized" };
  }

  return await browserManager.setActiveTab(tabId);
});

ipcMain.handle("browser:navigate-tab", async (event, tabId, url) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (!window) {
    return { success: false, error: "Window not found" };
  }

  const browserManager = window.browserManager;
  if (!browserManager) {
    return { success: false, error: "Browser manager not initialized" };
  }

  return await browserManager.navigateTab(tabId, url);
});

ipcMain.handle("browser:go-back", async (event, tabId) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (!window) {
    return { success: false, error: "Window not found" };
  }

  const browserManager = window.browserManager;
  if (!browserManager) {
    return { success: false, error: "Browser manager not initialized" };
  }

  return await browserManager.goBack(tabId);
});

ipcMain.handle("browser:go-forward", async (event, tabId) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (!window) {
    return { success: false, error: "Window not found" };
  }

  const browserManager = window.browserManager;
  if (!browserManager) {
    return { success: false, error: "Browser manager not initialized" };
  }

  return await browserManager.goForward(tabId);
});

ipcMain.handle("browser:reload", async (event, tabId) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (!window) {
    return { success: false, error: "Window not found" };
  }

  const browserManager = window.browserManager;
  if (!browserManager) {
    return { success: false, error: "Browser manager not initialized" };
  }

  return await browserManager.reload(tabId);
});

ipcMain.handle("browser:set-bounds", async (event, bounds, paneId) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (!window) {
    log.error("[IPC] browser:set-bounds - Window not found");
    return { success: false, error: "Window not found" };
  }

  const browserManager = window.browserManager;
  if (!browserManager) {
    log.error("[IPC] browser:set-bounds - Browser manager not initialized");
    return { success: false, error: "Browser manager not initialized" };
  }

  log.info("[IPC] browser:set-bounds called", { bounds, paneId });
  browserManager.setBounds(bounds, paneId);
  return { success: true };
});

ipcMain.handle("browser:hide-all", async (event) => {
  const window = BrowserWindow.fromWebContents(event.sender);
  if (!window) {
    return { success: false, error: "Window not found" };
  }

  const browserManager = window.browserManager;
  if (!browserManager) {
    return { success: false, error: "Browser manager not initialized" };
  }

  browserManager.hideAll();
  return { success: true };
});

// Track update availability state
let currentUpdateInfo = null;
let downloadStarted = false;
let lastProgressTime = null;
let lastProgressBytes = 0;
let downloadStartTime = null;
const DOWNLOAD_STALL_TIMEOUT = 30000; // 30 seconds without progress = stalled
const DOWNLOAD_MAX_DURATION = 600000; // 10 minutes max download time
const MAX_DOWNLOAD_RETRIES = 3;

// Track whether chunked download was used (requires manual installation on macOS)
let usedChunkedDownload = false;
// Store the path to the manually downloaded update file
let chunkedDownloadPath = null;

// Auto-updater event handlers
autoUpdater.on("checking-for-update", () => {
  log.info("[AutoUpdater] Checking for updates...");
  // Reset download state when checking
  downloadStarted = false;
  lastProgressTime = null;
  lastProgressBytes = 0;
  downloadStartTime = null;
  usedChunkedDownload = false;
  chunkedDownloadPath = null;
  // Send to all windows
  BrowserWindow.getAllWindows().forEach((window) => {
    window.webContents.send("update-status", { type: "checking" });
  });
});

autoUpdater.on("update-available", async (info) => {
  log.info("[AutoUpdater] Update available:", info.version);
  // Store update info for download validation
  currentUpdateInfo = info;
  log.info("[AutoUpdater] Stored update info:", {
    version: info.version,
    path: info.path,
    url: info.url,
    files: info.files?.map(f => ({ url: f.url, size: f.size, sha512: f.sha512 ? f.sha512.substring(0, 16) + '...' : 'none' }))
  });

  await trackStatsigEvent("update_available", {
    current_version: app.getVersion(),
    new_version: info?.version || "unknown",
    release_name: info?.releaseName || "",
  });

  // Send to all windows
  BrowserWindow.getAllWindows().forEach((window) => {
    window.webContents.send("update-status", {
      type: "available",
      version: info.version,
      releaseNotes: info.releaseNotes,
    });
  });
});

autoUpdater.on("update-not-available", () => {
  log.info("[AutoUpdater] Update not available");
  // Clear update info when no update is available
  currentUpdateInfo = null;
  // Send to all windows
  BrowserWindow.getAllWindows().forEach((window) => {
    window.webContents.send("update-status", { type: "not-available" });
  });
});

autoUpdater.on("error", (err) => {
  log.error("[AutoUpdater] Error:", err.message);

  // Provide more helpful error messages for common issues
  let userFriendlyError = err.message;
  if (err.message.includes("ERR_CONTENT_LENGTH_MISMATCH")) {
    userFriendlyError = "Download failed: File size mismatch. This may be caused by Cloudflare compression. Please try again or contact support.";
    log.error("[AutoUpdater] Content-Length mismatch detected - Cloudflare may be compressing the file. Check compression rules.");
  } else if (err.message.includes("ERR_HTTP2_PROTOCOL_ERROR")) {
    userFriendlyError = "Download failed: Network protocol error. Please try again.";
    log.error("[AutoUpdater] HTTP/2 protocol error - this should be resolved by disabling HTTP/2");
  }

  // Send to all windows
  BrowserWindow.getAllWindows().forEach((window) => {
    window.webContents.send("update-status", {
      type: "error",
      error: userFriendlyError,
    });
  });
});

autoUpdater.on("download-progress", (progressObj) => {
  const speedMBps = (progressObj.bytesPerSecond / (1024 * 1024)).toFixed(2);
  const message = `Download speed: ${speedMBps} MB/s - Downloaded ${progressObj.percent.toFixed(1)}% (${progressObj.transferred}/${progressObj.total})`;
  log.info("[AutoUpdater] Download progress:", message);

  // Track progress for stall detection
  const now = Date.now();
  if (progressObj.transferred > lastProgressBytes) {
    // Progress made - update tracking
    lastProgressTime = now;
    lastProgressBytes = progressObj.transferred;
  } else if (lastProgressTime && (now - lastProgressTime) > DOWNLOAD_STALL_TIMEOUT) {
    // No progress for 30+ seconds - likely stalled
    log.error(`[AutoUpdater] Download appears stalled - no progress for ${Math.round((now - lastProgressTime) / 1000)}s`);
    log.error(`[AutoUpdater] Last progress: ${lastProgressBytes} bytes, Current: ${progressObj.transferred} bytes`);
    // Emit error to trigger retry logic
    autoUpdater.emit("error", new Error("Download stalled - no progress for 30+ seconds"));
  }

  // Check total download duration
  if (downloadStartTime && (now - downloadStartTime) > DOWNLOAD_MAX_DURATION) {
    log.error(`[AutoUpdater] Download exceeded maximum duration: ${Math.round((now - downloadStartTime) / 1000)}s`);
    autoUpdater.emit("error", new Error("Download timeout - exceeded maximum duration"));
  }

  // Mark that download has started
  downloadStarted = true;
  // Send to all windows
  BrowserWindow.getAllWindows().forEach((window) => {
    window.webContents.send("update-status", {
      type: "download-progress",
      progress: progressObj,
    });
  });
});

autoUpdater.on("update-downloaded", async (info) => {
  log.info("[AutoUpdater] Update downloaded:", info.version);

  await trackStatsigEvent("update_download_completed", {
    new_version: info?.version || "unknown",
  });

  // Send to all windows - UI will handle prompting user to restart
  BrowserWindow.getAllWindows().forEach((window) => {
    window.webContents.send("update-status", {
      type: "downloaded",
      version: info.version,
    });
  });
  // No automatic dialog - let React UI handle the restart prompt
});

// Auto-updater IPC handlers
ipcMain.handle("check-for-updates", async () => {
  if (!app.isPackaged) {
    log.info("[AutoUpdater] Skipping update check in development");
    return { error: "Updates not available in development" };
  }

  try {
    log.info("[AutoUpdater] Manual check requested");
    const result = await autoUpdater.checkForUpdates();
    const updateInfo = result ? result.updateInfo : null;
    if (updateInfo) {
      log.info("[AutoUpdater] Check completed, update available:", updateInfo.version);
      // Store update info (also stored in update-available event, but store here too for safety)
      currentUpdateInfo = updateInfo;
    } else {
      log.info("[AutoUpdater] Check completed, no update available");
      currentUpdateInfo = null;
    }
    return { success: true, updateInfo };
  } catch (error) {
    log.error("[AutoUpdater] Manual check failed:", error.message);
    currentUpdateInfo = null;
    return { error: error.message };
  }
});

/**
 * Verify the SHA512 checksum of a downloaded file
 * @param {string} filePath - Path to the file to verify
 * @param {string} expectedSha512 - Expected SHA512 hash (base64 encoded)
 * @returns {Promise<boolean>} - True if checksum matches, false otherwise
 */
async function verifyFileChecksum(filePath, expectedSha512) {
  return new Promise((resolve, reject) => {
    if (!expectedSha512) {
      log.warn("[AutoUpdater] No SHA512 checksum provided, skipping verification");
      resolve(true);
      return;
    }

    log.info(`[AutoUpdater] Verifying checksum for: ${filePath}`);
    log.info(`[AutoUpdater] Expected SHA512: ${expectedSha512.substring(0, 16)}...`);

    const hash = crypto.createHash('sha512');
    const stream = fs.createReadStream(filePath);

    stream.on('data', (data) => hash.update(data));
    stream.on('end', () => {
      const computedHash = hash.digest('base64');
      const matches = computedHash === expectedSha512;

      log.info(`[AutoUpdater] Computed SHA512: ${computedHash.substring(0, 16)}...`);
      log.info(`[AutoUpdater] Checksum ${matches ? 'VALID' : 'INVALID'}`);

      if (!matches) {
        log.error(`[AutoUpdater] Checksum mismatch!`);
        log.error(`[AutoUpdater] Expected: ${expectedSha512}`);
        log.error(`[AutoUpdater] Got: ${computedHash}`);
      }

      resolve(matches);
    });
    stream.on('error', (err) => {
      log.error(`[AutoUpdater] Error reading file for checksum: ${err.message}`);
      reject(err);
    });
  });
}

// Helper function for chunked download fallback
async function downloadWithChunkedFallback() {
  if (!currentUpdateInfo || !currentUpdateInfo.files || currentUpdateInfo.files.length === 0) {
    throw new Error("No download URL available for chunked download");
  }

  // Find the zip file for this platform
  const platform = process.platform === 'darwin' ? 'mac' : process.platform;
  const arch = process.arch === 'arm64' ? 'arm64' : 'x64';
  const zipFile = currentUpdateInfo.files.find(f =>
    f.url && f.url.includes('.zip') && f.url.includes(platform) && f.url.includes(arch)
  );

  if (!zipFile || !zipFile.url) {
    throw new Error("Could not find download URL for chunked download");
  }

  // Store the expected checksum for verification
  const expectedSha512 = zipFile.sha512;

  // Construct full URL
  // With S3 provider, electron-updater may already provide full URLs in currentUpdateInfo.files[].url
  // If not, we need to construct it from the update info or fall back to custom domain
  let downloadUrl = zipFile.url;
  if (!downloadUrl.startsWith('http://') && !downloadUrl.startsWith('https://')) {
    // URL is relative - need to construct full URL using the configured update server URL
    downloadUrl = updateServerUrl + zipFile.url;
    log.info(`[AutoUpdater] Constructed download URL from relative path: ${downloadUrl}`);
  } else {
    log.info(`[AutoUpdater] Using absolute URL from update info: ${downloadUrl}`);
  }

  log.info(`[AutoUpdater] Starting chunked download from: ${downloadUrl}`);

  // Get download directory (where electron-updater stores downloads)
  const downloadPath = path.join(app.getPath('userData'), 'pending', path.basename(zipFile.url));
  const downloadDir = path.dirname(downloadPath);

  // Ensure directory exists
  if (!fs.existsSync(downloadDir)) {
    fs.mkdirSync(downloadDir, { recursive: true });
  }

  // Use chunked downloader
  const downloader = new ChunkedDownloader(downloadUrl, downloadPath, {
    headers: {
      'Accept-Encoding': 'identity',
      'Cache-Control': 'no-cache'
    },
    onProgress: (progress) => {
      // Emit progress events like electron-updater does
      downloadStarted = true;
      lastProgressTime = Date.now();
      lastProgressBytes = progress.transferred;

      BrowserWindow.getAllWindows().forEach((window) => {
        window.webContents.send("update-status", {
          type: "download-progress",
          progress: progress,
        });
      });
    }
  });

  const result = await downloader.download();

  // Verify checksum before accepting the download
  log.info("[AutoUpdater] Download complete, verifying checksum...");
  const checksumValid = await verifyFileChecksum(result.path, expectedSha512);

  if (!checksumValid) {
    // Delete the corrupted file
    try {
      fs.unlinkSync(result.path);
      log.info("[AutoUpdater] Deleted corrupted download file");
    } catch (e) {
      log.error("[AutoUpdater] Failed to delete corrupted file:", e.message);
    }
    throw new Error("Download verification failed: checksum mismatch. The file may have been corrupted during download.");
  }

  // Mark that we used chunked download - this requires manual installation on macOS
  usedChunkedDownload = true;
  chunkedDownloadPath = result.path;
  log.info(`[AutoUpdater] Chunked download verified and complete, stored path: ${chunkedDownloadPath}`);

  // Manually trigger update-downloaded event since we bypassed electron-updater
  log.info("[AutoUpdater] Triggering update-downloaded event");
  autoUpdater.emit("update-downloaded", {
    version: currentUpdateInfo.version,
    path: result.path
  });

  return { success: true };
}

ipcMain.handle("download-update", async () => {
  if (!app.isPackaged) {
    log.info("[AutoUpdater] Skipping download in development");
    return { error: "Updates not available in development" };
  }

  // Verify that an update is available before attempting download
  if (!currentUpdateInfo) {
    const errorMsg = "No update available. Please check for updates first.";
    log.error("[AutoUpdater] Download attempted but no update info available");
    log.info("[AutoUpdater] Attempting to check for updates now...");

    // Try to check for updates first
    try {
      const checkResult = await autoUpdater.checkForUpdates();
      if (!checkResult || !checkResult.updateInfo) {
        return { error: errorMsg };
      }
      // Update info should now be set by update-available event
      if (!currentUpdateInfo) {
        return { error: errorMsg };
      }
    } catch (checkError) {
      log.error("[AutoUpdater] Failed to check for updates:", checkError.message);
      return { error: errorMsg };
    }
  }

  // Reset chunked download tracking - will be set to true if we fall back to chunked
  usedChunkedDownload = false;
  chunkedDownloadPath = null;

  // Try native electron-updater download first, fall back to chunked on Cloudflare/HTTP2 errors
  let lastError = null;
  let useChunkedDownload = false;  // Initially false to try native download first

  for (let attempt = 1; attempt <= MAX_DOWNLOAD_RETRIES; attempt++) {
    try {
      // If chunked download is needed (due to previous Cloudflare/HTTP2 errors), use it
      if (useChunkedDownload) {
        log.info(`[AutoUpdater] Using chunked download fallback (attempt ${attempt})...`);
        return await downloadWithChunkedFallback();
      }

      log.info(`[AutoUpdater] Starting native download attempt ${attempt}/${MAX_DOWNLOAD_RETRIES}...`, {
        version: currentUpdateInfo?.version,
        hasUpdateInfo: !!currentUpdateInfo
      });

      // Reset download started flag and progress tracking
      downloadStarted = false;
      lastProgressTime = null;
      lastProgressBytes = 0;
      downloadStartTime = Date.now();

      await trackStatsigEvent("update_download_started", {
        new_version: currentUpdateInfo?.version || "unknown",
      });

      // Call downloadUpdate with timeout wrapper
      const downloadPromise = autoUpdater.downloadUpdate();
      const timeoutPromise = new Promise((_, reject) => {
        setTimeout(() => {
          reject(new Error(`Download timeout after ${DOWNLOAD_MAX_DURATION / 1000}s`));
        }, DOWNLOAD_MAX_DURATION);
      });

      await Promise.race([downloadPromise, timeoutPromise]);

      // Wait a short time to verify download actually started
      // If download-progress event fires, downloadStarted will be set to true
      await new Promise(resolve => setTimeout(resolve, 2000));

      if (!downloadStarted) {
        const errorMsg = "Download did not start. The update may no longer be available or there may be a network issue.";
        log.error("[AutoUpdater] Download call completed but no progress events received");
        log.error("[AutoUpdater] This indicates the download did not actually start");
        throw new Error(errorMsg);
      }

      // Native download succeeded - electron-updater handles installation
      usedChunkedDownload = false;
      chunkedDownloadPath = null;
      log.info(`[AutoUpdater] Native download completed successfully on attempt ${attempt}`);
      return { success: true };
    } catch (error) {
      lastError = error;
      log.error(`[AutoUpdater] Download attempt ${attempt} failed:`, error.message);
      log.error("[AutoUpdater] Error details:", error);

      // Don't retry on certain errors
      if (error.message.includes("No update available") ||
          error.message.includes("not available")) {
        return { error: error.message || "Failed to start download" };
      }

      // If download stalled or timed out, switch to chunked download
      if (error.message.includes("timeout") ||
          error.message.includes("stalled") ||
          error.message.includes("ERR_CONTENT_LENGTH") ||
          error.message.includes("ERR_HTTP2")) {
        log.warn("[AutoUpdater] Download failed due to connection issues, will use chunked download fallback");
        useChunkedDownload = true;
        continue; // Try chunked download on next iteration
      }

      // If this wasn't the last attempt, wait before retrying
      if (attempt < MAX_DOWNLOAD_RETRIES) {
        const backoffDelay = Math.min(1000 * Math.pow(2, attempt - 1), 10000); // 1s, 2s, 4s, max 10s
        log.info(`[AutoUpdater] Waiting ${backoffDelay}ms before retry...`);
        await new Promise(resolve => setTimeout(resolve, backoffDelay));
      }
    }
  }

  // If we get here and haven't tried chunked download yet, try it now
  if (!useChunkedDownload && lastError) {
    log.warn("[AutoUpdater] All standard download attempts failed, trying chunked download...");
    try {
      return await downloadWithChunkedFallback();
    } catch (chunkedError) {
      log.error("[AutoUpdater] Chunked download also failed:", chunkedError.message);
    }
  }

  // All retries failed
  log.error(`[AutoUpdater] All ${MAX_DOWNLOAD_RETRIES} download attempts failed`);
  return {
    error: lastError?.message || "Download failed after multiple attempts. Please check your network connection and try again."
  };
});

ipcMain.handle("install-update", async () => {
  if (!app.isPackaged) {
    return { error: "Updates not available in development" };
  }

  try {
    log.info("[AutoUpdater] Starting update installation...");
    log.info(`[AutoUpdater] usedChunkedDownload: ${usedChunkedDownload}, chunkedDownloadPath: ${chunkedDownloadPath}`);

    // Save main window state BEFORE setting flags that skip gracefulShutdown
    try {
      if (mainWindow && !mainWindow.isDestroyed()) {
        const state = windowStateClient.getStateFromWindow(mainWindow);
        if (state) {
          windowStateClient.saveWindowStateImmediate(state);
          log.info("[AutoUpdater] Saved main window state before update");
        }
      }
    } catch (saveError) {
      log.error("[AutoUpdater] Failed to save window state:", saveError);
    }

    await trackStatsigEvent("update_install_started", {
      new_version: currentUpdateInfo?.version || "unknown",
    });

    // Set BOTH flags to prevent any interference from quit handlers
    isQuitting = true;
    isInstallingUpdate = true;
    log.info("[AutoUpdater] Set isQuitting=true and isInstallingUpdate=true");

    // Destroy tray to prevent it from keeping the app alive
    if (tray && !tray.isDestroyed()) {
      tray.destroy();
      log.info("[AutoUpdater] Tray destroyed");
    }

    // CRITICAL: Stop the backend BEFORE updating to release file locks
    // Squirrel.Mac/ShipIt needs exclusive access to replace the app bundle
    if (backendManager) {
      log.info("[AutoUpdater] Stopping backend before update...");
      try {
        await backendManager.stop();
        log.info("[AutoUpdater] Backend stopped successfully");
      } catch (stopError) {
        log.error("[AutoUpdater] Error stopping backend:", stopError);
        // Continue with update even if stop fails - emergencyStop as fallback
        backendManager.emergencyStop();
      }
    }

    // Check if we need to use manual installation (chunked download was used)
    if (usedChunkedDownload && chunkedDownloadPath && process.platform === 'darwin') {
      log.info("[AutoUpdater] Using manual macOS installation for chunked download");
      return await performManualMacOSUpdate();
    }

    // Use standard electron-updater quitAndInstall for native downloads
    // Use setImmediate to ensure IPC response is sent before quitting
    setImmediate(() => {
      log.info("[AutoUpdater] Calling quitAndInstall...");

      // Set a failsafe timeout to force exit if quitAndInstall hangs
      // This ensures the app actually closes so the updater can run on next launch
      // increased to 15s to allow slower machines/updates to process
      setTimeout(() => {
        log.warn("[AutoUpdater] Failsafe: Force quitting app after timeout");
        app.exit(0);
      }, 15000); // 15 seconds

      // Parameters: isSilent=false, isForceRunAfter=true
      autoUpdater.quitAndInstall(false, true);
    });

    return { success: true };
  } catch (error) {
    log.error("[AutoUpdater] Error during update installation:", error);
    isQuitting = false; // Reset flags on error
    isInstallingUpdate = false;
    return { error: error.message };
  }
});

/**
 * Perform manual macOS update when chunked download was used.
 * This is necessary because electron-updater's quitAndInstall() doesn't know
 * where our manually downloaded file is located.
 */
async function performManualMacOSUpdate() {
  const { spawn } = require('child_process');

  try {
    // Get paths
    const zipPath = chunkedDownloadPath;
    const currentAppPath = app.getPath('exe').replace(/\/Contents\/MacOS\/.*$/, '');
    const tempDir = path.join(app.getPath('temp'), 'reliant-update');
    const updateHelperPath = app.isPackaged
      ? path.join(process.resourcesPath, 'update-helper.sh')
      : path.join(__dirname, 'update-helper.sh');

    log.info(`[AutoUpdater] Manual update paths:`);
    log.info(`  - ZIP path: ${zipPath}`);
    log.info(`  - Current app: ${currentAppPath}`);
    log.info(`  - Temp dir: ${tempDir}`);
    log.info(`  - Helper script: ${updateHelperPath}`);

    // Verify the zip file exists
    if (!fs.existsSync(zipPath)) {
      throw new Error(`Downloaded update file not found: ${zipPath}`);
    }

    // Verify the helper script exists
    if (!fs.existsSync(updateHelperPath)) {
      log.warn(`[AutoUpdater] Helper script not found, creating inline version`);
      // Create inline helper if bundled one doesn't exist
      const inlineHelperPath = path.join(app.getPath('temp'), 'update-helper.sh');
      fs.writeFileSync(inlineHelperPath, getUpdateHelperScript(), { mode: 0o755 });
      log.info(`[AutoUpdater] Created inline helper at: ${inlineHelperPath}`);
    }

    const helperToUse = fs.existsSync(updateHelperPath)
      ? updateHelperPath
      : path.join(app.getPath('temp'), 'update-helper.sh');

    // Spawn the update helper as a detached process
    // It will wait for this process to exit, then perform the update
    const currentPid = process.pid;
    log.info(`[AutoUpdater] Spawning update helper with PID to wait for: ${currentPid}`);

    const child = spawn('/bin/bash', [
      helperToUse,
      currentPid.toString(),
      zipPath,
      currentAppPath,
      tempDir
    ], {
      detached: true,
      stdio: 'ignore',
      env: {
        ...process.env,
        RELIANT_UPDATE_LOG: path.join(app.getPath('logs'), 'update-helper.log')
      }
    });

    child.unref();
    log.info(`[AutoUpdater] Update helper spawned with PID: ${child.pid}`);

    // Give the helper a moment to start
    await new Promise(resolve => setTimeout(resolve, 500));

    // Exit the app - the helper will handle the rest
    log.info("[AutoUpdater] Exiting app to allow update helper to proceed");
    app.exit(0);

    return { success: true };
  } catch (error) {
    log.error("[AutoUpdater] Manual macOS update failed:", error);
    isQuitting = false;
    isInstallingUpdate = false;
    throw error;
  }
}

/**
 * Returns the update helper shell script content.
 * This is used as a fallback if the bundled script is not found.
 */
function getUpdateHelperScript() {
  return `#!/bin/bash
# Reliant Update Helper
# Waits for the main process to exit, then replaces the app and relaunches

LOG_FILE="\${RELIANT_UPDATE_LOG:-/tmp/reliant-update-helper.log}"

log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" >> "$LOG_FILE"
}

log "Update helper started"
log "Arguments: PID=$1 ZIP=$2 APP=$3 TEMP=$4"

WAIT_PID="$1"
ZIP_PATH="$2"
APP_PATH="$3"
TEMP_DIR="$4"

# Validate arguments
if [ -z "$WAIT_PID" ] || [ -z "$ZIP_PATH" ] || [ -z "$APP_PATH" ] || [ -z "$TEMP_DIR" ]; then
  log "ERROR: Missing required arguments"
  exit 1
fi

# Wait for the main process to exit
log "Waiting for PID $WAIT_PID to exit..."
while kill -0 "$WAIT_PID" 2>/dev/null; do
  sleep 0.5
done
log "Process $WAIT_PID has exited"

# Kill any remaining reliant-backend processes that might hold file locks
log "Checking for orphaned reliant-backend processes..."
BACKEND_PIDS=$(pgrep -f "reliant-backend" 2>/dev/null || true)
if [ -n "$BACKEND_PIDS" ]; then
  log "Found orphaned backend processes: $BACKEND_PIDS"
  for pid in $BACKEND_PIDS; do
    kill -TERM "$pid" 2>/dev/null || true
  done
  sleep 2
  for pid in $BACKEND_PIDS; do
    kill -9 "$pid" 2>/dev/null || true
  done
fi

# Also kill orphaned crashpad handlers
CRASHPAD_PIDS=$(pgrep -f "chrome_crashpad_handler.*reliant" 2>/dev/null || true)
if [ -n "$CRASHPAD_PIDS" ]; then
  for pid in $CRASHPAD_PIDS; do
    kill -9 "$pid" 2>/dev/null || true
  done
fi

# Small delay to ensure files are released
sleep 1

# Create temp directory
log "Creating temp directory: $TEMP_DIR"
rm -rf "$TEMP_DIR"
mkdir -p "$TEMP_DIR"

# Unzip the update
log "Unzipping update from: $ZIP_PATH"
unzip -q "$ZIP_PATH" -d "$TEMP_DIR"
if [ $? -ne 0 ]; then
  log "ERROR: Failed to unzip update"
  exit 1
fi

# Find the .app in the extracted files
NEW_APP=$(find "$TEMP_DIR" -maxdepth 2 -name "*.app" -type d | head -1)
if [ -z "$NEW_APP" ]; then
  log "ERROR: Could not find .app in extracted files"
  exit 1
fi
log "Found new app at: $NEW_APP"

# Backup old app (optional, just rename)
BACKUP_PATH="\${APP_PATH}.backup"
log "Backing up old app to: $BACKUP_PATH"
rm -rf "$BACKUP_PATH"
mv "$APP_PATH" "$BACKUP_PATH"
if [ $? -ne 0 ]; then
  log "ERROR: Failed to backup old app"
  exit 1
fi

# Move new app into place
log "Installing new app to: $APP_PATH"
mv "$NEW_APP" "$APP_PATH"
if [ $? -ne 0 ]; then
  log "ERROR: Failed to install new app, attempting to restore backup"
  mv "$BACKUP_PATH" "$APP_PATH"
  exit 1
fi

# Clean up
log "Cleaning up..."
rm -rf "$BACKUP_PATH"
rm -rf "$TEMP_DIR"
rm -f "$ZIP_PATH"

# Relaunch the app
log "Relaunching app..."
open "$APP_PATH"

log "Update complete!"
exit 0
`;
}

/**
 * Accelerators currently applied to the application menu.
 *
 * Starts at the defaults and is replaced when the renderer pushes the user's
 * effective bindings — see electron/src/menu-accelerators.js for why the menu
 * must not hardcode these.
 */
let menuAccelerators = defaultAccelerators();

/** Apply renderer-supplied bindings and rebuild the menu. */
function updateMenuAccelerators(bindings) {
  if (!bindings || typeof bindings !== "object") return;
  menuAccelerators = resolveMenuAccelerators(bindings);
  createApplicationMenu();
}

// Create application menu
function createApplicationMenu() {
  const accel = menuAccelerators;
  const template = [
    {
      label: "Reliant",
      submenu: [
        { role: "about" },
        { type: "separator" },
        ...(app.isPackaged
          ? [
              {
                label: "Check for Updates...",
                click: async () => {
                  const result = await autoUpdater.checkForUpdates();
                  if (!result) {
                    dialog.showMessageBox({
                      type: "info",
                      title: "No Updates",
                      message: "You are running the latest version.",
                      buttons: ["OK"],
                    });
                  }
                },
              },
              { type: "separator" },
            ]
          : []),
        {
          label: "Quit Reliant",
          accelerator: "CmdOrCtrl+Q",
          click: () => {
            isQuitting = true;
            app.quit();
          },
        },
      ],
    },
    {
      label: "File",
      submenu: [
        {
          label: "New Tab",
          accelerator: accel.newChat,
          click: () => {
            // Send to focused window to create new tab
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("create-new-tab");
            } else {
              // If no window is open, create a new window with a tab
              createWindow({ createInitialTab: true });
            }
          },
        },
        {
          label: "New Window",
          accelerator: "CmdOrCtrl+N",
          click: () => createWindow(),
        },
        { type: "separator" },
        {
          label: "Close Tab",
          accelerator: accel.closeTab,
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("close-current-tab");
            }
          },
        },
        {
          label: "Reopen Last Tab",
          accelerator: accel.reopenLastClosedFile,
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("reopen-last-tab");
            }
          },
        },
        {
          label: "Close All Tabs",
          accelerator: "CmdOrCtrl+Alt+W",
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("close-all-tabs");
            }
          },
        },
        { type: "separator" },
        {
          label: "Close Window",
          accelerator: "CmdOrCtrl+Shift+W",
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.close();
            }
          },
        },
      ],
    },
    {
      label: "Edit",
      submenu: [
        { role: "undo" },
        { role: "redo" },
        { type: "separator" },
        { role: "cut" },
        { role: "copy" },
        { role: "paste" },
        { role: "selectall" },
      ],
    },
    {
      label: "View",
      submenu: [
        // Only show reload and DevTools in development (non-packaged builds)
        ...(!app.isPackaged
          ? [
              { role: "reload" },
              { role: "toggledevtools" },
              { type: "separator" },
            ]
          : []),

        {
          label: "Search in Chat",
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("focus-chat-search");
            }
          },
        },
        {
          label: "Global Search",
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("focus-global-search");
            }
          },
        },
        { type: "separator" },
        { role: "resetzoom" },
        { role: "zoomin" },
        { role: "zoomout" },
        { type: "separator" },
        { role: "togglefullscreen" },

        // Diagnostics. These are the only tools a user has when the window
        // comes up blank, so they must be reachable from the menu bar — which
        // still works when nothing has rendered.
        { type: "separator" },
        ...(shouldShowDevToolsMenuItem()
          ? [
              {
                label: "Toggle Developer Tools",
                accelerator:
                  process.platform === "darwin" ? "Cmd+Alt+Shift+I" : "Ctrl+Shift+Alt+I",
                click: () => {
                  const focusedWindow = BrowserWindow.getFocusedWindow();
                  if (!focusedWindow) return;
                  if (focusedWindow.webContents.isDevToolsOpened()) {
                    focusedWindow.webContents.closeDevTools();
                  } else {
                    focusedWindow.webContents.openDevTools({ mode: "detach" });
                  }
                },
              },
            ]
          : []),
        {
          label: "Open Log File",
          click: async () => {
            const logPath = log.getLogPath();
            if (!logPath) {
              await dialog.showErrorBox("No log file", "The log path is not available.");
              return;
            }
            // Reveal rather than open: the user gets the folder and can hand
            // the file to us, and it does not depend on a .log association.
            shell.showItemInFolder(logPath);
          },
        },
        {
          label: "Copy Diagnostics",
          click: async () => {
            const { clipboard } = require("electron");
            const focusedWindow = BrowserWindow.getFocusedWindow();
            let backendReady = false;
            let backendPort = null;
            let apiUrl = "";
            try {
              if (backendManager) {
                backendReady = await backendManager.isReady();
                backendPort = backendManager.getPort();
                apiUrl = backendManager.apiUrl || "";
              }
            } catch {
              // Diagnostics must never fail on the way to being reported.
            }
            const report = formatDiagnosticsReport({
              version: app.getVersion(),
              electronVersion: process.versions.electron,
              platform: process.platform,
              arch: process.arch,
              logPath: log.getLogPath(),
              rendererUrl: focusedWindow ? focusedWindow.webContents.getURL() : "",
              backendReady,
              backendPort,
              apiUrl,
            });
            clipboard.writeText(report);
            log.info("[Diagnostics] copied to clipboard:\n" + report);
          },
        },
      ],
    },
    {
      label: "Terminal",
      submenu: [
        {
          label: "New Terminal",
          accelerator: accel.newTerminal,
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("new-terminal");
            }
          },
        },
        {
          label: "Toggle Terminal",
          accelerator: accel.toggleTerminal,
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("toggle-terminal");
            }
          },
        },
        { type: "separator" },
        {
          label: "Clear Terminal",
          // Deliberately NOT Cmd+K: that chord is the renderer's sequence
          // prefix (Cmd+K T, Cmd+K G, ...). A menu accelerator is handled by
          // the OS before the renderer sees the keydown, so claiming Cmd+K here
          // would swallow every sequence in the app. Clearing the terminal is
          // scoped to the terminal anyway, so it belongs to that surface.
          accelerator: "CmdOrCtrl+Shift+L",
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("clear-terminal");
            }
          },
        },
      ],
    },
    {
      label: "Window",
      submenu: [{ role: "minimize" }, { role: "close" }],
    },
    {
      label: "Navigate",
      submenu: [
        {
          label: "Next Chat",
          accelerator: accel.nextChat,
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("navigate-next-chat");
            }
          },
        },
        {
          label: "Previous Chat",
          accelerator: accel.prevChat,
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("navigate-prev-chat");
            }
          },
        },
        {
          label: "Next Sidebar Tab",
          accelerator: accel.nextRightSidebarTab,
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("navigate-next-sidebar");
            }
          },
        },
        {
          label: "Previous Sidebar Tab",
          accelerator: accel.prevRightSidebarTab,
          click: () => {
            const focusedWindow = BrowserWindow.getFocusedWindow();
            if (focusedWindow) {
              focusedWindow.webContents.send("navigate-prev-sidebar");
            }
          },
        },
      ],
    },
  ];

  const menu = Menu.buildFromTemplate(template);
  Menu.setApplicationMenu(menu);
}

// Graceful shutdown function
async function gracefulShutdown(exitCode = 0) {
  log.debug("Starting graceful shutdown...");

  // Track app close event before shutdown
  try {
    const sessionDuration = Math.floor((Date.now() - appStartTime) / 1000);
    const analyticsState = readAnalyticsState();

    let authState = "anonymous";
    try {
      const session = authStorage.loadStoredAuth();
      if (session?.user?.id) authState = "authenticated";
    } catch (_) {}

    await trackStatsigEvent("app_closed", {
      session_duration_seconds: sessionDuration,
      auth_state: authState,
      app_open_count: analyticsState.appOpenCount,
    });

    // Give Statsig a moment to flush
    if (statsigClient) {
      await statsigClient.flush();
    }
  } catch (err) {
    log.warn("[Statsig] Failed to track app_closed:", err?.message || err);
  }

  // Save main window state before closing (uses backend API for worktree-local storage)
  try {
    if (mainWindow && !mainWindow.isDestroyed()) {
      const state = windowStateClient.getStateFromWindow(mainWindow);
      if (state) {
        windowStateClient.saveWindowStateImmediate(state);
        log.debug("[WindowState] Saved main window state on shutdown");
      }
    }
  } catch (error) {
    log.error("[WindowState] Failed to save window state on shutdown:", error);
  }

  // Set a timeout for forced shutdown
  const forceQuitTimeout = setTimeout(() => {
    log.debug("Force quitting after timeout");
    // Force destroy tray before exit
    if (tray && !tray.isDestroyed()) {
      try {
        tray.destroy();
      } catch (e) {
        log.error("Error destroying tray on force quit:", e);
      }
    }
    app.exit(exitCode);
  }, 10000); // 10 seconds max

  try {
    // First, destroy tray immediately to remove from system tray
    if (tray && !tray.isDestroyed()) {
      log.debug("Destroying system tray...");
      tray.destroy();
      tray = null;
    }

    // Close all windows without triggering close event handlers
    BrowserWindow.getAllWindows().forEach((window) => {
      window.removeAllListeners("close");
      window.removeAllListeners("closed");  // Prevent removeWindowState from erasing saved state
      window.close();
    });

    // Explicitly stop the backend for clean shutdown
    // This ensures data is flushed and resources are released properly
    if (backendManager) {
      log.info("[Shutdown] Stopping backend...");
      try {
        // Use a shorter timeout for normal shutdown (5s) vs update (25s)
        // The forceQuitTimeout (10s) is our safety net
        await Promise.race([
          backendManager.stop(),
          new Promise((_, reject) => setTimeout(() => reject(new Error('Backend stop timeout')), 5000))
        ]);
        log.info("[Shutdown] Backend stopped successfully");
      } catch (stopError) {
        log.warn("[Shutdown] Backend stop failed or timed out, forcing exit:", stopError.message);
        backendManager.emergencyStop();
      }
    }

    clearTimeout(forceQuitTimeout);
    app.exit(exitCode);
  } catch (error) {
    log.error("Error during shutdown:", error);
    clearTimeout(forceQuitTimeout);
    // Force destroy tray on error
    if (tray && !tray.isDestroyed()) {
      try {
        tray.destroy();
      } catch (e) {
        log.error("Error destroying tray on error:", e);
      }
    }
    app.exit(1);
  }
}

// Ensure required directories exist
// NEW STRUCTURE:
// - ~/.reliant/ = user config (user-editable)
// - app.getPath('userData') = internal data (managed by Reliant)
// - app.getPath('logs') = logs
function ensureDirectoriesExist() {
  const homeDir = require("os").homedir();
  const userConfigDir = path.join(homeDir, ".reliant");
  const appDataDir = app.getPath("userData");
  
  const dirs = [
    // User config directories
    userConfigDir,
    path.join(userConfigDir, "worktrees"),
    // Internal app data directories
    appDataDir,
    path.join(appDataDir, "data"),
    path.join(appDataDir, "auth"),
    path.join(appDataDir, "analytics"),
    path.join(appDataDir, "cache"),
    // Logs directory
    app.getPath("logs"),
  ];

  for (const dir of dirs) {
    if (!fs.existsSync(dir)) {
      fs.mkdirSync(dir, { recursive: true });
      log.debug("Created directory:", dir);
    }
  }
}

// Scheme privileges must be declared before the app is ready, or app:// is
// treated as an opaque origin and localStorage throws in the renderer.
registerSchemePrivileges(protocol);

// Single instance lock - MUST be before protocol registration
// Only enforce in production. In development, allow multiple instances to run
// (e.g., dev build alongside production, or multiple worktrees).
// Deep link testing in dev can be done by temporarily enabling the lock if needed.
let gotTheLock = true; // Default to true in development

if (app.isPackaged) {
  gotTheLock = app.requestSingleInstanceLock();

  if (!gotTheLock) {
    log.info("[App] Another instance is already running, quitting this instance");
    app.quit();
  }
}

if (gotTheLock) {
  // Register protocol handler for deep links
  if (process.defaultApp) {
    if (process.argv.length >= 2) {
      app.setAsDefaultProtocolClient("reliant", process.execPath, [
        path.resolve(process.argv[1]),
      ]);
      // Verify it was registered
      const isRegistered = app.isDefaultProtocolClient("reliant", process.execPath, [
        path.resolve(process.argv[1]),
      ]);
      log.info("[Protocol] Verification - is registered:", isRegistered);
    }
  } else {
    app.setAsDefaultProtocolClient("reliant");

    // Verify it was registered
    const isRegistered = app.isDefaultProtocolClient("reliant");
    log.info("[Protocol] Verification - is registered:", isRegistered);
  }

  // Handle second instance (when deep link is clicked)
  app.on("second-instance", (event, commandLine, workingDirectory) => {
    log.info("[DeepLink] second-instance event received");
    log.info("[DeepLink] commandLine args:", commandLine);

    const url = commandLine.find((arg) => arg.startsWith("reliant://"));
    if (url) {
      log.info("[DeepLink] Found deep link in second instance:", url);
      handleDeepLink(url);
    } else {
      log.info("[DeepLink] No deep link found in command line args");
    }

    if (mainWindow) {
      log.info("[DeepLink] Focusing existing main window");
      if (mainWindow.isMinimized()) mainWindow.restore();
      mainWindow.focus();
    } else {
      log.warn("[DeepLink] No main window to focus!");
    }
  });

  // Handle protocol on macOS (open-url event)
  app.on("open-url", (event, url) => {
    log.info("[DeepLink] open-url event received:", url);
    event.preventDefault();

    // If app is not ready yet, wait for it
    if (!app.isReady()) {
      log.info("[DeepLink] App not ready, storing for later");
      app.whenReady().then(() => {
        handleDeepLink(url);
      });
    } else {
      // Focus existing window first, before handling the deep link
      log.info("[DeepLink] App ready, focusing window and handling");
      if (mainWindow) {
        log.info("[DeepLink] mainWindow exists, focusing");
        if (mainWindow.isMinimized()) mainWindow.restore();
        mainWindow.show();
        mainWindow.focus();
      } else {
        log.warn("[DeepLink] mainWindow does not exist!");
      }
      handleDeepLink(url);
    }
  });
}

// Handle deep links - MUST be defined before app.whenReady()
function handleDeepLink(url) {
  log.info("[DeepLink] handleDeepLink called with:", url);
  try {
    const urlObj = new URL(url);
    log.info("[DeepLink] Parsed URL - hostname:", urlObj.hostname, "pathname:", urlObj.pathname);

    if (urlObj.hostname === "open") {
      const projectPath = decodeURIComponent(
        urlObj.searchParams.get("path") || ""
      );

      if (projectPath) {
        if (mainWindow && mainWindow.webContents) {
          log.info("[DeepLink] Sending open-project event");
          mainWindow.webContents.send("open-project", projectPath);
          if (mainWindow.isMinimized()) mainWindow.restore();
          mainWindow.focus();
        } else {
          log.info("[DeepLink] No mainWindow, storing as pending");
          pendingProjectPath = projectPath;
        }
      }
    } else {
      log.warn("[DeepLink] Unknown deep link hostname:", urlObj.hostname);
    }
  } catch (error) {
    log.error("[DeepLink] Error parsing URL:", error);
  }
}

// Check if app was launched with a URL (macOS)
if (process.platform === 'darwin') {
  // On macOS, command line args might contain a deep link
  const urlArg = process.argv.find(arg => arg.startsWith('reliant://'));
  if (urlArg) {
    // Store it to be handled after app is ready
    pendingLaunchDeepLink = urlArg;
  }
}

// Initialize Sentry BEFORE app.whenReady() fires (required by Sentry SDK)
initializeSentry();

// Check for hardware acceleration disable flag (for users with GPU issues causing black screens)
// Can be enabled via command line: --disable-gpu or environment variable: RELIANT_DISABLE_GPU=1
if (process.argv.includes("--disable-gpu") || process.env.RELIANT_DISABLE_GPU === "1") {
  log.info("[App] Hardware acceleration disabled via flag");
  app.disableHardwareAcceleration();
}

// App event handlers
app.whenReady().then(async () => {
  const appStartTime = Date.now();
  log.info("[App] ═══ App Ready Event Fired ═══");
  log.debug("[App] Platform:", process.platform);
  log.debug("[App] Electron version:", process.versions.electron);
  log.debug("[App] Node version:", process.versions.node);
  log.debug("[App] Chrome version:", process.versions.chrome);

  // Serve the packaged renderer over app://. Must be registered before the
  // first window loads.
  if (app.isPackaged) {
    const webRoot = path.join(process.resourcesPath, "web");
    registerAppProtocol(protocol, webRoot, log);
    log.info("[App] ✓ app:// protocol registered for", webRoot);
  }

  // Ensure required directories exist
  const dirStart = Date.now();
  ensureDirectoriesExist();
  log.info(`[App] ✓ Directories ensured in ${Date.now() - dirStart}ms`);

  // Create application menu
  const menuStart = Date.now();
  createApplicationMenu();
  log.info(`[App] ✓ Menu created in ${Date.now() - menuStart}ms`);

  // Initialize Statsig analytics (Sentry was initialized earlier, before app.ready)
  const statsigStart = Date.now();
  initializeStatsig();
  log.info(`[App] ✓ Statsig initialized in ${Date.now() - statsigStart}ms`);

  // Initialize window manager first (synchronous, fast)
  const wmStart = Date.now();
  log.info("[WindowManager] Creating WindowManager instance...");
  windowManager = new WindowManager();
  log.info(`[WindowManager] ✓ WindowManager created in ${Date.now() - wmStart}ms`);

  // Initialize browser manager
  const browserManager = new BrowserManager();
  log.info("[BrowserManager] ✓ BrowserManager created");

  // Start backend and window creation in parallel
  const backendCreateStart = Date.now();
  log.info("[Backend] Creating BackendManager instance...");
  // Inject authStorage so BackendManager can auto-mint a daemon PAT against
  // the current --server origin before spawning the daemon binary. Without
  // this, the daemon falls back to its own interactive registration flow,
  // which is broken under headless Electron (no TTY).
  backendManager = new BackendManager({ authStorage });
  log.info(`[Backend] ✓ BackendManager created in ${Date.now() - backendCreateStart}ms`);

  // Start backend in background (don't await yet)
  //
  // backendManager.start() resolves with the daemon's port once the process
  // is spawned successfully — including the "spawned, but idling under
  // --non-interactive with no credentials yet" case (BackendManager.
  // waitForReady() resolves `false` rather than throwing for that state; see
  // its doc comment). Only a genuine startup failure — bad binary, spawn
  // error, a real timeout that is NOT "awaiting credentials" — rejects here.
  // So this .catch is reserved for real failures: never signing in is not
  // one, and must not show "Backend Error".
  const backendStartTime = Date.now();
  log.info("[Backend] Starting backend service in background...");
  const backendStartPromise = backendManager.start().then((port) => {
    log.info(`[Backend] ✓ Backend service started in ${Date.now() - backendStartTime}ms`);
    log.debug(`[Backend] Backend process running on port ${port} (may still be awaiting sign-in)`);

    // Send port to all windows regardless of daemon readiness — the
    // renderer's login page does not depend on the daemon being ready, and
    // the config-ready handshake only needs a port number to unblock.
    BrowserWindow.getAllWindows().forEach((window) => {
      log.debug("Sending backend port to window:", port);
      window.webContents.send("backend-port", port);
    });

    return port;
  }).catch((error) => {
    log.error("Failed to start backend:", error);
    dialog.showErrorBox(
      "Backend Error",
      "Failed to start the backend service. Please check the logs and try again."
    );
    throw error;
  });

  // Ensure the `reliant` CLI is on $PATH (fire-and-forget; first-run only).
  // Silent best-effort: writes to /usr/local/bin if writable, otherwise
  // ~/.local/bin (macOS/Linux) or %LOCALAPPDATA%\Reliant\bin (Windows).
  // The sudo-capable install flow remains in the `install-cli` IPC handler.
  cliInstaller.ensureInstalledOnce().catch((err) => {
    log.warn("[CLIInstaller] background install failed:", err.message);
  });

  // Create tray (fast, synchronous)
  const trayStart = Date.now();
  if (process.platform === "darwin" || process.platform === "win32") {
    createTray();
    log.info(`[App] ✓ Tray created in ${Date.now() - trayStart}ms`);
  }

  // Restore saved windows or create a new one
  // Note: createWindow() now automatically registers with WindowManager
  const windowStart = Date.now();

  // Create the main window first (with default bounds)
  log.info("[Window] Creating main window...");
  await createWindow();
  log.info(`[App] ✓ Window created in ${Date.now() - windowStart}ms`);

  if (pendingLaunchDeepLink) {
    handleDeepLink(pendingLaunchDeepLink);
    pendingLaunchDeepLink = null;
  }

  // Wait for backend to be ready
  const backendWaitStart = Date.now();
  await backendStartPromise;
  const backendWaitTime = Date.now() - backendWaitStart;
  if (backendWaitTime > 10) {
    log.info(`[Backend] Waited additional ${backendWaitTime}ms for backend to finish`);
  } else {
    log.info(`[Backend] Backend already ready (no wait needed)`);
  }

  // Restore window state from local file
  try {
    if (mainWindow && !mainWindow.isDestroyed()) {
      const savedState = windowStateClient.getWindowState();
      if (savedState) {
        windowStateClient.applyStateToWindow(mainWindow, savedState);
        log.info("[Window] Restored window state from file");
      } else {
        log.info("[Window] No saved window state found, using defaults");
      }

      // Set up window state tracking (save on resize/move)
      const saveCurrentState = () => {
        if (mainWindow && !mainWindow.isDestroyed()) {
          const state = windowStateClient.getStateFromWindow(mainWindow);
          if (state) {
            windowStateClient.saveWindowState(state);
          }
        }
      };
      mainWindow.on('resize', saveCurrentState);
      mainWindow.on('move', saveCurrentState);
      mainWindow.on('maximize', saveCurrentState);
      mainWindow.on('unmaximize', saveCurrentState);
      mainWindow.on('enter-full-screen', saveCurrentState);
      mainWindow.on('leave-full-screen', saveCurrentState);
    }
  } catch (error) {
    log.warn("[Window] Failed to restore window state:", error.message);
  }

  log.info(`[App] ✓✓✓ TOTAL APP.WHENREADY TIME: ${Date.now() - appStartTime}ms`);

  // Log update configuration
  log.info(`[AutoUpdater] app.isPackaged: ${app.isPackaged}`);
  log.info(`[AutoUpdater] Current version: ${app.getVersion()}`);
  log.info(`[AutoUpdater] Update channel: ${updateChannel}`);
  if (!app.isPackaged) {
    log.info("[AutoUpdater] Automatic checks disabled in development mode");
  } else {
    log.info("[AutoUpdater] Automatic checks enabled - will check after window loads");
  }

  app.on("activate", () => {
    const windows = BrowserWindow.getAllWindows();
    log.debug("[App] activate event - Current window count:", windows.length);
    log.debug("[App] activate event - mainWindow exists:", !!mainWindow);

    // Only create a new window if there are truly no windows
    if (windows.length === 0) {
      log.debug("[App] No windows exist, creating new window");
      createWindow();
    } else {
      // Focus the main window or the first available window
      const windowToFocus = mainWindow || windows[0];
      if (windowToFocus.isMinimized()) windowToFocus.restore();
      windowToFocus.show();
      windowToFocus.focus();
      log.debug("[App] Focused existing window");
    }
  });
});

// Trust self-signed certificates for local gRPC server (HTTPS/HTTP2)
// The backend generates self-signed certs on startup for localhost
app.on("certificate-error", (event, webContents, url, error, certificate, callback) => {
  // Only trust certs for localhost connections to our backend
  const parsedUrl = new URL(url);
  if (parsedUrl.hostname === "localhost" || parsedUrl.hostname === "127.0.0.1") {
    log.info("[TLS] Trusting self-signed certificate for local backend:", url);
    event.preventDefault();
    callback(true); // Trust the certificate
  } else {
    log.warn("[TLS] Rejecting certificate for non-local URL:", url);
    callback(false); // Don't trust external self-signed certs
  }
});

// main.ts
app.on("browser-window-created", (_e, win) => {
  win.webContents.on("render-process-gone", (_e, details) => {
    log.error("[renderer gone]", details); // details.reason, details.exitCode
  });
});

app.on("child-process-gone", (_e, details) => {
  log.error("[child gone]", details); // can catch GPU process too
});

app.on("window-all-closed", () => {
  log.info("[App] window-all-closed event triggered, platform:", process.platform, "isInstallingUpdate:", isInstallingUpdate);

  // Always quit if installing an update (even on macOS)
  if (isInstallingUpdate) {
    log.info("[App] Update installation in progress, waiting for autoUpdater to quit app");
    // CRITICAL: Do NOT call app.quit() here. autoUpdater.quitAndInstall() handles the quit sequence.
    // Calling app.quit() here creates a race condition/conflict that can hang the app.
    return;
  }

  // Normal behavior: only quit on non-macOS platforms
  if (process.platform !== "darwin") {
    isQuitting = true;
    app.quit();
  }
});

app.on("before-quit", async (event) => {
  log.info("[App] before-quit event triggered, isQuitting:", isQuitting, "isInstallingUpdate:", isInstallingUpdate);

  // CRITICAL: Do NOT prevent quit if we're installing an update
  if (isInstallingUpdate) {
    log.info("[App] Update installation in progress, allowing quit to proceed");
    return; // Let the quit happen so update can install
  }

  if (!isQuitting) {
    event.preventDefault();
    isQuitting = true;
    await gracefulShutdown();
  }
});

app.on("will-quit", (event) => {
  log.info("[App] will-quit event triggered, isQuitting:", isQuitting, "isInstallingUpdate:", isInstallingUpdate);

  // CRITICAL: Do NOT prevent quit if we're installing an update
  if (isInstallingUpdate) {
    log.info("[App] Update installation in progress, allowing quit to proceed");
    return; // Let the quit happen so update can install
  }

  // Prevent default quit to ensure cleanup
  if (!isQuitting) {
    event.preventDefault();
    isQuitting = true;
    gracefulShutdown();
  }
});

// Handle uncaught exceptions
process.on("uncaughtException", (error) => {
  log.error("Uncaught exception:", error);
  gracefulShutdown(1);
});

process.on("unhandledRejection", (reason, promise) => {
  log.error("Unhandled rejection at:", promise, "reason:", reason);
});

// Handle signals
process.on("SIGTERM", () => {
  log.info("Received SIGTERM");
  isQuitting = true;
  gracefulShutdown();
});

process.on("SIGINT", () => {
  log.info("Received SIGINT");
  isQuitting = true;
  gracefulShutdown();
});

// Export for testing
module.exports = { createWindow };