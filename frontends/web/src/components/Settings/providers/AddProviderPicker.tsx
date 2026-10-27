import { useState } from "react";
import { ArrowLeft, Loader2 } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { cn } from "../../../lib/utils";
import { useProviderAccountActions } from "../../../hooks/provider-account-queries";
import { ConnectPanel } from "./ConnectPanel";
import { ProviderMark } from "./ProviderMark";
import { CONNECT_GROUPS, providerConfigs, VISIBLE_PROVIDERS, type ProviderId } from "./providerCatalog";

/** What the picker knows about Reliant credits, when this deployment offers them. */
export interface ReliantOffer {
  /** Reliant is already connected. */
  connected: boolean;
  /** "$18.40" — the wallet balance, when it has any. */
  balance?: string;
  enabling: boolean;
  onEnable: () => void;
  onOpenBilling: () => void;
}

export interface AddProviderPickerProps {
  /** Connected account count per provider. */
  connected: ReadonlyMap<string, number>;
  /** Absent when this deployment has no Reliant credits (no control plane). */
  reliant?: ReliantOffer;
  /** A connected provider was picked: open its card's "Add another …" panel. */
  onPickConnected: (provider: string) => void;
  onClose: () => void;
  variant?: "desktop" | "mobile";
}

/**
 * "Add provider": every provider, grouped by how you connect, with the ones
 * already connected marked "Connected · N" rather than removed. Picking a new
 * one opens the connect panel here; picking a connected one opens the same
 * panel on its card, so adding a second Claude account is reachable from
 * either place and neither is a dead end once everything is connected.
 */
export function AddProviderPicker({
  connected,
  reliant,
  onPickConnected,
  onClose,
  variant = "desktop",
}: AddProviderPickerProps) {
  const actions = useProviderAccountActions();
  const [choosing, setChoosing] = useState<ProviderId | null>(null);
  const mobile = variant === "mobile";

  const pick = (id: ProviderId) => {
    if (id === "reliant") {
      if (!reliant) return;
      if (reliant.connected) onPickConnected(id);
      else if (reliant.balance) reliant.onEnable();
      else reliant.onOpenBilling();
      return;
    }
    if ((connected.get(id) ?? 0) > 0) {
      onPickConnected(id);
      return;
    }
    setChoosing(id);
  };

  return (
    <section
      aria-label="Add a provider"
      data-testid="add-provider-picker"
      className="rounded-lg border border-border bg-card p-4"
    >
      <div className="mb-3 flex items-center justify-between gap-2">
        <div className="flex items-center gap-1">
          {choosing && (
            <Button
              variant="ghost"
              size="sm"
              icon={<ArrowLeft />}
              aria-label="Back to providers"
              onClick={() => setChoosing(null)}
            />
          )}
          <h2 className="text-sm font-semibold text-foreground">Add a provider</h2>
        </div>
        <Button
          variant="ghost"
          size={mobile ? "xl" : "sm"}
          className={mobile ? "min-h-[44px]" : undefined}
          onClick={onClose}
        >
          Close
        </Button>
      </div>

      {choosing ? (
        <ConnectPanel
          provider={choosing}
          mode="add"
          variant={variant}
          addApiKey={actions.addApiKey}
          onConnected={actions.invalidate}
          onClose={onClose}
        />
      ) : (
        <div className="space-y-4">
          {CONNECT_GROUPS.map((group) => {
            const ids = VISIBLE_PROVIDERS.filter(
              (id) => providerConfigs[id].connect === group.method && (id !== "reliant" || reliant),
            );
            if (ids.length === 0) return null;
            return (
              <div key={group.method}>
                <h3 className="mb-2 text-xs text-muted-foreground">{group.label}</h3>
                <ul className="grid gap-2 sm:grid-cols-2">
                  {ids.map((id) => (
                    <li key={id}>
                      <ProviderTile
                        id={id}
                        count={id === "reliant" ? (reliant?.connected ? 1 : 0) : (connected.get(id) ?? 0)}
                        reliant={id === "reliant" ? reliant : undefined}
                        onPick={() => pick(id)}
                      />
                    </li>
                  ))}
                </ul>
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

function ProviderTile({
  id,
  count,
  reliant,
  onPick,
}: {
  id: ProviderId;
  count: number;
  reliant?: ReliantOffer;
  onPick: () => void;
}) {
  const config = providerConfigs[id];
  let status: string;
  let tone = "text-muted-foreground";
  if (count > 0) {
    status = id === "reliant" ? "Connected" : `Connected · ${count}`;
    tone = "text-success-ink";
  } else if (reliant) {
    status = reliant.balance ? `${reliant.balance} available · Enable` : "Add credits in Billing";
  } else {
    status = config.blurb;
  }
  const busy = !!reliant?.enabling;
  return (
    <button
      type="button"
      onClick={onPick}
      disabled={busy}
      aria-label={`${config.name}${count > 0 ? `, connected` : ""}`}
      data-provider={id}
      className={cn(
        "flex w-full items-center gap-3 rounded-lg border border-border/60 bg-background p-3 text-left transition-colors",
        "hover:border-border hover:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 disabled:opacity-60",
      )}
    >
      <ProviderMark provider={id} />
      <span className="min-w-0">
        <span className="block text-sm font-medium text-foreground">{config.name}</span>
        <span className={cn("block truncate text-xs", tone)}>
          {busy ? (
            <span className="inline-flex items-center gap-1">
              <Loader2 className="h-3 w-3 animate-spin" /> Enabling…
            </span>
          ) : (
            status
          )}
        </span>
      </span>
    </button>
  );
}
