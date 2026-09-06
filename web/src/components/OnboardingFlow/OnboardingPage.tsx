import { ChevronLeft } from 'lucide-react';
import { cn } from '@/lib/utils';
import { ProgressBar } from './ProgressBar';
import { OnboardingEscapeHatch } from './OnboardingEscapeHatch';
import { useOnboardingPlan } from './useOnboardingPlan';
import { useOnboardingFacts } from './useOnboardingFacts';
import { BACK_CLEARS, deriveStep, stepMaxWidth, visibleStepsForPlan, STEP_COMPONENTS, STEP_LABELS } from './stepConfig';
import { useOnboardingTracking } from './analytics';
import { useTitleBarChrome } from '@/hooks/useTitleBarChrome';
import type { LaunchPlan } from './types';
// Ensure step components are registered on module load
import './steps';

export function OnboardingPage() {
  const { plan, updatePlan } = useOnboardingPlan();
  // The onboarding overlay sits at z-40 over the normal app chrome, so the
  // Layout/Header's drag region is occluded. Render our own drag strip across
  // the top of the window so users can still move the Electron window from
  // the title-bar area while onboarding is open. No-op in web.
  const { isElectron, dragRegionStyle } = useTitleBarChrome();

  // Current step is derived purely from (plan, facts). No URL `step` param, no
  // sessionStorage flags, no useEffect to sync. See ./stepConfig.ts.
  //
  // The facts — compute eligibility and wallet balance — are the ONLY inputs
  // that are not in the URL, and they are here rather than in the plan because
  // they are server-owned and change under us. They read pessimistic while
  // loading, so a still-settling query lands the user on the checkout step
  // rather than past it.
  const facts = useOnboardingFacts();
  const actualStep = deriveStep(plan, facts);
  // Hides an auto-skipped compute step, but never the step the user is on —
  // see visibleStepsForPlan. That guarantee is what makes the index below a
  // plain lookup rather than a -1 swallowed by Math.max.
  const steps = visibleStepsForPlan(plan, facts);

  const currentIndex = steps.indexOf(actualStep);
  const StepComponent = STEP_COMPONENTS[actualStep];

  // Back is suppressed on the first VISIBLE step, which after an auto-skip is
  // the model step. Without this the button renders and does nothing you can
  // see: BACK_CLEARS['model'] clears `compute`, derivation returns to the
  // compute step, and its auto-skip effect immediately advances again.
  const isFirst = currentIndex <= 0;
  const progressSteps = steps.map(id => ({ id, label: STEP_LABELS[id] }));

  useOnboardingTracking(actualStep);

  // onNext is a legacy no-op: step components call it after updatePlan() as
  // an "I'm done" signal, but derivation handles transitions automatically
  // once the plan changes. Kept on the prop type for compatibility.
  const onNext = () => {};

  // Back undoes the plan fields that drove derivation to the current step.
  const onBack = () => {
    const fields = BACK_CLEARS[actualStep];
    if (fields.length === 0) return;
    const updates = Object.fromEntries(
      fields.map(k => [k, undefined]),
    ) as Partial<LaunchPlan>;
    void updatePlan(updates);
  };

  // A missing step component is a bug either way — the registry is filled by a
  // module-scope side effect of importing './steps' and is typed through an
  // assertion, so nothing type-checks it being complete. What must not happen
  // is returning null here: that skips the card, and the card is what holds
  // Back, Settings and Sign out. Onboarding suppresses the app Header and
  // renders above every other surface, so a user who lands on an unregistered
  // step would have no route to settings and no way to sign out — the exact
  // dead end the escape hatch exists to close. Render the chrome; say so in
  // the step slot.
  return (
    <div
      className="fixed inset-0 z-40 flex items-center justify-center overflow-hidden p-4"
      role="dialog"
      aria-modal="true"
      aria-label="Onboarding setup"
    >
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-[radial-gradient(circle_at_20%_20%,rgba(14,165,233,0.32),transparent_32%),radial-gradient(circle_at_78%_12%,rgba(168,85,247,0.28),transparent_30%),radial-gradient(circle_at_52%_88%,rgba(16,185,129,0.22),transparent_34%),linear-gradient(135deg,rgba(2,6,23,0.94),rgba(24,24,27,0.88)_45%,rgba(30,41,59,0.95))] backdrop-blur-xl"
        aria-hidden="true"
      />
      <div className="absolute -left-24 top-10 h-64 w-64 rounded-full bg-sky-400/20 blur-3xl" aria-hidden="true" />
      <div className="absolute -right-16 top-1/3 h-72 w-72 rounded-full bg-fuchsia-500/20 blur-3xl" aria-hidden="true" />
      <div className="absolute bottom-0 left-1/3 h-64 w-64 rounded-full bg-emerald-400/15 blur-3xl" aria-hidden="true" />

      {/* Electron-only drag strip across the top of the window. Behind the
          card (no z-index bump) so it doesn't steal clicks from the card's
          content, but in front of the backdrop so the OS sees the drag. */}
      {isElectron && (
        <div
          className="absolute inset-x-0 top-0 h-9"
          style={dragRegionStyle}
          aria-hidden="true"
        />
      )}

      {/* Card */}
      <div
        className={cn(
          "relative w-full rounded-[1.35rem] border border-white/15 bg-background/88 font-sans shadow-[0_28px_90px_rgba(2,6,23,0.55)] backdrop-blur-2xl flex flex-col overflow-hidden",
          stepMaxWidth(actualStep),
          "transition-[max-width] duration-300",
          "max-h-[calc(100vh-80px)]",
          "before:absolute before:inset-x-0 before:top-0 before:h-px before:bg-gradient-to-r before:from-transparent before:via-white/50 before:to-transparent",
          "animate-in fade-in-0 zoom-in-98 duration-300"
        )}
      >
        <div className="absolute inset-x-0 top-0 h-40 bg-[radial-gradient(circle_at_18%_0%,rgba(14,165,233,0.18),transparent_38%),radial-gradient(circle_at_82%_0%,rgba(168,85,247,0.16),transparent_34%)]" aria-hidden="true" />

        {/* Progress bar, plus the way out. The escape hatch lives on this row
            because onboarding suppresses the app Header and renders above
            every other surface — without it, a user whose setup cannot
            complete has no route to settings and no way to sign out. */}
        <div className="relative flex items-center justify-between gap-4 border-b border-white/10 bg-white/[0.035] px-6 py-4">
          <div className="flex-1">
            <ProgressBar steps={progressSteps} currentStepIndex={currentIndex} />
          </div>
          <OnboardingEscapeHatch className="flex-shrink-0" />
        </div>

        {/* Step content */}
        <div className="relative flex-1 overflow-y-auto bg-[linear-gradient(180deg,rgba(255,255,255,0.035),rgba(255,255,255,0)_28%),hsl(var(--surface-modal))]">
          <div className="px-8 py-8">
            {StepComponent ? (
              <StepComponent
                plan={plan}
                updatePlan={updatePlan}
                onNext={onNext}
                onBack={onBack}
              />
            ) : (
              <div className="space-y-3 text-center" role="alert">
                <h2 className="text-lg font-semibold text-foreground">
                  This step didn&apos;t load
                </h2>
                <p className="text-sm text-muted-foreground">
                  Something went wrong setting up this part of onboarding. Try
                  going back, or open Settings to continue.
                </p>
              </div>
            )}
          </div>
        </div>

        {/* Footer */}
        <div className="relative flex items-center justify-between border-t border-white/10 bg-background/80 px-6 py-4 backdrop-blur">
          <div>
            {!isFirst && (
              <button
                type="button"
                onClick={onBack}
                className="flex items-center gap-1 rounded-lg border border-white/10 bg-white/[0.03] px-4 py-2 text-sm transition-colors hover:border-sky-400/40 hover:bg-sky-400/10"
              >
                <ChevronLeft className="w-4 h-4" /> Back
              </button>
            )}
          </div>
          <div />
        </div>
      </div>
    </div>
  );
}