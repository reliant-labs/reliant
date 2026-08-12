import { useEffect, useMemo } from "react";
import { useSurface } from "../../lib/surfaceContext";
import { useTourStore } from "../../store/tourStore";
import { useContextualTipsStore } from "../../store/contextualTipsStore";
import { CONTEXTUAL_TIP_DEFINITIONS } from "./contextualTipsRegistry";
import { ContextualTipCoachmark } from "./ContextualTipCoachmark";
import { useTourNavigation } from "./useTourNavigation";

export function ContextualTipsLayer() {
  // Coachmarks anchor to desktop chrome via DOM selectors that never match on
  // the mobile surface, so a tip there would either float unanchored or point
  // at nothing. See MobileLayout for the full overlay rationale.
  const surface = useSurface();
  const isInitialized = useContextualTipsStore((state) => state.isInitialized);
  const loadFailed = useContextualTipsStore((state) => state.loadFailed);
  const activeTipId = useContextualTipsStore((state) => state.activeTipId);
  const loadState = useContextualTipsStore((state) => state.loadState);
  const reevaluate = useContextualTipsStore((state) => state.reevaluate);
  const confirmTipShown = useContextualTipsStore((state) => state.confirmTipShown);
  const clearActiveTip = useContextualTipsStore((state) => state.clearActiveTip);
  const dismissTip = useContextualTipsStore((state) => state.dismissTip);
  const disableAllTips = useContextualTipsStore((state) => state.disableAllTips);
  const subscribeToSources = useContextualTipsStore((state) => state.subscribeToSources);
  const onboardingReady = useTourStore((state) => state.isInitialized);
  const onboardingComplete = useTourStore((state) => state.hasCompletedOnboarding);
  // isWizardActive is URL-derived: presence of ?tour=<step> means tour is on.
  // Reading from the navigation hook keeps tour visibility in lock-step with
  // the URL — no chance for store/URL drift.
  const { isWizardActive } = useTourNavigation();

  useEffect(() => {
    if (!onboardingReady) return;
    void loadState();
  }, [loadState, onboardingReady]);

  useEffect(() => {
    if (!isInitialized) return;
    const unsubscribe = subscribeToSources();
    return unsubscribe;
  }, [isInitialized, subscribeToSources]);

  useEffect(() => {
    if (!isInitialized) return;
    void reevaluate();
  }, [isInitialized, reevaluate, onboardingComplete, isWizardActive]);

  const activeTip = useMemo(() => {
    if (!activeTipId) return null;
    return CONTEXTUAL_TIP_DEFINITIONS.find((tip) => tip.id === activeTipId) ?? null;
  }, [activeTipId]);

  if (surface !== "desktop") {
    return null;
  }

  if (!onboardingReady || !onboardingComplete || isWizardActive || loadFailed || !activeTip) {
    return null;
  }

  return (
    <ContextualTipCoachmark
      key={activeTip.id}
      targetSelector={activeTip.targetSelector}
      title={activeTip.title}
      body={activeTip.body}
      onDismiss={() => void dismissTip(activeTip.id)}
      onDisableAll={() => void disableAllTips()}
      onConfirmShown={() => confirmTipShown(activeTip.id)}
      onTargetMissing={clearActiveTip}
    />
  );
}