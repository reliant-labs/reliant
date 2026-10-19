import { useMemo, useState } from "react";
import { AlertCircle } from "lucide-react";
import { cn } from "../../../lib/utils";
import type { ProviderAccount } from "../../../api/provider-account-grpc";
import {
  useProviderAccountActions,
  useProviderAccountsView,
  useRoutingMode,
  type RoutingMode,
} from "../../../hooks/provider-account-queries";
import { AccountRow } from "./AccountRow";
import { AddAccountButton, ConnectPanel } from "./AddAccountButton";
import { sortByPriority } from "./accountFormat";
import { useNow } from "./useNow";

const PROVIDER_ORDER = [
  "claude",
  "codex",
  "antigravity",
  "copilot",
  "reliant",
  "anthropic",
  "openai",
  "gemini",
  "openrouter",
];

/** Providers with their accounts, in the order Settings lists providers. */
export function groupByProvider(accounts: ProviderAccount[]): Array<{
  provider: string;
  displayName: string;
  accounts: ProviderAccount[];
}> {
  const byProvider = new Map<string, ProviderAccount[]>();
  for (const a of accounts) {
    byProvider.set(a.provider, [...(byProvider.get(a.provider) ?? []), a]);
  }
  const rank = (p: string) => {
    const i = PROVIDER_ORDER.indexOf(p);
    return i === -1 ? PROVIDER_ORDER.length : i;
  };
  return [...byProvider.entries()]
    .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b))
    .map(([provider, list]) => ({
      provider,
      displayName: list[0].providerDisplayName || provider,
      accounts: sortByPriority(list),
    }));
}

const ROUTING_OPTIONS: Array<{ value: RoutingMode; label: string; hint: string }> = [
  {
    value: "balance",
    label: "Balance",
    hint: "New chats start on the least-used account.",
  },
  {
    value: "priority",
    label: "In order",
    hint: "Use the first account until it is limited, then the next.",
  },
];

function RoutingModeSelect({
  provider,
  variant,
}: {
  provider: string;
  variant: "desktop" | "mobile";
}) {
  const { mode, setMode } = useRoutingMode(provider);
  const hint = ROUTING_OPTIONS.find((o) => o.value === mode)?.hint;
  return (
    <div className="flex flex-col gap-1">
      <label className="flex items-center gap-2 text-xs text-muted-foreground">
        <span>Routing</span>
        <select
          aria-label="Routing"
          value={mode}
          onChange={(e) => void setMode(e.target.value as RoutingMode)}
          className={cn(
            "rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring/40",
            variant === "mobile" ? "min-h-[44px]" : "py-1",
          )}
        >
          {ROUTING_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      </label>
      {hint && <p className="text-2xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

interface ProviderCardProps {
  provider: string;
  displayName: string;
  accounts: ProviderAccount[];
  variant: "desktop" | "mobile";
  now: Date;
}

function ProviderCard({ provider, displayName, accounts, variant, now }: ProviderCardProps) {
  const actions = useProviderAccountActions();
  const [error, setError] = useState<string | null>(null);
  const [reconnecting, setReconnecting] = useState<ProviderAccount | null>(null);
  const [refreshingId, setRefreshingId] = useState<string | null>(null);
  const [refreshNotes, setRefreshNotes] = useState<Record<string, string>>({});
  const [draggingId, setDraggingId] = useState<string | null>(null);

  const run = async (fn: () => Promise<unknown>) => {
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Something went wrong. Please try again.");
    }
  };

  const ids = useMemo(() => accounts.map((a) => a.id), [accounts]);

  const moveTo = (id: string, to: number) => {
    const from = ids.indexOf(id);
    if (from === -1 || to < 0 || to >= ids.length || from === to) return;
    const next = [...ids];
    next.splice(to, 0, next.splice(from, 1)[0]);
    void run(() => actions.reorder(provider, next));
  };

  const remove = (account: ProviderAccount) => {
    const last = accounts.length === 1;
    const prompt = [
      `Remove ${account.label}?`,
      last
        ? `${displayName} stops working until you connect an account again.`
        : `Chats running on it move to your other ${displayName} accounts.`,
      "Your other providers stay connected.",
    ].join("\n\n");
    if (!confirm(prompt)) return;
    void run(() => actions.remove(account));
  };

  const refresh = async (account: ProviderAccount) => {
    setRefreshingId(account.id);
    setRefreshNotes((n) => ({ ...n, [account.id]: "" }));
    try {
      const throttled = await actions.refreshUsage(account);
      if (throttled) {
        setRefreshNotes((n) => ({ ...n, [account.id]: "Just refreshed — try again in a minute" }));
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not refresh usage.");
    } finally {
      setRefreshingId(null);
    }
  };

  const mobile = variant === "mobile";

  return (
    <section
      data-testid="provider-card"
      data-provider={provider}
      aria-label={displayName}
      className={cn(
        "rounded-lg border border-border/40 bg-card p-4",
        mobile ? "elevation-1 border-0" : "shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]",
      )}
    >
      <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div>
          <h4 className="text-base font-semibold text-foreground">{displayName}</h4>
          <p className="text-xs text-muted-foreground">
            {accounts.length} {accounts.length === 1 ? "account" : "accounts"}
            {accounts.length > 1 && " · listed in priority order"}
          </p>
        </div>
        {accounts.length > 1 && <RoutingModeSelect provider={provider} variant={variant} />}
      </div>

      {error && (
        <div
          role="alert"
          className="mb-3 flex items-start gap-2 rounded-md border border-destructive/20 bg-destructive/10 p-2 text-xs text-destructive-ink"
        >
          <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>{error}</span>
        </div>
      )}

      <ol className="space-y-2" aria-label={`${displayName} accounts`}>
        {accounts.map((account, index) => (
          <AccountRow
            key={account.id}
            account={account}
            index={index}
            count={accounts.length}
            now={now}
            variant={variant}
            onRename={(label) => void run(() => actions.rename(account, label))}
            onTogglePause={() =>
              void run(() => actions.setPaused(account, account.status !== "paused"))
            }
            onMove={(dir) => moveTo(account.id, index + dir)}
            onReconnect={() => setReconnecting(account)}
            onRemove={() => remove(account)}
            onRefresh={() => void refresh(account)}
            refreshing={refreshingId === account.id}
            refreshNote={refreshNotes[account.id] || undefined}
            drag={
              mobile
                ? undefined
                : {
                    onDragStart: setDraggingId,
                    onDragEnd: () => setDraggingId(null),
                    onDropOn: (targetId) => {
                      if (draggingId) moveTo(draggingId, ids.indexOf(targetId));
                      setDraggingId(null);
                    },
                    dragging: draggingId === account.id,
                  }
            }
          />
        ))}
      </ol>

      {reconnecting ? (
        <ConnectPanel
          provider={provider}
          mode="reconnect"
          account={reconnecting}
          variant={variant}
          addApiKey={actions.addApiKey}
          onConnected={actions.invalidate}
          onClose={() => setReconnecting(null)}
        />
      ) : (
        <div className="mt-3">
          <AddAccountButton
            provider={provider}
            variant={variant}
            addApiKey={actions.addApiKey}
            onConnected={actions.invalidate}
          />
        </div>
      )}
    </section>
  );
}

interface AccountListProps {
  accounts: ProviderAccount[];
  variant?: "desktop" | "mobile";
}

/** Every connected provider as a card of its accounts, in priority order. */
export function AccountList({ accounts, variant = "desktop" }: AccountListProps) {
  const now = useNow();
  const groups = useMemo(() => groupByProvider(accounts), [accounts]);
  if (groups.length === 0) return null;
  return (
    <div className="space-y-3" data-testid="account-list">
      {groups.map((g) => (
        <ProviderCard
          key={g.provider}
          provider={g.provider}
          displayName={g.displayName}
          accounts={g.accounts}
          variant={variant}
          now={now}
        />
      ))}
    </div>
  );
}

/**
 * The account-aware replacement for the single-credential "Configured
 * Providers" list. Returns null until the server has answered with at least
 * one account, so callers can fall back to today's view on `null`.
 */
export function useAccountsSection(): ProviderAccount[] | null {
  const view = useProviderAccountsView();
  return view.status === "ready" && view.accounts.length > 0 ? view.accounts : null;
}

export function accountProviderIds(accounts: ProviderAccount[]): ReadonlySet<string> {
  return new Set(accounts.map((a) => a.provider));
}

export function ProviderAccountsSection({ accounts }: { accounts: ProviderAccount[] }) {
  return (
    <div>
      <h3 className="mb-4 text-lg font-semibold">Configured Providers</h3>
      <AccountList accounts={accounts} />
    </div>
  );
}
