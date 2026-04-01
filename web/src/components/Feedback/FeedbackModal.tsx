import { useCallback, useEffect, useMemo, useState } from 'react';
import { AlertCircle, CheckCircle2, Send } from 'lucide-react';
import { Modal } from '../ui/Modal';
import { Button } from '../ui/Button';
import { Textarea } from '../ui/Textarea';
import { cn } from '../../lib/utils';
import { submitFeedback } from '../../lib/feedback';
import { useFeedbackModalStore } from '../../store/feedbackModalStore';

function safeStringify(value: unknown) {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

export function FeedbackModal() {
  const isOpen = useFeedbackModalStore((s) => s.isOpen);
  const prefill = useFeedbackModalStore((s) => s.prefill);
  const close = useFeedbackModalStore((s) => s.close);

  const [includeDiagnostics, setIncludeDiagnostics] = useState(true);
  const [userNotes, setUserNotes] = useState('');
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; message: string } | null>(null);

  const { type, title, description, extraContext } = useMemo(() => {
    return {
      type: prefill?.type ?? 'bug',
      title: prefill?.title,
      description: prefill?.description,
      extraContext: prefill?.extraContext,
    };
  }, [prefill]);

  // If we open with no diagnostics in prefill, reflect that in the checkbox.
  // (Avoids showing diagnostics as enabled when we intentionally omitted them due to privacy settings.)
  useEffect(() => {
    if (!isOpen) return;
    if (!prefill) return;

    if (!prefill.extraContext || Object.keys(prefill.extraContext).length === 0) {
      setIncludeDiagnostics(false);
    } else {
      setIncludeDiagnostics(true);
    }
  }, [isOpen, prefill]);

  const effectiveTitle = title?.trim() || 'App crash';
  const effectiveDescription = useMemo(() => {
    // Keep this short and user-friendly. Diagnostics live in extraContext.
    const notes = userNotes.trim();
    const base = description?.trim();
    if (notes) return notes;
    if (base) return base;
    return 'Crash report submitted from the error screen.';
  }, [description, userNotes]);

  const handleClose = useCallback(() => {
    setResult(null);
    setIsSubmitting(false);
    setUserNotes('');
    setIncludeDiagnostics(true);
    close();
  }, [close]);

  const handleSubmit = useCallback(async () => {
    setIsSubmitting(true);
    setResult(null);

    const submissionExtraContext = includeDiagnostics ? (extraContext ?? {}) : {};

    const res = await submitFeedback({
      type,
      title: effectiveTitle,
      description: effectiveDescription,
      extraContext: submissionExtraContext,
    });

    setIsSubmitting(false);

    if (res.success) {
      // Close immediately after successful submission to reduce friction.
      handleClose();
      return;
    }

    setResult({ ok: false, message: res.error ?? 'Failed to send report. Please try again.' });
  }, [effectiveDescription, effectiveTitle, extraContext, includeDiagnostics, type, handleClose]);

  return (
    <Modal isOpen={isOpen} onClose={handleClose} title="Report a problem" size="lg">
      <div className="space-y-5">
        <div className="flex items-center gap-3">
          <div className="flex h-9 w-9 items-center justify-center rounded-full bg-destructive/10 text-destructive">
            <AlertCircle className="h-5 w-5" />
          </div>
          <div className="flex-1">
            <p className="text-sm text-foreground">
              Send an error report to help us investigate and fix this.
            </p>

          </div>
        </div>

        <div className="rounded-lg border border-border/60 bg-muted/20 p-3">
          <div className="flex items-center justify-between gap-4">
            <div className="min-w-0">
              <p className="text-xs text-muted-foreground">Report title</p>
              <p className="truncate text-sm font-medium text-foreground">{effectiveTitle}</p>
            </div>
            <label className="flex items-center gap-2 text-xs text-muted-foreground select-none">
              <input
                type="checkbox"
                className="h-4 w-4"
                checked={includeDiagnostics}
                onChange={(e) => setIncludeDiagnostics(e.target.checked)}
              />
              Include diagnostics
            </label>
          </div>

          <div className="mt-3">
            <p className="text-xs text-muted-foreground mb-2">Optional notes</p>
            <Textarea
              value={userNotes}
              onChange={(e) => setUserNotes(e.target.value)}
              rows={4}
              placeholder="What were you doing when it crashed? (optional)"
              className="resize-none"
            />
          </div>

          {includeDiagnostics ? (
            <details className="mt-3">
              <summary className="cursor-pointer select-none text-xs text-muted-foreground hover:text-foreground">
                Diagnostics preview
              </summary>
              <pre className={cn(
                'mt-2 max-h-48 overflow-auto rounded-md border border-border/60 bg-background p-2 text-[11px] leading-relaxed',
                'text-foreground'
              )}>
                {safeStringify(extraContext ?? {})}
              </pre>
            </details>
          ) : null}
        </div>

        {result ? (
          <div
            className={cn(
              'rounded-lg border p-3 text-sm',
              result.ok
                ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-200'
                : 'border-destructive/30 bg-destructive/10 text-destructive'
            )}
          >
            <div className="flex items-start gap-2">
              {result.ok ? (
                <CheckCircle2 className="h-4 w-4 mt-0.5" />
              ) : (
                <AlertCircle className="h-4 w-4 mt-0.5" />
              )}
              <span className="flex-1">{result.message}</span>
            </div>
          </div>
        ) : null}

        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={handleClose}>
            Close
          </Button>
          <Button
            variant="primary"
            onClick={handleSubmit}
            loading={isSubmitting}
            leftIcon={<Send className="h-4 w-4" />}
          >
            Send report
          </Button>
        </div>
      </div>
    </Modal>
  );
}
