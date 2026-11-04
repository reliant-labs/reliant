import { useMemo, useState } from "react";
import { AlertCircle, Loader2, Plus } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { useNavigate } from "@tanstack/react-router";
import { useProviderAccountActions, useProviderAccountsView } from "../../../hooks/provider-account-queries";
import { useWalletOverview } from "../../../hooks/useReliantAIQueries";
import { reliantAIAvailable } from "../../../services/controlPlane/reliantAI";
import { onboardingService } from "../../../services/controlPlane/onboarding";
import { useGlobalDataStore } from "../../../store/globalDataStore";
import { getEventBus } from "../../../lib/events";
import { formatCurrencyFromWalletFields } from "../cloud/billingUtils";
import type { ProviderAccount } from "../../../types/providerAccount";
import { AccountList } from "./AccountList";
import { AddProviderPicker, type ReliantOffer } from "./AddProviderPicker";
import { ConnectPanel } from "./ConnectPanel";
import { providerConfigs } from "./providerCatalog";

/**
 * Reliant credits as the picker and the empty state offer them: connected,
 * fundable now ("$18.40 available · Enable"), or a pointer to Billing.
 * Undefined when this deployment has no control plane, and so no credits.
 */
function useReliantOffer(accounts: ProviderAccount[], onError: (message: string) => void): ReliantOffer | undefined {
  const walletQ = useWalletOverview();
  const actions = useProviderAccountActions();
  const navigate = useNavigate();
  const [enabling, setEnabling] = useState(false);
  if (!reliantAIAvailable) return undefined;

  // Gated on LLM funding (the wallet), not compute eligibility: credit and
  // machine minutes are bought separately, and conflating them once hid
  // "Enable Reliant" from users holding real credit.
  const wallet = walletQ.data?.wallet;
  const nanos = wallet?.balanceUsdNanos;
  const hasFunds = nanos != null && BigInt(nanos) > 0n;
  return {
    connected: accounts.some((a) => a.provider === "reliant"),
    balance: hasFunds ? formatCurrencyFromWalletFields(nanos) : undefined,
    enabling,
    onEnable: () => {
      setEnabling(true);
      void (async () => {
        try {
          await onboardingService.provisionManagedKey();
          await actions.invalidate();
          await useGlobalDataStore.getState().refetchModels();
          getEventBus().emit("api-key:saved", { provider: "reliant" });
        } catch (e) {
          onError(e instanceof Error ? e.message : "Could not enable Reliant credits.");
        } finally {
          setEnabling(false);
        }
      })();
    },
    onOpenBilling: () => void navigate({ to: "/settings/$section", params: { section: "billing" } }),
  };
}

export interface ProvidersSectionProps {
  variant?: "desktop" | "mobile";
  /** The "Add provider" picker is open. The page header's button owns it. */
  pickerOpen: boolean;
  onPickerOpenChange: (open: boolean) => void;
}

/**
 * Settings → AI → Providers: the user's connected providers first, one card
 * each, and the one way to add more — the "Add provider" picker the page
 * header opens, or a card's scoped "Add another Claude account". Both open
 * the same connect panel.
 */
export function ProvidersSection({ variant = "desktop", pickerOpen, onPickerOpenChange }: ProvidersSectionProps) {
  const view = useProviderAccountsView();
  const actions = useProviderAccountActions();
  const navigate = useNavigate();
  const [addingFor, setAddingFor] = useState<string | null>(null);
  const [starting, setStarting] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const accounts = useMemo(() => (view.status === "ready" ? view.accounts : []), [view]);
  const connected = useMemo(() => {
    const counts = new Map<string, number>();
    for (const a of accounts) counts.set(a.provider, (counts.get(a.provider) ?? 0) + 1);
    return counts;
  }, [accounts]);
  const reliant = useReliantOffer(accounts, setError);

  const openReliantBilling = reliantAIAvailable
    ? () => void navigate({ to: "/settings/$section", params: { section: "billing" }, search: { tab: "ai" } })
    : undefined;

  const picker = pickerOpen && (
    <AddProviderPicker
      connected={connected}
      reliant={reliant}
      variant={variant}
      onPickConnected={(provider) => {
        onPickerOpenChange(false);
        if (provider !== "reliant") setAddingFor(provider);
      }}
      onClose={() => onPickerOpenChange(false)}
    />
  );

  const errorBanner = error && (
    <div
      role="alert"
      className="flex items-start gap-2 rounded-md border border-destructive/20 bg-destructive/10 p-3 text-sm text-destructive-ink"
    >
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
      <span>{error}</span>
    </div>
  );

  if (view.status === "loading") {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-border bg-card p-4 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        Loading your providers…
      </div>
    );
  }

  if (view.status === "unavailable") {
    return (
      <div className="space-y-4">
        <div
          role="alert"
          className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-card p-4 text-sm"
        >
          <span className="flex items-center gap-2 text-foreground">
            <AlertCircle className="h-4 w-4 shrink-0 text-destructive-ink" />
            Your providers could not be loaded.
          </span>
          <Button variant="outline" size="sm" onClick={() => void actions.invalidate()}>
            Try again
          </Button>
        </div>
        {picker}
      </div>
    );
  }

  if (accounts.length === 0) {
    return (
      <div className="space-y-4">
        {errorBanner}
        {picker || (
          <section
            aria-label="Connect a provider"
            data-testid="providers-empty"
            className="rounded-lg border border-border bg-card p-6"
          >
            <h2 className="text-sm font-semibold text-foreground">Connect the AI your agents run on</h2>
            <p className="mt-1 max-w-xl text-sm text-muted-foreground">
              Have a Claude or ChatGPT plan? Sign in with it and your agents use it the way you do. Otherwise add an
              API key{reliant ? ", or run on Reliant credits" : ""}.
            </p>
            {starting ? (
              <div className="mt-4">
                <ConnectPanel
                  provider={starting}
                  mode="add"
                  variant={variant}
                  addApiKey={actions.addApiKey}
                  onConnected={actions.invalidate}
                  onClose={() => setStarting(null)}
                />
              </div>
            ) : (
              <div className="mt-4 flex flex-wrap gap-2">
                {(["claude", "codex"] as const).map((id) => (
                  <Button
                    key={id}
                    variant="outline"
                    size={variant === "mobile" ? "xl" : "md"}
                    className={variant === "mobile" ? "min-h-[44px]" : undefined}
                    onClick={() => setStarting(id)}
                  >
                    Sign in with {providerConfigs[id].name}
                  </Button>
                ))}
                {reliant?.balance && (
                  <Button
                    variant="outline"
                    size={variant === "mobile" ? "xl" : "md"}
                    className={variant === "mobile" ? "min-h-[44px]" : undefined}
                    loading={reliant.enabling}
                    onClick={reliant.onEnable}
                  >
                    Use {reliant.balance} of Reliant credits
                  </Button>
                )}
                <Button
                  variant="ghost"
                  size={variant === "mobile" ? "xl" : "md"}
                    className={variant === "mobile" ? "min-h-[44px]" : undefined}
                  leftIcon={<Plus />}
                  onClick={() => onPickerOpenChange(true)}
                >
                  All providers
                </Button>
              </div>
            )}
          </section>
        )}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {errorBanner}
      {picker}
      <AccountList
        accounts={accounts}
        variant={variant}
        addingFor={addingFor}
        onAddingForChange={setAddingFor}
        onOpenReliantBilling={openReliantBilling}
      />
    </div>
  );
}
