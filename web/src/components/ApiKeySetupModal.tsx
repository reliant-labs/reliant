import { useCallback, useEffect, useMemo, useState } from "react";
import {
  CheckCircle2,
  Copy,
  ExternalLink,
  Eye,
  EyeOff,
  Loader2,
  Plus,
  RefreshCw,
  TestTube,
  Trash2,
  XCircle,
} from "lucide-react";
import { Modal } from "./ui/Modal";
import { api } from "../api/client";
import { useApiKeySetupStore } from "../store/apiKeySetupStore";
import { cn } from "../lib/utils";
import { logger } from "../lib/logger";
import { useCodexOAuth, useClaudeOAuth, useOAuthAvailability } from "../hooks";

const PROVIDERS = [
  {
    id: "reliant" as const,
    name: "Reliant",
    docsUrl: "",
    keyFormat: "cpat_...",
    usesOAuth: false as const,
    isManaged: true,
    description:
      "Managed Reliant access. Create a token here or store an existing one; runtime model access is exchanged automatically.",
  },
  {
    id: "claude" as const,
    name: "Claude Code",
    docsUrl: "https://claude.ai",
    keyFormat: "",
    usesOAuth: "claude" as const,
    isManaged: false,
    description: "Connect Claude Code with OAuth.",
  },
  {
    id: "codex" as const,
    name: "Codex (ChatGPT)",
    docsUrl: "https://github.com/openai/codex",
    keyFormat: "",
    usesOAuth: "codex" as const,
    isManaged: false,
    description: "Connect Codex with OAuth.",
  },
  {
    id: "anthropic" as const,
    name: "Anthropic",
    docsUrl: "https://console.anthropic.com/settings/keys",
    keyFormat: "sk-ant-...",
    usesOAuth: false as const,
    isManaged: false,
    description: "Use an Anthropic API key.",
  },
  {
    id: "openai" as const,
    name: "OpenAI",
    docsUrl: "https://platform.openai.com/api-keys",
    keyFormat: "sk-...",
    usesOAuth: false as const,
    isManaged: false,
    description: "Use an OpenAI API key.",
  },
  {
    id: "gemini" as const,
    name: "Google Gemini",
    docsUrl: "https://makersuite.google.com/app/apikey",
    keyFormat: "AIza...",
    usesOAuth: false as const,
    isManaged: false,
    description: "Use a Google Gemini API key.",
  },
  {
    id: "openrouter" as const,
    name: "OpenRouter",
    docsUrl: "https://openrouter.ai/keys",
    keyFormat: "sk-or-...",
    usesOAuth: false as const,
    isManaged: false,
    description: "Use an OpenRouter API key.",
  },
] as const;

type ProviderId = (typeof PROVIDERS)[number]["id"];

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

function parseErrorMessage(errorText: string, provider: string): string {
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
      return "Reliant is not configured yet. Create or store a token to continue.";
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

  if (provider === "openrouter") {
    if (lowerError.includes("no endpoints found matching your data policy")) {
      return "No models available with your current data policy.";
    }
  }

  if (
    lowerError.includes("unauthorized") ||
    lowerError.includes("401") ||
    lowerError.includes("invalid")
  ) {
    return "Invalid API key. Please check your credentials.";
  }

  if (lowerError.includes("rate limit") || lowerError.includes("429")) {
    return "Rate limit exceeded. Please wait and try again.";
  }

  if (lowerError.includes("quota") || lowerError.includes("billing")) {
    return "Account issue. Check your billing or quota.";
  }

  return errorText || "Validation failed. Please check your credentials.";
}

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
    default:
      return status ? status.replace(/_/g, " ") : "Unknown";
  }
};

const formatDateTime = (value?: string) => {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
};

export function ApiKeySetupModal() {
  const showModal = useApiKeySetupStore((s) => s.showModal);
  const dismissModal = useApiKeySetupStore((s) => s.dismissModal);

  const [selectedProvider, setSelectedProvider] = useState<ProviderId>("reliant");
  const [apiKey, setApiKey] = useState("");
  const [showKey, setShowKey] = useState(false);
  const [validationResult, setValidationResult] = useState<{
    valid: boolean;
    message: string;
  } | null>(null);
  const [isValidating, setIsValidating] = useState(false);
  const [isSaving, setIsSaving] = useState(false);
  const [reliantStatus, setReliantStatus] = useState<ReliantProviderRecord | null>(null);
  const [reliantLoading, setReliantLoading] = useState(false);
  const [reliantActionLoading, setReliantActionLoading] = useState<string | null>(null);
  const [reliantTokenName, setReliantTokenName] = useState("");
  const [createdReliantToken, setCreatedReliantToken] = useState("");
  const [copiedReliantToken, setCopiedReliantToken] = useState(false);

  const codexOAuth = useCodexOAuth();
  const claudeOAuth = useClaudeOAuth();
  const oauthAvailability = useOAuthAvailability();

  const provider = useMemo(
    () => PROVIDERS.find((p) => p.id === selectedProvider)!,
    [selectedProvider]
  );

  const refreshAfterConfiguration = useCallback(async () => {
    useApiKeySetupStore.setState({ hasApiKey: true, showModal: false });
    const { useGlobalDataStore } = await import("../store/globalDataStore");
    await useGlobalDataStore.getState().refetchModels();
    window.dispatchEvent(new CustomEvent("api-key-saved"));
  }, []);

  const refreshReliantStatus = useCallback(async () => {
    setReliantLoading(true);
    try {
      const status = await api.settings.getReliantProviderStatus();
      setReliantStatus(status);
    } catch (error) {
      console.error("Failed to load Reliant status:", error);
    } finally {
      setReliantLoading(false);
    }
  }, []);

  useEffect(() => {
    if (showModal) {
      void refreshReliantStatus();
      return;
    }

    codexOAuth.cancel();
    claudeOAuth.cancel();
    setApiKey("");
    setValidationResult(null);
    setIsValidating(false);
    setIsSaving(false);
    setShowKey(false);
    setSelectedProvider("reliant");
    setReliantTokenName("");
    setCreatedReliantToken("");
    setCopiedReliantToken(false);
  }, [claudeOAuth, codexOAuth, refreshReliantStatus, showModal]);

  const handleConnectOAuth = useCallback(
    async (oauthType: "codex" | "claude") => {
      setIsValidating(true);
      setValidationResult(null);

      const oauthHook = oauthType === "claude" ? claudeOAuth : codexOAuth;
      const displayName = oauthType === "claude" ? "Claude Code" : "Codex";

      try {
        const result = await oauthHook.start();
        if (!result.ok) {
          setValidationResult({ valid: false, message: result.message });
          return false;
        }

        setValidationResult({
          valid: true,
          message: result.message || `Connected to ${displayName} successfully!`,
        });

        await refreshAfterConfiguration();
        return true;
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        setValidationResult({ valid: false, message: parseErrorMessage(msg, oauthType) });
        return false;
      } finally {
        setIsValidating(false);
      }
    },
    [claudeOAuth, codexOAuth, refreshAfterConfiguration]
  );

  const handleProviderSelect = useCallback((providerId: ProviderId) => {
    setSelectedProvider(providerId);
    setValidationResult(null);
    setApiKey("");
    setShowKey(false);
    setCreatedReliantToken("");
  }, []);

  const handleValidate = useCallback(async () => {
    if (provider.usesOAuth) {
      await handleConnectOAuth(provider.usesOAuth as "codex" | "claude");
      return;
    }

    if (provider.isManaged && !apiKey.trim()) {
      setIsValidating(true);
      setValidationResult(null);
      try {
        const result = await api.settings.validateProviderAPIKey("reliant", "");
        setValidationResult({
          valid: result.valid,
          message: result.valid
            ? result.message || "Reliant access looks healthy."
            : parseErrorMessage(result.message || "Reliant validation failed", "reliant"),
        });
        await refreshReliantStatus();
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        setValidationResult({ valid: false, message: parseErrorMessage(msg, "reliant") });
      } finally {
        setIsValidating(false);
      }
      return;
    }

    if (!apiKey.trim()) {
      setValidationResult({
        valid: false,
        message: provider.isManaged
          ? "Paste a cpat_ token or create a new one first."
          : "Please enter an API key.",
      });
      return;
    }

    setIsValidating(true);
    setValidationResult(null);

    try {
      const result = await api.settings.validateProviderAPIKey(
        selectedProvider,
        apiKey.trim()
      );

      if (result.valid) {
        setValidationResult({
          valid: true,
          message:
            result.message ||
            (provider.isManaged ? "Reliant access looks healthy." : "API key looks valid."),
        });
      } else {
        setValidationResult({
          valid: false,
          message: parseErrorMessage(
            result.message || "Invalid API key",
            selectedProvider
          ),
        });
      }

      if (provider.isManaged) {
        await refreshReliantStatus();
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setValidationResult({
        valid: false,
        message: parseErrorMessage(msg, selectedProvider),
      });
    } finally {
      setIsValidating(false);
    }
  }, [
    apiKey,
    handleConnectOAuth,
    provider.isManaged,
    provider.usesOAuth,
    refreshReliantStatus,
    selectedProvider,
  ]);

  const handleSave = useCallback(async () => {
    if (!apiKey.trim()) {
      setValidationResult({
        valid: false,
        message: provider.isManaged
          ? "Paste a cpat_ token to store it locally."
          : "Please enter an API key.",
      });
      return;
    }

    setIsSaving(true);
    setValidationResult(null);

    try {
      const validation = await api.settings.validateProviderAPIKey(
        selectedProvider,
        apiKey.trim()
      );

      if (!validation.valid) {
        setValidationResult({
          valid: false,
          message: parseErrorMessage(
            validation.message || "Invalid API key",
            selectedProvider
          ),
        });
        return;
      }

      await api.settings.updateProvider(selectedProvider, apiKey.trim());
      if (provider.isManaged) {
        await refreshReliantStatus();
      }
      await refreshAfterConfiguration();

      logger.info("[ApiKeySetupModal] Saved provider credentials and refetched models", {
        provider: selectedProvider,
      });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setValidationResult({
        valid: false,
        message: parseErrorMessage(msg, selectedProvider),
      });
    } finally {
      setIsSaving(false);
    }
  }, [
    apiKey,
    provider.isManaged,
    refreshAfterConfiguration,
    refreshReliantStatus,
    selectedProvider,
  ]);

  const handleCreateReliantToken = useCallback(async () => {
    setReliantActionLoading("create");
    setValidationResult(null);

    try {
      const result = await api.settings.createReliantProviderToken({
        name: reliantTokenName.trim() || undefined,
      });
      if (!result.success) {
        throw new Error(result.message || "Failed to create Reliant token");
      }

      setCreatedReliantToken(result.token);
      setCopiedReliantToken(false);
      if (navigator.clipboard?.writeText) {
        try {
          await navigator.clipboard.writeText(result.token);
          setCopiedReliantToken(true);
          setTimeout(() => setCopiedReliantToken(false), 2000);
        } catch (error) {
          console.error("Failed to copy Reliant token:", error);
        }
      }

      setValidationResult({
        valid: true,
        message:
          result.message ||
          "Reliant token created, stored locally, and copied to your clipboard.",
      });
      setReliantTokenName("");

      const refreshFailures: string[] = [];
      try {
        await refreshReliantStatus();
      } catch (error) {
        console.error("Failed to refresh Reliant status after token creation:", error);
        refreshFailures.push("status");
      }
      try {
        await refreshAfterConfiguration();
      } catch (error) {
        console.error("Failed to refresh app state after token creation:", error);
        refreshFailures.push("app");
      }

      if (refreshFailures.length > 0) {
        setValidationResult({
          valid: true,
          message:
            "Reliant token created successfully. Some follow-up refresh steps failed, so you may need to refresh the UI before access status updates.",
        });
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setValidationResult({ valid: false, message: parseErrorMessage(msg, "reliant") });
    } finally {
      setReliantActionLoading(null);
    }
  }, [refreshAfterConfiguration, refreshReliantStatus, reliantTokenName]);

  const handleCopyToken = useCallback(async () => {
    if (!createdReliantToken || !navigator.clipboard?.writeText) {
      return;
    }

    try {
      await navigator.clipboard.writeText(createdReliantToken);
      setCopiedReliantToken(true);
      setTimeout(() => setCopiedReliantToken(false), 2000);
    } catch (error) {
      console.error("Failed to copy token:", error);
    }
  }, [createdReliantToken]);

  const handleRevokeReliantToken = useCallback(
    async (tokenId: string) => {
      if (!window.confirm("Revoke this Reliant token? This cannot be undone.")) {
        return;
      }

      setReliantActionLoading(`revoke:${tokenId}`);
      setValidationResult(null);

      try {
        const result = await api.settings.revokeReliantProviderToken(tokenId, false);
        if (!result.success) {
          throw new Error(result.message || "Failed to revoke Reliant token");
        }
        setValidationResult({
          valid: true,
          message: result.message || "Reliant token revoked.",
        });
        await refreshReliantStatus();
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        setValidationResult({
          valid: false,
          message: parseErrorMessage(msg, "reliant"),
        });
      } finally {
        setReliantActionLoading(null);
      }
    },
    [refreshReliantStatus]
  );

  const renderReliantStatus = () => {
    const access = reliantStatus?.access;
    return (
      <div className="space-y-4">
        <div className="rounded-lg border border-border bg-muted/20 p-4">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium">Managed access status</span>
            <span
              className={cn(
                "inline-flex items-center rounded-full px-2 py-1 text-xs font-medium",
                statusBadgeClasses(access?.state)
              )}
            >
              {statusLabel(access?.state)}
            </span>
          </div>
          <p className="mt-2 text-sm text-muted-foreground">
            {access?.message || "Reliant status will appear here once you configure a token."}
          </p>
          {reliantStatus?.masked_token ? (
            <p className="mt-2 font-mono text-xs text-muted-foreground">
              Stored token: {reliantStatus.masked_token}
            </p>
          ) : null}
          {access?.allowed_models?.length ? (
            <div className="mt-3 flex flex-wrap gap-2">
              {access.allowed_models.map((model) => (
                <span
                  key={model}
                  className="rounded-full border border-border bg-background px-2 py-1 text-xs"
                >
                  {model}
                </span>
              ))}
            </div>
          ) : null}
        </div>

        {createdReliantToken ? (
          <div className="rounded-lg border border-emerald-500/20 bg-emerald-500/10 p-4">
            <div className="flex items-start justify-between gap-3">
              <div>
                <p className="text-sm font-medium text-emerald-700">New Reliant token</p>
                <p className="mt-1 break-all font-mono text-xs text-emerald-700/90">
                  {createdReliantToken}
                </p>
                <p className="mt-2 text-xs text-emerald-700/80">
                  This token is only shown once. Copy it now if you need it elsewhere.
                </p>
              </div>
              <button
                type="button"
                onClick={() => void handleCopyToken()}
                className="inline-flex items-center gap-2 rounded-md border border-emerald-500/30 bg-background px-3 py-1.5 text-sm text-emerald-700 hover:bg-background/80"
              >
                <Copy className="h-4 w-4" />
                {copiedReliantToken ? "Copied" : "Copy"}
              </button>
            </div>
          </div>
        ) : null}

        <div className="space-y-4 rounded-lg border border-primary/20 bg-primary/5 p-4">
          <div className="space-y-1">
            <p className="text-sm font-medium text-foreground">Create a Reliant token</p>
            <p className="text-sm text-muted-foreground">
              This is the normal setup path. Reliant stores the token locally for this app and
              exchanges it for runtime model access automatically.
            </p>
          </div>
          <input
            value={reliantTokenName}
            onChange={(e) => setReliantTokenName(e.target.value)}
            placeholder="Optional token name"
            className="w-full rounded-lg border border-border bg-background px-3 py-2 text-sm"
          />
          <p className="text-xs text-muted-foreground">
            The full cpat_ token is shown once after creation in case you need to copy it
            elsewhere.
          </p>
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              onClick={() => void handleCreateReliantToken()}
              disabled={reliantActionLoading === "create"}
              className={cn(
                "px-4 py-2 text-sm bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors",
                reliantActionLoading === "create" && "opacity-50 cursor-not-allowed"
              )}
            >
              {reliantActionLoading === "create" ? (
                <span className="inline-flex items-center gap-2">
                  <Loader2 className="w-4 h-4 animate-spin" /> Creating…
                </span>
              ) : (
                <span className="inline-flex items-center gap-2">
                  <Plus className="w-4 h-4" /> Create token
                </span>
              )}
            </button>
            <button
              type="button"
              onClick={() => void refreshReliantStatus()}
              disabled={reliantLoading}
              className={cn(
                "px-4 py-2 text-sm border border-border rounded-lg hover:bg-muted transition-colors",
                reliantLoading && "opacity-50 cursor-not-allowed"
              )}
            >
              {reliantLoading ? (
                <span className="inline-flex items-center gap-2">
                  <Loader2 className="w-4 h-4 animate-spin" /> Refreshing…
                </span>
              ) : (
                <span className="inline-flex items-center gap-2">
                  <RefreshCw className="w-4 h-4" /> Refresh status
                </span>
              )}
            </button>
          </div>
        </div>

        <details className="rounded-lg border border-border bg-background p-4">
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
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                placeholder={provider.keyFormat}
                type={showKey ? "text" : "password"}
                className={cn(
                  "w-full rounded-lg border border-border bg-background px-3 py-2 pr-10 text-sm font-mono",
                  "focus:outline-none focus:ring-2 focus:ring-primary"
                )}
                autoComplete="off"
              />
              <button
                type="button"
                onClick={() => setShowKey((v) => !v)}
                className="absolute right-2 top-1/2 -translate-y-1/2 p-1 text-muted-foreground hover:text-foreground"
                aria-label={showKey ? "Hide token" : "Show token"}
              >
                {showKey ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
              </button>
            </div>
            <div className="flex flex-wrap gap-2">
              <button
                type="button"
                onClick={handleValidate}
                disabled={isValidating}
                className={cn(
                  "px-4 py-2 text-sm border border-border rounded-lg hover:bg-muted transition-colors",
                  isValidating && "opacity-50 cursor-not-allowed"
                )}
              >
                {isValidating ? (
                  <span className="inline-flex items-center gap-2">
                    <Loader2 className="w-4 h-4 animate-spin" /> Checking…
                  </span>
                ) : (
                  <span className="inline-flex items-center gap-2">
                    <TestTube className="w-4 h-4" /> Check access
                  </span>
                )}
              </button>
              <button
                type="button"
                onClick={handleSave}
                disabled={!apiKey.trim() || isSaving || isValidating}
                className={cn(
                  "px-4 py-2 text-sm border border-primary/30 bg-primary/10 text-primary rounded-lg hover:bg-primary/15 transition-colors",
                  (isSaving || isValidating) && "opacity-50 cursor-not-allowed"
                )}
              >
                {isSaving ? "Saving…" : "Store token"}
              </button>
            </div>
          </div>
        </details>

        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <div>
              <h4 className="text-sm font-medium">Control-plane tokens</h4>
              <p className="text-xs text-muted-foreground">
                Runtime model keys are exchanged from these stored control-plane tokens.
              </p>
            </div>
          </div>
          {reliantStatus?.tokens?.length ? (
            <div className="space-y-2">
              {reliantStatus.tokens.map((token) => (
                <div
                  key={token.id}
                  className="flex flex-col gap-3 rounded-lg border border-border bg-background p-3 md:flex-row md:items-center md:justify-between"
                >
                  <div>
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium">{token.name || token.token_prefix}</span>
                      <span className="rounded-full border border-border px-2 py-0.5 text-xs text-muted-foreground">
                        {token.token_prefix}
                      </span>
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
                    className={cn(
                      "inline-flex items-center gap-2 rounded-md border border-destructive/20 px-3 py-1.5 text-sm text-destructive hover:bg-destructive/10",
                      reliantActionLoading === `revoke:${token.id}` &&
                        "opacity-50 cursor-not-allowed"
                    )}
                  >
                    {reliantActionLoading === `revoke:${token.id}` ? (
                      <Loader2 className="w-4 h-4 animate-spin" />
                    ) : (
                      <Trash2 className="w-4 h-4" />
                    )}
                    Revoke
                  </button>
                </div>
              ))}
            </div>
          ) : (
            <div className="rounded-lg border border-dashed border-border p-4 text-sm text-muted-foreground">
              No Reliant tokens returned from the control plane yet.
            </div>
          )}
        </div>
      </div>
    );
  };

  return (
    <Modal
      isOpen={showModal}
      onClose={() => dismissModal(false)}
      title="Set up a provider"
      size="lg"
    >
      <div className="space-y-5">
        <p className="text-sm text-muted-foreground">
          Reliant needs at least one configured provider before workflows can run.
          You can use managed Reliant access, connect an OAuth-backed provider, or
          add a standard API key.
        </p>

        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <label className="text-sm font-medium text-foreground">Provider</label>
            {!provider.usesOAuth && !provider.isManaged && (
              <a
                href={provider.docsUrl}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
              >
                Get a key <ExternalLink className="w-4 h-4" />
              </a>
            )}
          </div>

          <div className="grid grid-cols-2 gap-2">
            {PROVIDERS.map((p) => (
              <button
                key={p.id}
                type="button"
                onClick={() => handleProviderSelect(p.id)}
                className={cn(
                  "text-left px-3 py-2 rounded-lg border transition-colors",
                  p.id === selectedProvider
                    ? "border-primary bg-primary/10 ring-2 ring-primary/30 shadow-sm"
                    : "border-border hover:bg-muted"
                )}
              >
                <div className="text-sm font-medium">{p.name}</div>
              </button>
            ))}
          </div>
        </div>

        {validationResult && (
          <div
            className={cn(
              "flex items-center gap-2 text-sm p-3 rounded-lg border",
              validationResult.valid
                ? "bg-emerald-500/10 text-emerald-600 border-emerald-500/20"
                : "bg-red-500/10 text-red-600 border-red-500/20"
            )}
          >
            {validationResult.valid ? (
              <CheckCircle2 className="w-4 h-4" />
            ) : (
              <XCircle className="w-4 h-4" />
            )}
            {validationResult.message}
          </div>
        )}

        {provider.isManaged ? (
          renderReliantStatus()
        ) : provider.usesOAuth ? (
          <div className="space-y-4">
            <div className="p-4 rounded-lg border border-border bg-muted/30">
              <div className="space-y-2">
                <p className="text-sm font-medium text-foreground">
                  Authenticate via {provider.name}
                </p>
                <p className="text-sm text-muted-foreground">
                  {oauthAvailability.available
                    ? `Sign in with ${provider.name} to connect your account.`
                    : "The local OAuth helper is not running. Start it in your terminal to enable login:"}
                </p>
                {!oauthAvailability.available && !oauthAvailability.loading && (
                  <code className="block mt-2 px-3 py-2 text-sm bg-background border border-border rounded-md font-mono select-all">
                    reliant auth serve
                  </code>
                )}
              </div>
            </div>

            <div className="flex items-center justify-between gap-3 pt-1">
              <button
                type="button"
                onClick={() => dismissModal(true)}
                className="text-sm text-muted-foreground hover:text-foreground"
              >
                Don&apos;t ask again
              </button>

              {oauthAvailability.available ? (
                <button
                  type="button"
                  onClick={handleValidate}
                  disabled={isValidating}
                  className={cn(
                    "px-4 py-2 text-sm bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors",
                    isValidating && "opacity-50 cursor-not-allowed"
                  )}
                >
                  {isValidating ? "Connecting…" : `Login with ${provider.name}`}
                </button>
              ) : (
                <button
                  type="button"
                  onClick={oauthAvailability.recheck}
                  disabled={oauthAvailability.loading}
                  className="px-4 py-2 text-sm border border-border rounded-lg hover:bg-muted transition-colors"
                >
                  {oauthAvailability.loading ? "Checking…" : "Retry"}
                </button>
              )}
            </div>
          </div>
        ) : (
          <>
            <div className="space-y-2">
              <label className="text-sm font-medium text-foreground">API key</label>
              <div className="relative">
                <input
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                  placeholder={provider.keyFormat}
                  type={showKey ? "text" : "password"}
                  className={cn(
                    "w-full rounded-lg border border-border bg-background px-3 py-2 pr-10 text-sm",
                    "focus:outline-none focus:ring-2 focus:ring-primary"
                  )}
                  autoComplete="off"
                />
                <button
                  type="button"
                  onClick={() => setShowKey((v) => !v)}
                  className="absolute right-2 top-1/2 -translate-y-1/2 p-1 text-muted-foreground hover:text-foreground"
                  aria-label={showKey ? "Hide API key" : "Show API key"}
                >
                  {showKey ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                </button>
              </div>
            </div>

            <div className="flex items-center justify-between gap-3 pt-1">
              <button
                type="button"
                onClick={() => dismissModal(true)}
                className="text-sm text-muted-foreground hover:text-foreground"
              >
                Don&apos;t ask again
              </button>

              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={handleValidate}
                  disabled={isValidating || isSaving}
                  className={cn(
                    "px-4 py-2 text-sm border border-border rounded-lg hover:bg-muted transition-colors",
                    (isValidating || isSaving) && "opacity-50 cursor-not-allowed"
                  )}
                >
                  {isValidating ? "Validating…" : "Validate"}
                </button>
                <button
                  type="button"
                  onClick={handleSave}
                  disabled={isSaving || isValidating}
                  className={cn(
                    "px-4 py-2 text-sm bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors",
                    (isSaving || isValidating) && "opacity-50 cursor-not-allowed"
                  )}
                >
                  {isSaving ? "Saving…" : "Save"}
                </button>
              </div>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}