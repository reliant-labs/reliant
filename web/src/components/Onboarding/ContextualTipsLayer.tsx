import { useEffect, useMemo } from "react";
import { useOnboardingChecklistStore } from "../../store/onboardingChecklistStore";
import { useContextualTipsStore } from "../../store/contextualTipsStore";
import { CONTEXTUAL_TIP_DEFINITIONS } from "./contextualTipsRegistry";
import { ContextualTipCoachmark } from "./ContextualTipCoachmark";

export function ContextualTipsLayer() {
  const isInitialized = useContextualTipsStore((state) => state.isInitialized);
  const activeTipId = useContextualTipsStore((state) => state.activeTipId);
  const loadState = useContextualTipsStore((state) => state.loadState);
  const reevaluate = useContextualTipsStore((state) => state.reevaluate);
  const dismissTip = useContextualTipsStore((state) => state.dismissTip);
  const disableAllTips = useContextualTipsStore((state) => state.disableAllTips);
  const subscribeToSources = useContextualTipsStore((state) => state.subscribeToSources);
  const onboardingReady = useOnboardingChecklistStore((state) => state.isInitialized);
  const onboardingComplete = useOnboardingChecklistStore((state) => state.hasCompletedOnboarding);
  const isWizardActive = useOnboardingChecklistStore((state) => state.isWizardActive);

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

  if (!onboardingReady || !onboardingComplete || isWizardActive || !activeTip) {
    return null;
  }

  return (
    <ContextualTipCoachmark
      targetSelector={activeTip.targetSelector}
      title={activeTip.title}
      body={activeTip.body}
      onDismiss={() => void dismissTip(activeTip.id)}
      onDisableAll={() => void disableAllTips()}
    />
  );
}