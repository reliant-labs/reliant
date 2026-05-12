import { useCallback, useEffect, useMemo, useState } from "react";
import { ExternalLink, Eye, EyeOff, CheckCircle2, XCircle } from "lucide-react";
import { Modal } from "./ui/Modal";
import { api } from "../api/client";
import { useApiKeySetupStore } from "../store/apiKeySetupStore";
import { cn } from "../lib/utils";
import { logger } from "../lib/logger";
import { useCodexOAuth, useClaudeOAuth, useOAuthAvailability } from "../hooks";
import { authServeCommand } from "../lib/cli-commands";

const PROVIDERS = [
  {
    id: "claude" as const,
    name: "Claude Code",
    docsUrl: "https://claude.ai",
    keyFormat: "",
    usesOAuth: "claude" as const,
  },
  {
    id: "codex" as const,
    name: "Codex (ChatGPT)",
    docsUrl: "https://github.com/openai/codex",
    keyFormat: "",
    usesOAuth: "codex" as const,
  },
  {
    id: "anthropic" as const,
    name: "Anthropic",
    docsUrl: "https://console.anthropic.com/settings/keys",
    keyFormat: "sk-ant-...",
    usesOAuth: false as const,
  },
  {
    id: "openai" as const,
    name: "OpenAI",
    docsUrl: "https://platform.openai.com/api-keys",
    keyFormat: "sk-...",
    usesOAuth: false as const,
  },
  {
    id: "gemini" as const,
    name: "Google Gemini",
    docsUrl: "https://makersuite.google.com/app/apikey",
    keyFormat: "AIza...",
    usesOAuth: false as const,
  },
  {
    id: "openrouter" as const,
    name: "OpenRouter",
    docsUrl: "https://openrouter.ai/keys",
    keyFormat: "sk-or-...",
    usesOAuth: false as const,
  },
];

const AUTO_MANAGED_PROVIDER_NAME = "Reliant";

type ProviderId = (typeof PROVIDERS)[number]["id"];

function parseErrorMessage(errorText: string, provider: string): string {
  const lowerError = (errorText || "").toLowerCase();

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

  return errorText || "Validation failed. Please check your API key.";
}

export function ApiKeySetupModal() {
  const showModal = useApiKeySetupStore((s) => s.showModal);
  const dismissModal = useApiKeySetupStore((s) => s.dismissModal);

  const [selectedProvider, setSelectedProvider] = useState<ProviderId>(
    "claude"
  );
  const [apiKey, setApiKey] = useState("");
  const [showKey, setShowKey] = useState(false);
  const [validationResult, setValidationResult] = useState<{
    valid: boolean;
    message: string;
  } | null>(null);
  const [isValidating, setIsValidating] = useState(false);
  const [isSaving, setIsSaving] = useState(false);
  const codexOAuth = useCodexOAuth();
  const claudeOAuth = useClaudeOAuth();
  const oauthAvailability = useOAuthAvailability();

  const provider = useMemo(
    () => PROVIDERS.find((p) => p.id === selectedProvider)!,
    [selectedProvider]
  );

  useEffect(() => {
    if (!showModal) {
      codexOAuth.cancel();
      claudeOAuth.cancel();
      setApiKey("");
      setValidationResult(null);
      setIsValidating(false);
      setIsSaving(false);
      setShowKey(false);
      setSelectedProvider("claude");
    }
  }, [showModal, codexOAuth, claudeOAuth]);

  const handleConnectOAuth = useCallback(async (oauthType: "codex" | "claude") => {
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

      setValidationResult({ valid: true, message: result.message || `Connected to ${displayName} successfully!` });

      // Mark store as having a key and close modal
      useApiKeySetupStore.setState({ hasApiKey: true, showModal: false });

      // Refetch models
      const { useGlobalDataStore } = await import("../store/globalDataStore");
      await useGlobalDataStore.getState().refetchModels();
      window.dispatchEvent(new CustomEvent("api-key-saved"));
      return true;
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setValidationResult({ valid: false, message: msg });
      return false;
    } finally {
      setIsValidating(false);
    }
  }, [codexOAuth, claudeOAuth]);

  // Handle provider selection only; OAuth starts from explicit button click.
  const handleProviderSelect = useCallback((providerId: ProviderId) => {
    setSelectedProvider(providerId);
    setValidationResult(null);
  }, []);

  const handleValidate = useCallback(async () => {
    // For OAuth providers, use the dedicated handler
    if (provider.usesOAuth) {
      await handleConnectOAuth(provider.usesOAuth as "codex" | "claude");
      return;
    }

    if (!apiKey.trim()) {
      setValidationResult({ valid: false, message: "Please enter an API key." });
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
        setValidationResult({ valid: true, message: "API key looks valid." });
      } else {
        setValidationResult({
          valid: false,
          message: parseErrorMessage(result.message || "Invalid API key", selectedProvider),
        });
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
  }, [apiKey, selectedProvider, provider.usesOAuth, handleConnectOAuth]);

  const handleSave = useCallback(async () => {
    if (!apiKey.trim()) {
      setValidationResult({ valid: false, message: "Please enter an API key." });
      return;
    }

    setIsSaving(true);
    setValidationResult(null);

    try {
      // Validate first (server-side) to avoid saving obviously bad keys.
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

      // Mark store as having a key so other ensure() calls stop prompting.
      useApiKeySetupStore.setState({ hasApiKey: true, showModal: false });

      // Immediately refetch models so they appear in dropdowns right away
      const { useGlobalDataStore } = await import("../store/globalDataStore");
      await useGlobalDataStore.getState().refetchModels();

      // Dispatch event to notify all model inputs to refresh
      window.dispatchEvent(new CustomEvent('api-key-saved'));

      logger.info("[ApiKeySetupModal] Saved API key and refetched models", { provider: selectedProvider });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setValidationResult({
        valid: false,
        message: parseErrorMessage(msg, selectedProvider),
      });
    } finally {
      setIsSaving(false);
    }
  }, [apiKey, selectedProvider]);

  return (
    <Modal
      isOpen={showModal}
      onClose={() => dismissModal(false)}
      title="Add an API key"
      size="lg"
    >
      <div className="space-y-5">
        <p className="text-sm text-muted-foreground">
          Reliant can sync your {AUTO_MANAGED_PROVIDER_NAME} access automatically after sign-in, or you can add another provider key to continue.
        </p>

        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <label className="text-sm font-medium text-foreground">Provider</label>
            {!provider.usesOAuth && (
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

        {/* OAuth Section */}
        {provider.usesOAuth ? (
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
                  <code className="block mt-2 px-3 py-2 text-sm bg-background border border-border rounded-md font-mono select-all break-all">
                    {authServeCommand()}
                  </code>
                )}
              </div>
            </div>

            {validationResult && (
              <div
                className={cn(
                  "flex items-center gap-2 text-sm p-3 rounded-lg",
                  validationResult.valid
                    ? "bg-emerald-500/10 text-emerald-600 border border-emerald-500/20"
                    : "bg-red-500/10 text-red-600 border border-red-500/20"
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

            <div className="flex items-center justify-between gap-3 pt-1">
              <button
                type="button"
                onClick={() => dismissModal(true)}
                className="text-sm text-muted-foreground hover:text-foreground"
              >
                Don't ask again
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
          /* Standard API Key Input Section */
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

              {validationResult && (
                <div
                  className={cn(
                    "text-xs",
                    validationResult.valid ? "text-emerald-500" : "text-red-500"
                  )}
                >
                  {validationResult.message}
                </div>
              )}
            </div>

            <div className="flex items-center justify-between gap-3 pt-1">
              <button
                type="button"
                onClick={() => dismissModal(true)}
                className="text-sm text-muted-foreground hover:text-foreground"
              >
                Don't ask again
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