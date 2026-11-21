import { RefreshCw } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
import Modal from "./forge-ui/modal";
import type { AppOutOfDateData } from "../store/modalStore";
import { logger } from "../lib/logger";
import { flushPendingDrafts } from "../lib/draftFlush";
import { useAppUpdateStore } from "../store/appUpdateStore";
import { settingsPath } from "@/lib/paths";

export interface AppOutOfDateModalProps {
  isOpen: boolean;
  onClose: () => void;
  data: AppOutOfDateData;
}

// The forced prompt has no way out but the fix.
const noDismiss = () => {};

/**
 * Shown when this app is older than the server it talks to. Mounted by
 * ModalLayer. Two ways in (api/versionSkew.ts):
 *
 *  - skew: the app asked for something the server no longer serves — an
 *    out-of-date desktop build, or a web tab still running the bundle from
 *    before a deploy. A suggestion: "Not now" closes it.
 *  - forced (`data.forced`): the server requires a newer build — it refused
 *    one with `upgrade_required`, or publishes a minimum above this build.
 *    Every call is refused, so there is no "Not now", and neither Escape nor
 *    the backdrop closes it.
 *
 * The fix differs by shell: the desktop app has to update; a browser tab only
 * has to reload, which fetches the current bundle. The reload writes pending
 * composer drafts first (lib/draftFlush.ts), so nothing typed is lost.
 */
export function AppOutOfDateModal({ isOpen, onClose, data }: AppOutOfDateModalProps) {
  const navigate = useNavigate();
  const isDesktop = typeof window !== "undefined" && !!window.electronAPI;
  const forced = !!data?.forced;

  const handleUpdate = () => {
    if (!forced) onClose();
    if (!isDesktop) {
      flushPendingDrafts();
      window.location.reload();
      return;
    }
    if (forced) {
      // The forced update (appUpdateStore.forceUpdate → the main process):
      // check, download and restart, under an update dialog that cannot be
      // dismissed either.
      useAppUpdateStore.getState().forceUpdate(data.minVersion ?? "", data.upgradeUrl).catch((err: unknown) => {
        logger.error("[AppOutOfDateModal] forced update failed", err);
      });
      return;
    }
    // Settings → About shows where the updater stands; the check opens the
    // update dialog over it when it finds an update.
    void navigate({ to: settingsPath("about") });
    useAppUpdateStore.getState().checkForUpdates().catch((err: unknown) => {
      logger.error("[AppOutOfDateModal] update check failed", err);
    });
  };

  const title = forced
    ? isDesktop
      ? "Update Reliant to continue"
      : "Reload Reliant to continue"
    : isDesktop
      ? "Update Reliant to continue"
      : "Reliant has been updated";

  const body = forced
    ? isDesktop
      ? "This version of the app is no longer supported by the Reliant service. Upgrade to the latest version to keep working — your chats are saved."
      : "This page is running a version of Reliant the service no longer supports. Reload to upgrade to the latest version — your chats and drafts are saved."
    : isDesktop
      ? "This version of the app is older than the Reliant service, so some actions no longer work. Install the latest version to pick up where you left off."
      : "This page is running an older version of Reliant, so some actions no longer work. Reload to get the latest version — your chats are saved.";

  return (
    <Modal
      open={isOpen}
      onClose={forced ? noDismiss : onClose}
      closeOnOverlay={!forced}
      hideCloseButton={forced}
      title={title}
      size="sm"
    >
      <div className="flex flex-col gap-4 p-6">
        <div className="flex items-start gap-3">
          <div className="rounded-full border border-border/60 bg-background p-2 text-primary-ink">
            <RefreshCw className="h-5 w-5" />
          </div>
          <div className="flex flex-1 flex-col gap-2">
            <p className="text-sm text-foreground">{body}</p>
            {forced && data.minVersion && (
              <p className="text-meta text-muted-foreground">
                The oldest supported version is {data.minVersion}.
              </p>
            )}
          </div>
        </div>

        <div className="flex justify-end gap-2">
          {!forced && (
            <button
              type="button"
              onClick={onClose}
              className="rounded-md border border-border px-4 py-2 text-sm text-foreground hover:bg-muted"
            >
              Not now
            </button>
          )}
          <button
            type="button"
            onClick={handleUpdate}
            className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
          >
            {isDesktop ? (forced ? "Update now" : "Check for updates") : "Reload"}
          </button>
        </div>
      </div>
    </Modal>
  );
}
