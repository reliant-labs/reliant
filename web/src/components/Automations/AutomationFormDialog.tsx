// Copyright (c) 2025 Reliant Labs

/**
 * Create or edit an automation.
 *
 * One dialog for both, because the server shares one definition shape between
 * CreateTrigger and UpdateTrigger. Every control is a native element with a
 * real <label>, so the form is fully keyboard- and screen-reader-operable
 * without any custom focus management beyond the Modal's.
 *
 * The workflow's inputs, presets and workspace are RunWorkflowForm — the same
 * store-free form the Run… dialog uses — so an automation edits exactly what a
 * manual run would send. Inputs belong to the workflow that declared them, so
 * switching workflow clears them, after a confirmation when any are set.
 *
 * UpdateTrigger is a full replacement on the server, so every field the
 * trigger carries is in this form's state and goes back on save.
 */

import { useEffect, useId, useMemo, useRef, useState, type FormEvent } from "react";
import { ChevronRight } from "lucide-react";

import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { useProjectStore } from "@/store/projectStore";
import { getWorkflowDisplayName, normalizeWorkflowRef } from "../workflow/useWorkflowInputs";
import { RunWorkflowForm, type RunWorkflowFormStatus } from "../workflow/run/RunWorkflowForm";
import { countRunInputs, type RunWorkflowValue } from "../workflow/run/runWorkflowValues";
import {
  sourceKindLabel,
  triggerErrorMessage,
  triggerSchedule,
  type OverlapPolicy,
  type Trigger,
  type TriggerDefinitionInput,
  type TriggerSchedule,
  type TriggerSource,
} from "@/api/trigger-grpc";
import {
  useCreateTrigger,
  useProjectDaemonInstalls,
  useProjectWorkflowList,
  useUpdateTrigger,
} from "@/hooks/trigger-queries";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { describeSchedule } from "@/lib/cronText";
import { cn } from "@/lib/utils";
import {
  DEFAULT_SCHEDULE_FORM,
  PRESET_OPTIONS,
  WEEKDAY_OPTIONS,
  formFromSchedule,
  scheduleFromForm,
  serverErrorField,
  validateScheduleForm,
  type IntervalUnit,
  type ScheduleFormState,
  type SchedulePreset,
} from "./scheduleForm";
import { errorTextClass, fieldClass, hintClass, labelClass, textareaClass } from "../workflow/run/runFormStyles";
import { buildDaemonChoices, defaultDaemonId } from "./daemonChoices";
import { validateTimezone } from "./timezone";

/**
 * Starting values for a NEW automation — a starter template, or "Save as
 * automation" from a run. Ignored when editing, where the trigger is the
 * source of truth.
 */
export interface AutomationPrefill {
  name?: string;
  projectId?: string;
  workflow?: string;
  message?: string;
  /** Any subset; unset fields take the form's defaults. */
  schedule?: Partial<TriggerSchedule>;
  presets?: Record<string, string>;
  /** Nested, as the wire carries them. */
  params?: Record<string, unknown>;
  worktreeId?: string;
}

export interface AutomationFormDialogProps {
  open: boolean;
  onClose: () => void;
  /** The trigger being edited; absent to create a new one. */
  trigger?: Trigger;
  /** Starting values for a new automation. */
  prefill?: AutomationPrefill;
  /** Project a new automation starts in. Defaults to the current project. */
  defaultProjectId?: string;
  onSaved?: (trigger: Trigger) => void;
}

/** A Go duration ("90s", "10m", "1h30m"); the server is the final judge. */
const GO_DURATION = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/;

/**
 * A source arm this build's generated code predates arrives with nothing to
 * send back, and UpdateTrigger is a full replacement — so it cannot be saved.
 */
const UNKNOWN_SOURCE_MESSAGE =
  "This automation's trigger was set up in a newer version of Reliant. Reload the app to edit it.";

function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function timezoneOptions(): string[] {
  try {
    const zones = Intl.supportedValuesOf("timeZone");
    return zones.includes("UTC") ? zones : ["UTC", ...zones];
  } catch {
    return ["UTC"];
  }
}

interface FieldErrors {
  name?: string;
  project?: string;
  daemon?: string;
  message?: string;
  inputs?: string;
  schedule?: string;
  timezone?: string;
  catchup?: string;
  form?: string;
}

export function AutomationFormDialog(props: AutomationFormDialogProps) {
  // Remount the body per open so a reopened dialog starts from the trigger's
  // stored state, never from a half-edited previous session.
  if (!props.open) return null;
  return <AutomationFormBody {...props} />;
}

function AutomationFormBody({
  onClose,
  trigger,
  prefill: prefillProp,
  defaultProjectId,
  onSaved,
}: AutomationFormDialogProps) {
  const isEdit = !!trigger;
  const prefill = isEdit ? undefined : prefillProp;
  const ids = useId();
  const fieldId = (name: string) => `${ids}-${name}`;

  const projects = useProjectStore((state) => state.projects);
  const currentProjectId = useProjectStore((state) => state.currentProject?.id);
  const loadProjects = useProjectStore((state) => state.loadProjects);

  useEffect(() => {
    void loadProjects().catch(() => undefined);
  }, [loadProjects]);

  // An edit starts from the trigger; a new automation from the prefill.
  const initialSchedule = trigger ? triggerSchedule(trigger) : prefill?.schedule;
  // Only a schedule has an editor here. Any other kind of source is shown
  // read-only and sent back exactly as stored, so editing the name or prompt
  // can never rewrite what makes the trigger fire.
  const lockedSource = trigger && trigger.source.kind !== "schedule" ? trigger.source : undefined;

  const [name, setName] = useState(trigger?.name ?? prefill?.name ?? "");
  const [projectId, setProjectId] = useState(
    trigger?.projectId ?? prefill?.projectId ?? defaultProjectId ?? currentProjectId ?? "",
  );
  const [workflow, setWorkflow] = useState(trigger?.workflow ?? prefill?.workflow ?? "");
  const [inputs, setInputs] = useState<RunWorkflowValue>(() => ({
    presets: { ...(trigger?.presets ?? prefill?.presets ?? {}) },
    params: { ...(trigger?.params ?? prefill?.params ?? {}) },
    worktreeId: trigger?.worktreeId ?? prefill?.worktreeId,
  }));
  const [inputsStatus, setInputsStatus] = useState<RunWorkflowFormStatus | null>(null);
  // A workflow switch waiting on "clear the inputs?" confirmation.
  const [pendingWorkflow, setPendingWorkflow] = useState<string | null>(null);
  const [daemonId, setDaemonId] = useState(trigger?.daemonId ?? "");
  // Once the user (or an edit's stored value) has chosen a daemon, the form
  // stops defaulting it — a project switch must not silently move the run.
  const daemonChosen = useRef(!!trigger?.daemonId);
  const [message, setMessage] = useState(trigger?.message ?? prefill?.message ?? "");
  const [schedule, setSchedule] = useState<ScheduleFormState>(() =>
    initialSchedule?.cron || initialSchedule?.interval
      ? formFromSchedule({ cron: initialSchedule.cron ?? [], interval: initialSchedule.interval })
      : DEFAULT_SCHEDULE_FORM,
  );
  const [timezone, setTimezone] = useState(initialSchedule?.timezone ?? browserTimezone());
  const [overlap, setOverlap] = useState<OverlapPolicy>(initialSchedule?.overlap ?? "skip");
  const [catchupWindow, setCatchupWindow] = useState(initialSchedule?.catchupWindow ?? "");
  const [notifyOnComplete, setNotifyOnComplete] = useState(trigger?.notifyOnComplete ?? false);
  // Advanced settings open on their own when they hold something non-default,
  // so an edit never hides a value it is about to save.
  const [advancedOpen, setAdvancedOpen] = useState(
    !!initialSchedule?.catchupWindow || initialSchedule?.overlap === "allow" || !!trigger?.notifyOnComplete,
  );
  const [errors, setErrors] = useState<FieldErrors>({});
  const [attempted, setAttempted] = useState(false);
  // "Discard your changes?" — shown when the dialog is dismissed while dirty.
  const [confirmDiscard, setConfirmDiscard] = useState(false);

  // What the dialog opened with, to tell whether the user has changed anything.
  // The daemon is excluded: the form defaults it on its own once the daemon
  // list loads, which is not a change the user made (an explicit choice is
  // tracked by daemonChosen instead).
  const initialFields = useRef({
    name,
    projectId,
    workflow,
    message,
    schedule: JSON.stringify(schedule),
    timezone,
    overlap,
    catchupWindow,
    notifyOnComplete,
  });
  // Workflow inputs change on their own too (RunWorkflowForm applies a
  // workflow's default presets asynchronously). Until the user interacts with
  // the inputs region, every change is absorbed into the baseline.
  const inputsTouched = useRef(false);
  const inputsBaseline = useRef(JSON.stringify(inputs));
  if (!inputsTouched.current) inputsBaseline.current = JSON.stringify(inputs);

  // The project list loads asynchronously; settle on a project once it does.
  // That is the form's default, not an edit, so it moves the baseline too.
  useEffect(() => {
    if (!projectId && projects.length > 0) {
      setProjectId(projects[0]!.id);
      initialFields.current.projectId = projects[0]!.id;
    }
  }, [projectId, projects]);

  /** Inputs belong to the workflow (and a workspace to the project): reset both. */
  const applyWorkflow = (next: string) => {
    setWorkflow(next);
    setInputs((prev) => ({ presets: {}, params: {}, worktreeId: prev.worktreeId }));
    setPendingWorkflow(null);
  };

  const requestWorkflowChange = (next: string) => {
    if (normalizeWorkflowRef(next) === normalizeWorkflowRef(workflow)) return;
    if (countRunInputs(inputs) > 0) {
      setPendingWorkflow(next);
      return;
    }
    applyWorkflow(next);
  };

  const changeProject = (next: string) => {
    setProjectId(next);
    // A worktree belongs to one project; moving the automation drops it.
    setInputs((prev) => ({ ...prev, worktreeId: undefined }));
  };

  const workflowsQuery = useProjectWorkflowList(projectId || undefined);
  const workflowOptions = useMemo(() => {
    const seen = new Set<string>();
    const listed = workflowsQuery.data ?? [];
    const options = listed
      // A draft is never runnable, so it is never offered where a runnable
      // workflow is required (WorkflowDraftStatus in workflow.proto): an
      // automation pinned to one would fail every firing.
      .filter((w) => w.status !== "draft")
      .filter((w) => {
        const key = normalizeWorkflowRef(w.name).toLowerCase();
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
      })
      .map((w) => ({ value: w.name, label: getWorkflowDisplayName(w.name, true) }))
      .sort((a, b) => a.label.localeCompare(b.label));
    // Keep a stored workflow selectable even if this project no longer lists
    // it, or it has gone back to draft — saying so, rather than blanking it.
    if (workflow && !options.some((o) => normalizeWorkflowRef(o.value) === normalizeWorkflowRef(workflow))) {
      const isDraft = listed.some(
        (w) => w.status === "draft" && normalizeWorkflowRef(w.name) === normalizeWorkflowRef(workflow),
      );
      const label = getWorkflowDisplayName(workflow, true);
      options.unshift({ value: workflow, label: isDraft ? `${label} (draft, cannot run)` : label });
    }
    return options;
  }, [workflowsQuery.data, workflow]);

  const { daemons, loading: daemonsLoading } = useDaemonStatus();
  const installsQuery = useProjectDaemonInstalls();
  const daemonChoices = useMemo(
    () => buildDaemonChoices(daemons, installsQuery.data ?? [], projectId),
    [daemons, installsQuery.data, projectId],
  );
  const daemonDataReady = !daemonsLoading && !installsQuery.isLoading;

  // Preselect the single obvious daemon for the project until a daemon has been
  // chosen (by the user, or by an edit's stored value, which is always kept —
  // the server is the judge of whether it is still valid, and says so inline).
  useEffect(() => {
    if (!daemonDataReady || daemonChosen.current) return;
    const next = defaultDaemonId(daemonChoices) ?? "";
    if (next !== daemonId) setDaemonId(next);
  }, [daemonDataReady, daemonChoices, daemonId]);

  // An edit's stored daemon stays selectable even if the registry no longer
  // lists it (deleted, or not yet re-registered) — the user sees what it is set
  // to and can change it, rather than the field silently going blank.
  const daemonOptions = useMemo(() => {
    if (!daemonId || daemonChoices.some((c) => c.daemonId === daemonId)) return daemonChoices;
    return [
      {
        daemonId,
        label: `daemon ${daemonId.slice(0, 8)}`,
        statusLabel: "not found",
        installed: false,
        eligible: true,
      },
      ...daemonChoices,
    ];
  }, [daemonChoices, daemonId]);
  const noDaemons = daemonDataReady && daemons.length === 0 && !daemonId;
  const noEligibleDaemons = daemonDataReady && daemons.length > 0 && !daemonChoices.some((c) => c.eligible);

  const zones = useMemo(timezoneOptions, []);
  const createMutation = useCreateTrigger();
  const updateMutation = useUpdateTrigger();
  const saving = createMutation.isPending || updateMutation.isPending;

  const scheduleError = validateScheduleForm(schedule);
  const timezoneError = validateTimezone(timezone);
  const preview =
    scheduleError || timezoneError ? null : describeSchedule({ ...scheduleFromForm(schedule), timezone });

  const updateSchedule = (patch: Partial<ScheduleFormState>) =>
    setSchedule((prev) => ({ ...prev, ...patch }));

  const buildSource = (): TriggerSource => {
    if (lockedSource) return lockedSource;
    const wire = scheduleFromForm(schedule);
    return {
      kind: "schedule",
      schedule: {
        cron: wire.cron,
        interval: wire.interval,
        timezone: timezone.trim() || "UTC",
        overlap,
        catchupWindow: catchupWindow.trim() || undefined,
      },
    };
  };

  const buildDefinition = (): TriggerDefinitionInput => ({
    name: name.trim(),
    projectId,
    worktreeId: inputs.worktreeId,
    workflow,
    presets: inputs.presets,
    params: inputs.params,
    message: message.trim(),
    daemonId,
    notifyOnComplete,
    source: buildSource(),
  });

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setAttempted(true);
    const next: FieldErrors = {};
    if (!name.trim()) next.name = "Give the automation a name.";
    if (!projectId) next.project = "Choose a project.";
    if (!daemonId) {
      next.daemon = noDaemons
        ? "Connect a daemon first — automations run on one of your daemons."
        : "Choose the daemon this automation runs on.";
    }
    if (!message.trim()) next.message = "Write the prompt each run starts from.";
    if (pendingWorkflow !== null) {
      next.inputs = "Confirm or cancel the workflow change first.";
    } else if (inputsStatus?.loading && workflow) {
      next.inputs = "Wait for this workflow's inputs to load.";
    } else if (inputsStatus && inputsStatus.missingRequired.length > 0) {
      next.inputs = `Fill in the required inputs: ${inputsStatus.missingRequired.join(", ")}.`;
    }
    if (lockedSource?.kind === "unknown") next.form = UNKNOWN_SOURCE_MESSAGE;
    if (scheduleError && !lockedSource) next.schedule = scheduleError;
    if (timezoneError && !lockedSource) next.timezone = timezoneError;
    const catchup = catchupWindow.trim();
    if (catchup && !lockedSource && !GO_DURATION.test(catchup)) {
      next.catchup = "Use a duration like 10m, 2h or 1h30m.";
      setAdvancedOpen(true);
    }
    setErrors(next);
    if (Object.keys(next).length > 0) {
      const first = (["name", "project", "daemon", "message", "inputs", "schedule", "timezone", "catchup"] as const).find(
        (k) => next[k],
      );
      if (first) document.getElementById(fieldId(first))?.focus();
      return;
    }

    try {
      const definition = buildDefinition();
      const saved = trigger
        ? await updateMutation.mutateAsync({ id: trigger.id, input: definition })
        : await createMutation.mutateAsync(definition);
      onSaved?.(saved);
      onClose();
    } catch (error) {
      // Server validation (bad cron, unknown zone, interval too short) comes
      // back as InvalidArgument with a precise message; put it next to the
      // field it is about.
      const text = triggerErrorMessage(error);
      const field = serverErrorField(text);
      if (field === "catchup") setAdvancedOpen(true);
      setErrors({ [field]: text });
    }
  };

  const describedBy = (...parts: Array<string | false | undefined>) =>
    parts.filter(Boolean).join(" ") || undefined;

  const isDirty = () => {
    const initial = initialFields.current;
    return (
      name !== initial.name ||
      projectId !== initial.projectId ||
      workflow !== initial.workflow ||
      message !== initial.message ||
      JSON.stringify(schedule) !== initial.schedule ||
      timezone !== initial.timezone ||
      overlap !== initial.overlap ||
      catchupWindow !== initial.catchupWindow ||
      notifyOnComplete !== initial.notifyOnComplete ||
      (daemonChosen.current && daemonId !== (trigger?.daemonId ?? "")) ||
      (inputsTouched.current && JSON.stringify(inputs) !== inputsBaseline.current)
    );
  };

  // Escape, the backdrop and the close button all come here. Unsaved input —
  // above all a long prompt — is never dropped without asking.
  const requestClose = () => {
    if (saving) return;
    if (confirmDiscard) {
      setConfirmDiscard(false);
      return;
    }
    if (isDirty()) {
      setConfirmDiscard(true);
      return;
    }
    onClose();
  };

  return (
    <Modal
      isOpen
      onClose={requestClose}
      title={isEdit ? "Edit automation" : "New automation"}
      size="lg"
    >
      <form onSubmit={onSubmit} noValidate className="space-y-6" aria-label={isEdit ? "Edit automation" : "New automation"}>
        {errors.form && (
          <div
            role="alert"
            className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive-ink"
          >
            {errors.form}
          </div>
        )}

        <section className="space-y-4" aria-labelledby={fieldId("what-heading")}>
          <h3 id={fieldId("what-heading")} className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            What it runs
          </h3>

          <div>
            <label htmlFor={fieldId("name")} className={labelClass}>
              Name
            </label>
            <input
              id={fieldId("name")}
              className={fieldClass}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Morning triage"
              autoComplete="off"
              aria-invalid={!!errors.name}
              aria-describedby={describedBy(errors.name && fieldId("name-error"))}
            />
            {errors.name && (
              <p id={fieldId("name-error")} className={errorTextClass}>
                {errors.name}
              </p>
            )}
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <label htmlFor={fieldId("project")} className={labelClass}>
                Project
              </label>
              <select
                id={fieldId("project")}
                className={fieldClass}
                value={projectId}
                onChange={(e) => changeProject(e.target.value)}
                aria-invalid={!!errors.project}
                aria-describedby={describedBy(errors.project && fieldId("project-error"))}
              >
                {projects.length === 0 && <option value="">Loading projects…</option>}
                {projects.map((project) => (
                  <option key={project.id} value={project.id}>
                    {project.name}
                  </option>
                ))}
              </select>
              {errors.project && (
                <p id={fieldId("project-error")} className={errorTextClass}>
                  {errors.project}
                </p>
              )}
            </div>

            <div>
              <label htmlFor={fieldId("workflow")} className={labelClass}>
                Workflow
              </label>
              <select
                id={fieldId("workflow")}
                className={fieldClass}
                value={pendingWorkflow ?? workflow}
                onChange={(e) => requestWorkflowChange(e.target.value)}
                aria-describedby={fieldId("workflow-hint")}
              >
                <option value="">Your default workflow</option>
                {workflowOptions.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
              <p id={fieldId("workflow-hint")} className={hintClass}>
                {workflowsQuery.isLoading
                  ? "Loading this project's workflows…"
                  : "The default is resolved each time the automation runs."}
              </p>
            </div>
          </div>

          <div>
            <label htmlFor={fieldId("daemon")} className={labelClass}>
              Runs on
            </label>
            {noDaemons ? (
              <div
                id={fieldId("daemon")}
                tabIndex={-1}
                className="rounded-md border border-border/60 bg-background px-3 py-2 text-sm text-muted-foreground"
                aria-describedby={describedBy(errors.daemon && fieldId("daemon-error"))}
              >
                You have no daemon yet. An automation's runs execute on one of your daemons — connect
                one (a cloud machine or your own) from the project picker, then come back.
              </div>
            ) : (
              <select
                id={fieldId("daemon")}
                className={fieldClass}
                value={daemonId}
                onChange={(e) => {
                  daemonChosen.current = true;
                  setDaemonId(e.target.value);
                }}
                aria-invalid={!!errors.daemon}
                aria-describedby={describedBy(fieldId("daemon-hint"), errors.daemon && fieldId("daemon-error"))}
              >
                <option value="" disabled>
                  {daemonDataReady ? "Choose a daemon" : "Loading daemons…"}
                </option>
                {daemonOptions.map((choice) => (
                  <option key={choice.daemonId} value={choice.daemonId} disabled={!choice.eligible}>
                    {choice.label} ({choice.statusLabel}
                    {choice.installed ? ", project installed" : ""}
                    {choice.ineligibleReason ? `, ${choice.ineligibleReason}` : ""})
                  </option>
                ))}
              </select>
            )}
            {!noDaemons && (
              <p id={fieldId("daemon-hint")} className={hintClass}>
                {noEligibleDaemons
                  ? "None of your daemons has this project installed. Install it on one from the project picker."
                  : "Every run's tools execute here. A daemon that is offline when the automation fires is woken if it can be."}
              </p>
            )}
            {errors.daemon && (
              <p id={fieldId("daemon-error")} className={errorTextClass}>
                {errors.daemon}
              </p>
            )}
          </div>

          <div>
            <label htmlFor={fieldId("message")} className={labelClass}>
              Prompt
            </label>
            <textarea
              id={fieldId("message")}
              className={textareaClass}
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              rows={4}
              placeholder="Review yesterday's failed CI runs and open an issue for each new failure."
              aria-invalid={!!errors.message}
              aria-describedby={describedBy(fieldId("message-hint"), errors.message && fieldId("message-error"))}
            />
            <p id={fieldId("message-hint")} className={hintClass}>
              Each run starts from this message. Nobody will be watching, so say everything the agent needs.
            </p>
            {errors.message && (
              <p id={fieldId("message-error")} className={errorTextClass}>
                {errors.message}
              </p>
            )}
          </div>

          {pendingWorkflow !== null && (
            <div
              role="alertdialog"
              aria-labelledby={fieldId("switch-title")}
              aria-describedby={fieldId("switch-description")}
              className="rounded-md border border-warning/40 bg-background px-3 py-3"
            >
              <p id={fieldId("switch-title")} className="text-sm font-medium text-foreground">
                Switch to {pendingWorkflow ? getWorkflowDisplayName(pendingWorkflow, true) : "your default workflow"}?
              </p>
              <p id={fieldId("switch-description")} className="mt-1 text-xs text-muted-foreground">
                {(() => {
                  const count = countRunInputs(inputs);
                  return `This clears the ${count} input setting${count === 1 ? "" : "s"} you have made, because they belong to the current workflow.`;
                })()}
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

          {projectId && (
            <div
              id={fieldId("inputs")}
              tabIndex={-1}
              className="focus:outline-none"
              // Any user interaction here makes later input changes count as
              // edits; before it, changes are the form's own defaults.
              onChangeCapture={() => (inputsTouched.current = true)}
              onClickCapture={() => (inputsTouched.current = true)}
              onKeyDownCapture={() => (inputsTouched.current = true)}
            >
              <RunWorkflowForm
                projectId={projectId}
                workflowRef={workflow}
                value={inputs}
                onChange={setInputs}
                onStatusChange={setInputsStatus}
                showValidation={attempted}
                // A new automation starts from the workflow's default presets,
                // as a manual run would; an edit keeps exactly what was saved.
                applyDefaultPresets={!isEdit && !prefill?.presets && !prefill?.params}
              />
              {errors.inputs && !(attempted && inputsStatus?.missingRequired.length) && (
                <p className={errorTextClass} role="alert">
                  {errors.inputs}
                </p>
              )}
            </div>
          )}
        </section>

        <section className="space-y-4" aria-labelledby={fieldId("when-heading")}>
          <h3 id={fieldId("when-heading")} className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            When it runs
          </h3>

          {lockedSource ? (
            <p
              id={fieldId("locked-source")}
              className="rounded-md border border-border/60 bg-background px-3 py-2 text-sm text-foreground"
            >
              <span className="font-medium">{sourceKindLabel(lockedSource)} trigger.</span>{" "}
              <span className="text-muted-foreground">
                {lockedSource.kind === "unknown"
                  ? UNKNOWN_SOURCE_MESSAGE
                  : "Its trigger settings can't be changed here yet. Saving keeps them exactly as they are."}
              </span>
            </p>
          ) : (
            <>
              <ScheduleFields
                fieldId={fieldId}
                schedule={schedule}
                onChange={updateSchedule}
                error={errors.schedule}
              />

              <div>
                <label htmlFor={fieldId("timezone")} className={labelClass}>
                  Time zone
                </label>
                <input
                  id={fieldId("timezone")}
                  className={fieldClass}
                  value={timezone}
                  onChange={(e) => setTimezone(e.target.value)}
                  list={fieldId("timezones")}
                  autoComplete="off"
                  spellCheck={false}
                  aria-invalid={!!errors.timezone}
                  aria-describedby={describedBy(fieldId("timezone-hint"), errors.timezone && fieldId("timezone-error"))}
                />
                <datalist id={fieldId("timezones")}>
                  {zones.map((zone) => (
                    <option key={zone} value={zone} />
                  ))}
                </datalist>
                <p id={fieldId("timezone-hint")} className={hintClass}>
                  An IANA zone such as America/New_York. Times above are in this zone.
                </p>
                {errors.timezone && (
                  <p id={fieldId("timezone-error")} className={errorTextClass}>
                    {errors.timezone}
                  </p>
                )}
              </div>

              <p
                className="rounded-md border border-border/60 bg-background px-3 py-2 text-sm text-foreground"
                aria-live="polite"
              >
                <span className="text-muted-foreground">Runs: </span>
                {preview ??
                  (timezoneError && !scheduleError
                    ? "Fix the time zone to see when this runs."
                    : "Finish the schedule to see when this runs.")}
              </p>
            </>
          )}
        </section>

        <section aria-labelledby={fieldId("advanced-heading")}>
          <h3 id={fieldId("advanced-heading")} className="m-0">
            <button
              type="button"
              className="flex items-center gap-1.5 rounded-sm text-xs font-semibold uppercase tracking-wide text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
              aria-expanded={advancedOpen}
              aria-controls={fieldId("advanced-body")}
              onClick={() => setAdvancedOpen((open) => !open)}
            >
              <ChevronRight
                aria-hidden="true"
                className={cn("h-3.5 w-3.5 transition-transform motion-reduce:transition-none", advancedOpen && "rotate-90")}
              />
              Advanced
            </button>
          </h3>
          {/* Kept mounted while closed so a value set here is never lost. */}
          <div id={fieldId("advanced-body")} hidden={!advancedOpen} className="mt-4 space-y-4">
            {/* Overlap and catch-up live on the schedule source; a locked source keeps its own. */}
            {!lockedSource && (
              <fieldset>
                <legend className={labelClass}>If the previous run is still going</legend>
                <div className="space-y-2">
                  <OverlapOption
                    id={fieldId("overlap-skip")}
                    name={fieldId("overlap")}
                    checked={overlap === "skip"}
                    onSelect={() => setOverlap("skip")}
                    title="Skip this run"
                    description="Recommended. Nothing starts while the last run is active or paused; the skip is recorded in the history."
                  />
                  <OverlapOption
                    id={fieldId("overlap-allow")}
                    name={fieldId("overlap")}
                    checked={overlap === "allow"}
                    onSelect={() => setOverlap("allow")}
                    title="Start another run anyway"
                    description="Runs can pile up side by side if each one takes longer than the gap between them."
                  />
                </div>
              </fieldset>
            )}

            <div>
              <label className="flex cursor-pointer items-start gap-2 text-sm text-foreground">
                <input
                  id={fieldId("notify-on-complete")}
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 rounded border-border"
                  checked={notifyOnComplete}
                  onChange={(e) => setNotifyOnComplete(e.target.checked)}
                  aria-describedby={fieldId("notify-on-complete-hint")}
                />
                <span>Notify me when it finishes</span>
              </label>
              <p id={fieldId("notify-on-complete-hint")} className={hintClass}>
                Off by default: finished runs are silent and only land in Runs. Failures always notify, once per
                streak.
              </p>
            </div>

            {!lockedSource && (
              <div>
                <label htmlFor={fieldId("catchup")} className={labelClass}>
                  Catch-up window
                </label>
                <input
                  id={fieldId("catchup")}
                  className={cn(fieldClass, "font-mono sm:w-48")}
                  value={catchupWindow}
                  onChange={(e) => setCatchupWindow(e.target.value)}
                  placeholder="10m"
                  spellCheck={false}
                  autoComplete="off"
                  aria-invalid={!!errors.catchup}
                  aria-describedby={describedBy(fieldId("catchup-hint"), errors.catchup && fieldId("catchup-error"))}
                />
                <p id={fieldId("catchup-hint")} className={hintClass}>
                  How late a run missed during an outage may still start, such as 30m or 2h. Empty means 10 minutes.
                </p>
                {errors.catchup && (
                  <p id={fieldId("catchup-error")} className={errorTextClass}>
                    {errors.catchup}
                  </p>
                )}
              </div>
            )}
          </div>
        </section>

        {confirmDiscard ? (
          <div
            role="alertdialog"
            aria-labelledby={fieldId("discard-title")}
            aria-describedby={fieldId("discard-description")}
            className="flex flex-wrap items-center justify-between gap-3 border-t border-border/60 pt-4"
          >
            <div className="min-w-0">
              <p id={fieldId("discard-title")} className="text-sm font-medium text-foreground">
                Discard your changes?
              </p>
              <p id={fieldId("discard-description")} className="text-xs text-muted-foreground">
                {isEdit ? "Your edits to this automation" : "This new automation"} will be lost.
              </p>
            </div>
            <div className="flex gap-2">
              <Button
                type="button"
                variant="ghost"
                autoFocus
                onClick={() => setConfirmDiscard(false)}
              >
                Keep editing
              </Button>
              <Button type="button" variant="destructive" onClick={onClose}>
                Discard
              </Button>
            </div>
          </div>
        ) : (
          <div className="flex justify-end gap-2 border-t border-border/60 pt-4">
            <Button type="button" variant="ghost" onClick={requestClose} disabled={saving}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" loading={saving}>
              {isEdit ? "Save changes" : "Create automation"}
            </Button>
          </div>
        )}
      </form>
    </Modal>
  );
}

interface ScheduleFieldsProps {
  fieldId: (name: string) => string;
  schedule: ScheduleFormState;
  onChange: (patch: Partial<ScheduleFormState>) => void;
  error?: string;
}

function ScheduleFields({ fieldId, schedule, onChange, error }: ScheduleFieldsProps) {
  const errorId = error ? fieldId("schedule-error") : undefined;
  const needsTime =
    schedule.preset === "daily" || schedule.preset === "weekdays" || schedule.preset === "weekly";

  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <div>
          <label htmlFor={fieldId("schedule")} className={labelClass}>
            Repeat
          </label>
          <select
            id={fieldId("schedule")}
            className={fieldClass}
            value={schedule.preset}
            onChange={(e) => onChange({ preset: e.target.value as SchedulePreset })}
            aria-invalid={!!error}
            aria-describedby={errorId}
          >
            {PRESET_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </div>

        {schedule.preset === "weekly" && (
          <div>
            <label htmlFor={fieldId("weekday")} className={labelClass}>
              Day
            </label>
            <select
              id={fieldId("weekday")}
              className={fieldClass}
              value={schedule.weekday}
              onChange={(e) => onChange({ weekday: Number(e.target.value) })}
            >
              {WEEKDAY_OPTIONS.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
          </div>
        )}

        {needsTime && (
          <div>
            <label htmlFor={fieldId("time")} className={labelClass}>
              At
            </label>
            <input
              id={fieldId("time")}
              type="time"
              className={fieldClass}
              value={schedule.time}
              onChange={(e) => onChange({ time: e.target.value })}
              aria-describedby={errorId}
            />
          </div>
        )}

        {schedule.preset === "hourly" && (
          <div>
            <label htmlFor={fieldId("minute")} className={labelClass}>
              Minutes past the hour
            </label>
            <input
              id={fieldId("minute")}
              type="number"
              min={0}
              max={59}
              className={fieldClass}
              value={Number.isNaN(schedule.minute) ? "" : schedule.minute}
              onChange={(e) => onChange({ minute: e.target.valueAsNumber })}
              aria-describedby={errorId}
            />
          </div>
        )}

        {schedule.preset === "interval" && (
          <div>
            <span id={fieldId("interval-label")} className={labelClass}>
              Every
            </span>
            <div className="flex gap-2" role="group" aria-labelledby={fieldId("interval-label")}>
              <input
                id={fieldId("interval-value")}
                type="number"
                min={1}
                className={cn(fieldClass, "w-24")}
                value={Number.isNaN(schedule.intervalValue) ? "" : schedule.intervalValue}
                onChange={(e) => onChange({ intervalValue: e.target.valueAsNumber })}
                aria-label="Interval amount"
                aria-describedby={errorId}
              />
              <select
                className={fieldClass}
                value={schedule.intervalUnit}
                onChange={(e) => onChange({ intervalUnit: e.target.value as IntervalUnit })}
                aria-label="Interval unit"
              >
                <option value="m">minutes</option>
                <option value="h">hours</option>
              </select>
            </div>
          </div>
        )}
      </div>

      {schedule.preset === "advanced" && (
        <div className="grid gap-4 sm:grid-cols-[2fr_1fr]">
          <div>
            <label htmlFor={fieldId("cron")} className={labelClass}>
              Cron expressions
            </label>
            <textarea
              id={fieldId("cron")}
              className={cn(textareaClass, "min-h-[72px] font-mono")}
              value={schedule.cronText}
              onChange={(e) => onChange({ cronText: e.target.value })}
              spellCheck={false}
              placeholder="0 9 * * 1-5"
              aria-invalid={!!error}
              aria-describedby={[fieldId("cron-hint"), errorId].filter(Boolean).join(" ")}
            />
            <p id={fieldId("cron-hint")} className={hintClass}>
              One 5-field expression per line: minute, hour, day of month, month, day of week. It runs whenever any line matches.
            </p>
          </div>
          <div>
            <label htmlFor={fieldId("advanced-interval")} className={labelClass}>
              Also every (optional)
            </label>
            <input
              id={fieldId("advanced-interval")}
              className={cn(fieldClass, "font-mono")}
              value={schedule.advancedInterval}
              onChange={(e) => onChange({ advancedInterval: e.target.value })}
              placeholder="30m"
              spellCheck={false}
              aria-describedby={fieldId("advanced-interval-hint")}
            />
            <p id={fieldId("advanced-interval-hint")} className={hintClass}>
              A duration like 30m or 2h.
            </p>
          </div>
        </div>
      )}

      {error && (
        <p id={errorId} className={errorTextClass} role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

interface OverlapOptionProps {
  id: string;
  name: string;
  checked: boolean;
  onSelect: () => void;
  title: string;
  description: string;
}

function OverlapOption({ id, name, checked, onSelect, title, description }: OverlapOptionProps) {
  return (
    <label
      htmlFor={id}
      className={cn(
        "flex cursor-pointer items-start gap-3 rounded-md border px-3 py-2.5 transition-colors",
        checked ? "border-primary/60 bg-primary/5" : "border-border hover:border-border/80",
      )}
    >
      <input
        id={id}
        type="radio"
        name={name}
        checked={checked}
        onChange={onSelect}
        // Name from the title alone; the explanation is the description, so a
        // screen reader announces "Skip this run, radio" and then the detail.
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
