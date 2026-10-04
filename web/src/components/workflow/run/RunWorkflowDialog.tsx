// Copyright (c) 2025 Reliant Labs

/**
 * Run… — start a workflow with typed inputs, without the chat composer.
 *
 * A thin host for RunWorkflowForm: it adds the prompt, starts the chat with
 * StartChat and opens it. A manual run is ATTENDED (research/WORKFLOW_UI.md
 * §14.1 decision 11): a person pressed the button and is right here, so there
 * is no "run without me" toggle, and the run opens as a chat rather than in
 * the Runs view.
 */

import { useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";
import { ConnectError } from "@connectrpc/connect";

import { chatGrpc, MessageRole, type Chat } from "@/api/chat-grpc";
import { loadRunContext } from "@/components/runs/loadRunContext";
import { Modal } from "../../ui/Modal";
import { Button } from "../../ui/Button";
import { getWorkflowDisplayName } from "../useWorkflowInputs";
import { RunWorkflowForm, type RunWorkflowFormStatus } from "./RunWorkflowForm";
import { errorTextClass, hintClass, labelClass, textareaClass } from "./runFormStyles";
import { EMPTY_RUN_VALUE, type RunWorkflowValue } from "./runWorkflowValues";

export interface RunWorkflowDialogProps {
  open: boolean;
  onClose: () => void;
  projectId: string;
  workflowRef: string;
  /**
   * Opens the started chat. Defaults to selecting it in its project and
   * navigating there; a host that is already inside the project view can
   * do less.
   */
  onStarted?: (chat: Chat) => void | Promise<void>;
}

export function RunWorkflowDialog(props: RunWorkflowDialogProps) {
  // Remount per open so each run starts from a clean form.
  if (!props.open) return null;
  return <RunWorkflowDialogBody {...props} />;
}

function errorMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

function RunWorkflowDialogBody({ onClose, projectId, workflowRef, onStarted }: RunWorkflowDialogProps) {
  const navigate = useNavigate();
  const [value, setValue] = useState<RunWorkflowValue>(EMPTY_RUN_VALUE);
  const [prompt, setPrompt] = useState("");
  const [status, setStatus] = useState<RunWorkflowFormStatus>({
    loading: true,
    missingRequired: [],
    definitionError: null,
  });
  const [attempted, setAttempted] = useState(false);
  const [promptError, setPromptError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);

  const title = `Run ${getWorkflowDisplayName(workflowRef, true)}`;

  const openChat = async (chat: Chat) => {
    if (onStarted) {
      await onStarted(chat);
      return;
    }
    await loadRunContext(chat.id);
    await navigate({ to: "/project/$projectId", params: { projectId: chat.projectId }, search: {} });
  };

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setAttempted(true);
    setFormError(null);
    const promptMissing = !prompt.trim();
    setPromptError(promptMissing ? "Write the message the run starts from." : null);
    if (promptMissing || status.loading || status.missingRequired.length > 0 || status.definitionError) {
      if (promptMissing) document.getElementById("run-workflow-prompt")?.focus();
      return;
    }

    setStarting(true);
    try {
      const { chat } = await chatGrpc.start({
        project_id: projectId,
        worktree_id: value.worktreeId,
        daemon_id: value.daemonId,
        workflow: workflowRef,
        messages: [{ role: MessageRole.USER, content: prompt.trim() }],
        workflow_params: value.params,
        selected_presets: value.presets,
      });
      onClose();
      await openChat(chat);
    } catch (error) {
      // Input validation (a missing or mistyped input, an unavailable model)
      // comes back as InvalidArgument with the server's own wording.
      setFormError(errorMessage(error));
    } finally {
      setStarting(false);
    }
  };

  return (
    <Modal isOpen onClose={starting ? () => undefined : onClose} title={title} size="lg">
      <form onSubmit={onSubmit} noValidate className="space-y-5" aria-label={title}>
        {formError && (
          <div
            role="alert"
            className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            {formError}
          </div>
        )}

        <div>
          <label htmlFor="run-workflow-prompt" className={labelClass}>
            Message
          </label>
          <textarea
            id="run-workflow-prompt"
            className={textareaClass}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            rows={4}
            placeholder="What should this run do?"
            aria-invalid={!!promptError}
            aria-describedby={promptError ? "run-workflow-prompt-error" : "run-workflow-prompt-hint"}
          />
          {promptError ? (
            <p id="run-workflow-prompt-error" className={errorTextClass}>
              {promptError}
            </p>
          ) : (
            <p id="run-workflow-prompt-hint" className={hintClass}>
              The run starts as a chat with this message. You can answer its questions as it goes.
            </p>
          )}
        </div>

        <RunWorkflowForm
          projectId={projectId}
          workflowRef={workflowRef}
          value={value}
          onChange={setValue}
          onStatusChange={setStatus}
          showValidation={attempted}
          applyDefaultPresets
          showMachinePicker
          disabled={starting}
        />

        <div className="flex justify-end gap-2 border-t border-border/60 pt-4">
          <Button type="button" variant="ghost" onClick={onClose} disabled={starting}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" loading={starting}>
            Run
          </Button>
        </div>
      </form>
    </Modal>
  );
}
