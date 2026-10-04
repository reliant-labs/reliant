// Copyright (c) 2025 Reliant Labs

/**
 * Create or edit an automation.
 *
 * One dialog for both, because the server shares one definition shape between
 * CreateTrigger and UpdateTrigger. Every control is a native element with a
 * real <label>, so the form is fully keyboard- and screen-reader-operable
 * without any custom focus management beyond the Modal's.
 *
 * WHAT IS NOT EDITABLE HERE. A trigger also carries preset assignments and
 * workflow input values. The chat composer's controls for those are bound to
 * the chat params store and the CURRENT project, not to a free-standing value,
 * so they are not reusable as-is. Rather than drop them, an edit carries them
 * through untouched (UpdateTrigger is a full replacement, so omitting them
 * would erase them) — unless the workflow changes, since inputs belong to the
 * workflow that declared them.
 */

import { useEffect, useId, useMemo, useState, type FormEvent } from "react";

import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { useProjectStore } from "@/store/projectStore";
import { getWorkflowDisplayName, normalizeWorkflowRef } from "../workflow/useWorkflowInputs";
import {
  definitionFromTrigger,
  triggerErrorMessage,
  type OverlapPolicy,
  type Trigger,
  type TriggerDefinitionInput,
} from "@/api/trigger-grpc";
import {
  useCreateTrigger,
  useProjectWorkflowList,
  useUpdateTrigger,
} from "@/hooks/trigger-queries";
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
import { errorTextClass, fieldClass, hintClass, labelClass, textareaClass } from "./automationFormStyles";

export interface AutomationFormDialogProps {
  open: boolean;
  onClose: () => void;
  /** The trigger being edited; absent to create a new one. */
  trigger?: Trigger;
  /** Project a new automation starts in. Defaults to the current project. */
  defaultProjectId?: string;
  onSaved?: (trigger: Trigger) => void;
}

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
  message?: string;
  schedule?: string;
  timezone?: string;
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
  defaultProjectId,
  onSaved,
}: AutomationFormDialogProps) {
  const isEdit = !!trigger;
  const ids = useId();
  const fieldId = (name: string) => `${ids}-${name}`;

  const projects = useProjectStore((state) => state.projects);
  const currentProjectId = useProjectStore((state) => state.currentProject?.id);
  const loadProjects = useProjectStore((state) => state.loadProjects);

  useEffect(() => {
    void loadProjects().catch(() => undefined);
  }, [loadProjects]);

  const [name, setName] = useState(trigger?.name ?? "");
  const [projectId, setProjectId] = useState(
    trigger?.projectId ?? defaultProjectId ?? currentProjectId ?? "",
  );
  const [workflow, setWorkflow] = useState(trigger?.workflow ?? "");
  const [message, setMessage] = useState(trigger?.message ?? "");
  const [schedule, setSchedule] = useState<ScheduleFormState>(() =>
    trigger?.schedule ? formFromSchedule(trigger.schedule) : DEFAULT_SCHEDULE_FORM,
  );
  const [timezone, setTimezone] = useState(trigger?.schedule?.timezone ?? browserTimezone());
  const [overlap, setOverlap] = useState<OverlapPolicy>(trigger?.schedule?.overlap ?? "skip");
  const [errors, setErrors] = useState<FieldErrors>({});

  // The project list loads asynchronously; settle on a project once it does.
  useEffect(() => {
    if (!projectId && projects.length > 0) setProjectId(projects[0]!.id);
  }, [projectId, projects]);

  const workflowsQuery = useProjectWorkflowList(projectId || undefined);
  const workflowOptions = useMemo(() => {
    const seen = new Set<string>();
    const options = (workflowsQuery.data ?? [])
      .filter((w) => {
        const key = normalizeWorkflowRef(w.name).toLowerCase();
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
      })
      .map((w) => ({ value: w.name, label: getWorkflowDisplayName(w.name, true) }))
      .sort((a, b) => a.label.localeCompare(b.label));
    // Keep a stored workflow selectable even if this project no longer lists it.
    if (workflow && !options.some((o) => normalizeWorkflowRef(o.value) === normalizeWorkflowRef(workflow))) {
      options.unshift({ value: workflow, label: getWorkflowDisplayName(workflow, true) });
    }
    return options;
  }, [workflowsQuery.data, workflow]);

  const zones = useMemo(timezoneOptions, []);
  const createMutation = useCreateTrigger();
  const updateMutation = useUpdateTrigger();
  const saving = createMutation.isPending || updateMutation.isPending;

  const scheduleError = validateScheduleForm(schedule);
  const preview = scheduleError
    ? null
    : describeSchedule({ ...scheduleFromForm(schedule), timezone });

  const carriedInputCount = trigger
    ? Object.keys(trigger.presets).length + Object.keys(trigger.params).length
    : 0;
  const workflowChanged =
    !!trigger && normalizeWorkflowRef(trigger.workflow) !== normalizeWorkflowRef(workflow);

  const updateSchedule = (patch: Partial<ScheduleFormState>) =>
    setSchedule((prev) => ({ ...prev, ...patch }));

  const buildDefinition = (): TriggerDefinitionInput => {
    const base = trigger ? definitionFromTrigger(trigger) : undefined;
    const wire = scheduleFromForm(schedule);
    const keepInputs = base && !workflowChanged;
    return {
      name: name.trim(),
      projectId,
      // A worktree belongs to one project; moving the automation drops it.
      worktreeId: base && base.projectId === projectId ? base.worktreeId : undefined,
      workflow,
      presets: keepInputs ? base.presets : {},
      params: keepInputs ? base.params : {},
      message: message.trim(),
      schedule: {
        cron: wire.cron,
        interval: wire.interval,
        timezone: timezone.trim() || "UTC",
        overlap,
        catchupWindow: base?.schedule.catchupWindow,
      },
    };
  };

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    const next: FieldErrors = {};
    if (!name.trim()) next.name = "Give the automation a name.";
    if (!projectId) next.project = "Choose a project.";
    if (!message.trim()) next.message = "Write the prompt each run starts from.";
    if (scheduleError) next.schedule = scheduleError;
    setErrors(next);
    if (Object.keys(next).length > 0) {
      const first = (["name", "project", "message", "schedule"] as const).find((k) => next[k]);
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
      setErrors({ [serverErrorField(text)]: text });
    }
  };

  const describedBy = (...parts: Array<string | false | undefined>) =>
    parts.filter(Boolean).join(" ") || undefined;

  return (
    <Modal
      isOpen
      onClose={saving ? () => undefined : onClose}
      title={isEdit ? "Edit automation" : "New automation"}
      size="lg"
    >
      <form onSubmit={onSubmit} noValidate className="space-y-6" aria-label={isEdit ? "Edit automation" : "New automation"}>
        {errors.form && (
          <div
            role="alert"
            className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive"
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
                onChange={(e) => setProjectId(e.target.value)}
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
                value={workflow}
                onChange={(e) => setWorkflow(e.target.value)}
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

          {carriedInputCount > 0 && (
            <p className="rounded-md border border-border/60 bg-background px-3 py-2 text-xs text-muted-foreground">
              {workflowChanged
                ? `This automation's ${carriedInputCount} workflow input setting${carriedInputCount === 1 ? "" : "s"} will be cleared, because they belong to the previous workflow.`
                : `This automation also sets ${carriedInputCount} workflow input${carriedInputCount === 1 ? "" : "s"}, which are kept as they are.`}
            </p>
          )}
        </section>

        <section className="space-y-4" aria-labelledby={fieldId("when-heading")}>
          <h3 id={fieldId("when-heading")} className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            When it runs
          </h3>

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
            {preview ?? "Finish the schedule to see when this runs."}
          </p>

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
        </section>

        <div className="flex justify-end gap-2 border-t border-border/60 pt-4">
          <Button type="button" variant="ghost" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" loading={saving}>
            {isEdit ? "Save changes" : "Create automation"}
          </Button>
        </div>
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
