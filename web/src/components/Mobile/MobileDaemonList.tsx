/**
 * `/m/daemons` — mobile machine management.
 *
 * The phone surface can now perform the high-value lifecycle work users need
 * away from desktop: create a cloud machine, watch status, resume, suspend,
 * and delete. Port access, tokens, and detailed diagnostics remain desktop
 * settings because they need denser forms and copy-once secrets.
 *
 * Data comes from the same `useDaemonList` / daemon mutation hooks the desktop
 * surfaces use, so every lifecycle action refreshes one shared cache entry. The
 * only mobile-specific behavior is the poll gate — see `useVisibilityPolling`.
 */

import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { ChevronRight, Loader2, Plus, Server, X } from "lucide-react";
import { Virtuoso } from "react-virtuoso";
import { DaemonSize, DaemonType } from "@/gen/controlplane/v1/public/shared_pb";
import {
  isEntitlementDenial,
  useCreateDaemon,
  useDaemonList,
} from "@/hooks/useOnboardingQueries";
import type { Daemon } from "@/services/controlPlane/daemon";
import { cn } from "../../lib/utils";
import { heartbeatMs, presentDaemon, sizeLabel } from "./daemonPresentation";
import { relativeTimeFromMs } from "./relativeTime";
import { useVisibilityPolling } from "./useVisibilityPolling";
import { MobileMenuButton } from "./MobileMenuButton";
import {
  MOBILE_PRIMARY_ACTION,
  MOBILE_ROW,
  MobileEmptyState,
  MobileRowIcon,
  MobileScreenHeader,
} from "./MobileChrome";

/** Matches the desktop daemon poll so the shared cache entry stays fresh. */
const POLL_INTERVAL_MS = 5_000;

const MOBILE_FIELD =
  "w-full min-h-[44px] rounded-lg border border-border bg-background px-3 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring/20";

const MOBILE_SECONDARY_ACTION =
  "flex min-h-[44px] items-center justify-center rounded-lg border border-border px-4 text-sm font-medium text-foreground active:bg-foreground/5 disabled:opacity-60";

const SIZE_OPTIONS = [
  { value: DaemonSize.SMALL, label: "Small", specs: "1 CPU · 2GB RAM" },
  { value: DaemonSize.MEDIUM, label: "Medium", specs: "2 CPU · 4GB RAM" },
  { value: DaemonSize.LARGE, label: "Large", specs: "4 CPU · 8GB RAM" },
  { value: DaemonSize.XL, label: "XL", specs: "8 CPU · 16GB RAM" },
] as const;

const IDLE_TIMEOUT_OPTIONS = [
  { value: "15m", label: "15 minutes" },
  { value: "30m", label: "30 minutes" },
  { value: "1h", label: "1 hour" },
  { value: "2h", label: "2 hours" },
  { value: "4h", label: "4 hours" },
] as const;

export function DaemonStatusPill({ daemon }: { daemon: Daemon }) {
  const { label, pillClassName } = presentDaemon(daemon);
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset",
        pillClassName,
      )}
    >
      {label}
    </span>
  );
}

function CreateMachineSheet({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("mobile-machine");
  const [size, setSize] = useState<DaemonSize>(DaemonSize.SMALL);
  const [idleTimeout, setIdleTimeout] = useState("30m");
  const [gitRepo, setGitRepo] = useState("");
  const [gitBranch, setGitBranch] = useState("main");
  const [error, setError] = useState("");

  const create = useCreateDaemon();
  const busy = create.isPending;

  const submit = async () => {
    const trimmedName = name.trim();
    if (!trimmedName || busy) return;
    setError("");
    try {
      await create.mutateAsync({
        name: trimmedName,
        daemonType: DaemonType.MANAGED,
        size,
        idleTimeout,
        gitRepo: gitRepo.trim() || undefined,
        gitBranch: gitBranch.trim() || "main",
      });
      onClose();
    } catch (err) {
      if (isEntitlementDenial(err)) return;
      setError(err instanceof Error ? err.message : "Failed to create machine");
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex flex-col justify-end">
      <button
        type="button"
        aria-label="Dismiss"
        onClick={busy ? undefined : onClose}
        disabled={busy}
        className="absolute inset-0 bg-black/40"
      />
      <div
        role="dialog"
        aria-modal="true"
        aria-label="New machine"
        className="relative flex max-h-[90vh] flex-col rounded-t-2xl border-t border-border bg-background shadow-lg"
        style={{ paddingBottom: "env(safe-area-inset-bottom)" }}
      >
        <div className="flex min-h-[56px] items-center justify-between border-b border-border px-4 py-2">
          <span className="text-sm font-semibold text-foreground">New machine</span>
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            aria-label="Close"
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted disabled:opacity-40"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <form
          className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          {error && (
            <div className="rounded-lg border border-destructive/30 bg-destructive/10 p-3">
              <p className="text-sm text-destructive">{error}</p>
            </div>
          )}

          <div>
            <label
              htmlFor="mobile-machine-name"
              className="mb-1 block text-xs font-semibold uppercase tracking-wide text-muted-foreground"
            >
              Name
            </label>
            <input
              id="mobile-machine-name"
              required
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder="mobile-machine"
              className={MOBILE_FIELD}
            />
          </div>

          <div>
            <p className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Size
            </p>
            <div className="grid grid-cols-2 gap-2">
              {SIZE_OPTIONS.map((option) => {
                const selected = option.value === size;
                return (
                  <button
                    key={option.value}
                    type="button"
                    onClick={() => setSize(option.value)}
                    aria-pressed={selected}
                    className={cn(
                      "rounded-lg border p-3 text-left active:bg-foreground/5",
                      selected
                        ? "border-primary bg-primary/10"
                        : "border-border bg-background",
                    )}
                  >
                    <p className="text-sm font-medium text-foreground">{option.label}</p>
                    <p className="mt-1 text-xs text-muted-foreground">{option.specs}</p>
                  </button>
                );
              })}
            </div>
          </div>

          <div>
            <label
              htmlFor="mobile-machine-idle-timeout"
              className="mb-1 block text-xs font-semibold uppercase tracking-wide text-muted-foreground"
            >
              Auto-suspend after inactivity
            </label>
            <select
              id="mobile-machine-idle-timeout"
              value={idleTimeout}
              onChange={(event) => setIdleTimeout(event.target.value)}
              className={MOBILE_FIELD}
            >
              {IDLE_TIMEOUT_OPTIONS.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
            <p className="mt-1 text-xs text-muted-foreground">
              Suspended machines are not billed.
            </p>
          </div>

          <div>
            <label
              htmlFor="mobile-machine-repo"
              className="mb-1 block text-xs font-semibold uppercase tracking-wide text-muted-foreground"
            >
              Repository URL (optional)
            </label>
            <input
              id="mobile-machine-repo"
              type="url"
              value={gitRepo}
              onChange={(event) => setGitRepo(event.target.value)}
              placeholder="https://github.com/owner/repo.git"
              className={MOBILE_FIELD}
            />
          </div>

          <div>
            <label
              htmlFor="mobile-machine-branch"
              className="mb-1 block text-xs font-semibold uppercase tracking-wide text-muted-foreground"
            >
              Branch
            </label>
            <input
              id="mobile-machine-branch"
              value={gitBranch}
              onChange={(event) => setGitBranch(event.target.value)}
              placeholder="main"
              className={MOBILE_FIELD}
            />
          </div>

          <div className="flex gap-2 border-t border-border pt-4">
            <button
              type="button"
              onClick={onClose}
              disabled={busy}
              className={cn(MOBILE_SECONDARY_ACTION, "flex-1")}
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!name.trim() || busy}
              className={cn(MOBILE_PRIMARY_ACTION, "flex-1")}
            >
              {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
              {busy ? "Creating…" : "Create"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function NewMachineButton({ onCreate }: { onCreate: () => void }) {
  return (
    <button
      type="button"
      onClick={onCreate}
      aria-label="New machine"
      className="flex min-h-[44px] items-center gap-1 rounded-lg px-3 text-sm font-medium text-primary active:bg-primary/10"
    >
      <Plus className="h-4 w-4" />
      <span>New</span>
    </button>
  );
}

/**
 * One machine, as its own card.
 *
 * Card-per-row rather than the shared rounded container the settings and
 * workflow screens use: this list is virtualized, and a group container would
 * have to wrap items Virtuoso renders and recycles independently. A card per
 * item gives the same rounded, raised, separated reading with no fight against
 * the virtualizer — and a machine is a self-contained object, so one card each
 * is arguably the truer mapping anyway.
 */
function DaemonRow({ daemon }: { daemon: Daemon }) {
  const beat = heartbeatMs(daemon);
  const size = sizeLabel(daemon);

  return (
    <div className="px-4 pb-2">
      <Link
        to="/m/daemons/$daemonId"
        params={{ daemonId: daemon.id }}
        className={cn(MOBILE_ROW, "rounded-lg border-b-0 elevation-1")}
      >
        <MobileRowIcon icon={Server} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-medium text-foreground">
              {daemon.name || "Unnamed machine"}
            </span>
            {size && (
              <span className="shrink-0 rounded-md bg-primary/10 px-1.5 py-0.5 text-xs font-medium text-primary">
                {size}
              </span>
            )}
          </div>

          <div className="mt-1.5 flex items-center gap-2 text-xs text-muted-foreground">
            <DaemonStatusPill daemon={daemon} />
            {beat !== null && <span>{relativeTimeFromMs(beat)}</span>}
          </div>
        </div>

        <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
      </Link>
    </div>
  );
}

function DaemonListHeader({ onCreate }: { onCreate: () => void }) {
  return (
    <MobileScreenHeader
      title="Machines"
      leading={<MobileMenuButton />}
      trailing={<NewMachineButton onCreate={onCreate} />}
    />
  );
}

export function MobileDaemonList() {
  const refetchInterval = useVisibilityPolling(POLL_INTERVAL_MS);
  const { data: daemons, isLoading } = useDaemonList({ refetchInterval });
  const [createOpen, setCreateOpen] = useState(false);

  const header = <DaemonListHeader onCreate={() => setCreateOpen(true)} />;
  const createSheet = createOpen ? (
    <CreateMachineSheet onClose={() => setCreateOpen(false)} />
  ) : null;

  if (isLoading) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        {header}
        <div className="flex flex-1 items-center justify-center">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        </div>
        {createSheet}
      </div>
    );
  }

  if (!daemons || daemons.length === 0) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        {header}
        <MobileEmptyState
          icon={Server}
          title="No machines yet"
          description="Machines run your agents. Start one here, then connect GitHub to clone repos onto it."
          action={
            <button
              type="button"
              onClick={() => setCreateOpen(true)}
              className={MOBILE_PRIMARY_ACTION}
            >
              <Plus className="h-4 w-4" />
              New machine
            </button>
          }
        />
        {createSheet}
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      {header}

      <Virtuoso
        className="min-h-0 flex-1"
        data={daemons}
        // Stable identity across the 5s refetch: without it a daemon changing
        // status can recycle into a neighbour's DOM node mid-scroll.
        computeItemKey={(_, daemon) => daemon.id}
        itemContent={(_, daemon) => <DaemonRow daemon={daemon} />}
        components={{
          Header: () => <div className="h-4" />,
          Footer: () => <div className="h-6" />,
        }}
      />
      {createSheet}
    </div>
  );
}
