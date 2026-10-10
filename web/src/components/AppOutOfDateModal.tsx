import { RefreshCw } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
import { Modal } from "./ui/Modal";
import type { AppOutOfDateData } from "../store/modalStore";
import { logger } from "../lib/logger";

export interface AppOutOfDateModalProps {
  isOpen: boolean;
  onClose: () => void;
  data: AppOutOfDateData;
}

/**
 * Shown when this app asked the server for something it no longer serves
 * (api/versionSkew.ts) — an out-of-date desktop build, or a web tab still
 * running the bundle from before a deploy. Mounted by ModalLayer.
 *
 * The fix differs by shell: the desktop app has to update; a browser tab only
 * has to reload, which fetches the current bundle.
 */
export function AppOutOfDateModal({ isOpen, onClose }: AppOutOfDateModalProps) {
  const navigate = useNavigate();
  const isDesktop = typeof window !== "undefined" && !!window.electronAPI;

  const handleUpdate = () => {
    onClose();
    if (!isDesktop) {
      window.location.reload();
      return;
    }
    // The updater's progress UI lives in Settings → About.
    void navigate({ to: "/settings/$section", params: { section: "about" } });
    window.electronAPI?.checkForUpdates?.().catch((err: unknown) => {
      logger.error("[AppOutOfDateModal] update check failed", err);
    });
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={isDesktop ? "Update Reliant to continue" : "Reliant has been updated"}
      size="sm"
    >
      <div className="flex flex-col gap-4 p-6">
        <div className="flex items-start gap-3">
          <div className="rounded-full border border-border/60 bg-background p-2 text-primary">
            <RefreshCw className="h-5 w-5" />
          </div>
          <p className="flex-1 text-sm text-foreground">
            {isDesktop
              ? "This version of the app is older than the Reliant service, so some actions no longer work. Install the latest version to pick up where you left off."
              : "This page is running an older version of Reliant, so some actions no longer work. Reload to get the latest version — your chats are saved."}
          </p>
        </div>

        <div className="flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-border px-4 py-2 text-sm text-foreground hover:bg-muted"
          >
            Not now
          </button>
          <button
            type="button"
            onClick={handleUpdate}
            className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
          >
            {isDesktop ? "Check for updates" : "Reload"}
          </button>
        </div>
      </div>
    </Modal>
  );
}
