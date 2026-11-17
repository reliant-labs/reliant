// Copyright (c) 2025 Reliant Labs

/**
 * "Ask Reliant" when there is no Reliant project at all to start the chat in:
 * the forge app was built and deployed outside Reliant (standalone forge on
 * someone's own computer, CI), so nothing here holds its code.
 *
 * It used to say "Add one from the main screen first" and stop. It now says
 * what the chat is missing and offers the step that provides it, by what the
 * reader already has:
 *
 *   no machine     Add a machine (AddMachineSheet, the one way the app adds
 *                  one). With the app's GitHub repository known
 *                  (askReliantSource), the sheet starts with it, so the new
 *                  cloud machine clones it as a project. Once the machine is
 *                  ready, `onRetry` resolves the project again.
 *   a machine      Add project (the Add project dialog): clone, filled in with
 *                  the app's repository when it is known and there is a cloud
 *                  machine to clone onto, or open the folder on the reader's
 *                  own computer.
 *   either way     Hand it to your own agent: the handoff sheet's prompt and
 *                  forge commands, which need no project at all.
 *
 * Which project a forge app belongs to, and whether Reliant should create one
 * on its own, is the projects-and-apps design (control-plane
 * docs/design/unified-shell-and-projects.md); this only makes the existing
 * steps reachable from here.
 */

import { useState } from "react";
import { Bot, FolderPlus, MonitorSmartphone } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import { AddMachineSheet } from "@/components/Machines";
import { isCloudDaemon } from "@/components/Projects/picker/format";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { trackEvent } from "@/lib/analytics";
import { openAddProject } from "@/store/addProjectDialogStore";

import { INK_BUTTON } from "../../Releases/releaseParts";
import { BUTTON_PRIMARY, BUTTON_SECONDARY } from "../Dashboards/parts";
import { useForgeSourceRepo } from "./askReliantSource";

export interface AskReliantNoProjectProps {
  forgeProject?: string;
  onCancel: () => void;
  /** Open the handoff sheet for the same subject instead. */
  onHandoff: () => void;
  /** Resolve the project again: a machine the reader just added may have brought one. */
  onRetry: () => void;
}

type Step = "add_machine" | "add_project" | "handoff";

export function AskReliantNoProject({ forgeProject, onCancel, onHandoff, onRetry }: AskReliantNoProjectProps) {
  const { daemons, loading } = useDaemonStatus();
  const repo = useForgeSourceRepo(forgeProject);
  const [addingMachine, setAddingMachine] = useState(false);
  // Only an answer of "none" means no machine; an unread list offers Add project,
  // whose dialog explains machines itself.
  const noMachine = !loading && daemons.length === 0;
  const canClone = daemons.some((daemon) => isCloudDaemon(daemon.daemonType));
  const app = forgeProject?.trim() || "this app";

  const track = (step: Step) => trackEvent("ask_agent_no_project", { step, has_machine: !noMachine, has_source_repo: !!repo });

  const addProject = () => {
    track("add_project");
    onCancel();
    openAddProject(repo && canClone ? "clone" : undefined, repo ? { repoUrl: `https://github.com/${repo.fullName}` } : undefined);
  };
  const addMachine = () => {
    track("add_machine");
    setAddingMachine(true);
  };
  const handoff = () => {
    track("handoff");
    onHandoff();
  };

  return (
    <div className="relative z-[70]" onKeyDown={(event) => event.key === "Escape" && event.nativeEvent.preventDefault()}>
      <Modal
        open
        onClose={onCancel}
        title={`Reliant has no project for ${app} yet`}
        description="An agent works in a checkout of the app's code, on a machine. Nothing here holds this app's code, so there is nowhere to start the chat."
        footer={
          <div className="flex justify-end gap-2">
            <button type="button" className={BUTTON_SECONDARY} onClick={onCancel}>
              Cancel
            </button>
            {noMachine ? (
              <button type="button" className={BUTTON_PRIMARY} onClick={addMachine} data-testid="ask-reliant-add-machine">
                Add a machine
              </button>
            ) : (
              <button type="button" className={BUTTON_PRIMARY} onClick={addProject} disabled={loading} data-testid="ask-reliant-add-project">
                Add project
              </button>
            )}
          </div>
        }
      >
        <div className="space-y-3 text-sm" data-testid="ask-reliant-no-projects">
          {loading ? (
            <p className="text-ink-muted">Checking your machines…</p>
          ) : noMachine ? (
            <div className="flex items-start gap-2" data-testid="ask-reliant-no-machine">
              <MonitorSmartphone className="mt-0.5 h-4 w-4 shrink-0 text-ink-muted" aria-hidden="true" />
              <p className="text-ink">
                First, connect a machine: a cloud machine, or your own computer.{" "}
                {repo ? (
                  <>
                    A cloud machine clones <code className="font-mono text-xs">{repo.fullName}</code>, the repository {app} was deployed from, as a project.
                  </>
                ) : (
                  <>Then add the app&apos;s repository to it as a project.</>
                )}
              </p>
            </div>
          ) : (
            <div className="flex items-start gap-2" data-testid="ask-reliant-add-project-step">
              <FolderPlus className="mt-0.5 h-4 w-4 shrink-0 text-ink-muted" aria-hidden="true" />
              <p className="text-ink">
                Add the app&apos;s code as a project: clone its repository onto a cloud machine, or open the folder it is in on your own computer. Then ask again.
                {repo && (
                  <>
                    {" "}
                    It was deployed from <code className="font-mono text-xs">{repo.fullName}</code>
                    {canClone ? ", which the clone form starts with." : "."}
                  </>
                )}
              </p>
            </div>
          )}
          <p className="text-xs text-ink-muted">
            Or{" "}
            <button type="button" className={INK_BUTTON} onClick={handoff} data-testid="ask-reliant-handoff">
              <Bot className="mr-0.5 inline h-3 w-3 align-[-1px]" aria-hidden="true" />
              hand it to your own agent
            </button>{" "}
            (Claude Code, Codex, Cursor) with the forge commands to run. That needs no project.
          </p>
        </div>
      </Modal>
      <AddMachineSheet open={addingMachine} onClose={() => setAddingMachine(false)} repo={repo} onReady={onRetry} />
    </div>
  );
}
