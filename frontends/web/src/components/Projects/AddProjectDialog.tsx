// Copyright (c) 2025 Reliant Labs

/**
 * Add project — a dialog over whatever the user is doing, opened from the
 * sidebar's project menu, Settings → Projects, the new-chat project chip, the
 * Add Project shortcut and the tray (store/addProjectDialogStore). It replaces
 * the full-page project picker: adding a project never takes the chat window
 * away.
 *
 * Two methods: Clone a repository (choose the machine, then the repository)
 * and Open a folder (the native dialog in Electron; the in-app folder browser,
 * which also takes a typed path, in the browser).
 */

import { useCallback, useEffect, useState } from "react";
import { ArrowLeft, Loader2 } from "lucide-react";

import Modal from "../forge-ui/modal";
import { cn } from "../../lib/utils";
import { useAddProjectDialogStore } from "../../store/addProjectDialogStore";
import { AddProjectOptions } from "./AddProjectOptions";
import { CloneTargetPicker } from "./CloneTargetPicker";
import { DirectoryPicker } from "./DirectoryPicker";
import { RepoSelector } from "./RepoSelector";
import { pickerButton } from "./picker/buttonStyles";
import { ADD_PROJECT_LABEL, CLONE_REPOSITORY_LABEL, PROJECT_DEFINITION } from "./projectCopy";
import { useAddProject } from "./useAddProject";

type Step = "choose" | "clone";

export function AddProjectDialog() {
  const isOpen = useAddProjectDialogStore((state) => state.isOpen);
  const method = useAddProjectDialogStore((state) => state.method);
  const close = useAddProjectDialogStore((state) => state.closeAddProject);
  const [step, setStep] = useState<Step>("choose");
  const [isBrowsingFolders, setIsBrowsingFolders] = useState(false);

  const handleClose = useCallback(() => {
    setStep("choose");
    close();
  }, [close]);

  const {
    actions,
    cloudDaemons,
    effectiveCloneDaemonId,
    setChosenCloneDaemonId,
    cloneRepo,
    openFolderPath,
    cloneStatus,
  } = useAddProject({
    onAdded: handleClose,
    onChooseClone: () => setStep("clone"),
    onChooseFolderInBrowser: () => {
      // The folder browser is its own dialog; this one steps aside for it.
      close();
      setIsBrowsingFolders(true);
    },
  });

  // Opened on a method: go straight to it.
  const openAction = actions.find((a) => a.kind === "open");
  useEffect(() => {
    if (!isOpen || !method) return;
    if (method === "clone") {
      setStep("clone");
    } else if (method === "folder" && openAction && !openAction.disabled) {
      // Straight to the folder chooser: the native dialog, or the in-app
      // browser. This dialog has nothing to add on top of either.
      close();
      openAction.onClick();
    }
    // Only on open: the actions are rebuilt every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isOpen, method]);

  return (
    <>
      <Modal
        open={isOpen}
        onClose={handleClose}
        title={step === "clone" ? CLONE_REPOSITORY_LABEL : ADD_PROJECT_LABEL}
        description={step === "choose" ? PROJECT_DEFINITION : undefined}
        size={step === "clone" ? "lg" : "md"}
        headerActions={
          step === "clone" && method !== "clone" ? (
            <button
              type="button"
              onClick={() => setStep("choose")}
              className={pickerButton("ghost", "sm")}
              data-testid="add-project-back"
            >
              <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
              Back
            </button>
          ) : undefined
        }
      >
        <div className="forge-ui" data-testid="add-project-dialog">
          {step === "choose" ? (
            <AddProjectOptions actions={actions} />
          ) : (
            <>
              {/* The target comes FIRST: which machine the checkout lands on
                  is a decision about the clone. Renders nothing when there is
                  only one candidate. */}
              <CloneTargetPicker
                daemons={cloudDaemons}
                selectedDaemonId={effectiveCloneDaemonId}
                onSelect={setChosenCloneDaemonId}
              />
              <RepoSelector onSelect={(repo) => void cloneRepo(repo)} analyticsPhase="add_project" />
            </>
          )}
        </div>
      </Modal>

      <DirectoryPicker
        isOpen={isBrowsingFolders}
        onClose={() => setIsBrowsingFolders(false)}
        onSelect={(path) => {
          setIsBrowsingFolders(false);
          void openFolderPath(path);
        }}
      />

      {cloneStatus && (
        <div
          role="status"
          className={cn(
            "fixed bottom-4 left-1/2 z-50 flex -translate-x-1/2 items-center gap-2",
            "rounded-lg border border-border bg-card px-3 py-2 text-sm text-foreground shadow-lg",
          )}
        >
          <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
          {cloneStatus}
        </div>
      )}
    </>
  );
}

/**
 * The app-global mount (routes.tsx). The dialog reads the machine list and
 * the GitHub credential, so it mounts on its first open — never on the
 * signed-out routes — and then stays, so a clone or folder browse it started
 * can finish after it closes.
 */
export function AddProjectDialogHost() {
  const isOpen = useAddProjectDialogStore((state) => state.isOpen);
  const [mounted, setMounted] = useState(isOpen);
  useEffect(() => {
    if (isOpen) setMounted(true);
  }, [isOpen]);
  return mounted ? <AddProjectDialog /> : null;
}
