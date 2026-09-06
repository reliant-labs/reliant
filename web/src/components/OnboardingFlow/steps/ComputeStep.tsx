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
import { useCloudEligibility } from "@/hooks/useOnboardingQueries";
import { RedeemCouponForm } from "@/components/RedeemCouponForm";
import { RedeemedCouponKind } from "@/services/controlPlane/reliantAI";
import { useBundledDaemonPending } from "@/hooks/useBundledDaemonPending";
import { isCloudCompute } from "../types";
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
    (d) => d.status === DaemonStatus.ACTIVE || d.status === DaemonStatus.IDLE,
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
    return isCloudCompute(compute) ? "github_repo" : "local_folder";
  }
  return "new_project";
}

export function ComputeStep({
  plan,
  updatePlan,
  onNext,
}: StepProps & { hideHeader?: boolean }) {
  const [showLocal, setShowLocal] = useState(plan.compute === "local_daemon");
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
  const hasAdvanced = useRef(false);
  const hasTrackedConnectedRef = useRef(false);

  // Cloud eligibility via React Query.
  //
  // getIsDev() is deliberately NOT an input here (same reasoning as ModelStep).
  // It used to force eligible=true in dev, which offered the cloud choice
  // for a user the server considers unfunded: the click sailed past this gate
  // and failed later at the daemon service's own check, after the UI had
  // already committed to provisioning. It also suppressed the ineligible copy
  // and the coupon field — the one affordance that could have fixed the
  // problem. Dev now sees what the server reports; ?onboarding-credits= is the
  // escape hatch for exercising either branch on purpose.
  const forcedEligibility = getForcedEligibility();
  const {
    eligible: cloudEligible,
    isLoading: cloudLoading,
    refetch: refetchCloudEligibility,
  } = useCloudEligibility();

  const eligible =
    forcedEligibility === "eligible" ||
    (forcedEligibility == null && cloudEligible);
  const loading = forcedEligibility == null && cloudLoading;

  // Whether the user can CHOOSE a hosted machine — which is now everyone, as
  // long as this build has hosted machines at all.
  //
  // ENTITLEMENT NO LONGER GATES THE CHOICE. It used to: a new account has no
  // subscription and no trial, so `eligible` was false for essentially every
  // first-time visitor, and the card responded by hiding its own primary
  // control and offering a link out to /settings/billing instead. The step
  // whose only job is to ask a question refused to accept the answer.
  //
  // Choosing cloud while un-entitled is not a dead end any more, because
  // `deriveStep` routes exactly that plan to the checkout step. So this asks
  // the question, records the answer, and lets the flow handle the money. What
  // eligibility still does is decide what the card SAYS — see below.
  const canChooseCloud = HAS_CLOUD_DAEMONS && !loading;

  /**
   * Record "run this on a Reliant machine" and advance. That is the whole of
   * it — no `listDaemons`, no `CreateDaemon`, no `ResumeDaemon`.
   *
   * This used to provision the machine here, and additionally from an effect
   * that watched eligibility flip after a coupon redemption. Both are gone:
   * a call that creates a billable resource may fire only at a commit point,
   * and provisioning now happens once, in `commitLaunchPlan`, after the
   * terminal step confirms onboarding.
   *
   * Deleting the side effect also deleted ~120 lines of effect-ordering race
   * commentary. The races (a bundled local daemon appearing between the
   * eligibility flip and the cloud start running, and committing
   * `local_daemon` over a pending cloud start) existed only because two
   * effects were competing to commit the plan. With one of them no longer
   * acting, there is nothing to order.
   */
  const chooseCloud = async () => {
    if (!HAS_CLOUD_DAEMONS) return;
    // There is deliberately no `if (!eligible) return` here any more. It was
    // right when recording an un-entitled cloud plan moved a dead end further
    // down the flow; the checkout step is where that plan now goes, and
    // refusing the choice would skip it.
    setShowLocal(false);
    // Claim the advance before writing. Without it the local auto-skip effect
    // stays armed, and a bundled desktop daemon appearing on the next render
    // overwrites the cloud choice with `local_daemon` — which is what used to
    // drop users on project-picker having answered nothing.
    hasAdvanced.current = true;
    await updatePlan({
      compute: "cloud_paid",
      localPath: undefined,
      projectName: undefined,
    });
    trackEvent("onboarding_compute_selected", { compute: "cloud" });
    onNext();
  };

  // Clicking "I'll connect my own" only flips local UI state — it does NOT
  // commit `plan.compute` yet. If we set it here, `deriveStep` would see
  // compute set + modelProvider unset and immediately route the user to the
  // model step, skipping the download/connect instructions that render below
  // when `showLocal && !activeDaemon`. We commit compute once the daemon
  // actually connects (the useEffect below) or via the explicit Continue
  // button when a daemon is already running.
  const handleLocal = () => {
    setShowLocal(true);
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

  // NOTE: there is deliberately NO effect here that acts on eligibility.
  //
  // There used to be one: redeeming a compute coupon armed `pendingCloudStart`,
  // and when the eligibility refetch landed this effect fired `CreateDaemon` —
  // a billable, resource-creating call triggered by a server state change
  // rather than by a user deciding anything. It also raced the local auto-skip
  // effect below, which is what produced "redeeming a coupon skipped every step
  // and took me to the project picker".
  //
  // Redemption now only refetches eligibility, which turns the cloud card's
  // choice back on. The user still chooses; the machine is still started once,
  // at the commit point. See commitLaunchPlan.ts.

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
  // We wait for `!daemonLoading` so the initial in-flight ListDaemons
  // doesn't briefly read as "no daemons" and let the user click through
  // the prompt before detection settles.
  const hasUsableDaemon = hasUsableDaemonForOnboarding(daemons);
  useEffect(() => {
    if (daemonLoading) return;
    if (!hasUsableDaemon) return;
    // `hasAdvanced` is set synchronously by chooseCloud before it writes the
    // plan, so a daemon arriving after a cloud choice cannot overwrite it.
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
  }, [hasUsableDaemon, daemonLoading]);

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
              isCloudCompute(plan.compute)
                ? "border-primary bg-primary/10"
                : "border-primary/25 bg-primary/5",
              !HAS_CLOUD_DAEMONS && "border-border/50 bg-muted/30 opacity-80",
            )}
          >
            <div className="flex min-w-0 items-start gap-4">
              <div className="flex-shrink-0 rounded-lg bg-primary/15 p-2.5 text-primary">
                <Cloud className="h-6 w-6" />
              </div>
              <div className="min-w-0 space-y-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-semibold text-foreground">
                    In the Cloud
                  </span>
                  <span className="rounded bg-primary/20 px-1.5 py-0.5 text-2xs font-medium uppercase tracking-wider text-primary">
                    Nothing to install
                  </span>
                </div>
                {/* Describes what the user can do RIGHT NOW. Promising "start
                    a machine now" to someone with no compute plan sets up the
                    dead end this card no longer has. */}
                {/* Eligibility no longer changes whether the user may choose,
                    so it only changes what they are told to expect: an
                    entitled user's machine starts at the end of setup, while
                    an un-entitled one passes through payment on the way. Both
                    are true statements about the same button. */}
                <span className="block text-xs leading-relaxed text-muted-foreground">
                  {!HAS_CLOUD_DAEMONS
                    ? "Hosted machines are not available in this setup."
                    : loading || eligible
                      ? "We start one for you, ready in a few minutes. Setup continues while it boots."
                      : "We run it for you — no setup, and it keeps working after you close your laptop. Monthly plans start during setup."}
                </span>
              </div>
            </div>

            {/* Two states now, not three.

                This card has been through both failure modes. First a disabled
                "Start my machine" that a brand-new user could never click,
                because the signup compute grant is gone and every new account
                resolves to NO_SUBSCRIPTION. Then no button at all for those
                users, with "Set up billing" promoted in its place — which
                fixed the inert control by replacing it with an exit from the
                flow.

                Neither is needed once payment is a step. The choice is always
                offered, because choosing is all this step does, and where an
                un-entitled choice leads is the checkout step rather than a
                different page. */}
            {loading ? (
              <div className="inline-flex w-full items-center justify-center gap-2 rounded-lg bg-muted px-4 py-2.5 text-sm font-semibold text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                Checking availability...
              </div>
            ) : canChooseCloud ? (
              // "Use a Reliant machine", not "Start my machine". The verb is
              // the contract: this records a choice and moves on, and a label
              // promising the machine is starting would be describing work
              // that now happens at the end of onboarding.
              <button
                type="button"
                onClick={chooseCloud}
                className="inline-flex w-full items-center justify-center gap-2 rounded-lg bg-sky-600 px-4 py-2.5 text-sm font-semibold text-white shadow-sm shadow-sky-600/20 transition-colors hover:bg-sky-500"
              >
                Use a Reliant machine
              </button>
            ) : null}

            {!loading && !HAS_CLOUD_DAEMONS && (
              <p className="text-xs leading-relaxed text-muted-foreground">
                Hosted machines are not available in this setup. Choose
                &ldquo;Use your own computer&rdquo; to continue — it is free.
              </p>
            )}

            {/* Coupon redemption stays, and is offered to everyone: someone
                who is already entitled may still be holding a code, and
                hiding the field until they run out means redeeming it requires
                first spending down.

                What is GONE is the "Set up billing" / "View plans" button that
                sat beside it. It navigated to /settings/billing — a full exit
                from a wizard whose state lives in a URL search param, needing
                a `returnTo` round-trip to get back. Prices are now shown on
                the checkout step, which is inside the flow, so there is
                nothing left for it to do. */}
            {HAS_CLOUD_DAEMONS && !loading && (
              <RedeemCouponForm
                variant="collapsed"
                size="sm"
                onRedeemed={(result) => {
                  // A redemption REFETCHES; it does not act. Enough compute
                  // minutes make `requiresPayment` false, and the checkout
                  // step simply never appears. This callback used to arm an
                  // auto-start that provisioned a machine on the next render —
                  // the speculative-execution defect this step no longer has.
                  if (result.kind === RedeemedCouponKind.COMPUTE_MINUTES) {
                    void refetchCloudEligibility();
                  }
                }}
              />
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
                  Use your own computer
                </span>
                <span className="rounded bg-emerald-500/15 px-1.5 py-0.5 text-2xs font-medium uppercase tracking-wider text-emerald-500">
                  Free
                </span>
              </div>
              <span className="block text-xs leading-relaxed text-muted-foreground">
                Connect any machine to Reliant. Just download the Reliant
                command line tool and connect to our platform.
              </span>
            </div>
          </button>
        </div>

        {/* No inline error slot any more. The only errors this step could
            raise came from provisioning, and it no longer provisions —
            choosing is local and cannot fail. Provisioning failures surface at
            the commit point, where the retry lives. */}

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
