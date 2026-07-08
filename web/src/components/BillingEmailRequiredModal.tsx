import { useEffect, useState } from "react";
import { Mail, ArrowUpRight, ShieldCheck } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
import { Modal } from "./ui/Modal";
import { BillingService } from "@/gen/controlplane/v1/public/billing_service_pb";
import { getControlPlaneClient } from "../services/controlPlane/client";
import { useAuthStore } from "@/store/authStore";
import { logger } from "../lib/logger";
import type { BillingEmailRequiredData } from "../store/modalStore";

export interface BillingEmailRequiredModalProps {
  isOpen: boolean;
  onClose: () => void;
  data: BillingEmailRequiredData;
}

/**
 * BillingEmailRequiredModal prompts the user for a billing email when the
 * backend responds with x-reliant-reason=billing_email_missing (raised by
 * control-plane resolveBillingEmail when no usable email exists). Mounted by
 * ModalLayer; opened by the connect interceptor (api/upgradeInterceptor.ts).
 *
 * It has two states, keyed on whether the current account carries a usable
 * email (authStore user):
 *
 *   1. NO account email — an anonymous session, or a provider (e.g. GitHub with
 *      a private primary email) that withheld a verified address. These users
 *      have no trustworthy identity to bill against, so we do NOT let them type
 *      an unverified address straight into Stripe. Instead we route them through
 *      the existing account-upgrade flow (UpgradeAccount at /upgrade), which
 *      links a real identity / verifies an email via OTP and honors returnTo to
 *      bring them right back. Re-triggering checkout then resolves the email.
 *
 *   2. HAS account email — prefill the input with it (editable, so the user can
 *      bill to invoicing@company.com instead), call BillingService.
 *      UpdateBillingEmail on save, and close so the caller can retry checkout.
 *
 * The interceptor re-throws the original error (it is a UX overlay, not error
 * recovery), so "retry" is the user re-clicking Subscribe after the modal
 * closes — billing.tsx suppresses the raw error toast for this reason via
 * isBackendModalError, leaving this guided modal to own the flow.
 */
export function BillingEmailRequiredModal({
  isOpen,
  onClose,
  data,
}: BillingEmailRequiredModalProps) {
  const navigate = useNavigate();
  const user = useAuthStore((s) => s.user);
  const accountEmail = user?.email?.trim() ?? "";

  // No usable account email → the user must establish a verified identity first
  // (covers both the anonymous session and the non-anonymous-but-no-email
  // provider case). Everything else prefills and edits the known address.
  const needsIdentity = !accountEmail;

  const [email, setEmail] = useState(accountEmail);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  // authStore.user can hydrate after this modal mounts; prefill the account
  // email once it lands, unless the user has already typed something.
  useEffect(() => {
    if (accountEmail) setEmail((cur) => (cur ? cur : accountEmail));
  }, [accountEmail]);

  const handleManageBilling = () => {
    onClose();
    // In-app billing dashboard — replaces the old external /admin/billing link.
    void navigate({ to: "/settings/$section", params: { section: "billing" } });
  };

  // Route into the existing account-upgrade flow with a returnTo that lands the
  // user back where they were (the billing dashboard) once they carry an email.
  const handleVerifyIdentity = () => {
    onClose();
    const returnTo =
      typeof window !== "undefined"
        ? window.location.pathname + window.location.search
        : "/settings/billing";
    void navigate({ to: "/upgrade", search: { returnTo } });
  };

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

  // ── State 1: no usable account email → verify identity first ──────────
  if (needsIdentity) {
    return (
      <Modal
        isOpen={isOpen}
        onClose={onClose}
        title="Verify your email to subscribe"
        size="sm"
      >
        <div className="flex flex-col gap-4 p-6">
          <div className="flex items-start gap-3">
            <div className="rounded-full bg-blue-100 p-2 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300">
              <ShieldCheck className="h-5 w-5" />
            </div>
            <div className="flex-1 text-sm text-foreground">
              <p>
                Subscribing to a paid plan needs a verified email on your
                account. Add one now — link a provider or set an email and
                password — and we&apos;ll bring you right back to finish
                checkout.
              </p>
              {data.message ? (
                <p className="mt-2 text-xs text-muted-foreground">
                  {data.message}
                </p>
              ) : null}
            </div>
          </div>

          <div className="flex items-center justify-end gap-2">
            <button
              type="button"
              onClick={onClose}
              className="rounded-md border border-border px-4 py-2 text-sm text-foreground hover:bg-muted"
            >
              Not now
            </button>
            <button
              type="button"
              onClick={handleVerifyIdentity}
              className="inline-flex items-center gap-1 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
            >
              Verify your email
              <ArrowUpRight className="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
      </Modal>
    );
  }

  // ── State 2: has an account email → confirm / edit the billing email ──
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
              We need a billing address to continue. We&apos;ve prefilled your
              account email — keep it, or change it to the address you want on
              your Stripe invoices.
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
          <button
            type="button"
            onClick={handleManageBilling}
            className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
          >
            Manage billing
            <ArrowUpRight className="h-3 w-3" />
          </button>
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
