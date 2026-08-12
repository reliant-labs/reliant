/**
 * Settings → Machines section.
 *
 * Ports admin-web's "Workspaces" (daemons) management into reliant-web as a
 * self-contained settings panel, using ONLY public control-plane RPCs
 * (controlplane.v1.DaemonService + BillingService) for lifecycle. Data access
 * lives in `@/services/controlPlane/environments`; this file is presentation +
 * local UI state only.
 *
 * Layout: a single machines view (list / create / detail). Everything is
 * rendered inside /settings/environments; the detail view is internal
 * component state (no nested route needed). An optional `?daemon=<id>` search
 * param deep-links straight into a detail view (used by the onboarding
 * DaemonConnectingGate "View logs" action). Daemon access tokens are managed
 * in the standalone System → Access Tokens settings section.
 */
import React, { useMemo, useState } from "react";
import { createPortal } from "react-dom";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useSearch } from "@tanstack/react-router";
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
  DaemonStatus,
  PortAccessMode,
  createEnvironment,
  deleteDaemon,
  describeError,
  getComputeSubscription,
  getDaemon,
  listDaemons,
  listPortAccessRules,
  portAccessRulesQueryKey,
  removePortAccess,
  resumeEnvironment,
  setPortAccess,
  suspendDaemon,
  type Daemon,
  type PortAccessRule,
} from "@/services/controlPlane/environments";
import { SelfHostedDaemonConnect } from "@/components/Projects/SelfHostedDaemonConnect";

// ── Query keys ──────────────────────────────────────────────────────────────
const QK = {
  daemons: ["cp", "environments", "list"] as const,
  daemon: (id: string) => ["cp", "environments", "detail", id] as const,
  // Shared with the header DetectedPortsChip's one-click-public toggle so a
  // "Make public" there invalidates this panel's rules query and vice-versa.
  ports: portAccessRulesQueryKey,
  computeSub: ["cp", "environments", "computeSubscription"] as const,
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

// ── Size tiers (plan-gated) ─────────────────────────────────────────────────
const SIZE_TIERS = [
  { value: DaemonSize.SMALL, label: "Small", specs: "1 CPU · 2GB RAM", price: "$0.02/min" },
  { value: DaemonSize.MEDIUM, label: "Medium", specs: "2 CPU · 4GB RAM", price: "$0.04/min" },
  { value: DaemonSize.LARGE, label: "Large", specs: "4 CPU · 8GB RAM", price: "$0.08/min" },
  { value: DaemonSize.XL, label: "XL", specs: "8 CPU · 16GB RAM", price: "$0.16/min" },
] as const;

const sizeLabel: Record<number, string> = {
  [DaemonSize.SMALL]: "Small",
  [DaemonSize.MEDIUM]: "Medium",
  [DaemonSize.LARGE]: "Large",
  [DaemonSize.XL]: "XL",
};

const IDLE_TIMEOUT_OPTIONS = [
  { value: "15m", label: "15 minutes" },
  { value: "30m", label: "30 minutes" },
  { value: "1h", label: "1 hour" },
  { value: "2h", label: "2 hours" },
  { value: "4h", label: "4 hours" },
] as const;

// Maps the lowercase names stored in the plan's `limits` JSON blob
// (allowed_daemon_sizes) to the DaemonSize enum. Kept aligned with the
// backend's checkDaemonSizeAllowed. Returns null when no plan / malformed.
const SIZE_NAME_TO_ENUM: Record<string, DaemonSize> = {
  small: DaemonSize.SMALL,
  medium: DaemonSize.MEDIUM,
  large: DaemonSize.LARGE,
  xl: DaemonSize.XL,
};

function parseAllowedDaemonSizes(rawLimits?: string): DaemonSize[] | null {
  if (!rawLimits) return null;
  try {
    const parsed = JSON.parse(rawLimits) as Record<string, unknown>;
    const sizes = parsed.allowed_daemon_sizes;
    if (!Array.isArray(sizes)) return null;
    const enums: DaemonSize[] = [];
    for (const s of sizes) {
      if (typeof s !== "string") continue;
      const e = SIZE_NAME_TO_ENUM[s.toLowerCase()];
      if (e !== undefined) enums.push(e);
    }
    return enums;
  } catch {
    return null;
  }
}

const accessModeLabel: Record<number, string> = {
  [PortAccessMode.PUBLIC]: "Public",
  [PortAccessMode.AUTHENTICATED]: "Authenticated",
  [PortAccessMode.TOKEN]: "Token",
  [PortAccessMode.UNSPECIFIED]: "Unspecified",
};

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
    <div className="mb-4 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
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
export function EnvironmentsSection() {
  const search = useSearch({ strict: false }) as { daemon?: string };
  // Deep-link: ?daemon=<id> opens the detail view directly.
  const [selectedId, setSelectedId] = useState<string | null>(search.daemon ?? null);

  // Without a control plane there are no managed machines to list, but the
  // self-hosted path is exactly the one that still works — so the setup
  // instructions matter MORE here, not less.
  if (!capabilities.cloudDaemons) {
    return (
      <div className="mx-auto max-w-4xl space-y-6">
        <PageHeader title="Machines" subtitle="Managed and self-hosted machines that run your projects." />
        <EmptyState
          icon={Server}
          title="Machines unavailable"
          description="Machines are managed by the Reliant control plane, which isn't configured for this build. Connect a self-hosted machine to keep working locally."
        />
        <SelfHostedSetupCard />
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-5xl">
      {selectedId ? (
        <EnvironmentDetail daemonId={selectedId} onBack={() => setSelectedId(null)} />
      ) : (
        <div className="space-y-6">
          <div>
            <PageHeader
              title="Machines"
              subtitle="Managed and self-hosted machines that run your projects."
            />
            <EnvironmentsList onOpenDetail={(id) => setSelectedId(id)} />
          </div>
          <SelfHostedSetupCard />
        </div>
      )}
    </div>
  );
}

// ── Machines list + create ──────────────────────────────────────────────────
function EnvironmentsList({ onOpenDetail }: { onOpenDetail: (id: string) => void }) {
  const qc = useQueryClient();
  const [statusFilter, setStatusFilter] = useState<number>(DaemonStatus.UNSPECIFIED);
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<Daemon | null>(null);
  const [actionError, setActionError] = useState("");

  const daemonsQ = useQuery({
    queryKey: QK.daemons,
    queryFn: async () => (await listDaemons()).daemons,
    staleTime: 10_000,
    refetchInterval: 15_000,
  });

  const computeSubQ = useQuery({
    queryKey: QK.computeSub,
    queryFn: () => getComputeSubscription(),
    staleTime: 30_000,
  });
  const allowedSizes = useMemo(
    () => parseAllowedDaemonSizes(computeSubQ.data?.plan?.limits),
    [computeSubQ.data?.plan?.limits],
  );
  const hasActivePlan = !!computeSubQ.data && allowedSizes !== null && allowedSizes.length > 0;

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

  const daemons = (daemonsQ.data ?? []).filter(
    (d) => statusFilter === DaemonStatus.UNSPECIFIED || d.status === statusFilter,
  );

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
        {hasActivePlan ? (
          <Button onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4" /> New Machine
          </Button>
        ) : !computeSubQ.isLoading ? (
          <Badge variant="neutral" label="Subscribe to a compute plan to create machines" />
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
            <p className="text-sm font-medium text-destructive">Failed to load machines</p>
            <p className="mt-1 text-sm text-muted-foreground">{describeError(daemonsQ.error)}</p>
            <Button variant="outline" size="sm" className="mt-3" onClick={() => daemonsQ.refetch()}>
              <RefreshCw className="h-3.5 w-3.5" /> Retry
            </Button>
          </CardContent>
        </Card>
      ) : daemons.length === 0 ? (
        <EmptyState
          icon={Server}
          title="No machines"
          description={
            hasActivePlan
              ? "Create your first cloud machine."
              : "Subscribe to a compute plan, then create a machine. Machines run on the compute sizes your plan allows."
          }
          action={
            hasActivePlan ? (
              <Button onClick={() => setCreateOpen(true)}>
                <Plus className="h-4 w-4" /> New Machine
              </Button>
            ) : undefined
          }
        />
      ) : (
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
              const isSuspended = d.status === DaemonStatus.SUSPENDED;
              const resources =
                [d.resources?.cpuRequest, d.resources?.memoryRequest, d.storageSize]
                  .filter(Boolean)
                  .join(" · ") || "—";
              const busy = suspendMut.isPending || resumeMut.isPending;
              return (
                <Tr key={d.id}>
                  <Td>
                    <button
                      type="button"
                      onClick={() => onOpenDetail(d.id)}
                      className="font-medium text-foreground hover:text-primary hover:underline"
                    >
                      {d.name}
                    </button>
                  </Td>
                  <Td>
                    <StatusDot variant={statusDotVariant[status]} label={badge.label} />
                  </Td>
                  <Td className="text-muted-foreground">{resources}</Td>
                  <Td className="text-muted-foreground">{fmtTimestamp(d.createdAt)}</Td>
                  <Td className="text-right">
                    <div className="inline-flex items-center gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={busy}
                        onClick={() => (isSuspended ? resumeMut.mutate(d.id) : suspendMut.mutate(d.id))}
                      >
                        {isSuspended ? <Play className="h-4 w-4" /> : <Pause className="h-4 w-4" />}
                        {isSuspended ? "Resume" : "Suspend"}
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => setDeleteTarget(d)}>
                        <Trash2 className="h-4 w-4 text-destructive" />
                      </Button>
                    </div>
                  </Td>
                </Tr>
              );
            })}
          </Tbody>
        </Table>
      )}

      <CreateEnvironmentModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        allowedSizes={allowedSizes}
        onCreated={() => { setCreateOpen(false); invalidate(); }}
      />

      <Modal open={deleteTarget !== null} onClose={() => setDeleteTarget(null)} title="Delete Machine">
        <p className="text-sm text-muted-foreground">
          Are you sure you want to delete <span className="font-semibold text-foreground">{deleteTarget?.name}</span>?
          This action cannot be undone.
        </p>
        <div className="mt-6 flex justify-end gap-3">
          <Button variant="outline" onClick={() => setDeleteTarget(null)}>Cancel</Button>
          <Button
            variant="danger"
            isLoading={deleteMut.isPending}
            onClick={() => deleteTarget && deleteMut.mutate(deleteTarget.id)}
          >
            {deleteMut.isPending ? "Deleting…" : "Delete"}
          </Button>
        </div>
      </Modal>
    </div>
  );
}

function CreateEnvironmentModal({
  open,
  onClose,
  allowedSizes,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  allowedSizes: DaemonSize[] | null;
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [gitRepo, setGitRepo] = useState("");
  const [idleTimeout, setIdleTimeout] = useState("30m");
  const [size, setSize] = useState<DaemonSize>(DaemonSize.MEDIUM);
  const [error, setError] = useState("");

  const tiers = useMemo(
    () => (allowedSizes ? SIZE_TIERS.filter((t) => allowedSizes.includes(t.value)) : []),
    [allowedSizes],
  );

  // Keep the selected size within the plan's allowed set.
  const effectiveSize = useMemo(() => {
    if (!allowedSizes || allowedSizes.length === 0) return size;
    return allowedSizes.includes(size) ? size : allowedSizes[0];
  }, [allowedSizes, size]);

  const createMut = useMutation({
    mutationFn: () =>
      createEnvironment({ name: name.trim(), size: effectiveSize, idleTimeout, gitRepo: gitRepo.trim() || undefined }),
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
          <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
            {tiers.map((t) => {
              const selected = effectiveSize === t.value;
              return (
                <button
                  key={t.value}
                  type="button"
                  onClick={() => setSize(t.value)}
                  className={cn(
                    "rounded-lg border-2 p-3 text-left transition-colors",
                    selected ? "border-primary bg-primary/5" : "border-border bg-card hover:border-muted-foreground/40",
                  )}
                >
                  <div className="text-sm font-semibold text-foreground">{t.label}</div>
                  <div className="mt-1 text-xs text-muted-foreground">{t.specs}</div>
                  <div className="mt-1 text-xs font-medium text-primary">{t.price}</div>
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
function InfoRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-4 border-b border-border py-2 last:border-0">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className="text-right text-sm font-medium text-foreground">{value || "—"}</dd>
    </div>
  );
}

function EnvironmentDetail({ daemonId, onBack }: { daemonId: string; onBack: () => void }) {
  const qc = useQueryClient();
  const [error, setError] = useState("");
  const [deleteOpen, setDeleteOpen] = useState(false);

  const daemonQ = useQuery({
    queryKey: QK.daemon(daemonId),
    queryFn: () => getDaemon(daemonId),
    refetchInterval: 15_000,
  });
  const daemon = daemonQ.data?.daemon;
  const workspaceBaseDomain = daemonQ.data?.workspaceBaseDomain ?? "";

  const refetchAll = () => {
    qc.invalidateQueries({ queryKey: QK.daemon(daemonId) });
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

  const status = daemon ? daemonStatus(daemon) : "pending";
  const badge = statusBadge[status];
  const connected = daemon?.status === DaemonStatus.ACTIVE;
  const isSuspended = daemon?.status === DaemonStatus.SUSPENDED;
  const busy = suspendMut.isPending || resumeMut.isPending || deleteMut.isPending;

  return (
    <div className="space-y-6">
      <button
        type="button"
        onClick={onBack}
        className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="h-4 w-4" /> Back to Machines
      </button>

      {daemonQ.isLoading ? (
        <Card><CardContent className="text-sm text-muted-foreground">Loading machine…</CardContent></Card>
      ) : daemonQ.error ? (
        <Card><CardContent className="text-sm text-destructive">{describeError(daemonQ.error)}</CardContent></Card>
      ) : !daemon ? (
        <Card><CardContent className="text-sm text-muted-foreground">Machine not found.</CardContent></Card>
      ) : (
        <>
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="flex flex-wrap items-center gap-3">
              <h2 className="text-xl font-semibold text-foreground">{daemon.name}</h2>
              <Badge label={badge.label} variant={badge.variant} />
              <Badge label={connected ? "Connected" : "Disconnected"} variant={connected ? "success" : "neutral"} />
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button variant="outline" disabled={busy} onClick={() => (isSuspended ? resumeMut.mutate() : suspendMut.mutate())}>
                {isSuspended ? <><Play className="h-4 w-4" /> Resume</> : <><Pause className="h-4 w-4" /> Suspend</>}
              </Button>
              <Button variant="danger" disabled={busy} onClick={() => setDeleteOpen(true)}>
                <Trash2 className="h-4 w-4" /> Delete
              </Button>
            </div>
          </div>

          {error && <ErrorNote message={error} />}

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <Card>
              <CardHeader><CardTitle className="inline-flex items-center gap-2"><Server className="h-4 w-4 text-muted-foreground" /> Overview</CardTitle></CardHeader>
              <CardContent>
                <dl>
                  <InfoRow label="Size" value={sizeLabel[daemon.size] ?? "Custom"} />
                  <InfoRow label="Storage" value={daemon.storageSize} />
                  <InfoRow label="Created" value={fmtTimestamp(daemon.createdAt)} />
                  <InfoRow label="Updated" value={fmtTimestamp(daemon.updatedAt)} />
                </dl>
              </CardContent>
            </Card>
            <Card>
              <CardHeader><CardTitle className="inline-flex items-center gap-2"><Activity className="h-4 w-4 text-muted-foreground" /> Status & Activity</CardTitle></CardHeader>
              <CardContent>
                <dl>
                  <InfoRow label="Connection" value={<Badge label={connected ? "Connected" : "Disconnected"} variant={connected ? "success" : "neutral"} />} />
                  <InfoRow label="Connected at" value={fmtTimestamp(daemon.connectedAt)} />
                  <InfoRow label="Idle timeout" value={daemon.idleTimeout || "Not set"} />
                  <InfoRow label="Last status" value={daemon.lastStatusMessage} />
                </dl>
              </CardContent>
            </Card>
          </div>

          <PortAccessPanel daemonId={daemon.id} workspaceBaseDomain={workspaceBaseDomain} />

          <Modal open={deleteOpen} onClose={() => setDeleteOpen(false)} title="Delete Machine">
            <p className="text-sm text-muted-foreground">
              Are you sure you want to delete <span className="font-semibold text-foreground">{daemon.name}</span>?
              This action cannot be undone.
            </p>
            <div className="mt-6 flex justify-end gap-3">
              <Button variant="outline" onClick={() => setDeleteOpen(false)}>Cancel</Button>
              <Button variant="danger" isLoading={deleteMut.isPending} onClick={() => deleteMut.mutate()}>
                {deleteMut.isPending ? "Deleting…" : "Delete"}
              </Button>
            </div>
          </Modal>
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
                          <Trash2 className="h-4 w-4 text-destructive" /> {removing ? "Removing…" : "Remove"}
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
          <AlertTriangle className="mt-0.5 h-5 w-5 flex-shrink-0 text-warning" />
          <p className="text-sm text-foreground">Copy this token now. You won't be able to see it again.</p>
        </div>
        <div className="flex items-center gap-2">
          <code className="flex-1 overflow-x-auto rounded-md bg-muted px-3 py-2 font-mono text-sm text-foreground">{token}</code>
          <Button variant="outline" onClick={copy}>
            {copied ? <><Check className="h-4 w-4 text-success" /> Copied</> : <><Copy className="h-4 w-4" /> Copy</>}
          </Button>
        </div>
        <div className="flex justify-end">
          <Button onClick={() => { setCopied(false); onClose(); }}>Done</Button>
        </div>
      </div>
    </Modal>
  );
}