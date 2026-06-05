import { useState } from "react";
import { Mail, ExternalLink } from "lucide-react";
import { Modal } from "./ui/Modal";
import { BillingService } from "@/gen/controlplane/v1/public/billing_service_pb";
import { getControlPlaneClient } from "../services/controlPlane/client";
import { logger } from "../lib/logger";
import type { BillingEmailRequiredData } from "../store/modalStore";

export interface BillingEmailRequiredModalProps {
  isOpen: boolean;
  onClose: () => void;
  data: BillingEmailRequiredData;
}

/**
 * BillingEmailRequiredModal prompts the user for a billing email when the
 * backend responds with x-reliant-reason=billing_email_missing. Calls
 * BillingService.UpdateBillingEmail (control-plane) and closes on success,
 * letting the caller retry the action that originally failed.
 *
 * Mirrors the UpgradeRequiredModal pattern: ModalLayer mounts it; the
 * connect interceptor opens it; the user's save call goes back through the
 * same control-plane transport (which carries auth + tracing).
 */
export function BillingEmailRequiredModal({
  isOpen,
  onClose,
  data,
}: BillingEmailRequiredModalProps) {
  const [email, setEmail] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const handleSave = async () => {
    const trimmed = email.trim();
    if (!trimmed) {
      setError("Please enter an email address.");
      return;
    }
    setSaving(true);
    setError("");
    try {
      await getControlPlaneClient(BillingService).updateBillingEmail({
        email: trimmed,
      });
      onClose();
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      logger.error("[BillingEmailRequiredModal] save failed", err);
      setError(msg || "Failed to save billing email.");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title="Set your billing email"
      size="sm"
    >
      <div className="flex flex-col gap-4 p-6">
        <div className="flex items-start gap-3">
          <div className="rounded-full bg-blue-100 p-2 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300">
            <Mail className="h-5 w-5" />
          </div>
          <div className="flex-1 text-sm text-foreground">
            <p>
              We need a billing address to continue. Your sign-in email isn&apos;t
              available — common when GitHub is set to keep your primary email
              private. Set a billing email now and we&apos;ll use it for Stripe.
            </p>
            {data.message ? (
              <p className="mt-2 text-xs text-muted-foreground">{data.message}</p>
            ) : null}
          </div>
        </div>

        <div className="flex flex-col gap-1">
          <label className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
            Email address
          </label>
          <input
            type="email"
            autoFocus
            placeholder="you@company.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            disabled={saving}
            className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-primary"
          />
        </div>

        {error ? (
          <p className="text-xs text-red-600 dark:text-red-400">{error}</p>
        ) : null}

        <div className="flex items-center justify-between gap-2">
          <a
            href={
              typeof window !== "undefined"
                ? new URL("/admin/billing", window.location.origin).toString()
                : "/admin/billing"
            }
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
          >
            Manage in admin
            <ExternalLink className="h-3 w-3" />
          </a>
          <div className="flex gap-2">
            <button
              type="button"
              onClick={onClose}
              disabled={saving}
              className="rounded-md border border-border px-4 py-2 text-sm text-foreground hover:bg-muted disabled:opacity-50"
            >
              Not now
            </button>
            <button
              type="button"
              onClick={handleSave}
              disabled={saving || !email.trim()}
              className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
            >
              {saving ? "Saving…" : "Save"}
            </button>
          </div>
        </div>
      </div>
    </Modal>
  );
}
