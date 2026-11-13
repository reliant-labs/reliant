import { useEffect, useMemo, useRef, useState } from "react";
// CheckCircle2, not Monitor: the free row is a row in the machine list now, so
// it marks SELECTION exactly as PlanTiles does. The Cloud/Monitor pair went
// with the two-card layout — an icon on one row and none on the others is the
// shape difference that made the free option read as a different kind of
// answer.
import { Check, CheckCircle2, ChevronDown, Loader2, Smartphone } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  PlanTileRow,
  commonPlanHours,
  type ComputePlanOption,
} from "@/components/Billing/PlanTiles";
import { machinePricingFootnote } from "@/components/Billing/daemonUsage";
import {
  formatSizeLabel,
  isPurchasableComputePlan,
  sortPlansForDisplay,
} from "@/components/Settings/cloud/billingUtils";
import { deriveMachineOptions, oneRowPerPlan } from "@/components/Billing/machineOptions";
import { usePlans } from "@/hooks/useCloudBillingQueries";
import { getForcedEligibility } from "../forcedEligibility";
import { MACHINE_BURST } from "@/components/Billing/machineSpecs";
import {
  DaemonStatus,
  type DaemonInfo,
} from "@/gen/reliant/v1/daemon_registry_pb";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useCloudEligibility } from "@/hooks/useOnboardingQueries";
import { ConnectWait } from "../ConnectWait";
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
import { PairMachinePanel } from "@/components/Machines/PairMachinePanel";
import { StepFooterAction } from "../StepFooter";
import { trackEvent } from "@/lib/analytics";
import { capabilities } from "@/services/controlPlane/capabilities";
import { formatPrice } from "@/components/Billing/priceFormat";
import { formatMachineSeconds } from "@/lib/formatMachineMinutes";
import { isPhoneViewport } from "@/lib/mobileRedirect";

const HAS_CLOUD_DAEMONS = capabilities.cloudDaemons;

// The "where do you want to run your daemon?" step is bootstrap-only: it
// disambiguates a brand-new user between "Reliant Cloud" and "your own
// machine." Once a daemon — local OR managed — is registered to the user's
// account, that bootstrap question is moot. The post-onboarding workspace
// picker handles selection when multiple daemons exist.
//
// "Usable" means the user has clearly already picked a daemon location, so
// the where-question must not re-appear:
//   - ACTIVE:    actively attached (the obvious case).
//   - PENDING:   a managed machine that is provisioning or cloning. The user
//                asked for it; it is coming.
//   - SUSPENDED: a real machine, deliberately parked, which resumes on use.
// DISCONNECTED / FAILED / UNSPECIFIED are skipped: a stale row, a machine
// that will never come up, or an unhealthy registration — the cases where the
// user genuinely needs to (re)decide.
//
// There used to be TWO of these predicates, one per DaemonStatus enum, because
// the UI read two different daemon lists whose numeric values collided
// (reliant ACTIVE=1 was control-plane PENDING=1; reliant IDLE=2 was
// control-plane ACTIVE=2). There is now one list and one enum, so there is one
// predicate — and PENDING and SUSPENDED are nameable here for the first time,
// which is why this reads richer than the registry half it replaces rather
// than narrower than the control-plane half. See
// docs/design/one-daemon-list.md.
export function hasUsableDaemonForOnboarding(
  daemons: ReadonlyArray<Pick<DaemonInfo, "status">>,
): boolean {
  return daemons.some(
    (d) =>
      d.status === DaemonStatus.ACTIVE ||
      d.status === DaemonStatus.PENDING ||
      d.status === DaemonStatus.SUSPENDED,
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
  // A phone cannot run the desktop app or the CLI, so it cannot finish pairing
  // a machine, but the free answer still leads and is still the default: the
  // user sets it up from a laptop (see PhoneLocalSteps) and this step advances
  // on its own once that computer connects. Read once at mount so a resize
  // cannot swap the list under the user.
  const [onPhone] = useState(() => HAS_CLOUD_DAEMONS && isPhoneViewport());
  // The free answer is the preselected one, unless the user already chose a
  // hosted machine (Back from checkout lands here with that recorded, and a
  // size picked before a reload is in the plan too; neither must be flipped).
  const [showLocal, setShowLocal] = useState(
    plan.compute === "local_daemon" ||
      (!isCloudCompute(plan.compute) && !plan.computePlanId),
  );
  // While the free option is chosen the hosted rows fold into one disclosure so
  // the connect instructions sit right under the list. This is only the user's
  // wish to look at them again; it is meaningless (and ignored) otherwise.
  const [cloudExpanded, setCloudExpanded] = useState(false);
  const {
    activeDaemon,
    daemons,
    loading: daemonLoading,
    unreachable: registryUnreachable,
    refresh: refreshDaemons,
  } = useDaemonStatus();
  // The user chose to stop waiting for the first machine list. Only offered
  // once the wait is slow (ConnectWait), so it never races detection.
  const [gaveUpWaiting, setGaveUpWaiting] = useState(false);
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
    allowedDaemonSizes,
    hasActiveSubscription: hasSubscription,
    grantedSmallDaemonSecondsRemaining: grantedSecondsRemaining = 0,
  } = useCloudEligibility();

  const eligible =
    forcedEligibility === "eligible" ||
    (forcedEligibility == null && cloudEligible);
  const loading = forcedEligibility == null && cloudLoading;

  // ── The prices live HERE now, on the step that asks the question ─────
  //
  // Choosing a machine and paying for it used to be two screens: this step
  // asked "cloud or your own?", and the checkout step — after the model
  // question — was the first place a price or a size appeared. So the user
  // committed to hosted compute before learning what it cost, and picked the
  // size on a page whose purpose was collecting a card.
  //
  // Now the decision carries its own price. Nothing here mounts Stripe or
  // creates anything: picking a tile writes `computePlanId` to the plan and
  // that is all. The card comes once, at the end, for everything owed.
  const plansQ = usePlans();

  const computePlans = useMemo(
    () =>
      sortPlansForDisplay(
        (plansQ.data?.plans ?? []).filter(isPurchasableComputePlan),
      ),
    [plansQ.data],
  );

  // Which sizes to offer is the union of what the catalog sells, and plan and
  // size are ONE axis — picking a size picks the cheapest plan that runs it.
  //
  // The derivation is shared with the settings Plans tab (deriveMachineOptions).
  // It used to live inline here, and the Plans tab answered the same question
  // a different way — a plan-name grid behind a size filter — which is the
  // divergence that produced two unlike surfaces for one choice. One
  // declaration, two callers that differ only in what a row DOES.
  //
  // One row per plan (oneRowPerPlan): the rows select by `planId`, and two
  // sizes sharing a cheapest plan rendered as two rows that selected together.
  const planOptions = useMemo<ComputePlanOption[]>(
    () => oneRowPerPlan(deriveMachineOptions(computePlans, plansQ.data?.daemonPricing)),
    [computePlans, plansQ.data?.daemonPricing],
  );

  // The machine sizes are shown to EVERYONE who can choose a hosted machine.
  //
  // This used to be `!eligible`, so redeeming a compute coupon deleted the
  // "Choose your machine" heading and all four tiles on the very next render.
  // The user's report was "entering a coupon took me to this page... or maybe
  // it just hid the options?" — nothing navigated; the card rearranged under
  // them at the moment they acted, which is indistinguishable from being moved.
  //
  // The original reasoning — an entitled user has nothing to buy, and a price
  // list invites them to buy a second machine — is right about the PRICES and
  // wrong about the CHOICE. A coupon grants minutes, not a size, so the size
  // is still a real decision, and it is made here or nowhere: the checkout
  // step has no size picker, and an entitled user never reaches it anyway.
  // Hiding the tiles silently took that decision away as a reward for
  // redeeming, and pinned every entitled user to whatever the commit defaulted
  // to.
  //
  // So the tiles stay and the PRICES change — see `coverage` below. That
  // satisfies the real constraint (do not ask an entitled user for money)
  // without removing the question.
  const showPlanChoice = HAS_CLOUD_DAEMONS && !loading;
  const showCloudRows = showPlanChoice && (!showLocal || cloudExpanded);
  const cheapestMonthlyCents = planOptions.length
    ? Math.min(...planOptions.map((option) => option.monthlyPriceCents))
    : null;

  // Whether this user's machine is already paid for — by a coupon, a grant, or
  // an existing subscription. It decides what the tiles SAY, never whether
  // they appear.
  const machineCovered = eligible;

  /**
   * WHICH machine the coverage actually pays for.
   *
   * Not all of them, and getting this wrong is worse than showing prices. The
   * server's `checkDaemonSizeAllowed` gives a coupon-only user (no
   * subscription) a size allowance of M alone (the coupon unit) — a compute
   * grant buys machine TIME, deliberately not a bigger machine. So marking
   * every tile "Covered" tells the user their code bought an XL, and the
   * commit is then refused with "your plan does not include daemon size xl"
   * at the last step of onboarding. Observed doing exactly that in dev.
   *
   * Marking only the cheapest tile the server says it will admit
   * (`allowed_daemon_sizes`) keeps the claim true. It is not simply the
   * cheapest tile: S1 and S2 sell below M, and a coupon does not run them.
   * The others still show their price, so choosing one is visibly a
   * purchase. With no answer yet (the forced-eligibility escape hatch), the
   * cheapest tile stands in.
   */
  const allowedSizes = new Set((allowedDaemonSizes ?? []).map((size) => size.toLowerCase()));
  const coveredOption = machineCovered
    ? (planOptions.find((option) => option.size !== undefined && allowedSizes.has(option.size)) ??
      (allowedSizes.size === 0 ? planOptions[0] : undefined))
    : undefined;
  const coveredPlanId = coveredOption?.planId;
  // Read from `size` rather than reformatting the row. The specs live in the
  // row's own `detail` slot now, so `label` is already the bare machine name —
  // but this sentence is prose about a SIZE, not a restatement of a row, and
  // deriving it from the size keeps it that way if the row's label ever
  // changes again. It must read "covers the M machine".
  const coveredSize = coveredOption?.size;
  const coveredSizeLabel = coveredSize ? formatSizeLabel(coveredSize) : "smallest";

  // Until the user picks a tile, the default is the machine their coverage
  // pays for: a coupon-only user who continues without touching the tiles
  // must commit the M the coupon runs, not the cheaper S1 the server refuses
  // at the last step. Uncovered, the cheapest tile is the default.
  const selectedPlanId =
    planOptions.find((option) => option.planId === plan.computePlanId)
      ?.planId ?? coveredPlanId ?? planOptions[0]?.planId;
  const selectedPlanLabel = planOptions.find(
    (option) => option.planId === selectedPlanId,
  )?.label;

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
    // drop users on finish having answered nothing.
    hasAdvanced.current = true;
    await updatePlan({
      compute: "cloud_paid",
      // The size chosen HERE, beside its price, rather than later on the
      // payment screen — and recorded whether or not the user is entitled.
      //
      // It used to be written only for an un-entitled user, on the reasoning
      // that an entitled one "picks nothing". They do pick something: the
      // SIZE. A compute grant buys minutes, not a tier, and this is the only
      // screen that asks. Leaving it undefined meant the tile had no effect
      // for exactly the users whose machine was already paid for.
      //
      // It does not create a bill. `requiresPayment` owes compute only when
      // `computeEligible` is false, so an entitled user still skips checkout
      // with the id recorded.
      computePlanId: selectedPlanId,
      localPath: undefined,
      projectName: undefined,
    });
    trackEvent("onboarding_compute_selected", {
      compute: "cloud",
      // "" rather than undefined: the analytics payload takes only concrete
      // values, and an empty catalog leaves no plan to name.
      plan_id: selectedPlanId || "",
    });
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
  //   3. The dev stack's Electron pairing (control-plane's `forge env up
  //      dev`): the Electron-spawned local daemon registers with the control-plane right after sign-in.
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
  if ((daemonLoading || awaitingBundledDaemon) && !gaveUpWaiting) {
    return (
      <div
        className="space-y-5 py-6 text-center"
        role="status"
        aria-live="polite"
        data-testid="compute-step-loading"
      >
        <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-primary/15 text-primary-ink">
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
              ? "The desktop app brings its own. Almost done."
              : "Looking for a machine you already connected."}
          </p>
        </div>
        <div className="mx-auto max-w-[420px] text-left">
          <ConnectWait
            label={awaitingBundledDaemon ? "Waiting for the app's machine" : "Looking for your machines"}
            onRetry={() => refreshDaemons?.()}
            daemons={daemons}
            unreachable={registryUnreachable}
            skip={{ label: "Choose a machine myself", onClick: () => setGaveUpWaiting(true) }}
          />
        </div>
      </div>
    );
  }

  const cloudRows = (
    <>
      {showCloudRows &&
        planOptions.map((option) => (
          <PlanTileRow
            key={option.planId}
            plan={option}
            // NOTHING is selected while the user's own computer is the
            // choice. `selectedPlanId` falls back to the covered plan, else
            // the first, so
            // that committing cloud always carries a size — harmless
            // when the tiles were a separate block, but in one list that
            // default paints Small as selected beside an equally
            // selected free row, showing two chosen machines for one
            // question.
            selected={!showLocal && option.planId === selectedPlanId}
            covered={option.planId === coveredPlanId}
            onSelect={() => {
              // Picking a hosted size is also picking "hosted", so it
              // closes the free row's expanded instructions. Without
              // this the connect panel stays open under a row that is no
              // longer selected.
              setShowLocal(false);
              setCloudExpanded(false);
              void updatePlan({ computePlanId: option.planId });
            }}
          />
        ))}

      {showCloudRows && plansQ.isLoading && (
        <div className="flex items-center gap-2 px-1 py-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" /> Loading machines…
        </div>
      )}
    </>
  );

  return (
    // 560px, matching the checkout step's column. The card itself stays at the
    // shared 840px (see stepMaxWidth — every step shares one width so the card
    // never resizes between steps); a single column of full-width rows wants a
    // readable measure, not the full card.
    <div className="mx-auto w-full max-w-[560px] space-y-6">
      {/* ONE heading, and it is the question. There used to be two — a large
          "Where should Reliant run your code?" over a small uppercase "Choose
          your machine" — asking the same thing twice at two sizes. It stays
          put when the free row is picked: hiding it re-flowed the whole step
          under the user's click. */}
      <div className="space-y-1.5 text-center">
        <h2
          id="compute-step-heading"
          className="text-xl font-semibold tracking-tight text-foreground"
        >
          Choose your machine
        </h2>
        <p className="text-sm text-muted-foreground">
          Where Reliant runs your code. You can add more machines later.
        </p>
      </div>

      <div className="space-y-4">
        {/* ── ONE list, one price column ──────────────────────────────────

            Where the code runs and what that costs is one question, so every
            answer — the user's own computer included — is a row of one list,
            with its price in the same column. "Use your own computer / Free"
            on the same axis as "Small / $20.00/mo" is what makes it a peer
            rather than a consolation. (Two cards side by side were tried; the
            free card was 81% dead space and had no price to weigh.)

            Free leads, and nothing separates it from the hosted rows — no
            divider, no second heading, no icon only it carries. The owner
            asked for exactly that ("make it look like another option"), and
            each of those was a separator that announced it as a different
            kind of answer.

            The rows are mapped here with PlanTileRow rather than by mounting
            PlanTiles, which would nest the priced rows in their own container:
            adjacent on screen, a separate list to a screen reader. It also
            keeps the free row independent of the plan catalog's loading and
            empty states — it is the one machine that needs no catalog. The
            group is labelled by the heading above, which covers every row. */}
        <div
          role="group"
          aria-labelledby="compute-step-heading"
          className="space-y-2"
        >
          {/* The free option, as the FIRST ROW. Same geometry, type and price
              column as PlanTileRow (keep the two in step). It is still NOT a
              plan: it writes no `computePlanId`, and picking it opens the
              connect instructions at the bottom of the step. */}
          <button
            type="button"
            onClick={handleLocal}
            aria-pressed={showLocal}
            className={cn(
              "flex w-full min-w-0 items-center justify-between gap-4 rounded-lg border px-4 py-3 text-left transition-colors",
              showLocal
                ? "border-primary bg-primary/10"
                : "border-border bg-background hover:border-primary/40 hover:bg-muted/50",
            )}
          >
            <span className="min-w-0">
              <span className="flex items-center gap-2 text-sm font-medium text-foreground">
                Your own computer
                {showLocal && <CheckCircle2 className="h-4 w-4 text-primary-ink" />}
              </span>
              <span className="block text-xs text-muted-foreground">
                Runs on your machine, so it&apos;s free. Bring your own model
                key or subscription. Needs the Reliant desktop app or CLI.
              </span>
            </span>
            <span className="flex-shrink-0 text-sm font-medium tabular-nums text-foreground">
              Free
            </span>
          </button>

          {/* The hosted machines — the REST of the same list, with no header
              of their own (a header would re-divide the list into "yours" and
              "ours"). Picking one records the choice and nothing else: nothing
              exists at Stripe until the single checkout at the end of the flow.

              With the free row chosen they fold into one disclosure, so the
              connect instructions sit close under the list while the hosted
              options stay one click away. */}
          {showPlanChoice && showLocal && (
            <button
              type="button"
              onClick={() => setCloudExpanded((open) => !open)}
              aria-expanded={cloudExpanded}
              aria-controls={
                cloudExpanded ? "compute-cloud-options" : undefined
              }
              data-testid="compute-cloud-toggle"
              className="flex w-full min-w-0 items-center justify-between gap-4 rounded-lg border border-border bg-background px-4 py-3 text-left transition-colors hover:border-primary/40 hover:bg-muted/50"
            >
              <span className="min-w-0">
                <span className="block text-sm font-medium text-foreground">
                  Reliant Cloud machines
                  {cheapestMonthlyCents !== null &&
                    ` · from ${formatPrice(cheapestMonthlyCents)}/mo`}
                </span>
                <span className="block text-xs text-muted-foreground">
                  {machineCovered
                    ? "Keeps running after you close your laptop."
                    : "Paid. Keeps running after you close your laptop."}
                </span>
              </span>
              <span className="flex flex-shrink-0 items-center gap-1 text-xs text-muted-foreground">
                {cloudExpanded ? "Hide" : "Show"}
                <ChevronDown
                  className={cn(
                    "h-4 w-4 transition-transform",
                    cloudExpanded && "rotate-180",
                  )}
                  aria-hidden
                />
              </span>
            </button>
          )}

          {/* Flat siblings of the free row when nothing is folded, so the
              list stays one list; wrapped (and addressable by the
              disclosure's aria-controls) only while revealed under it. */}
          {showLocal && showCloudRows ? (
            <div id="compute-cloud-options" className="space-y-2">
              {cloudRows}
            </div>
          ) : (
            cloudRows
          )}
        </div>

        {/* The fine print under the list — at most two short muted lines,
            not the four stacked paragraphs it used to be. Only what changes a
            decision or explains a price survives.

            - The coverage line says what a redeemed code did, where the price
              used to be (it is why one row now reads "Covered"), and names the
              LIMIT the server enforces: a compute grant buys machine time on
              the M machine only, and a user who picks another size on the
              strength of a coupon is refused at provisioning. S1 and S2 are
              smaller and still not covered, so the sentence says "other
              sizes", never "larger".
            - The burst headroom and the hours rule hold at every size, so they
              are said once here rather than on each row. The rows print what a
              machine reserves; the burst sentence is absent entirely if the
              ladder stops being uniform (see machineBurst). A coupon-only user
              has no monthly plan, so "Includes 160 hours a month" would
              promise them something they did not get. */}
        {showCloudRows &&
          !plansQ.isLoading &&
          (planOptions.length > 0 ? (
            <div className="space-y-1.5 px-1">
              {machineCovered && (
                <p
                  className="text-xs leading-relaxed text-foreground"
                  data-testid="compute-step-coverage-note"
                >
                  {grantedSecondsRemaining >= 60
                    ? `Your code covers ${formatMachineSeconds(grantedSecondsRemaining)}, one-time — no charge today.`
                    : `Your code covers the ${coveredSizeLabel} machine — no charge today.`}{" "}
                  {coveredSize === "m"
                    ? "S1, S2 and the larger sizes need a monthly plan."
                    : "Other sizes need a monthly plan."}
                </p>
              )}
              <p className="text-xs leading-relaxed text-muted-foreground">
                {MACHINE_BURST && (
                  <>
                    <span data-testid="compute-step-burst-note">
                      Busy builds get up to {MACHINE_BURST.cpu}× the CPU and{" "}
                      {MACHINE_BURST.memory}× the memory shown.
                    </span>{" "}
                  </>
                )}
                <span data-testid="machine-pricing-line">
                  {machinePricingFootnote(
                    machineCovered && !hasSubscription
                      ? null
                      : commonPlanHours(planOptions),
                  )}
                </span>
              </p>
            </div>
          ) : (
            <p
              className="px-1 text-xs leading-relaxed text-muted-foreground"
              data-testid="compute-step-plans-unavailable"
            >
              Prices didn&apos;t load — that&apos;s on us. You can still
              continue, or redeem a code below.
            </p>
          ))}

        {/* The step's way forward is in the card FOOTER, which cannot scroll
            away (below six machine rows an in-box button sat under the fold).
            See the StepFooterAction at the end of this component. */}

        {!loading && !HAS_CLOUD_DAEMONS && (
          <p className="px-1 text-xs leading-relaxed text-muted-foreground">
            Hosted machines aren&apos;t available here. Use your own computer —
            it&apos;s free.
          </p>
        )}

        {/* Coupon redemption is offered to everyone who is choosing a hosted
            machine — someone already entitled may still be holding a code,
            and hiding the field until they run out would mean redeeming it
            requires first spending down.

            It is ABSENT while "Use your own computer" is selected, for a
            weaker reason than the cloud CTA: that button contradicted the
            selection and destroyed it on click; this field does neither —
            redeeming leaves `showLocal` untouched. The argument is relevance:
            a compute coupon buys machine time the user has just declined.

            What makes this safe is not visible on this step: the field also
            accepts WALLET_CREDIT codes, so hiding it here could have removed
            the only way to redeem an AI-credit code. It does not, because the
            local-daemon path goes straight to the model step next, which
            carries its own coupon field. */}
        {HAS_CLOUD_DAEMONS && !loading && !showLocal && (
          <RedeemCouponForm
            variant="collapsed"
            size="sm"
            onRedeemed={(result) => {
              // A redemption REFETCHES; it does not act. Enough compute
              // minutes make `requiresPayment` false, and the checkout step
              // simply never appears. This callback used to arm an auto-start
              // that provisioned a machine on the next render — the
              // speculative-execution defect this step no longer has.
              if (result.kind === RedeemedCouponKind.COMPUTE_MINUTES) {
                void refetchCloudEligibility();
              }
            }}
          />
        )}
      </div>

      {/* ── What to do next, at the BOTTOM of the step ────────────────────

          Selecting "Use your own computer" is only half an answer: the machine
          still has to be connected, and this is where that happens — after
          everything, because it is the consequence of choosing, not one of the
          choices. It reads as the click's result only because the hosted rows
          fold and the cloud CTA and coupon field go away while it shows, so
          the gap above it is a short list and one line of fine print.

          The two states are distinct answers, not a loading ladder: a daemon
          is ALREADY running (confirm and go — Continue is in the footer), or
          one is not (install, run, approve). */}
      {showLocal && activeDaemon && (
        <div className="flex items-start gap-3 rounded-lg border border-success/30 bg-success/5 px-4 py-3">
          <Check className="mt-0.5 h-4 w-4 text-success" />
          <div>
            <h3 className="text-sm font-medium text-foreground">
              Your machine is connected
            </h3>
            <p className="mt-0.5 text-xs text-muted-foreground">
              Continue to finish setting up.
            </p>
          </div>
        </div>
      )}

      {/* Shared with the Add machine sheet and Settings → Machines; it owns
          the install/run/approve UI and the "waiting to connect" state. The
          onboarding auto-advance still happens via the hasUsableDaemon effect
          above. No inline error slot: choosing cannot fail, and provisioning
          failures surface at the commit point, where the retry lives. */}
      {showLocal && !activeDaemon && (
        <>
          {onPhone ? (
            <PhoneLocalSteps />
          ) : (
            <PairMachinePanel mode="bootstrap" onConnected={handleLocalContinue} />
          )}
          <ConnectWait
            label="Looking for your computer"
            onRetry={() => refreshDaemons?.()}
            daemons={daemons}
            unreachable={registryUnreachable}
          />
        </>
      )}

      {/* The way forward, rendered into the card footer. One slot, four
          states, so the footer always says what happens next:
            - checking eligibility       → disabled "Checking availability…"
            - a hosted machine is picked → "Continue with <size>"
            - own computer, connected    → "Continue"
            - own computer, not yet      → disabled "Waiting…" (it advances on
                                           its own when the machine connects)
          "Continue with Large", not "Start my machine": this records a choice
          and moves on. Provisioning happens at the end of onboarding, and a
          label promising a start would be describing work that is not here. */}
      <StepFooterAction>
        {loading ? (
          <button type="button" disabled className={FOOTER_PENDING}>
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden />
            Checking availability…
          </button>
        ) : showLocal ? (
          activeDaemon ? (
            <button type="button" onClick={handleLocalContinue} className={FOOTER_PRIMARY}>
              Continue
            </button>
          ) : (
            <button type="button" disabled className={FOOTER_PENDING}>
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden />
              Waiting for your computer to connect…
            </button>
          )
        ) : canChooseCloud ? (
          <button type="button" onClick={chooseCloud} className={FOOTER_PRIMARY}>
            {selectedPlanLabel
              ? `Continue with ${selectedPlanLabel}`
              : "Continue with a Reliant machine"}
          </button>
        ) : null}
      </StepFooterAction>
    </div>
  );
}

// What "your own computer" means on a phone: the phone cannot install or pair
// anything, so this says how to do it from a laptop and offers the handoff as
// the action. It deliberately has no download link or install button. The step
// keeps waiting underneath (ConnectWait and the footer) and advances by itself
// when the laptop connects to this account, so choosing this never dead-ends.
function PhoneLocalSteps() {
  const [copied, setCopied] = useState<"idle" | "copied" | "failed">("idle");
  const link = typeof window === "undefined" ? "" : window.location.href;
  const appUrl = typeof window === "undefined" ? "" : window.location.origin;
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(link);
      setCopied("copied");
    } catch {
      setCopied("failed");
    }
  };
  const mailto = `mailto:?subject=${encodeURIComponent("Continue setting up Reliant")}&body=${encodeURIComponent(link)}`;
  return (
    <div
      className="space-y-3 rounded-lg border border-border bg-muted/30 px-4 py-3"
      data-testid="compute-step-phone-local"
    >
      <p className="flex items-start gap-2 text-sm font-medium text-foreground">
        <Smartphone className="mt-0.5 h-4 w-4 flex-shrink-0" aria-hidden />
        Set up your own computer from a laptop
      </p>
      <ol className="list-decimal space-y-1.5 pl-5 text-xs leading-relaxed text-muted-foreground">
        <li>
          On your laptop, open{" "}
          <span className="break-all font-medium text-foreground">{appUrl}</span> and sign in.
        </li>
        <li>Install the Reliant desktop app (or run the CLI command shown there).</li>
        <li>
          Your computer connects to this account. Come back here, or keep going on the laptop.
        </li>
      </ol>
      <div className="flex flex-wrap items-center gap-3 text-xs">
        <button
          type="button"
          onClick={() => void copy()}
          className="font-medium text-primary-ink underline-offset-2 hover:underline"
        >
          Continue on your computer: copy link
        </button>
        <a
          href={mailto}
          className="font-medium text-primary-ink underline-offset-2 hover:underline"
        >
          Email it to myself
        </a>
        <span role="status" aria-live="polite" className="text-muted-foreground">
          {copied === "copied"
            ? "Link copied"
            : copied === "failed"
              ? "Couldn\u2019t copy. Use the email link."
              : ""}
        </span>
      </div>
    </div>
  );
}

// Sized to sit beside the footer's Back button (px-4 py-2 text-sm). Semantic
// tokens, so the CTA follows the color scheme and reads in light mode too —
// the raw sky/white-alpha versions vanished on a light card.
const FOOTER_PRIMARY =
  "inline-flex items-center gap-2 rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-primary-foreground shadow-sm transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";
const FOOTER_PENDING =
  "inline-flex cursor-not-allowed items-center gap-2 rounded-lg border border-border bg-background px-4 py-2 text-sm text-muted-foreground";
