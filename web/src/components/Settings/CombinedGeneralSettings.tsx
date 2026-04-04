import { useCallback, useEffect, useMemo, useState } from "react";
import {
  AlertCircle,
  Check,
  CheckCircle2,
  ChevronDown,
  Copy,
  Eye,
  EyeOff,
  Loader2,
  Plus,
  RefreshCw,
  Settings2,
  TestTube,
  Trash2,
} from "lucide-react";
import { Toggle } from "../ui/Toggle";
import { cn } from "../../lib/utils";
import { api } from "../../api/client";
import { useGlobalDataStore } from "../../store/globalDataStore";
import {
  resetApiKeySetupDismissed,
  useApiKeySetupStore,
} from "../../store/apiKeySetupStore";
import { useCodexOAuth, useClaudeOAuth, useOAuthAvailability } from "../../hooks";

interface ProviderSummary {
  provider: string;
  displayName: string;
  hasApiKey: boolean;
  maskedKey?: string;
  configured: boolean;
  authMethod?: string;
  status?: string;
  statusMessage?: string;
}

interface CombinedGeneralSettingsProps {
  providers: ProviderSummary[];
  onProvidersUpdate?: () => void | Promise<void>;
}

interface ReliantTokenRecord {
  id: string;
  name: string;
  token_prefix: string;
  ephemeral: boolean;
  created_at: string;
  last_used_at?: string;
  expires_at?: string;
  revoked_at?: string;
}

interface ReliantAccessRecord {
  state: string;
  message: string;
  plan_id?: string;
  plan_code?: string;
  allowed_models: string[];
  request_tags: string[];
  spend: number;
  hard_budget_usd: number;
  budget_duration: string;
  rpm_limit: number;
  tpm_limit: number;
  max_parallel_requests: number;
  key_duration: string;
}

interface ReliantProviderRecord {
  configured: boolean;
  masked_token?: string;
  tokens: ReliantTokenRecord[];
  access?: ReliantAccessRecord;
}

const VISIBLE_PROVIDERS = [
  "reliant",
  "claude",
  "codex",
  "anthropic",
  "openai",
  "gemini",
  "openrouter",
] as const;

const providerConfigs = {
  reliant: {
    name: "Reliant",
    docsUrl: "",
    keyFormat: "cpat_...",
    description:
      "Managed Reliant access. Create a Reliant token here or store an existing one; runtime access is exchanged and limited by your plan.",
    usesOAuth: false,
    isManaged: true,
  },
  claude: {
    name: "Claude Code",
    docsUrl: "https://claude.ai",
    keyFormat: "",
    description:
      "Claude 4.5 and 4.6 models via Claude OAuth (uses Claude authentication)",
    usesOAuth: "claude" as const,
    isManaged: false,
  },
  codex: {
    name: "Codex (ChatGPT)",
    docsUrl: "https://github.com/openai/codex",
    keyFormat: "",
    description:
      "GPT-5.3 Codex via ChatGPT backend (uses Codex authentication)",
    usesOAuth: "codex" as const,
    isManaged: false,
  },
  anthropic: {
    name: "Anthropic",
    docsUrl: "https://console.anthropic.com/settings/keys",
    keyFormat: "sk-ant-...",
    description: "Claude models through Anthropic API keys",
    usesOAuth: false,
    isManaged: false,
  },
  openai: {
    name: "OpenAI",
    docsUrl: "https://platform.openai.com/api-keys",
    keyFormat: "sk-...",
    description: "GPT models through OpenAI API keys",
    usesOAuth: false,
    isManaged: false,
  },
  gemini: {
    name: "Google Gemini",
    docsUrl: "https://makersuite.google.com/app/apikey",
    keyFormat: "AIza...",
    description: "Gemini models through Google API keys",
    usesOAuth: false,
    isManaged: false,
  },
  openrouter: {
    name: "OpenRouter",
    docsUrl: "https://openrouter.ai/keys",
    keyFormat: "sk-or-...",
    description: "Access multiple model families through OpenRouter",
    usesOAuth: false,
    isManaged: false,
  },
} as const;

type ProviderId = keyof typeof providerConfigs;

const isProviderConnected = (provider: {
  configured: boolean;
  hasApiKey: boolean;
}) => provider.configured || provider.hasApiKey;

const statusBadgeClasses = (status?: string) => {
  switch ((status || "").toLowerCase()) {
    case "connected":
      return "bg-emerald-500/10 text-emerald-600 border border-emerald-500/20";
    case "not_configured":
      return "bg-muted text-muted-foreground border border-border";
    case "subscription_required":
    case "billing_required":
    case "failed_precondition":
      return "bg-amber-500/10 text-amber-700 border border-amber-500/20";
    case "resource_exhausted":
    case "rate_limit":
    case "quota_exceeded":
    case "quota":
    case "unauthenticated":
    case "internal":
      return "bg-red-500/10 text-red-600 border border-red-500/20";
    default:
      return "bg-muted text-muted-foreground border border-border";
  }
};

const statusLabel = (status?: string) => {
  switch ((status || "").toLowerCase()) {
    case "connected":
      return "Connected";
    case "not_configured":
      return "Not configured";
    case "subscription_required":
      return "Subscription required";
    case "billing_required":
      return "Billing required";
    case "resource_exhausted":
      return "Usage limited";
    case "failed_precondition":
      return "Setup required";
    case "unauthenticated":
      return "Invalid token";
    case "internal":
      return "Error";
    case "unavailable":
      return "Unavailable";
    default:
      return status ? status.replace(/_/g, " ") : "Unknown";
  }
};

const formatDateTime = (value?: string) => {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
};

const formatCurrency = (value?: number) => {
  if (typeof value !== "number") return "—";
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency: "USD",
    maximumFractionDigits: 2,
  }).format(value);
};

const parseErrorMessage = (errorText: string, provider: string): string => {
  const lowerError = (errorText || "").toLowerCase();

  if (provider === "reliant") {
    if (lowerError.includes("signed-in session required")) {
      return "You need to be signed in to Reliant before creating or managing tokens.";
    }
    if (
      lowerError.includes("missing authorization token") ||
      lowerError.includes("invalid authorization header")
    ) {
      return "Your Reliant session is missing or invalid. Please sign in again and retry.";
    }
    if (
      lowerError.includes("control-plane client is not configured") ||
      lowerError.includes("reliant control-plane client is not configured")
    ) {
      return "This Reliant dev server is missing RELIANT_CONTROLPLANE_URL, so managed Reliant token actions are unavailable in this worktree.";
    }
    if (lowerError.includes("not configured")) {
      return "Reliant is not configured yet. Create or store a Reliant token to continue.";
    }
    if (
      lowerError.includes("unauthenticated") ||
      lowerError.includes("invalid token") ||
      lowerError.includes("invalid reliant token")
    ) {
      return "Reliant could not verify this token or session. Try signing in again or create a fresh token.";
    }
    if (lowerError.includes("subscription")) {
      return "A Reliant subscription is required for managed access.";
    }
    if (lowerError.includes("billing")) {
      return "Reliant billing is required before this token can be used.";
    }
    if (lowerError.includes("quota")) {
      return "Reliant quota is exhausted for this token or plan.";
    }
    if (
      lowerError.includes("rate limit") ||
      lowerError.includes("resource_exhausted")
    ) {
      return "Reliant rate limit reached. Wait a moment and try again.";
    }
    if (lowerError.includes("control-plane client is not configured")) {
      return "Reliant managed access is not available in this environment.";
    }
  }

  if (provider === "claude") {
    if (lowerError.includes("not authenticated")) {
      return "Claude is not connected. Please use Login with Claude.";
    }
    if (lowerError.includes("expired")) {
      return "Claude session expired. Please reconnect with Login with Claude.";
    }
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Claude authentication failed. Please reconnect with Login with Claude.";
    }
  }

  if (provider === "codex") {
    if (lowerError.includes("not authenticated")) {
      return "Codex is not connected. Please use Login with Codex.";
    }
    if (lowerError.includes("expired")) {
      return "Codex session expired. Please reconnect with Login with Codex.";
    }
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Codex authentication failed. Please reconnect with Login with Codex.";
    }
  }

  if (provider === "openrouter") {
    if (lowerError.includes("no endpoints found matching your data policy")) {
      return "OpenRouter requires privacy settings configuration. Visit OpenRouter privacy settings, then try again.";
    }
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Invalid OpenRouter API key. Please check your key.";
    }
  }

  if (
    lowerError.includes("unauthorized") ||
    lowerError.includes("authentication") ||
    lowerError.includes("invalid") ||
    lowerError.includes("api key") ||
    lowerError.includes("401")
  ) {
    return "Invalid API key. Please check your credentials and try again.";
  }

  if (lowerError.includes("rate limit") || lowerError.includes("429")) {
    return "Rate limit exceeded. Please wait a moment before trying again.";
  }

  if (
    lowerError.includes("quota") ||
    lowerError.includes("limit") ||
    lowerError.includes("billing") ||
    lowerError.includes("usage")
  ) {
    return "Usage limit or billing issue. Please check your account status.";
  }

  return errorText.length > 200
    ? "Validation failed. Please check your credentials and try again."
    : errorText;
};

export function CombinedGeneralSettings({
  providers,
  onProvidersUpdate,
}: CombinedGeneralSettingsProps) {
  const [selectedProvider, setSelectedProvider] = useState<string>("");
  const [apiKey, setApiKey] = useState<string>("");
  const [showKey, setShowKey] = useState<boolean>(false);
  const [validating, setValidating] = useState<boolean>(false);
  const [saving, setSaving] = useState<boolean>(false);
  const [validationMessage, setValidationMessage] = useState<{
    valid: boolean;
    message: string;
  } | null>(null);

  const [editingProvider, setEditingProvider] = useState<string | null>(null);
  const [editApiKeys, setEditApiKeys] = useState<Record<string, string>>({});
  const [showEditKeys, setShowEditKeys] = useState<Record<string, boolean>>({});
  const [deletingProvider, setDeletingProvider] = useState<string | null>(null);

  const [streamingEnabled, setStreamingEnabled] = useState<boolean>(false);
  const [loadingPreferences, setLoadingPreferences] = useState<boolean>(true);

  const [reliantProvider, setReliantProvider] = useState<ReliantProviderRecord | null>(null);
  const [reliantLoading, setReliantLoading] = useState<boolean>(false);
  const [reliantActionLoading, setReliantActionLoading] = useState<string | null>(null);
  const [reliantTokenName, setReliantTokenName] = useState<string>("");
  const [createdReliantToken, setCreatedReliantToken] = useState<string>("");
  const [copiedReliantToken, setCopiedReliantToken] = useState<boolean>(false);

  const codexOAuth = useCodexOAuth();
  const claudeOAuth = useClaudeOAuth();
  const oauthAvailability = useOAuthAvailability();

  const currentReliantSummary = useMemo(
    () => providers.find((p) => p.provider === "reliant"),
    [providers]
  );

  const configuredProviders = useMemo(
    () =>
      providers.filter(
        (p) =>
          isProviderConnected(p) &&
          VISIBLE_PROVIDERS.includes(p.provider as (typeof VISIBLE_PROVIDERS)[number])
      ),
    [providers]
  );

  const availableProviders = useMemo(
    () =>
      Object.entries(providerConfigs).filter(
        ([id]) =>
          VISIBLE_PROVIDERS.includes(id as (typeof VISIBLE_PROVIDERS)[number]) &&
          !providers.find((p) => p.provider === id && isProviderConnected(p))
      ) as [ProviderId, (typeof providerConfigs)[ProviderId]][],
    [providers]
  );

  const selectedConfig = selectedProvider
    ? providerConfigs[selectedProvider as ProviderId]
    : undefined;

  const refreshReliantStatus = useCallback(async () => {
    setReliantLoading(true);
    try {
      const status = await api.settings.getReliantProviderStatus();
      setReliantProvider(status);
    } catch (error) {
      console.error("Failed to load Reliant provider status:", error);
    } finally {
      setReliantLoading(false);
    }
  }, []);

  const refreshProviderSurfaces = useCallback(
    async (hasConfiguredProvider?: boolean) => {
      await Promise.resolve(onProvidersUpdate?.());
      await refreshReliantStatus();
      await useGlobalDataStore.getState().refetchModels();
      window.dispatchEvent(new CustomEvent("api-key-saved"));
      if (typeof hasConfiguredProvider === "boolean") {
        useApiKeySetupStore.setState({
          hasApiKey: hasConfiguredProvider,
          showModal: false,
        });
      }
    },
    [onProvidersUpdate, refreshReliantStatus]
  );

  useEffect(() => {
    void refreshReliantStatus();
  }, [refreshReliantStatus]);

  useEffect(() => {
    const loadPreferences = async () => {
      try {
        const data = await api.settings.getPreferences();
        setStreamingEnabled((data.streaming_enabled as boolean) ?? false);
      } catch (error) {
        console.error("Failed to load preferences:", error);
      } finally {
        setLoadingPreferences(false);
      }
    };

    void loadPreferences();
  }, []);

  const handleStreamingToggle = async (enabled: boolean) => {
    try {
      await api.settings.updatePreferences({ streaming_enabled: enabled });
      setStreamingEnabled(enabled);
    } catch (error) {
      console.error("Failed to update streaming preference:", error);
    }
  };

  const handleCopyCreatedReliantToken = async () => {
    if (!createdReliantToken || !navigator.clipboard?.writeText) {
      return;
    }
    try {
      await navigator.clipboard.writeText(createdReliantToken);
      setCopiedReliantToken(true);
      setTimeout(() => setCopiedReliantToken(false), 2000);
    } catch (error) {
      console.error("Failed to copy Reliant token:", error);
    }
  };

  const handleDeleteProvider = async (provider: string) => {
    const config = providerConfigs[provider as ProviderId];
    if (
      !window.confirm(
        `Are you sure you want to remove ${config?.name || provider} from Reliant?`
      )
    ) {
      return;
    }

    setDeletingProvider(provider);
    setValidationMessage(null);

    try {
      await api.settings.updateProvider(provider, "");
      const remainingConfigured = providers.filter(
        (p) => p.provider !== provider && isProviderConnected(p)
      );
      const hasConfiguredProvider = remainingConfigured.length > 0;
      await refreshProviderSurfaces(hasConfiguredProvider);

      if (!hasConfiguredProvider) {
        resetApiKeySetupDismissed();
        useApiKeySetupStore.setState({ hasApiKey: false });
      }

      setEditingProvider((current) => (current === provider ? null : current));
      setValidationMessage({
        valid: true,
        message:
          provider === "reliant"
            ? "Reliant disconnected successfully."
            : `${config?.name || provider} removed successfully.`,
      });
      setTimeout(() => setValidationMessage(null), 3000);
    } catch (error) {
      const message = error instanceof Error ? error.message : "Failed to remove provider";
      setValidationMessage({
        valid: false,
        message: parseErrorMessage(message, provider),
      });
    } finally {
      setDeletingProvider(null);
    }
  };

  const handleSaveApiKey = async (provider?: string) => {
    const targetProvider = (provider || selectedProvider) as ProviderId;
    const targetKey = provider ? editApiKeys[provider] : apiKey;

    if (!targetProvider) {
      return;
    }

    if (!targetKey?.trim()) {
      setValidationMessage({
        valid: false,
        message:
          targetProvider === "reliant"
            ? "Paste a cpat_ token to store it locally."
            : "Please enter an API key.",
      });
      return;
    }

    if (!provider) {
      setSaving(true);
    }
    setValidationMessage(null);

    try {
      await api.settings.updateProvider(targetProvider, targetKey.trim());
      await refreshProviderSurfaces(true);

      if (provider) {
        setEditingProvider(null);
        setEditApiKeys((current) => ({ ...current, [provider]: "" }));
        setShowEditKeys((current) => ({ ...current, [provider]: false }));
      } else {
        setSelectedProvider("");
        setApiKey("");
        setShowKey(false);
      }

      setValidationMessage({
        valid: true,
        message:
          targetProvider === "reliant"
            ? "Reliant token saved successfully."
            : "API key saved successfully.",
      });
      setTimeout(() => setValidationMessage(null), 3000);
    } catch (error) {
      const errorText = error instanceof Error ? error.message : "Invalid API key";
      setValidationMessage({
        valid: false,
        message: parseErrorMessage(errorText, targetProvider),
      });
    } finally {
      if (!provider) {
        setSaving(false);
      }
    }
  };

  const handleValidateApiKey = async (providerOverride?: ProviderId, keyOverride?: string) => {
    const targetProvider = providerOverride || (selectedProvider as ProviderId);
    const targetKey = typeof keyOverride === "string" ? keyOverride : apiKey;

    if (!targetProvider) {
      return;
    }

    if (targetProvider !== "reliant" && !targetKey.trim()) {
      setValidationMessage({ valid: false, message: "Please enter an API key." });
      return;
    }

    setValidating(true);
    setValidationMessage(null);

    try {
      const result = await api.settings.validateProviderAPIKey(
        targetProvider,
        targetProvider === "reliant" ? targetKey.trim() : targetKey.trim()
      );
      const message = result.valid
        ? result.message ||
          (targetProvider === "reliant"
            ? "Reliant access looks healthy."
            : "Connection successful! API key is valid.")
        : parseErrorMessage(
            result.message || "Connection failed. Please check your credentials.",
            targetProvider
          );

      setValidationMessage({ valid: result.valid, message });
      if (targetProvider === "reliant") {
        await refreshReliantStatus();
      }
    } catch (error) {
      const errorText = error instanceof Error ? error.message : "Failed to validate provider";
      setValidationMessage({
        valid: false,
        message: parseErrorMessage(errorText, targetProvider),
      });
    } finally {
      setValidating(false);
    }
  };

  const handleConnectOAuth = async (oauthType: "claude" | "codex") => {
    setValidating(true);
    setValidationMessage(null);

    const oauthHook = oauthType === "claude" ? claudeOAuth : codexOAuth;
    const displayName = oauthType === "claude" ? "Claude Code" : "Codex";

    try {
      const result = await oauthHook.start();
      if (!result.ok) {
        setValidationMessage({ valid: false, message: result.message });
        return;
      }

      await refreshProviderSurfaces(true);
      setSelectedProvider("");
      setValidationMessage({
        valid: true,
        message: result.message || `Connected to ${displayName} successfully!`,
      });
      setTimeout(() => setValidationMessage(null), 3000);
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : "Connection failed";
      setValidationMessage({
        valid: false,
        message: parseErrorMessage(errorMessage, oauthType),
      });
    } finally {
      setValidating(false);
    }
  };

  const handleCreateReliantToken = async () => {
    setReliantActionLoading("create");
    setValidationMessage(null);

    try {
      const result = await api.settings.createReliantProviderToken({
        name: reliantTokenName.trim() || undefined,
      });

      if (!result.success) {
        throw new Error(result.message || "Failed to create Reliant token");
      }

      setCreatedReliantToken(result.token);
      setCopiedReliantToken(false);
      setSelectedProvider("");
      setEditingProvider("reliant");
      setApiKey("");
      setReliantTokenName("");

      if (result.token && navigator.clipboard?.writeText) {
        try {
          await navigator.clipboard.writeText(result.token);
          setCopiedReliantToken(true);
          setTimeout(() => setCopiedReliantToken(false), 2000);
        } catch (error) {
          console.error("Failed to copy created Reliant token:", error);
        }
      }

      setValidationMessage({
        valid: true,
        message:
          result.message ||
          "Reliant token created, saved locally, and copied to your clipboard.",
      });

      let refreshFailed = false;
      try {
        await refreshProviderSurfaces(true);
      } catch (error) {
        refreshFailed = true;
        console.error("Failed to refresh provider surfaces after Reliant token creation:", error);
      }

      if (refreshFailed) {
        setValidationMessage({
          valid: true,
          message:
            "Reliant token created successfully. Some follow-up refresh steps failed, so you may need to refresh the UI before status updates appear.",
        });
      }
      setTimeout(() => setValidationMessage(null), 4000);
    } catch (error) {
      const errorText = error instanceof Error ? error.message : "Failed to create Reliant token";
      setValidationMessage({
        valid: false,
        message: parseErrorMessage(errorText, "reliant"),
      });
    } finally {
      setReliantActionLoading(null);
    }
  };

  const handleRevokeReliantToken = async (tokenId: string) => {
    if (!window.confirm("Revoke this Reliant token? This cannot be undone.")) {
      return;
    }

    setReliantActionLoading(`revoke:${tokenId}`);
    setValidationMessage(null);

    try {
      const result = await api.settings.revokeReliantProviderToken(tokenId, false);
      if (!result.success) {
        throw new Error(result.message || "Failed to revoke Reliant token");
      }
      await refreshReliantStatus();
      setValidationMessage({
        valid: true,
        message: result.message || "Reliant token revoked.",
      });
      setTimeout(() => setValidationMessage(null), 3000);
    } catch (error) {
      const errorText = error instanceof Error ? error.message : "Failed to revoke Reliant token";
      setValidationMessage({
        valid: false,
        message: parseErrorMessage(errorText, "reliant"),
      });
    } finally {
      setReliantActionLoading(null);
    }
  };

  const renderValidationBanner = validationMessage ? (
    <div
      className={cn(
        "flex items-start gap-2 rounded-md border p-3 text-sm",
        validationMessage.valid
          ? "border-emerald-500/20 bg-emerald-500/10 text-emerald-700"
          : "border-red-500/20 bg-red-500/10 text-red-600"
      )}
    >
      {validationMessage.valid ? (
        <CheckCircle2 className="mt-0.5 h-4 w-4" />
      ) : (
        <AlertCircle className="mt-0.5 h-4 w-4" />
      )}
      <span>{validationMessage.message}</span>
    </div>
  ) : null;

  const renderReliantAccessSummary = () => {
    const access = reliantProvider?.access;
    const providerStatus = currentReliantSummary?.status;
    const providerMessage = currentReliantSummary?.statusMessage;

    return (
      <div className="space-y-4 rounded-lg border border-border bg-muted/20 p-4">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium">Managed access status</span>
          <span
            className={cn(
              "inline-flex items-center rounded-full px-2 py-1 text-xs font-medium",
              statusBadgeClasses(providerStatus || access?.state)
            )}
          >
            {statusLabel(providerStatus || access?.state)}
          </span>
        </div>
        <p className="text-sm text-muted-foreground">
          {providerMessage || access?.message || "Reliant status will appear here once configured."}
        </p>

        {access && (
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            <div className="rounded-md border border-border bg-background p-3">
              <div className="text-xs uppercase text-muted-foreground">Plan</div>
              <div className="mt-1 text-sm font-medium">
                {access.plan_code || access.plan_id || "—"}
              </div>
            </div>
            <div className="rounded-md border border-border bg-background p-3">
              <div className="text-xs uppercase text-muted-foreground">Spend</div>
              <div className="mt-1 text-sm font-medium">
                {formatCurrency(access.spend)}
              </div>
            </div>
            <div className="rounded-md border border-border bg-background p-3">
              <div className="text-xs uppercase text-muted-foreground">Hard budget</div>
              <div className="mt-1 text-sm font-medium">
                {formatCurrency(access.hard_budget_usd)}
              </div>
            </div>
            <div className="rounded-md border border-border bg-background p-3">
              <div className="text-xs uppercase text-muted-foreground">RPM / TPM</div>
              <div className="mt-1 text-sm font-medium">
                {access.rpm_limit || 0} / {access.tpm_limit || 0}
              </div>
            </div>
            <div className="rounded-md border border-border bg-background p-3">
              <div className="text-xs uppercase text-muted-foreground">Parallel requests</div>
              <div className="mt-1 text-sm font-medium">
                {access.max_parallel_requests || 0}
              </div>
            </div>
            <div className="rounded-md border border-border bg-background p-3">
              <div className="text-xs uppercase text-muted-foreground">Key duration</div>
              <div className="mt-1 text-sm font-medium">{access.key_duration || "—"}</div>
            </div>
          </div>
        )}

        {access?.allowed_models?.length ? (
          <div className="space-y-2">
            <div className="text-xs uppercase text-muted-foreground">Allowed models</div>
            <div className="flex flex-wrap gap-2">
              {access.allowed_models.map((model) => (
                <span
                  key={model}
                  className="rounded-full border border-border bg-background px-2 py-1 text-xs"
                >
                  {model}
                </span>
              ))}
            </div>
          </div>
        ) : null}
      </div>
    );
  };

  const renderReliantTokenList = () => (
    <div className="space-y-3 rounded-lg border border-border bg-card p-4">
      <div className="flex items-center justify-between">
        <div>
          <h4 className="text-sm font-semibold">Control-plane tokens</h4>
          <p className="text-xs text-muted-foreground">
            Reliant tokens are stored locally as control-plane credentials. Runtime model keys are exchanged on demand.
          </p>
        </div>
        <button
          type="button"
          onClick={() => void refreshReliantStatus()}
          disabled={reliantLoading}
          className="inline-flex items-center gap-2 rounded-md border border-border px-3 py-1.5 text-sm hover:bg-accent disabled:opacity-50"
        >
          {reliantLoading ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <RefreshCw className="h-4 w-4" />
          )}
          Refresh
        </button>
      </div>

      {reliantProvider?.masked_token ? (
        <div className="rounded-md border border-border bg-muted/30 p-3 text-sm">
          <div className="text-xs uppercase text-muted-foreground">Stored token</div>
          <div className="mt-1 font-mono text-foreground">{reliantProvider.masked_token}</div>
        </div>
      ) : null}

      {createdReliantToken ? (
        <div className="rounded-md border border-emerald-500/20 bg-emerald-500/10 p-3">
          <div className="flex items-start justify-between gap-3">
            <div>
              <div className="text-sm font-medium text-emerald-700">New Reliant token</div>
              <p className="mt-1 break-all font-mono text-xs text-emerald-700/90">
                {createdReliantToken}
              </p>
              <p className="mt-2 text-xs text-emerald-700/80">
                This token is shown once. Copy it now if you need it outside Reliant.
              </p>
            </div>
            <button
              type="button"
              onClick={() => void handleCopyCreatedReliantToken()}
              className="inline-flex items-center gap-2 rounded-md border border-emerald-500/30 bg-background px-3 py-1.5 text-sm text-emerald-700 hover:bg-background/80"
            >
              <Copy className="h-4 w-4" />
              {copiedReliantToken ? "Copied" : "Copy"}
            </button>
          </div>
        </div>
      ) : null}

      <div className="space-y-2">
        {reliantProvider?.tokens?.length ? (
          reliantProvider.tokens.map((token) => (
            <div
              key={token.id}
              className="flex flex-col gap-3 rounded-md border border-border bg-background p-3 md:flex-row md:items-center md:justify-between"
            >
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{token.name || token.token_prefix}</span>
                  <span className="rounded-full border border-border px-2 py-0.5 text-xs text-muted-foreground">
                    {token.token_prefix}
                  </span>
                  {token.ephemeral ? (
                    <span className="rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
                      Ephemeral
                    </span>
                  ) : null}
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
                  Created {formatDateTime(token.created_at)}
                  {token.last_used_at ? ` • Last used ${formatDateTime(token.last_used_at)}` : ""}
                  {token.expires_at ? ` • Expires ${formatDateTime(token.expires_at)}` : ""}
                </div>
              </div>
              <button
                type="button"
                onClick={() => void handleRevokeReliantToken(token.id)}
                disabled={reliantActionLoading === `revoke:${token.id}`}
                className="inline-flex items-center gap-2 rounded-md border border-destructive/20 px-3 py-1.5 text-sm text-destructive hover:bg-destructive/10 disabled:opacity-50"
              >
                {reliantActionLoading === `revoke:${token.id}` ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <Trash2 className="h-4 w-4" />
                )}
                Revoke
              </button>
            </div>
          ))
        ) : (
          <div className="rounded-md border border-dashed border-border p-4 text-sm text-muted-foreground">
            No Reliant tokens returned from the control plane yet.
          </div>
        )}
      </div>
    </div>
  );

  const renderReliantManager = (mode: "add" | "edit") => {
    const editValue = editApiKeys.reliant || "";
    const showingEditValue = mode === "edit";
    const inputValue = showingEditValue ? editValue : apiKey;
    const inputShown = showingEditValue ? !!showEditKeys.reliant : showKey;
    const setInputValue = (value: string) => {
      if (showingEditValue) {
        setEditApiKeys((current) => ({ ...current, reliant: value }));
      } else {
        setApiKey(value);
      }
      setValidationMessage(null);
    };

    return (
      <div className="space-y-4">
        <div className="rounded-lg border border-border bg-muted/30 p-4">
          <div className="space-y-2">
            <p className="text-sm font-medium text-foreground">Managed Reliant access</p>
            <p className="text-sm text-muted-foreground">
              Create a Reliant token here and Reliant will store it locally, then exchange it
              for runtime model access automatically.
            </p>
          </div>
        </div>

        {renderValidationBanner}
        {renderReliantAccessSummary()}

        <div className="space-y-4 rounded-lg border border-primary/20 bg-primary/5 p-4">
          <div className="space-y-1">
            <p className="text-sm font-medium text-foreground">Create a Reliant token</p>
            <p className="text-sm text-muted-foreground">
              This is the normal setup path. The token is stored locally right away and the full
              cpat_ value is shown once in case you need to copy it elsewhere.
            </p>
          </div>
          <input
            type="text"
            value={reliantTokenName}
            onChange={(e) => setReliantTokenName(e.target.value)}
            placeholder="Optional token name"
            className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
          />
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              onClick={() => void handleCreateReliantToken()}
              disabled={reliantActionLoading === "create"}
              className="inline-flex items-center gap-2 rounded-md bg-primary px-4 py-2 text-sm text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
            >
              {reliantActionLoading === "create" ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <Plus className="h-4 w-4" />
              )}
              Create token
            </button>
            <button
              type="button"
              onClick={() => void refreshReliantStatus()}
              disabled={reliantLoading}
              className="inline-flex items-center gap-2 rounded-md border border-border px-4 py-2 text-sm hover:bg-accent disabled:opacity-50"
            >
              {reliantLoading ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <RefreshCw className="h-4 w-4" />
              )}
              Refresh status
            </button>
          </div>
        </div>

        <details className="rounded-lg border border-border bg-card p-4">
          <summary className="cursor-pointer list-none text-sm font-medium text-foreground">
            Use an existing token instead
          </summary>
          <div className="mt-4 space-y-2">
            <p className="text-sm text-muted-foreground">
              Paste a cpat_ token only if you already created one in another Reliant client or on
              another device.
            </p>
            <div className="relative">
              <input
                type={inputShown ? "text" : "password"}
                value={inputValue}
                onChange={(e) => setInputValue(e.target.value)}
                placeholder="cpat_..."
                className="w-full rounded-md border border-input bg-background px-3 py-2 pr-10 font-mono text-sm"
              />
              <button
                type="button"
                onClick={() => {
                  if (showingEditValue) {
                    setShowEditKeys((current) => ({
                      ...current,
                      reliant: !current.reliant,
                    }));
                  } else {
                    setShowKey((current) => !current);
                  }
                }}
                className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              >
                {inputShown ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            </div>
            <div className="flex flex-wrap gap-2">
              <button
                type="button"
                onClick={() => void handleValidateApiKey("reliant", inputValue)}
                disabled={validating}
                className="inline-flex items-center gap-2 rounded-md border border-border px-4 py-2 text-sm hover:bg-accent disabled:opacity-50"
              >
                {validating ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <TestTube className="h-4 w-4" />
                )}
                Check access
              </button>
              <button
                type="button"
                onClick={() => void handleSaveApiKey(showingEditValue ? "reliant" : undefined)}
                disabled={!inputValue.trim() || saving}
                className="inline-flex items-center gap-2 rounded-md border border-primary/30 bg-primary/10 px-4 py-2 text-sm text-primary hover:bg-primary/15 disabled:opacity-50"
              >
                {saving ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <Check className="h-4 w-4" />
                )}
                {showingEditValue ? "Save token" : "Store token"}
              </button>
            </div>
          </div>
        </details>

        {renderReliantTokenList()}
      </div>
    );
  };

  return (
    <div className="space-y-6">
      <div data-onboarding="ai-providers-settings">
        <h2 className="text-2xl font-bold tracking-tight">AI Provider Configuration</h2>
        <p className="text-muted-foreground">
          Connect your AI providers to enable model access and conversations.
        </p>
      </div>

      {availableProviders.length > 0 && (
        <div className="rounded-lg border border-border bg-card p-6">
          <h3 className="mb-4 text-lg font-semibold">Add New Provider</h3>

          <div className="space-y-4">
            <div className="space-y-2">
              <label className="text-sm font-medium">Select Provider</label>
              <div className="relative">
                <select
                  value={selectedProvider}
                  onChange={(e) => {
                    setSelectedProvider(e.target.value);
                    setApiKey("");
                    setShowKey(false);
                    setCreatedReliantToken("");
                    setValidationMessage(null);
                  }}
                  className="w-full appearance-none rounded-md border border-input bg-background px-3 py-2 pr-10"
                >
                  <option value="">Choose a provider...</option>
                  {availableProviders.map(([id, config]) => (
                    <option key={id} value={id}>
                      {config.name}
                    </option>
                  ))}
                </select>
                <ChevronDown className="pointer-events-none absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
              </div>
            </div>

            {selectedProvider && selectedConfig && (
              <>
                {selectedConfig.isManaged ? (
                  renderReliantManager("add")
                ) : selectedConfig.usesOAuth ? (
                  <div className="space-y-4">
                    <div className="rounded-lg border border-border bg-muted/30 p-4">
                      <p className="text-sm font-medium text-foreground">
                        Authenticate via {selectedConfig.name}
                      </p>
                      <p className="text-sm text-muted-foreground">
                        {oauthAvailability.available
                          ? `Sign in with ${providerConfigs[selectedProvider as ProviderId]?.name} to connect your account.`
                          : "The local OAuth helper is not running. Start it in your terminal to enable login:"}
                      </p>
                      {!oauthAvailability.available && !oauthAvailability.loading && (
                        <code className="block mt-2 px-3 py-2 text-sm bg-background border border-border rounded-md font-mono select-all">
                          reliant auth serve
                        </code>
                      )}
                    </div>

                    {renderValidationBanner}

                    <div className="flex justify-end">
                      {oauthAvailability.available ? (
                        <button
                          type="button"
                          onClick={() => void handleConnectOAuth(selectedConfig.usesOAuth)}
                          disabled={validating}
                          className="inline-flex items-center gap-2 rounded-md border border-primary/30 bg-primary/10 px-4 py-2 text-sm text-primary hover:bg-primary/15 disabled:opacity-50"
                        >
                          {validating ? (
                            <>
                              <Loader2 className="h-4 w-4 animate-spin" />
                              Connecting...
                            </>
                          ) : (
                            <>Login with {selectedConfig.name}</>
                          )}
                        </button>
                      ) : (
                        <button
                          className="px-4 py-2 text-sm font-medium border rounded-md transition-colors disabled:opacity-50"
                          onClick={oauthAvailability.recheck}
                          disabled={oauthAvailability.loading}
                        >
                          {oauthAvailability.loading ? "Checking…" : "Retry"}
                        </button>
                      )}
                    </div>
                  </div>
                ) : (
                  <div className="space-y-4">
                    <div className="rounded-lg border border-border bg-muted/30 p-4">
                      <p className="text-sm font-medium text-foreground">{selectedConfig.name}</p>
                      <p className="mt-1 text-sm text-muted-foreground">
                        {selectedConfig.description}
                      </p>
                    </div>

                    <div className="space-y-2">
                      <label className="text-sm font-medium">API key</label>
                      <div className="relative">
                        <input
                          type={showKey ? "text" : "password"}
                          value={apiKey}
                          onChange={(e) => {
                            setApiKey(e.target.value);
                            setValidationMessage(null);
                          }}
                          placeholder={selectedConfig.keyFormat}
                          className="w-full rounded-md border border-input bg-background px-3 py-2 pr-10 font-mono text-sm"
                        />
                        <button
                          type="button"
                          onClick={() => setShowKey((current) => !current)}
                          className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                        >
                          {showKey ? (
                            <EyeOff className="h-4 w-4" />
                          ) : (
                            <Eye className="h-4 w-4" />
                          )}
                        </button>
                      </div>
                      <p className="text-xs text-muted-foreground">
                        Get your API key from{" "}
                        <a
                          href={selectedConfig.docsUrl}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="text-primary hover:underline"
                        >
                          {selectedConfig.name}
                        </a>
                        .
                      </p>
                    </div>

                    {renderValidationBanner}

                    <div className="flex flex-wrap gap-2">
                      <button
                        type="button"
                        onClick={() => void handleValidateApiKey()}
                        disabled={!apiKey.trim() || validating}
                        className="inline-flex items-center gap-2 rounded-md border border-border px-4 py-2 text-sm hover:bg-accent disabled:opacity-50"
                      >
                        {validating ? (
                          <Loader2 className="h-4 w-4 animate-spin" />
                        ) : (
                          <TestTube className="h-4 w-4" />
                        )}
                        Test Connection
                      </button>
                      <button
                        type="button"
                        onClick={() => void handleSaveApiKey()}
                        disabled={!apiKey.trim() || saving}
                        className="inline-flex items-center gap-2 rounded-md bg-primary px-4 py-2 text-sm text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
                      >
                        {saving ? (
                          <Loader2 className="h-4 w-4 animate-spin" />
                        ) : (
                          <Plus className="h-4 w-4" />
                        )}
                        Add Provider
                      </button>
                    </div>
                  </div>
                )}
              </>
            )}
          </div>
        </div>
      )}

      {configuredProviders.length > 0 && (
        <div>
          <h3 className="mb-4 text-lg font-semibold">Configured Providers</h3>
          <div className="space-y-3">
            {configuredProviders.map((provider) => {
              const config = providerConfigs[provider.provider as ProviderId];
              const isReliant = provider.provider === "reliant";
              const badgeStatus = provider.status || (isProviderConnected(provider) ? "connected" : "not_configured");

              return (
                <div
                  key={provider.provider}
                  className="rounded-lg border border-border bg-card p-4"
                >
                  <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
                    <div className="flex items-start gap-3">
                      <div className="flex h-8 w-8 items-center justify-center rounded-full bg-primary/10">
                        <span className="text-xs font-semibold text-primary">
                          {config?.name?.charAt(0) || "P"}
                        </span>
                      </div>
                      <div className="space-y-1">
                        <h4 className="font-semibold">{provider.displayName}</h4>
                        <div className="flex flex-wrap items-center gap-2">
                          <span
                            className={cn(
                              "inline-flex items-center rounded-full px-2 py-1 text-xs font-medium",
                              statusBadgeClasses(badgeStatus)
                            )}
                          >
                            {statusLabel(badgeStatus)}
                          </span>
                          {provider.authMethod ? (
                            <span className="rounded-full border border-border px-2 py-1 text-xs text-muted-foreground">
                              {provider.authMethod === "oauth"
                                ? "OAuth"
                                : provider.authMethod === "reliant"
                                  ? "Managed"
                                  : "API key"}
                            </span>
                          ) : null}
                          {(provider.maskedKey || (isReliant && reliantProvider?.masked_token)) ? (
                            <span className="font-mono text-sm text-muted-foreground" data-sentry-mask>
                              {provider.maskedKey || reliantProvider?.masked_token}
                            </span>
                          ) : null}
                        </div>
                        {provider.statusMessage ? (
                          <p className="text-sm text-muted-foreground">
                            {provider.statusMessage}
                          </p>
                        ) : null}
                      </div>
                    </div>

                    <div className="flex flex-wrap items-center gap-2">
                      {isReliant ? (
                        <button
                          type="button"
                          onClick={() =>
                            setEditingProvider((current) =>
                              current === provider.provider ? null : provider.provider
                            )
                          }
                          className="inline-flex items-center gap-2 rounded-md border border-border px-3 py-1.5 text-sm hover:bg-accent"
                        >
                          <Settings2 className="h-4 w-4" />
                          {editingProvider === provider.provider ? "Hide" : "Manage"}
                        </button>
                      ) : !config?.usesOAuth ? (
                        <button
                          type="button"
                          onClick={() => {
                            if (editingProvider === provider.provider) {
                              setEditingProvider(null);
                              setEditApiKeys((current) => ({
                                ...current,
                                [provider.provider]: "",
                              }));
                              setShowEditKeys((current) => ({
                                ...current,
                                [provider.provider]: false,
                              }));
                            } else {
                              setEditingProvider(provider.provider);
                              setEditApiKeys((current) => ({
                                ...current,
                                [provider.provider]: "",
                              }));
                            }
                            setValidationMessage(null);
                          }}
                          className="inline-flex items-center gap-2 rounded-md border border-border px-3 py-1.5 text-sm hover:bg-accent"
                        >
                          <Settings2 className="h-4 w-4" />
                          {editingProvider === provider.provider ? "Cancel" : "Update"}
                        </button>
                      ) : null}

                      {isReliant ? (
                        <button
                          type="button"
                          onClick={() => void refreshReliantStatus()}
                          disabled={reliantLoading}
                          className="inline-flex items-center gap-2 rounded-md border border-border px-3 py-1.5 text-sm hover:bg-accent disabled:opacity-50"
                        >
                          {reliantLoading ? (
                            <Loader2 className="h-4 w-4 animate-spin" />
                          ) : (
                            <RefreshCw className="h-4 w-4" />
                          )}
                          Refresh
                        </button>
                      ) : null}

                      <button
                        type="button"
                        onClick={() => void handleDeleteProvider(provider.provider)}
                        disabled={deletingProvider === provider.provider}
                        className="inline-flex items-center gap-2 rounded-md border border-destructive/20 px-3 py-1.5 text-sm text-destructive hover:bg-destructive/10 disabled:opacity-50"
                      >
                        {deletingProvider === provider.provider ? (
                          <Loader2 className="h-4 w-4 animate-spin" />
                        ) : (
                          <Trash2 className="h-4 w-4" />
                        )}
                        {config?.usesOAuth || isReliant ? "Disconnect" : "Delete"}
                      </button>
                    </div>
                  </div>

                  {renderValidationBanner}

                  {editingProvider === provider.provider && isReliant ? (
                    <div className="mt-4 border-t border-border pt-4">
                      {renderReliantManager("edit")}
                    </div>
                  ) : null}

                  {editingProvider === provider.provider && !isReliant && !config?.usesOAuth ? (
                    <div className="mt-4 space-y-4 border-t border-border pt-4">
                      <div className="space-y-2">
                        <label className="text-sm font-medium">Update API Key</label>
                        <div className="relative">
                          <input
                            type={showEditKeys[provider.provider] ? "text" : "password"}
                            value={editApiKeys[provider.provider] || ""}
                            onChange={(e) => {
                              setEditApiKeys((current) => ({
                                ...current,
                                [provider.provider]: e.target.value,
                              }));
                              setValidationMessage(null);
                            }}
                            placeholder="Enter new API key to update"
                            className="w-full rounded-md border border-input bg-background px-3 py-2 pr-10 font-mono text-sm"
                          />
                          <button
                            type="button"
                            onClick={() =>
                              setShowEditKeys((current) => ({
                                ...current,
                                [provider.provider]: !current[provider.provider],
                              }))
                            }
                            className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                          >
                            {showEditKeys[provider.provider] ? (
                              <EyeOff className="h-4 w-4" />
                            ) : (
                              <Eye className="h-4 w-4" />
                            )}
                          </button>
                        </div>
                        <p className="text-xs text-muted-foreground">
                          Existing API keys cannot be viewed. Enter a new key to replace the current one.
                        </p>
                      </div>

                      <div className="flex flex-wrap gap-2">
                        <button
                          type="button"
                          onClick={() =>
                            void handleValidateApiKey(
                              provider.provider as ProviderId,
                              editApiKeys[provider.provider] || ""
                            )
                          }
                          disabled={!editApiKeys[provider.provider] || validating}
                          className="inline-flex items-center gap-2 rounded-md border border-border px-4 py-2 text-sm hover:bg-accent disabled:opacity-50"
                        >
                          {validating ? (
                            <Loader2 className="h-4 w-4 animate-spin" />
                          ) : (
                            <TestTube className="h-4 w-4" />
                          )}
                          Test Connection
                        </button>
                        <button
                          type="button"
                          onClick={() => void handleSaveApiKey(provider.provider)}
                          disabled={!editApiKeys[provider.provider]}
                          className="inline-flex items-center gap-2 rounded-md bg-primary px-4 py-2 text-sm text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
                        >
                          <Check className="h-4 w-4" />
                          Save Changes
                        </button>
                      </div>
                    </div>
                  ) : null}
                </div>
              );
            })}
          </div>
        </div>
      )}

      <div className="mt-6 rounded-lg border border-border bg-card p-4">
        <h3 className="mb-4 text-base font-semibold">Chat Preferences</h3>

        <div className="space-y-4">
          <div className="flex items-center justify-between gap-6">
            <div>
              <label htmlFor="streaming-toggle" className="text-sm font-medium">
                Response Streaming
              </label>
              <p className="mt-1 text-xs text-muted-foreground">
                Enable streaming to see AI responses as they are generated. Disable it for faster complete responses.
              </p>
            </div>
            <Toggle
              id="streaming-toggle"
              checked={streamingEnabled}
              onChange={handleStreamingToggle}
              disabled={loadingPreferences}
              label={`${streamingEnabled ? "Disable" : "Enable"} response streaming`}
            />
          </div>

          <div className="rounded-md p-3 text-xs text-muted-foreground elevation-1">
            <strong>Note:</strong> When streaming is disabled, responses arrive all at once after processing is complete.
          </div>
        </div>
      </div>
    </div>
  );
}