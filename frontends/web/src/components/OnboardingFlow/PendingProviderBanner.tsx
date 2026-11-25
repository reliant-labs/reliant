import { useCallback, useEffect, useRef, useState } from "react";
import { PlugZap, X } from "lucide-react";

import { hasAnyConfiguredProvider, useProviderStatuses } from "@/hooks/settings-queries";
import { trackEvent } from "@/lib/analytics";
import { useAuthIdentity } from "@/lib/auth/use-auth";
import { pendingProviderCopy } from "@/lib/changes_gen";
import { changesStore, userChangeContext } from "@/lib/changesStore";
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
 *
 * Its copy is the Forge Change `pending-provider-copy` (changes.yaml, unit
 * `user`): `current` is the banner below as it always was; `outcome-first`
 * leads with what connecting gets the person and offers the API key as a
 * second button. The change is decided once, when the banner first shows (the
 * evaluation is the person's enrollment, so it must not happen for someone
 * who never sees it), and kept for the banner's life. Until the change is
 * launched in the console, everyone gets `current`.
 */
export function PendingProviderBanner() {
  const [provider, setProvider] = useState<keyof typeof REDIRECT_OAUTH_DISPLAY_NAMES | null>(null);
  const statuses = useProviderStatuses();
  const openModal = useModalStore((s) => s.openModal);
  const identity = useAuthIdentity();
  const outcomeFirst = useRef<boolean | null>(null);

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
  if (outcomeFirst.current === null) {
    const store = changesStore();
    // No store yet (or the desktop app): `current`, and no enrollment.
    outcomeFirst.current = store ? pendingProviderCopy(store, userChangeContext(identity)) : false;
  }

  if (outcomeFirst.current) {
    return <OutcomeFirstBanner name={name} onConnect={connect} onDismiss={dismiss} />;
  }

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

/**
 * The `outcome-first` variant of the banner (the change `pending-provider-copy`):
 * the same region, actions and events, with the heading saying what connecting
 * gets the person and the API key as a second button rather than a link, for
 * someone with no desktop app or CLI on this computer.
 */
function OutcomeFirstBanner({
  name,
  onConnect,
  onDismiss,
}: {
  name: string;
  onConnect: (which: "subscription" | "key") => void;
  onDismiss: () => void;
}) {
  return (
    <div
      role="region"
      aria-label="Finish connecting your model"
      data-testid="pending-provider-banner"
      data-change-variant="outcome-first"
      className="flex w-full items-start gap-3 rounded-lg border border-primary/30 bg-primary/5 p-3 text-left"
    >
      <PlugZap className="mt-0.5 h-4 w-4 shrink-0 text-primary-ink" aria-hidden />
      <div className="min-w-0 flex-1 space-y-1">
        <p className="text-sm font-medium text-foreground">Connect a model to start chatting</p>
        <p className="text-xs text-muted-foreground">
          Reliant needs a model to answer you. Your {name} signs in on your own computer, through the Reliant desktop
          app or CLI. No app there yet? An API key works right here.
        </p>
        <div className="flex flex-wrap items-center gap-2 pt-1">
          <button
            type="button"
            onClick={() => onConnect("subscription")}
            className="rounded-md bg-primary px-3 py-1.5 text-xs font-semibold text-primary-foreground hover:bg-primary/90"
          >
            Connect {name}
          </button>
          <button
            type="button"
            onClick={() => onConnect("key")}
            className="rounded-md border border-border px-3 py-1.5 text-xs font-semibold text-foreground hover:bg-foreground/5"
          >
            Use an API key
          </button>
        </div>
      </div>
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss"
        className="rounded p-1 text-muted-foreground hover:text-foreground"
      >
        <X className="h-4 w-4" aria-hidden />
      </button>
    </div>
  );
}
