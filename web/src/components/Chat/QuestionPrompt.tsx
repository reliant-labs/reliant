import { useState, useCallback, useRef, useEffect } from "react";
import { Check, Send, MessageSquare, ChevronRight } from "lucide-react";
import { cn } from "../../lib/utils";

interface QuestionOption {
  label: string;
  description: string;
  preview?: string;
}

export interface QuestionItem {
  question: string;
  options: QuestionOption[];
  allowMultiple: boolean;
}

export interface QuestionAnswer {
  question: string;
  selected: string[];
  freetext?: string;
}

export interface QuestionPromptProps {
  questions: QuestionItem[];
  onSubmit: (answers: { answers: QuestionAnswer[] }) => void;
}

export function QuestionPrompt({ questions, onSubmit }: QuestionPromptProps) {
  const totalSteps = questions.length;
  const isMultiStep = totalSteps > 1;

  const [currentStep, setCurrentStep] = useState(0);
  const [answers, setAnswers] = useState<QuestionAnswer[]>(() =>
    questions.map((q) => ({ question: q.question, selected: [], freetext: undefined }))
  );
  const [isOtherActive, setIsOtherActive] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const freetextRef = useRef<HTMLTextAreaElement>(null);

  const current = questions[currentStep];
  const currentAnswer = answers[currentStep];
  const selected = new Set(currentAnswer.selected);
  const freetext = currentAnswer.freetext ?? "";
  const hasOptions = current.options.length > 0;
  const isLastStep = currentStep === totalSteps - 1;

  const setCurrentAnswer = useCallback(
    (updater: (prev: QuestionAnswer) => QuestionAnswer) => {
      setAnswers((prev) => {
        const next = [...prev];
        next[currentStep] = updater(next[currentStep]);
        return next;
      });
    },
    [currentStep]
  );

  const toggleOption = useCallback(
    (label: string) => {
      setCurrentAnswer((prev) => {
        const s = new Set(prev.selected);
        if (s.has(label)) {
          s.delete(label);
        } else {
          if (!current.allowMultiple) {
            s.clear();
          }
          s.add(label);
        }
        return { ...prev, selected: Array.from(s) };
      });
      if (!current.allowMultiple) {
        // Clear freetext when selecting a predefined option in single-select
        setIsOtherActive(false);
        setCurrentAnswer((prev) => ({ ...prev, freetext: undefined }));
      }
    },
    [current.allowMultiple, setCurrentAnswer]
  );

  const setFreetext = useCallback(
    (value: string) => {
      setCurrentAnswer((prev) => ({ ...prev, freetext: value }));
    },
    [setCurrentAnswer]
  );

  const canAdvance =
    !submitting &&
    (selected.size > 0 || (isOtherActive && freetext.trim().length > 0));

  const handleNext = useCallback(() => {
    if (!canAdvance) return;
    setCurrentStep((s) => s + 1);
    // Reset "Other" state for next question
    setIsOtherActive(false);
  }, [canAdvance]);

  const handleBack = useCallback(() => {
    setCurrentStep((s) => Math.max(0, s - 1));
    setIsOtherActive(false);
  }, []);

  const handleSubmit = useCallback(() => {
    if (!canAdvance) return;
    setSubmitting(true);
    // Finalize the current answer's freetext
    const finalAnswers = answers.map((a) => {
      const ans: QuestionAnswer = {
        question: a.question,
        selected: a.selected,
      };
      if (a.freetext?.trim()) {
        ans.freetext = a.freetext.trim();
      }
      return ans;
    });
    onSubmit({ answers: finalAnswers });
  }, [canAdvance, answers, onSubmit]);

  const handleFreetextKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        if (isLastStep) {
          handleSubmit();
        } else {
          handleNext();
        }
      }
    },
    [isLastStep, handleSubmit, handleNext]
  );

  // Restore "Other" active state when navigating back
  useEffect(() => {
    setIsOtherActive(!!currentAnswer.freetext);
  }, [currentStep]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="px-2 py-3 max-h-[40vh] overflow-y-auto overscroll-contain">
      {/* Step indicator (multi-question only) */}
      {isMultiStep && (
        <div className="flex items-center gap-1.5 mb-2">
          {questions.map((_, i) => (
            <span
              key={i}
              className={cn(
                "w-1.5 h-1.5 rounded-full transition-colors",
                i === currentStep
                  ? "bg-primary"
                  : i < currentStep
                    ? "bg-primary/40"
                    : "bg-muted-foreground/25"
              )}
            />
          ))}
          <span className="text-[11px] text-muted-foreground ml-1.5">
            {currentStep + 1} / {totalSteps}
          </span>
        </div>
      )}

      {/* Question text */}
      <p className="text-sm font-medium text-foreground mb-3 leading-relaxed">
        {current.question}
      </p>

      <div>
        {/* Options + Other + Navigation */}
        <div className="flex flex-col gap-1.5 min-w-0 w-full">
          {/* Option buttons */}
          {hasOptions &&
            current.options.map((option) => {
              const isSelected = selected.has(option.label);
              return (
                <button
                  key={option.label}
                  type="button"
                  onClick={() => toggleOption(option.label)}
                  className={cn(
                    "group flex items-start gap-2.5 w-full text-left rounded-lg px-3 py-2",
                    "border transition-all duration-150 cursor-pointer",
                    "focus:outline-none focus:ring-2 focus:ring-primary/40 focus:ring-offset-1 focus:ring-offset-background",
                    isSelected
                      ? "border-primary/50 bg-primary/10"
                      : "border-border/50 hover:border-border hover:bg-muted/50"
                  )}
                  aria-pressed={isSelected}
                  role={current.allowMultiple ? "checkbox" : "radio"}
                  aria-checked={isSelected}
                >
                  {/* Selection indicator */}
                  <span
                    className={cn(
                      "flex-shrink-0 mt-0.5 flex items-center justify-center rounded",
                      "w-4 h-4 border transition-colors duration-150",
                      current.allowMultiple ? "rounded-[3px]" : "rounded-full",
                      isSelected
                        ? "bg-primary border-primary text-primary-foreground"
                        : "border-muted-foreground/40 group-hover:border-muted-foreground/60"
                    )}
                  >
                    {isSelected && <Check className="w-2.5 h-2.5" strokeWidth={3} />}
                  </span>

                  {/* Label + Description */}
                  <div className="min-w-0 flex-1">
                    <span className={cn(
                      "text-sm font-medium block",
                      isSelected ? "text-foreground" : "text-foreground/80"
                    )}>
                      {option.label}
                    </span>
                    {option.description && (
                      <span className="text-xs text-muted-foreground leading-relaxed block mt-0.5">
                        {option.description}
                      </span>
                    )}
                  </div>
                </button>
              );
            })}

          {/* Freetext input — always visible, acts as "Other" */}
          <textarea
            ref={freetextRef}
            value={freetext}
            onChange={(e) => {
              setFreetext(e.target.value);
              // Typing activates freetext mode
              if (e.target.value && !isOtherActive) {
                setIsOtherActive(true);
                if (!current.allowMultiple) {
                  setCurrentAnswer((a) => ({ ...a, selected: [] }));
                }
              }
              // Clearing text deactivates it
              if (!e.target.value) {
                setIsOtherActive(false);
              }
            }}
            onKeyDown={handleFreetextKeyDown}
            placeholder="Or type your own answer..."
            rows={1}
            className={cn(
              "w-full rounded-md border bg-background px-3 py-2 text-sm",
              "placeholder:text-muted-foreground/50",
              "focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/50",
              "resize-none transition-colors",
              isOtherActive
                ? "border-primary/50"
                : "border-border/40"
            )}
          />

          {/* Navigation buttons */}
          <div className="flex items-center justify-between pt-1">
            <div>
              {isMultiStep && currentStep > 0 && (
                <button
                  type="button"
                  onClick={handleBack}
                  className={cn(
                    "text-xs text-muted-foreground hover:text-foreground",
                    "transition-colors cursor-pointer"
                  )}
                >
                  &larr; Back
                </button>
              )}
            </div>
            <button
              type="button"
              onClick={isLastStep ? handleSubmit : handleNext}
              disabled={!canAdvance}
              className={cn(
                "inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium",
                "transition-all duration-150",
                "focus:outline-none focus:ring-2 focus:ring-primary/50 focus:ring-offset-1 focus:ring-offset-background",
                canAdvance
                  ? "bg-primary text-primary-foreground hover:bg-primary/90 cursor-pointer"
                  : "bg-muted text-muted-foreground cursor-not-allowed opacity-50"
              )}
              style={canAdvance ? {
                backgroundColor: 'hsl(var(--primary))',
                color: 'hsl(var(--primary-foreground))',
              } : undefined}
            >
              {submitting ? (
                <MessageSquare className="w-3.5 h-3.5 animate-pulse" />
              ) : isLastStep ? (
                <Send className="w-3.5 h-3.5" />
              ) : (
                <ChevronRight className="w-3.5 h-3.5" />
              )}
              {submitting ? "Submitting..." : isLastStep ? "Submit" : "Next"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}