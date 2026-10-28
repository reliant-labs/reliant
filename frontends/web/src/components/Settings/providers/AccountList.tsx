import { useEffect, useMemo, useRef, useState } from "react";
import { AlertCircle, KeyRound, Plus } from "lucide-react";
import Button from "@/components/forge-ui/button";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import { cn } from "../../../lib/utils";
import type { ProviderAccount } from "../../../types/providerAccount";
import { OnLimitDefault } from "./OnLimitDefault";
import { useProviderAccountActions } from "../../../hooks/provider-account-queries";
import { useRoutingMode, type RoutingMode } from "../../../hooks/provider-routing-queries";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import {
  AccountActionsCell,
  AccountDetail,
  AccountNameCell,
  AccountOrderCell,
  AccountUsageCell,
} from "./AccountRow";
import { ConnectPanel } from "./ConnectPanel";
import { needsAttention, sortByPriority } from "./accountFormat";

/** One account as its provider's table holds it. */
type AccountTableRow = { id: string; account: ProviderAccount };
import { accountNoun, addAnotherLabel, providerName, VISIBLE_PROVIDERS } from "./providerCatalog";
import { ProviderMark } from "./ProviderMark";
import { useNow } from "./useNow";

const PROVIDER_ORDER: readonly string[] = VISIBLE_PROVIDERS;

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
      displayName: providerName(provider, list[0].providerDisplayName),
      accounts: sortByPriority(list),
    }));
}

const ROTATION_OPTIONS: Array<{ value: RoutingMode; label: string; hint: string }> = [
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

function RotationSelect({ provider, variant }: { provider: string; variant: "desktop" | "mobile" }) {
  const { mode, setMode } = useRoutingMode(provider);
  const hint = ROTATION_OPTIONS.find((o) => o.value === mode)?.hint;
  return (
    <label className="flex items-center gap-1.5 text-xs text-muted-foreground" title={hint}>
      <span>Rotation</span>
      <select
        aria-label="Rotation"
        value={mode}
        onChange={(e) => void setMode(e.target.value as RoutingMode)}
        className={cn(
          "rounded-md border border-border bg-background px-2 text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-ring/40",
          variant === "mobile" ? "min-h-[44px] text-sm" : "py-0.5",
        )}
      >
        {ROTATION_OPTIONS.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  );
}

function Dot() {
  return (
    <span aria-hidden="true" className="text-muted-foreground/60">
      ·
    </span>
  );
}

interface ProviderCardProps {
  provider: string;
  displayName: string;
  accounts: ProviderAccount[];
  variant: "desktop" | "mobile";
  now: Date;
  /** The card's "Add another …" panel is open. */
  adding: boolean;
  onAddingChange: (open: boolean) => void;
  /** Opens Reliant's keys and spend (in Billing). Only the Reliant card uses it. */
  onOpenReliantBilling?: () => void;
}

function ProviderCard({
  provider,
  displayName,
  accounts,
  variant,
  now,
  adding,
  onAddingChange,
  onOpenReliantBilling,
}: ProviderCardProps) {
  const actions = useProviderAccountActions();
  const [error, setError] = useState<string | null>(null);
  const [reconnecting, setReconnecting] = useState<ProviderAccount | null>(null);
  const [removing, setRemoving] = useState<ProviderAccount | null>(null);
  const [refreshingId, setRefreshingId] = useState<string | null>(null);
  const [refreshNotes, setRefreshNotes] = useState<Record<string, string>>({});
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [expandedByUser, setExpandedByUser] = useState<Record<string, boolean>>({});
  const cardRef = useRef<HTMLElement>(null);

  // The page's "Add provider" picker opens this card's panel for a provider
  // that is already connected; bring the card to the user.
  useEffect(() => {
    if (adding) cardRef.current?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
  }, [adding]);

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

  const confirmRemove = () => {
    const account = removing;
    setRemoving(null);
    if (account) void run(() => actions.remove(account));
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
  const isReliant = provider === "reliant";
  const count = accounts.length;
  const noun = accountNoun(provider);
  const several = count > 1;
  const draggable = !mobile && several;

  // null follows the account's state; a click pins it open or shut.
  const isOpen = (account: ProviderAccount) => expandedByUser[account.id] ?? needsAttention(account, now);
  const rows: AccountTableRow[] = accounts.map((account) => ({ id: account.id, account }));
  const columns: DataTableColumn<AccountTableRow>[] = [
    ...(several
      ? [
          {
            key: "order",
            header: "Priority",
            srHeader: true,
            width: mobile ? "2.5rem" : "4rem",
            className: "px-3",
            headerClassName: "px-3",
            interactive: true,
            render: (_: unknown, { account }: AccountTableRow) => {
              const index = ids.indexOf(account.id);
              return (
                <AccountOrderCell
                  account={account}
                  index={index}
                  count={count}
                  onMove={(dir) => moveTo(account.id, index + dir)}
                  drag={draggable ? { onDragStart: setDraggingId, onDragEnd: () => setDraggingId(null) } : undefined}
                />
              );
            },
          } satisfies DataTableColumn<AccountTableRow>,
        ]
      : []),
    {
      key: "account",
      header: "Account",
      render: (_, { account }) => (
        <AccountNameCell
          account={account}
          now={now}
          mobile={mobile}
          renaming={renamingId === account.id}
          onRename={(label) => void run(() => actions.rename(account, label))}
          onRenameEnd={() => setRenamingId(null)}
        />
      ),
    },
    ...(mobile
      ? []
      : [
          {
            key: "usage",
            header: "Usage",
            width: "13rem",
            className: "max-sm:hidden",
            headerClassName: "max-sm:hidden",
            render: (_: unknown, { account }: AccountTableRow) => (
              <AccountUsageCell account={account} now={now} open={isOpen(account)} />
            ),
          } satisfies DataTableColumn<AccountTableRow>,
        ]),
    {
      key: "actions",
      header: "Actions",
      srHeader: true,
      interactive: true,
      width: mobile ? "7rem" : accounts.some((a) => a.status === "needs_sign_in") ? "10.5rem" : "5.5rem",
      className: "px-3",
      headerClassName: "px-3",
      render: (_, { account }) => {
        const index = ids.indexOf(account.id);
        return (
          <AccountActionsCell
            account={account}
            index={index}
            count={count}
            mobile={mobile}
            open={isOpen(account)}
            onToggleOpen={() => setExpandedByUser((s) => ({ ...s, [account.id]: !isOpen(account) }))}
            onStartRename={() => setRenamingId(account.id)}
            onTogglePause={() => void run(() => actions.setPaused(account, account.status !== "paused"))}
            onMove={(dir) => moveTo(account.id, index + dir)}
            onReconnect={() => {
              onAddingChange(false);
              setReconnecting(account);
            }}
            onRemove={() => setRemoving(account)}
          />
        );
      },
    },
  ];

  return (
    <section
      ref={cardRef}
      data-testid="provider-card"
      data-provider={provider}
      aria-label={displayName}
      className="rounded-lg border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-3">
          <ProviderMark provider={provider} />
          <div className="min-w-0">
            <h3 className="text-sm font-semibold text-foreground">{displayName}</h3>
            <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
              <span>
                {count} {count === 1 ? noun : `${noun}s`}
              </span>
              {count > 1 && (
                <>
                  <Dot />
                  <RotationSelect provider={provider} variant={variant} />
                </>
              )}
              <Dot />
              <OnLimitDefault provider={provider} displayName={displayName} variant={variant} />
            </div>
          </div>
        </div>
        {isReliant ? (
          onOpenReliantBilling && (
            <Button
              variant="ghost"
              size={mobile ? "xl" : "sm"}
              className={mobile ? "min-h-[44px]" : undefined}
              leftIcon={<KeyRound />}
              onClick={onOpenReliantBilling}
            >
              Keys &amp; spend
            </Button>
          )
        ) : (
          <Button
            variant="ghost"
            size={mobile ? "xl" : "sm"}
            className={mobile ? "min-h-[44px]" : undefined}
            leftIcon={<Plus />}
            aria-expanded={adding}
            onClick={() => {
              setReconnecting(null);
              onAddingChange(!adding);
            }}
          >
            {addAnotherLabel(provider)}
          </Button>
        )}
      </div>

      {(adding || reconnecting) && (
        <div className="mt-3">
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
            <ConnectPanel
              provider={provider}
              mode="add"
              another
              variant={variant}
              addApiKey={actions.addApiKey}
              onConnected={actions.invalidate}
              onClose={() => onAddingChange(false)}
            />
          )}
        </div>
      )}

      {error && (
        <div
          role="alert"
          className="mt-3 flex items-start gap-2 rounded-md border border-destructive/20 bg-destructive/10 p-2 text-xs text-destructive-ink"
        >
          <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>{error}</span>
        </div>
      )}

      <div className="forge-ui mt-3 overflow-hidden rounded-lg border border-border/60 bg-background">
        <DataTable<AccountTableRow>
          ariaLabel={`${displayName} accounts`}
          flush
          layout="fixed"
          columns={columns}
          data={rows}
          getRowKey={(row) => row.id}
          getRowProps={({ account }) => ({
            "data-testid": "account-row",
            "data-account-id": account.id,
            "data-expanded": String(isOpen(account)),
            className: cn(
              "bg-background",
              draggingId === account.id && "opacity-50",
              account.status === "paused" && "opacity-80",
            ),
            ...(draggable
              ? {
                  onDragOver: (e) => e.preventDefault(),
                  onDrop: (e) => {
                    e.preventDefault();
                    if (draggingId) moveTo(draggingId, ids.indexOf(account.id));
                    setDraggingId(null);
                  },
                }
              : {}),
          })}
          getRowDetail={({ account }) =>
            isOpen(account) ? (
              <AccountDetail
                account={account}
                now={now}
                mobile={mobile}
                indent={several}
                onRefresh={() => void refresh(account)}
                refreshing={refreshingId === account.id}
                refreshNote={refreshNotes[account.id] || undefined}
              />
            ) : undefined
          }
        />
      </div>

      <ConfirmationDialog
        open={removing !== null}
        title={removing ? `Remove ${removing.label}?` : ""}
        description={
          count === 1
            ? `${displayName} stops working until you connect an account again. Your other providers stay connected.`
            : `Chats running on it move to your other ${displayName} ${noun}s. Your other providers stay connected.`
        }
        confirmLabel="Remove"
        onConfirm={confirmRemove}
        onCancel={() => setRemoving(null)}
      />
    </section>
  );
}

interface AccountListProps {
  accounts: ProviderAccount[];
  variant?: "desktop" | "mobile";
  /**
   * The provider whose "Add another …" panel is open. Controlled by the page so
   * its "Add provider" picker can open a connected provider's panel; left
   * unset, the list keeps the state itself.
   */
  addingFor?: string | null;
  onAddingForChange?: (provider: string | null) => void;
  onOpenReliantBilling?: () => void;
}

/** Every connected provider as a card of its accounts, in priority order. */
export function AccountList({
  accounts,
  variant = "desktop",
  addingFor: addingForProp,
  onAddingForChange,
  onOpenReliantBilling,
}: AccountListProps) {
  const now = useNow();
  const groups = useMemo(() => groupByProvider(accounts), [accounts]);
  const [localAddingFor, setLocalAddingFor] = useState<string | null>(null);
  const addingFor = addingForProp !== undefined ? addingForProp : localAddingFor;
  const setAddingFor = onAddingForChange ?? setLocalAddingFor;
  if (groups.length === 0) return null;
  return (
    <div className="space-y-4" data-testid="account-list">
      {groups.map((g) => (
        <ProviderCard
          key={g.provider}
          provider={g.provider}
          displayName={g.displayName}
          accounts={g.accounts}
          variant={variant}
          now={now}
          adding={addingFor === g.provider}
          onAddingChange={(open) => setAddingFor(open ? g.provider : null)}
          onOpenReliantBilling={onOpenReliantBilling}
        />
      ))}
    </div>
  );
}
