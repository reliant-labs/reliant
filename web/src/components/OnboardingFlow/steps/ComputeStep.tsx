import { useEffect, useRef, useState } from "react";
import { Check, Cloud, Loader2, Monitor } from "lucide-react";
import { cn } from "@/lib/utils";
import { getForcedEligibility } from "../forcedEligibility";
import {
  DaemonStatus,
  type DaemonInfo,
} from "@/gen/reliant/v1/daemon_registry_pb";
// Aliased on purpose: this is a DIFFERENT enum from the DaemonStatus above,
// with different numeric values. See hasUsableControlPlaneDaemonForOnboarding.
import { DaemonStatus as ControlPlaneDaemonStatus } from "@/gen/controlplane/v1/public/shared_pb";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useEventBus } from "@/lib/event-context";
import {
  useCloudEligibility,
  useCreateDaemon,
  useResumeDaemon,
  isReasonedQuotaError,
  isEntitlementDenial,
} from "@/hooks/useOnboardingQueries";
import { RedeemCouponForm } from "@/components/RedeemCouponForm";
import { RedeemedCouponKind } from "@/services/controlPlane/reliantAI";
import { useGoToBilling } from "@/hooks/useGoToBilling";
import { useBundledDaemonPending } from "@/hooks/useBundledDaemonPending";
import type {
  CodeSource,
  ComputeChoice,
  OnboardingIntent,
  StepProps,
} from "../types";
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

// The control-plane's Daemon carries a DIFFERENT DaemonStatus enum than
// reliant's DaemonInfo, and the two are NOT interchangeable — the numbers
// disagree:
//
//   controlplane: UNSPECIFIED=0 PENDING=1 ACTIVE=2  SUSPENDED=3
//   reliant:      UNSPECIFIED=0 ACTIVE=1  IDLE=2    DISCONNECTED=3
//
// reliant's IDLE(2) collides with control-plane's ACTIVE(2), and reliant's
// ACTIVE(1) collides with control-plane's PENDING(1). Casting one array to
// the other — the obvious way to silence the type error — would make a
// PENDING daemon read as ACTIVE and let onboarding declare itself complete
// against a daemon that has not started.
//
// So the control-plane shape gets its OWN predicate, written against its own
// enum. Same question, different vocabulary.
export function hasUsableControlPlaneDaemonForOnboarding(
  daemons: ReadonlyArray<{ status: ControlPlaneDaemonStatus }>,
): boolean {
  return daemons.some(
    (d) =>
      d.status === ControlPlaneDaemonStatus.ACTIVE ||
      d.status === ControlPlaneDaemonStatus.PENDING,
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
  // A packaged desktop build ships its own daemon, but it does not REGISTER
  // until after sign-in — measured at ~1.2s post-restart on prod, though the
  // renderer only learns of it when the daemon-connected event lands. Until
  // then ListDaemons legitimately returns empty, and asking the user to pick
  // their compute during that window is asking a question that answers
  // itself. `awaitingBundledDaemon` is true only in the desktop app, only
  // before a daemon has appeared, and only until the event or the budget
  // resolves it.
  const awaitingBundledDaemon = useBundledDaemonPending(
    hasUsableDaemonForOnboarding(daemons),
  );
  const events = useEventBus();
  const hasAdvanced = useRef(false);
  const hasTrackedConnectedRef = useRef(false);

  // Cloud eligibility via React Query.
  //
  // getIsDev() is deliberately NOT an input here (same reasoning as ModelStep).
  // It used to force eligible=true in dev, which ENABLED "Start cloud daemon"
  // for a user the server considers unfunded: the click sailed past this gate
  // and failed later at the daemon service's own check, after the UI had
  // already committed to provisioning. It also suppressed the ineligible copy
  // and the coupon field — the one affordance that could have fixed the
  // problem. Dev now sees what the server reports; ?onboarding-credits= is the
  // escape hatch for exercising either branch on purpose.
  const goToBilling = useGoToBilling();
  const forcedEligibility = getForcedEligibility();
  const {
    eligible: cloudEligible,
    reason: cloudReason,
    isLoading: cloudLoading,
    refetch: refetchCloudEligibility,
  } = useCloudEligibility();

  const eligible =
    forcedEligibility === "eligible" ||
    (forcedEligibility == null && cloudEligible);
  // Fallback matches the NO_SUBSCRIPTION copy: if the server sends a reason
  // this build has no string for, the user still gets an actionable sentence
  // rather than a bare statement of what they lack.
  const reason =
    eligible
      ? null
      : (cloudReason ??
        "Redeem a coupon code or choose a plan to start a cloud machine.");
  const loading = forcedEligibility == null && cloudLoading;

  // Whether a "Start my machine" click would actually reach a machine. Named
  // and derived the same way ConnectDaemonModal does, because it is the same
  // question asked at the other door into the same action — and it now decides
  // whether the button EXISTS, not merely whether it is disabled.
  const canStartCloud = HAS_CLOUD_DAEMONS && eligible && !loading;

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

  // Declared here (above handleLocal, which clears them) so the whole
  // component body can reach them. See the effect further down for what they
  // do and why the intent needs BOTH a state flag and a ref.
  // "A compute coupon was redeemed and the cloud start it implies has not
  // happened yet."
  //
  // A ref AND a state flag would be two spellings of one fact, and they drifted
  // apart in exactly the window that matters. The ref is the source of truth
  // because the local auto-skip effect below must see the intent SYNCHRONOUSLY
  // — a daemon can appear on the very next render — while the counter bumps
  // the render that lets the start effect re-evaluate once eligibility flips.
  const pendingCloudStart = useRef(false);
  const [cloudStartAttempt, setCloudStartAttempt] = useState(0);

  const handleCloud = async () => {
    if (!HAS_CLOUD_DAEMONS) return;
    if (isStartingCloud) return;
    // Belt and braces with the button's disabled state. `disabled` is a
    // rendering concern — it can be defeated by a stale render, a keyboard
    // activation racing a refetch, or devtools — and starting to provision a
    // machine the server will refuse is worse than doing nothing. The daemon
    // service remains the authority; this just stops the UI from committing to
    // a flow it has already been told will fail.
    if (!eligible) return;

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
          // A resume that fails because the machine is still settling is
          // genuinely non-fatal — the user can retry from the chat banner, and
          // blocking onboarding on it would be worse than proceeding.
          //
          // An ENTITLEMENT denial is a different thing entirely, and swallowing
          // it was the bug behind "Start cloud daemon continues the onboarding":
          // the user was advanced into the app with no running machine and no
          // explanation. Let those through to the outer catch, which surfaces
          // the server's message and leaves the user on this step where the
          // coupon field is.
          try {
            await resumeDaemonMutation.mutateAsync(daemonId);
          } catch (err) {
            if (isEntitlementDenial(err)) throw err;
            // else: transient//settling — proceed to the provisioning UI.
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

      // Claim the advance before committing, exactly as
      // commitLocalAndAdvance does. Without this the cloud path left
      // `hasAdvanced` false, so the local auto-skip effect stayed armed and a
      // daemon appearing after this point — the desktop app's bundled one,
      // or the cloud daemon we just started registering — overwrote
      // compute:'cloud_free_trial' with 'local_daemon' and dropped the user
      // on project-picker.
      hasAdvanced.current = true;
      await updatePlan({
        compute: "cloud_free_trial",
        daemonProvisioning: needsProvisioning,
        localPath: undefined,
        projectName: undefined,
      });
      trackEvent("onboarding_compute_selected", { compute: "cloud" });
      onNext();
    } catch (err) {
      // Whatever happens here, we do NOT advance: onNext() is above, inside
      // the try, so any throw leaves the user on this step — which is where
      // the coupon field and the plans link are.
      //
      // Release the advance claim too. The user is staying on this step, so
      // the local auto-skip must be able to take over again if a daemon shows
      // up — a failed cloud start should not strand someone who has one.
      hasAdvanced.current = false;
      //
      // Reasoned-quota errors are already routed into the global
      // UpgradeRequiredModal by upgradeInterceptor — don't double-surface as
      // an inline error or toast.
      if (isReasonedQuotaError(err)) {
        return;
      }
      // Other entitlement denials (PermissionDenied / FailedPrecondition) get
      // no modal, so show the server's message inline. It is already written
      // for a user ("no compute subscription — subscribe to a compute plan to
      // create daemons") and names the remedy.
      if (isEntitlementDenial(err)) {
        setError(err instanceof Error ? err.message : "Cannot start a hosted machine.");
        void refetchCloudEligibility();
        return;
      }
      const msg =
        err instanceof Error ? err.message : "Failed to start your machine";
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
    // An explicit local choice overrides a pending post-redemption cloud
    // start. The minutes stay on the account for later.
    pendingCloudStart.current = false;
  };

  // `autoSkipped` distinguishes "the user chose local" from "we found a
  // daemon and advanced without asking". The latter must not leave a step in
  // the progress bar or a Back button pointing at a question that was never
  // put to them — see LaunchPlan.computeAutoSkipped.
  const commitLocalAndAdvance = async (
    daemonPreconnected: boolean,
    autoSkipped = false,
  ) => {
    if (hasAdvanced.current) return;
    hasAdvanced.current = true;
    await updatePlan({
      compute: "local_daemon",
      daemonProvisioning: false,
      localPath: undefined,
      projectName: undefined,
      computeAutoSkipped: autoSkipped || undefined,
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

  // A coupon that unblocks compute is, in context, the user saying "I want a
  // cloud machine" — they typed a code into the cloud card to get one. Making
  // them then find and click "Start my machine" is a second confirmation of
  // a decision already made, on a button that did not exist a moment ago.
  //
  // This CANNOT call handleCloud() directly from the redeem callback: that
  // callback closes over `eligible` from the render that mounted the form,
  // where it is still false, and handleCloud's own `if (!eligible) return`
  // guard would swallow the call. Eligibility is server-owned and only flips
  // after refetchCloudEligibility() lands. So the redemption arms an intent
  // and this effect fires it on the render where starting would actually
  // work — the same shape as the auto-skip effect below.
  //
  // The intent is ALSO held in a ref, read by the local auto-skip effect
  // below. State alone is not enough: `startingCloud` — the guard that effect
  // already had against racing handleCloud — only goes true once handleCloud
  // runs, and there is at least one render between "eligibility flipped" and
  // "handleCloud started". A local daemon appearing in that window (the
  // desktop app's bundled daemon is the common case) let auto-skip commit
  // compute:'local_daemon' first, which sent the user to project-picker
  // having answered nothing. The ref closes that window synchronously.
  useEffect(() => {
    if (!pendingCloudStart.current) return;
    // Still waiting on the eligibility refetch. Stay armed.
    if (!canStartCloud) return;
    // The user changed their mind while the refetch was in flight (picked
    // local, or a detected daemon already advanced the step). Starting a cloud
    // machine under them now would hijack a decision they have since made.
    if (showLocal || hasAdvanced.current) {
      pendingCloudStart.current = false;
      return;
    }
    if (startingCloud) return;
    // Stays TRUE across the await. The local auto-skip effect reads this ref to
    // know a cloud start is in flight, and handleCloud does not set
    // `startingCloud` until it actually runs — clearing here would reopen the
    // exact window where an arriving daemon commits local instead.
    void handleCloud().finally(() => {
      pendingCloudStart.current = false;
    });
    // Re-entry is prevented by `startingCloud` flipping true synchronously
    // inside handleCloud, and by hasAdvanced once it commits.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cloudStartAttempt, canStartCloud, showLocal, startingCloud]);

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
    // A redemption has asked for a CLOUD machine and the start is still
    // pending. `startingCloud` does not cover the render between eligibility
    // flipping and handleCloud running, and committing local in that window is
    // what sent users to project-picker with nothing answered.
    if (pendingCloudStart.current) return;
    if (hasAdvanced.current) return;
    if (!hasTrackedConnectedRef.current) {
      hasTrackedConnectedRef.current = true;
      trackEvent("onboarding_daemon_connected");
    }
    void commitLocalAndAdvance(Boolean(activeDaemon), true);
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
  if (daemonLoading || awaitingBundledDaemon) {
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
            {awaitingBundledDaemon
              ? "Getting your machine ready…"
              : "Checking your setup…"}
          </h2>
          <p className="text-xs text-muted-foreground">
            {awaitingBundledDaemon
              ? "This app comes with everything it needs — just finishing up."
              : "One moment while we look for a machine you have already set up."}
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {!showLocal && (
        <div className="space-y-2 text-center">
          <h2 className="text-2xl font-semibold tracking-tight text-foreground">
            Where should Reliant run your code?
          </h2>
          <p className="mx-auto max-w-[52ch] text-sm leading-relaxed text-muted-foreground">
            Reliant needs a computer to open your project, edit files and run
            commands. Use one we host, or connect your own.
          </p>
        </div>
      )}

      <div className="mx-auto w-full max-w-[840px] space-y-6">
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
                    A Reliant machine
                  </span>
                  <span className="rounded bg-primary/20 px-1.5 py-0.5 text-2xs font-medium uppercase tracking-wider text-primary">
                    Nothing to install
                  </span>
                </div>
                {/* Describes what the user can do RIGHT NOW. Promising "start
                    a machine now" to someone with no compute plan sets up the
                    dead end this card no longer has. */}
                <span className="block text-xs leading-relaxed text-muted-foreground">
                  {!HAS_CLOUD_DAEMONS
                    ? "Hosted machines are not available in this setup."
                    : canStartCloud || loading
                      ? "We start one for you, ready in a few minutes. Setup continues while it boots."
                      : // Deliberately says what the hosted option IS and
                        // stops. The `reason` line directly below names what
                        // to do about it, and having both sentences give the
                        // same instruction read as a stutter.
                        "We run it for you — no setup, and it keeps working after you close your laptop."}
                </span>
              </div>
            </div>

            {/* Three states, and only ONE of them shows a start button.

                A disabled "Start my machine" used to render unconditionally,
                and for the single most common visitor to this screen — a brand
                new user — it was permanently greyed out: the signup compute
                auto-grant is gone, so every new account resolves to
                NO_SUBSCRIPTION and lands here ineligible. The card's primary
                control was therefore an inert one, with the two controls that
                could actually change that (redeem a code, set up billing)
                demoted to small links beneath it. The button that cannot be
                clicked was the most prominent thing on the card.

                So when the user cannot start a machine we do not render a
                start button at all — the coupon and billing actions ARE the
                primary controls, because they are the whole of what the user
                can do here. The start button returns, as the primary control,
                exactly when clicking it would work. */}
            {loading ? (
              <div className="inline-flex w-full items-center justify-center gap-2 rounded-lg bg-muted px-4 py-2.5 text-sm font-semibold text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                Checking availability...
              </div>
            ) : canStartCloud ? (
              <button
                type="button"
                onClick={handleCloud}
                disabled={startingCloud}
                className={cn(
                  "inline-flex w-full items-center justify-center gap-2 rounded-lg px-4 py-2.5 text-sm font-semibold transition-colors",
                  startingCloud
                    ? "cursor-not-allowed bg-muted text-muted-foreground"
                    : "bg-sky-600 text-white shadow-sm shadow-sky-600/20 hover:bg-sky-500",
                )}
              >
                {startingCloud && <Loader2 className="h-4 w-4 animate-spin" />}
                {startingCloud ? "Starting your machine..." : "Start my machine"}
              </button>
            ) : null}

            {/* Gated on !eligible alone, NOT on `reason` being non-empty: a
                blocked user must always get the explanation AND the way out
                (billing), even if the server sends back a reason this build
                does not have copy for. A missing string is not a reason to
                hide the only escape route on the screen. */}
            {!loading && !canStartCloud && (
              <p className="text-xs leading-relaxed text-muted-foreground">
                {!HAS_CLOUD_DAEMONS
                  ? 'Hosted machines are not available in this setup. Choose "Use my own computer" to continue — it is free.'
                  : reason}
              </p>
            )}

            {/* Plans and coupon redemption are shown to EVERY user, not only
                blocked ones — someone who can already start a machine may
                still be holding a code, and "where is the pricing button" has
                one correct answer, which is "always visible".

                What changes with eligibility is their WEIGHT, not their
                presence: for a user who cannot start a machine these are the
                only two working controls on the card, so they render as full
                buttons; for a user who can, they sit under the start button as
                secondary links. */}
            {HAS_CLOUD_DAEMONS && !loading && (
              <div className={cn("space-y-2", !canStartCloud && "pt-0.5")}>
                <RedeemCouponForm
                  variant={canStartCloud ? "collapsed" : "button"}
                  size="sm"
                  onRedeemed={(result) => {
                    void refetchCloudEligibility();
                    // Only a COMPUTE_MINUTES code buys a machine. A
                    // wallet-credit code (LLM funding) leaves the user exactly
                    // as ineligible for compute as before, so auto-starting
                    // after one would fire a request the server refuses and
                    // surface an error for a redemption that succeeded.
                    if (result.kind === RedeemedCouponKind.COMPUTE_MINUTES) {
                      // Ref first and synchronously: a local daemon can appear
                      // on the very next render, and the auto-skip effect must
                      // already see that a cloud start is pending.
                      pendingCloudStart.current = true;
                      setCloudStartAttempt((n) => n + 1);
                    }
                  }}
                />
                <button
                  type="button"
                  onClick={goToBilling}
                  className={cn(
                    "transition-colors",
                    canStartCloud
                      ? "flex items-center gap-1 text-xs font-medium text-sky-500 hover:text-sky-400"
                      : "inline-flex w-full items-center justify-center rounded-lg border border-sky-600/40 bg-sky-600/10 px-3 py-2 text-xs font-semibold text-sky-500 hover:bg-sky-600/20",
                  )}
                >
                  {canStartCloud ? <>View plans &rarr;</> : "Set up billing"}
                </button>
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
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-semibold text-foreground">
                  Use my own computer
                </span>
                <span className="rounded bg-emerald-500/15 px-1.5 py-0.5 text-2xs font-medium uppercase tracking-wider text-emerald-500">
                  Free
                </span>
              </div>
              <span className="block text-xs leading-relaxed text-muted-foreground">
                Run it on this computer or your own server. Your code stays
                where it is. Takes a couple of minutes to set up.
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
                  Your machine is connected
                </h3>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  Reliant found a machine already running. Continue to pick a
                  folder to work in.
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
    </div>
  );
}

