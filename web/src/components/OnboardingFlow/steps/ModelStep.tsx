import { useCallback, useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { CheckCircle2, ExternalLink, Eye, EyeOff, KeyRound, Loader2, XCircle } from "lucide-react";
import { api } from "@/api/client";
import { cn } from "@/lib/utils";
import { getIsDev } from "@/lib/constants";
import { logger } from "@/lib/logger";
import { authServeCommand } from "@/lib/cli-commands";
import { useCodexOAuth, useClaudeOAuth, useOAuthAvailability } from "@/hooks";
import { useCloudEligibility } from "@/hooks/useOnboardingQueries";
// TODO: Remove this store import once the ApiKeySetupModal is converted to event-driven
import { useApiKeySetupStore } from "@/store/apiKeySetupStore";
import { trackEvent } from "@/lib/analytics";
import { getEventBus } from "@/lib/events";
import type { ModelProvider, StepProps } from "../types";

const PROVIDERS = [
  {
    id: "reliant" as const,
    modelProvider: "reliant_credits" as ModelProvider,
    name: "Reliant",
    docsUrl: "",
    keyFormat: "",
    usesOAuth: false as const,
    builtIn: true as const,
  },
  {
    id: "claude" as const,
    modelProvider: "anthropic" as ModelProvider,
    name: "Claude Code",
    docsUrl: "https://claude.ai",
    keyFormat: "",
    usesOAuth: "claude" as const,
    builtIn: false as const,
  },
  {
    id: "codex" as const,
    modelProvider: "openai" as ModelProvider,
    name: "Codex (ChatGPT)",
    docsUrl: "https://github.com/openai/codex",
    keyFormat: "",
    usesOAuth: "codex" as const,
    builtIn: false as const,
  },
  {
    id: "anthropic" as const,
    modelProvider: "anthropic" as ModelProvider,
    name: "Anthropic",
    docsUrl: "https://console.anthropic.com/settings/keys",
    keyFormat: "sk-ant-...",
    usesOAuth: false as const,
    builtIn: false as const,
  },
  {
    id: "openai" as const,
    modelProvider: "openai" as ModelProvider,
    name: "OpenAI",
    docsUrl: "https://platform.openai.com/api-keys",
    keyFormat: "sk-...",
    usesOAuth: false as const,
    builtIn: false as const,
  },
  {
    id: "openrouter" as const,
    modelProvider: "openrouter" as ModelProvider,
    name: "OpenRouter",
    docsUrl: "https://openrouter.ai/keys",
    keyFormat: "sk-or-...",
    usesOAuth: false as const,
    builtIn: false as const,
  },
];

type ProviderId = (typeof PROVIDERS)[number]["id"];

function parseErrorMessage(errorText: string, provider: string): string {
  const lowerError = (errorText || "").toLowerCase();

  if (provider === "openrouter" && lowerError.includes("no endpoints found matching your data policy")) {
    return "No models available with your current data policy.";
  }
  if (lowerError.includes("unauthorized") || lowerError.includes("401") || lowerError.includes("invalid")) {
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

function getForcedEligibility(): "eligible" | "ineligible" | null {
  if (typeof window === "undefined") return null;
  const value = new URLSearchParams(window.location.search).get("onboarding-credits");
  if (value === "eligible") return "eligible";
  if (value === "ineligible") return "ineligible";
  return null;
}

export function ModelStep({ plan, updatePlan, onNext }: StepProps) {
  const codexOAuth = useCodexOAuth();
  const claudeOAuth = useClaudeOAuth();
  const oauthAvailability = useOAuthAvailability();
  const cloudEligibility = useCloudEligibility();

  const forcedEligibility = getForcedEligibility();
  const isEligible = forcedEligibility === "eligible"
    || (forcedEligibility == null && (getIsDev() || cloudEligibility.eligible));
  const eligibilityLoading = forcedEligibility == null && !getIsDev() && cloudEligibility.isLoading;

  const [selectedProvider, setSelectedProvider] = useState<ProviderId>("reliant");
  const [apiKey, setApiKey] = useState("");
  const [showKey, setShowKey] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [validationResult, setValidationResult] = useState<{ valid: boolean; message: string } | null>(null);

  const provider = useMemo(
    () => PROVIDERS.find((item) => item.id === selectedProvider)!,
    [selectedProvider],
  );

  const validateKeyMutation = useMutation({
    mutationFn: ({ providerId, key }: { providerId: string; key: string }) =>
      api.settings.validateProviderAPIKey(providerId, key),
  });

  const saveKeyMutation = useMutation({
    mutationFn: ({ providerId, key }: { providerId: string; key: string }) =>
      api.settings.updateProvider(providerId, key),
  });

  const saving = saveKeyMutation.isPending;
  const validating = validateKeyMutation.isPending;

  const finishOnboarding = useCallback(async (modelProvider: ModelProvider) => {
    if (!plan.compute) {
      setError("Choose where Reliant should run before finishing setup.");
      return;
    }

    setError(null);
    trackEvent('onboarding_model_selected', { provider: modelProvider });
    await updatePlan({ modelProvider });
    onNext();
  }, [onNext, plan, updatePlan]);

  const handleConnectOAuth = useCallback(async () => {
    if (!provider.usesOAuth) return;
    setError(null);
    setValidationResult(null);

    const oauthHook = provider.usesOAuth === "claude" ? claudeOAuth : codexOAuth;
    try {
      const result = await oauthHook.start();
      if (!result.ok) {
        setValidationResult({ valid: false, message: result.message });
        return;
      }

      // TODO: Remove once ApiKeySetupModal is event-driven
      useApiKeySetupStore.setState({ hasApiKey: true, showModal: false, hasChecked: true });
      const { useGlobalDataStore } = await import("@/store/globalDataStore");
      await useGlobalDataStore.getState().refetchModels();
      getEventBus().emit("api-key:saved", { provider: provider.modelProvider });
      await finishOnboarding(provider.modelProvider);
    } catch (err) {
      setValidationResult({ valid: false, message: err instanceof Error ? err.message : String(err) });
    }
  }, [claudeOAuth, codexOAuth, finishOnboarding, provider]);

  const handleSaveKey = useCallback(async () => {
    if (!apiKey.trim() || provider.usesOAuth) return;
    setError(null);
    setValidationResult(null);

    try {
      const validation = await validateKeyMutation.mutateAsync({
        providerId: selectedProvider,
        key: apiKey.trim(),
      });
      if (!validation.valid) {
        setValidationResult({
          valid: false,
          message: parseErrorMessage(validation.message || "Invalid API key", selectedProvider),
        });
        return;
      }

      await saveKeyMutation.mutateAsync({
        providerId: selectedProvider,
        key: apiKey.trim(),
      });

      // TODO: Remove once ApiKeySetupModal is event-driven
      useApiKeySetupStore.setState({ hasApiKey: true, showModal: false, hasChecked: true });
      const { useGlobalDataStore } = await import("@/store/globalDataStore");
      await useGlobalDataStore.getState().refetchModels();
      getEventBus().emit("api-key:saved", { provider: selectedProvider });
      logger.info("[OnboardingModelStep] Saved API key", { provider: selectedProvider });
      await finishOnboarding(provider.modelProvider);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setValidationResult({ valid: false, message: parseErrorMessage(message, selectedProvider) });
    }
  }, [apiKey, finishOnboarding, provider, saveKeyMutation, selectedProvider, validateKeyMutation]);

  const creditsAvailable = isEligible;


  return (
    <div className="space-y-6">
      <div className="space-y-2 text-center">
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">
          Choose model access
        </h2>
        <p className="text-sm text-muted-foreground">
          Connect your own provider, or use Reliant credits when capacity is available.
        </p>
      </div>

      <div className="space-y-3">
        <div className="space-y-4 rounded-xl border border-border/50 bg-muted/30 p-4">
          <div className="flex items-start gap-3">
            <KeyRound className="mt-0.5 h-4 w-4 text-primary" />
            <div>
              <h3 className="text-sm font-medium text-foreground">Connect your own provider</h3>
              <p className="mt-0.5 text-xs text-muted-foreground">
                OAuth providers connect directly. API keys are validated before being saved.
              </p>
            </div>
          </div>

          <div className="grid grid-cols-2 gap-2">
            {PROVIDERS.map((item) => (
              <button
                key={item.id}
                type="button"
                onClick={() => {
                  setSelectedProvider(item.id);
                  setValidationResult(null);
                  setError(null);
                }}
                className={cn(
                  "rounded-lg border px-3 py-2 text-left transition-colors",
                  item.id === selectedProvider
                    ? "border-primary bg-primary/10 ring-2 ring-primary/20"
                    : "border-border/40 bg-background hover:bg-muted/60",
                )}
              >
                <span className="block text-sm font-medium text-foreground">{item.name}</span>
              </button>
            ))}
          </div>

          {provider.builtIn ? (
            <div className="space-y-3 rounded-lg border border-border/40 bg-background/70 p-4">
              <p className="text-sm font-medium text-foreground">Use Reliant&apos;s model routing</p>
              {eligibilityLoading ? (
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  Checking credit availability...
                </div>
              ) : creditsAvailable ? (
                <p className="text-xs leading-relaxed text-muted-foreground">
                  $20 free credit included &mdash; no API key needed.
                </p>
              ) : (
                <p className="text-xs leading-relaxed text-muted-foreground">
                  No API key needed. Reliant routes to the best available model automatically.
                </p>
              )}
              <button
                type="button"
                onClick={() => finishOnboarding("reliant_credits")}
                disabled={saving}
                className={cn(
                  "inline-flex w-full items-center justify-center gap-2 rounded-lg py-2.5 text-sm font-semibold transition-colors",
                  !saving
                    ? "bg-primary text-primary-foreground hover:bg-primary/90"
                    : "cursor-not-allowed bg-muted text-muted-foreground",
                )}
              >
                {saving && <Loader2 className="h-4 w-4 animate-spin" />}
                Start with Reliant
              </button>
            </div>
          ) : provider.usesOAuth ? (
            <div className="space-y-3 rounded-lg border border-border/40 bg-background/70 p-4">
              <p className="text-sm font-medium text-foreground">Authenticate via {provider.name}</p>
              <p className="text-xs leading-relaxed text-muted-foreground">
                {oauthAvailability.available
                  ? `Sign in with ${provider.name} to connect your account.`
                  : "The local OAuth helper is not running. Start it in your terminal to enable login:"}
              </p>
              {!oauthAvailability.available && !oauthAvailability.loading && (
                <code className="block select-all rounded-md border border-border bg-background px-3 py-2 font-mono text-sm break-all">
                  {authServeCommand()}
                </code>
              )}
              <button
                type="button"
                onClick={handleConnectOAuth}
                disabled={validating || oauthAvailability.loading || !oauthAvailability.available}
                className={cn(
                  "inline-flex w-full items-center justify-center gap-2 rounded-lg py-2.5 text-sm font-semibold transition-colors",
                  !validating && oauthAvailability.available && !oauthAvailability.loading
                    ? "bg-primary text-primary-foreground hover:bg-primary/90"
                    : "cursor-not-allowed bg-muted text-muted-foreground",
                )}
              >
                {validating && <Loader2 className="h-4 w-4 animate-spin" />}
                Connect {provider.name}
              </button>
            </div>
          ) : (
            <div className="space-y-3 rounded-lg border border-border/40 bg-background/70 p-4">
              <div className="flex items-center justify-between">
                <label htmlFor="onboarding-llm-key-input" className="text-xs text-muted-foreground">
                  API key
                </label>
                <a
                  href={provider.docsUrl}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
                >
                  Get a key <ExternalLink className="h-3.5 w-3.5" />
                </a>
              </div>
              <div className="relative">
                <input
                  id="onboarding-llm-key-input"
                  type={showKey ? "text" : "password"}
                  value={apiKey}
                  onChange={(event) => setApiKey(event.target.value)}
                  placeholder={provider.keyFormat || "Enter your API key"}
                  className={cn(
                    "w-full rounded-lg border border-border/40 bg-background px-3 py-2.5 pr-10 font-mono text-sm text-foreground transition-colors placeholder:text-muted-foreground/50",
                    "focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/30",
                  )}
                />
                <button
                  type="button"
                  onClick={() => setShowKey((value) => !value)}
                  className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-muted-foreground hover:text-foreground"
                  aria-label={showKey ? "Hide API key" : "Show API key"}
                >
                  {showKey ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                </button>
              </div>
              <button
                type="button"
                onClick={handleSaveKey}
                disabled={!apiKey.trim() || saving}
                className={cn(
                  "inline-flex w-full items-center justify-center gap-2 rounded-lg py-2.5 text-sm font-semibold transition-colors",
                  apiKey.trim() && !saving
                    ? "bg-primary text-primary-foreground hover:bg-primary/90"
                    : "cursor-not-allowed bg-muted text-muted-foreground",
                )}
              >
                {saving && <Loader2 className="h-4 w-4 animate-spin" />}
                Save key and start
              </button>
            </div>
          )}
        </div>
      </div>

      {validationResult && (
        <div
          className={cn(
            "flex items-center gap-2 rounded-lg border p-3 text-sm",
            validationResult.valid
              ? "border-emerald-500/20 bg-emerald-500/10 text-emerald-600"
              : "border-red-500/20 bg-red-500/10 text-red-600",
          )}
        >
          {validationResult.valid ? <CheckCircle2 className="h-4 w-4" /> : <XCircle className="h-4 w-4" />}
          {validationResult.message}
        </div>
      )}

      {error && <p className="text-center text-xs text-destructive">{error}</p>}
    </div>
  );
}