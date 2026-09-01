import { useCallback, useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import {
  CheckCircle2,
  ExternalLink,
  Eye,
  EyeOff,
  KeyRound,
  Loader2,
  XCircle,
} from "lucide-react";
import { api } from "@/api/client";
import { cn } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { useCodexOAuth, useClaudeOAuth, useCopilotOAuth, useOAuthAvailability } from "@/hooks";
import { OAuthHelperPanel } from "@/components/OAuthHelperPanel";
import { CopilotDevicePanel } from "@/components/CopilotDevicePanel";
import { useWalletOverview } from "@/hooks/useReliantAIQueries";
import { RedeemCouponForm } from "@/components/RedeemCouponForm";
// TODO: Remove this store import once the ApiKeySetupModal is converted to event-driven
import { useApiKeySetupStore } from "@/store/apiKeySetupStore";
import { trackEvent } from "@/lib/analytics";
import { getEventBus } from "@/lib/events";
import type { ModelProvider, StepProps } from "../types";
import { getForcedEligibility } from "../forcedEligibility";
import { useGoToBilling } from "@/hooks/useGoToBilling";

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
    id: "copilot" as const,
    modelProvider: "copilot" as ModelProvider,
    name: "GitHub Copilot",
    docsUrl: "https://github.com/settings/copilot",
    keyFormat: "",
    usesOAuth: "copilot" as const,
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

  if (
    provider === "openrouter" &&
    lowerError.includes("no endpoints found matching your data policy")
  ) {
    return "No models available with your current data policy.";
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

export function ModelStep({ plan, updatePlan, onNext }: StepProps) {
  const goToBilling = useGoToBilling();
  const codexOAuth = useCodexOAuth();
  const claudeOAuth = useClaudeOAuth();
  const copilotOAuth = useCopilotOAuth();
  // getIsDev() is deliberately NOT an input here. It used to force this step
  // into its "eligible" branch in dev, which meant a dev build claimed "you
  // have credit available" against an empty wallet AND hid the coupon field
  // behind that claim — the exact state the coupon flow exists to rescue. Dev
  // now sees what the server reports; getForcedEligibility() is the escape
  // hatch for exercising either branch on purpose.
  const forcedEligibility = getForcedEligibility();

  const [selectedProvider, setSelectedProvider] =
    useState<ProviderId>("reliant");
  const [apiKey, setApiKey] = useState("");
  const [showKey, setShowKey] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [validationResult, setValidationResult] = useState<{
    valid: boolean;
    message: string;
  } | null>(null);

  const provider = useMemo(
    () => PROVIDERS.find((item) => item.id === selectedProvider)!,
    [selectedProvider],
  );

  // Only probe the localhost OAuth helper once the user selects a redirect-based
  // OAuth provider (the OAuthHelperPanel is shown) — never proactively on mount.
  // Copilot uses the device flow and needs no local helper.
  const oauthAvailability = useOAuthAvailability({
    enabled: provider.usesOAuth === "claude" || provider.usesOAuth === "codex",
  });

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

  // Wallet balance is the WHOLE gate for managed Reliant, because it is the
  // only thing the LLM proxy actually enforces: AuthorizeUsage reserves
  // against the org wallet and rejects a zero balance outright, so a user who
  // gets past this step unfunded strands at their FIRST message.
  //
  // Compute eligibility (useCloudEligibility) is deliberately NOT consulted.
  // It answers "may this account run a cloud daemon" — a different product,
  // bought separately. Gating model access on it denied LLM credit to anyone
  // without compute minutes, which is the same category error as granting
  // compute because someone had LLM credit.
  const walletQ = useWalletOverview();
  const walletLoading = forcedEligibility == null && walletQ.isLoading;
  const balanceNanos = walletQ.data?.wallet?.balanceUsdNanos;
  const hasFunds = balanceNanos != null && BigInt(balanceNanos) > 0n;
  const creditsAvailable =
    forcedEligibility === "eligible" ||
    (forcedEligibility == null && hasFunds);

  const finishOnboarding = useCallback(
    async (modelProvider: ModelProvider) => {
      if (!plan.compute) {
        setError("Choose where Reliant should run before finishing setup.");
        return;
      }

      // Managed Reliant needs a funded wallet. The button that calls this is
      // already `disabled` without one, but `disabled` is a rendering concern —
      // a stale render, a keyboard activation racing the wallet refetch, or
      // devtools all reach the handler anyway. Committing the choice here would
      // send the user into the app with a provider that fails on their very
      // first message (the LLM proxy rejects a zero balance outright).
      //
      // Bring-your-own-key providers are unaffected: they carry their own
      // credentials and never touch the wallet.
      // Keyed on hasFunds, NOT creditsAvailable: `?onboarding-credits=eligible`
      // is a dev escape hatch for exercising the enabled-looking UI, and it
      // must not authorize a real unfunded account to commit. The wallet
      // balance is the only authority the LLM proxy respects.
      if (modelProvider === "reliant_credits" && !hasFunds) {
        setError(
          "Redeem a coupon code or set up billing before continuing with Reliant.",
        );
        return;
      }

      setError(null);
      trackEvent("onboarding_model_selected", { provider: modelProvider });
      await updatePlan({ modelProvider });
      onNext();
    },
    [hasFunds, onNext, plan, updatePlan],
  );

  const handleConnectOAuth = useCallback(async () => {
    if (!provider.usesOAuth) return;
    setError(null);
    setValidationResult(null);

    const oauthHook =
      provider.usesOAuth === "claude" ? claudeOAuth : codexOAuth;
    try {
      const result = await oauthHook.start();
      if (!result.ok) {
        setValidationResult({ valid: false, message: result.message });
        return;
      }

      // TODO: Remove once ApiKeySetupModal is event-driven
      useApiKeySetupStore.setState({
        hasApiKey: true,
        showModal: false,
        hasChecked: true,
      });
      const { useGlobalDataStore } = await import("@/store/globalDataStore");
      await useGlobalDataStore.getState().refetchModels();
      getEventBus().emit("api-key:saved", { provider: provider.modelProvider });
      await finishOnboarding(provider.modelProvider);
    } catch (err) {
      setValidationResult({
        valid: false,
        message: err instanceof Error ? err.message : String(err),
      });
    }
  }, [claudeOAuth, codexOAuth, finishOnboarding, provider]);

  // GitHub Copilot uses the device-authorization flow (device code → poll),
  // driven by the shared CopilotDevicePanel. The panel surfaces its own
  // in-progress / error UI; this only advances onboarding on success.
  const handleConnectCopilot = useCallback(async () => {
    setError(null);
    setValidationResult(null);

    const result = await copilotOAuth.start();
    if (!result.ok) {
      // The device panel surfaces the error message from the hook.
      return;
    }

    // TODO: Remove once ApiKeySetupModal is event-driven
    useApiKeySetupStore.setState({
      hasApiKey: true,
      showModal: false,
      hasChecked: true,
    });
    const { useGlobalDataStore } = await import("@/store/globalDataStore");
    await useGlobalDataStore.getState().refetchModels();
    getEventBus().emit("api-key:saved", { provider: "copilot" });
    await finishOnboarding(provider.modelProvider);
  }, [copilotOAuth, finishOnboarding, provider]);

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
          message: parseErrorMessage(
            validation.message || "Invalid API key",
            selectedProvider,
          ),
        });
        return;
      }

      await saveKeyMutation.mutateAsync({
        providerId: selectedProvider,
        key: apiKey.trim(),
      });

      // TODO: Remove once ApiKeySetupModal is event-driven
      useApiKeySetupStore.setState({
        hasApiKey: true,
        showModal: false,
        hasChecked: true,
      });
      const { useGlobalDataStore } = await import("@/store/globalDataStore");
      await useGlobalDataStore.getState().refetchModels();
      getEventBus().emit("api-key:saved", { provider: selectedProvider });
      logger.info("[OnboardingModelStep] Saved API key", {
        provider: selectedProvider,
      });
      await finishOnboarding(provider.modelProvider);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      setValidationResult({
        valid: false,
        message: parseErrorMessage(message, selectedProvider),
      });
    }
  }, [
    apiKey,
    finishOnboarding,
    provider,
    saveKeyMutation,
    selectedProvider,
    validateKeyMutation,
  ]);


  return (
    <div className="space-y-6">
      <div className="space-y-2 text-center">
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">
          Which AI should Reliant use?
        </h2>
        <p className="mx-auto max-w-[52ch] text-sm leading-relaxed text-muted-foreground">
          Already pay for Claude, ChatGPT or Copilot? Sign in and use it here.
          Or add credit to Reliant and we handle the rest.
        </p>
      </div>

      <div className="space-y-3">
        <div className="space-y-4 rounded-xl border border-border/50 bg-muted/30 p-4">
          <div className="flex items-start gap-3">
            <KeyRound className="mt-0.5 h-4 w-4 text-primary" />
            <div>
              <h3 className="text-sm font-medium text-foreground">
                Choose a provider
              </h3>
              <p className="mt-0.5 text-xs text-muted-foreground">
                Bring a subscription you already pay for, paste an API key, or
                use Reliant credit.
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
                  // Abort/clear any prior Copilot device flow when switching.
                  copilotOAuth.reset();
                }}
                className={cn(
                  "rounded-lg border px-3 py-2 text-left transition-colors",
                  item.id === selectedProvider
                    ? "border-primary bg-primary/10 ring-2 ring-primary/20"
                    : "border-border/40 bg-background hover:bg-muted/60",
                )}
              >
                <span className="block text-sm font-medium text-foreground">
                  {item.name}
                </span>
              </button>
            ))}
          </div>

          {provider.builtIn ? (
            <div className="space-y-3 rounded-lg border border-border/40 bg-background/70 p-4">
              <p className="text-sm font-medium text-foreground">
                Use Reliant&apos;s model routing
              </p>
              {walletLoading ? (
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  Checking credit availability...
                </div>
              ) : creditsAvailable ? (
                <p className="text-xs leading-relaxed text-muted-foreground">
                  You have credit available &mdash; no API key needed.
                </p>
              ) : (
                <p className="text-xs leading-relaxed text-muted-foreground">
                  No API key needed — add credit with a code or a card. If you
                  already pay for Claude, ChatGPT or Copilot, pick it above
                  instead and this is free.
                </p>
              )}
              {/* Always offered, never gated on the current balance: a user
                  who already has credit may still be holding a code, and
                  hiding the field until they run out means redeeming it
                  requires first spending down. Credit is no longer granted
                  automatically at signup, so for an empty wallet this is also
                  the only path that keeps "Start with Reliant" from
                  succeeding into a first message that fails on zero balance. */}
              <RedeemCouponForm variant="open" size="sm" />
              {!walletLoading && !creditsAvailable && (
                <p className="text-xs leading-relaxed text-muted-foreground">
                  No code?{" "}
                  <button
                    type="button"
                    onClick={goToBilling}
                    className="underline underline-offset-2 hover:text-foreground"
                  >
                    Set up billing
                  </button>{" "}
                  to add credit, or pick another provider above.
                </p>
              )}
              <button
                type="button"
                onClick={() => finishOnboarding("reliant_credits")}
                disabled={saving || walletLoading || !creditsAvailable}
                title={
                  !creditsAvailable && !walletLoading
                    ? "Redeem a coupon code or set up billing to continue with Reliant"
                    : undefined
                }
                className={cn(
                  "inline-flex w-full items-center justify-center gap-2 rounded-lg py-2.5 text-sm font-semibold transition-colors",
                  !saving && !walletLoading && creditsAvailable
                    ? "bg-primary text-primary-foreground hover:bg-primary/90"
                    : "cursor-not-allowed bg-muted text-muted-foreground",
                )}
              >
                {saving && <Loader2 className="h-4 w-4 animate-spin" />}
                Start with Reliant
              </button>
            </div>
          ) : provider.usesOAuth === "copilot" ? (
            <CopilotDevicePanel
              oauth={copilotOAuth}
              onStart={handleConnectCopilot}
            />
          ) : provider.usesOAuth ? (
            <OAuthHelperPanel
              providerName={provider.name}
              available={oauthAvailability.available}
              loading={oauthAvailability.loading}
              onRetry={oauthAvailability.recheck}
              onConnect={handleConnectOAuth}
              connecting={validating}
              connectLabel={`Connect ${provider.name}`}
              buttonAlign="stretch"
              size="compact"
            />
          ) : (
            <div className="space-y-3 rounded-lg border border-border/40 bg-background/70 p-4">
              <div className="flex items-center justify-between">
                <label
                  htmlFor="onboarding-llm-key-input"
                  className="text-xs text-muted-foreground"
                >
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
                  {showKey ? (
                    <EyeOff className="h-4 w-4" />
                  ) : (
                    <Eye className="h-4 w-4" />
                  )}
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
          {validationResult.valid ? (
            <CheckCircle2 className="h-4 w-4" />
          ) : (
            <XCircle className="h-4 w-4" />
          )}
          {validationResult.message}
        </div>
      )}

      {error && <p className="text-center text-xs text-destructive">{error}</p>}
    </div>
  );
}

