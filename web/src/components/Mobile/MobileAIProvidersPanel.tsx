/**
 * Mobile-native "AI providers" panel — the last screen on this surface that
 * still imported desktop chrome wholesale. The previous `MobileAISettingsScreen`
 * rendered desktop `CombinedGeneralSettings` verbatim, which meant a 24.3×24px
 * streaming toggle and a 308×40px `<select>`, both under the 44px floor.
 *
 * This is a rebuild of the presentation only. It calls the SAME functions
 * `CombinedGeneralSettings` calls — `api.settings.updateProvider`,
 * `api.settings.validateProviderAPIKey`, `api.settings.getPreferences` /
 * `updatePreferences`, `useCodexOAuth` / `useClaudeOAuth` / `useCopilotOAuth`,
 * `onboardingService.provisionManagedKey` — and imports `providerConfigs`,
 * `VISIBLE_PROVIDERS`, and `parseErrorMessage` from `CombinedGeneralSettings`
 * itself rather than re-deriving them, so the provider list, key formats,
 * docs links, and error-message parsing can never drift between the two
 * surfaces. A divergent write path here would be a real bug, not a UI
 * difference — see the module comment on that file.
 *
 * OAuth: `OAuthHelperPanel` and `CopilotDevicePanel` are reused as-is (not
 * forked) — both are already generic components used identically by the
 * onboarding flow and the API-key setup modal, and both already clear the
 * 44px floor on their action buttons.
 *
 * Reliant is excluded from the generic "Add a provider" list and handled by
 * its own "Available" card, same split desktop makes between the manual-key
 * form and the standalone Enable tile — Reliant has no key entry, only a
 * provision call.
 *
 * Omitted vs. desktop, same rationale as the rest of this panel's history:
 * "Advanced model tuning" (465-line `ModelPreferences`, four knobs per model
 * tag) is replaced by `MobileModelPreferences`, which keeps only the model
 * choice. "Manage AI keys & spend" (`onOpenReliantAI`) is omitted — that CTA
 * opens desktop's `ReliantAISection` data tables, which has no mobile home.
 */

import { useCallback, useEffect, useState } from "react";
import {
  AlertCircle,
  Check,
  ChevronRight,
  Eye,
  EyeOff,
  Info,
  Loader2,
  Sparkles,
  Trash2,
  X,
} from "lucide-react";
import { api } from "../../api/client";
import { cn } from "../../lib/utils";
import { useGlobalDataStore } from "../../store/globalDataStore";
import {
  useApiKeySetupStore,
  resetApiKeySetupDismissed,
} from "../../store/apiKeySetupStore";
import {
  useCodexOAuth,
  useClaudeOAuth,
  useCopilotOAuth,
  useOAuthAvailability,
} from "../../hooks";
import { useCloudEligibility } from "../../hooks/useOnboardingQueries";
import { onboardingService } from "../../services/controlPlane/onboarding";
import { OAuthHelperPanel } from "../OAuthHelperPanel";
import { CopilotDevicePanel } from "../CopilotDevicePanel";
import { getEventBus } from "../../lib/events";
import {
  VISIBLE_PROVIDERS,
  providerConfigs,
  parseErrorMessage,
  type ProviderId,
} from "../Settings/CombinedGeneralSettings";
import { MobileModelPreferences } from "./MobileModelPreferences";
import { MOBILE_ROW, MobileCardGroup, MobileRowIcon } from "./MobileChrome";
import { MobileToggleRow } from "./MobileSettingsRow";

interface ProviderStatus {
  provider: string;
  configured: boolean;
  hasApiKey: boolean;
  maskedKey?: string;
  displayName: string;
}

interface MobileAIProvidersPanelProps {
  providers: ProviderStatus[];
  onProvidersUpdate: () => void;
}

type Banner = { valid: boolean; message: string } | null;

function BannerMessage({ banner }: { banner: Banner }) {
  if (!banner) return null;
  return (
    <div
      className={cn(
        "mt-3 flex items-start gap-2 rounded-lg border p-3 text-xs",
        banner.valid
          ? "border-success/30 bg-success/10 text-success"
          : "border-destructive/30 bg-destructive/10 text-destructive",
      )}
    >
      {banner.valid ? (
        <Check className="mt-0.5 h-4 w-4 shrink-0" />
      ) : (
        <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
      )}
      <span>{banner.message}</span>
    </div>
  );
}

/** Bottom sheet chrome shared by every "add a provider" flow below. */
function AddProviderSheet({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: React.ReactNode;
}) {
  return (
    <div className="fixed inset-0 z-50 flex flex-col justify-end">
      <button
        type="button"
        aria-label="Dismiss"
        onClick={onClose}
        className="absolute inset-0 bg-black/40"
      />
      <div
        className="relative flex max-h-[85vh] flex-col rounded-t-2xl border-t border-border bg-background shadow-lg"
        style={{ paddingBottom: "env(safe-area-inset-bottom)" }}
      >
        <div className="flex items-center justify-between border-b border-border px-4 py-3">
          <span className="text-sm font-semibold text-foreground">{title}</span>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
          >
            <X className="h-5 w-5" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-4">{children}</div>
      </div>
    </div>
  );
}

/** API-key entry for a manual-key provider (everything except OAuth/external). */
function ApiKeyEntry({
  providerId,
  onSave,
  saving,
}: {
  providerId: ProviderId;
  onSave: (key: string) => Promise<void>;
  saving: boolean;
}) {
  const config = providerConfigs[providerId];
  const [key, setKey] = useState("");
  const [showKey, setShowKey] = useState(false);
  const [validating, setValidating] = useState(false);
  const [banner, setBanner] = useState<Banner>(null);

  const handleValidate = async () => {
    setValidating(true);
    setBanner(null);
    try {
      const result = await api.settings.validateProviderAPIKey(providerId, key);
      setBanner({
        valid: result.valid,
        message: result.valid
          ? "Connection successful! API key is valid."
          : parseErrorMessage(
              result.message || "Connection failed. Please check your API key.",
              providerId,
            ),
      });
    } catch {
      setBanner({ valid: false, message: "Failed to test connection. Please try again." });
    } finally {
      setValidating(false);
    }
  };

  return (
    <>
      <label className="mb-1.5 block text-sm font-medium text-foreground">API key</label>
      <div className="relative">
        <input
          type={showKey ? "text" : "password"}
          value={key}
          onChange={(e) => {
            setKey(e.target.value);
            setBanner(null);
          }}
          placeholder={`Enter your ${config?.name} API key`}
          autoComplete="off"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          className="min-h-[44px] w-full rounded-md border border-border bg-background px-3 py-2.5 pr-10 font-mono text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring/40"
        />
        <button
          type="button"
          onClick={() => setShowKey((v) => !v)}
          aria-label={showKey ? "Hide key" : "Show key"}
          className="absolute right-0 top-1/2 flex min-h-[44px] min-w-[44px] -translate-y-1/2 items-center justify-center text-muted-foreground"
        >
          {showKey ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
        </button>
      </div>
      {config?.docsUrl && (
        <p className="mt-1.5 text-xs text-muted-foreground">
          Get your API key from{" "}
          <a
            href={config.docsUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="text-primary underline underline-offset-2"
          >
            {config.name} Console
          </a>
        </p>
      )}

      <BannerMessage banner={banner} />

      <div className="mt-4 flex gap-2">
        <button
          type="button"
          onClick={() => void handleValidate()}
          disabled={!key || validating}
          className="flex min-h-[44px] flex-1 items-center justify-center rounded-lg border border-border text-sm font-medium text-foreground active:bg-muted disabled:opacity-50"
        >
          {validating ? <Loader2 className="h-4 w-4 animate-spin" /> : "Test connection"}
        </button>
        <button
          type="button"
          onClick={() => void onSave(key)}
          disabled={!key || saving}
          className="flex min-h-[44px] flex-1 items-center justify-center rounded-lg bg-primary text-sm font-medium text-primary-foreground active:opacity-80 disabled:opacity-50"
        >
          {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : "Save"}
        </button>
      </div>
    </>
  );
}

/** Row for a provider already carrying a key/connection — shows status and a disconnect action. */
function ConfiguredProviderRow({
  provider,
  onDisconnect,
  disconnecting,
}: {
  provider: ProviderStatus;
  onDisconnect: () => void;
  disconnecting: boolean;
}) {
  const config = providerConfigs[provider.provider as ProviderId];
  const isReliant = provider.provider === "reliant";
  const label = config?.usesOAuth || isReliant ? "Disconnect" : "Delete";

  return (
    <div className={MOBILE_ROW}>
      <MobileRowIcon icon={Sparkles} />
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium text-foreground">
          {provider.displayName}
        </div>
        <div className="flex items-center gap-2 text-xs text-success">
          <Check className="h-3 w-3 shrink-0" />
          <span>Connected</span>
          {!isReliant && provider.maskedKey && (
            <span className="truncate font-mono text-muted-foreground" data-sentry-mask>
              {provider.maskedKey}
            </span>
          )}
        </div>
      </div>
      <button
        type="button"
        onClick={onDisconnect}
        disabled={disconnecting}
        aria-label={`${label} ${provider.displayName}`}
        className="flex min-h-[44px] min-w-[44px] shrink-0 items-center justify-center rounded-md text-destructive active:bg-destructive/10 disabled:opacity-50"
      >
        {disconnecting ? (
          <Loader2 className="h-4 w-4 animate-spin" />
        ) : (
          <Trash2 className="h-4 w-4" />
        )}
      </button>
    </div>
  );
}

export function MobileAIProvidersPanel({
  providers,
  onProvidersUpdate,
}: MobileAIProvidersPanelProps) {
  const codexOAuth = useCodexOAuth();
  const claudeOAuth = useClaudeOAuth();
  const copilotOAuth = useCopilotOAuth();
  const cloudEligibility = useCloudEligibility();

  const [addSheetProvider, setAddSheetProvider] = useState<ProviderId | null>(null);
  const [savingKey, setSavingKey] = useState(false);
  const [deletingProvider, setDeletingProvider] = useState<string | null>(null);
  const [enablingReliant, setEnablingReliant] = useState(false);
  const [connectingOAuth, setConnectingOAuth] = useState(false);
  const [oauthBanner, setOauthBanner] = useState<Banner>(null);

  const [streamingEnabled, setStreamingEnabled] = useState(false);
  const [loadingPreferences, setLoadingPreferences] = useState(true);

  const selectedOAuth = addSheetProvider
    ? providerConfigs[addSheetProvider]?.usesOAuth
    : undefined;
  const oauthAvailability = useOAuthAvailability({
    enabled: selectedOAuth === "claude" || selectedOAuth === "codex",
  });

  useEffect(() => {
    let cancelled = false;
    api.settings
      .getPreferences()
      .then((data) => {
        if (!cancelled) setStreamingEnabled((data.streaming_enabled as boolean) ?? false);
      })
      .catch((error) => console.error("Failed to load preferences:", error))
      .finally(() => {
        if (!cancelled) setLoadingPreferences(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const handleStreamingToggle = async (enabled: boolean) => {
    setStreamingEnabled(enabled);
    try {
      await api.settings.updatePreferences({ streaming_enabled: enabled });
    } catch (error) {
      console.error("Failed to update streaming preference:", error);
      setStreamingEnabled(!enabled);
    }
  };

  const refreshAfterKeyChange = useCallback(
    async (provider: string) => {
      onProvidersUpdate();
      await useGlobalDataStore.getState().refetchModels();
      getEventBus().emit("api-key:saved", { provider });
    },
    [onProvidersUpdate],
  );

  const configuredProviders = providers.filter(
    (p) =>
      p.hasApiKey &&
      VISIBLE_PROVIDERS.includes(p.provider as (typeof VISIBLE_PROVIDERS)[number]),
  );
  const reliantConfigured = configuredProviders.some((p) => p.provider === "reliant");
  const canOfferReliant = cloudEligibility.eligible && !reliantConfigured;

  // Reliant has its own "Available" card below (provision-only, no key
  // entry) — excluding it here keeps this list to providers that actually
  // open the add-provider sheet.
  const availableProviders = (Object.keys(providerConfigs) as ProviderId[]).filter(
    (id) =>
      id !== "reliant" &&
      VISIBLE_PROVIDERS.includes(id as (typeof VISIBLE_PROVIDERS)[number]) &&
      !providers.find((p) => p.provider === id && p.hasApiKey),
  );

  const handleSaveApiKey = async (key: string) => {
    if (!addSheetProvider) return;
    setSavingKey(true);
    try {
      await api.settings.updateProvider(addSheetProvider, key);
      await refreshAfterKeyChange(addSheetProvider);
      setAddSheetProvider(null);
    } catch (error) {
      console.error("Failed to save API key:", error);
    } finally {
      setSavingKey(false);
    }
  };

  const handleDeleteProvider = async (provider: string) => {
    const config = providerConfigs[provider as ProviderId];
    const displayName = provider === "reliant" ? "Reliant" : config?.name || provider;
    const prompt =
      provider === "reliant"
        ? "Disconnect Reliant? You can re-enable it later from Settings."
        : `Remove the API key for ${displayName}?`;
    if (!confirm(prompt)) return;

    setDeletingProvider(provider);
    try {
      await api.settings.updateProvider(provider, "");
      await refreshAfterKeyChange(provider);
      const remainingWithKeys = providers.filter(
        (p) => p.hasApiKey && p.provider !== provider,
      );
      if (remainingWithKeys.length === 0) {
        resetApiKeySetupDismissed();
        useApiKeySetupStore.setState({ hasApiKey: false });
      }
    } catch (error) {
      console.error("Failed to delete provider:", error);
    } finally {
      setDeletingProvider(null);
    }
  };

  const handleEnableReliant = async () => {
    setEnablingReliant(true);
    try {
      await onboardingService.provisionManagedKey();
      await refreshAfterKeyChange("reliant");
    } catch (error) {
      console.error("Failed to enable Reliant:", error);
    } finally {
      setEnablingReliant(false);
    }
  };

  const handleConnectOAuth = async (kind: "claude" | "codex") => {
    setConnectingOAuth(true);
    setOauthBanner(null);
    const oauthHook = kind === "claude" ? claudeOAuth : codexOAuth;
    const displayName = kind === "claude" ? "Claude Code" : "Codex";
    try {
      const result = await oauthHook.start();
      if (!result.ok) {
        setOauthBanner({ valid: false, message: result.message });
        return;
      }
      await refreshAfterKeyChange(kind);
      setOauthBanner({
        valid: true,
        message: result.message || `Connected to ${displayName} successfully!`,
      });
      setTimeout(() => setAddSheetProvider(null), 800);
    } catch (error) {
      const message = error instanceof Error ? error.message : "Connection failed";
      setOauthBanner({ valid: false, message: parseErrorMessage(message, kind) });
    } finally {
      setConnectingOAuth(false);
    }
  };

  const handleConnectCopilot = async () => {
    setOauthBanner(null);
    const result = await copilotOAuth.start();
    if (!result.ok) return;
    await refreshAfterKeyChange("copilot");
    setAddSheetProvider(null);
    copilotOAuth.reset();
  };

  const closeAddSheet = () => {
    setAddSheetProvider(null);
    setOauthBanner(null);
    copilotOAuth.reset();
  };

  return (
    <div className="min-h-0 flex-1 overflow-y-auto py-4">
      {/* Notice stays a static fact rather than a live probe — see this
          panel's predecessor's module comment on why `useOAuthAvailability`
          is never called just to decide whether to SHOW this text. */}
      <div className="mx-4 mb-4 flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-xs text-amber-700 dark:text-amber-400">
        <Info className="mt-0.5 h-4 w-4 shrink-0" />
        <p>
          Claude and Codex sign-in needs <code>reliant auth serve</code> running on
          a computer — a phone can&apos;t start it. Connect those from desktop, or
          use GitHub Copilot, which signs in entirely on this device.
        </p>
      </div>

      {configuredProviders.length > 0 && (
        <div className="px-4">
          <MobileCardGroup label="Connected">
            {configuredProviders.map((provider) => (
              <ConfiguredProviderRow
                key={provider.provider}
                provider={provider}
                disconnecting={deletingProvider === provider.provider}
                onDisconnect={() => void handleDeleteProvider(provider.provider)}
              />
            ))}
          </MobileCardGroup>
        </div>
      )}

      {canOfferReliant && (
        <div className="mt-4 px-4">
          <MobileCardGroup label="Available">
            <button
              type="button"
              onClick={() => void handleEnableReliant()}
              disabled={enablingReliant}
              className={cn(MOBILE_ROW, "disabled:opacity-60")}
            >
              <MobileRowIcon icon={Sparkles} />
              <div className="min-w-0 flex-1">
                <div className="text-sm font-medium text-foreground">Reliant</div>
                <div className="truncate text-xs text-muted-foreground">
                  Model routing with included credits
                </div>
              </div>
              {enablingReliant ? (
                <Loader2 className="h-4 w-4 shrink-0 animate-spin text-muted-foreground" />
              ) : (
                <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
              )}
            </button>
          </MobileCardGroup>
        </div>
      )}

      {availableProviders.length > 0 && (
        <div className="mt-4 px-4">
          <MobileCardGroup label="Add a provider">
            {availableProviders.map((id) => {
              const config = providerConfigs[id];
              return (
                <button
                  key={id}
                  type="button"
                  onClick={() => {
                    setOauthBanner(null);
                    setAddSheetProvider(id);
                  }}
                  className={MOBILE_ROW}
                >
                  <MobileRowIcon icon={Sparkles} />
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-medium text-foreground">{config.name}</div>
                    <div className="truncate text-xs text-muted-foreground">
                      {config.description}
                    </div>
                  </div>
                  <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
                </button>
              );
            })}
          </MobileCardGroup>
        </div>
      )}

      <div className="mt-4 px-4">
        <MobileCardGroup label="Chat preferences">
          <MobileToggleRow
            label="Response streaming"
            description="Show responses as they generate. Disable for faster complete responses."
            checked={streamingEnabled}
            disabled={loadingPreferences}
            onChange={(checked) => void handleStreamingToggle(checked)}
          />
        </MobileCardGroup>
      </div>

      <div className="mt-4 px-4">
        <MobileCardGroup label="Default models">
          <div className="p-4">
            <p className="mb-3 text-xs text-muted-foreground">
              Which model each tier uses by default. For thinking level,
              temperature, and compaction overrides, use desktop.
            </p>
            <MobileModelPreferences providers={providers} />
          </div>
        </MobileCardGroup>
      </div>

      {/* Add-provider sheet: routes to the right entry UI per provider kind. */}
      {addSheetProvider &&
        (() => {
          const config = providerConfigs[addSheetProvider];

          if (config.usesOAuth === "copilot") {
            return (
              <AddProviderSheet title={config.name} onClose={closeAddSheet}>
                <CopilotDevicePanel oauth={copilotOAuth} onStart={handleConnectCopilot} />
              </AddProviderSheet>
            );
          }

          if (config.usesOAuth === "claude" || config.usesOAuth === "codex") {
            return (
              <AddProviderSheet title={config.name} onClose={closeAddSheet}>
                <OAuthHelperPanel
                  providerName={config.name}
                  available={oauthAvailability.available}
                  loading={oauthAvailability.loading}
                  onRetry={oauthAvailability.recheck}
                  onConnect={() =>
                    void handleConnectOAuth(config.usesOAuth as "claude" | "codex")
                  }
                  connecting={connectingOAuth}
                  buttonAlign="stretch"
                />
                <BannerMessage banner={oauthBanner} />
              </AddProviderSheet>
            );
          }

          return (
            <AddProviderSheet title={config.name} onClose={closeAddSheet}>
              <ApiKeyEntry
                providerId={addSheetProvider}
                saving={savingKey}
                onSave={handleSaveApiKey}
              />
            </AddProviderSheet>
          );
        })()}
    </div>
  );
}
