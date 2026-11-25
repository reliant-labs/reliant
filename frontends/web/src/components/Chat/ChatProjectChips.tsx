// Copyright (c) 2025 Reliant Labs

/**
 * A chat's projects as chips: its own project (the primary) first, which
 * cannot be removed here, then the projects attached beside it, each with a
 * ×, then "Add project", which opens the project selector.
 *
 * Driven by a ChatProjectsControl (hooks/useChatProjects): useChatProjects
 * for an existing chat (the composer), useDraftChatProjects for a new one
 * (the new-chat view, which shows the primary in its own project chip and
 * passes `showPrimary={false}`). Mobile renders the same.
 *
 * The selector lists the chat's org only, without the chat's projects. With
 * the chat's machine known, the projects on it come first; every other one is
 * "Set up on <machine>" (cloned there, then attached), and a plain folder on
 * another machine is listed disabled. A project the server says is not on the
 * machine becomes the notice under the chips, with the same set-up.
 */

import { useCallback, useMemo } from "react";
import { Folder, Loader2, Plus, X } from "lucide-react";

import Tooltip from "../forge-ui/tooltip";
import { canChoose, canSetUpElsewhere, projectPlacement } from "../../lib/projectPlacement";
import { cn } from "../../lib/utils";
import type { ChatProjectsControl, ChatProjectSetUp } from "../../hooks/useChatProjects";
import type { ProjectCatalogRow } from "../../services/projects/catalog";
import {
  ProjectSelector,
  ProjectSelectorPanel,
  type ProjectRowState,
  type ProjectSelection,
} from "../Projects/ProjectSelector";
import { ComposerChip } from "./AttachmentPreview";
import { COMPOSER_CONTROL } from "./composerControl";

export interface ChatProjectChipsProps {
  control: ChatProjectsControl;
  /** The chat's org: the selector lists its projects only. Omitted: the tab's. */
  orgId?: string | null;
  /** Show the primary's chip (default). Off where another control shows it. */
  showPrimary?: boolean;
  /** Show the add control (default, when the control is `supported`). */
  showAdd?: boolean;
  /** The add control's label ("Add project"). */
  addLabel?: string;
  /**
   * The add control's shape, always with a dashed outline: `chip` (default)
   * is a pill among the composer's project chips; `toolbar` is the composer
   * tools row's pill (COMPOSER_CONTROL, like the model and mode pills beside
   * it); `control` matches the new-chat view's project, machine and workspace
   * buttons.
   */
  addSize?: "chip" | "toolbar" | "control";
  disabled?: boolean;
  className?: string;
}

const ADD_BUTTON_BASE =
  "flex shrink-0 items-center border border-dashed border-border text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60";

const ADD_BUTTON = {
  chip: "h-8 gap-1 rounded-full px-2.5 text-xs touch:h-11",
  toolbar: cn("gap-1 rounded-full px-2.5 text-xs font-medium", COMPOSER_CONTROL),
  control: "gap-2 rounded-lg px-5 py-2.5 text-sm font-medium hover:border-border-strong",
} as const;

const ADD_ICON = { chip: "h-3.5 w-3.5", toolbar: "h-3.5 w-3.5", control: "h-4 w-4" } as const;

const NOTICE_BUTTON =
  "rounded-md px-1.5 py-0.5 text-meta font-medium text-primary-ink hover:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/**
 * The project selector's props for adding a project to the chat: the chat's
 * projects left out, the machine's first, the rest offered as a set-up.
 */
function usePickerProps(control: ChatProjectsControl) {
  const { projects, machine, installs } = control;
  const rowState = useCallback(
    (row: ProjectCatalogRow): ProjectRowState | undefined => {
      if (!machine || canChoose(projectPlacement(installs, row.key), machine.daemonId)) return undefined;
      if (!canSetUpElsewhere(row.project)) return { hint: "Folder elsewhere", disabled: true };
      return { hint: `Set up on ${machine.name}` };
    },
    [machine, installs],
  );
  const rowRank = useCallback(
    (row: ProjectCatalogRow) => (!machine || canChoose(projectPlacement(installs, row.key), machine.daemonId) ? 0 : 1),
    [machine, installs],
  );
  const excludeKeys = useMemo(() => projects.map((p) => p.projectId), [projects]);
  return {
    mode: "chat" as const,
    footer: false,
    excludeKeys,
    rowState,
    rowRank,
    placeholder: "Add a project to this chat…",
    onSelect: (selection: ProjectSelection) => {
      if (selection.kind === "project") control.add(selection.row.project);
    },
  };
}

/**
 * The selector inline, for a surface with no room for a popover (the
 * composer's ⊕ sheet): pick a project and it is added.
 */
export function ChatProjectAddPanel({
  control,
  orgId,
  onClose,
}: {
  control: ChatProjectsControl;
  orgId?: string | null;
  onClose: () => void;
}) {
  const picker = usePickerProps(control);
  return <ProjectSelectorPanel {...picker} orgId={orgId} onClose={onClose} />;
}

export function ChatProjectChips({
  control,
  orgId,
  showPrimary = true,
  showAdd = true,
  addLabel = "Add project",
  addSize = "chip",
  disabled = false,
  className,
}: ChatProjectChipsProps) {
  const { projects, supported, pending, setUp } = control;
  const picker = usePickerProps(control);

  const shown = showPrimary ? projects : projects.filter((p) => !p.primary);
  const canAdd = supported && showAdd;
  if (shown.length === 0 && !canAdd && !setUp) return null;

  return (
    <div className={cn("flex min-w-0 flex-col gap-1", className)} data-testid="chat-project-chips">
      <div className="flex min-w-0 flex-wrap items-center gap-1.5">
        {shown.map((project) => {
          const busy = pending.has(project.projectId);
          return (
            <div key={project.projectId} data-testid={`chat-project-chip-${project.projectId}`} data-primary={project.primary || undefined}>
              <ComposerChip
                icon={
                  busy ? (
                    <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" aria-hidden />
                  ) : (
                    <Folder className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
                  )
                }
                label={project.name || "Project"}
                onRemove={!project.primary && !disabled && !busy ? () => control.remove(project.projectId) : undefined}
                removeLabel={`Remove ${project.name || "the project"} from this chat`}
              />
            </div>
          );
        })}
        {canAdd && (
          <ProjectSelector
            {...picker}
            orgId={orgId}
            align="left"
            side="top"
            disabled={disabled}
            trigger={(props) => (
              <Tooltip content="Let this chat work in another project too" side="top" delay={300} triggerClassName="inline-flex">
                <button
                  type="button"
                  {...props}
                  onClick={(event) => {
                    // The composer focuses its text on a click anywhere in it.
                    event.stopPropagation();
                    props.onClick(event);
                  }}
                  disabled={disabled}
                  data-testid="chat-project-add"
                  className={cn(ADD_BUTTON_BASE, ADD_BUTTON[addSize])}
                >
                  <Plus className={ADD_ICON[addSize]} aria-hidden />
                  <span>{addLabel}</span>
                </button>
              </Tooltip>
            )}
          />
        )}
      </div>
      {setUp && (
        <SetUpNotice setUp={setUp} onConfirm={control.confirmSetUp} onDismiss={control.dismissSetUp} disabled={disabled} />
      )}
    </div>
  );
}

function SetUpNotice({
  setUp,
  onConfirm,
  onDismiss,
  disabled,
}: {
  setUp: ChatProjectSetUp;
  onConfirm: () => void;
  onDismiss: () => void;
  disabled: boolean;
}) {
  const { projectName: project, machine } = setUp;
  let text: string;
  let action: string | null = null;
  if (setUp.state === "setting_up") {
    text = `Setting up ${project} on ${machine.name}. It's added once it's there.`;
  } else if (setUp.state === "failed") {
    const why = setUp.error ? `: ${setUp.error.replace(/[.\s]+$/, "")}.` : ".";
    text = `Setting up ${project} on ${machine.name} failed${why}`;
    action = setUp.canClone ? "Try again" : null;
  } else if (setUp.canClone) {
    text = `${project} isn't set up on ${machine.name}, the machine this chat runs on.`;
    action = `Set up on ${machine.name}`;
  } else {
    text = `${project} is a folder on another machine, so it can't be added on ${machine.name}.`;
  }
  return (
    <div
      className="flex min-w-0 items-center gap-1.5 px-1 text-meta text-muted-foreground"
      role="status"
      data-testid="chat-project-setup"
      data-state={setUp.state}
      onClick={(event) => event.stopPropagation()}
    >
      {setUp.state === "setting_up" && <Loader2 className="h-3 w-3 shrink-0 animate-spin" aria-hidden />}
      <span className="min-w-0">{text}</span>
      {action && (
        <button type="button" className={NOTICE_BUTTON} onClick={onConfirm} disabled={disabled} data-testid="chat-project-setup-confirm">
          {action}
        </button>
      )}
      {setUp.state !== "setting_up" && (
        <button
          type="button"
          aria-label="Dismiss"
          onClick={onDismiss}
          className="ml-auto flex h-5 w-5 shrink-0 items-center justify-center rounded-md hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <X className="h-3 w-3" aria-hidden />
        </button>
      )}
    </div>
  );
}
