// Copyright (c) 2025 Reliant Labs

/**
 * New / edit automation with its own WHEN: an inline trigger row
 * (CreateTrigger / UpdateTrigger). The automation dialog renders this unless
 * the automation turns on a workflow's DECLARED triggers (DeclaredAutomationForm,
 * EnableWorkflow); the person sees one dialog and one word either way.
 *
 * In the order a person decides (report 03 §6.1): When → Do (the prompt) →
 * where it runs, as one inferred line. Name, project, workflow and machine
 * are inferred; the workspace, the inputs that have defaults, overlap and
 * who it writes as wait behind a disclosure. Catch-up window and "also
 * every" are not offered (a stored value is kept and sent back), and there is
 * no Enabled switch: creating it turns it on.
 *
 * UpdateTrigger is a full replacement, so every field the trigger carries is
 * in this form's state and goes back on save — a source the person did not
 * touch goes back exactly as stored.
 */

import { useEffect, useId, useMemo, useRef, useState, type FormEvent } from "react";
import { Pencil } from "lucide-react";
import { toast } from "sonner";

import { useIntegrationEventNaming } from "@/hooks/catalog-queries";
import { useProjects } from "@/hooks/project-queries";
import { triggerErrorMessage, type ActingIdentityKey, type OverlapPolicy, type Trigger, type TriggerDefinitionInput, type WebhookCredential } from "@/hooks/trigger-model";
import { useCreateTrigger, useProjectDaemonInstalls, useTriggers, useUpdateTrigger } from "@/hooks/trigger-queries";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useWorkflowLibrary } from "@/hooks/workflow-library-queries";
import { describeSchedule, timezoneLabel } from "@/lib/cronText";
import { describeDeclaredSource, integrationOf, type DeclaredTrigger } from "@/lib/declaredTriggers";
import { triggerRunsWorkflow } from "@/lib/triggerRail";
import { cn } from "@/lib/utils";
import { workflowDisplayName } from "@/lib/workflowDisplayName";
import { useProjectStore } from "@/store/projectStore";
import Button from "../../forge-ui/button";
import Modal from "../../forge-ui/modal";
import ToggleSwitch from "../../forge-ui/toggle_switch";
import { normalizeWorkflowRef } from "../../workflow/useWorkflowInputs";
import { RunWorkflowForm, type RunWorkflowFormStatus } from "../../workflow/run/RunWorkflowForm";
import { errorTextClass, fieldClass, hintClass, labelClass, textareaClass } from "../../workflow/run/runFormStyles";
import { countRunInputs, type RunWorkflowValue } from "../../workflow/run/runWorkflowValues";
import { ActingIdentityField, defaultActingIdentity } from "../ActingIdentityField";
import { buildDaemonChoices, NO_MACHINE, noMachineBlockerFor } from "../daemonChoices";
import { scheduleFromForm, serverErrorField, validateScheduleForm } from "../scheduleForm";
import { validateTimezone } from "../timezone";
import { autoName, uniqueName } from "./autoName";
import { DescribeIt } from "./DescribeIt";
import { Disclosure } from "./Disclosure";
import { defaultMachine } from "./machineDefault";
import { useRecentMachine } from "./useRecentMachine";
import type { AutomationPrefill } from "./openAutomationDialog";
import { WebhookCredentialPanel } from "./WebhookCredentialPanel";
import { WhenField } from "./WhenField";
import { TestEventPreview } from "./TestEventPreview";
import { WhereSummary } from "./WhereSummary";
import { defaultWhen, filterOfWhen, sourceFromWhen, whenError, whenFromSource, type WhenState } from "./whenModel";

export interface InlineAutomationFormProps {
  onClose: () => void;
  /** The automation being edited; absent to create one. */
  trigger?: Trigger;
  prefill?: AutomationPrefill;
  projectId?: string;
  /** The workflow it runs, fixed (opened for one workflow). */
  workflowRef?: string;
  workflowTitle?: string;
  onSaved?: (triggers: Trigger[]) => void;
  /** Turn on a workflow that already declares this WHEN instead. */
  onUseWorkflow?: (workflowRef: string, workflowTitle: string, projectId: string) => void;
  /** The workflow declares its own triggers: go back to them. */
  onBackToDeclared?: () => void;
}

interface FieldErrors {
  project?: string;
  machine?: string;
  message?: string;
  inputs?: string;
  when?: string;
  form?: string;
}

const UNKNOWN_SOURCE_MESSAGE = "This automation's trigger was set up in a newer version of Reliant. Reload the app to edit it.";

export function InlineAutomationForm({
  onClose,
  trigger,
  prefill: prefillProp,
  projectId: projectIdProp,
  workflowRef,
  workflowTitle,
  onSaved,
  onUseWorkflow,
  onBackToDeclared,
}: InlineAutomationFormProps) {
  const isEdit = !!trigger;
  const initialPrefill = isEdit ? undefined : prefillProp;
  const ids = useId();
  const fid = (name: string) => `${ids}-${name}`;
  const createTrigger = useCreateTrigger();
  const updateTrigger = useUpdateTrigger();
  const naming = useIntegrationEventNaming();

  const { projects } = useProjects();
  const currentProjectId = useProjectStore((s) => s.currentProject?.id);

  // An inline source this client cannot edit (or an arm it predates) is sent
  // back verbatim; an unknown one cannot be saved at all.
  const storedWhen = trigger ? whenFromSource(trigger.source, { filter: trigger.filter }) : undefined;
  const fixedWorkflow = workflowRef !== undefined && !isEdit;

  const [name, setName] = useState(trigger?.name ?? initialPrefill?.name ?? "");
  const [editingName, setEditingName] = useState(false);
  const [projectId, setProjectId] = useState(trigger?.projectId ?? initialPrefill?.projectId ?? projectIdProp ?? currentProjectId ?? "");
  const [workflow, setWorkflow] = useState(trigger?.workflow ?? workflowRef ?? initialPrefill?.workflow ?? "");
  const [pendingWorkflow, setPendingWorkflow] = useState<string | null>(null);
  const [inputs, setInputs] = useState<RunWorkflowValue>(() => ({
    presets: { ...(trigger?.presets ?? initialPrefill?.presets ?? {}) },
    params: { ...(trigger?.params ?? initialPrefill?.params ?? {}) },
    worktreeId: trigger?.worktreeId ?? initialPrefill?.worktreeId,
  }));
  const [inputsStatus, setInputsStatus] = useState<RunWorkflowFormStatus | null>(null);
  const [machine, setMachine] = useState(trigger ? (trigger.noMachine ? NO_MACHINE : trigger.daemonId) : (initialPrefill?.machine ?? ""));
  // Once chosen (by the person, a prefill or the stored value) the machine is
  // never re-defaulted: a project switch must not silently move the runs.
  const machineChosen = useRef(!!trigger || !!initialPrefill?.machine);
  const [message, setMessage] = useState(trigger?.message ?? initialPrefill?.message ?? "");
  const [when, setWhen] = useState<WhenState>(() => {
    if (storedWhen) return storedWhen;
    if (initialPrefill?.source) {
      const fromPrefill = whenFromSource(initialPrefill.source, { filter: initialPrefill.filter, catalogRef: initialPrefill.catalogRef });
      if (fromPrefill) return fromPrefill;
    }
    return defaultWhen();
  });
  const whenTouched = useRef(false);
  const [connectionId, setConnectionId] = useState(trigger?.connectionId ?? "");
  const [notify, setNotify] = useState(trigger?.notifyOnComplete ?? initialPrefill?.notifyOnComplete ?? false);
  // Keep a stored or explicit actor; an untouched new automation defaults to the bot.
  const [chosenIdentity, setActingIdentity] = useState<ActingIdentityKey | undefined>(trigger?.actingIdentity);
  const actingIdentity = chosenIdentity ?? defaultActingIdentity(when.kind === "integration" && !!when.event && integrationOf(when.event)?.integration === "github");
  const [assumptions, setAssumptions] = useState(initialPrefill?.assumptions ?? []);
  const [inputsOpen, setInputsOpen] = useState(false);
  // Open when a stored identity differs from what a new automation would get.
  const [advancedOpen, setAdvancedOpen] = useState(when.overlap === "allow" || (!!trigger?.actingIdentity && trigger.actingIdentity !== defaultActingIdentity(false)));
  const [serverErrors, setErrors] = useState<FieldErrors>({});
  const [blurred, setBlurred] = useState<Partial<Record<keyof FieldErrors, boolean>>>({});
  const blurField = (key: keyof FieldErrors) => setBlurred((prev) => ({ ...prev, [key]: true }));
  const [attempted, setAttempted] = useState(false);
  const [saving, setSaving] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [created, setCreated] = useState<{ trigger: Trigger; webhook?: WebhookCredential } | null>(null);
  const dirty = useRef(false);
  const touch = () => (dirty.current = true);

  useEffect(() => {
    if (!projectId && projects.length > 0) setProjectId(currentProjectId ?? projects[0]!.id);
  }, [projectId, projects, currentProjectId]);

  const library = useWorkflowLibrary(projectId || undefined);
  const listed = useMemo(() => (library.data?.workflows ?? []).filter((w) => w.status !== "draft"), [library.data]);
  const listing = listed.find((w) => triggerRunsWorkflow({ workflow: w.name }, workflow));
  const workflowName = workflow ? (listing ? workflowDisplayName(listing) : (workflowTitle ?? normalizeWorkflowRef(workflow))) : "";
  const noMachineBlocker = useMemo(() => noMachineBlockerFor(listed, workflow), [listed, workflow]);

  const projectTriggers = useTriggers(projectId || undefined, { enabled: !!projectId });
  const { daemons, loading: daemonsLoading } = useDaemonStatus();
  const installs = useProjectDaemonInstalls();
  const choices = useMemo(() => buildDaemonChoices(daemons, installs.data ?? [], projectId), [daemons, installs.data, projectId]);
  const { lastUsed, loading: historyLoading } = useRecentMachine(projectId, projectTriggers.data ?? [], choices);
  const machineDataReady = !daemonsLoading && !installs.isLoading && !historyLoading && (!workflow || library.isSuccess);
  useEffect(() => {
    if (!machineDataReady || machineChosen.current) return;
    const next = defaultMachine({ choices, needsNoMachine: !!workflow && !noMachineBlocker, lastUsed });
    if (next !== machine) setMachine(next);
  }, [machineDataReady, choices, workflow, noMachineBlocker, lastUsed, machine]);

  // ---- derived ---------------------------------------------------------------
  const whenWords = useMemo(() => {
    if (when.kind === "schedule") {
      if (validateScheduleForm(when.schedule)) return "";
      // A name says when in the person's own terms; the zone is the dialog's, not the name's.
      const zone = when.timezone.trim() || "UTC";
      const words = describeSchedule({ ...scheduleFromForm(when.schedule), timezone: zone });
      const suffix = ` ${timezoneLabel(zone)}`;
      return words.endsWith(suffix) ? words.slice(0, -suffix.length) : words;
    }
    if (when.kind === "webhook") return "On webhook";
    return when.event ? describeDeclaredSource(when.event, describeSchedule, naming) : "";
  }, [when, naming]);
  const derivedName = autoName({ when: whenWords, workflowName, prompt: message });
  const shownName = name.trim() || derivedName;

  // A Library workflow that already declares this app event: turning that on
  // is usually the better answer than a hand-made automation (§6.3).
  const handlers = useMemo(() => {
    if (isEdit || fixedWorkflow || when.kind !== "integration" || !when.event || !onUseWorkflow) return [];
    const mine = integrationOf(when.event);
    if (!mine?.integration) return [];
    return listed.filter((w) =>
      (w.triggers ?? []).some((t) => {
        const theirs = integrationOf(t as unknown as DeclaredTrigger);
        return theirs?.integration === mine.integration && theirs.events.some((e) => mine.events.includes(e));
      }),
    );
  }, [isEdit, fixedWorkflow, when, listed, onUseWorkflow]);

  // ---- describe it -----------------------------------------------------------
  const applyDraft = (p: AutomationPrefill) => {
    touch();
    if (p.message !== undefined) setMessage(p.message);
    if (!fixedWorkflow && p.workflow !== undefined && p.workflow !== workflow) {
      setWorkflow(p.workflow);
      setInputs((prev) => ({ presets: {}, params: {}, worktreeId: prev.worktreeId }));
    }
    if (p.params || p.presets) setInputs((prev) => ({ ...prev, presets: { ...(p.presets ?? {}) }, params: { ...(p.params ?? {}) } }));
    if (p.source) {
      const next = whenFromSource(p.source, { filter: p.filter, catalogRef: p.catalogRef });
      if (next) setWhen(next);
    }
    if (p.name) setName(p.name);
    setAssumptions(p.assumptions ?? []);
  };

  // ---- workflow switch -------------------------------------------------------
  const applyWorkflow = (next: string) => {
    setWorkflow(next);
    setInputs((prev) => ({ presets: {}, params: {}, worktreeId: prev.worktreeId }));
    setPendingWorkflow(null);
  };
  const requestWorkflowChange = (next: string) => {
    touch();
    if (normalizeWorkflowRef(next) === normalizeWorkflowRef(workflow)) return;
    if (countRunInputs(inputs) > 0) {
      setPendingWorkflow(next);
      return;
    }
    applyWorkflow(next);
  };

  // ---- validation ------------------------------------------------------------
  const validate = (): FieldErrors => {
    const next: FieldErrors = {};
    if (!projectId) next.project = "Choose a project.";
    if (!machine) {
      next.machine = daemons.length === 0 ? "Connect a machine first, or choose No machine." : "Choose where it runs.";
    } else if (machine === NO_MACHINE && noMachineBlocker && !trigger?.noMachine) {
      next.machine = `No machine isn't available: ${noMachineBlocker}. Choose a machine.`;
    } else if (machine !== NO_MACHINE && machineDataReady && !choices.some((c) => c.daemonId === machine && c.eligible && c.statusLabel !== "failed")) {
      next.machine = "This machine is no longer available for this project. Choose another.";
    }
    if (!message.trim()) next.message = "Say what each run should do.";
    if (pendingWorkflow !== null) next.inputs = "Confirm or cancel the workflow change first.";
    else if (inputsStatus?.loading && workflow) next.inputs = "Wait for this workflow's inputs to load.";
    else if (inputsStatus && inputsStatus.missingRequired.length > 0) next.inputs = `Fill in the required inputs: ${inputsStatus.missingRequired.join(", ")}.`;
    if (trigger && !storedWhen && trigger.source.kind === "unknown") next.form = UNKNOWN_SOURCE_MESSAGE;
    if (!trigger || storedWhen || whenTouched.current) {
      const whenProblem = when.kind === "schedule" ? (validateScheduleForm(when.schedule) ?? validateTimezone(when.timezone)) : whenError(when);
      if (whenProblem) next.when = whenProblem;
    }
    return next;
  };

  // Compute client errors from current values so fixing a field clears it immediately.
  const currentErrors = validate();
  const errors: FieldErrors = { ...serverErrors };
  for (const key of ["when", "message", "inputs", "project", "machine"] as const) {
    if (attempted || blurred[key]) errors[key] = currentErrors[key] ?? serverErrors[key];
  }
  const requiredLeft = Object.keys(currentErrors).filter((key) => key !== "form" && key !== "inputs").length +
    (inputsStatus?.missingRequired.length || (currentErrors.inputs ? 1 : 0));

  const buildDefinition = (): TriggerDefinitionInput => {
    // An edit whose WHEN was not touched sends back the stored arm itself, so
    // fields this client does not edit (HMAC, poll interval) survive.
    const keepStored = !!trigger && !whenTouched.current;
    const source = keepStored ? trigger!.source : sourceFromWhen(when);
    const taken = (projectTriggers.data ?? []).filter((t) => t.projectId === projectId && t.id !== trigger?.id).map((t) => t.name);
    const finalName = name.trim() || uniqueName(derivedName, taken);
    return {
      name: finalName,
      projectId,
      worktreeId: machine === NO_MACHINE ? undefined : inputs.worktreeId,
      workflow,
      presets: inputs.presets,
      params: inputs.params,
      message: message.trim(),
      daemonId: machine === NO_MACHINE ? "" : machine,
      noMachine: machine === NO_MACHINE,
      notifyOnComplete: notify,
      actingIdentity,
      connectionId: when.kind === "integration" ? connectionId || undefined : undefined,
      source,
      filter: keepStored ? trigger!.filter : filterOfWhen(when),
    };
  };

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setAttempted(true);
    const next = validate();
    setErrors({});
    if (Object.keys(next).length > 0) {
      const first = (["when", "message", "inputs", "project", "machine"] as const).find((k) => next[k]);
      if (first === "inputs") setInputsOpen(true);
      if (first) document.getElementById(fid(first === "when" ? "when-anchor" : first))?.focus();
      return;
    }
    setSaving(true);
    try {
      const definition = buildDefinition();
      if (trigger) {
        const saved = await updateTrigger.mutateAsync({ id: trigger.id, input: definition });
        toast.success(`${saved.name} is saved`);
        onSaved?.([saved]);
        onClose();
        return;
      }
      const result = await createTrigger.mutateAsync(definition);
      onSaved?.([result.trigger]);
      if (result.webhook || when.kind === "webhook" || when.kind === "integration" || when.kind === "workflowEvent") {
        setCreated(result);
      } else {
        toast.success(`${result.trigger.name} is on`);
        onClose();
      }
    } catch (error) {
      const text = triggerErrorMessage(error);
      const field = serverErrorField(text);
      setErrors(
        field === "daemon"
          ? { machine: text }
          : field === "schedule" || field === "timezone" || field === "catchup"
            ? { when: text }
            : { form: text },
      );
    } finally {
      setSaving(false);
    }
  };

  const requestClose = () => {
    if (saving) return;
    if (confirmDiscard) {
      setConfirmDiscard(false);
      return;
    }
    if (dirty.current) {
      setConfirmDiscard(true);
      return;
    }
    onClose();
  };

  // ---- created (webhook) -----------------------------------------------------
  if (created) {
    return (
      <Modal open onClose={onClose} size="lg" title={`${created.trigger.name} is on`}>
        <div className="space-y-4">
          {when.kind === "webhook" ? <>
            <p className="text-sm text-muted-foreground">Point the sender at this URL. Each request starts a run, and the request body is attached to it.</p>
            <WebhookCredentialPanel trigger={created.trigger} credential={created.webhook} />
          </> : <>
            <p className="text-sm text-muted-foreground">The next matching event starts a run. Preview a sample without starting anything.</p>
            <TestEventPreview trigger={created.trigger} />
          </>}
          <div className="flex justify-end border-t border-border pt-4">
            <Button variant="primary" onClick={onClose}>
              Done
            </Button>
          </div>
        </div>
      </Modal>
    );
  }

  const title = isEdit ? "Edit automation" : "New automation";
  const showWritesAsInAdvanced = when.kind !== "integration" && (!!workflow || actingIdentity === "app");
  const primaryLabel = isEdit ? "Save changes" : when.kind === "webhook" ? "Create and show URL" : "Create";

  return (
    <Modal open onClose={requestClose} title={title} size="lg">
      <form onSubmit={onSubmit} onChange={() => { touch(); setErrors({}); }} noValidate className="space-y-6" aria-label={title}>
        <div className="-mt-2 flex min-w-0 items-center gap-2 text-sm">
          {editingName ? (
            <>
              <label htmlFor={fid("name")} className="sr-only">
                Name
              </label>
              <input
                id={fid("name")}
                className={fieldClass}
                value={name}
                placeholder={derivedName}
                autoFocus
                onChange={(e) => setName(e.target.value)}
                onBlur={() => setEditingName(false)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    setEditingName(false);
                  }
                }}
              />
            </>
          ) : (
            <button
              type="button"
              onClick={() => setEditingName(true)}
              className="group inline-flex min-w-0 items-center gap-1.5 rounded-sm text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
              aria-label={`Name: ${shownName || "derived from when and what"}. Rename`}
              data-testid="automation-name"
            >
              <span className="truncate">{shownName || "Named from when and what"}</span>
              <Pencil className="h-3.5 w-3.5 shrink-0 opacity-60 group-hover:opacity-100" aria-hidden />
            </button>
          )}
        </div>

        {errors.form && (
          <div role="alert" className="rounded-md border border-danger-border bg-danger-surface px-3 py-2 text-sm text-danger-ink">
            {errors.form}
          </div>
        )}

        {!isEdit && projectId && <DescribeIt projectId={projectId} workflow={fixedWorkflow ? workflow : undefined} onDraft={applyDraft} />}

        {assumptions.length > 0 && (
          <div role="note" className="rounded-md border border-border/60 bg-background px-3 py-2 text-xs text-muted-foreground">
            <span className="font-medium text-foreground">Check these:</span>
            <ul className="mt-1 list-disc space-y-0.5 pl-4">
              {assumptions.map((a) => (
                <li key={a}>{a}</li>
              ))}
            </ul>
          </div>
        )}

        <section className="space-y-2" aria-labelledby={fid("when-heading")} onBlur={() => blurField("when")}>
          <h3 id={fid("when-heading")} className="text-sm font-semibold text-foreground">
            When
          </h3>
          <span id={fid("when-anchor")} tabIndex={-1} className="sr-only focus:outline-none">
            When it runs
          </span>
          {trigger && !storedWhen ? (
            <p className="rounded-md border border-border/60 bg-background px-3 py-2 text-sm text-muted-foreground">
              {trigger.source.kind === "unknown" ? UNKNOWN_SOURCE_MESSAGE : "This trigger's event can't be edited here yet; everything else can."}
            </p>
          ) : (
            <WhenField
              idPrefix={fid("when")}
              when={when}
              onChange={(next) => {
                whenTouched.current = true;
                touch();
                setWhen(next);
              }}
              error={errors.when}
              connectionId={connectionId}
              onConnectionChange={(id) => {
                touch();
                setConnectionId(id);
              }}
              actingIdentity={actingIdentity}
              onActingIdentityChange={setActingIdentity}
              triggerId={trigger?.id}
            >
              {handlers.length > 0 && (
                <p className="rounded-md border border-border/60 bg-background px-3 py-2 text-xs text-muted-foreground" data-testid="workflow-handlers">
                  Workflows that already handle this:{" "}
                  {handlers.map((w, i) => (
                    <span key={w.name}>
                      {i > 0 && ", "}
                      <span className="font-medium text-foreground">{workflowDisplayName(w)}</span> ·{" "}
                      <button
                        type="button"
                        onClick={() => onUseWorkflow!(w.name, workflowDisplayName(w), projectId)}
                        className="rounded-sm font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                      >
                        Use it
                      </button>
                    </span>
                  ))}
                </p>
              )}
            </WhenField>
          )}
          {onBackToDeclared && (
            <button
              type="button"
              onClick={onBackToDeclared}
              className="rounded-sm text-xs font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            >
              Use the workflow's own triggers instead
            </button>
          )}
        </section>

        <section className="space-y-3" aria-labelledby={fid("do-heading")}>
          <h3 id={fid("do-heading")} className="text-sm font-semibold text-foreground">
            Do
          </h3>
          {fixedWorkflow ? (
            <p className="text-sm text-foreground">
              Runs <span className="font-medium">{workflowName || "this workflow"}</span>
            </p>
          ) : (
            <div>
              <label htmlFor={fid("workflow")} className="sr-only">
                Workflow
              </label>
              <select
                id={fid("workflow")}
                className={cn(fieldClass, "sm:w-auto")}
                value={pendingWorkflow ?? workflow}
                onChange={(e) => requestWorkflowChange(e.target.value)}
                aria-describedby={fid("workflow-hint")}
              >
                <option value="">Agent (your default)</option>
                {workflow && !listed.some((w) => triggerRunsWorkflow({ workflow: w.name }, workflow)) && (
                  <option value={workflow}>{workflowName}</option>
                )}
                {listed
                  .slice()
                  .sort((a, b) => workflowDisplayName(a).localeCompare(workflowDisplayName(b)))
                  .map((w) => (
                    <option key={w.name} value={w.name}>
                      {workflowDisplayName(w)}
                    </option>
                  ))}
              </select>
              <p id={fid("workflow-hint")} className={hintClass}>
                {workflow ? (listing?.description ?? "Its steps run each time.") : "Your default agent and its tools. Pick a workflow for a fixed set of steps."}
              </p>
            </div>
          )}

          {pendingWorkflow !== null && (
            <div role="alertdialog" aria-labelledby={fid("switch-title")} className="rounded-md border border-warning/40 bg-background px-3 py-3">
              <p id={fid("switch-title")} className="text-sm font-medium text-foreground">
                Switch workflow? This clears the input settings you made for the current one.
              </p>
              <div className="mt-3 flex justify-end gap-2">
                <Button type="button" variant="ghost" size="sm" onClick={() => setPendingWorkflow(null)}>
                  Keep current workflow
                </Button>
                <Button type="button" variant="primary" size="sm" onClick={() => applyWorkflow(pendingWorkflow)}>
                  Switch and clear inputs
                </Button>
              </div>
            </div>
          )}

          <div>
            <label htmlFor={fid("message")} className={labelClass}>
              What should it do?
            </label>
            <textarea
              id={fid("message")}
              className={textareaClass}
              rows={4}
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              placeholder="Summarise yesterday's failed CI runs and open an issue for each new failure."
              onBlur={() => blurField("message")}
              aria-invalid={!!errors.message}
              aria-describedby={`${fid("message-hint")}${errors.message ? ` ${fid("message-error")}` : ""}`}
            />
            <p id={fid("message-hint")} className={hintClass}>
              {when.kind === "webhook"
                ? "Each run starts from this message, with the request body attached. Nobody will be watching, so say everything the agent needs."
                : "Each run starts from this message. Nobody will be watching, so say everything the agent needs."}
            </p>
            {errors.message && <p id={fid("message-error")} className={errorTextClass} role="alert">{errors.message}</p>}
          </div>

          {workflow && projectId && (
            <div
              id={fid("inputs")}
              tabIndex={-1}
              className="focus:outline-none"
              onBlur={() => blurField("inputs")}
            >
              <Disclosure
                open={inputsOpen || (attempted && !!errors.inputs)}
                onToggle={() => setInputsOpen((o) => !o)}
                title="Workflow settings"
                summary={
                  inputsStatus?.missingRequired.length
                    ? `${inputsStatus.missingRequired.length} required`
                    : countRunInputs(inputs) > 0
                      ? `${countRunInputs(inputs)} set`
                      : "all defaults"
                }
              >
                <RunWorkflowForm
                  projectId={projectId}
                  workflowRef={workflow}
                  value={inputs}
                  onChange={setInputs}
                  onStatusChange={(status) => {
                    setInputsStatus(status);
                    // Required inputs with no default block saving: show them.
                    if (status.missingRequired.length > 0 && !isEdit) setInputsOpen(true);
                  }}
                  showValidation={attempted || !!blurred.inputs}
                  applyDefaultPresets={!isEdit && !initialPrefill?.presets && !initialPrefill?.params}
                  hideWorktree
                  hideInputsLabel
                />
              </Disclosure>
              {attempted && errors.inputs && !inputsStatus?.missingRequired.length && (
                <p className={errorTextClass} role="alert">
                  {errors.inputs}
                </p>
              )}
            </div>
          )}
        </section>

        <WhereSummary
          idPrefix={ids}
          projects={projects}
          projectId={projectId}
          onProjectChange={
            isEdit
              ? undefined
              : (next) => {
                  touch();
                  setProjectId(next);
                  setInputs((prev) => ({ ...prev, worktreeId: undefined }));
                }
          }
          machine={machine}
          onMachineChange={(next) => {
            touch();
            machineChosen.current = true;
            setMachine(next);
          }}
          choices={choices}
          daemonsLoading={daemonsLoading}
          hasDaemons={daemons.length > 0}
          noMachineBlocker={noMachineBlocker}
          needsNoMachine={!!workflow && !noMachineBlocker}
          needsMachine={!!listing?.needsMachine?.length}
          worktreeId={inputs.worktreeId}
          onWorktreeChange={(worktreeId) => setInputs((prev) => ({ ...prev, worktreeId }))}
          projectError={errors.project}
          machineError={errors.machine}
          onBlurField={blurField}
        />

        <div>
          <div className="flex items-center gap-2">
            <ToggleSwitch
              checked={notify}
              onChange={(v) => {
                touch();
                setNotify(v);
              }}
              label="Notify me when a run succeeds"
              size="lg"
              className="forge-ui"
            />
          </div>
          <p className={hintClass}>Failures always notify you, once per streak.</p>
        </div>

        {(when.kind === "schedule" || showWritesAsInAdvanced) && (
          <Disclosure open={advancedOpen} onToggle={() => setAdvancedOpen((o) => !o)} title="Advanced">
            {when.kind === "schedule" && (
              <fieldset>
                <legend className={labelClass}>If the previous run is still going</legend>
                <div className="space-y-2">
                  <OverlapOption
                    id={fid("overlap-skip")}
                    name={fid("overlap")}
                    checked={when.overlap === "skip"}
                    onSelect={() => {
                      whenTouched.current = true;
                      setWhen((w) => ({ ...w, overlap: "skip" as OverlapPolicy }));
                    }}
                    title="Skip this run"
                    description="Recommended. Nothing starts while the last run is active or paused; the skip is recorded in the history."
                  />
                  <OverlapOption
                    id={fid("overlap-allow")}
                    name={fid("overlap")}
                    checked={when.overlap === "allow"}
                    onSelect={() => {
                      whenTouched.current = true;
                      setWhen((w) => ({ ...w, overlap: "allow" as OverlapPolicy }));
                    }}
                    title="Start another run anyway"
                    description="Runs can pile up side by side if each one takes longer than the gap between them."
                  />
                </div>
              </fieldset>
            )}
            {showWritesAsInAdvanced && <ActingIdentityField value={actingIdentity} onChange={setActingIdentity} />}
          </Disclosure>
        )}

        {confirmDiscard ? (
          <div role="alertdialog" aria-labelledby={fid("discard-title")} className="flex flex-wrap items-center justify-between gap-3 border-t border-border/60 pt-4">
            <div className="min-w-0">
              <p id={fid("discard-title")} className="text-sm font-medium text-foreground">
                Discard your changes?
              </p>
              <p className="text-xs text-muted-foreground">{isEdit ? "Your edits to this automation" : "This new automation"} will be lost.</p>
            </div>
            <div className="flex gap-2">
              <Button type="button" variant="ghost" autoFocus onClick={() => setConfirmDiscard(false)}>
                Keep editing
              </Button>
              <Button type="button" variant="danger" onClick={onClose}>
                Discard
              </Button>
            </div>
          </div>
        ) : (
          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-border/60 pt-4">
            <p className="mr-auto text-xs text-muted-foreground" role="status">{requiredLeft ? `${requiredLeft} required ${requiredLeft === 1 ? "field" : "fields"} left` : "Ready to save"}</p>
            <Button type="button" variant="ghost" onClick={requestClose} disabled={saving}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" loading={saving}>
              {primaryLabel}
            </Button>
          </div>
        )}
      </form>
    </Modal>
  );
}

function OverlapOption({
  id,
  name,
  checked,
  onSelect,
  title,
  description,
}: {
  id: string;
  name: string;
  checked: boolean;
  onSelect: () => void;
  title: string;
  description: string;
}) {
  return (
    <label
      htmlFor={id}
      className={cn(
        "flex cursor-pointer items-start gap-3 rounded-md border px-3 py-2.5 transition-colors",
        checked ? "border-primary/60 bg-card ring-1 ring-primary/30" : "border-border bg-background hover:border-muted-foreground/40",
      )}
    >
      <input
        id={id}
        type="radio"
        name={name}
        checked={checked}
        onChange={onSelect}
        aria-labelledby={`${id}-title`}
        aria-describedby={`${id}-description`}
        className="mt-0.5 h-4 w-4 accent-[hsl(var(--primary))]"
      />
      <span className="min-w-0">
        <span id={`${id}-title`} className="block text-sm font-medium text-foreground">
          {title}
        </span>
        <span id={`${id}-description`} className="block text-xs text-muted-foreground">
          {description}
        </span>
      </span>
    </label>
  );
}
