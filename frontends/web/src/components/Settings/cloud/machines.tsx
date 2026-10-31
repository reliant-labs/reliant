/**
 * Settings → Machines section.
 *
 * Ports admin-web's "Workspaces" (daemons) management into reliant-web as a
 * self-contained settings panel, using ONLY public control-plane RPCs
 * (controlplane.v1.DaemonService + BillingService) for lifecycle. Data access
 * lives in `@/services/controlPlane/environments`; this file is presentation +
 * local UI state only.
 *
 * Layout: a single machines view (list / create / detail). The list is
 * /settings/machines and one machine's detail is
 * /settings/machines/$machineId, so Back, refresh and deep links (the
 * onboarding DaemonConnectingGate "View logs" action, docs) all work. Daemon
 * access tokens are managed in the standalone System → Access Tokens settings
 * section.
 *
 * Each machine's detail view carries an Access section (./machineAccess): the
 * outside AI apps granted access to that machine. Those grants are reliant's,
 * not control-plane's, so they exist on a build with no control plane too —
 * which is why this section renders a registry-only mode there (list, detail,
 * Access) instead of a dead end: it is the only place such a grant can be
 * seen or revoked.
 */
import React, { useEffect, useId, useMemo, useState } from "react";
import { createPortal } from "react-dom";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import {
  Activity,
  AlertTriangle,
  ArrowLeft,
  Check,
  Copy,
  Cpu,
  ExternalLink,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Server,
  Shield,
  Laptop,
  Star,
  Trash2,
  X,
} from "lucide-react";

import { cn } from "@/lib/utils";
import Tooltip from "@/components/forge-ui/tooltip";
import { capabilities } from "@/services/controlPlane/capabilities";
import Button from "@/components/forge-ui/button";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import { SettingsPageHeader } from "../SettingsPageHeader";
import {
  Badge,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  EmptyState,
  StatusDot,
  type BadgeVariant,
  type StatusDotVariant,
} from "./ui";
import {
  DaemonSize,
  PortAccessMode,
  deleteDaemon,
  describeError,
  getDaemon,
  growDaemonDisk,
  listPortAccessRules,
  portAccessRulesQueryKey,
  removePortAccess,
  resizeDaemon,
  resumeEnvironment,
  setPortAccess,
  suspendDaemon,
  type PortAccessRule,
} from "@/services/controlPlane/environments";
import {
  DaemonStatus,
  type DaemonInfo as Daemon,
} from "@/gen/reliant/v1/daemon_registry_pb";
import { daemonListQuery, fetchDaemonList, invalidateDaemonList } from "@/hooks/daemon-registry-queries";
import {
  LIFECYCLE_PHASE_UNSPECIFIED,
  lifecyclePlan,
  restartMachine,
  type LifecycleAction,
  type RestartStage,
} from "./machineLifecycle";
import { PairMachinePanel } from "@/components/Machines/PairMachinePanel";
import { AddMachineSheet } from "@/components/Machines/AddMachineSheet";
import { clearWaking, markWaking, presentMachineStatus, useWakingMachines } from "@/lib/machineWake";
import { getComputeEligibility } from "@/services/controlPlane/billing";
import { useGoToBilling } from "@/hooks/useGoToBilling";
import { useComputeUsage, usePlans } from "@/hooks/useCloudBillingQueries";
import {
  machinePricingFootnote,
  planOwnSizeHours,
  provisionedDiskGib,
  type DaemonPricingLike,
} from "@/components/Billing/daemonUsage";
import { formatMachineSpec } from "@/components/Billing/machineSpecs";
import { MachineCost } from "@/components/Billing/MachineCost";
import { CardInset } from "@/components/forge-ui/card";
import { MachineDiskCard } from "./MachineDiskCard";
import { RenameMachineButton, RenameMachineField } from "./RenameMachine";
import { machineBuildLabel } from "@/lib/machineBuild";
import { machineCrashSummary } from "@/lib/machineMemory";
import { DiskGrowNotice, GrowDiskModal, type GrowStage } from "./GrowDiskModal";
import { diskGrowStatus, isStorageDenied } from "./diskGrow";
import { DaemonDiskState } from "@/gen/controlplane/controlplane/v1/shared_pb";
import { CLOUD_PROJECT_ROOT } from "@/lib/cloudProjectPath";
import { toast } from "@/lib/toast-manager";
import { machineDisplayName, type NameableMachine } from "@/lib/machineName";
import { MachineAccess, activeGrantCounts, appCountLabel, useConnectors } from "./machineAccess";
import { useMakeDefaultMachine } from "@/hooks/useMakeDefaultMachine";
// The overage formatter, shared with the billing purchase grid so the two
// surfaces cannot disagree about how a rate is written.



// ── Query keys ──────────────────────────────────────────────────────────────
// The machine LIST is not here: it is the one shared daemon list
// (daemon-registry-queries), which the list view and the detail view both read
// on their own cadence. A restart's 2s poll therefore refreshes every reader
// of the list while it runs — one list, one answer
// (docs/design/one-daemon-list.md).
const QK = {
  daemon: (id: string) => ["cp", "environments", "detail", id] as const,
  // Shared with the header DetectedPortsChip's one-click-public toggle so a
  // "Make public" there invalidates this panel's rules query and vice-versa.
  ports: portAccessRulesQueryKey,
  computeSub: ["cp", "environments", "computeSubscription"] as const,
  computeEligibility: ["cp", "environments", "computeEligibility"] as const,
};

// ── Status presentation ─────────────────────────────────────────────────────
type WsStatus = "active" | "suspended" | "failed" | "pending" | "disconnected" | "unknown";

const statusFromEnum: Record<number, WsStatus> = {
  [DaemonStatus.ACTIVE]: "active",
  [DaemonStatus.SUSPENDED]: "suspended",
  [DaemonStatus.FAILED]: "failed",
  [DaemonStatus.PENDING]: "pending",
  [DaemonStatus.DISCONNECTED]: "disconnected",
};

const statusBadge: Record<WsStatus, { label: string; variant: BadgeVariant }> = {
  active: { label: "Active", variant: "success" },
  suspended: { label: "Suspended", variant: "warning" },
  failed: { label: "Failed", variant: "error" },
  pending: { label: "Pending", variant: "neutral" },
  disconnected: { label: "Disconnected", variant: "error" },
  unknown: { label: "Status unknown", variant: "neutral" },
};

const statusDotVariant: Record<WsStatus, StatusDotVariant> = {
  active: "active",
  suspended: "paused",
  failed: "error",
  pending: "pending",
  disconnected: "error",
  unknown: "pending",
};

function daemonStatus(d: Daemon): WsStatus {
  return statusFromEnum[d.status] ?? "unknown";
}

/**
 * The reason a machine is in a bad state, when there is one worth showing.
 *
 * "Failed" on its own is not an answer to the only question the user has,
 * which is what to do about it. A machine whose storage request exceeded the
 * plan's limit needs a smaller size or a bigger plan; one that lost its
 * connection needs nothing. Both rendered as a bare red dot, so the user
 * could not tell them apart and support had to read a cluster log to answer.
 *
 * The backend already writes a user-safe sentence here (the control-plane
 * translates the underlying error and never passes raw Kubernetes text
 * through), so this only decides WHEN to show it: on the states where the
 * machine is not working and the message therefore explains something.
 * Showing it beside a healthy machine would be stale-message noise.
 *
 * SUSPENDED counts, and it is the least obvious of the three. A machine the
 * USER stopped needs no explanation and carries none. But the control plane
 * also stops machines on its own — the reconciler parks one whose workspace
 * has gone missing and whose owner has no compute funding — and that is a
 * state change nobody asked for. Without the reason the owner sees a machine
 * that stopped itself and a Start button that will fail for the same
 * unstated cause. The message is only rendered when one exists, so a
 * user-suspended machine is unaffected.
 */
function daemonFailureReason(d: Daemon): string | null {
  const status = daemonStatus(d);
  if (status !== "failed" && status !== "disconnected" && status !== "suspended") {
    return null;
  }
  const message = d.lastStatusMessage?.trim();
  return message ? message : null;
}

// The registry carries daemon_type as the string the daemon registered with.
// "self_hosted" is what tools_daemon.go records; "external" is control-plane's
// word for the same thing, accepted so a row back-filled from its vocabulary
// is still treated as unmanaged.
const EXTERNAL_DAEMON_TYPES = ["self_hosted", "external"];

function isExternalDaemon(d: Pick<Daemon, "daemonType">): boolean {
  return EXTERNAL_DAEMON_TYPES.includes(d.daemonType);
}

export const CONNECTED_MACHINE_REMOVE_REASON =
  "Connected machines can't be removed. Disconnect it first.";

// A connected self-hosted machine re-registers itself on its next heartbeat, so
// "forgetting" it just makes the row vanish and reappear. Cloud machines are
// deleted as before; a disconnected self-hosted row can still be forgotten.
export function canRemoveDaemon(
  d: Pick<Daemon, "daemonType" | "status">,
): { allowed: boolean; reason?: string } {
  if (isExternalDaemon(d) && d.status === DaemonStatus.ACTIVE) {
    return { allowed: false, reason: CONNECTED_MACHINE_REMOVE_REASON };
  }
  return { allowed: true };
}

// What a machine is called: the name its owner gave it, never a managed
// machine's pod hostname. One rule for every surface — see lib/machineName.
export function daemonDisplayName(d: NameableMachine): string {
  return machineDisplayName(d);
}

// ── Size tiers (plan-gated) ─────────────────────────────────────────────────
//
// No prices here. These four once carried a per-minute price — $0.02 / $0.04
// / $0.08 / $0.16 — a client-side table of what machines cost, sitting on the
// button that creates one. Prices come from the server's per-size list
// (ListPlans' daemon_pricing) at render time.
//
// No spec copy either: `formatMachineSpec` is the one statement of what a
// size reserves, shared with the billing page and onboarding. This file used
// to keep its own ("1 CPU · 2GB RAM" for a Small that reserves 0.5 CPU), so
// the same machine was described two ways depending on the screen.
const SIZE_TIERS = [
  { value: DaemonSize.DAEMON_SIZE_SMALL, name: "small", label: "Small", specs: formatMachineSpec("small") },
  { value: DaemonSize.DAEMON_SIZE_MEDIUM, name: "medium", label: "Medium", specs: formatMachineSpec("medium") },
  { value: DaemonSize.DAEMON_SIZE_LARGE, name: "large", label: "Large", specs: formatMachineSpec("large") },
  { value: DaemonSize.DAEMON_SIZE_XL, name: "xl", label: "XL", specs: formatMachineSpec("xl") },
  // The catalog sells 2xl (see Billing/machineSpecs.ts). Without a row here a
  // plan that allows it could neither create one nor resize to one, and an
  // existing 2xl machine read as "Custom". Whether it is OFFERED is still the
  // server's call, via allowed_daemon_sizes.
  { value: DaemonSize.DAEMON_SIZE_2XL, name: "2xl", label: "2XL", specs: formatMachineSpec("2xl") },
] as const;

// ── Copy for the un-funded state ────────────────────────────────────────────
//
// Both strings name the COUPON first, then the plan, because the server's own
// denial does — checkDaemonSizeAllowed returns "redeem a coupon code or
// subscribe to a compute plan to start a machine", and that ordering is
// deliberate: since the signup auto-grant was removed every brand-new account
// lands here, and a code is the path most of them were handed. A prompt that
// says only "subscribe" hides the option the user is holding.
//
// This replaced a `<Badge variant="neutral">` — a bordered pill that looked
// like a button, did nothing on click, and named no way forward at all.
const NO_FUNDING_DESCRIPTION =
  "Redeem a coupon code or subscribe to a compute plan to start a machine. Machines run on the compute sizes your plan allows.";


/**
 * Which sizes may this caller run, per the server?
 *
 * The answer arrives on `GetCurrentUserComputeEligibility.allowed_daemon_sizes`
 * as wire strings ("small", "medium", …); this maps them onto the tiers this
 * page can render. Sizes the client has no tier for are dropped rather than
 * guessed at.
 *
 * It used to be derived here from the compute SUBSCRIPTION, which is the bug
 * this replaces: a coupon grants machine minutes and no subscription, so that
 * derivation decided a fully-entitled user could run nothing. The server
 * resolves the set — from the plan, or ["small"] when there is none — and the
 * client no longer holds an opinion about it.
 *
 * It is a membership test, not a ladder: an allowed set of `[small, large]`
 * without `medium` offers exactly that.
 */
function sizeTiersFromWire(allowedDaemonSizes: string[]): DaemonSize[] {
  return SIZE_TIERS.filter((t) => allowedDaemonSizes.includes(t.name)).map(
    (t) => t.value,
  );
}

const accessModeLabel: Record<number, string> = {
  [PortAccessMode.PUBLIC]: "Public",
  [PortAccessMode.AUTHENTICATED]: "Authenticated",
  [PortAccessMode.TOKEN]: "Token",
  [PortAccessMode.UNSPECIFIED]: "Unspecified",
};

// Existing-machine costs read provisioning detail, not a tier's standard disk.

// ── Date helpers ────────────────────────────────────────────────────────────
function fmtTimestamp(ts?: Timestamp): string {
  if (!ts) return "—";
  try {
    return timestampDate(ts).toLocaleString();
  } catch {
    return "—";
  }
}

// ── Inline Modal ────────────────────────────────────────────────────────────
function Modal({
  open,
  onClose,
  title,
  children,
  maxWidth = "max-w-lg",
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: React.ReactNode;
  maxWidth?: string;
}) {
  if (!open) return null;
  return createPortal(
    <div className="fixed inset-0 z-[1000] flex items-center justify-center p-4">
      <div className="absolute inset-0 bg-black/60" onClick={onClose} aria-hidden />
      <div
        role="dialog"
        aria-modal="true"
        className={cn(
          "relative z-10 w-full overflow-hidden rounded-lg border border-border bg-card shadow-xl",
          maxWidth,
        )}
      >
        <div className="flex items-center justify-between border-b border-border px-5 py-4">
          <h2 className="text-sm font-semibold text-foreground">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
            aria-label="Close"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="max-h-[70vh] overflow-y-auto px-5 py-4">{children}</div>
      </div>
    </div>,
    document.body,
  );
}


const inputCls =
  "w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring";

function ErrorNote({ message }: { message?: string }) {
  if (!message) return null;
  return (
    <div className="mb-4 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive-ink">
      {message}
    </div>
  );
}

// ── Self-hosted setup instructions ──────────────────────────────────────────
/**
 * "Run Reliant on your own machine" — install, run, approve.
 *
 * The body is `PairMachinePanel`, the SAME component the Add machine sheet
 * and onboarding render, so the download links, the `reliant daemon start`
 * command (which varies by deployment — see lib/cli-commands) and the pairing
 * approval have one source of truth. mode="reference" drops the waiting
 * affordances; pairing a machine from here still works.
 */
function SelfHostedSetupCard() {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="inline-flex items-center gap-2">
          <Laptop className="h-4 w-4 text-muted-foreground" />
          Run Reliant on your own machine
        </CardTitle>
        <p className="text-sm text-muted-foreground">
          Install the desktop app or CLI on a laptop or server and start it. It
          prints a code; approve it here and the machine shows up in the list.
        </p>
      </CardHeader>
      <CardContent>
        <PairMachinePanel mode="reference" />
      </CardContent>
    </Card>
  );
}

// ── Root section ────────────────────────────────────────────────────────────
export function MachinesSection() {
  // The open machine is the URL (/settings/machines/$machineId), so Back
  // returns to the list, a refresh keeps it open, and it can be linked to.
  // `?daemon=<id>` is the older deep link; the route redirects it onto the
  // path, and it is still read here so a stale link never lands on the list.
  const params = useParams({ strict: false }) as { machineId?: string };
  const search = useSearch({ strict: false }) as { daemon?: string; from?: string };
  const navigate = useNavigate();
  const selectedId = params.machineId ?? search.daemon ?? null;
  const openMachine = (machineId: string) =>
    navigate({ to: "/settings/machines/$machineId", params: { machineId } });
  const backToList = () => navigate({ to: "/settings/$section", params: { section: "machines" } });
  // Without a control plane there are no managed machines to create or
  // drive, but registered machines — and the app access granted to them —
  // still exist, so the list and detail render in a registry-only mode rather
  // than a dead end. The self-hosted setup instructions matter MORE here.
  const cloud = capabilities.cloudDaemons;

  return (
    <div>
      {selectedId ? (
        <EnvironmentDetail daemonId={selectedId} cloud={cloud} onBack={backToList} />
      ) : (
        <div className="space-y-6">
          <div>
            <SettingsPageHeader
              title="Machines"
              description="Managed and self-hosted machines that run your projects."
            />
            {!cloud && (
              <p className="mb-4 text-sm text-muted-foreground">
                Cloud machines are managed by the Reliant control plane, which isn&apos;t configured for this build.
                Self-hosted machines you connect still appear here.
              </p>
            )}
            {search.from === "connectors" && (
              <p
                role="status"
                className="mb-4 rounded-md border border-border/60 bg-background px-3 py-2 text-sm text-foreground"
              >
                <span className="font-medium">Connectors moved here.</span>{" "}
                <span className="text-muted-foreground">
                  Outside AI apps now get access to one machine at a time. Open a machine and use its Access
                  section to grant, review or revoke an app.
                </span>
              </p>
            )}
            <EnvironmentsList cloud={cloud} onOpenDetail={openMachine} />
          </div>
          <SelfHostedSetupCard />
        </div>
      )}
    </div>
  );
}

// ── Machines list + create ──────────────────────────────────────────────────
function EnvironmentsList({ cloud, onOpenDetail }: { cloud: boolean; onOpenDetail: (id: string) => void }) {
  const qc = useQueryClient();
  const [statusFilter, setStatusFilter] = useState<number>(DaemonStatus.UNSPECIFIED);
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<Daemon | null>(null);
  const [actionError, setActionError] = useState("");
  // Which machines outside AI apps can reach. Read once for the whole list
  // (ListConnectors is per user, not per machine) and counted per row.
  const appCounts = activeGrantCounts(useConnectors().data);
  // The LIST comes from reliant's registry — the one daemon list
  // (docs/design/one-daemon-list.md). The per-machine DETAIL view below still
  // calls control-plane's GetDaemon, which is kept deliberately: port-access
  // rules, the workspace base domain and the provisioning spec are
  // control-plane concerns with no reliant equivalent.
  const daemonsQ = useQuery(daemonListQuery({
    staleTime: 10_000,
    refetchInterval: 15_000,
  }));

  // Add machine is always offered: the sheet itself shows which sizes the
  // plan runs and takes payment in place for the rest. Eligibility here only
  // picks the empty state's words (the server's own prediction of
  // internal/svcdaemon.checkDaemonSizeAllowed — never a reimplementation).
  const eligibilityQ = useQuery({
    queryKey: QK.computeEligibility,
    queryFn: () => getComputeEligibility(),
    staleTime: 30_000,
    enabled: cloud,
  });

  const invalidate = () => invalidateDaemonList(qc);

  const suspendMut = useMutation({
    mutationFn: (id: string) => suspendDaemon(id),
    onSuccess: () => { setActionError(""); invalidate(); },
    onError: (e) => setActionError(describeError(e, "Failed to suspend machine")),
  });
  const resumeMut = useMutation({
    mutationFn: (id: string) => resumeEnvironment(id),
    onSuccess: (_data, id) => { markWaking(id); setActionError(""); invalidate(); },
    onError: (e) => setActionError(describeError(e, "Failed to resume machine")),
  });
  const deleteMut = useMutation({
    mutationFn: (id: string) => deleteDaemon(id),
    onSuccess: () => { setDeleteTarget(null); setActionError(""); invalidate(); },
    onError: (e) => setActionError(describeError(e, "Failed to delete machine")),
  });

  // The status filter applies globally across both groups (rather than one
  // dropdown per group) — a user picking "Suspended" wants every suspended
  // machine, cloud or self-hosted, and a second dropdown for a section that's
  // often empty would be clutter without a real use case.
  const daemons = (daemonsQ.data ?? []).filter(
    (d) => statusFilter === DaemonStatus.UNSPECIFIED || d.status === statusFilter,
  );
  const managedDaemons = daemons.filter((d) => !isExternalDaemon(d));
  const selfHostedDaemons = daemons.filter((d) => isExternalDaemon(d));

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <select
          value={String(statusFilter)}
          onChange={(e) => setStatusFilter(Number(e.target.value))}
          className={cn(inputCls, "w-44")}
        >
          <option value={String(DaemonStatus.UNSPECIFIED)}>All statuses</option>
          <option value={String(DaemonStatus.PENDING)}>Pending</option>
          <option value={String(DaemonStatus.ACTIVE)}>Active</option>
          <option value={String(DaemonStatus.SUSPENDED)}>Suspended</option>
          <option value={String(DaemonStatus.FAILED)}>Failed</option>
          <option value={String(DaemonStatus.DISCONNECTED)}>Disconnected</option>
        </select>
        {cloud && (
          <Button onClick={() => setCreateOpen(true)} size="xl" leftIcon={<Plus />}>
            Add machine
          </Button>
        )}
      </div>

      {actionError && <ErrorNote message={actionError} />}

      {daemonsQ.isLoading ? (
        <Card>
          <CardContent className="text-sm text-muted-foreground">Loading machines…</CardContent>
        </Card>
      ) : daemonsQ.error ? (
        <Card>
          <CardContent>
            <p className="text-sm font-medium text-destructive-ink">Failed to load machines</p>
            <p className="mt-1 text-sm text-muted-foreground">{describeError(daemonsQ.error)}</p>
            <Button variant="outline" size="md" className="mt-3" onClick={() => daemonsQ.refetch()} leftIcon={<RefreshCw />}>
              Retry
            </Button>
          </CardContent>
        </Card>
      ) : daemons.length === 0 && !cloud ? (
        <EmptyState
          icon={Server}
          title="No machines"
          description="Connect a self-hosted machine with the steps below."
        />
      ) : daemons.length === 0 ? (
        <EmptyState
          icon={Server}
          title="No machines yet"
          description={
            eligibilityQ.data?.eligible
              ? "Add a cloud machine, or connect your own computer."
              : NO_FUNDING_DESCRIPTION
          }
          action={
            <Button onClick={() => setCreateOpen(true)} size="xl" leftIcon={<Plus />}>
              Add machine
            </Button>
          }
        />
      ) : (
        <div className="space-y-6">
          {managedDaemons.length > 0 && (
            <div className="space-y-2">
              <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                Cloud machines
              </h3>
              <ManagedMachinesTable
                daemons={managedDaemons}
                appCounts={appCounts}
                cloud={cloud}
                onOpenDetail={onOpenDetail}
                onDelete={setDeleteTarget}
                onSuspend={(id) => suspendMut.mutate(id)}
                onResume={(id) => resumeMut.mutate(id)}
                busy={suspendMut.isPending || resumeMut.isPending}
              />
            </div>
          )}
          {selfHostedDaemons.length > 0 && (
            <div className="space-y-2">
              <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                Self-hosted machines
              </h3>
              <SelfHostedMachinesTable
                daemons={selfHostedDaemons}
                appCounts={appCounts}
                onOpenDetail={onOpenDetail}
                // Forgetting a machine is a control-plane write.
                onRemove={cloud ? setDeleteTarget : undefined}
              />
            </div>
          )}
        </div>
      )}

      <AddMachineSheet
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onReady={(machine) => {
          invalidate();
          onOpenDetail(machine.daemonId);
        }}
      />

      <RemoveMachineModal
        target={deleteTarget}
        isPending={deleteMut.isPending}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => deleteTarget && deleteMut.mutate(deleteTarget.daemonId)}
      />
    </div>
  );
}

/**
 * "N apps" beside a machine's name when outside AI apps can reach it — so a
 * user scanning the list can tell which machines have been opened up without
 * opening each one. Nothing renders for a machine with no active grants.
 */
function AppAccessIndicator({ count }: { count: number }) {
  if (count <= 0) return null;
  return (
    <Badge
      label={appCountLabel(count)}
      variant="info"
      size="sm"
      className="ml-2 align-middle"
    />
  );
}

function ManagedMachinesTable({
  daemons,
  appCounts,
  cloud,
  onOpenDetail,
  onDelete,
  onSuspend,
  onResume,
  busy,
}: {
  daemons: Daemon[];
  appCounts: Map<string, number>;
  /** False without a control plane: lifecycle and delete are its writes. */
  cloud: boolean;
  onOpenDetail: (id: string) => void;
  onDelete: (d: Daemon) => void;
  onSuspend: (id: string) => void;
  onResume: (id: string) => void;
  busy: boolean;
  pricing?: DaemonPricingLike;
}) {
  const waking = useWakingMachines();
  const [renaming, setRenaming] = useState<string | null>(null);
  // Forget wakes that are over (the machine came up, failed, or never left
  // sleep), so a later Pending is not mislabelled as a wake.
  useEffect(() => {
    for (const d of daemons) {
      const started = waking.get(d.daemonId);
      if (started === undefined) continue;
      if (presentMachineStatus(daemonStatus(d), d.lastStatusMessage, started).wakeFinished) clearWaking(d.daemonId);
    }
  }, [daemons, waking]);
  const columns: DataTableColumn<MachineRow>[] = [
    {
      key: "name",
      header: "Name",
      render: (_, { d }) =>
        renaming === d.daemonId ? (
          <RenameMachineField daemonId={d.daemonId} currentName={d.name} onDone={() => setRenaming(null)} />
        ) : (
          <MachineName daemon={d} appCount={appCounts.get(d.daemonId) ?? 0} />
        ),
    },
    {
      key: "status",
      header: "Status",
      className: "whitespace-normal py-2",
      render: (_, { d }) => {
        const status = daemonStatus(d);
        const presentation = presentMachineStatus(status, d.lastStatusMessage, waking.get(d.daemonId));
        const failureReason = presentation.label ? null : daemonFailureReason(d);
        return (
          <>
            <StatusDot
              variant={presentation.label ? "pending" : statusDotVariant[status]}
              label={presentation.label ?? statusBadge[status].label}
            />
            {presentation.progress && <p className="mt-1 max-w-xs text-xs text-muted-foreground">{presentation.progress}</p>}
            {failureReason && (
              // Destructive red for a machine that BROKE; muted for one that
              // is merely stopped. A stopped machine is a normal state whose
              // reason is informational, and colouring it as an error would
              // make the whole list look on fire whenever the reconciler
              // parks something for lack of funding.
              <p className={cn("mt-1 max-w-xs text-xs", status === "suspended" ? "text-muted-foreground" : "text-destructive-ink")}>
                {failureReason}
              </p>
            )}
          </>
        );
      },
    },
    {
      // Specs come from the size tier rather than from per-machine resource
      // requests. Those requests are part of the provisioning SPEC, which
      // lives only in control-plane and is deliberately absent from the one
      // list; the size determines them, and SIZE_TIERS already states the
      // mapping this page renders everywhere else. A machine with no reported
      // size (every self-hosted one) shows "—".
      key: "resources",
      header: "Resources",
      className: "text-muted-foreground",
      render: (_, { d }) => SIZE_TIERS.find((t) => t.name === d.size)?.specs || "—",
    },
    { key: "created", header: "Created", className: "text-muted-foreground", render: (_, { d }) => fmtTimestamp(d.createdAt) },
    {
      key: "actions",
      header: "Actions",
      srHeader: true,
      interactive: true,
      className: "text-right",
      render: (_, { d }) => {
        // Default is this server's own write, so it is offered without a
        // control plane too; lifecycle and delete are the control plane's.
        if (!cloud) return <MakeDefaultButton daemon={d} />;
        const isSuspended = d.status === DaemonStatus.SUSPENDED;
        return (
          <div className="inline-flex items-center gap-1">
            <MakeDefaultButton daemon={d} />
            <RenameMachineButton label={daemonDisplayName(d)} onClick={() => setRenaming(d.daemonId)} />
            <Button
              variant="ghost"
              size="md"
              disabled={busy}
              onClick={() => (isSuspended ? onResume(d.daemonId) : onSuspend(d.daemonId))}
            >
              {isSuspended ? <Play className="h-4 w-4" /> : <Pause className="h-4 w-4" />}
              {isSuspended ? "Resume" : "Suspend"}
            </Button>
            {/* A suspended machine still holds its disk, and the disk is
                billed (design §5.1). Shown beside Delete because Delete is the
                only thing that stops it. */}
            <MachineCost daemonId={d.daemonId} size={d.size} suspended={isSuspended} />
            <Button
              variant="ghost"
              size="md"
              onClick={() => onDelete(d)}
              aria-label={`Delete ${daemonDisplayName(d)}`}
              title="Delete machine"
              icon={<Trash2 className="text-destructive-ink" aria-hidden="true" />}
            />
          </div>
        );
      },
    },
  ];
  return (
    <div className="forge-ui">
      <DataTable<MachineRow>
        ariaLabel="Cloud machines"
        columns={columns}
        data={daemons.map((d) => ({ id: d.daemonId, d }))}
        getRowKey={(r) => r.id}
        // The row being renamed holds a form, so it is not a link meanwhile.
        getRowAction={(r) => (r.id === renaming ? undefined : () => onOpenDetail(r.id))}
        getRowLabel={(r) => daemonDisplayName(r.d)}
      />
    </div>
  );
}

type MachineRow = { id: string; d: Daemon };

/**
 * "Make default": the machine new chats, worktrees and anything else that
 * names no machine run on, in whatever state it is in (a sleeping default is
 * woken, never passed over). Without one, they go to a connected machine.
 * Hidden on the machine that already is the default — its Default badge says so.
 */
function MakeDefaultButton({ daemon }: { daemon: Daemon }) {
  const { makeDefault, isPending } = useMakeDefaultMachine();
  if (daemon.isDefault) return null;
  return (
    <Button
      variant="ghost"
      size="md"
      disabled={isPending}
      onClick={() => makeDefault(daemon.daemonId)}
      leftIcon={<Star className="h-4 w-4" aria-hidden="true" />}
      aria-label={`Make ${daemonDisplayName(daemon)} your default machine`}
    >
      Make default
    </Button>
  );
}

/**
 * The machine's name, its app-access badge and its reported build: the row's
 * button content (the whole row opens the machine), so spans throughout.
 */
function MachineName({ daemon, appCount }: { daemon: Daemon; appCount: number }) {
  const build = machineBuildLabel(daemon);
  return (
    <span className="block">
      <span className="font-medium text-foreground">{daemonDisplayName(daemon)}</span>
      {daemon.isDefault && <Badge label="Default" variant="neutral" size="sm" className="ml-2 align-middle" />}
      <AppAccessIndicator count={appCount} />
      {build && <span className="mt-0.5 block text-xs text-muted-foreground">{build}</span>}
    </span>
  );
}

function SelfHostedMachinesTable({
  daemons,
  appCounts,
  onOpenDetail,
  onRemove,
}: {
  daemons: Daemon[];
  appCounts: Map<string, number>;
  onOpenDetail: (id: string) => void;
  /** Absent without a control plane, which owns the machine record. */
  onRemove?: (d: Daemon) => void;
}) {
  const [renaming, setRenaming] = useState<string | null>(null);
  const columns: DataTableColumn<MachineRow>[] = [
    {
      key: "name",
      header: "Name",
      render: (_, { d }) =>
        renaming === d.daemonId ? (
          <RenameMachineField daemonId={d.daemonId} currentName={d.name} onDone={() => setRenaming(null)} />
        ) : (
          <MachineName daemon={d} appCount={appCounts.get(d.daemonId) ?? 0} />
        ),
    },
    {
      key: "status",
      header: "Status",
      render: (_, { d }) => {
        const status = daemonStatus(d);
        return <StatusDot variant={statusDotVariant[status]} label={statusBadge[status].label} />;
      },
    },
    { key: "platform", header: "Platform", className: "text-muted-foreground", render: (_, { d }) => d.platform || "—" },
    {
      key: "lastSeen",
      header: "Last seen",
      className: "text-muted-foreground",
      render: (_, { d }) => (d.status === DaemonStatus.ACTIVE ? "Connected now" : fmtTimestamp(d.connectedAt)),
    },
    {
      key: "actions",
      header: "Actions",
      srHeader: true,
      interactive: true,
      className: "text-right",
      render: (_, { d }) => {
        if (!onRemove) return <MakeDefaultButton daemon={d} />;
        const removal = canRemoveDaemon(d);
        const button = (
          <Button
            variant="ghost"
            size="md"
            disabled={!removal.allowed}
            onClick={() => onRemove(d)}
            leftIcon={<Trash2 className="text-destructive-ink" />}
          >
            Remove
          </Button>
        );
        return (
          <div className="inline-flex items-center gap-1">
            <MakeDefaultButton daemon={d} />
            <RenameMachineButton label={daemonDisplayName(d)} onClick={() => setRenaming(d.daemonId)} />
            {removal.allowed ? (
              button
            ) : (
              <Tooltip content={removal.reason ?? ""} side="left">
                {button}
              </Tooltip>
            )}
          </div>
        );
      },
    },
  ];
  return (
    <div className="forge-ui">
      <DataTable<MachineRow>
        ariaLabel="Self-hosted machines"
        columns={columns}
        data={daemons.map((d) => ({ id: d.daemonId, d }))}
        getRowKey={(r) => r.id}
        // The row being renamed holds a form, so it is not a link meanwhile.
        getRowAction={(r) => (r.id === renaming ? undefined : () => onOpenDetail(r.id))}
        getRowLabel={(r) => daemonDisplayName(r.d)}
      />
    </div>
  );
}

// Removing a self-hosted machine's row and deleting a cloud machine are
// different actions in the user's mental model — one tears down real
// infrastructure, the other just forgets a laptop that will reappear the
// next time it connects (UpsertExternalDaemonConnected un-deletes on
// reconnect). Same modal, type-conditional copy.
function RemoveMachineModal({
  target,
  isPending,
  onClose,
  onConfirm,
}: {
  target: Daemon | null;
  isPending: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  const external = target ? isExternalDaemon(target) : false;
  const title = external ? "Forget This Machine" : "Delete Machine";
  const confirmLabel = external ? "Forget" : "Delete";
  const confirmingLabel = external ? "Forgetting…" : "Deleting…";

  return (
    <Modal open={target !== null} onClose={onClose} title={title}>
      <p className="text-sm text-muted-foreground">
        {external ? (
          <>
            Remove <span className="font-semibold text-foreground">{target && daemonDisplayName(target)}</span> from
            this list? This only removes the connection record here — it does not affect the actual machine, and it
            will reappear if that machine reconnects.
          </>
        ) : (
          <>
            Are you sure you want to delete{" "}
            <span className="font-semibold text-foreground">{target ? daemonDisplayName(target) : ""}</span>? This action cannot be undone.
          </>
        )}
      </p>
      <div className="mt-6 flex justify-end gap-3">
        <Button variant="outline" onClick={onClose} size="xl">Cancel</Button>
        <Button variant="danger" loading={isPending} onClick={onConfirm} size="xl">
          {isPending ? confirmingLabel : confirmLabel}
        </Button>
      </div>
    </Modal>
  );
}

// ── Machine detail ──────────────────────────────────────────────────────────

/**
 * The lifecycle buttons for a machine, per its status.
 *
 * Which buttons exist and whether they are clickable is `lifecyclePlan`'s
 * decision, not this component's — the policy is a table, it is tested
 * directly, and keeping it out of here is what stops "can a failed machine
 * resume" from being re-derived inline. This renders the plan.
 *
 * Self-hosted machines get nothing: `lifecyclePlan` returns an empty offer
 * for them, and the detail view explains why in prose instead.
 */
const STAGE_LABEL: Record<RestartStage, string> = {
  stopping: "Stopping…",
  resizing: "Resizing…",
  starting: "Starting…",
};

function MachineLifecycleActions({
  daemon,
  busy,
  restartStage,
  cycling,
  onSuspend,
  onResume,
  onRestart,
  onResize,
}: {
  daemon: Daemon;
  busy: boolean;
  restartStage: RestartStage | null;
  /** Which action is driving `restartStage` — that button narrates it. */
  cycling: LifecycleAction | null;
  onSuspend: () => void;
  onResume: () => void;
  onRestart: () => void;
  onResize: () => void;
}) {
  const plan = lifecyclePlan(daemon, restartStage);
  if (plan.offer.length === 0) return null;

  // The reason rides on `title` as well as disabling the button, so a user
  // who wonders why Resume is dead can find out by hovering rather than
  // guessing. A disabled control with no stated cause is the thing this
  // avoids.
  const disabled = busy || plan.disabledReason !== null;
  const reason = plan.disabledReason ?? undefined;

  // While a restart (or a resize of a running machine, which is a restart
  // with a step in the middle) runs, the button that started it narrates the
  // stage — the whole operation takes a pod teardown plus a cold start, which
  // is long enough that a silent spinner reads as a hang.
  const narrated = (action: LifecycleAction, idle: string) =>
    restartStage && cycling === action ? STAGE_LABEL[restartStage] : idle;

  const label: Record<LifecycleAction, string> = {
    suspend: "Suspend",
    resume: "Resume",
    restart: narrated("restart", "Restart"),
    resize: narrated("resize", "Resize"),
  };
  const icon: Record<LifecycleAction, React.ReactNode> = {
    suspend: <Pause className="h-4 w-4" />,
    resume: <Play className="h-4 w-4" />,
    restart: <RefreshCw className={cn("h-4 w-4", restartStage && cycling === "restart" && "animate-spin")} />,
    resize: <Cpu className="h-4 w-4" />,
  };
  const onClick: Record<LifecycleAction, () => void> = {
    suspend: onSuspend,
    resume: onResume,
    restart: onRestart,
    resize: onResize,
  };

  return (
    <>
      {plan.offer.map((action) => (
        <Button
          key={action}
          variant="outline"
          disabled={disabled}
          title={reason}
          onClick={onClick[action]}
         size="xl">
          {icon[action]} {label[action]}
        </Button>
      ))}
    </>
  );
}

/**
 * Restart confirmation.
 *
 * A restart is not destructive but it IS disruptive, and the disruption is
 * invisible from this page: anything running on the machine — an agent
 * mid-task, an open terminal, a dev server — goes away when the pod does.
 * Naming that before the first RPC is the difference between a restart and a
 * surprise.
 */
function RestartMachineModal({
  target,
  isPending,
  stage,
  onClose,
  onConfirm,
}: {
  target: Daemon | null;
  isPending: boolean;
  stage: RestartStage | null;
  onClose: () => void;
  onConfirm: () => void;
}) {
  return (
    <Modal open={target !== null} onClose={onClose} title="Restart Machine">
      <p className="text-sm text-muted-foreground">
        Restart <span className="font-semibold text-foreground">{target && daemonDisplayName(target)}</span>? The
        machine stops and starts again, so any open sessions on it — running agents,
        terminals and dev servers — will disconnect. Files on its disk are kept.
      </p>
      <p className="mt-3 text-sm text-muted-foreground">
        A restart is also how a machine picks up a new workspace image.
      </p>
      {/* Progress, in the modal that started it. The sequence outlives a
          single RPC, so closing this on click would leave the user watching
          an unchanged page with no indication anything was happening. */}
      {stage && (
        <p className="mt-4 text-sm font-medium text-foreground" data-testid="restart-progress">
          {stage === "stopping" ? "Stopping the machine…" : "Starting the machine…"}
        </p>
      )}
      <div className="mt-6 flex justify-end gap-3">
        <Button variant="outline" disabled={isPending} onClick={onClose} size="xl">Cancel</Button>
        <Button loading={isPending} onClick={onConfirm} size="xl">
          {isPending ? "Restarting…" : "Restart machine"}
        </Button>
      </div>
    </Modal>
  );
}

/**
 * Resize: pick a new size, then confirm what that will do.
 *
 * A size changes only while the machine is stopped (control-plane
 * docs/design/daemon-resize.md), so what confirming DOES depends on the
 * machine's state, and the copy says which. A suspended machine is resized
 * now and starts on the new size next time. A running one is restarted onto
 * it, which disconnects whatever runs there — the same warning Restart gives,
 * for the same reason.
 *
 * Sizes come from the server answer the create picker uses
 * (GetCurrentUserComputeEligibility.allowed_daemon_sizes). A size the plan
 * does not include is shown disabled rather than hidden: someone resizing up
 * is exactly the person who needs to learn the size exists and what unlocks
 * it.
 */
function ResizeMachineModal({
  target,
  currentSize,
  running,
  allowedSizes,
  sizesLoading,
  pricing,
  includedHours,
  isPending,
  stage,
  error,
  onClose,
  onConfirm,
  onSeePlans,
}: {
  target: Daemon | null;
  currentSize: DaemonSize | undefined;
  running: boolean;
  allowedSizes: DaemonSize[];
  sizesLoading: boolean;
  pricing?: DaemonPricingLike;
  includedHours?: number;
  isPending: boolean;
  stage: RestartStage | null;
  error: string;
  onClose: () => void;
  onConfirm: (size: DaemonSize) => void;
  onSeePlans: () => void;
}) {
  const groupId = useId();
  const [selected, setSelected] = useState<DaemonSize | null>(null);
  // A fresh choice every time the modal opens: carrying a previous pick into
  // a later resize of a machine that has since changed would pre-select a
  // size nobody chose this time.
  useEffect(() => {
    if (!target) setSelected(null);
  }, [target]);

  const currentIndex = SIZE_TIERS.findIndex((t) => t.value === currentSize);
  const selectedIndex = SIZE_TIERS.findIndex((t) => t.value === selected);
  const shrinking = selected !== null && currentIndex >= 0 && selectedIndex < currentIndex;
  const ownHours = planOwnSizeHours(
    pricing,
    SIZE_TIERS.filter((t) => allowedSizes.includes(t.value)).map((t) => t.name),
    includedHours,
  );
  const someDisallowed = !sizesLoading && SIZE_TIERS.some((t) => !allowedSizes.includes(t.value));
  const name = target ? daemonDisplayName(target) : "";

  return (
    <Modal open={target !== null} onClose={isPending ? () => {} : onClose} title="Resize Machine" maxWidth="max-w-xl">
      <p className="text-sm text-muted-foreground" data-testid="resize-effect">
        {running ? (
          <>
            Resizing restarts <span className="font-semibold text-foreground">{name}</span>, so any open
            sessions on it — running agents, terminals and dev servers — will disconnect. Files on
            its disk are kept.
          </>
        ) : (
          <>
            <span className="font-semibold text-foreground">{name}</span> is suspended. The new size
            takes effect the next time it starts.
          </>
        )}
      </p>

      <div role="radiogroup" aria-label="Size" className="mt-4 grid grid-cols-2 gap-2">
        {SIZE_TIERS.filter((t) => allowedSizes.includes(t.value) || t.value === currentSize).map((t) => {
          const isCurrent = t.value === currentSize;
          const allowed = allowedSizes.includes(t.value);
          const choosable = !isCurrent && allowed && !sizesLoading && !isPending;
          const isSelected = selected === t.value;
          const labelId = `${groupId}-size-${t.name}`;
          const detailId = `${labelId}-detail`;
          return (
            <button
              key={t.name}
              type="button"
              role="radio"
              aria-checked={isSelected}
              aria-labelledby={labelId}
              aria-describedby={detailId}
              disabled={!choosable}
              data-testid={`resize-size-${t.name}`}
              onClick={() => setSelected(t.value)}
              className={cn(
                "rounded-lg border-2 p-3 text-left transition-colors disabled:cursor-not-allowed",
                isSelected
                  ? "border-primary bg-primary/5"
                  : "border-border bg-card enabled:hover:border-muted-foreground/40",
                !choosable && !isCurrent && "opacity-50",
              )}
            >
              <div className="flex items-baseline justify-between gap-2">
                <span id={labelId} className="text-sm font-semibold text-foreground">{t.label}</span>
                {isCurrent && <Badge label="Current" variant="neutral" />}
              </div>
              <div id={detailId} className="mt-1 whitespace-nowrap text-xs text-muted-foreground">{t.specs}</div>
            </button>
          );
        })}
      </div>

      <p className="mt-2 text-xs text-muted-foreground" data-testid="resize-pricing-note">{machinePricingFootnote(ownHours)}</p>
      {sizesLoading && <p className="mt-2 text-xs text-muted-foreground">Checking which sizes your plan includes…</p>}
      {someDisallowed && (
        <p className="mt-2 text-xs text-muted-foreground">
          Larger sizes need a bigger compute plan.{" "}
          <button type="button" onClick={onSeePlans} className="font-medium text-primary hover:underline">
            See plans
          </button>
        </p>
      )}

      {/* Disks only grow (the operator never shrinks a bound volume), so a
          smaller size keeps the disk it has — and the meter bills a disk at
          its real capacity, not at the smaller tier's number. Saying so
          before the click is the difference between a known trade-off and
          a surprise on the bill. */}
      {shrinking && (
        <CardInset className="mt-4 text-sm text-muted-foreground" data-testid="resize-disk-note">
          The disk doesn&apos;t shrink. Disk above the new size&apos;s standard is billed while running too.
        </CardInset>
      )}

      {stage && (
        <p className="mt-4 text-sm font-medium text-foreground" data-testid="resize-progress">
          {stage === "stopping"
            ? "Stopping the machine…"
            : stage === "resizing"
              ? "Applying the new size…"
              : "Starting the machine…"}
        </p>
      )}

      <ErrorNote message={error} />

      <div className="mt-6 flex justify-end gap-3">
        <Button variant="outline" disabled={isPending} onClick={onClose} size="xl">Cancel</Button>
        <Button
          loading={isPending}
          disabled={selected === null}
          onClick={() => selected !== null && onConfirm(selected)}
         size="xl">
          {isPending ? "Resizing…" : running ? "Resize and restart" : "Resize"}
        </Button>
      </div>
    </Modal>
  );
}

function InfoRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-4 border-b border-border py-2 last:border-0">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className="text-right text-sm font-medium text-foreground">{value || "—"}</dd>
    </div>
  );
}

function EnvironmentDetail({
  daemonId,
  cloud,
  onBack,
}: {
  daemonId: string;
  /** False without a control plane: no spec, lifecycle, delete or port rules. */
  cloud: boolean;
  onBack: () => void;
}) {
  const qc = useQueryClient();
  const [error, setError] = useState("");
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [restartOpen, setRestartOpen] = useState(false);
  // Non-null only while THIS client is driving a restart. It gates every
  // lifecycle button (see lifecyclePlan) and drives the progress copy, since
  // a restart is two RPCs with a wait between them and the daemon's own
  // status is not sufficient to tell you one is in flight.
  const [restartStage, setRestartStage] = useState<RestartStage | null>(null);
  // Which action owns `restartStage`: a Restart, or a Resize of a running
  // machine (a restart with the resize done while it is stopped).
  const [cycling, setCycling] = useState<LifecycleAction | null>(null);
  const [resizeOpen, setResizeOpen] = useState(false);
  const [resizeError, setResizeError] = useState("");
  const [renaming, setRenaming] = useState(false);
  const [growOpen, setGrowOpen] = useState(false);
  const [growError, setGrowError] = useState("");
  const [growDenied, setGrowDenied] = useState(false);
  // The grow RPC's own stage. With a restart, restartStage takes over after it.
  const [growStage, setGrowStage] = useState<GrowStage | null>(null);
  const goToBilling = useGoToBilling("machines");

  // The detail view reads BOTH halves, from the service that owns each.
  //
  // Status, lifecycle phase and liveness come from the one daemon list — the
  // registry is the only thing that knows whether a machine has actually
  // attached, and the lifecycle buttons below act on that. The provisioning
  // SPEC and the preview-proxy facts (port-access rules, the workspace base
  // domain, idle timeout, storage) come from control-plane's GetDaemon, which
  // is kept deliberately for exactly this: they have no reliant equivalent.
  // See docs/design/one-daemon-list.md.
  const listQ = useQuery(daemonListQuery({
    refetchInterval: restartStage ? 2_000 : 15_000,
  }));
  const daemon = listQ.data?.find((d) => d.daemonId === daemonId);

  const daemonQ = useQuery({
    queryKey: QK.daemon(daemonId),
    queryFn: () => getDaemon(daemonId),
    // 2s while a restart runs: the default 15s would leave the progress copy
    // ("Stopping…" → "Starting…") lagging the machine by up to a quarter
    // minute, which reads as a hang during the one operation that most needs
    // to look alive. 5s while the disk grows, which takes about a minute.
    refetchInterval: (query) =>
      restartStage ? 2_000 : query.state.data?.daemon?.disk?.state === DaemonDiskState.EXPANDING ? 5_000 : 15_000,
    enabled: cloud,
  });
  const spec = daemonQ.data?.daemon;
  const workspaceBaseDomain = daemonQ.data?.workspaceBaseDomain ?? "";

  const refetchAll = () => {
    qc.invalidateQueries({ queryKey: QK.daemon(daemonId) });
    invalidateDaemonList(qc);
  };

  const suspendMut = useMutation({
    mutationFn: () => suspendDaemon(daemonId),
    onSuccess: () => { setError(""); refetchAll(); },
    onError: (e) => setError(describeError(e, "Failed to suspend machine")),
  });
  const resumeMut = useMutation({
    mutationFn: () => resumeEnvironment(daemonId),
    onSuccess: () => { markWaking(daemonId); setError(""); refetchAll(); },
    onError: (e) => setError(describeError(e, "Failed to resume machine")),
  });
  const deleteMut = useMutation({
    mutationFn: () => deleteDaemon(daemonId),
    onSuccess: () => { setDeleteOpen(false); invalidateDaemonList(qc); onBack(); },
    onError: (e) => setError(describeError(e, "Failed to delete machine")),
  });

  /**
   * Restart = suspend, wait for the pod to actually stop, resume.
   *
   * The wait is not padding. SuspendDaemon marks the daemon row SUSPENDED
   * before the pod is torn down, so a restart that trusted `status` would
   * resume into a still-running pod and the pod would never be rebuilt —
   * which is exactly the "my machine won't pick up the new image" problem
   * this feature exists to solve. restartMachine polls `lifecycle_phase`,
   * which tracks the real workspace. See machineLifecycle.ts's header.
   *
   * Resume is also the point at which the control plane re-stamps the
   * desired image (prod runs ROLLOUT_STRATEGY=resume, which never
   * force-rolls a running pod), so this sequence is what actually rolls a
   * new workspace image.
   */
  // The stop → (resize) → start sequence shared by Restart and by Resize of a
  // running machine.
  const cycleMachine = (resize?: () => Promise<void>) =>
    restartMachine({
      suspend: () => suspendDaemon(daemonId),
      resume: () => resumeEnvironment(daemonId),
      // Polls the REGISTRY, which owns status and lifecycle phase. This
      // read used to be control-plane's GetDaemon; pointing it at the one
      // list keeps the signal restartMachine waits on (phase SUSPENDED) and
      // the status the UI shows from coming out of two different services,
      // which is the disagreement docs/design/one-daemon-list.md removes.
      // Through the shared entry, so each poll also moves the page's status.
      poll: async () => {
        const row = (await fetchDaemonList(qc)).find((d) => d.daemonId === daemonId);
        return {
          phase: row?.lifecyclePhase ?? LIFECYCLE_PHASE_UNSPECIFIED,
          status: row?.status ?? 0,
        };
      },
      sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
      onStage: setRestartStage,
      resize,
    });

  const restartMut = useMutation({
    mutationFn: async () => {
      setCycling("restart");
      await cycleMachine();
    },
    onSuccess: () => { setError(""); refetchAll(); },
    onError: (e) => setError(describeError(e, "Failed to restart machine")),
    // Clear the stage on BOTH paths: leaving it set after a failure would
    // disable every lifecycle button with a stale "Stopping…" reason and
    // strand the machine with no way to act on it from this page.
    onSettled: () => { setRestartStage(null); setCycling(null); refetchAll(); },
  });

  /**
   * Resize. The control plane resizes only a SUSPENDED machine, and the size
   * lands when it next starts (control-plane docs/design/daemon-resize.md):
   *
   *   suspended → one ResizeDaemon call; the machine stays suspended.
   *   otherwise → the Restart sequence with the resize done while stopped.
   *               restartMachine starts the machine again even if the resize
   *               is refused, so a denial costs a restart, never a machine
   *               the user had running.
   */
  const resizeMut = useMutation({
    mutationFn: async (size: DaemonSize) => {
      if (daemon?.status === DaemonStatus.SUSPENDED) {
        await resizeDaemon(daemonId, size);
        return;
      }
      setCycling("resize");
      await cycleMachine(() => resizeDaemon(daemonId, size));
    },
    onSuccess: (_data, size) => {
      setResizeError("");
      setResizeOpen(false);
      const label = SIZE_TIERS.find((t) => t.value === size)?.label ?? "the new size";
      toast.success(
        daemon?.status === DaemonStatus.SUSPENDED
          ? `${daemon ? daemonDisplayName(daemon) : "The machine"} will start as ${label}`
          : `${daemon ? daemonDisplayName(daemon) : "The machine"} is restarting as ${label}`,
      );
    },
    onError: (e) => setResizeError(describeError(e, "Failed to resize machine")),
    onSettled: () => { setRestartStage(null); setCycling(null); refetchAll(); },
  });

  /**
   * Grow disk. The cloud disk grows at once, running or suspended, so unlike
   * Resize the grow itself needs no stop. A running guest keeps the disk size
   * it booted with, though, so "Grow and restart now" grows FIRST and then
   * runs the Restart sequence. Growing first means a refusal (the plan's
   * limit, funding) costs nothing: the machine is never stopped for a grow
   * that did not happen.
   */
  const growMut = useMutation({
    mutationFn: async ({ targetGib, restart }: { targetGib: number; restart: boolean }) => {
      setGrowStage("growing");
      await growDaemonDisk(daemonId, targetGib);
      setGrowStage(null);
      if (!restart) return;
      try {
        await cycleMachine();
      } catch (e) {
        throw new Error(
          `The disk is growing to ${targetGib} GB, but the restart failed: ${describeError(e)}. ` +
            "Restart the machine to use the space.",
        );
      }
    },
    onSuccess: (_data, { targetGib, restart }) => {
      setGrowError("");
      setGrowDenied(false);
      setGrowOpen(false);
      const name = daemon ? daemonDisplayName(daemon) : "The machine";
      toast.success(
        restart
          ? `${name} restarted with a ${targetGib} GB disk`
          : `Growing ${name}'s disk to ${targetGib} GB`,
      );
    },
    onError: (e) => {
      setGrowError(describeError(e, "Failed to grow the disk"));
      setGrowDenied(isStorageDenied(e));
    },
    onSettled: () => { setGrowStage(null); setRestartStage(null); setCycling(null); refetchAll(); },
  });

  // The same server answer the create picker uses, so the two cannot disagree
  // about what is runnable. Read for a managed machine up front, not only
  // once Resize opens: its disk-growth limit decides whether "Grow disk" is
  // offered at all, and a plan with no limit offers no growth.
  const managedMachine = daemon ? !isExternalDaemon(daemon) : false;
  const eligibilityQ = useQuery({
    queryKey: QK.computeEligibility,
    queryFn: () => getComputeEligibility(),
    staleTime: 30_000,
    enabled: cloud && (resizeOpen || managedMachine),
  });
  const resizeAllowed = useMemo(
    () => sizeTiersFromWire(eligibilityQ.data?.allowedDaemonSizes ?? []),
    [eligibilityQ.data?.allowedDaemonSizes],
  );
  const pricing = usePlans({ enabled: cloud }).data?.daemonPricing;
  // The plan's monthly allowance (a server total, not what is left of it).
  const includedSmallHours = useComputeUsage("current", { enabled: cloud }).data?.includedSmallDaemonSeconds;
  const includedHours = includedSmallHours === undefined ? undefined : Number(includedSmallHours) / 3600;
  const disk = spec?.disk;
  const maxDiskGib = eligibilityQ.data?.maxDaemonStorageGib ?? 0;
  // Offered only where the plan grows disks at all. A machine already at
  // its plan's limit still gets it: the modal says so and names what would
  // raise the limit.
  const canGrowDisk = cloud && managedMachine && (disk?.requestedGib ?? 0) > 0 && maxDiskGib > 0;
  const openGrow = () => { setGrowError(""); setGrowDenied(false); setGrowOpen(true); };

  const status = daemon ? daemonStatus(daemon) : "pending";
  const waking = useWakingMachines();
  const presentation = presentMachineStatus(status, daemon?.lastStatusMessage, waking.get(daemonId));
  useEffect(() => {
    if (presentation.wakeFinished) clearWaking(daemonId);
  }, [presentation.wakeFinished, daemonId]);
  const badge = presentation.label
    ? { label: presentation.label, variant: statusBadge.pending.variant }
    : statusBadge[status];
  const connected = daemon?.status === DaemonStatus.ACTIVE;
  const busy =
    suspendMut.isPending ||
    resumeMut.isPending ||
    deleteMut.isPending ||
    restartMut.isPending ||
    resizeMut.isPending ||
    growMut.isPending;
  // The registry's size name first; control-plane's spec as a fallback for a
  // row whose size never reached the mirror.
  const currentSize =
    SIZE_TIERS.find((t) => t.name === daemon?.size)?.value ??
    (spec?.size ? SIZE_TIERS.find((t) => t.value === spec.size)?.value : undefined);
  const external = daemon ? isExternalDaemon(daemon) : false;
  // Restart is offered beside a grow only where the Restart button itself
  // would be live: not on a FAILED machine, nor one still starting.
  const restartPlan = daemon ? lifecyclePlan(daemon, null) : null;
  const restartable = Boolean(restartPlan?.offer.includes("restart") && restartPlan.disabledReason === null);
  const growNotice = disk ? diskGrowStatus(disk, daemon ? daemon.status !== DaemonStatus.SUSPENDED : false) : null;
  // The registry list is what proves the machine exists; the control-plane
  // spec is an extra half that only a cloud build has.
  const loading = listQ.isLoading || (cloud && daemonQ.isLoading);
  const loadError = cloud ? daemonQ.error : listQ.error;
  // A managed pod's projects live under the clone root; a personal machine
  // has no safe default, so the user names one (or picks a reported project).
  const suggestedRoots = Array.from(
    new Set((daemon?.projects ?? []).map((p) => p.path?.trim()).filter((p): p is string => Boolean(p))),
  ).slice(0, 6);

  return (
    <div className="space-y-6">
      <button
        type="button"
        onClick={onBack}
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="h-4 w-4" /> Back to Machines
      </button>

      {loading ? (
        <Card><CardContent className="text-sm text-muted-foreground">Loading machine…</CardContent></Card>
      ) : loadError ? (
        <Card><CardContent className="text-sm text-destructive-ink">{describeError(loadError)}</CardContent></Card>
      ) : !daemon ? (
        <Card><CardContent className="text-sm text-muted-foreground">Machine not found.</CardContent></Card>
      ) : (
        <>
          <div
            data-testid="machine-detail-header"
            className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between"
          >
            <div className="flex flex-wrap items-center gap-3">
              {renaming ? (
                <RenameMachineField daemonId={daemon.daemonId} currentName={daemon.name} onDone={() => setRenaming(false)} />
              ) : (
                <>
                  <h2 className="text-xl font-semibold text-foreground">{daemonDisplayName(daemon)}</h2>
                  {cloud && <RenameMachineButton label={daemonDisplayName(daemon)} onClick={() => setRenaming(true)} />}
                </>
              )}
              {/*
                ONE badge. This header used to render two: the status badge
                ("Disconnected", red) and a separate connection badge
                ("Disconnected", grey), which for a disconnected machine
                printed the same word twice in two different colors and read
                as two conflicting facts. Lifecycle status is the broader of
                the two and already covers the connection case, so the
                connection badge is gone from here — the Status & Activity
                card below still states it as a labelled row, which is where
                a second opinion belongs if the two ever disagree.
              */}
              <Badge label={badge.label} variant={badge.variant} />
            </div>
            {cloud && (
<div className="flex flex-wrap items-center gap-2">
              <MachineLifecycleActions
                daemon={daemon}
                busy={busy}
                restartStage={restartStage}
                cycling={cycling}
                onSuspend={() => suspendMut.mutate()}
                onResume={() => resumeMut.mutate()}
                onRestart={() => setRestartOpen(true)}
                onResize={() => { setResizeError(""); setResizeOpen(true); }}
              />
              {(() => {
                const removal = canRemoveDaemon(daemon);
                const button = (
                  <Button
                    variant="danger"
                    disabled={busy || !removal.allowed}
                    onClick={() => setDeleteOpen(true)}
                   size="xl"
                    leftIcon={<Trash2 />}
                  >
                    {external ? "Remove" : "Delete"}
                  </Button>
                );
                return removal.allowed ? (
                  button
                ) : (
                  <Tooltip content={removal.reason ?? ""} side="bottom">
                    {button}
                  </Tooltip>
                );
              })()}
            </div>
)}
          </div>

          {/* A self-hosted machine runs on hardware this page does not
              control, so it gets an explanation rather than disabled
              buttons — a greyed-out Suspend would imply the capability
              exists and is merely unavailable right now. */}
          {external && cloud && (
            <p className="text-sm text-muted-foreground">
              This machine runs on your own hardware, so it can't be suspended or
              restarted from here. Stop or restart the Reliant daemon on the machine
              itself.
            </p>
          )}

          {error && <ErrorNote message={error} />}

          {/*
            Why a machine is failed or stopped, at the top, not buried in a
            detail row. This used to render only as "Last status" three cards
            down, so a user looking at a red "Failed" badge had no reason next
            to it and no cue that one existed further down the page.

            A STOPPED machine gets the same prominence but not the error
            styling: being stopped is normal, and the reason is there to
            explain a stop the user did not perform.
          */}
          {daemonFailureReason(daemon) &&
            (status === "suspended" ? (
              <div className="mb-4 rounded-md border border-border bg-background px-3 py-2 text-sm text-muted-foreground">
                {daemonFailureReason(daemon)}
              </div>
            ) : (
              <ErrorNote message={daemonFailureReason(daemon) ?? undefined} />
            ))}
          {lifecyclePlan(daemon, null).recoveryHint && (
            <p className="mb-4 text-sm text-muted-foreground" data-testid="machine-recovery-hint">
              {lifecyclePlan(daemon, null).recoveryHint}
            </p>
          )}
          {cloud && !external && growNotice && (
            <DiskGrowNotice
              status={growNotice}
              machineName={daemonDisplayName(daemon)}
              restartable={restartable && !busy}
              onRestart={() => setRestartOpen(true)}
            />
          )}

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <Card>
              <CardHeader><CardTitle className="inline-flex items-center gap-2"><Server className="h-4 w-4 text-muted-foreground" /> Overview</CardTitle></CardHeader>
              <CardContent>
                <dl>
                  {external ? (
                    <>
                      <InfoRow label="Hostname" value={daemon.hostname} />
                      <InfoRow label="Platform" value={daemon.platform} />
                    </>
                  ) : (
                    <>
                      <InfoRow label="Size" value={SIZE_TIERS.find((t) => t.value === currentSize)?.label ?? "Custom"} />
                      <InfoRow label="Cost" value={<MachineCost daemonId={daemon.daemonId} size={daemon.size} />} />
                      <InfoRow
                        label="Storage"
                        value={
                          (disk?.requestedGib || spec?.storageSize) && (
                            <span className="inline-flex items-baseline gap-2">
                              {provisionedDiskGib(disk) !== null ? `${provisionedDiskGib(disk)} GB disk` : "Disk size unavailable"}
                              {canGrowDisk && (
                                <button
                                  type="button"
                                  onClick={openGrow}
                                  disabled={busy}
                                  className="font-medium text-primary hover:underline disabled:cursor-not-allowed disabled:opacity-50"
                                >
                                  Grow
                                </button>
                              )}
                            </span>
                          )
                        }
                      />
                    </>
                  )}
                  <InfoRow label="Created" value={fmtTimestamp(daemon.createdAt)} />
                  <InfoRow label="Updated" value={fmtTimestamp(spec?.updatedAt)} />
                </dl>
              </CardContent>
            </Card>
            <Card>
              <CardHeader><CardTitle className="inline-flex items-center gap-2"><Activity className="h-4 w-4 text-muted-foreground" /> Status & Activity</CardTitle></CardHeader>
              <CardContent>
                <dl>
                  <InfoRow label="Connection" value={<Badge label={connected ? "Connected" : "Disconnected"} variant={connected ? "success" : "neutral"} />} />
                  {/*
                    The label tracks the state. "Connected at <timestamp>"
                    beside a DISCONNECTED machine describes a connection that
                    no longer exists, and reads as a live one — the timestamp
                    is in fact when the machine was last connected. Same
                    value, honestly captioned.
                  */}
                  <InfoRow
                    label={connected ? "Connected at" : "Last connected"}
                    value={fmtTimestamp(daemon.connectedAt)}
                  />
                  {!external && <InfoRow label="Idle timeout" value={spec?.idleTimeout || "Not set"} />}
                  <InfoRow label="Last status" value={daemon.lastStatusMessage} />
                  {/* Restarts the machine did not choose: its VM dying, or the
                      daemon's container killed for memory. Without this row a
                      crash reads as a reconnect. */}
                  {!external && <InfoRow label="Last crash" value={machineCrashSummary(spec, fmtTimestamp) ?? "None"} />}
                </dl>
              </CardContent>
            </Card>
          </div>

          {/* Every volume the machine reports, and what fills the full one. */}
          <MachineDiskCard
            daemon={daemon}
            machineLabel={daemonDisplayName(daemon)}
            onGrowDisk={canGrowDisk ? openGrow : undefined}
            growDisabled={busy}
          />

          {/* Which outside AI apps can reach this machine. Above port access:
              it is the broader grant — tools on the machine, not one port. */}
          <MachineAccess
            daemonId={daemonId}
            machineName={daemonDisplayName(daemon)}
            personal={external}
            defaultPathRoot={external ? "" : CLOUD_PROJECT_ROOT}
            suggestedRoots={suggestedRoots}
          />

          {cloud && <PortAccessPanel daemonId={daemonId} workspaceBaseDomain={workspaceBaseDomain} />}

          <RemoveMachineModal
            target={deleteOpen ? daemon : null}
            isPending={deleteMut.isPending}
            onClose={() => setDeleteOpen(false)}
            onConfirm={() => deleteMut.mutate()}
          />

          <RestartMachineModal
            target={restartOpen ? daemon : null}
            isPending={restartMut.isPending}
            stage={restartStage}
            onClose={() => setRestartOpen(false)}
            onConfirm={() =>
              restartMut.mutate(undefined, { onSuccess: () => setRestartOpen(false) })
            }
          />

          <ResizeMachineModal
            target={resizeOpen ? daemon : null}
            currentSize={currentSize}
            running={daemon.status !== DaemonStatus.SUSPENDED}
            allowedSizes={resizeAllowed}
            sizesLoading={eligibilityQ.isLoading}
            pricing={pricing}
            includedHours={includedHours}
            isPending={resizeMut.isPending}
            stage={cycling === "resize" ? restartStage : null}
            error={resizeError}
            onClose={() => setResizeOpen(false)}
            onConfirm={(size) => resizeMut.mutate(size)}
            onSeePlans={goToBilling}
          />

          {disk && (
            <GrowDiskModal
              open={growOpen}
              machineName={daemonDisplayName(daemon)}
              currentGib={disk.requestedGib}
              provisionedGib={provisionedDiskGib(disk)}
              maxGib={maxDiskGib}
              limitLoading={eligibilityQ.isLoading}
              running={daemon.status !== DaemonStatus.SUSPENDED}
              restartable={restartable}
              pricing={pricing}
              isPending={growMut.isPending}
              stage={growMut.isPending ? (growStage ?? restartStage) : null}
              error={growError}
              storageDenied={growDenied}
              onClose={() => setGrowOpen(false)}
              onConfirm={(targetGib, restart) => growMut.mutate({ targetGib, restart })}
              onSeePlans={goToBilling}
            />
          )}
        </>
      )}
    </div>
  );
}

function PortAccessPanel({ daemonId, workspaceBaseDomain }: { daemonId: string; workspaceBaseDomain: string }) {
  const qc = useQueryClient();
  const [port, setPort] = useState("");
  const [mode, setMode] = useState<PortAccessMode>(PortAccessMode.PUBLIC);
  const [error, setError] = useState("");
  const [createdToken, setCreatedToken] = useState<string | null>(null);

  const rulesQ = useQuery({
    queryKey: QK.ports(daemonId),
    queryFn: () => listPortAccessRules(daemonId),
  });
  const rules: PortAccessRule[] = rulesQ.data ?? [];

  const invalidate = () => qc.invalidateQueries({ queryKey: QK.ports(daemonId) });

  const addMut = useMutation({
    mutationFn: () => setPortAccess({ daemonId, port: parseInt(port, 10), accessMode: mode }),
    onSuccess: (res) => {
      if (res.accessToken) setCreatedToken(res.accessToken);
      setPort("");
      setMode(PortAccessMode.PUBLIC);
      setError("");
      invalidate();
    },
    onError: (e) => setError(describeError(e, "Failed to add port rule")),
  });
  const removeMut = useMutation({
    mutationFn: (p: number) => removePortAccess(daemonId, p),
    onSuccess: () => invalidate(),
    onError: (e) => setError(describeError(e, "Failed to remove port rule")),
  });

  return (
    <Card>
      <CardHeader><CardTitle className="inline-flex items-center gap-2"><Shield className="h-4 w-4 text-muted-foreground" /> Port Access</CardTitle></CardHeader>
      <CardContent>
        <form
          className="flex flex-wrap items-end gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            const p = parseInt(port, 10);
            if (!p || p < 1 || p > 65535) return;
            setError("");
            addMut.mutate();
          }}
        >
          <div className="flex-1 min-w-[8rem]">
            <label htmlFor="port" className="mb-1.5 block text-sm font-medium text-foreground">Port</label>
            <input id="port" type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} placeholder="3000" className={inputCls} />
          </div>
          <div className="flex-1 min-w-[10rem]">
            <label htmlFor="mode" className="mb-1.5 block text-sm font-medium text-foreground">Access mode</label>
            <select id="mode" value={String(mode)} onChange={(e) => setMode(Number(e.target.value) as PortAccessMode)} className={inputCls}>
              <option value={String(PortAccessMode.PUBLIC)}>Public</option>
              <option value={String(PortAccessMode.AUTHENTICATED)}>Authenticated</option>
              <option value={String(PortAccessMode.TOKEN)}>Token</option>
            </select>
          </div>
          <Button type="submit" loading={addMut.isPending} disabled={!port} size="xl" leftIcon={<Plus />}>
            Add
          </Button>
        </form>

        {error && <div className="mt-3"><ErrorNote message={error} /></div>}

        {rules.length === 0 ? (
          <p className="mt-4 text-center text-sm text-muted-foreground">No port access rules. Add one above to expose a port.</p>
        ) : (
          <div className="mt-4">
            <div className="forge-ui">
              <DataTable<{ id: string; r: (typeof rules)[number] }>
                ariaLabel="Port access rules"
                columns={[
                  { key: "port", header: "Port", className: "font-mono", render: (_, { r }) => r.port },
                  {
                    key: "access",
                    header: "Access",
                    render: (_, { r }) => <Badge label={accessModeLabel[r.accessMode] ?? "Unknown"} variant="neutral" />,
                  },
                  {
                    key: "url",
                    header: "URL",
                    render: (_, { r }) => {
                      const url = workspaceBaseDomain ? workspaceBaseDomain.replace("{port}", String(r.port)) : "";
                      return url ? (
                        <a href={url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 font-mono text-xs text-primary hover:underline">
                          {url} <ExternalLink className="h-3 w-3" />
                        </a>
                      ) : (
                        "—"
                      );
                    },
                  },
                  {
                    key: "actions",
                    header: "Actions",
                    srHeader: true,
                    className: "text-right",
                    render: (_, { r }) => {
                      const removing = removeMut.isPending && removeMut.variables === r.port;
                      return (
                        <Button variant="ghost" size="md" disabled={removing} onClick={() => removeMut.mutate(r.port)} leftIcon={<Trash2 className="text-destructive-ink" />}>
                          {removing ? "Removing…" : "Remove"}
                        </Button>
                      );
                    },
                  },
                ]}
                data={rules.map((r) => ({ id: r.id, r }))}
                getRowKey={(row) => row.id}
              />
            </div>
          </div>
        )}

        <TokenRevealModal token={createdToken} onClose={() => setCreatedToken(null)} title="Port Access Token Created" />
      </CardContent>
    </Card>
  );
}

// Shared "copy this once" reveal modal for newly-minted tokens.
function TokenRevealModal({ token, onClose, title }: { token: string | null; onClose: () => void; title: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    if (!token) return;
    await navigator.clipboard.writeText(token);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  }
  return (
    <Modal open={token !== null} onClose={() => { setCopied(false); onClose(); }} title={title}>
      <div className="space-y-4">
        <div className="flex items-start gap-3 rounded-md border border-warning/30 bg-warning/10 p-3">
          <AlertTriangle className="mt-0.5 h-5 w-5 flex-shrink-0 text-warning-ink" />
          <p className="text-sm text-foreground">Copy this token now. You won't be able to see it again.</p>
        </div>
        <div className="flex items-center gap-2">
          <code className="flex-1 overflow-x-auto rounded-md border border-border/60 bg-background px-3 py-2 font-mono text-sm text-foreground">{token}</code>
          <Button variant="outline" onClick={copy} size="xl">
            {copied ? <><Check className="h-4 w-4 text-success-ink" /> Copied</> : <><Copy className="h-4 w-4" /> Copy</>}
          </Button>
        </div>
        <div className="flex justify-end">
          <Button onClick={() => { setCopied(false); onClose(); }} size="xl">Done</Button>
        </div>
      </div>
    </Modal>
  );
}