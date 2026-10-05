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
 * /settings/environments and one machine's detail is
 * /settings/environments/$machineId, so Back, refresh and deep links (the
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
import React, { useMemo, useState } from "react";
import { createPortal } from "react-dom";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import {
  Activity,
  AlertTriangle,
  ArrowLeft,
  Check,
  Clock,
  Copy,
  Cpu,
  ExternalLink,
  GitBranch,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Server,
  Shield,
  Laptop,
  Trash2,
  X,
} from "lucide-react";

import { cn } from "@/lib/utils";
import { Tooltip } from "@/components/ui/Tooltip";
import { capabilities } from "@/services/controlPlane/capabilities";
import {
  Button,
  Badge,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  EmptyState,
  PageHeader,
  StatusDot,
  Table,
  Tbody,
  Td,
  Th,
  Thead,
  Tr,
  type BadgeVariant,
  type StatusDotVariant,
} from "./ui";
import {
  DaemonSize,
  PortAccessMode,
  createEnvironment,
  deleteDaemon,
  describeError,
  getComputeSubscription,
  getDaemon,
  listPortAccessRules,
  portAccessRulesQueryKey,
  removePortAccess,
  resumeEnvironment,
  setPortAccess,
  suspendDaemon,
  type PortAccessRule,
} from "@/services/controlPlane/environments";
import { create } from "@bufbuild/protobuf";
import { grpcClient } from "@/api/grpc-client";
import {
  DaemonStatus,
  ListDaemonsRequestSchema,
  type DaemonInfo as Daemon,
} from "@/gen/reliant/v1/daemon_registry_pb";
import {
  LIFECYCLE_PHASE_UNSPECIFIED,
  lifecyclePlan,
  restartMachine,
  type LifecycleAction,
  type RestartStage,
} from "./machineLifecycle";
import { SelfHostedDaemonConnect } from "@/components/Projects/SelfHostedDaemonConnect";
import { getComputeEligibility } from "@/services/controlPlane/billing";
import { useGoToBilling } from "@/hooks/useGoToBilling";
import { usePlans } from "@/hooks/useCloudBillingQueries";
import { suspendedFeeLabel, type DaemonPricingLike } from "@/components/Billing/daemonUsage";
import { CLOUD_PROJECT_ROOT } from "@/lib/cloudProjectPath";
import { MachineAccess, activeGrantCounts, appCountLabel, useConnectors } from "./machineAccess";
// The overage formatter, shared with the billing purchase grid so the two
// surfaces cannot disagree about how a rate is written.
import { formatOverageRate } from "./billingUtils";

// ── Query keys ──────────────────────────────────────────────────────────────
const QK = {
  daemons: ["cp", "environments", "list"] as const,
  // The detail view reads the same registry list but on its own cadence (2s
  // during a restart). A separate key rather than sharing QK.daemons: two
  // observers of one key negotiate a single interval, so sharing would either
  // slow the restart progress copy to the list's 15s or speed the whole list
  // up to 2s for every mounted consumer.
  detailList: ["cp", "environments", "detailList"] as const,
  daemon: (id: string) => ["cp", "environments", "detail", id] as const,
  // Shared with the header DetectedPortsChip's one-click-public toggle so a
  // "Make public" there invalidates this panel's rules query and vice-versa.
  ports: portAccessRulesQueryKey,
  computeSub: ["cp", "environments", "computeSubscription"] as const,
  computeEligibility: ["cp", "environments", "computeEligibility"] as const,
};

// ── Status presentation ─────────────────────────────────────────────────────
type WsStatus = "active" | "suspended" | "failed" | "pending" | "disconnected";

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
};

const statusDotVariant: Record<WsStatus, StatusDotVariant> = {
  active: "active",
  suspended: "paused",
  failed: "error",
  pending: "pending",
  disconnected: "error",
};

function daemonStatus(d: Daemon): WsStatus {
  return statusFromEnum[d.status] ?? "pending";
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

// A UUID (v4-shaped, 36 chars with dashes at the standard offsets) is not a
// name a person chose — it's what the control-plane falls back to when a
// self-hosted daemon connects without registering one (see
// control-plane/internal/natsio/daemon_event_consumer.go). Render something
// readable instead: the hostname if we have one, else a short id-derived tag.
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function looksLikeBareUuid(name: string): boolean {
  return UUID_RE.test(name.trim());
}

export function daemonDisplayName(d: Pick<Daemon, "daemonId" | "hostname">): string {
  const name = d.hostname?.trim() ?? "";
  const isPlaceholder = !name || name === d.daemonId || looksLikeBareUuid(name);
  if (!isPlaceholder) return name;
  const shortId = (d.daemonId || "").slice(0, 8);
  return shortId ? `Self-hosted machine (${shortId})` : "Self-hosted machine";
}

// ── Size tiers (plan-gated) ─────────────────────────────────────────────────
//
// Specs only. These four carried a per-minute price — $0.02 / $0.04 / $0.08 /
// $0.16 — which was a client-side table of what machines cost, sitting on the
// button that creates one. Nothing on the wire states a per-size rate: the
// server states a per-PLAN overage rate, and that is the only number here
// anyone can reconcile against a charge. Same defect as the per-plan-id price
// tables that were deleted from billingUtils, one step closer to the money.
const SIZE_TIERS = [
  { value: DaemonSize.DAEMON_SIZE_SMALL, name: "small", label: "Small", specs: "1 CPU · 2GB RAM" },
  { value: DaemonSize.DAEMON_SIZE_MEDIUM, name: "medium", label: "Medium", specs: "2 CPU · 4GB RAM" },
  { value: DaemonSize.DAEMON_SIZE_LARGE, name: "large", label: "Large", specs: "4 CPU · 8GB RAM" },
  { value: DaemonSize.DAEMON_SIZE_XL, name: "xl", label: "XL", specs: "8 CPU · 16GB RAM" },
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
const NO_FUNDING_CTA = "Redeem a coupon or choose a plan";
const NO_FUNDING_DESCRIPTION =
  "Redeem a coupon code or subscribe to a compute plan to start a machine. Machines run on the compute sizes your plan allows.";

const IDLE_TIMEOUT_OPTIONS = [
  { value: "15m", label: "15 minutes" },
  { value: "30m", label: "30 minutes" },
  { value: "1h", label: "1 hour" },
  { value: "2h", label: "2 hours" },
  { value: "4h", label: "4 hours" },
] as const;

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

/**
 * A suspended machine's monthly disk fee: its size's disk (the one storage
 * number, from the server's price list) × the per-GiB-month fee. Null when
 * the server sent no price list or the size is unknown to it.
 */
function suspendedFeeOf(d: Daemon, pricing?: DaemonPricingLike): string | null {
  const name = SIZE_TIERS.find((t) => t.name === d.size)?.name;
  const row = name ? pricing?.sizes.find((s) => s.size === name) : undefined;
  return row ? suspendedFeeLabel(pricing, Number(row.storageGib)) : null;
}

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

function Field({ label, htmlFor, children }: { label: React.ReactNode; htmlFor?: string; children: React.ReactNode }) {
  return (
    <div className="mb-4">
      <label htmlFor={htmlFor} className="mb-1.5 block text-sm font-medium text-foreground">
        {label}
      </label>
      {children}
    </div>
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
 * "Run Reliant on your own machine" — the download + install + connect steps.
 *
 * The body is `SelfHostedDaemonConnect`, the SAME component onboarding's
 * ComputeStep and the ProjectPicker's connect modal render. That is
 * deliberate: download URLs, the Homebrew cask, the token step, and the
 * `reliant daemon start` command (which varies by deployment — see
 * lib/cli-commands) then have exactly one source of truth. Passing
 * mode="reference" drops the bootstrap-only flow control, since a user on
 * this page usually already has a working machine and is adding another.
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
          Install the desktop app or CLI on a laptop or server, then connect it
          with an access token. It shows up here once it connects.
        </p>
      </CardHeader>
      <CardContent>
        <SelfHostedDaemonConnect mode="reference" />
      </CardContent>
    </Card>
  );
}

// ── Root section ────────────────────────────────────────────────────────────
export function MachinesSection() {
  // The open machine is the URL (/settings/environments/$machineId), so Back
  // returns to the list, a refresh keeps it open, and it can be linked to.
  // `?daemon=<id>` is the older deep link; the route redirects it onto the
  // path, and it is still read here so a stale link never lands on the list.
  const params = useParams({ strict: false }) as { machineId?: string };
  const search = useSearch({ strict: false }) as { daemon?: string; from?: string };
  const navigate = useNavigate();
  const selectedId = params.machineId ?? search.daemon ?? null;
  const openMachine = (machineId: string) =>
    navigate({ to: "/settings/environments/$machineId", params: { machineId } });
  const backToList = () => navigate({ to: "/settings/$section", params: { section: "environments" } });
  // Without a control plane there are no managed machines to create or
  // drive, but registered machines — and the app access granted to them —
  // still exist, so the list and detail render in a registry-only mode rather
  // than a dead end. The self-hosted setup instructions matter MORE here.
  const cloud = capabilities.cloudDaemons;

  return (
    <div className="mx-auto max-w-5xl">
      {selectedId ? (
        <EnvironmentDetail daemonId={selectedId} cloud={cloud} onBack={backToList} />
      ) : (
        <div className="space-y-6">
          <div>
            <PageHeader
              title="Machines"
              subtitle="Managed and self-hosted machines that run your projects."
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
  // The per-daemon price list (design §6.2), for the suspended-disk fee shown
  // beside Delete. Absent from an older server, in which case no fee renders.
  const daemonPricing = usePlans({ enabled: cloud }).data?.daemonPricing;
  // Which machines outside AI apps can reach. Read once for the whole list
  // (ListConnectors is per user, not per machine) and counted per row.
  const appCounts = activeGrantCounts(useConnectors().data);
  // Routes to /settings/billing?tab=plans — the place a coupon is redeemed and
  // a plan is bought. Shared with every other "go buy compute" call site so
  // the destination cannot drift; see the hook's own header.
  const goToBilling = useGoToBilling();

  // The LIST comes from reliant's registry — the one daemon list
  // (docs/design/one-daemon-list.md). The per-machine DETAIL view below still
  // calls control-plane's GetDaemon, which is kept deliberately: port-access
  // rules, the workspace base domain and the provisioning spec are
  // control-plane concerns with no reliant equivalent.
  const daemonsQ = useQuery({
    queryKey: QK.daemons,
    queryFn: async () => {
      const resp = await grpcClient
        .daemonRegistry()
        .listDaemons(create(ListDaemonsRequestSchema));
      return resp.daemons;
    },
    staleTime: 10_000,
    refetchInterval: 15_000,
  });

  // THE GATE. GetCurrentUserComputeEligibility is the server's own prediction
  // of internal/svcdaemon.checkDaemonSizeAllowed, and it is the only thing that
  // decides whether machine creation is offered here.
  //
  // This page used to gate on getComputeSubscription() alone, which is a
  // strictly narrower rule than the server's: a redeemed compute coupon grants
  // machine MINUTES and no subscription, so that check reported "not
  // subscribed" for a user the server would have happily started a machine
  // for, and replaced New Machine with a dead badge telling them to subscribe.
  // Predicting a server rule by reimplementing a piece of it is how that
  // happened; asking the server is how it stops happening.
  const eligibilityQ = useQuery({
    queryKey: QK.computeEligibility,
    queryFn: () => getComputeEligibility(),
    staleTime: 30_000,
    enabled: cloud,
  });
  const allowedSizes = useMemo(
    () => sizeTiersFromWire(eligibilityQ.data?.allowedDaemonSizes ?? []),
    [eligibilityQ.data?.allowedDaemonSizes],
  );
  // Both halves are required and they are different facts: eligibility is
  // "is there funding at all", sizes is "is there anything runnable". The
  // server enforces both, so offering a Create button that satisfies only one
  // would just move the denial to after the click.
  const canCreate = Boolean(eligibilityQ.data?.eligible) && allowedSizes.length > 0;

  // Not part of the gate — the per-minute overage rate is a display fact that
  // only an active subscription states, and the eligibility response
  // deliberately carries no price. A coupon-funded user has no subscription
  // and therefore no rate to show, which is the truthful answer rather than a
  // zero standing in for one.
  const computeSubQ = useQuery({
    queryKey: QK.computeSub,
    queryFn: () => getComputeSubscription(),
    staleTime: 30_000,
    enabled: cloud,
  });
  const plan = computeSubQ.data?.plan;

  const invalidate = () => qc.invalidateQueries({ queryKey: QK.daemons });

  const suspendMut = useMutation({
    mutationFn: (id: string) => suspendDaemon(id),
    onSuccess: () => { setActionError(""); invalidate(); },
    onError: (e) => setActionError(describeError(e, "Failed to suspend machine")),
  });
  const resumeMut = useMutation({
    mutationFn: (id: string) => resumeEnvironment(id),
    onSuccess: () => { setActionError(""); invalidate(); },
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
        {!cloud ? null : canCreate ? (
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4" /> New Machine
          </Button>
        ) : !eligibilityQ.isLoading ? (
          <Button variant="outline" onClick={goToBilling}>
            {NO_FUNDING_CTA}
          </Button>
        ) : null}
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
            <Button variant="outline" size="sm" className="mt-3" onClick={() => daemonsQ.refetch()}>
              <RefreshCw className="h-3.5 w-3.5" /> Retry
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
          title="No machines"
          description={
            canCreate
              ? "Create your first cloud machine."
              : NO_FUNDING_DESCRIPTION
          }
          action={
            canCreate ? (
              <Button onClick={() => setCreateOpen(true)}>
                <Plus className="h-4 w-4" /> New Machine
              </Button>
            ) : (
              <Button variant="outline" onClick={goToBilling}>
                {NO_FUNDING_CTA}
              </Button>
            )
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
                pricing={daemonPricing}
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

      <CreateEnvironmentModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        allowedSizes={allowedSizes}
        overageCentsPerMinute={
          plan?.structuredLimits?.daemonOveragePerMinuteCents ?? 0
        }
        onCreated={() => { setCreateOpen(false); invalidate(); }}
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
  pricing,
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
  return (
    <Table>
      <Thead>
        <Tr>
          <Th>Name</Th>
          <Th>Status</Th>
          <Th>Resources</Th>
          <Th>Created</Th>
          <Th className="text-right">Actions</Th>
        </Tr>
      </Thead>
      <Tbody>
        {daemons.map((d) => {
          const status = daemonStatus(d);
          const badge = statusBadge[status];
          const failureReason = daemonFailureReason(d);
          const isSuspended = d.status === DaemonStatus.SUSPENDED;
          // Specs come from the size tier rather than from per-machine
          // resource requests. Those requests are part of the provisioning
          // SPEC, which lives only in control-plane and is deliberately absent
          // from the one list; the size determines them, and SIZE_TIERS already
          // states the mapping this page renders everywhere else. A machine
          // with no reported size (every self-hosted one) shows "—".
          const resources =
            SIZE_TIERS.find((t) => t.name === d.size)?.specs || "—";
          return (
            <Tr key={d.daemonId}>
              <Td>
                <button
                  type="button"
                  onClick={() => onOpenDetail(d.daemonId)}
                  className="font-medium text-foreground hover:text-primary hover:underline"
                >
                  {daemonDisplayName(d)}
                </button>
                <AppAccessIndicator count={appCounts.get(d.daemonId) ?? 0} />
              </Td>
              <Td>
                <StatusDot variant={statusDotVariant[status]} label={badge.label} />
                {failureReason && (
                  // Destructive red for a machine that BROKE; muted for one
                  // that is merely stopped. A stopped machine is a normal
                  // state whose reason is informational, and colouring it as
                  // an error would make the whole list look on fire whenever
                  // the reconciler parks something for lack of funding.
                  <p
                    className={cn(
                      "mt-1 max-w-xs text-xs",
                      status === "suspended" ? "text-muted-foreground" : "text-destructive-ink",
                    )}
                  >
                    {failureReason}
                  </p>
                )}
              </Td>
              <Td className="text-muted-foreground">{resources}</Td>
              <Td className="text-muted-foreground">{fmtTimestamp(d.createdAt)}</Td>
              <Td className="text-right">
                {cloud && (
                  <div className="inline-flex items-center gap-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={busy}
                      onClick={() => (isSuspended ? onResume(d.daemonId) : onSuspend(d.daemonId))}
                    >
                      {isSuspended ? <Play className="h-4 w-4" /> : <Pause className="h-4 w-4" />}
                      {isSuspended ? "Resume" : "Suspend"}
                    </Button>
                    {/* A suspended machine still holds its disk, and the disk is
                        billed (design §5.1). Shown beside Delete because Delete
                        is the only thing that stops it. */}
                    {isSuspended && suspendedFeeOf(d, pricing) && (
                      <span className="text-xs text-muted-foreground" data-testid="machine-suspended-fee">
                        {suspendedFeeOf(d, pricing)} while suspended
                      </span>
                    )}
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => onDelete(d)}
                      aria-label={`Delete ${daemonDisplayName(d)}`}
                      title="Delete machine"
                    >
                      <Trash2 className="h-4 w-4 text-destructive-ink" aria-hidden="true" />
                    </Button>
                  </div>
                )}
              </Td>
            </Tr>
          );
        })}
      </Tbody>
    </Table>
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
  return (
    <Table>
      <Thead>
        <Tr>
          <Th>Name</Th>
          <Th>Status</Th>
          <Th>Platform</Th>
          <Th>Last seen</Th>
          <Th className="text-right">Actions</Th>
        </Tr>
      </Thead>
      <Tbody>
        {daemons.map((d) => {
          const status = daemonStatus(d);
          const badge = statusBadge[status];
          const connected = d.status === DaemonStatus.ACTIVE;
          const lastSeen = connected
            ? "Connected now"
            : fmtTimestamp(d.connectedAt);
          return (
            <Tr key={d.daemonId}>
              <Td>
                <button
                  type="button"
                  onClick={() => onOpenDetail(d.daemonId)}
                  className="font-medium text-foreground hover:text-primary hover:underline"
                >
                  {daemonDisplayName(d)}
                </button>
                <AppAccessIndicator count={appCounts.get(d.daemonId) ?? 0} />
              </Td>
              <Td>
                <StatusDot variant={statusDotVariant[status]} label={badge.label} />
              </Td>
              <Td className="text-muted-foreground">{d.platform || "—"}</Td>
              <Td className="text-muted-foreground">{lastSeen}</Td>
              <Td className="text-right">
                {onRemove && (() => {
                  const removal = canRemoveDaemon(d);
                  const button = (
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={!removal.allowed}
                      onClick={() => onRemove(d)}
                    >
                      <Trash2 className="h-4 w-4 text-destructive-ink" /> Remove
                    </Button>
                  );
                  return removal.allowed ? (
                    button
                  ) : (
                    <Tooltip content={removal.reason ?? ""} placement="left">
                      {button}
                    </Tooltip>
                  );
                })()}
              </Td>
            </Tr>
          );
        })}
      </Tbody>
    </Table>
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
        <Button variant="outline" onClick={onClose}>Cancel</Button>
        <Button variant="danger" isLoading={isPending} onClick={onConfirm}>
          {isPending ? confirmingLabel : confirmLabel}
        </Button>
      </div>
    </Modal>
  );
}

function CreateEnvironmentModal({
  open,
  onClose,
  allowedSizes,
  overageCentsPerMinute,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  // Always a list, never null. The nullable form used to mean "no plan, so
  // nothing is known"; the server now answers the size question directly, so
  // an empty list means exactly "no size may be started" and there is no
  // third, unknowable state to model.
  allowedSizes: DaemonSize[];
  overageCentsPerMinute: number;
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [gitRepo, setGitRepo] = useState("");
  const [idleTimeout, setIdleTimeout] = useState("30m");
  // No hardcoded default. MEDIUM used to be it, so a small-only plan opened
  // this modal with a size the server would refuse already selected. null
  // means "not yet chosen"; `effectiveSize` resolves it to the first size the
  // plan actually allows.
  const [size, setSize] = useState<DaemonSize | null>(null);
  const [error, setError] = useState("");

  const tiers = useMemo(
    () => SIZE_TIERS.filter((t) => allowedSizes.includes(t.value)),
    [allowedSizes],
  );

  // Keep the selection inside the plan's allowed set, and default to the
  // first allowed size rather than to a constant. Undefined when the plan
  // allows nothing at all, which is what makes Create unclickable.
  const effectiveSize = useMemo((): DaemonSize | undefined => {
    if (allowedSizes.length === 0) return undefined;
    return size !== null && allowedSizes.includes(size) ? size : allowedSizes[0];
  }, [allowedSizes, size]);

  const createMut = useMutation({
    mutationFn: () => {
      // Refuse rather than guess. A size the plan does not allow is one the
      // server rejects at CreateDaemon time, and guessing here would surface
      // that as a mysterious failure after the user pressed Create.
      if (effectiveSize === undefined) {
        throw new Error("Your plan does not allow any machine sizes.");
      }
      return createEnvironment({ name: name.trim(), size: effectiveSize, idleTimeout, gitRepo: gitRepo.trim() || undefined });
    },
    onSuccess: () => {
      setName("");
      setGitRepo("");
      setIdleTimeout("30m");
      setError("");
      onCreated();
    },
    onError: (e) => setError(describeError(e, "Failed to create machine")),
  });

  return (
    <Modal open={open} onClose={onClose} title="Create Machine" maxWidth="max-w-xl">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          setError("");
          createMut.mutate();
        }}
      >
        <Field label="Name" htmlFor="env-name">
          <input
            id="env-name"
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="my-machine"
            className={inputCls}
          />
        </Field>

        <Field
          label={
            <span className="inline-flex items-center gap-1.5">
              <GitBranch className="h-4 w-4 text-muted-foreground" /> Repository
              <span className="text-xs font-normal text-muted-foreground">(optional)</span>
            </span>
          }
          htmlFor="env-repo"
        >
          <input
            id="env-repo"
            type="url"
            value={gitRepo}
            onChange={(e) => setGitRepo(e.target.value)}
            placeholder="https://github.com/owner/repo.git"
            className={inputCls}
          />
          <p className="mt-1 text-xs text-muted-foreground">Automatic cloning is coming in a follow-up release.</p>
        </Field>

        <Field label={<span className="inline-flex items-center gap-1.5"><Cpu className="h-4 w-4 text-muted-foreground" /> Size</span>}>
          {/* A real radiogroup, not a row of styled buttons: this is a
              single-choice control and assistive tech should be told so. */}
          <div role="radiogroup" aria-label="Size" className="grid grid-cols-2 gap-2 md:grid-cols-4">
            {tiers.map((t) => {
              const selected = effectiveSize === t.value;
              return (
                <button
                  key={t.value}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  onClick={() => setSize(t.value)}
                  className={cn(
                    "rounded-lg border-2 p-3 text-left transition-colors",
                    selected ? "border-primary bg-primary/5" : "border-border bg-card hover:border-muted-foreground/40",
                  )}
                >
                  <div className="text-sm font-semibold text-foreground">{t.label}</div>
                  <div className="mt-1 text-xs text-muted-foreground">{t.specs}</div>
                </button>
              );
            })}
          </div>
          {tiers.length === 0 && (
            <p className="text-xs text-muted-foreground">No sizes available on your current plan.</p>
          )}
          {tiers.length > 0 && tiers.length < SIZE_TIERS.length && (
            <p className="mt-2 text-xs text-muted-foreground">Larger sizes are gated by your compute plan.</p>
          )}
          {/* The one rate the server actually states. It replaces four
              per-size rates the client invented; every size on a plan draws
              from the same bucket of included minutes and overflows at the
              same plan rate, so a per-size price implied a weighting the
              metering does not do. */}
          {tiers.length > 0 && overageCentsPerMinute > 0 && (
            <p className="mt-2 text-xs text-muted-foreground">
              Included hours are shared across machines. Beyond them, usage is
              billed at {formatOverageRate(overageCentsPerMinute)}.
            </p>
          )}
        </Field>

        <Field
          label={<span className="inline-flex items-center gap-1.5"><Clock className="h-4 w-4 text-muted-foreground" /> Auto-suspend after inactivity</span>}
          htmlFor="env-idle"
        >
          <select id="env-idle" value={idleTimeout} onChange={(e) => setIdleTimeout(e.target.value)} className={inputCls}>
            {IDLE_TIMEOUT_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>{o.label}</option>
            ))}
          </select>
          <p className="mt-1 text-xs text-muted-foreground">Suspended machines are not billed.</p>
        </Field>

        <ErrorNote message={error} />

        <div className="flex justify-end gap-3 border-t border-border pt-4">
          <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
          <Button type="submit" isLoading={createMut.isPending} disabled={!name.trim() || tiers.length === 0}>
            {createMut.isPending ? "Creating…" : "Create"}
          </Button>
        </div>
      </form>
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
function MachineLifecycleActions({
  daemon,
  busy,
  restartStage,
  onSuspend,
  onResume,
  onRestart,
}: {
  daemon: Daemon;
  busy: boolean;
  restartStage: RestartStage | null;
  onSuspend: () => void;
  onResume: () => void;
  onRestart: () => void;
}) {
  const plan = lifecyclePlan(daemon, restartStage);
  if (plan.offer.length === 0) return null;

  // The reason rides on `title` as well as disabling the button, so a user
  // who wonders why Resume is dead can find out by hovering rather than
  // guessing. A disabled control with no stated cause is the thing this
  // avoids.
  const disabled = busy || plan.disabledReason !== null;
  const reason = plan.disabledReason ?? undefined;

  const label: Record<LifecycleAction, string> = {
    suspend: "Suspend",
    resume: "Resume",
    // While a restart runs, the button narrates the stage it is in — the
    // whole operation takes a pod teardown plus a cold start, which is long
    // enough that a silent spinner reads as a hang.
    restart: restartStage === "stopping" ? "Stopping…" : restartStage === "starting" ? "Starting…" : "Restart",
  };
  const icon: Record<LifecycleAction, React.ReactNode> = {
    suspend: <Pause className="h-4 w-4" />,
    resume: <Play className="h-4 w-4" />,
    restart: <RefreshCw className={cn("h-4 w-4", restartStage && "animate-spin")} />,
  };
  const onClick: Record<LifecycleAction, () => void> = {
    suspend: onSuspend,
    resume: onResume,
    restart: onRestart,
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
        >
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
        <Button variant="outline" disabled={isPending} onClick={onClose}>Cancel</Button>
        <Button isLoading={isPending} onClick={onConfirm}>
          {isPending ? "Restarting…" : "Restart machine"}
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

  // The detail view reads BOTH halves, from the service that owns each.
  //
  // Status, lifecycle phase and liveness come from the one daemon list — the
  // registry is the only thing that knows whether a machine has actually
  // attached, and the lifecycle buttons below act on that. The provisioning
  // SPEC and the preview-proxy facts (port-access rules, the workspace base
  // domain, idle timeout, storage) come from control-plane's GetDaemon, which
  // is kept deliberately for exactly this: they have no reliant equivalent.
  // See docs/design/one-daemon-list.md.
  const listQ = useQuery({
    queryKey: QK.detailList,
    queryFn: async () => {
      const resp = await grpcClient
        .daemonRegistry()
        .listDaemons(create(ListDaemonsRequestSchema));
      return resp.daemons;
    },
    refetchInterval: restartStage ? 2_000 : 15_000,
  });
  const daemon = listQ.data?.find((d) => d.daemonId === daemonId);

  const daemonQ = useQuery({
    queryKey: QK.daemon(daemonId),
    queryFn: () => getDaemon(daemonId),
    // 2s while a restart runs: the default 15s would leave the progress copy
    // ("Stopping…" → "Starting…") lagging the machine by up to a quarter
    // minute, which reads as a hang during the one operation that most needs
    // to look alive.
    refetchInterval: restartStage ? 2_000 : 15_000,
    enabled: cloud,
  });
  const spec = daemonQ.data?.daemon;
  const workspaceBaseDomain = daemonQ.data?.workspaceBaseDomain ?? "";

  const refetchAll = () => {
    qc.invalidateQueries({ queryKey: QK.daemon(daemonId) });
    qc.invalidateQueries({ queryKey: QK.detailList });
    qc.invalidateQueries({ queryKey: QK.daemons });
  };

  const suspendMut = useMutation({
    mutationFn: () => suspendDaemon(daemonId),
    onSuccess: () => { setError(""); refetchAll(); },
    onError: (e) => setError(describeError(e, "Failed to suspend machine")),
  });
  const resumeMut = useMutation({
    mutationFn: () => resumeEnvironment(daemonId),
    onSuccess: () => { setError(""); refetchAll(); },
    onError: (e) => setError(describeError(e, "Failed to resume machine")),
  });
  const deleteMut = useMutation({
    mutationFn: () => deleteDaemon(daemonId),
    onSuccess: () => { setDeleteOpen(false); qc.invalidateQueries({ queryKey: QK.daemons }); onBack(); },
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
  const restartMut = useMutation({
    mutationFn: async () => {
      await restartMachine({
        suspend: () => suspendDaemon(daemonId),
        resume: () => resumeEnvironment(daemonId),
        // Polls the REGISTRY, which owns status and lifecycle phase. This
        // read used to be control-plane's GetDaemon; pointing it at the one
        // list keeps the signal restartMachine waits on (phase SUSPENDED) and
        // the status the UI shows from coming out of two different services,
        // which is the disagreement docs/design/one-daemon-list.md removes.
        poll: async () => {
          const resp = await grpcClient
            .daemonRegistry()
            .listDaemons(create(ListDaemonsRequestSchema));
          const row = resp.daemons.find((d) => d.daemonId === daemonId);
          return {
            phase: row?.lifecyclePhase ?? LIFECYCLE_PHASE_UNSPECIFIED,
            status: row?.status ?? 0,
          };
        },
        sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
        onStage: setRestartStage,
      });
    },
    onSuccess: () => { setError(""); refetchAll(); },
    onError: (e) => setError(describeError(e, "Failed to restart machine")),
    // Clear the stage on BOTH paths: leaving it set after a failure would
    // disable every lifecycle button with a stale "Stopping…" reason and
    // strand the machine with no way to act on it from this page.
    onSettled: () => { setRestartStage(null); refetchAll(); },
  });

  const status = daemon ? daemonStatus(daemon) : "pending";
  const badge = statusBadge[status];
  const connected = daemon?.status === DaemonStatus.ACTIVE;
  const busy =
    suspendMut.isPending || resumeMut.isPending || deleteMut.isPending || restartMut.isPending;
  const external = daemon ? isExternalDaemon(daemon) : false;
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
              <h2 className="text-xl font-semibold text-foreground">{daemonDisplayName(daemon)}</h2>
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
                onSuspend={() => suspendMut.mutate()}
                onResume={() => resumeMut.mutate()}
                onRestart={() => setRestartOpen(true)}
              />
              {(() => {
                const removal = canRemoveDaemon(daemon);
                const button = (
                  <Button
                    variant="danger"
                    disabled={busy || !removal.allowed}
                    onClick={() => setDeleteOpen(true)}
                  >
                    <Trash2 className="h-4 w-4" /> {external ? "Remove" : "Delete"}
                  </Button>
                );
                return removal.allowed ? (
                  button
                ) : (
                  <Tooltip content={removal.reason ?? ""} placement="bottom">
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
                      <InfoRow label="Size" value={SIZE_TIERS.find((t) => t.name === daemon.size)?.label ?? "Custom"} />
                      <InfoRow label="Storage" value={spec?.storageSize} />
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
                </dl>
              </CardContent>
            </Card>
          </div>

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
          <Button type="submit" isLoading={addMut.isPending} disabled={!port}>
            <Plus className="h-4 w-4" /> Add
          </Button>
        </form>

        {error && <div className="mt-3"><ErrorNote message={error} /></div>}

        {rules.length === 0 ? (
          <p className="mt-4 text-center text-sm text-muted-foreground">No port access rules. Add one above to expose a port.</p>
        ) : (
          <div className="mt-4">
            <Table>
              <Thead>
                <Tr>
                  <Th>Port</Th>
                  <Th>Access</Th>
                  <Th>URL</Th>
                  <Th className="text-right">Actions</Th>
                </Tr>
              </Thead>
              <Tbody>
                {rules.map((r) => {
                  const url = workspaceBaseDomain ? workspaceBaseDomain.replace("{port}", String(r.port)) : "";
                  const removing = removeMut.isPending && removeMut.variables === r.port;
                  return (
                    <Tr key={r.id}>
                      <Td className="font-mono">{r.port}</Td>
                      <Td><Badge label={accessModeLabel[r.accessMode] ?? "Unknown"} variant="neutral" /></Td>
                      <Td>
                        {url ? (
                          <a href={url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 font-mono text-xs text-primary hover:underline">
                            {url} <ExternalLink className="h-3 w-3" />
                          </a>
                        ) : "—"}
                      </Td>
                      <Td className="text-right">
                        <Button variant="ghost" size="sm" disabled={removing} onClick={() => removeMut.mutate(r.port)}>
                          <Trash2 className="h-4 w-4 text-destructive-ink" /> {removing ? "Removing…" : "Remove"}
                        </Button>
                      </Td>
                    </Tr>
                  );
                })}
              </Tbody>
            </Table>
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
          <Button variant="outline" onClick={copy}>
            {copied ? <><Check className="h-4 w-4 text-success-ink" /> Copied</> : <><Copy className="h-4 w-4" /> Copy</>}
          </Button>
        </div>
        <div className="flex justify-end">
          <Button onClick={() => { setCopied(false); onClose(); }}>Done</Button>
        </div>
      </div>
    </Modal>
  );
}