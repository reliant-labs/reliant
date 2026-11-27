// Copyright (c) 2026 Reliant Labs

/**
 * "Ask the agent for ideas": a new chat with the prompt below waiting in its
 * composer, unsent. It is the Ask Reliant pattern
 * (Forge/Observability/AskReliant/startAskReliantChat.ts, `send: false`): a
 * fresh workspace in the person's project, the prompt handed to the new-chat
 * composer, the person reads, edits and sends it. No workflow is created; the
 * agent saves what it proposes as Proposed ideas, for a person to accept or
 * reject.
 *
 * Where no chat can start (AskAgentModeProvider mode="handoff": the forge
 * shell, a demo), the dialog offers the prompt to copy instead.
 */

import { useRouter } from "@tanstack/react-router";
import { useState } from "react";

import Button from "@/components/forge-ui/button";
import Modal from "@/components/forge-ui/modal";
import Textarea from "@/components/forge-ui/textarea";
import { fetchProjects } from "@/hooks/project-queries";
import { projectChatsPath } from "@/lib/paths";
import { toast } from "@/lib/toast-manager";
import { mostRecentProject, useProjectStore } from "@/store/projectStore";

import { useAskAgentMode } from "../../Forge/Observability/AskReliant/askAgentMode";
import { startAskReliantChat } from "../../Forge/Observability/AskReliant/startAskReliantChat";

/** The prompt the chat starts with: the GTM tools' names (plan B.4), and that ideas stay Proposed. */
export const ASK_FOR_IDEAS_PROMPT = [
  "Propose three GTM ideas for the next two weeks.",
  "Read our positioning and the verified facts first (get_gtm_positioning), and the open campaigns (list_gtm_campaigns).",
  "Use product analytics for evidence (read_product_analytics). Save each idea with save_gtm_idea; leave them Proposed.",
].join(" ");

/** A workspace name (and branch) of its own for each ask. */
function workspaceName(): string {
  return `ask-gtm-ideas-${Math.random().toString(36).slice(2, 8)}`;
}

export function AskForIdeasDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [prompt, setPrompt] = useState(ASK_FOR_IDEAS_PROMPT);
  const [starting, setStarting] = useState(false);
  const router = useRouter({ warn: false });
  const { mode } = useAskAgentMode();
  const handoff = mode === "handoff";

  async function start() {
    setStarting(true);
    try {
      const project = useProjectStore.getState().currentProject ?? mostRecentProject(await fetchProjects().catch(() => []));
      if (!project) {
        toast.warning("Add a project first: the chat runs in one of your projects.");
        return;
      }
      await startAskReliantChat({
        project,
        prompt: prompt.trim() || ASK_FOR_IDEAS_PROMPT,
        workspaceName: workspaceName(),
        send: false,
        openProject: (projectId) => void router?.navigate({ to: projectChatsPath(projectId), search: {} }),
      });
      onClose();
    } catch (error) {
      void toast.error(error);
    } finally {
      setStarting(false);
    }
  }

  async function copy() {
    await navigator.clipboard.writeText(prompt);
    toast.success("Prompt copied");
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="md"
      sheetOnMobile
      title="Ask the agent for ideas"
      description={
        handoff
          ? "Paste this prompt into your agent. It saves what it proposes here, as Proposed ideas for you to accept or reject."
          : "Opens a new chat with this prompt, waiting for you to send it. The agent saves what it proposes here, as Proposed ideas for you to accept or reject."
      }
      footer={
        <div className="flex w-full justify-end gap-2">
          <Button variant="secondary" size="sm" onClick={onClose} disabled={starting}>
            Cancel
          </Button>
          {handoff ? (
            <Button variant="primary" size="sm" onClick={() => void copy()}>
              Copy prompt
            </Button>
          ) : (
            <Button variant="primary" size="sm" onClick={() => void start()} loading={starting}>
              Open chat
            </Button>
          )}
        </div>
      }
    >
      <label className="mb-1.5 block text-sm font-medium text-ink" htmlFor="gtm-ask-prompt">
        Prompt
      </label>
      <Textarea id="gtm-ask-prompt" value={prompt} onChange={(e) => setPrompt(e.target.value)} rows={6} resize="vertical" />
    </Modal>
  );
}
