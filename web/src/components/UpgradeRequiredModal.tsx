import { ExternalLink, Zap } from "lucide-react";
import { Modal } from "./ui/Modal";
import type { UpgradeRequiredData } from "../store/modalStore";

export interface UpgradeRequiredModalProps {
  isOpen: boolean;
  onClose: () => void;
  data: UpgradeRequiredData;
}

// Canonical reason codes (mirror control-plane/internal/enforcement/check.go).
// Unknown reasons fall through to the generic copy below.
const REASON_COPY: Record<string, { title: string; body: string }> = {
  free_tier_compute_minutes: {
    title: "You've used your free compute minutes",
    body: "Free-tier workspaces include a limited number of compute minutes per month. Upgrade to keep your daemon running.",
  },
  free_tier_global_budget: {
    title: "Free tier quota exceeded",
    body: "The shared free-tier budget for the month is exhausted. Upgrade to a paid plan to continue.",
  },
};

const GENERIC_COPY = {
  title: "Quota exceeded",
  body: "You've hit a plan limit. Upgrade to continue using this feature.",
};

export function UpgradeRequiredModal({
  isOpen,
  onClose,
  data,
}: UpgradeRequiredModalProps) {
  const copy = REASON_COPY[data.reason] ?? GENERIC_COPY;

  const handleUpgrade = () => {
    if (!data.upgradeUrl) return;
    if (window.electronAPI?.openExternal) {
      void window.electronAPI.openExternal(absoluteUpgradeUrl(data.upgradeUrl));
    } else {
      window.open(absoluteUpgradeUrl(data.upgradeUrl), "_blank", "noopener,noreferrer");
    }
    onClose();
  };

  return (
    <Modal isOpen={isOpen} onClose={onClose} title={copy.title} size="sm">
      <div className="flex flex-col gap-4 p-6">
        <div className="flex items-start gap-3">
          <div className="rounded-full bg-amber-100 p-2 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300">
            <Zap className="h-5 w-5" />
          </div>
          <div className="flex-1 text-sm text-foreground">
            <p>{copy.body}</p>
            {data.message ? (
              <p className="mt-2 text-xs text-muted-foreground">{data.message}</p>
            ) : null}
          </div>
        </div>

        <div className="flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-border px-4 py-2 text-sm text-foreground hover:bg-muted"
          >
            Not now
          </button>
          {data.upgradeUrl ? (
            <button
              type="button"
              onClick={handleUpgrade}
              className="inline-flex items-center gap-1.5 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
            >
              Upgrade plan
              <ExternalLink className="h-3.5 w-3.5" />
            </button>
          ) : null}
        </div>
      </div>
    </Modal>
  );
}

// absoluteUpgradeUrl turns a backend-supplied path like "/billing/plans" into
// a full URL. The backend sends relative paths because it doesn't know which
// host the user reached us on (web vs. electron vs. self-hosted), and the
// electron client needs an absolute URL for shell.openExternal anyway.
function absoluteUpgradeUrl(url: string): string {
  if (/^https?:\/\//i.test(url)) return url;
  if (typeof window !== "undefined" && window.location?.origin) {
    return new URL(url, window.location.origin).toString();
  }
  return url;
}
