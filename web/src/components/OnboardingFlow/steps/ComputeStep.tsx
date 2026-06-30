import { useEffect, useRef, useState } from "react";
import { Check, Cloud, Loader2, Monitor } from "lucide-react";
import { cn } from "@/lib/utils";
import { getIsDev } from "@/lib/constants";
import {
  DaemonStatus,
  type DaemonInfo,
} from "@/gen/reliant/v1/daemon_registry_pb";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useEventBus } from "@/lib/event-context";
import {
  useCloudEligibility,
  useCreateDaemon,
  useResumeDaemon,
  isReasonedQuotaError,
} from "@/hooks/useOnboardingQueries";
import type {
  CodeSource,
  ComputeChoice,
  OnboardingIntent,
  StepProps,
} from "../types";
import { DaemonConnectionDiagrams } from "../DaemonConnectionDiagrams";
import { SelfHostedDaemonConnect } from "@/components/Projects/SelfHostedDaemonConnect";
import { trackEvent } from "@/lib/analytics";
import { capabilities } from "@/services/controlPlane/capabilities";

const HAS_CLOUD_DAEMONS = capabilities.cloudDaemons;
const DAEMON_TYPE_MANAGED = 1;
const DAEMON_SIZE_SMALL = 1;

// The "where do you want to run your daemon?" step is bootstrap-only: it
// disambiguates a brand-new user between "Reliant Cloud" and "your own
// machine." Once a daemon — local OR managed — is registered to the user's
// account, that bootstrap question is moot. The post-onboarding workspace
// picker handles selection when multiple daemons exist.
//
// We treat ACTIVE and IDLE as "usable":
//   - ACTIVE: actively connected (the obvious case).
//   - IDLE:   registered but currently transitioning (cloud daemon
//             provisioning, local daemon just-came-up between gateway
//             reconnect attempts). The user has clearly already picked a
//             daemon location, so the where-question shouldn't re-appear.
// DISCONNECTED / UNSPECIFIED are skipped: those signal a stale row or an
// unhealthy registration where the user genuinely needs to (re)decide.
export function hasUsableDaemonForOnboarding(daemons: DaemonInfo[]): boolean {
  return daemons.some(
    (d) =>
      d.status === DaemonStatus.ACTIVE || d.status === DaemonStatus.IDLE,
  );
}

// Derives the code-source classification for a given compute + intent. The
// result is no longer stored on the plan (the wizard never branched on it);
// it's computed on demand for analytics (see analytics.markOnboardingFinalized).
export function codeSourceForCompute(
  compute: ComputeChoice,
  intent: OnboardingIntent | undefined,
): CodeSource {
  if (intent === "existing_codebase") {
    return compute === "cloud_free_trial" ? "github_repo" : "local_folder";
  }
  return "new_project";
}

export function ComputeStep({
  plan,
  updatePlan,
  onNext,
}: StepProps & { hideHeader?: boolean }) {
  const [showLocal, setShowLocal] = useState(plan.compute === "local_daemon");
  const [error, setError] = useState<string | null>(null);
  const { activeDaemon, daemons, loading: daemonLoading } = useDaemonStatus();
  const events = useEventBus();
  const hasAdvanced = useRef(false);
  const hasTrackedConnectedRef = useRef(false);

  // Cloud eligibility via React Query
  const isDev = getIsDev();
  const {
    eligible: cloudEligible,
    reason: cloudReason,
    isLoading: cloudLoading,
  } = useCloudEligibility();
  const eligible = isDev ? true : cloudEligible;
  const reason = isDev ? null : cloudReason;
  const loading = isDev ? false : cloudLoading;

  // Daemon mutations via React Query. We use mutateAsync below because the
  // surrounding handler needs to sequence listDaemons → resume/create →
  // updatePlan; the hooks' default-onError still suppresses reasoned-quota
  // errors from the toast/banner side, but mutateAsync rejects regardless,
  // so the outer catch must consult isReasonedQuotaError before surfacing.
  const createDaemonMutation = useCreateDaemon();
  const resumeDaemonMutation = useResumeDaemon();

  // `isStartingCloud` covers the gap between click and the first mutation
  // firing — dynamic chunk imports + the listDaemons() round-trip can take
  // 100ms–2s on a cold load, during which the button would otherwise look
  // idle and invite a second click.
  const [isStartingCloud, setIsStartingCloud] = useState(false);
  const startingCloud = isStartingCloud || createDaemonMutation.isPending;

  const handleCloud = async () => {
    if (!HAS_CLOUD_DAEMONS) return;
    if (isStartingCloud) return;

    setError(null);
    setShowLocal(false);
    setIsStartingCloud(true);
    try {
      // Resolve the right path for this user. If a daemon already exists
      // (e.g. from a prior session) we either reuse it or resume it — the
      // server-side CreateDaemon is NOT a wake-up for a suspended workspace,
      // so calling it again leaves a suspended daemon suspended.
      const { listDaemons, hasActiveDaemon } =
        await import("@/services/controlPlane/daemon");
      const existing = await listDaemons();
      const daemons = existing.daemons;

      let needsProvisioning = true;

      if (hasActiveDaemon(daemons)) {
        // Active daemon already present — nothing to do.
        needsProvisioning = false;
      } else if (daemons.length > 0) {
        // Daemon exists but isn't active (suspended / disconnected / failed).
        // Resume it. Resume failure is non-fatal — the user can retry from
        // the chat banner; we still proceed to the provisioning UI so the
        // app waits for state to settle.
        const daemonId = daemons[0]?.id ?? "";
        if (daemonId) {
          try {
            await resumeDaemonMutation.mutateAsync(daemonId);
          } catch {
            // Surface as a soft error via the provisioning UI rather than
            // blocking onboarding. The hook already routes reasoned-quota
            // errors into the upgrade modal.
          }
        }
      } else {
        // No daemons → create one. createDaemon may itself 409 if another
        // tab raced us; treat "already exists" as a resume.
        try {
          await createDaemonMutation.mutateAsync({
            name: "onboarding-daemon",
            daemonType: DAEMON_TYPE_MANAGED,
            size: DAEMON_SIZE_SMALL,
            gitRepo: "",
            gitBranch: "main",
          });
        } catch (err) {
          const msg = err instanceof Error ? err.message.toLowerCase() : "";
          if (
            msg.includes("plan limit") ||
            msg.includes("already") ||
            msg.includes("exists")
          ) {
            const fallback = await listDaemons();
            const fallbackId = fallback.daemons[0]?.id ?? "";
            if (fallbackId) {
              try {
                await resumeDaemonMutation.mutateAsync(fallbackId);
              } catch {
                /* non-fatal */
              }
            }
          } else {
            throw err;
          }
        }
      }

      await updatePlan({
        compute: "cloud_free_trial",
        daemonProvisioning: needsProvisioning,
        localPath: undefined,
        projectName: undefined,
      });
      trackEvent("onboarding_compute_selected", { compute: "cloud" });
      onNext();
    } catch (err) {
      // Reasoned-quota errors are already routed into the global
      // UpgradeRequiredModal by upgradeInterceptor — don't double-surface as
      // an inline error or toast.
      if (isReasonedQuotaError(err)) {
        return;
      }
      const msg =
        err instanceof Error ? err.message : "Failed to start hosted daemon";
      setError(msg);
      events.emit("toast:show", { message: msg, variant: "error" });
    } finally {
      setIsStartingCloud(false);
    }
  };

  // Clicking "I'll connect my own" only flips local UI state — it does NOT
  // commit `plan.compute` yet. If we set it here, `deriveStep` would see
  // compute set + modelProvider unset and immediately route the user to the
  // model step, skipping the download/connect instructions that render below
  // when `showLocal && !activeDaemon`. We commit compute once the daemon
  // actually connects (the useEffect below) or via the explicit Continue
  // button when a daemon is already running.
  const handleLocal = () => {
    setError(null);
    setShowLocal(true);
  };

  const commitLocalAndAdvance = async (daemonPreconnected: boolean) => {
    if (hasAdvanced.current) return;
    hasAdvanced.current = true;
    await updatePlan({
      compute: "local_daemon",
      daemonProvisioning: false,
      localPath: undefined,
      projectName: undefined,
    });
    trackEvent("onboarding_compute_selected", {
      compute: "local",
      daemon_preconnected: daemonPreconnected,
    });
    onNext();
  };

  const handleLocalContinue = async () => {
    await commitLocalAndAdvance(Boolean(activeDaemon));
  };

  // Auto-skip the compute step whenever the user already has a usable
  // daemon registered. Cases this covers:
  //   1. Initial mount with a daemon already running (Electron's main
  //      process auto-starts the daemon on first launch; users on the web
  //      may also have one from a prior session).
  //   2. User clicked "I'll connect my own", followed the instructions,
  //      and their newly-started daemon just connected.
  //   3. The cloud-dev `make dev-electron` pairing: the Electron-spawned
  //      local daemon registers with the control-plane right after sign-in.
  //      Its lifecycle status flips ACTIVE the moment the NATS connect
  //      event lands; in the gap between registration and that flip it
  //      sits at IDLE — we still treat it as "user has a daemon" so the
  //      bootstrap "where" question doesn't briefly appear and then vanish.
  // The `!startingCloud` guard avoids racing handleCloud: while the cloud
  // flow is running, an already-usable daemon (e.g. a stale local one)
  // must not pre-empt the cloud commit.
  //
  // We wait for `!daemonLoading` so the initial in-flight ListDaemons
  // doesn't briefly read as "no daemons" and let the user click through
  // the prompt before detection settles.
  const hasUsableDaemon = hasUsableDaemonForOnboarding(daemons);
  useEffect(() => {
    if (daemonLoading) return;
    if (!hasUsableDaemon) return;
    if (startingCloud) return;
    if (hasAdvanced.current) return;
    if (!hasTrackedConnectedRef.current) {
      hasTrackedConnectedRef.current = true;
      trackEvent("onboarding_daemon_connected");
    }
    void commitLocalAndAdvance(Boolean(activeDaemon));
    // commitLocalAndAdvance closes over updatePlan / onNext / activeDaemon,
    // but the hasAdvanced ref guards against re-entry,
    // so we intentionally narrow the dep list to the trigger conditions.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasUsableDaemon, daemonLoading, startingCloud]);

  // Block the form behind a deterministic loading state until the FIRST
  // listDaemons settle. Otherwise the radio + Continue button render while
  // the query is still in-flight, and a fast user can click through and set
  // hasAdvanced=true before the auto-skip effect ever evaluates
  // hasUsableDaemonForOnboarding(daemons).
  //
  // We key on `daemonLoading` (= TanStack's `isLoading`), which is true ONLY
  // during the initial fetch and flips to false on the first settle.
  // `isFetching` would also be true during the 5s background polls; gating
  // on it would remount this UI every poll cycle and flicker the form.
  // Visual pattern mirrors DaemonConnectingGate's "connecting" phase
  // (centered spinner in a tinted circle + headline) so the two onboarding
  // wait states feel consistent.
  if (daemonLoading) {
    return (
      <div
        className="space-y-5 py-6 text-center"
        role="status"
        aria-live="polite"
        data-testid="compute-step-loading"
      >
        <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-primary/15 text-primary">
          <Loader2 className="h-7 w-7 animate-spin" />
        </div>
        <div className="space-y-1">
          <h2 className="text-sm font-medium text-foreground">
            Checking your workspace…
          </h2>
          <p className="text-xs text-muted-foreground">
            One moment while we look for an existing daemon.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {!showLocal && <DaemonConnectionDiagrams />}

      <div className="grid gap-3 sm:grid-cols-2">
        <div
          className={cn(
            "flex min-w-0 flex-col gap-4 rounded-xl border-2 p-5 text-left transition-all",
            plan.compute === "cloud_free_trial"
              ? "border-primary bg-primary/10"
              : "border-primary/25 bg-primary/5",
            !HAS_CLOUD_DAEMONS && "border-border/50 bg-muted/30 opacity-80",
          )}
        >
          <div className="flex min-w-0 items-start gap-4">
            <div className="flex-shrink-0 rounded-lg bg-primary/15 p-2.5 text-primary">
              {startingCloud ? (
                <Loader2 className="h-6 w-6 animate-spin" />
              ) : (
                <Cloud className="h-6 w-6" />
              )}
            </div>
            <div className="min-w-0 space-y-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-semibold text-foreground">
                  Reliant Cloud
                </span>
                <span className="rounded bg-primary/20 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wider text-primary">
                  Fastest
                </span>
              </div>
              <span className="block text-xs leading-relaxed text-muted-foreground">
                {HAS_CLOUD_DAEMONS
                  ? "Start a hosted daemon now. If provisioning takes a few minutes, Reliant will continue setup and connect when it is ready."
                  : "Cloud daemons are not enabled for this environment."}
              </span>
            </div>
          </div>

          <button
            type="button"
            onClick={handleCloud}
            disabled={
              startingCloud || !HAS_CLOUD_DAEMONS || !eligible || loading
            }
            className={cn(
              "inline-flex w-full items-center justify-center gap-2 rounded-lg px-4 py-2.5 text-sm font-semibold transition-colors",
              startingCloud || !HAS_CLOUD_DAEMONS || !eligible || loading
                ? "cursor-not-allowed bg-muted text-muted-foreground"
                : "bg-sky-600 text-white shadow-sm shadow-sky-600/20 hover:bg-sky-500",
            )}
          >
            {(startingCloud || loading) && (
              <Loader2 className="h-4 w-4 animate-spin" />
            )}
            {startingCloud
              ? "Requesting daemon..."
              : loading
                ? "Checking availability..."
                : "Start cloud daemon"}
          </button>
          {(!HAS_CLOUD_DAEMONS || (!eligible && reason)) && (
            <div className="space-y-1.5">
              <p className="text-xs leading-relaxed text-muted-foreground">
                {!HAS_CLOUD_DAEMONS
                  ? 'Cloud daemons are unavailable because this environment is not configured for cloud mode. Choose "I\'ll connect my own" to continue.'
                  : reason}
              </p>
              {HAS_CLOUD_DAEMONS && !eligible && (
                <a
                  href="https://reliantlabs.io/pricing"
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1 text-xs font-medium text-sky-500 transition-colors hover:text-sky-400"
                >
                  View plans &rarr;
                </a>
              )}
            </div>
          )}
        </div>

        <button
          type="button"
          onClick={handleLocal}
          className={cn(
            "flex min-w-0 items-start gap-4 rounded-xl border-2 p-5 text-left transition-all",
            "hover:border-primary/50 hover:bg-muted/50",
            showLocal
              ? "border-primary bg-primary/10"
              : "border-border/50 bg-background",
          )}
        >
          <div className="flex-shrink-0 rounded-lg bg-muted p-2.5 text-muted-foreground">
            <Monitor className="h-6 w-6" />
          </div>
          <div className="min-w-0 space-y-1">
            <span className="block text-sm font-semibold text-foreground">
              I'll connect my own
            </span>
            <span className="block text-xs leading-relaxed text-muted-foreground">
              Run the daemon on a laptop or server that can access the directory
              you choose.
            </span>
          </div>
        </button>
      </div>

      {error && <p className="text-center text-xs text-destructive">{error}</p>}

      {showLocal && activeDaemon && (
        <div className="space-y-3 rounded-xl border border-emerald-500/30 bg-emerald-500/5 p-4">
          <div className="flex items-start gap-3">
            <Check className="mt-0.5 h-4 w-4 text-emerald-500" />
            <div>
              <h3 className="text-sm font-medium text-foreground">
                Daemon already connected
              </h3>
              <p className="mt-0.5 text-xs text-muted-foreground">
                Reliant detected a running daemon. Continue to choose a
                directory.
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={handleLocalContinue}
            className="w-full rounded-lg bg-zinc-950 py-2.5 text-sm font-medium text-white transition-colors hover:bg-zinc-800 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200"
          >
            Continue
          </button>
        </div>
      )}

      {showLocal && !activeDaemon && (
        <div className="rounded-xl border border-border/50 bg-muted/30 p-4">
          {/* Self-hosted connect instructions are shared with the
              ProjectPicker's in-place "Connect a new daemon" flow. The
              onboarding-specific auto-advance still happens via the
              hasUsableDaemon effect above; SelfHostedDaemonConnect just owns
              the download/token/start UI and the "waiting to connect" state. */}
          <SelfHostedDaemonConnect onConnected={handleLocalContinue} />
        </div>
      )}
    </div>
  );
}
