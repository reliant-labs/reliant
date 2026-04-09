import React from "react";

interface FormWizardProps {
  steps: Array<{ title: string; content: React.ReactNode }>;
  currentStep?: number;
}

export default function FormWizard({
  steps,
  currentStep = 0,
}: FormWizardProps) {
  const clampedStep = Math.max(0, Math.min(currentStep, steps.length - 1));

  return (
    <div className="mx-auto max-w-3xl px-6 py-10">
      {/* Step indicator */}
      <nav className="mb-10">
        <ol className="flex items-center">
          {steps.map((step, i) => {
            const isCompleted = i < clampedStep;
            const isCurrent = i === clampedStep;

            return (
              <li
                key={i}
                className={`flex items-center ${
                  i < steps.length - 1 ? "flex-1" : ""
                }`}
              >
                <div className="flex items-center gap-2">
                  <span
                    className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-full text-sm font-medium ${
                      isCompleted
                        ? "bg-indigo-600 text-white"
                        : isCurrent
                          ? "border-2 border-indigo-600 text-indigo-600"
                          : "border-2 border-gray-300 text-gray-400"
                    }`}
                  >
                    {isCompleted ? "\u2713" : i + 1}
                  </span>
                  <span
                    className={`hidden text-sm font-medium sm:inline ${
                      isCurrent ? "text-indigo-600" : "text-gray-500"
                    }`}
                  >
                    {step.title}
                  </span>
                </div>

                {i < steps.length - 1 && (
                  <div
                    className={`mx-3 h-0.5 flex-1 ${
                      isCompleted ? "bg-indigo-600" : "bg-gray-300"
                    }`}
                  />
                )}
              </li>
            );
          })}
        </ol>
      </nav>

      {/* Step content */}
      <div className="rounded-xl border border-gray-200 bg-white p-8">
        {steps[clampedStep]?.content}
      </div>
    </div>
  );
}
