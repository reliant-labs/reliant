import { useId } from "react";
import { cn } from "../../lib/utils";
import { useProjectStore } from "../../store/projectStore";
import {
  useBatchUpsertSettings,
  useDeleteSetting,
  useListSettings,
} from "../../hooks/settings-service-hooks_gen";
import { SettingsPageHeader } from "./SettingsPageHeader";

/**
 * Settings → Automations: when an agent's change to a trigger activation needs
 * your approval card. The server reads `automation.approvals` (account-wide,
 * or a project's own row, which wins there) and decides each change with it:
 * internal/automationpolicy/approvals.go. Pausing never needs a card.
 */
export const AUTOMATION_APPROVALS_KEY = "automation.approvals";

export type AutomationApprovalMode = "always" | "on_capability_change" | "never";

export const DEFAULT_AUTOMATION_APPROVAL_MODE: AutomationApprovalMode = "on_capability_change";

export const AUTOMATION_APPROVAL_OPTIONS: ReadonlyArray<{
  value: AutomationApprovalMode;
  label: string;
  description: string;
}> = [
  {
    value: "on_capability_change",
    label: "When a change widens its power (default)",
    description:
      "Edits, resumes and definition changes apply right away and are recorded in the activation's history. A card is still required for a new activation, a new or different connection or scope, a change of who it acts as, a new machine, new secrets, or a production deploy step.",
  },
  {
    value: "never",
    label: "Never",
    description:
      "Every change an agent makes applies without a card, from interactive and unattended runs alike, and is recorded in the activation's history with what it added and removed.",
  },
  {
    value: "always",
    label: "Always",
    description: "Every create, edit and resume needs your card. Unattended runs can only pause.",
  },
];

export function parseAutomationApprovalMode(value: string | undefined): AutomationApprovalMode | undefined {
  return AUTOMATION_APPROVAL_OPTIONS.find((option) => option.value === value)?.value;
}

function ModeChoices({
  value,
  onChange,
  disabled,
  inherit,
}: {
  value: AutomationApprovalMode | "inherit";
  onChange: (value: AutomationApprovalMode | "inherit") => void;
  disabled?: boolean;
  /** When set, offers "use the account setting", naming what it currently is. */
  inherit?: string;
}) {
  const name = useId();
  const choices: Array<{ value: AutomationApprovalMode | "inherit"; label: string; description: string }> = [
    ...(inherit ? [{ value: "inherit" as const, label: "Use the account setting", description: inherit }] : []),
    ...AUTOMATION_APPROVAL_OPTIONS,
  ];
  return (
    <div role="radiogroup" className="divide-y divide-border rounded-lg border border-border bg-card">
      {choices.map((choice) => (
        <label
          key={choice.value}
          className={cn(
            "flex cursor-pointer items-start gap-3 px-4 py-3",
            disabled && "cursor-not-allowed opacity-60",
          )}
        >
          <input
            type="radio"
            name={name}
            className="mt-1"
            checked={value === choice.value}
            disabled={disabled}
            onChange={() => onChange(choice.value)}
          />
          <span className="space-y-1">
            <span className="block text-sm font-medium">{choice.label}</span>
            <span className="block text-xs text-muted-foreground">{choice.description}</span>
          </span>
        </label>
      ))}
    </div>
  );
}

function labelOf(mode: AutomationApprovalMode): string {
  return AUTOMATION_APPROVAL_OPTIONS.find((option) => option.value === mode)?.label ?? mode;
}

export function AutomationSettings() {
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = currentProject?.id;
  const account = useListSettings({});
  const project = useListSettings({ projectId }, { enabled: !!projectId });
  const upsert = useBatchUpsertSettings();
  const remove = useDeleteSetting();

  const find = (settings: Array<{ key: string; value: string }> | undefined) =>
    settings?.find((setting) => setting.key === AUTOMATION_APPROVALS_KEY)?.value;
  const accountMode = parseAutomationApprovalMode(find(account.data?.settings)) ?? DEFAULT_AUTOMATION_APPROVAL_MODE;
  const projectValue = projectId ? find(project.data?.settings) : undefined;
  const projectMode = parseAutomationApprovalMode(projectValue);
  const busy = upsert.isPending || remove.isPending;

  const save = (mode: AutomationApprovalMode, scope?: string) =>
    upsert.mutate({ settings: [{ key: AUTOMATION_APPROVALS_KEY, value: mode }], projectId: scope });

  return (
    <div className="space-y-8">
      <SettingsPageHeader
        title="Automations"
        description="When an agent creates, edits or resumes one of your trigger activations, this decides whether it needs your approval card. Pausing never does. Every change is recorded in the activation's history either way."
      />

      <section className="space-y-3">
        <div>
          <h3 className="text-sm font-semibold">Automation approvals</h3>
          <p className="text-xs text-muted-foreground">For every project, unless the project sets its own below.</p>
        </div>
        <ModeChoices value={accountMode} disabled={busy || account.isLoading} onChange={(mode) => mode !== "inherit" && save(mode)} />
      </section>

      {projectId && (
        <section className="space-y-3">
          <div>
            <h3 className="text-sm font-semibold">This project: {currentProject?.name ?? projectId}</h3>
            <p className="text-xs text-muted-foreground">Overrides the account setting for activations in this project.</p>
          </div>
          <ModeChoices
            value={projectMode ?? "inherit"}
            disabled={busy || project.isLoading}
            inherit={`Currently: ${labelOf(accountMode)}`}
            onChange={(mode) =>
              mode === "inherit"
                ? remove.mutate({ key: AUTOMATION_APPROVALS_KEY, projectId })
                : save(mode, projectId)
            }
          />
        </section>
      )}

      {(upsert.error || remove.error) && (
        <p role="alert" className="text-sm text-destructive">
          The setting could not be saved. Try again.
        </p>
      )}
    </div>
  );
}
