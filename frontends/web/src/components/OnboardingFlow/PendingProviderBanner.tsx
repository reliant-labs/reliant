import { useCallback, useEffect, useState } from "react";
import { PlugZap, X } from "lucide-react";

import { hasAnyConfiguredProvider, useProviderStatuses } from "@/hooks/settings-queries";
import { trackEvent } from "@/lib/analytics";
import { REDIRECT_OAUTH_DISPLAY_NAMES } from "@/lib/oauth-providers";
import { clearPendingProvider, readPendingProvider } from "@/lib/pendingProvider";
import { useModalStore } from "@/store/modalStore";

/**
 * "Connect your Claude subscription", on the first chat.
 *
 * Onboarding lets a user choose a subscription sign-in before they have the
 * desktop app or CLI running (a cloud user reaches the model step first), and
 * records the choice instead of blocking on it. This is where it is finished:
 * it opens the same sign-in dialog Settings uses, on the chosen provider.
 *
 * It disappears by itself once any provider is configured, however that
 * happened (the sign-in here, a key pasted in Settings), and can be dismissed.
 */
export function PendingProviderBanner() {
  const [provider, setProvider] = useState<keyof typeof REDIRECT_OAUTH_DISPLAY_NAMES | null>(null);
  const statuses = useProviderStatuses();
  const openModal = useModalStore((s) => s.openModal);

  useEffect(() => {
    let cancelled = false;
    void readPendingProvider().then((id) => {
      if (!cancelled) setProvider(id);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  const configured = hasAnyConfiguredProvider(statuses.data?.providers ?? []);

  useEffect(() => {
    if (provider && configured) {
      void clearPendingProvider();
      setProvider(null);
    }
  }, [provider, configured]);

  const connect = useCallback(
    (which: "subscription" | "key") => {
      if (!provider) return;
      trackEvent("pending_provider_connect_clicked", { provider, via: which });
      openModal("api-key-setup", { provider: which === "key" ? "anthropic" : provider });
    },
    [openModal, provider],
  );

  const dismiss = useCallback(() => {
    trackEvent("pending_provider_dismissed", { provider: provider ?? "none" });
    void clearPendingProvider();
    setProvider(null);
  }, [provider]);

  if (!provider || configured) return null;
  const name = REDIRECT_OAUTH_DISPLAY_NAMES[provider];

  return (
    <div
      role="region"
      aria-label="Finish connecting your model"
      data-testid="pending-provider-banner"
      className="flex w-full items-start gap-3 rounded-lg border border-primary/30 bg-primary/5 p-3 text-left"
    >
      <PlugZap className="mt-0.5 h-4 w-4 shrink-0 text-primary-ink" aria-hidden />
      <div className="min-w-0 flex-1 space-y-1">
        <p className="text-sm font-medium text-foreground">Connect your {name}</p>
        <p className="text-xs text-muted-foreground">
          You chose to sign in with it. That sign-in runs on your own computer, so it needs the Reliant desktop app or
          CLI there. Until it is connected, Reliant has no model to answer with.
        </p>
        <div className="flex flex-wrap items-center gap-3 pt-1">
          <button
            type="button"
            onClick={() => connect("subscription")}
            className="rounded-md bg-primary px-3 py-1.5 text-xs font-semibold text-primary-foreground hover:bg-primary/90"
          >
            Connect {name}
          </button>
          <button
            type="button"
            onClick={() => connect("key")}
            className="text-xs font-medium text-primary-ink hover:underline"
          >
            Use an API key instead
          </button>
        </div>
      </div>
      <button
        type="button"
        onClick={dismiss}
        aria-label="Dismiss"
        className="rounded p-1 text-muted-foreground hover:text-foreground"
      >
        <X className="h-4 w-4" aria-hidden />
      </button>
    </div>
  );
}
