import { useEffect, useMemo, useRef, useState } from "react";
// CheckCircle2, not Monitor: the free row is a row in the machine list now, so
// it marks SELECTION exactly as PlanTiles does. The Cloud/Monitor pair went
// with the two-card layout — an icon on one row and none on the others is the
// shape difference that made the free option read as a different kind of
// answer.
import { Check, CheckCircle2, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  PlanTileRow,
  describeIncludedHours,
  type ComputePlanOption,
} from "@/components/Billing/PlanTiles";
import {
  DAEMON_SIZE_ORDER,
  derivePlanDisplay,
  formatSizeLabel,
  isPurchasableComputePlan,
  smallestPlanAllowingSize,
  sortPlansForDisplay,
} from "@/components/Settings/cloud/billingUtils";
import { usePlans } from "@/hooks/useCloudBillingQueries";
import { getForcedEligibility } from "../forcedEligibility";
import { MACHINE_BURST, formatMachineSpec } from "../machineSpecs";
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
  const planOptions = useMemo<ComputePlanOption[]>(() => {
    const offered = new Set<string>();
    for (const p of computePlans) {
      for (const s of p.structuredLimits?.allowedDaemonSizes ?? []) {
        offered.add(s.toLowerCase());
      }
    }
    const out: ComputePlanOption[] = [];
    for (const size of DAEMON_SIZE_ORDER.filter((s) => offered.has(s))) {
      const sizePlan = smallestPlanAllowingSize(computePlans, size);
      if (!sizePlan) continue;
      const display = derivePlanDisplay(sizePlan);
      if (display.monthlyPriceCents == null) continue;
      // What the size actually BUYS, beside the size's name. "Medium" is a
      // label for a machine, not a description of one, and the user picking it
      // had no way to know whether it would hold their build. The figures are
      // the reserved floor; the burst ceiling is stated once under the list
      // (see MACHINE_BURST_MULTIPLE) rather than four times inside it.
      const spec = formatMachineSpec(size);
      const sizeLabel = formatSizeLabel(size);
      out.push({
        planId: sizePlan.id,
        // Size IS the choice here — one plan per size, cheapest that runs it —
        // so the tile is labelled by the machine, not by the plan's name.
        //
        // The spec rides in `label` because that is the seam PlanTiles gives a
        // caller — it renders the caller's label rather than deriving one,
        // precisely so its two callers can name different axes. Adding a spec
        // slot to PlanTiles instead would push an onboarding-only concern into
        // the component the settings checkout also renders.
        label: spec ? `${sizeLabel} — ${spec}` : sizeLabel,
        size,
        monthlyPriceCents: display.monthlyPriceCents,
        includedMinutes: display.includedMinutes,
        overageCentsPerMinute: display.overageCentsPerMinute,
      });
    }
    return out;
  }, [computePlans]);

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

  // Whether this user's machine is already paid for — by a coupon, a grant, or
  // an existing subscription. It decides what the tiles SAY, never whether
  // they appear.
  const machineCovered = eligible;

  /**
   * WHICH machine the coverage actually pays for.
   *
   * Not all of them, and getting this wrong is worse than showing prices. The
   * server's `checkDaemonSizeAllowed` resolves a coupon-only user's size
   * allowance from `plan_compute_free`, which permits SMALL alone — a compute
   * grant buys machine TIME, deliberately not a bigger machine. So marking
   * every tile "Covered" tells the user their code bought an XL, and the
   * commit is then refused with "your plan does not include daemon size xl"
   * at the last step of onboarding. Observed doing exactly that in dev.
   *
   * Marking only the smallest offered tile keeps the claim true: that is the
   * one the grant genuinely covers, and the others still show their price, so
   * choosing one is visibly a purchase.
   */
  const coveredPlanId = machineCovered ? planOptions[0]?.planId : undefined;
  // Read from `size`, NOT from the tile's `label`. The label now carries the
  // machine's specs as well as its name, and this sentence has to read as
  // prose: "covers the Small machine", never "covers the Small — 2 GB RAM ·
  // 0.5 CPU machine".
  const smallestOfferedSize = planOptions[0]?.size;
  const coveredSizeLabel = smallestOfferedSize
    ? formatSizeLabel(smallestOfferedSize)
    : "smallest";

  const selectedPlanId =
    planOptions.find((option) => option.planId === plan.computePlanId)
      ?.planId ?? planOptions[0]?.planId;

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

      {/* ── ONE list, not two cards ────────────────────────────────────
          
          This was a `sm:grid-cols-2` of two cards, and the layout broke when
          the machine tiles moved inline: the cloud card grew to ~550px while
          the local card held ~106px of content. Grid items stretch to the row
          height, so the right-hand card was 445px of empty black — measured,
          81% dead. It read as broken rather than sparse.
          
          `items-start` would have fixed the dead space and left the real
          problem: the two options were never comparable. One was a card
          carrying a whole priced sub-decision; the other was a button with a
          badge and no price, so the eye had nothing to weigh "$20.00/mo"
          against. The free option looked like an afterthought beside the paid
          one.
          
          They are ONE question — where does my code run, and what does it
          cost — so they are now one list with one price column. "Your own
          computer / Free" sits on the same axis as "Small / $20.00/mo", which
          is what makes it a peer rather than a consolation. It also removes
          the height mismatch at the root instead of papering over it.
          
          ── The free option is a ROW IN the machine list ───────────────
          
          This block used to say "cloud stays first: that is the product's
          emphasis and a layout fix is not the place to flip it", with the
          local option last under an "Or run it yourself" rule. The owner has
          since asked for the opposite, in both respects: free to the top, and
          "separate it less from the box, ie: make it look like another
          option."
          
          The old reasoning was about not smuggling a product decision into a
          layout fix. It was never an argument that cloud-first is RIGHT — only
          that a refactor should not be the thing that decides it. Asked
          directly, the owner decided it, so the constraint is discharged
          rather than overruled.
          
          It then took two passes to land, and the first one is the instructive
          failure. Moving the row to the top of the step, and then INSIDE the
          bordered card, both kept it a distinct block that merely sat nearer
          the others — because what actually separated it was never the border.
          It was the HEADING: "Choose your machine" titled the hosted tiles
          alone, so the list of machines was by definition the list the free
          option was not in. Any amount of re-boxing leaves that intact.
          
          So the heading now covers every machine, your own included, and the
          free option is the first ROW of that one list — same geometry, same
          name-over-subtitle stack, same price column, same selected state.
          "Use your own computer / Free" is a row exactly like "Small /
          $15.00/mo" is a row. The hosted rows lost their own header entirely:
          a second heading over the rest of the list would re-draw the division
          this removes, so what it carried is said once beneath the list.
          
          Two smaller consequences. The px-[36px] alignment hack is gone — it
          existed only to cancel the cloud card's padding from outside it, and
          inside one list plain px-4 puts every price in one column by
          construction. And the row's Monitor icon is gone: an icon on one row
          and none on the others is precisely the shape difference that made
          this option read as a different kind of answer. */}
      {/* 560px, matching the checkout step's column. The card itself stays at
          the shared 840px (see stepMaxWidth — every step shares one width so
          the card never resizes between steps); what changes is the measure of
          the content inside it. A single column of full-width rows wants a
          readable measure, not the full card. */}
      <div className="mx-auto w-full max-w-[560px] space-y-6">
        {/* ONE box, holding ONE list, under ONE heading.
        
            The border used to belong to the cloud option, and it carried that
            option's selected state — primary when `isCloudCompute(plan.compute)`.
            It cannot mean either of those things now that the free row is one
            of the rows inside it, so it means neither: it is a neutral
            container, and selection is expressed on the ROWS, which is the
            only level at which a selection exists. */}
        <div className="space-y-4 rounded-xl border border-border/60 bg-muted/20 p-5">
          {/* ONE heading over ALL the machines, your own included.
          
              "Choose your machine" used to head the hosted tiles alone, which
              is what kept the free option outside the list no matter where the
              box was drawn: the question the heading asked excluded one of its
              own answers. The user's machine IS a machine you can choose, so
              it belongs under the heading that says so, and the heading now
              renders whether or not there are tiles beneath it. */}
          <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
            Choose your machine
          </h3>

          {/* The list — ONE flat list, rendered here rather than by PlanTiles.
          
              PlanTiles wraps its rows in its own container, so mounting it
              here would nest the priced rows one level below the free row:
              adjacent on screen, but a separate list in the DOM, and a screen
              reader would announce them as such. The free row would be a block
              beside the machine list again, which is the whole defect.
              
              So this maps the rows itself with PlanTileRow — the primitive
              PlanTiles is now also built from — and every row in this list is
              a sibling of every other. `space-y-2` is PlanTiles' own row
              spacing, kept identical so the two surfaces stay visually one
              component.
              
              This is why the loading and empty states are handled per row
              below rather than by PlanTiles: the free option must not vanish
              because the plan catalog is slow or down. It is the one machine
              that needs no catalog at all. */}
          <div className="space-y-2">
            {/* The free option, as the FIRST ROW of the machine list.
            
                It has been an outside row and then an inside row above the
                card; it is now a row IN the list, which is what "under Choose
                your machine" comes to. Everything that made it read as a
                different class of answer is gone: no icon where the tiles have
                none, the same px-4 py-3 geometry, the same name-over-subtitle
                stack, and its price in the same column.
                
                The icon went with the move and is the one loss worth naming. A
                Monitor beside the label distinguished "yours" from "ours" at a
                glance — but only by making this row shaped unlike every row it
                sits with, which is the demotion being removed. The subtitle
                carries the distinction in words instead.
                
                It is still NOT a plan: it writes no `computePlanId`, and
                picking it opens the connect instructions below rather than
                recording a purchase. Being a peer in the list is a claim about
                the QUESTION it answers, not about what it costs us. */}
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
                <span className="flex items-center gap-2 text-sm font-semibold text-foreground">
                  Use your own computer
                  {showLocal && (
                    <CheckCircle2 className="h-4 w-4 text-primary" />
                  )}
                </span>
                <span className="block text-xs text-muted-foreground">
                  Whatever CPU and memory it already has. Connect it with the
                  Reliant command line tool.
                </span>
              </span>
              <span className="flex-shrink-0 text-sm font-semibold text-emerald-500">
                Free
              </span>
            </button>

            {/* The connect instructions are NOT here — they render at the
                bottom of the step. See the block after the list.

                They were briefly inline, expanding the row like an accordion,
                which is the conventional meaning of selecting a row. The owner
                asked for them at the bottom instead, and the reason it works
                here is that selecting the free row also hides the cloud CTA:
                what sits between the row and its instructions is a short list
                and two lines of prose, not the ~800px of card-plus-button that
                made the earlier bottom-anchored version read as "the click did
                nothing". */}

            {/* The hosted machines — the REST of the same list.
            
                There is no "In the Cloud" header over them any more, and that
                is the change rather than a casualty of it. A header there
                would re-divide the list into "your option" and "our options",
                which is the separation being removed; what it used to carry
                (these are hosted, nothing to install, plans start during
                setup) is said once beneath the list, where the burst ceiling
                and the coupon coverage are already said.
                
                Picking a tile records the choice and nothing else — no intent
                is minted, no card is mounted, nothing exists at Stripe until
                the single checkout at the end of the flow. */}
            {showPlanChoice &&
              planOptions.map((option) => (
                <PlanTileRow
                  key={option.planId}
                  plan={option}
                  // NOTHING is selected while the user's own computer is the
                  // choice. `selectedPlanId` falls back to the first plan so
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
                    void updatePlan({ computePlanId: option.planId });
                  }}
                />
              ))}

            {showPlanChoice && plansQ.isLoading && (
              <div className="flex items-center gap-2 px-1 py-2 text-sm text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" /> Loading machines…
              </div>
            )}
          </div>

          {showPlanChoice && (
            <div className="space-y-2">
              {/* What the hosted rows are, said once, under the list.
              
                  This is the surviving half of the old cloud card's header.
                  Eligibility does not change whether the user may choose, so
                  it only changes what they are told to expect: an entitled
                  user's machine starts at the end of setup, while an
                  un-entitled one passes through payment on the way. Both are
                  true statements about the same button. */}
              <p className="text-xs leading-relaxed text-muted-foreground">
                {!HAS_CLOUD_DAEMONS
                  ? "Hosted machines are not available in this setup."
                  : loading || machineCovered
                    ? // No longer says "monthly plans start during setup" to
                      // someone whose plan is already covered — that line
                      // survived a coupon redemption and told the user they
                      // were about to be charged for the thing they had just
                      // paid for with a code.
                      "The paid machines are ours: nothing to install, and we start one for you, ready in a few minutes. Setup continues while it boots."
                    : "Paid machines run in Reliant Cloud, and allow access from any device, including mobile."}
              </p>
              {/* The hours, said ONCE — for the same reason the burst ceiling
                  below is said once. The allowance does not vary by size, so
                  it is a property of hosted machines rather than of any row,
                  and a per-row copy was reading as "a bigger machine buys more
                  hours". Sized from a real plan so it still tracks the
                  catalog. */}
              {!plansQ.isLoading && planOptions.length > 0 && (
                <p
                  className="text-xs leading-relaxed text-muted-foreground"
                  data-testid="compute-step-hours-note"
                >
                  {describeIncludedHours(planOptions[0])}
                </p>
              )}
              {/* The burst ceiling, said once for the same reason the hours
                  are: it does not vary by size, so it is a property of hosted
                  machines rather than of any row. Reserved figures are what
                  the rows print; this is the headroom above them, which is the
                  axis a build actually dies on.

                  Absent entirely if the ladder stops being uniform — see
                  machineBurst. */}
              {MACHINE_BURST &&
                !plansQ.isLoading &&
                planOptions.length > 0 && (
                  <p
                    className="text-xs leading-relaxed text-muted-foreground"
                    data-testid="compute-step-burst-note"
                  >
                    Those are the reserved figures. When a build needs more, a
                    machine can burst to {MACHINE_BURST.cpu}× the CPU and{" "}
                    {MACHINE_BURST.memory}× the memory it reserves, at no extra
                    cost.
                  </p>
                )}
              {/* Says what the coupon did, at the moment and place the
                    money used to be. Without this the page still changes
                    under the user on redeem — the price becomes "Covered" —
                    but nothing connects that to the code they just entered.
                    It also names the LIMIT, because the server enforces one:
                    a compute grant buys machine time at the free plan's size
                    (small), not a bigger machine, and a user who picks Large
                    on the strength of a coupon is refused at provisioning
                    with "your plan does not include daemon size large". */}
              {machineCovered &&
                !plansQ.isLoading &&
                planOptions.length > 0 && (
                  <p
                    className="text-xs leading-relaxed text-primary"
                    data-testid="compute-step-coverage-note"
                  >
                    Your code covers the {coveredSizeLabel} machine — no charge
                    today. Larger machines need a monthly plan, and you can
                    upgrade any time.
                  </p>
                )}
              {!plansQ.isLoading && planOptions.length === 0 && (
                <p
                  className="text-xs leading-relaxed text-muted-foreground"
                  data-testid="compute-step-plans-unavailable"
                >
                  We couldn&apos;t load the plans just now — that&apos;s on our
                  end, not your setup. You can still continue, or redeem a code
                  below.
                </p>
              )}
            </div>
          )}

          {/* The cloud CTA, and it is ABSENT while the free option is chosen.
          
              It is the commit for hosted compute, so with "Use your own
              computer" selected it was a primary button that contradicted the
              selection directly above it — the page showed a machine chosen
              and, beneath it, one button offering to choose a different one.
              Worse, it was the ONLY primary button on screen, so it read as
              the way forward; clicking it silently discarded the free choice
              and bought a machine.
              
              Hiding it leaves the free path with no button here at all, which
              is correct: that path advances from the connect panel at the
              bottom (Continue, when a daemon is already running) or
              automatically when a daemon connects. Nothing is stranded. */}
          {loading ? (
            <div className="inline-flex w-full items-center justify-center gap-2 rounded-lg bg-muted px-4 py-2.5 text-sm font-semibold text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Checking availability...
            </div>
          ) : canChooseCloud && !showLocal ? (
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
              Hosted machines are not available in this setup. Choose &ldquo;Use
              your own computer&rdquo; to continue — it is free.
            </p>
          )}

          {/* Coupon redemption is offered to everyone who is choosing a
                hosted machine — someone already entitled may still be holding
                a code, and hiding the field until they run out would mean
                redeeming it requires first spending down.

                It is ABSENT while "Use your own computer" is selected, for a
                weaker reason than the CTA above. That button contradicted the
                selection and destroyed it on click; this field does neither —
                redeeming leaves `showLocal` untouched, so the choice
                survives. The argument is relevance: a compute coupon buys
                machine time the user has just declined to use.

                The thing that makes this safe is not visible on this step:
                the field also accepts WALLET_CREDIT codes, not just compute
                codes, so hiding it here could have removed the only way to
                redeem an AI-credit code. It does not, because the local-daemon
                path goes straight to the model step next, which carries its
                own coupon field.

                What is GONE entirely is the "Set up billing" / "View plans"
                button that sat beside it. It navigated to /settings/billing —
                a full exit from a wizard whose state lives in a URL search
                param, needing a `returnTo` round-trip to get back. Prices are
                now shown on the checkout step, which is inside the flow, so
                there is nothing left for it to do. */}
          {HAS_CLOUD_DAEMONS && !loading && !showLocal && (
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

        {/* ── What to do next, at the BOTTOM of the step ────────────────
        
            Selecting "Use your own computer" is only half an answer: the
            machine still has to be connected, and this is where that happens.
            It sits outside the options box, after everything, because it is no
            longer part of choosing — it is the consequence of having chosen.
            
            Bottom-anchoring was tried once before and reverted, so the
            difference matters. Then, the free row was at the top and the
            entire cloud card sat between it and this panel; the click appeared
            inert because its result was most of a screen away. Now the cloud
            CTA is hidden whenever this panel is showing, so the gap is a short
            list and two lines of prose. Same position, different distance.
            
            The two states are distinct answers, not a loading ladder: a daemon
            is ALREADY running (confirm and go), or one is not (download,
            token, wait). */}
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
                the download/token/start UI and the "waiting to connect"
                state. */}
            <SelfHostedDaemonConnect onConnected={handleLocalContinue} />
          </div>
        )}

        {/* No inline error slot any more. The only errors this step could
            raise came from provisioning, and it no longer provisions —
            choosing is local and cannot fail. Provisioning failures surface at
            the commit point, where the retry lives. */}
      </div>
    </div>
  );
}
