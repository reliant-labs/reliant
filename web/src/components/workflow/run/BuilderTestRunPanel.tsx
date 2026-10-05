// Copyright (c) 2025 Reliant Labs

/**
 * Test run — run the workflow being edited, from the builder.
 *
 * Hosts RunWorkflowForm (typed inputs, worktree, machine) over the draft on
 * the canvas. Run saves the draft first and then starts it by its slug, so
 * the run is exactly what is on screen; a save that fails (or a complete
 * workflow that no longer validates) stops here and nothing starts. The run
 * is ATTENDED (research/WORKFLOW_UI.md §14.1 decision 11) and is tagged
 * builder.test, which keeps it out of the sidebar and the default Runs list.
 *
 * Node statuses are painted onto the canvas by the builder (see
 * useBuilderTestRun); this panel reports the run's own state and links to the
 * full run.
 */

import { useState, type FormEvent } from "react";
import { Link } from "@tanstack/react-router";
import { ConnectError } from "@connectrpc/connect";
import { FlaskConical } from "lucide-react";

import { chatGrpc, MessageRole } from "@/api/chat-grpc";
import { useChat } from "@/hooks/chat-queries";
import { isLiveRunStatus, runStatus } from "@/lib/runStatus";
import { cn } from "@/lib/utils";
import { RunStatusBadge } from "../../ui/RunStatusIndicator";
import { Button } from "../../ui/Button";
import { ConfigurationPanel } from "../ConfigurationPanel";
import { RunWorkflowForm, type RunWorkflowFormStatus } from "./RunWorkflowForm";
import { errorTextClass, hintClass, labelClass, textareaClass } from "./runFormStyles";
import { EMPTY_RUN_VALUE, type RunWorkflowValue } from "./runWorkflowValues";
import "../config/config-panel.css";

export interface BuilderTestRunPanelProps {
  projectId: string;
  /** The workflow's saved name. Empty until it has been saved once. */
  workflowRef: string;
  /**
   * Saves the canvas as it is. Resolves to the saved workflow's slug, or null
   * when it was not saved (the builder has already said why).
   */
  saveDraft: () => Promise<string | null>;
  /** The test run's chat, once started; the builder follows it on the canvas. */
  testChatId: string | null;
  onStarted: (chatId: string) => void;
  onClose: () => void;
  bottomOffset?: number;
  topOffset?: number;
  /** Dock left of an open step panel rather than over it. */
  docked?: boolean;
}

function errorMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

export function BuilderTestRunPanel({
  projectId,
  workflowRef,
  saveDraft,
  testChatId,
  onStarted,
  onClose,
  bottomOffset,
  topOffset,
  docked = false,
}: BuilderTestRunPanelProps) {
  const [value, setValue] = useState<RunWorkflowValue>(EMPTY_RUN_VALUE);
  const [prompt, setPrompt] = useState("");
  const [status, setStatus] = useState<RunWorkflowFormStatus>({
    loading: false,
    missingRequired: [],
    definitionError: null,
  });
  const [attempted, setAttempted] = useState(false);
  const [promptError, setPromptError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);
  // The form reads the SAVED definition; remount it after each save so inputs
  // added on the canvas appear for the next run.
  const [savedEpoch, setSavedEpoch] = useState(0);

  const chat = useChat(testChatId ?? undefined).data;
  const runState = chat
    ? runStatus({ state: chat.workflowState, stopReason: chat.workflowStopReason, activity: chat.activity })
    : null;
  const live = runState ? isLiveRunStatus(runState) : false;

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setAttempted(true);
    setFormError(null);
    const promptMissing = !prompt.trim();
    setPromptError(promptMissing ? "Write the message the run starts from." : null);
    if (promptMissing) {
      document.getElementById("builder-test-run-prompt")?.focus();
      return;
    }
    if (status.loading || status.missingRequired.length > 0 || status.definitionError) return;

    setStarting(true);
    try {
      // Save first: the run is the draft as saved. A failed save starts nothing.
      const slug = await saveDraft();
      if (!slug) return;
      setSavedEpoch((epoch) => epoch + 1);

      const { chat: started } = await chatGrpc.start({
        project_id: projectId,
        worktree_id: value.worktreeId,
        daemon_id: value.daemonId,
        workflow: slug,
        messages: [{ role: MessageRole.USER, content: prompt.trim() }],
        workflow_params: value.params,
        selected_presets: value.presets,
        builder_test: true,
      });
      onStarted(started.id);
    } catch (error) {
      // Validation (an invalid definition, a missing input) comes back with
      // the server's own wording.
      setFormError(errorMessage(error));
    } finally {
      setStarting(false);
    }
  };

  return (
    <div className={cn("cpv2-test-run", docked && "cpv2-test-run--docked")}>
      <ConfigurationPanel
        title="Test run"
        subtitle="Runs the draft on this canvas"
        subtitleMono={false}
        icon={<FlaskConical />}
        onClose={onClose}
        bottomOffset={bottomOffset}
        topOffset={topOffset}
      >
        <form onSubmit={onSubmit} noValidate aria-label="Test run" className="cpv2-section space-y-4">
          {formError && (
            <div
              role="alert"
              className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive-ink"
            >
              {formError}
            </div>
          )}

          {testChatId && (
            <div className="flex flex-wrap items-center gap-2" data-testid="test-run-status">
              {runState && <RunStatusBadge status={runState} />}
              <Link
                to="/workflows/runs/$runId"
                params={{ runId: testChatId }}
                className="text-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
              >
                Watch full run
              </Link>
            </div>
          )}

          <div>
            <label htmlFor="builder-test-run-prompt" className={labelClass}>
              Message
            </label>
            <textarea
              id="builder-test-run-prompt"
              className={textareaClass}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              rows={3}
              placeholder="What should this run do?"
              aria-invalid={!!promptError}
              disabled={starting}
            />
            {promptError && <p className={errorTextClass}>{promptError}</p>}
          </div>

          {workflowRef ? (
            <RunWorkflowForm
              key={savedEpoch}
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
          ) : (
            <p className={hintClass}>Inputs appear once the workflow has been saved. Run saves it first.</p>
          )}

          <div className="flex items-center justify-between gap-2 border-t border-border/60 pt-3">
            <p className={cn(hintClass, "mt-0")}>
              {live ? "Running now. Statuses show on the canvas." : "Saves the draft, then runs it. Tests stay out of your chat list."}
            </p>
            <Button type="submit" variant="primary" size="sm" loading={starting} disabled={live}>
              {testChatId ? "Run again" : "Run"}
            </Button>
          </div>
        </form>
      </ConfigurationPanel>
    </div>
  );
}
