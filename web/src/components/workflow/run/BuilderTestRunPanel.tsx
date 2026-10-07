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
 * The run is drawn on the canvas by the builder (see useBuilderTestRun);
 * this panel reports the run's own state, takes the author to the step that
 * failed, and keeps the inputs so "Run again" repeats the same run.
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
import { RunFormError } from "./RunFormError";
import { RunWorkflowForm, type RunWorkflowFormStatus } from "./RunWorkflowForm";
import { errorTextClass, hintClass, labelClass, textareaClass } from "./runFormStyles";
import { EMPTY_RUN_VALUE, type RunWorkflowValue } from "./runWorkflowValues";
import "../config/config-panel.css";

/** What a test run was started with; "Run again" starts the next one with the same. */
export interface TestRunRequest {
  prompt: string;
  value: RunWorkflowValue;
}

/** The step a finished test run failed at, as the panel names it. */
export interface TestRunFailure {
  /** "Call LLM · summarize" */
  label: string;
  message?: string;
}

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
  /** The last run's message and inputs, so the panel reopens with them. */
  initialRequest?: TestRunRequest | null;
  onStarted: (chatId: string, request: TestRunRequest) => void;
  /** The step the run failed at, when one did. */
  failure?: TestRunFailure | null;
  /** Select the failed step and, when its error names a field, that field. */
  onGoToProblem?: () => void;
  /** Take the run off the canvas. */
  onClear?: () => void;
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
  initialRequest,
  onStarted,
  failure,
  onGoToProblem,
  onClear,
  onClose,
  bottomOffset,
  topOffset,
  docked = false,
}: BuilderTestRunPanelProps) {
  const [value, setValue] = useState<RunWorkflowValue>(() => initialRequest?.value ?? EMPTY_RUN_VALUE);
  const [prompt, setPrompt] = useState(() => initialRequest?.prompt ?? "");
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
  // Only a run that is executing holds Run again back. A run parked on a
  // failed step (paused) or waiting on a person is still live, but the next
  // test run is a separate run, and starting it is how a fix gets tried.
  const busy = runState?.key === "running" || runState?.key === "queued";

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
      onStarted(started.id, { prompt: prompt.trim(), value });
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
          {formError && <RunFormError message={formError} />}

          {testChatId && (
            <div className="space-y-2" data-testid="test-run-status">
              <div className="flex flex-wrap items-center gap-2">
                {runState && <RunStatusBadge status={runState} size="md" />}
                <Link
                  to="/workflows/runs/$runId"
                  params={{ runId: testChatId }}
                  className="text-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                >
                  Watch full run
                </Link>
                {onClear && !live && (
                  <button
                    type="button"
                    onClick={onClear}
                    className="ml-auto text-xs text-muted-foreground hover:text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                  >
                    Clear from canvas
                  </button>
                )}
              </div>
              {failure && (
                <div
                  data-testid="test-run-failure"
                  className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive-ink"
                >
                  <p className="font-medium">{failure.label} failed</p>
                  {failure.message && <p className="mt-0.5 line-clamp-3 break-words">{failure.message}</p>}
                  {onGoToProblem && (
                    <Button type="button" variant="secondary" size="sm" className="mt-2" onClick={onGoToProblem}>
                      Go to problem
                    </Button>
                  )}
                </div>
              )}
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
              {busy ? "Running now. Statuses show on the canvas." : "Saves the draft, then runs it. Tests stay out of your chat list."}
            </p>
            <Button type="submit" variant="primary" size="sm" loading={starting} disabled={busy}>
              {testChatId ? "Run again" : "Run"}
            </Button>
          </div>
        </form>
      </ConfigurationPanel>
    </div>
  );
}
