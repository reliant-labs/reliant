/**
 * Add machine — the ONE way the app adds a machine.
 *
 * Every place that needs a machine opens this sheet: the no-machine state on
 * files, terminal, search and changes; the chat's Connect a machine dialog; the
 * "No machine yet" wait state; the project picker; Settings → Machines;
 * mobile; and (mounted by their owners) the global window and the automation
 * dialog. Five separate create flows, with five sets of defaults, names and
 * payment rules, used to live in those places; one of them created nothing
 * and another could not add a second machine.
 *
 * The sheet, in order:
 *
 *   choose     "Your own computer — Free", then every cloud size the catalog
 *              sells. Small (or the caller's suggestion) is preselected.
 *              Sizes above the plan are upgrade rows, not hidden. Name
 *              (unique, generated), auto-suspend and repository sit under
 *              Advanced.
 *   pay        Only when the size is not on the plan: the embedded compute
 *              checkout, in place. Never a redirect to billing.
 *   paid       "Your plan is active" and ONE explicit button that creates the
 *              machine. A billable create fires only on a person's click,
 *              never automatically after a payment settles.
 *   creating → provisioning
 *              The machine's own startup phases, with "Keep working" to
 *              close the sheet while it comes up.
 *   pair       Your own computer: device-code pairing (PairMachinePanel).
 *
 * When the machine is ready, `onReady` hands it back to the caller so the
 * action that needed a machine completes: the chat attaches, the panel loads,
 * the automation selects it. Callers keep the sheet mounted while it is
 * closed, so a machine still provisioning after "Keep working" reports in.
 */
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { ArrowLeft, CheckCircle2, ChevronDown, Github, Laptop, X } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import Button from "@/components/forge-ui/button";
import { cn } from "@/lib/utils";
import { toast } from "@/lib/toast-manager";
import { capabilities } from "@/services/controlPlane/capabilities";
import { getComputeEligibility } from "@/services/controlPlane/billing";
import type { GitRepo } from "@/services/controlPlane/git";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { invalidateDaemonList } from "@/hooks/daemon-registry-queries";
import { computeEligibilityQueryKey } from "@/hooks/computeEligibilityKey";
import { usePlans, useComputeSubscription, cloudBillingKeys } from "@/hooks/useCloudBillingQueries";
import { useAccountGate } from "@/hooks/useAccountGate";
import { classifyDaemonWait } from "@/lib/daemon-wait";
import { cloudPathForRepo } from "@/lib/cloudProjectPath";
import { isPurchasableComputePlan, sortPlansForDisplay, formatCentsAsDollars, type DaemonSizeName } from "@/components/Settings/cloud/billingUtils";
import { createMachine } from "@/components/Settings/cloud/createMachine";
import { describeError } from "@/services/controlPlane/environments";
import { deriveMachineOptions } from "@/components/Billing/machineOptions";
import { ComputeSubscriptionCheckout } from "@/components/Billing/ComputeSubscriptionCheckout";
import { LinkIdentityModal } from "@/components/Billing/LinkIdentityModal";
import { DaemonWaitState } from "@/components/DaemonWaitState";

import { PairMachinePanel } from "./PairMachinePanel";
import {
  cloudMachineRows,
  DAEMON_SIZE_BY_NAME,
  defaultMachineName,
  defaultRow,
  MACHINE_NAME_RULE,
  type CloudMachineRow,
} from "./addMachine";

const RepoSelector = React.lazy(() =>
  import("@/components/Projects/RepoSelector").then((m) => ({ default: m.RepoSelector })),
);

const IDLE_TIMEOUT_OPTIONS = [
  { value: "15m", label: "15 minutes" },
  { value: "30m", label: "30 minutes" },
  { value: "1h", label: "1 hour" },
  { value: "2h", label: "2 hours" },
  { value: "4h", label: "4 hours" },
] as const;

export interface ReadyMachine {
  daemonId: string;
  name: string;
}

export interface AddMachineSheetProps {
  open: boolean;
  onClose: () => void;
  /** The new machine, once it is connected and can run work. */
  onReady?: (machine: ReadyMachine) => void;
  /** Preselected size, e.g. the size the project last ran on. Default Small. */
  suggestedSize?: DaemonSizeName;
  /** Preselected repository, when the caller is a project. */
  repo?: GitRepo | null;
  /** Start on "Your own computer". */
  initialKind?: "cloud" | "self";
  title?: string;
}

type Step =
  | { kind: "choose" }
  | { kind: "pay" }
  | { kind: "paid" }
  | { kind: "creating" }
  | { kind: "provisioning"; daemonId: string; name: string; startedAt: number }
  | { kind: "pair" };

/**
 * Mounted everywhere, but does nothing until it is opened: the catalog,
 * eligibility and subscription reads start when the sheet does. It stays
 * live after closing only while a machine it created is still coming up.
 */
export function AddMachineSheet(props: AddMachineSheetProps) {
  const [watching, setWatching] = useState(false);
  if (!props.open && !watching) return null;
  return <AddMachineSheetBody {...props} onWatchingChange={setWatching} />;
}

function AddMachineSheetBody({
  open,
  onClose,
  onReady,
  suggestedSize,
  repo: callerRepo = null,
  initialKind,
  title = "Add a machine",
  onWatchingChange,
}: AddMachineSheetProps & { onWatchingChange: (watching: boolean) => void }) {
  const cloud = capabilities.cloudDaemons;
  const qc = useQueryClient();
  const { daemons } = useDaemonStatus();

  // ── What may be started, and on what terms ───────────────────────────────
  const plansQ = usePlans({ enabled: cloud && open });
  const subQ = useComputeSubscription();
  const eligibilityQ = useQuery({
    queryKey: computeEligibilityQueryKey,
    queryFn: () => getComputeEligibility(),
    staleTime: 30_000,
    enabled: cloud && open,
  });
  const hasSubscription = Boolean(subQ.data?.subscription);
  const rows = useMemo(() => {
    const plans = sortPlansForDisplay((plansQ.data?.plans ?? []).filter(isPurchasableComputePlan));
    const options = deriveMachineOptions(plans, plansQ.data?.daemonPricing);
    return cloudMachineRows(options, eligibilityQ.data?.allowedDaemonSizes ?? [], hasSubscription);
  }, [plansQ.data, eligibilityQ.data?.allowedDaemonSizes, hasSubscription]);
  const catalogLoading = cloud && (plansQ.isLoading || eligibilityQ.isLoading);

  // ── The person's choices ─────────────────────────────────────────────────
  const [kind, setKind] = useState<"cloud" | "self">(initialKind ?? (cloud ? "cloud" : "self"));
  const [size, setSize] = useState<DaemonSizeName | undefined>(undefined);
  const row = rows.find((r) => r.size === size) ?? defaultRow(rows, suggestedSize);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [typedName, setTypedName] = useState<string | null>(null);
  const [idleTimeout, setIdleTimeout] = useState("30m");
  const [repo, setRepo] = useState<GitRepo | null>(callerRepo);
  const [pickingRepo, setPickingRepo] = useState(false);
  const takenNames = daemons.map((d) => d.name);
  const generatedName = defaultMachineName(row?.size ?? "m", takenNames, repo?.cloneUrl);
  const name = typedName ?? generatedName;
  const nameValid = MACHINE_NAME_RULE.test(name);

  const [step, setStep] = useState<Step>({ kind: "choose" });
  const [error, setError] = useState("");
  const [linkAttempt, setLinkAttempt] = useState(0);
  const accountGate = useAccountGate();

  // A fresh open starts fresh — unless a machine is still coming up, which
  // the sheet keeps watching for its caller.
  const wasOpen = useRef(open);
  useEffect(() => {
    if (open && !wasOpen.current && step.kind !== "creating" && step.kind !== "provisioning") {
      setKind(initialKind ?? (cloud ? "cloud" : "self"));
      setSize(undefined);
      setTypedName(null);
      setIdleTimeout("30m");
      setRepo(callerRepo);
      setPickingRepo(false);
      setAdvancedOpen(false);
      setStep({ kind: "choose" });
      setError("");
    }
    wasOpen.current = open;
  }, [open, step.kind, initialKind, cloud, callerRepo]);

  // ── Ready: hand the machine back, once ───────────────────────────────────
  const reportedRef = useRef<string | null>(null);
  const finish = useCallback(
    (machine: ReadyMachine) => {
      if (reportedRef.current === machine.daemonId) return;
      reportedRef.current = machine.daemonId;
      onReady?.(machine);
      if (!open) toast.success(`${machine.name} is ready`);
      setStep({ kind: "choose" });
      onClose();
    },
    [onReady, onClose, open],
  );

  const provisioning = step.kind === "provisioning" ? step : null;
  const inFlight = step.kind === "creating" || step.kind === "provisioning";
  useEffect(() => onWatchingChange(inFlight), [inFlight, onWatchingChange]);
  const provisioned = provisioning ? daemons.find((d) => d.daemonId === provisioning.daemonId) : undefined;
  useEffect(() => {
    if (provisioning && provisioned?.status === DaemonStatus.ACTIVE) {
      finish({ daemonId: provisioning.daemonId, name: provisioning.name });
    }
  }, [provisioning, provisioned?.status, finish]);

  // ── Create ───────────────────────────────────────────────────────────────
  const create = async (attempt = 0): Promise<void> => {
    if (!row) return;
    setError("");
    setStep({ kind: "creating" });
    const machineName = attempt === 0 ? name : defaultMachineName(row.size, [...takenNames, name], repo?.cloneUrl);
    try {
      const result = await createMachine({
        machine: { name: machineName, size: DAEMON_SIZE_BY_NAME[row.size], idleTimeout },
        repo,
      });
      if (result.repo.kind === "failed") {
        toast.error(`${result.machineName} was created, but ${result.repo.projectName} could not be added: ${result.repo.message}`);
      }
      invalidateDaemonList(qc);
      if (!result.daemonId) {
        setStep({ kind: "choose" });
        setError("The machine was created, but its id was not returned. It will appear in Settings → Machines.");
        return;
      }
      setStep({ kind: "provisioning", daemonId: result.daemonId, name: result.machineName, startedAt: Date.now() });
    } catch (err) {
      if (accountGate.handleError(err, () => void create(attempt))) {
        setStep({ kind: "choose" });
        return;
      }
      // A generated name another tab took a moment ago: take the next one.
      // A name the person typed is theirs to change.
      if (typedName === null && attempt === 0 && ConnectError.from(err).code === Code.AlreadyExists) {
        await create(1);
        return;
      }
      setStep({ kind: row.terms === "included" ? "choose" : "paid" });
      setError(describeError(err, "Could not create the machine"));
    }
  };

  const primary = (): { label: string; run: () => void; disabled?: boolean } => {
    if (kind === "self") return { label: "Continue", run: () => setStep({ kind: "pair" }) };
    if (!row) return { label: "Create machine", run: () => undefined, disabled: true };
    if (row.terms !== "included") {
      return { label: "Continue to payment", run: () => setStep({ kind: "pay" }), disabled: !row.plan };
    }
    return {
      label: `Create ${name}`,
      run: () => void accountGate.run(() => create()),
      disabled: !nameValid,
    };
  };

  // Has the plan for the chosen size landed, according to the SERVER?
  const sizeHasLanded = useCallback(async () => {
    if (!row) return false;
    try {
      const { data } = await eligibilityQ.refetch();
      void qc.invalidateQueries({ queryKey: cloudBillingKeys.computeSubscription });
      return (data?.allowedDaemonSizes ?? []).map((s) => s.toLowerCase()).includes(row.size);
    } catch {
      return false;
    }
  }, [row, eligibilityQ, qc]);

  // ── Render ───────────────────────────────────────────────────────────────
  const body = (() => {
    switch (step.kind) {
      case "pay":
        return row?.plan ? (
          <div className="space-y-3">
            <BackButton onClick={() => setStep({ kind: "choose" })} />
            <ComputeSubscriptionCheckout
              key={`compute:${linkAttempt}`}
              plans={rows.filter((r) => r.plan).map((r) => r.plan!)}
              selectedPlanId={row.plan.planId}
              onSelectPlan={(option) => option.size && setSize(option.size)}
              currentPlanId={subQ.data?.subscription?.plan?.id ?? null}
              confirmSettlement={sizeHasLanded}
              onDone={() => setStep({ kind: "paid" })}
              renderIdentityRequired={(message) => (
                <LinkIdentityModal
                  message={message}
                  onLinked={() => setLinkAttempt((n) => n + 1)}
                  onDismiss={() => setStep({ kind: "choose" })}
                />
              )}
            />
          </div>
        ) : null;
      case "paid":
        return (
          <div className="space-y-4" data-testid="add-machine-paid">
            <div className="flex items-start gap-3 rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4">
              <CheckCircle2 className="mt-0.5 h-4 w-4 text-emerald-500" aria-hidden="true" />
              <div>
                <p className="text-sm font-medium text-foreground">Your plan is active</p>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  {row ? `${row.label} machines are on your plan now.` : "Your plan covers this machine now."} Nothing
                  has been started yet.
                </p>
              </div>
            </div>
            {error && <p className="text-xs text-destructive-ink">{error}</p>}
            <Button className="w-full" onClick={() => void accountGate.run(() => create())} disabled={!nameValid}>
              {`Create ${name}`}
            </Button>
          </div>
        );
      case "creating":
        return <p className="py-6 text-center text-sm text-muted-foreground">Creating {name}…</p>;
      case "provisioning": {
        const wait = classifyDaemonWait({
          daemon: provisioned ?? null,
          elapsedMs: Date.now() - step.startedAt,
          isCloud: true,
        });
        return (
          <div className="space-y-4">
            <DaemonWaitState
              state={{ ...wait, showManage: false }}
              variant="panel"
              className="h-auto rounded-lg border border-border/50 bg-background p-4"
            />
            <div className="flex justify-end">
              <Button variant="ghost" onClick={onClose}>
                Keep working
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              We&apos;ll tell you when {step.name} is ready. It also appears in Settings → Machines.
            </p>
          </div>
        );
      }
      case "pair":
        return (
          <div className="space-y-3">
            <BackButton onClick={() => setStep({ kind: "choose" })} />
            <PairMachinePanel
              mode="add"
              onConnected={(daemonId) => {
                const machine = daemons.find((d) => d.daemonId === daemonId);
                finish({ daemonId, name: machine?.name || machine?.hostname || "Your machine" });
              }}
            />
          </div>
        );
      default:
        return (
          <ChooseStep
            cloud={cloud}
            loading={catalogLoading}
            rows={rows}
            kind={kind}
            selectedSize={row?.size}
            onSelectSelf={() => setKind("self")}
            onSelectSize={(s) => {
              setKind("cloud");
              setSize(s);
            }}
            advancedOpen={advancedOpen}
            onToggleAdvanced={() => setAdvancedOpen((v) => !v)}
            advanced={
              <div className="space-y-4">
                <label className="block space-y-1">
                  <span className="text-xs font-medium text-foreground">Name</span>
                  <input
                    value={name}
                    onChange={(e) => setTypedName(e.target.value.toLowerCase())}
                    className="w-full rounded-md border border-border bg-background px-3 py-1.5 text-sm text-foreground"
                    aria-invalid={!nameValid}
                  />
                  {!nameValid && (
                    <span className="block text-xs text-destructive-ink">
                      Lowercase letters, numbers and hyphens; start and end with a letter or number.
                    </span>
                  )}
                </label>
                <label className="block space-y-1">
                  <span className="text-xs font-medium text-foreground">Auto-suspend after inactivity</span>
                  <select
                    value={idleTimeout}
                    onChange={(e) => setIdleTimeout(e.target.value)}
                    className="w-full rounded-md border border-border bg-background px-3 py-1.5 text-sm text-foreground"
                  >
                    {IDLE_TIMEOUT_OPTIONS.map((o) => (
                      <option key={o.value} value={o.value}>
                        {o.label}
                      </option>
                    ))}
                  </select>
                  <span className="block text-xs text-muted-foreground">A suspended machine is not billed for compute.</span>
                </label>
                <div className="space-y-1">
                  <span className="text-xs font-medium text-foreground">Repository</span>
                  {pickingRepo ? (
                    <React.Suspense fallback={<p className="py-3 text-sm text-muted-foreground">Loading repositories…</p>}>
                      <RepoSelector
                        onSelect={(picked) => {
                          setRepo(picked);
                          setPickingRepo(false);
                        }}
                        analyticsPhase="create_machine"
                      />
                    </React.Suspense>
                  ) : repo ? (
                    <div className="flex items-center gap-2 rounded-md border border-border bg-background px-3 py-2">
                      <Github className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm text-foreground">{repo.fullName}</div>
                        <div className="truncate text-xs text-muted-foreground">Cloned to {cloudPathForRepo(repo)}</div>
                      </div>
                      <Button size="sm" variant="ghost" icon={<X />} aria-label="Remove repository" onClick={() => setRepo(null)} />
                    </div>
                  ) : (
                    <div className="flex items-center justify-between gap-2">
                      <span className="text-xs text-muted-foreground">None — add projects to it later.</span>
                      <Button size="sm" variant="outline" leftIcon={<Github />} onClick={() => setPickingRepo(true)}>
                        Choose repository
                      </Button>
                    </div>
                  )}
                </div>
              </div>
            }
          />
        );
    }
  })();

  const action = step.kind === "choose" ? primary() : null;

  return (
    <>
      <Modal
        open={open}
        onClose={onClose}
        title={step.kind === "pair" ? "Connect your own computer" : title}
        size="lg"
        footer={
          action ? (
            <div className="flex items-center justify-between gap-2">
              {error ? <p className="text-xs text-destructive-ink">{error}</p> : <span />}
              <div className="flex gap-2">
                <Button variant="ghost" onClick={onClose}>
                  Cancel
                </Button>
                <Button onClick={action.run} disabled={action.disabled} data-testid="add-machine-primary">
                  {action.label}
                </Button>
              </div>
            </div>
          ) : undefined
        }
      >
        <div className="px-6 py-4">{body}</div>
      </Modal>
      {accountGate.modal}
    </>
  );
}

function BackButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="inline-flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
    >
      <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
      Back
    </button>
  );
}

function ChooseStep({
  cloud,
  loading,
  rows,
  kind,
  selectedSize,
  onSelectSelf,
  onSelectSize,
  advancedOpen,
  onToggleAdvanced,
  advanced,
}: {
  cloud: boolean;
  loading: boolean;
  rows: CloudMachineRow[];
  kind: "cloud" | "self";
  selectedSize?: DaemonSizeName;
  onSelectSelf: () => void;
  onSelectSize: (size: DaemonSizeName) => void;
  advancedOpen: boolean;
  onToggleAdvanced: () => void;
  advanced: React.ReactNode;
}) {
  return (
    <div className="space-y-4">
      <div role="radiogroup" aria-label="Machine" className="space-y-2">
        <MachineRowButton
          selected={kind === "self"}
          onSelect={onSelectSelf}
          label={
            <span className="inline-flex items-center gap-2">
              <Laptop className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
              Your own computer
            </span>
          }
          detail="A laptop or server you run Reliant on"
          right={<span className="text-sm font-semibold text-foreground">Free</span>}
        />
        {cloud &&
          (loading ? (
            <p className="px-1 py-2 text-sm text-muted-foreground">Loading machine sizes…</p>
          ) : (
            rows.map((r) => (
              <MachineRowButton
                key={r.size}
                selected={kind === "cloud" && r.size === selectedSize}
                onSelect={() => onSelectSize(r.size)}
                label={r.label}
                detail={r.detail}
                right={<RowTerms row={r} />}
              />
            ))
          ))}
      </div>

      {kind === "cloud" && (
        <div className="rounded-lg border border-border/60">
          <button
            type="button"
            onClick={onToggleAdvanced}
            aria-expanded={advancedOpen}
            className="flex w-full items-center justify-between px-3 py-2 text-xs font-medium text-muted-foreground hover:text-foreground"
          >
            Advanced: name, auto-suspend, repository
            <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", advancedOpen && "rotate-180")} />
          </button>
          {advancedOpen && <div className="border-t border-border/60 p-3">{advanced}</div>}
        </div>
      )}
    </div>
  );
}

function RowTerms({ row }: { row: CloudMachineRow }) {
  if (row.terms === "included") {
    return (
      <span className="rounded-full bg-primary/10 px-2 py-0.5 text-2xs font-semibold uppercase tracking-wider text-primary">
        On your plan
      </span>
    );
  }
  const price = row.plan ? `${formatCentsAsDollars(row.plan.monthlyPriceCents)}/mo` : "";
  return (
    <span className="text-right">
      <span className="block text-sm font-semibold text-foreground">{price}</span>
      {row.terms === "upgrade" && <span className="block text-2xs text-muted-foreground">upgrades your plan</span>}
    </span>
  );
}

function MachineRowButton({
  selected,
  onSelect,
  label,
  detail,
  right,
}: {
  selected: boolean;
  onSelect: () => void;
  label: React.ReactNode;
  detail: string;
  right: React.ReactNode;
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={cn(
        "flex w-full items-center justify-between gap-4 rounded-lg border px-4 py-3 text-left transition-colors",
        selected ? "border-primary bg-primary/10" : "border-border bg-background hover:border-primary/40 hover:bg-muted/50",
      )}
    >
      <span className="min-w-0">
        <span className="block text-sm font-semibold text-foreground">{label}</span>
        <span className="block text-xs text-muted-foreground">{detail}</span>
      </span>
      <span className="flex-shrink-0">{right}</span>
    </button>
  );
}
