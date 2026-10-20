// Copyright (c) 2025 Reliant Labs

/**
 * Run now — start a workflow by hand, with its inputs, on a machine of your
 * choosing, optionally replaying a past event.
 *
 * A thin host for RunWorkflowForm, like RunWorkflowDialog, but it starts the run
 * with ChatService.RunNow: the launch is recorded as manual.run, it works
 * for a workflow whose Chat trigger is off (pressing the button is how such a
 * workflow is run by hand), the message is optional, and "No machine" is a
 * choice. A manual run is attended: it opens as a chat.
 *
 * Replay: a workflow that declares triggers can be run against an event one of
 * them received. The event's payload is shown as JSON and is editable; the
 * trigger's own inputs and prompt map it, exactly as a real fire would, and the
 * result fills the form. The trigger's filter is not applied — the person has
 * decided the event is wanted.
 */

import { useEffect, useId, useMemo, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";

import { chatGrpc } from "@/api/chat-grpc";
import { triggerGrpc, type ReplayCandidate } from "@/api/trigger-grpc";
import { NO_MACHINE, noMachineBlockerFor } from "@/components/Automations/daemonChoices";
import { loadRunContext } from "@/components/runs/loadRunContext";
import { useDebounce } from "@/hooks/useDebounce";
import { useWorkflowLibrary } from "@/hooks/workflow-library-queries";
import { Modal } from "../../ui/Modal";
import { Button } from "../../ui/Button";
import { getWorkflowDisplayName } from "../useWorkflowInputs";
import { RunFormError } from "./RunFormError";
import { RunWorkflowForm, type RunWorkflowFormStatus } from "./RunWorkflowForm";
import { errorTextClass, fieldClass, hintClass, labelClass, textareaClass } from "./runFormStyles";
import { candidateLabel, formatPayload, parsePayload, REPLAY_KINDS } from "./runNowReplay";
import { EMPTY_RUN_VALUE, type RunWorkflowValue } from "./runWorkflowValues";

export interface RunNowDialogProps {
  open: boolean;
  onClose: () => void;
  projectId: string;
  workflowRef: string;
}

const HAND_WRITTEN = "";

export function RunNowDialog(props: RunNowDialogProps) {
  // Remount per open so each run starts from a clean form.
  if (!props.open) return null;
  return <RunNowDialogBody {...props} />;
}

function errorMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

interface Mapping {
  inputs: Record<string, unknown>;
  prompt: string;
}

function RunNowDialogBody({ onClose, projectId, workflowRef }: RunNowDialogProps) {
  const navigate = useNavigate();
  const ids = useId();
  const id = (name: string) => `${ids}-${name}`;

  const library = useWorkflowLibrary(projectId);
  const listing = library.data?.workflows.find((w) => w.name === workflowRef);
  const triggerNames = useMemo(() => (listing?.triggers ?? []).map((t) => t.name).filter(Boolean), [listing]);
  const canReplay = triggerNames.length > 0;
  const noMachineBlocker = noMachineBlockerFor(library.data?.workflows ?? [], workflowRef);

  const [value, setValue] = useState<RunWorkflowValue>(EMPTY_RUN_VALUE);
  const valueRef = useRef(value);
  valueRef.current = value;
  const [message, setMessage] = useState("");
  const messageEdited = useRef(false);
  const [status, setStatus] = useState<RunWorkflowFormStatus>({ loading: true, missingRequired: [], definitionError: null });
  const [attempted, setAttempted] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);

  // Replay.
  const [replaying, setReplaying] = useState(false);
  const [triggerName, setTriggerName] = useState("");
  const [eventKey, setEventKey] = useState(HAND_WRITTEN);
  const [kind, setKind] = useState<string>(REPLAY_KINDS[0]);
  const [payloadText, setPayloadText] = useState("{}");
  const [mapping, setMapping] = useState<Mapping | null>(null);
  const [mappingError, setMappingError] = useState<string | null>(null);
  const [mapping_, setMapping_] = useState(false);

  const effectiveTrigger = triggerName || (triggerNames.length === 1 ? triggerNames[0]! : "");

  const candidatesQuery = useQuery({
    queryKey: ["run-now", "replay-events", projectId, workflowRef],
    queryFn: () => triggerGrpc.listReplayEvents(projectId, workflowRef),
    enabled: replaying,
    staleTime: 15_000,
  });
  const candidates = useMemo(
    () =>
      (candidatesQuery.data ?? []).filter((c) => !effectiveTrigger || c.triggerName === effectiveTrigger),
    [candidatesQuery.data, effectiveTrigger],
  );
  const chosen: ReplayCandidate | undefined = candidates.find((c) => c.event.id === eventKey);

  const payload = useMemo(() => parsePayload(payloadText), [payloadText]);
  const debouncedPayloadText = useDebounce(payloadText, 300);

  // Map the event through the trigger's inputs and prompt whenever what it
  // reads changes. The server does the mapping, so the form shows what the real
  // trigger would do rather than a second implementation of it.
  useEffect(() => {
    if (!replaying || !effectiveTrigger) {
      setMapping(null);
      setMappingError(null);
      return;
    }
    const parsed = parsePayload(debouncedPayloadText);
    if (!parsed.ok) return;
    let cancelled = false;
    setMapping_(true);
    void triggerGrpc
      .previewReplay(
        projectId,
        workflowRef,
        {
          triggerName: effectiveTrigger,
          eventId: chosen?.event.id,
          kind: chosen?.kind || kind,
          occurredAt: chosen?.event.occurredAt,
          payload: parsed.value,
        },
        chosen?.event.sender,
      )
      .then((result) => {
        if (cancelled) return;
        setMapping(result);
        setMappingError(null);
        // The mapped inputs fill the form; the prompt fills the message until
        // the person writes their own.
        const current = valueRef.current;
        setValue({ ...current, params: { ...current.params, ...result.inputs } });
        if (!messageEdited.current) setMessage(result.prompt);
      })
      .catch((error: unknown) => {
        if (cancelled) return;
        setMapping(null);
        setMappingError(errorMessage(error));
      })
      .finally(() => {
        if (!cancelled) setMapping_(false);
      });
    return () => {
      cancelled = true;
    };
    // chosen/kind identify the event; the payload text is the debounced one.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [replaying, effectiveTrigger, debouncedPayloadText, chosen?.event.id, kind, projectId, workflowRef]);

  const pickEvent = (key: string) => {
    setEventKey(key);
    const candidate = candidates.find((c) => c.event.id === key);
    if (candidate) {
      setPayloadText(formatPayload(candidate.payload));
      setKind(candidate.kind || REPLAY_KINDS[0]);
    }
  };

  const title = `Run ${getWorkflowDisplayName(workflowRef, true)}`;

  const openChat = async (chatId: string, chatProjectId: string) => {
    await loadRunContext(chatId);
    await navigate({ to: "/project/$projectId", params: { projectId: chatProjectId }, search: {} });
  };

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setAttempted(true);
    setFormError(null);
    if (status.loading || status.missingRequired.length > 0 || status.definitionError) return;
    if (replaying) {
      if (!effectiveTrigger) {
        setFormError("Choose the trigger the event arrived through.");
        return;
      }
      if (!payload.ok) return;
      if (mappingError) return;
    }

    const noMachine = value.daemonId === NO_MACHINE;
    setStarting(true);
    try {
      const { chat } = await chatGrpc.runNow({
        projectId,
        workflow: workflowRef,
        message: message.trim() || undefined,
        inputs: value.params,
        presets: value.presets,
        worktreeId: value.worktreeId,
        daemonId: noMachine ? undefined : value.daemonId,
        noMachine,
        replay:
          replaying && payload.ok
            ? {
                triggerName: effectiveTrigger,
                eventId: chosen?.event.id,
                kind: chosen?.kind || kind,
                occurredAt: chosen?.event.occurredAt,
                payload: payload.value,
              }
            : undefined,
      });
      onClose();
      await openChat(chat.id, chat.projectId);
    } catch (error) {
      setFormError(errorMessage(error));
    } finally {
      setStarting(false);
    }
  };

  const payloadInvalid = replaying && !payload.ok;
  const blocked = replaying && (payloadInvalid || !!mappingError);

  return (
    <Modal isOpen onClose={starting ? () => undefined : onClose} title={title} size="lg">
      <form onSubmit={onSubmit} noValidate className="space-y-5" aria-label={title}>
        {formError && <RunFormError message={formError} onNavigate={onClose} />}

        {canReplay && (
          <fieldset className="space-y-2">
            <legend className={labelClass}>Start from</legend>
            <div className="flex flex-wrap gap-4 text-sm">
              <label className="flex items-center gap-2">
                <input
                  type="radio"
                  name={id("mode")}
                  checked={!replaying}
                  onChange={() => setReplaying(false)}
                  disabled={starting}
                />
                A fresh run
              </label>
              <label className="flex items-center gap-2">
                <input
                  type="radio"
                  name={id("mode")}
                  checked={replaying}
                  onChange={() => setReplaying(true)}
                  disabled={starting}
                />
                Replay a past event
              </label>
            </div>
          </fieldset>
        )}

        {replaying && (
          <section aria-label="Replay a past event" className="space-y-3 rounded-lg border border-border/60 bg-card p-3">
            {triggerNames.length > 1 && (
              <div>
                <label htmlFor={id("trigger")} className={labelClass}>
                  Through trigger
                </label>
                <select
                  id={id("trigger")}
                  className={fieldClass}
                  value={effectiveTrigger}
                  onChange={(e) => {
                    setTriggerName(e.target.value);
                    setEventKey(HAND_WRITTEN);
                  }}
                  disabled={starting}
                >
                  <option value="">Choose a trigger</option>
                  {triggerNames.map((name) => (
                    <option key={name} value={name}>
                      {name}
                    </option>
                  ))}
                </select>
                <p className={hintClass}>The trigger whose inputs and prompt read the event.</p>
              </div>
            )}

            <div>
              <label htmlFor={id("event")} className={labelClass}>
                Past event
              </label>
              <select
                id={id("event")}
                className={fieldClass}
                value={eventKey}
                onChange={(e) => pickEvent(e.target.value)}
                disabled={starting}
              >
                <option value={HAND_WRITTEN}>Write one by hand</option>
                {candidates.map((candidate) => (
                  <option key={candidate.event.id} value={candidate.event.id}>
                    {candidateLabel(candidate)}
                  </option>
                ))}
              </select>
              <p className={hintClass} aria-live="polite">
                {candidatesQuery.isLoading
                  ? "Loading this workflow's recent events…"
                  : candidatesQuery.isError
                    ? "Could not load past events; you can still write one by hand."
                    : candidates.length === 0
                      ? "No events yet: enable the workflow's triggers to collect some, or write one by hand."
                      : "Events its triggers received, newest first. Their filters turned some away; any can be replayed."}
              </p>
            </div>

            {!chosen && (
              <div>
                <label htmlFor={id("kind")} className={labelClass}>
                  Event kind
                </label>
                <select
                  id={id("kind")}
                  className={fieldClass}
                  value={kind}
                  onChange={(e) => setKind(e.target.value)}
                  disabled={starting}
                >
                  {REPLAY_KINDS.map((k) => (
                    <option key={k} value={k}>
                      {k}
                    </option>
                  ))}
                </select>
              </div>
            )}

            <div>
              <label htmlFor={id("payload")} className={labelClass}>
                Event payload (JSON)
              </label>
              <textarea
                id={id("payload")}
                className={`${textareaClass} font-mono text-xs`}
                rows={8}
                spellCheck={false}
                value={payloadText}
                onChange={(e) => setPayloadText(e.target.value)}
                disabled={starting}
                aria-invalid={payloadInvalid}
                aria-describedby={payloadInvalid ? id("payload-error") : id("payload-hint")}
              />
              {payload.ok === false ? (
                <p id={id("payload-error")} className={errorTextClass} role="alert">
                  {payload.error}
                </p>
              ) : (
                <p id={id("payload-hint")} className={hintClass}>
                  What the trigger reads as <code>trigger.payload</code>. Edit it freely: the run sees what is here.
                </p>
              )}
            </div>

            <div aria-live="polite" data-testid="replay-mapping">
              {mappingError ? (
                <p className={errorTextClass} role="alert">
                  {mappingError}
                </p>
              ) : mapping_ ? (
                <p className={hintClass}>Mapping the event…</p>
              ) : mapping ? (
                <MappingSummary mapping={mapping} />
              ) : null}
            </div>
          </section>
        )}

        <div>
          <label htmlFor={id("message")} className={labelClass}>
            Message
          </label>
          <textarea
            id={id("message")}
            className={textareaClass}
            value={message}
            onChange={(e) => {
              messageEdited.current = true;
              setMessage(e.target.value);
            }}
            rows={4}
            disabled={starting}
            placeholder={replaying ? "The trigger's prompt appears here." : "What should this run do? Optional."}
            aria-describedby={id("message-hint")}
          />
          <p id={id("message-hint")} className={hintClass}>
            {replaying
              ? "The event is added after this message, as it would be for a real fire."
              : "Leave it blank to start the run from “Run now.” The run opens as a chat you can answer."}
          </p>
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
          offerNoMachine
          noMachineBlocker={noMachineBlocker}
          disabled={starting}
        />

        <div className="flex justify-end gap-2 border-t border-border/60 pt-4">
          <Button type="button" variant="ghost" onClick={onClose} disabled={starting}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" loading={starting} disabled={blocked}>
            Run now
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function MappingSummary({ mapping }: { mapping: Mapping }) {
  const entries = Object.entries(mapping.inputs);
  if (entries.length === 0 && !mapping.prompt) {
    return <p className={hintClass}>This trigger maps nothing from the event; the run reads it from the message.</p>;
  }
  return (
    <div className="rounded-md bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
      <p className="font-medium text-foreground">The trigger maps this event to</p>
      <ul className="mt-1 space-y-0.5">
        {entries.map(([name, value]) => (
          <li key={name}>
            <code>{name}</code> = <code>{JSON.stringify(value)}</code>
          </li>
        ))}
        {mapping.prompt && <li>a prompt, filled into the message</li>}
      </ul>
    </div>
  );
}
