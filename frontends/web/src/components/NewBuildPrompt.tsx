import { useEffect } from "react";
import { toast } from "sonner";
import { appBuild } from "../lib/appBuild";
import { getIsDev, isElectron } from "../lib/constants";
import { watchForNewBuild } from "../lib/deployedBuild";
import { useModalStore } from "../store/modalStore";

export const NEW_BUILD_TOAST_ID = "new-web-build";

/**
 * Where the watch runs: a production web tab. Not Electron — it serves its own
 * bundle and updates through electron-updater — and not the Vite dev server,
 * which hot-reloads and has no /version.json.
 */
function shouldWatch(): boolean {
  return !isElectron() && !getIsDev();
}

/**
 * Tells a long-lived tab that a newer web build was deployed (lib/deployedBuild.ts).
 *
 * A non-blocking toast, deliberately: the user may be halfway through a
 * message, so the tab never reloads by itself, and a dismissed prompt stays
 * dismissed until yet another build ships. The blocking out-of-date modal is
 * for a real incompatibility (api/versionSkew.ts); while it is up it already
 * offers the reload, so the toast steps aside. Mounted once, in RootShell.
 */
export function NewBuildPrompt({ enabled = shouldWatch() }: { enabled?: boolean }) {
  const modalUp = useModalStore((s) => s.activeModal === "app-out-of-date");

  useEffect(() => {
    if (!enabled) return;
    return watchForNewBuild({
      running: appBuild(),
      onNewBuild: () => {
        if (useModalStore.getState().activeModal === "app-out-of-date") return;
        toast.info("A new version of Reliant is available", {
          id: NEW_BUILD_TOAST_ID,
          description: "Reload when you're ready. Your chats are saved.",
          duration: Infinity,
          action: { label: "Reload", onClick: () => window.location.reload() },
        });
      },
    });
  }, [enabled]);

  useEffect(() => {
    if (modalUp) toast.dismiss(NEW_BUILD_TOAST_ID);
  }, [modalUp]);

  return null;
}
