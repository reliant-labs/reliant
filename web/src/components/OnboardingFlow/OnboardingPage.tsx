import { ChevronLeft } from 'lucide-react';
import { cn } from '@/lib/utils';
import { ProgressBar } from './ProgressBar';
import { useOnboardingPlan } from './useOnboardingPlan';
import { BACK_CLEARS, deriveStep, getStepsForPlan, STEP_COMPONENTS, STEP_LABELS } from './stepConfig';
import type { LaunchPlan } from './types';
// Ensure step components are registered on module load
import './steps';

export function OnboardingPage() {
  const { plan, updatePlan } = useOnboardingPlan();

  // Current step is derived purely from plan state. No URL `step` param,
  // no sessionStorage flags, no useEffect to sync. See ./stepConfig.ts.
  const actualStep = deriveStep(plan);
  const steps = getStepsForPlan(plan);
  const safeIndex = Math.max(0, steps.indexOf(actualStep));
  const StepComponent = STEP_COMPONENTS[actualStep];
  const isFirst = safeIndex === 0;
  const progressSteps = steps.map(id => ({ id, label: STEP_LABELS[id] }));

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

  if (!StepComponent) return null;

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

      {/* Card */}
      <div
        className={cn(
          "relative w-full max-w-[840px] rounded-[1.35rem] border border-white/15 bg-background/88 font-sans shadow-[0_28px_90px_rgba(2,6,23,0.55)] backdrop-blur-2xl flex flex-col overflow-hidden",
          "max-h-[calc(100vh-80px)]",
          "before:absolute before:inset-x-0 before:top-0 before:h-px before:bg-gradient-to-r before:from-transparent before:via-white/50 before:to-transparent",
          "animate-in fade-in-0 zoom-in-98 duration-300"
        )}
      >
        <div className="absolute inset-x-0 top-0 h-40 bg-[radial-gradient(circle_at_18%_0%,rgba(14,165,233,0.18),transparent_38%),radial-gradient(circle_at_82%_0%,rgba(168,85,247,0.16),transparent_34%)]" aria-hidden="true" />

        {/* Progress bar */}
        <div className="relative flex items-center justify-between border-b border-white/10 bg-white/[0.035] px-6 py-4">
          <div className="flex-1">
            <ProgressBar steps={progressSteps} currentStepIndex={safeIndex} />
          </div>
        </div>

        {/* Step content */}
        <div className="relative flex-1 overflow-y-auto bg-[linear-gradient(180deg,rgba(255,255,255,0.035),rgba(255,255,255,0)_28%),hsl(var(--surface-modal))]">
          <div className="px-8 py-8">
            <StepComponent
              plan={plan}
              updatePlan={updatePlan}
              onNext={onNext}
              onBack={onBack}
            />
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