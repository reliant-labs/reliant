// Copyright (c) 2025 Reliant Labs

/**
 * The builder header's actions for an editable workflow. Four stay visible
 * (Run, Inputs, Save, Publish); YAML, Tests, Duplicate and Unpublish live in
 * a "More" menu, a WAI-ARIA menu button that the keyboard reaches
 * (forge-ui DropdownMenu).
 *
 * Tour anchors: `data-tour="workflow-inputs"` and `data-testid="workflow-save"`
 * stay on visible buttons (the workflows journey targets both).
 * `workflow-unpublish` is the menu item's test id.
 */

import { CheckCircle2, Code, Copy, MoreHorizontal, PencilRuler, Play, Settings2, TestTube2 } from "lucide-react";

import DropdownMenu from "../forge-ui/dropdown_menu";
import Tooltip from "../forge-ui/tooltip";

export interface BuilderHeaderActionsProps {
  showRun: boolean;
  runOpen: boolean;
  onToggleRun: () => void;
  onOpenInputs: () => void;
  onOpenYaml: () => void;
  onOpenTests: () => void;
  onDuplicate: () => void;
  /** Present only while the workflow is published. */
  onUnpublish?: () => void;
  unpublishDisabled?: boolean;
  onSave: () => void;
  saveDisabled: boolean;
  saving: boolean;
  saveIsPrimary: boolean;
  /** Present only while the workflow is a draft. */
  publish?: { onPublish: () => void; reasons: string[]; hasModifications: boolean };
  classes: { button: string; secondary: string; primary: string };
}

export function BuilderHeaderActions({
  showRun,
  runOpen,
  onToggleRun,
  onOpenInputs,
  onOpenYaml,
  onOpenTests,
  onDuplicate,
  onUnpublish,
  unpublishDisabled,
  onSave,
  saveDisabled,
  saving,
  saveIsPrimary,
  publish,
  classes,
}: BuilderHeaderActionsProps) {
  return (
    <>
      {showRun && (
        <button onClick={onToggleRun} className={classes.button} aria-pressed={runOpen}>
          <Play className="w-4 h-4" />
          Run
        </button>
      )}
      <button onClick={onOpenInputs} className={classes.button} data-tour="workflow-inputs">
        <Settings2 className="w-4 h-4" />
        Inputs
      </button>
      <DropdownMenu
        align="right"
        label="More workflow actions"
        menuClassName="w-64"
        trigger={(props) => (
          <button type="button" {...props} className={classes.button} data-testid="workflow-more">
            <MoreHorizontal className="w-4 h-4" aria-hidden="true" />
            More
          </button>
        )}
        groups={[
          {
            items: [
              { label: "YAML", icon: <Code className="w-4 h-4" />, onClick: onOpenYaml },
              { label: "Tests", icon: <TestTube2 className="w-4 h-4" />, onClick: onOpenTests },
              { label: "Duplicate", icon: <Copy className="w-4 h-4" />, onClick: onDuplicate },
              ...(onUnpublish
                ? [
                    {
                      label: "Unpublish",
                      icon: <PencilRuler className="w-4 h-4" />,
                      hint: "Stops it running",
                      disabled: unpublishDisabled,
                      testId: "workflow-unpublish",
                      onClick: onUnpublish,
                    },
                  ]
                : []),
            ],
          },
        ]}
      />
      <button
        onClick={onSave}
        disabled={saveDisabled}
        className={saveIsPrimary ? classes.primary : classes.secondary}
        data-testid="workflow-save"
      >
        {saving ? "Saving…" : "Save"}
      </button>
      {publish && (
        <Tooltip
          content={
            publish.reasons.length > 0
              ? `Can't publish yet: ${publish.reasons.join(" ")}`
              : publish.hasModifications
                ? "Save your changes and make this workflow runnable"
                : "Make this workflow runnable: by chat, Run, and its triggers"
          }
        >
          {/* The wrapper keeps the tooltip working while the button is disabled. */}
          <span className="inline-flex">
            <button
              onClick={publish.onPublish}
              disabled={publish.reasons.length > 0}
              className={classes.primary}
              data-testid="workflow-publish"
            >
              <CheckCircle2 className="w-4 h-4" />
              Publish
            </button>
          </span>
        </Tooltip>
      )}
    </>
  );
}
