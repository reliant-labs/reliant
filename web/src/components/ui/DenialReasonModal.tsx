import { useState, useEffect } from "react";
import { Modal } from "./Modal";
import { XCircle } from "lucide-react";

interface DenialReasonModalProps {
  isOpen: boolean;
  onClose: () => void;
  onConfirm: (reason: string) => void;
  title?: string;
  description?: string;
  isMultiple?: boolean;
}

export function DenialReasonModal({
  isOpen,
  onClose,
  onConfirm,
  title = "Deny Tool Request",
  description,
  isMultiple = false,
}: DenialReasonModalProps) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    if (!isOpen) {
      setReason("");
      setError("");
    }
  }, [isOpen]);

  const handleConfirm = () => {
    const trimmedReason = reason.trim();
    if (!trimmedReason) {
      setError("Please provide a reason for denial");
      return;
    }
    if (trimmedReason.length < 5) {
      setError("Please provide a more detailed reason (at least 5 characters)");
      return;
    }
    onConfirm(trimmedReason);
    onClose();
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={title}
      size="md"
    >
      <div className="space-y-6">
        <div className="flex items-start gap-4">
          <div className="p-3 rounded-xl bg-destructive/5 ring-1 ring-destructive/20">
            <XCircle className="w-6 h-6 text-destructive" />
          </div>
          <div className="flex-1 pt-1">
            <p className="text-sm text-foreground leading-relaxed">
              {description ||
                (isMultiple
                  ? "Please provide a reason for denying all pending tool requests. This will help improve future interactions."
                  : "Please provide a reason for denying this tool request. This will help the AI understand your preferences.")}
            </p>
          </div>
        </div>

        <div className="space-y-3">
          <label
            htmlFor="denial-reason"
            className="block text-sm font-semibold text-foreground"
          >
            Denial Reason <span className="text-destructive">*</span>
          </label>
          <textarea
            id="denial-reason"
            value={reason}
            onChange={(e) => {
              setReason(e.target.value);
              setError("");
            }}
            placeholder="Explain why this tool request is being denied..."
            className={`w-full px-4 py-3 border rounded-lg text-sm transition-colors
              ${
                error
                  ? "border-destructive/30 focus:border-destructive focus:ring-destructive/20"
                  : "border-border focus:border-primary focus:ring-primary/20"
              }
              bg-background
              text-foreground
              placeholder:text-muted-foreground/60 placeholder:font-normal placeholder:italic
              focus:outline-none focus:ring-4 resize-none`}
            rows={4}
            autoFocus
          />
          {error && (
            <p className="text-sm text-destructive flex items-center gap-2">
              <XCircle className="w-4 h-4" />
              {error}
            </p>
          )}
        </div>

        <div className="flex justify-end gap-3 pt-4 border-t border-border">
          <button
            onClick={onClose}
            className="px-5 py-2.5 text-sm font-medium text-foreground
                     bg-muted hover:bg-muted/80
                     border border-border
                     rounded-lg transition-all
                     focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
          >
            Cancel
          </button>
          <button
            onClick={handleConfirm}
            className="px-5 py-2.5 text-sm font-semibold text-destructive-foreground
                     bg-destructive hover:bg-destructive/90
                     rounded-lg shadow-sm transition-all
                     focus:outline-none focus:ring-2 focus:ring-destructive focus:ring-offset-2 focus:ring-offset-background
                     disabled:opacity-50 disabled:cursor-not-allowed"
          >
            Deny {isMultiple ? "All" : "Request"}
          </button>
        </div>
      </div>
    </Modal>
  );
}