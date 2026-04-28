import { useState, useEffect } from "react";
import {
  Loader2,
  AlertCircle,
  Check,
  Eye,
  EyeOff,
  Plus,
  Settings2,
  TestTube,
  ChevronDown,
  Trash2,
  CheckCircle2,
  XCircle,
} from "lucide-react";
import { Toggle } from "../ui/Toggle";
import { cn } from "../../lib/utils";
import { api } from "../../api/client";
import { useGlobalDataStore } from "../../store/globalDataStore";
import { ModelPreferences } from "./ModelPreferences";
import {
  useApiKeySetupStore,
  resetApiKeySetupDismissed,
} from "../../store/apiKeySetupStore";
import { useCodexOAuth, useClaudeOAuth, useOAuthAvailability } from "../../hooks";

interface CombinedGeneralSettingsProps {
  providers: Array<{
    provider: string;
    displayName: string;
    hasApiKey: boolean;
    maskedKey?: string;
    configured: boolean;
  }>;
  onProvidersUpdate?: () => void;
}

// Providers visible in the UI (other providers are hidden but implementations remain)
const VISIBLE_PROVIDERS = [
  "claude",
  "codex",
  "anthropic",
  "openai",
  "gemini",
  "openrouter",
] as const;

const providerConfigs = {
  claude: {
    name: "Claude Code",
    docsUrl: "https://claude.ai",
    keyFormat: "",
    description:
      "Claude 4.5 and 4.6 models via Claude OAuth (uses Claude authentication)",
    usesOAuth: "claude" as const,
  },
  codex: {
    name: "Codex (ChatGPT)",
    docsUrl: "https://github.com/openai/codex",
    keyFormat: "",
    description:
      "GPT-5.3 Codex (flagship) via ChatGPT backend (uses Codex authentication)",
    usesOAuth: "codex" as const,
  },
  openrouter: {
    name: "OpenRouter",
    docsUrl: "https://openrouter.ai/keys",
    keyFormat: "sk-or-...",
    description:
      "Access 400+ AI models through one API (Claude, GPT, Gemini, Grok)",
    usesOAuth: false,
  },
  anthropic: {
    name: "Anthropic",
    docsUrl: "https://console.anthropic.com/settings/keys",
    keyFormat: "sk-ant-...",
    description: "Claude 3.5, Claude 3, and other Anthropic models",
    usesOAuth: false,
  },
  openai: {
    name: "OpenAI",
    docsUrl: "https://platform.openai.com/api-keys",
    keyFormat: "sk-...",
    description: "GPT-4, GPT-3.5, and other OpenAI models",
    usesOAuth: false,
  },
  gemini: {
    name: "Google Gemini",
    docsUrl: "https://makersuite.google.com/app/apikey",
    keyFormat: "AIza...",
    description: "Gemini Pro, Gemini Ultra, and other Google models",
    usesOAuth: false,
  },
  azure: {
    name: "Azure OpenAI",
    docsUrl: "https://portal.azure.com/",
    keyFormat: "deployment-specific",
    description: "OpenAI models hosted on Azure",
    usesOAuth: false,
  },
  bedrock: {
    name: "AWS Bedrock",
    docsUrl: "https://console.aws.amazon.com/bedrock/",
    keyFormat: "AWS credentials",
    description: "Claude, Llama, and other models on AWS",
    usesOAuth: false,
  },
  copilot: {
    name: "GitHub Copilot",
    docsUrl: "https://github.com/settings/copilot",
    keyFormat: "GitHub token",
    description: "GitHub Copilot API access",
    usesOAuth: false,
  },
  groq: {
    name: "Groq",
    docsUrl: "https://console.groq.com/keys",
    keyFormat: "gsk_...",
    description: "Fast inference with Groq LPU",
    usesOAuth: false,
  },
  vertexai: {
    name: "Vertex AI",
    docsUrl: "https://console.cloud.google.com/vertex-ai",
    keyFormat: "GCP credentials",
    description: "Google Cloud AI models",
    usesOAuth: false,
  },
  xai: {
    name: "xAI",
    docsUrl: "https://x.ai/",
    keyFormat: "xai-...",
    description: "Grok and other xAI models",
    usesOAuth: false,
  },
  local: {
    name: "Local Models",
    docsUrl: "",
    keyFormat: "N/A",
    description: "Ollama, llama.cpp, and other local models",
    usesOAuth: false,
  },
};

type ProviderId = keyof typeof providerConfigs;

// Comprehensive error message parser for all API providers
const parseErrorMessage = (errorText: string, provider: string): string => {
  const lowerError = errorText.toLowerCase();

  // Claude specific errors
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
    if (lowerError.includes("rate limit") || lowerError.includes("429")) {
      return "Claude rate limit exceeded. Please wait a moment before trying again.";
    }
    if (lowerError.includes("session") || lowerError.includes("invalid")) {
      return "Claude session error. Please reconnect with Login with Claude.";
    }
  }

  // Codex (ChatGPT) specific errors
  if (provider === "codex") {
    if (lowerError.includes("not authenticated")) {
      return "Codex is not connected. Please use Login with Codex.";
    }
    if (
      lowerError.includes("missing_scope") ||
      lowerError.includes("api.responses.write")
    ) {
      return "Codex needs updated API permissions. Disconnect and use Login with Codex again.";
    }
    if (lowerError.includes("expired")) {
      return "Codex session expired. Please reconnect with Login with Codex.";
    }
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Codex authentication failed. Please reconnect with Login with Codex.";
    }
    if (lowerError.includes("rate limit") || lowerError.includes("429")) {
      return "Codex rate limit exceeded. Please wait a moment before trying again.";
    }
    if (lowerError.includes("session") || lowerError.includes("invalid")) {
      return "Codex session error. Please reconnect with Login with Codex.";
    }
  }

  // OpenRouter specific errors
  if (provider === "openrouter") {
    if (lowerError.includes("no endpoints found matching your data policy")) {
      return "OpenRouter requires privacy settings configuration. Please visit https://openrouter.ai/settings/privacy to configure your data policy, then try again.";
    }
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Invalid OpenRouter API key. Please check your key at https://openrouter.ai/keys";
    }
    if (lowerError.includes("quota") || lowerError.includes("limit")) {
      return "OpenRouter quota exceeded. Please check your usage limits at https://openrouter.ai/activity";
    }
  }

  // Anthropic specific errors
  if (provider === "anthropic") {
    if (lowerError.includes("model:") && lowerError.includes("not found")) {
      // Extract the specific model name from the error if possible
      const modelMatch = errorText.match(/model:\s*([^\s,]+)/i);
      const modelName = modelMatch ? modelMatch[1] : "the specified model";
      return `The model "${modelName}" was not found. This model may not exist, may require beta access, or may not be available in your region. Your API key is valid, but please check https://console.anthropic.com/ for available models.`;
    }
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Invalid Anthropic API key. Please check your key at https://console.anthropic.com/settings/keys";
    }
    if (lowerError.includes("rate limit") || lowerError.includes("429")) {
      return "Anthropic rate limit exceeded. Please wait a moment before trying again.";
    }
    if (lowerError.includes("quota") || lowerError.includes("usage")) {
      return "Anthropic usage limit reached. Please check your account usage at https://console.anthropic.com/account/usage";
    }
  }

  // OpenAI specific errors
  if (provider === "openai") {
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Invalid OpenAI API key. Please check your key at https://platform.openai.com/api-keys";
    }
    if (lowerError.includes("quota") || lowerError.includes("billing")) {
      return "OpenAI billing issue. Please check your account and billing at https://platform.openai.com/account/billing";
    }
    if (lowerError.includes("rate limit") || lowerError.includes("429")) {
      return "OpenAI rate limit exceeded. Please upgrade your plan or wait before trying again.";
    }
  }

  // Google Gemini specific errors
  if (provider === "gemini") {
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Invalid Google API key. Please check your key at https://makersuite.google.com/app/apikey";
    }
    if (lowerError.includes("quota") || lowerError.includes("limit")) {
      return "Google API quota exceeded. Please check your quota limits in the Google Cloud Console.";
    }
  }

  // Groq specific errors
  if (provider === "groq") {
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Invalid Groq API key. Please check your key at https://console.groq.com/keys";
    }
    if (lowerError.includes("rate limit") || lowerError.includes("429")) {
      return "Groq rate limit exceeded. Please wait before trying again.";
    }
  }

  // xAI specific errors
  if (provider === "xai") {
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Invalid xAI API key. Please check your account at https://x.ai/";
    }
  }

  // Azure specific errors
  if (provider === "azure") {
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Invalid Azure credentials. Please check your Azure OpenAI deployment configuration.";
    }
    if (lowerError.includes("deployment")) {
      return "Azure deployment issue. Please verify your deployment name and region in the Azure portal.";
    }
  }

  // AWS Bedrock specific errors
  if (provider === "bedrock") {
    if (lowerError.includes("unauthorized") || lowerError.includes("403")) {
      return "Invalid AWS credentials or insufficient permissions. Please check your IAM permissions for Bedrock.";
    }
    if (lowerError.includes("region")) {
      return "AWS region issue. Please ensure Bedrock is available in your configured region.";
    }
  }

  // GitHub Copilot specific errors
  if (provider === "copilot") {
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Invalid GitHub token. Please check your Copilot subscription and token permissions.";
    }
  }

  // Vertex AI specific errors
  if (provider === "vertexai") {
    if (lowerError.includes("unauthorized") || lowerError.includes("403")) {
      return "Invalid GCP credentials. Please check your service account permissions for Vertex AI.";
    }
  }

  // Generic authentication errors
  if (
    lowerError.includes("unauthorized") ||
    lowerError.includes("authentication") ||
    lowerError.includes("invalid") ||
    lowerError.includes("api key") ||
    lowerError.includes("401")
  ) {
    return "Invalid API key. Please check your credentials and try again.";
  }

  // Generic rate limiting
  if (lowerError.includes("rate limit") || lowerError.includes("429")) {
    return "Rate limit exceeded. Please wait a moment before trying again.";
  }

  // Generic quota/billing issues
  if (
    lowerError.includes("quota") ||
    lowerError.includes("limit") ||
    lowerError.includes("billing") ||
    lowerError.includes("usage")
  ) {
    return "Usage limit or billing issue. Please check your account status.";
  }

  // Network/connection errors
  if (
    lowerError.includes("network") ||
    lowerError.includes("connection") ||
    lowerError.includes("timeout") ||
    lowerError.includes("refused")
  ) {
    return "Connection failed. Please check your internet connection and try again.";
  }

  // Server errors
  if (
    lowerError.includes("500") ||
    lowerError.includes("502") ||
    lowerError.includes("503") ||
    lowerError.includes("server error")
  ) {
    return "Provider server error. Please try again in a few moments.";
  }

  // If no specific pattern matches, return a cleaned version of the original error
  return errorText.length > 200
    ? "API validation failed. Please check your key and try again."
    : errorText;
};

export function CombinedGeneralSettings({
  providers,
  onProvidersUpdate,
}: CombinedGeneralSettingsProps) {
  // Add Provider State
  const [selectedProvider, setSelectedProvider] = useState<string>("");
  const [apiKey, setApiKey] = useState<string>("");
  const [showKey, setShowKey] = useState<boolean>(false);
  const [validating, setValidating] = useState<boolean>(false);
  const [saving, setSaving] = useState<boolean>(false);
  const [validationMessage, setValidationMessage] = useState<{
    valid: boolean;
    message: string;
  } | null>(null);

  // Edit Provider State
  const [editingProvider, setEditingProvider] = useState<string | null>(null);
  const [editApiKeys, setEditApiKeys] = useState<{ [key: string]: string }>({});
  const [showEditKeys, setShowEditKeys] = useState<{ [key: string]: boolean }>(
    {}
  );
  const [deletingProvider, setDeletingProvider] = useState<string | null>(null);

  // Streaming preference state
  const [streamingEnabled, setStreamingEnabled] = useState<boolean>(false);
  const [loadingPreferences, setLoadingPreferences] = useState<boolean>(true);

  const codexOAuth = useCodexOAuth();
  const claudeOAuth = useClaudeOAuth();
  const oauthAvailability = useOAuthAvailability();

  // Filter to only show visible providers (hide others but keep implementations)
  const configuredProviders = providers.filter(
    (p) =>
      p.hasApiKey &&
      VISIBLE_PROVIDERS.includes(
        p.provider as (typeof VISIBLE_PROVIDERS)[number]
      )
  );
  const availableProviders = Object.entries(providerConfigs).filter(
    ([id]) =>
      VISIBLE_PROVIDERS.includes(id as (typeof VISIBLE_PROVIDERS)[number]) &&
      !providers.find((p) => p.provider === id && p.hasApiKey)
  ) as [ProviderId, (typeof providerConfigs)[ProviderId]][];

  const handleDeleteProvider = async (provider: string) => {
    const config = providerConfigs[provider as keyof typeof providerConfigs];
    if (
      !confirm(
        `Are you sure you want to remove the API key for ${
          config?.name || provider
        }?`
      )
    ) {
      return;
    }

    setDeletingProvider(provider);
    try {
      await api.settings.updateProvider(provider, ""); // Empty key triggers deletion
      onProvidersUpdate?.();

      // Refresh models immediately - the API call already waits for the database write to complete
      await useGlobalDataStore.getState().refetchModels();

      // Dispatch event to notify all model inputs to refresh
      window.dispatchEvent(new CustomEvent('api-key-saved'));

      // Check if this was the last API key - if so, reset the API key setup state
      // so the modal will show again when user navigates to project/chat screens
      const remainingWithKeys = providers.filter(
        (p) => p.hasApiKey && p.provider !== provider
      );
      if (remainingWithKeys.length === 0) {
        // Reset the dismissed state and hasApiKey so modal can show again
        resetApiKeySetupDismissed();
        useApiKeySetupStore.setState({ hasApiKey: false });
      }
    } catch (error) {
      console.error("Failed to delete provider:", error);
    } finally {
      setDeletingProvider(null);
    }
  };

  const handleSaveApiKey = async (provider?: string) => {
    const targetProvider = provider || selectedProvider;
    const targetKey = provider ? editApiKeys[provider] : apiKey;

    if (!provider) {
      setSaving(true);
    }
    setValidationMessage(null);

    try {
      await api.settings.updateProvider(targetProvider, targetKey || "");
      onProvidersUpdate?.();

      // Refresh models immediately - the API call already waits for the database write to complete
      await useGlobalDataStore.getState().refetchModels();

      // Dispatch event to notify all model inputs to refresh
      window.dispatchEvent(new CustomEvent('api-key-saved'));

      if (provider) {
        // Editing existing provider
        setEditingProvider(null);
        setEditApiKeys({ ...editApiKeys, [provider]: "" });
      } else {
        // Adding new provider
        setSelectedProvider("");
        setApiKey("");
        setValidationMessage({
          valid: true,
          message: "API key saved successfully",
        });
        setTimeout(() => setValidationMessage(null), 3000);
      }
    } catch (error) {
      console.error("Failed to save API key:", error);
      const errorText =
        error instanceof Error ? error.message : "Invalid API key";

      // Use comprehensive error parser
      const errorMessage = parseErrorMessage(errorText, targetProvider);

      setValidationMessage({
        valid: false,
        message: errorMessage,
      });
    } finally {
      if (!provider) {
        setSaving(false);
      }
    }
  };

  // Load streaming and notification preferences on mount
  useEffect(() => {
    const loadPreferences = async () => {
      try {
        const data = await api.settings.getPreferences();
        setStreamingEnabled((data.streaming_enabled as boolean) ?? false);
        // Convert backend settings to interaction mode
      } catch (error) {
        console.error("Failed to load preferences:", error);
      } finally {
        setLoadingPreferences(false);
      }
    };
    loadPreferences();
  }, []);

  // Handle streaming toggle
  const handleStreamingToggle = async (enabled: boolean) => {
    try {
      await api.settings.updatePreferences({
        streaming_enabled: enabled,
      });
      setStreamingEnabled(enabled);
    } catch (error) {
      console.error("Failed to update streaming preference:", error);
    }
  };

  // Validate API key via gRPC
  const handleValidateApiKey = async () => {
    setValidating(true);
    setValidationMessage(null);

    try {
      const result = await api.settings.validateProviderAPIKey(
        selectedProvider,
        apiKey
      );
      let message =
        result.message || "Connection failed. Please check your API key.";

      // Use comprehensive error parser for validation errors
      if (!result.valid) {
        message = parseErrorMessage(message, selectedProvider);
      }

      setValidationMessage({
        valid: result.valid,
        message: result.valid
          ? "Connection successful! API key is valid."
          : message,
      });
    } catch (error) {
      console.error("Failed to validate API key:", error);
      setValidationMessage({
        valid: false,
        message: "Failed to test connection. Please try again.",
      });
    } finally {
      setValidating(false);
    }
  };

  const handleConnectOAuth = async (oauthType: string) => {
    setValidating(true);
    setValidationMessage(null);

    const oauthHook = oauthType === "claude" ? claudeOAuth : codexOAuth;
    const displayName = oauthType === "claude" ? "Claude Code" : "Codex";

    try {
      const result = await oauthHook.start();

      if (!result.ok) {
        setValidationMessage({
          valid: false,
          message: result.message,
        });
        return;
      }

      onProvidersUpdate?.();

      // Refresh models
      await useGlobalDataStore.getState().refetchModels();
      window.dispatchEvent(new CustomEvent("api-key-saved"));

      // Reset form
      setSelectedProvider("");
      setValidationMessage({
        valid: true,
        message: result.message || `Connected to ${displayName} successfully!`,
      });
      setTimeout(() => setValidationMessage(null), 3000);
    } catch (error) {
      console.error(`Failed to connect ${displayName}:`, error);
      const errorMessage = error instanceof Error ? error.message : "Connection failed";
      setValidationMessage({
        valid: false,
        message: parseErrorMessage(errorMessage, oauthType),
      });
    } finally {
      setValidating(false);
    }
  };

  return (
    <div className="space-y-6">
      <div data-onboarding="ai-providers-settings">
        <h2 className="text-2xl font-bold tracking-tight">
          AI Provider Configuration
        </h2>
        <p className="text-muted-foreground">
          Connect your AI providers to enable model access and conversations.
        </p>
      </div>

      {/* Add Provider Section */}
      {availableProviders.length > 0 && (
        <div className="border border-border/40 rounded-lg p-6 bg-card shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]">
          <h3 className="text-lg font-semibold mb-4">Add New Provider</h3>

          <div className="space-y-4">
            <div className="space-y-2">
              <label className="text-sm font-medium">Select Provider</label>
              <div className="relative">
                <select
                  value={selectedProvider}
                  onChange={(e) => {
                    const providerId = e.target.value;
                    setSelectedProvider(providerId);
                    setValidationMessage(null);
                  }}
                  className="w-full px-3 py-2 pr-10 border border-border/40 bg-background rounded-md text-sm appearance-none cursor-pointer focus:ring-2 focus:ring-ring/40"
                >
                  <option value="">Choose a provider...</option>
                  {availableProviders.map(([id, config]) => (
                    <option key={id} value={id}>
                      {config.name}
                    </option>
                  ))}
                </select>
                <ChevronDown className="absolute right-3 top-1/2 -translate-y-1/2 h-4 w-4 pointer-events-none text-muted-foreground" />
              </div>
            </div>

            {selectedProvider && (
              providerConfigs[selectedProvider as ProviderId]?.usesOAuth ? (
                /* OAuth Section */
                <div className="space-y-4">
                  <div className="p-4 rounded-lg border border-border/40 bg-muted/30">
                    <div className="space-y-2">
                      <p className="text-sm font-medium text-foreground">
                        Authenticate via {providerConfigs[selectedProvider as ProviderId]?.name}
                      </p>
                      <p className="text-sm text-muted-foreground">
                        {oauthAvailability.available
                          ? `Sign in with ${providerConfigs[selectedProvider as ProviderId]?.name} to connect your account.`
                          : "The local OAuth helper is not running. Start it in your terminal to enable login:"}
                      </p>
                      {!oauthAvailability.available && !oauthAvailability.loading && (
                        <code className="block mt-2 px-3 py-2 text-sm bg-background border border-border/40 rounded-md font-mono select-all">
                          reliant auth serve
                        </code>
                      )}
                    </div>
                  </div>

                  {validationMessage && (
                    <div
                      className={cn(
                        "flex items-center gap-2 text-sm p-3 rounded-lg",
                        validationMessage.valid
                          ? "bg-emerald-500/10 text-emerald-600 border border-emerald-500/20"
                          : "bg-red-500/10 text-red-600 border border-red-500/20"
                      )}
                    >
                      {validationMessage.valid ? (
                        <CheckCircle2 className="w-4 h-4" />
                      ) : (
                        <XCircle className="w-4 h-4" />
                      )}
                      {validationMessage.message}
                    </div>
                  )}

                  <div className="flex justify-end">
                    {oauthAvailability.available ? (
                      <button
                        className="px-4 py-2 text-sm font-medium border border-primary/40 bg-primary/10 text-primary rounded-md transition-colors hover:bg-primary/20 disabled:opacity-50 flex items-center gap-2"
                        onClick={() => handleConnectOAuth(providerConfigs[selectedProvider as ProviderId]?.usesOAuth as string)}
                        disabled={validating}
                      >
                        {validating ? (
                          <>
                            <Loader2 className="h-4 w-4 animate-spin" />
                            Checking...
                          </>
                        ) : (
                          <>Login with {providerConfigs[selectedProvider as ProviderId]?.name}</>
                        )}
                      </button>
                    ) : (
                      <button
                        className="px-4 py-2 text-sm font-medium border border-border/40 bg-background rounded-md transition-colors hover:bg-accent disabled:opacity-50"
                        onClick={oauthAvailability.recheck}
                        disabled={oauthAvailability.loading}
                      >
                        {oauthAvailability.loading ? "Checking…" : "Retry"}
                      </button>
                    )}
                  </div>
                </div>
              ) : (
                /* Standard API Key Input Section */
                <>
                  <div className="space-y-2">
                    <label className="text-sm font-medium">API Key</label>
                    <div className="relative">
                      <input
                        type={showKey ? "text" : "password"}
                        value={apiKey}
                        onChange={(e) => {
                          setApiKey(e.target.value);
                          setValidationMessage(null);
                        }}
                        placeholder={`Enter your ${
                          providerConfigs[
                            selectedProvider as ProviderId
                          ]?.name
                        } API key`}
                        className="w-full px-3 py-2 border border-border/40 bg-background rounded-md pr-10 font-mono text-sm focus:ring-2 focus:ring-ring/40"
                      />
                      <button
                        type="button"
                        onClick={() => setShowKey(!showKey)}
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
                        href={
                          providerConfigs[
                            selectedProvider as ProviderId
                          ]?.docsUrl
                        }
                        target="_blank"
                        rel="noopener noreferrer"
                        className="text-primary hover:underline"
                      >
                        {
                          providerConfigs[
                            selectedProvider as ProviderId
                          ]?.name
                        }{" "}
                        Console
                      </a>
                    </p>
                  </div>

                  {validationMessage && (
                    <div
                      className={cn(
                        "flex items-start gap-2 p-3 rounded-md",
                        validationMessage.valid
                          ? "bg-success/10 text-success border border-success/20"
                          : "bg-destructive/10 text-destructive border border-destructive/20"
                      )}
                    >
                      {validationMessage.valid ? (
                        <Check className="h-4 w-4 mt-0.5" />
                      ) : (
                        <AlertCircle className="h-4 w-4 mt-0.5" />
                      )}
                      <span className="text-sm">{validationMessage.message}</span>
                    </div>
                  )}

                  <div className="flex gap-2">
                    <button
                      className="px-4 py-2 text-sm font-medium border border-border/40 bg-background hover:bg-accent hover:text-accent-foreground rounded-md transition-colors disabled:opacity-50 flex items-center gap-2"
                      onClick={handleValidateApiKey}
                      disabled={!apiKey || validating}
                    >
                      {validating ? (
                        <>
                          <Loader2 className="h-4 w-4 animate-spin" />
                          Testing...
                        </>
                      ) : (
                        <>
                          <TestTube className="h-4 w-4" />
                          Test Connection
                        </>
                      )}
                    </button>
                    <button
                      className="px-4 py-2 text-sm font-medium border border-primary/40 bg-primary/10 text-primary rounded-md transition-colors hover:bg-primary/20 disabled:opacity-50 flex items-center gap-2"
                      onClick={() => handleSaveApiKey()}
                      disabled={!apiKey || saving}
                    >
                      {saving ? (
                        <>
                          <Loader2 className="h-4 w-4 animate-spin" />
                          Saving...
                        </>
                      ) : (
                        <>
                          <Plus className="h-4 w-4" />
                          Add Provider
                        </>
                      )}
                    </button>
                  </div>
                </>
              )
            )}
          </div>
        </div>
      )}

      {/* Configured Providers List */}
      {configuredProviders.length > 0 && (
        <div>
          <h3 className="text-lg font-semibold mb-4">Configured Providers</h3>
          <div className="space-y-3">
            {configuredProviders.map((provider) => {
              const config =
                providerConfigs[
                  provider.provider as ProviderId
                ];
              return (
                <div
                  key={provider.provider}
                  className="border border-border/40 rounded-lg bg-card p-4 shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]"
                >
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-3">
                      <div className="w-8 h-8 rounded-full bg-primary/10 flex items-center justify-center">
                        <span className="text-xs font-semibold text-primary">
                          {config?.name?.charAt(0) || "P"}
                        </span>
                      </div>
                      <div>
                        <h4 className="font-semibold">
                          {provider.displayName}
                        </h4>
                        <div className="flex items-center gap-3 mt-1">
                          <span className="flex items-center gap-1 text-sm text-success">
                            <Check className="h-3 w-3" />
                            Connected
                          </span>
                          {provider.maskedKey && (
                            <span className="text-sm text-muted-foreground font-mono" data-sentry-mask>
                              {provider.maskedKey}
                            </span>
                          )}
                        </div>
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      {/* Hide Update button for providers that use OAuth auth (like Codex) */}
                      {!config?.usesOAuth && (
                        <button
                          className="px-3 py-1.5 text-sm border border-border/40 rounded-md hover:bg-accent transition-colors flex items-center gap-1"
                          onClick={() => {
                            if (editingProvider === provider.provider) {
                              setEditingProvider(null);
                              setEditApiKeys({
                                ...editApiKeys,
                                [provider.provider]: "",
                              });
                              setShowEditKeys({
                                ...showEditKeys,
                                [provider.provider]: false,
                              });
                            } else {
                              setEditingProvider(provider.provider);
                              setEditApiKeys({
                                ...editApiKeys,
                                [provider.provider]: "",
                              });
                            }
                          }}
                        >
                          <Settings2 className="w-4 h-4" />
                          {editingProvider === provider.provider
                            ? "Cancel"
                            : "Update"}
                        </button>
                      )}
                      <button
                        className="px-3 py-1.5 text-sm border border-destructive/20 text-destructive rounded-md hover:bg-destructive/10 transition-colors flex items-center gap-1"
                        onClick={() => handleDeleteProvider(provider.provider)}
                        disabled={deletingProvider === provider.provider}
                      >
                        {deletingProvider === provider.provider ? (
                          <Loader2 className="w-4 h-4 animate-spin" />
                        ) : (
                          <Trash2 className="w-4 h-4" />
                        )}
                        {config?.usesOAuth ? "Disconnect" : "Delete"}
                      </button>
                    </div>
                  </div>

                  {/* Only show edit section for providers that use API-key auth */}
                  {editingProvider === provider.provider && !config?.usesOAuth && (
                    <div className="border-t border-border/40 mt-4 pt-4 space-y-4">
                      <div className="space-y-2">
                        <label className="text-sm font-medium">
                          Update API Key
                        </label>
                        <div className="relative">
                          <input
                            type={
                              showEditKeys[provider.provider]
                                ? "text"
                                : "password"
                            }
                            value={editApiKeys[provider.provider] || ""}
                            onChange={(e) => {
                              setEditApiKeys({
                                ...editApiKeys,
                                [provider.provider]: e.target.value,
                              });
                            }}
                            placeholder="Enter new API key to update"
                            className="w-full px-3 py-2 border border-border/40 bg-background rounded-md pr-10 font-mono text-sm focus:ring-2 focus:ring-ring/40"
                          />
                          <button
                            type="button"
                            onClick={() =>
                              setShowEditKeys({
                                ...showEditKeys,
                                [provider.provider]:
                                  !showEditKeys[provider.provider],
                              })
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
                          Note: For security, existing API keys cannot be
                          viewed. Enter a new key to update.
                        </p>
                      </div>

                      <div className="flex gap-2">
                        <button
                          className="px-4 py-2 text-sm font-medium bg-primary text-primary-foreground hover:bg-primary/90 rounded-md transition-colors disabled:opacity-50 flex items-center gap-2"
                          onClick={() => handleSaveApiKey(provider.provider)}
                          disabled={!editApiKeys[provider.provider]}
                        >
                          <Check className="h-4 w-4" />
                          Save Changes
                        </button>
                      </div>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* Chat Preferences Section */}
      <div className="mt-6 border border-border/40 rounded-lg bg-card p-4 shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]">
        <h3 className="text-base font-semibold mb-4">Chat Preferences</h3>

        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <div>
              <label htmlFor="streaming-toggle" className="text-sm font-medium">
                Response Streaming
              </label>
              <p className="text-xs text-muted-foreground mt-1">
                Enable streaming to see AI responses as they're generated.
                Disable for faster complete responses.
              </p>
            </div>
            <Toggle
              id="streaming-toggle"
              checked={streamingEnabled}
              onChange={handleStreamingToggle}
              disabled={loadingPreferences}
              label={`${
                streamingEnabled ? "Disable" : "Enable"
              } response streaming`}
            />
          </div>

          <div className="text-xs text-muted-foreground elevation-1 p-3 rounded-md">
            <strong>Note:</strong> When streaming is disabled, responses arrive
            all at once after processing is complete. This can be faster for
            short responses but provides no visual feedback during generation.
          </div>

        </div>
      </div>

      {/* Model Preferences Section */}
      <ModelPreferences providers={providers} />
    </div>
  );
}