/**
 * Renderer for the workflow-editing tools: create_workflow, edit_workflow and
 * write_workflow.
 *
 * Their arguments carry workflow YAML inside a JSON string, so the generic
 * renderer drew them as one escaped line full of "\n". They get the diff a
 * file edit gets instead (see workflowEditDiff), followed by the tool's
 * result: the workflow's status and every validation error and warning.
 */

import { memo } from 'react';
import type { ToolContentProps } from './types';
import { PreviewAwareFileMutation } from './FileToolRenderer';
import { formatErrorMessage } from '../../../lib/utils';
import { workflowEditDiff } from '../../../lib/toolFormatters';

/** Highlights the diff as YAML; the viewer reads only the extension. */
const WORKFLOW_DIFF_FILENAME = 'workflow.yaml';

function WorkflowEditToolRendererComponent({ ctx }: ToolContentProps) {
  const { toolName, input, result } = ctx;
  const diff = workflowEditDiff(toolName, input);
  const outcome = result?.content?.trim();

  if (!diff && !outcome) return null;

  return (
    <div className="tool-content-file">
      {diff && (
        <PreviewAwareFileMutation
          filePath={WORKFLOW_DIFF_FILENAME}
          originalContent={diff.original}
          modifiedContent={diff.modified}
          disablePreview
        />
      )}
      {outcome &&
        (result?.is_error ? (
          <div className="px-2 py-1.5 text-xs text-warning-ink bg-warning/5 border-t border-border/50">
            {formatErrorMessage(outcome)}
          </div>
        ) : (
          <div
            className="whitespace-pre-wrap border-t border-border/50 px-2 py-1.5 text-xs text-muted-foreground"
            data-testid="workflow-edit-outcome"
          >
            {outcome}
          </div>
        ))}
    </div>
  );
}

export const WorkflowEditToolRenderer = memo(WorkflowEditToolRendererComponent);
