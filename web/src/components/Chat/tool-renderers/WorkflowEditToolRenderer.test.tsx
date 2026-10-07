/**
 * The workflow-editing tools draw their YAML as a diff, the way a file edit
 * does — not as the raw JSON arguments, where the YAML is one escaped string
 * full of "\n" (gtm/media/v2 README, R1).
 */

import { render, screen } from '@testing-library/react';
import { ToolContentArea } from './index';
import type { ToolRenderContext } from './types';
import { workflowEditDiff } from '../../../lib/toolFormatters';

const diffViewer = vi.hoisted(() =>
  vi.fn(({ filename, original, modified }: { filename?: string; original: string; modified: string }) => (
    <div data-testid="diff-viewer" data-filename={filename}>
      <pre data-testid="diff-original">{original}</pre>
      <pre data-testid="diff-modified">{modified}</pre>
    </div>
  )),
);

vi.mock('../LightweightDiffViewer', () => ({
  LightweightDiffViewer: diffViewer,
}));

const OLD = '  - id: agent\n    type: call_llm';
const NEW = '  - id: agent\n    type: call_llm\n    model: "{{inputs.model}}"';
const YAML = 'name: review\nentry: [agent]\nnodes:\n  - id: agent\n    type: call_llm';

function ctx(overrides: Partial<ToolRenderContext>): ToolRenderContext {
  return {
    toolName: 'edit_workflow',
    toolCallId: 'tool-1',
    input: {},
    isExpanded: true,
    isCompleted: true,
    isExecuting: false,
    isPreparing: false,
    hasFailed: false,
    ...overrides,
  };
}

describe('workflow-editing tool calls', () => {
  beforeEach(() => {
    diffViewer.mockClear();
  });

  it('draws edit_workflow as a YAML diff of old_string → new_string, not escaped JSON', () => {
    render(
      <ToolContentArea
        ctx={ctx({
          input: { id: 'wf-1', old_string: OLD, new_string: NEW },
          result: {
            name: 'edit_workflow',
            content: 'Workflow updated successfully.\n\nStatus: complete (runnable).',
          },
        })}
      />,
    );

    const viewer = screen.getByTestId('diff-viewer');
    expect(viewer).toHaveAttribute('data-filename', expect.stringMatching(/\.yaml$/));
    expect(screen.getByTestId('diff-original').textContent).toBe(OLD);
    expect(screen.getByTestId('diff-modified').textContent).toBe(NEW);
    // The raw arguments, as the generic renderer printed them, are gone.
    expect(screen.queryByText('Input')).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain('"old_string"');
    expect(document.body.textContent).not.toContain('\\n');
    // The status and validation findings follow the diff.
    expect(screen.getByTestId('workflow-edit-outcome')).toHaveTextContent('Status: complete (runnable).');
  });

  it.each(['create_workflow', 'write_workflow', 'mcp__reliant__write_workflow'])(
    'draws %s as the whole definition added',
    (toolName) => {
      render(<ToolContentArea ctx={ctx({ toolName, input: { id: 'wf-1', content: YAML } })} />);

      expect(screen.getByTestId('diff-original').textContent).toBe('');
      expect(screen.getByTestId('diff-modified').textContent).toBe(YAML);
      expect(screen.queryByText('Input')).not.toBeInTheDocument();
    },
  );

  it('reads arguments that arrive as a JSON string', () => {
    render(
      <ToolContentArea
        ctx={ctx({ input: JSON.stringify({ id: 'wf-1', old_string: OLD, new_string: NEW }) })}
      />,
    );

    expect(screen.getByTestId('diff-modified').textContent).toBe(NEW);
  });

  it('shows a rejected edit as a warning beneath the diff', () => {
    render(
      <ToolContentArea
        ctx={ctx({
          input: { id: 'wf-1', old_string: OLD, new_string: NEW },
          result: { name: 'edit_workflow', content: 'old_string not found in workflow.', is_error: true },
        })}
      />,
    );

    expect(screen.getByTestId('diff-viewer')).toBeInTheDocument();
    expect(screen.getByText('old_string not found in workflow.')).toBeInTheDocument();
    expect(screen.queryByTestId('workflow-edit-outcome')).not.toBeInTheDocument();
  });

  it('has no diff for create_workflow with the default template', () => {
    expect(workflowEditDiff('create_workflow', { name: 'triage' })).toBeNull();
    render(
      <ToolContentArea
        ctx={ctx({
          toolName: 'create_workflow',
          input: { name: 'triage' },
          result: { name: 'create_workflow', content: "Workflow 'triage' created successfully." },
        })}
      />,
    );

    expect(screen.queryByTestId('diff-viewer')).not.toBeInTheDocument();
    expect(screen.getByTestId('workflow-edit-outcome')).toHaveTextContent("Workflow 'triage' created successfully.");
  });
});
