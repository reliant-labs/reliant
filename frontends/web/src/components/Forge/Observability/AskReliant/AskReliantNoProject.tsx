// Copyright (c) 2025 Reliant Labs

/**
 * "Ask Reliant" when there is no Reliant project at all to start the chat in:
 * the forge app was built and deployed outside Reliant (standalone forge on
 * someone's own computer, CI), so nothing here holds its code.
 *
 * It used to say "Add one from the main screen first" and stop. It now says
 * what the chat is missing and how to get it, by what the reader already has:
 *
 *   no machine     The two ways to get one, as instructions (owner,
 *                  2026-11-18). Your own computer, free: PairMachinePanel, the
 *                  install-and-pair instructions onboarding, Settings ›
 *                  Machines and the Add machine sheet already show; once the
 *                  machine connects, `onRetry` resolves the project again. A
 *                  cloud machine needs a plan: a link to the plans
 *                  (useGoToBilling). Nothing here creates a machine, so
 *                  nothing here can bill.
 *   a machine      Add project (the Add project dialog): clone, filled in with
 *                  the app's repository when every recorded source agrees on
 *                  one and there is a cloud machine to clone onto, or open the
 *                  folder on the reader's own computer.
 *   either way     Hand it to your own agent: the handoff sheet's prompt and
 *                  forge commands, which need no project at all.
 *
 * Which project a forge app belongs to, and whether Reliant should create one
 * on its own, is the projects-and-apps design (control-plane
 * docs/design/unified-shell-and-projects.md); this only makes the existing
 * steps reachable from here.
 */

import { Bot, Cloud, FolderPlus, Laptop } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import { PairMachinePanel } from "@/components/Machines";
import { isCloudDaemon } from "@/components/Projects/picker/format";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useGoToBilling } from "@/hooks/useGoToBilling";
import { trackEvent } from "@/lib/analytics";
import { capabilities } from "@/services/controlPlane/capabilities";
import { openAddProject } from "@/store/addProjectDialogStore";

import { INK_BUTTON } from "../../Releases/releaseParts";
import { BUTTON_PRIMARY, BUTTON_SECONDARY } from "../Dashboards/parts";
import { useForgeSourceRepo } from "./askReliantSource";

export interface AskReliantNoProjectProps {
  forgeProject?: string;
  onCancel: () => void;
  /** Open the handoff sheet for the same subject instead. */
  onHandoff: () => void;
  /** Resolve the project again: a machine the reader just connected may have brought one. */
  onRetry: () => void;
}

type Step = "pair_machine" | "cloud_plans" | "add_project" | "handoff";

export function AskReliantNoProject({ forgeProject, onCancel, onHandoff, onRetry }: AskReliantNoProjectProps) {
  const { daemons, loading } = useDaemonStatus();
  // Only an answer of "none" means no machine; an unread list offers Add project,
  // whose dialog explains machines itself.
  const noMachine = !loading && daemons.length === 0;
  // The repository is for Add project alone: read it only when that is the step.
  const repo = useForgeSourceRepo(noMachine ? undefined : forgeProject);
  const goToBilling = useGoToBilling("machines");
  const canClone = daemons.some((daemon) => isCloudDaemon(daemon.daemonType));
  const app = forgeProject?.trim() || "this app";

  const track = (step: Step) => trackEvent("ask_agent_no_project", { step, has_machine: !noMachine, has_source_repo: !!repo });

  const addProject = () => {
    track("add_project");
    onCancel();
    openAddProject(repo && canClone ? "clone" : undefined, repo ? { repoUrl: `https://github.com/${repo.fullName}` } : undefined);
  };
  const seePlans = () => {
    track("cloud_plans");
    onCancel();
    goToBilling();
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
        size={noMachine ? "lg" : "md"}
        title={`Reliant has no project for ${app} yet`}
        description="An agent works in a checkout of the app's code, on a machine. Nothing here holds this app's code, so there is nowhere to start the chat."
        footer={
          <div className="flex justify-end gap-2">
            <button type="button" className={BUTTON_SECONDARY} onClick={onCancel}>
              Cancel
            </button>
            {!noMachine && (
              <button type="button" className={BUTTON_PRIMARY} onClick={addProject} disabled={loading} data-testid="ask-reliant-add-project">
                Add project
              </button>
            )}
          </div>
        }
      >
        <div className="space-y-4 text-sm" data-testid="ask-reliant-no-projects">
          {loading ? (
            <p className="text-ink-muted">Checking your machines…</p>
          ) : noMachine ? (
            <div className="space-y-4" data-testid="ask-reliant-no-machine">
              <p className="text-ink">First, connect a machine. Then add the app&apos;s code to it as a project and ask again.</p>
              <section className="space-y-2" data-testid="ask-reliant-pair-machine">
                <h4 className="flex items-center gap-2 font-medium text-ink">
                  <Laptop className="h-4 w-4 text-ink-muted" aria-hidden="true" />
                  Connect your own computer: free
                </h4>
                <PairMachinePanel
                  mode="bootstrap"
                  onConnected={() => {
                    track("pair_machine");
                    onRetry();
                  }}
                />
              </section>
              {capabilities.cloudDaemons && (
                <section className="space-y-2 border-t border-border pt-4" data-testid="ask-reliant-cloud-machine">
                  <h4 className="flex items-center gap-2 font-medium text-ink">
                    <Cloud className="h-4 w-4 text-ink-muted" aria-hidden="true" />
                    Use a sandbox: needs a plan
                  </h4>
                  <p className="text-ink-muted">
                    Choose a plan in Billing, then add the sandbox from Settings › Agent sandboxes.{" "}
                    <button type="button" className={INK_BUTTON} onClick={seePlans} data-testid="ask-reliant-see-plans">
                      See plans
                    </button>
                  </p>
                </section>
              )}
            </div>
          ) : (
            <div className="flex items-start gap-2" data-testid="ask-reliant-add-project-step">
              <FolderPlus className="mt-0.5 h-4 w-4 shrink-0 text-ink-muted" aria-hidden="true" />
              <p className="text-ink">
                Add the app&apos;s code as a project: clone its repository onto a sandbox, or open the folder it is in on your own computer. Then ask again.
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
    </div>
  );
}
