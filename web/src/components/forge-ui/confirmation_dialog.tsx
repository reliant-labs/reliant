import React, { useEffect, useId, useRef } from "react";

interface ConfirmationDialogProps {
  open: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  title: string;
  description?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  variant?: "danger" | "warning";
  loading?: boolean;
}

const variantStyles = {
  danger: {
    icon: "bg-danger-surface text-danger",
    button: "bg-danger hover:bg-danger-hover focus:ring-danger",
    iconPath: "M6 18L18 6M6 6l12 12",
  },
  warning: {
    icon: "bg-warning-surface text-warning",
    button: "bg-warning hover:bg-warning-hover focus:ring-warning",
    iconPath:
      "M12 9v3.75m-9.303 3.376c-.866 1.5.217 3.374 1.948 3.374h14.71c1.73 0 2.813-1.874 1.948-3.374L13.949 3.378c-.866-1.5-3.032-1.5-3.898 0L2.697 16.126ZM12 15.75h.007v.008H12v-.008Z",
  },
};

export default function ConfirmationDialog({
  open,
  onConfirm,
  onCancel,
  title,
  description,
  confirmLabel = "Confirm",
  cancelLabel = "Cancel",
  variant = "danger",
  loading = false,
}: ConfirmationDialogProps) {
  const titleId = useId();
  const descriptionId = useId();
  const confirmRef = useRef<HTMLButtonElement>(null);

  // Escape cancels. A modal that traps a user in front of an irreversible
  // action with no keyboard way out is the worst place to omit this, and
  // Escape is the gesture every other dialog on the platform honours.
  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || loading) return;
      event.preventDefault();
      onCancel();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, loading, onCancel]);

  // Move focus into the dialog when it opens. Without this, focus stays on the
  // element that triggered it, so a screen-reader user is never told the
  // dialog appeared and a keyboard user tabs through the page behind it.
  useEffect(() => {
    if (!open) return;
    confirmRef.current?.focus();
  }, [open]);

  if (!open) return null;

  const styles = variantStyles[variant];

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
      {/* Backdrop — a real button, so dismissing works by keyboard too */}
      <button
        type="button"
        aria-label="Dismiss dialog"
        className="fixed inset-0 bg-black/50 backdrop-blur-sm transition-opacity"
        onClick={onCancel}
      />

      {/*
       * Dialog. role + aria-modal + a labelled title are what make assistive
       * technology announce this as a modal and read the consequence aloud —
       * on a component whose entire purpose is confirming a destructive
       * action, that is correctness, not polish.
       */}
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        className="relative w-full max-w-md rounded-xl bg-surface p-6 shadow-xl"
      >
        <div className="flex gap-4">
          {/* Icon */}
          <div
            className={`flex h-10 w-10 shrink-0 items-center justify-center rounded-full ${styles.icon}`}
          >
            <svg
              className="h-5 w-5"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
              strokeWidth={2}
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                d={styles.iconPath}
              />
            </svg>
          </div>

          {/* Content */}
          <div className="min-w-0">
            <h3 id={titleId} className="text-base font-semibold text-ink">
              {title}
            </h3>
            {description && (
              <p id={descriptionId} className="mt-2 text-sm text-ink-muted">
                {description}
              </p>
            )}
          </div>
        </div>

        {/* Actions */}
        <div className="mt-6 flex justify-end gap-3">
          <button
            type="button"
            onClick={onCancel}
            disabled={loading}
            className="rounded-lg border border-border-strong bg-surface px-4 py-2 text-sm font-medium text-ink-muted shadow-sm transition hover:bg-surface-muted focus:outline-none focus:ring-2 focus:ring-border-strong focus:ring-offset-2 disabled:opacity-50"
          >
            {cancelLabel}
          </button>
          <button
            ref={confirmRef}
            type="button"
            onClick={onConfirm}
            disabled={loading}
            className={`inline-flex items-center gap-2 rounded-lg px-4 py-2 text-sm font-medium text-on-accent shadow-sm transition focus:outline-none focus:ring-2 focus:ring-offset-2 disabled:opacity-50 ${styles.button}`}
          >
            {loading && (
              <svg
                className="h-4 w-4 animate-spin"
                viewBox="0 0 24 24"
                fill="none"
              >
                <circle
                  cx="12"
                  cy="12"
                  r="10"
                  stroke="currentColor"
                  strokeWidth="4"
                  className="opacity-25"
                />
                <path
                  d="M4 12a8 8 0 018-8v4a4 4 0 00-4 4H4z"
                  fill="currentColor"
                  className="opacity-75"
                />
              </svg>
            )}
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
