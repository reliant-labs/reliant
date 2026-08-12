import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { WorkflowErrorMessage } from '../WorkflowErrorMessage';
import type { ErrorUpdate } from '../../../types/streaming';

const TEMPORAL_WRAPPED_ERROR =
  'activity error (type: CallLLM, scheduledEventID: 104, startedEventID: 105, identity: 53948@host): ' +
  'failed to stream LLM response: upstream connection reset';

function createErrorUpdate(overrides: Partial<ErrorUpdate> = {}): ErrorUpdate {
  return {
    update_type: 'error',
    id: 'error-1',
    chat_id: 'chat-1',
    activity_type: 'V2_CallLLM',
    activity_id: 'activity-1',
    error_message: 'rate limit exceeded',
    timestamp: '2025-01-01T12:00:00.000Z',
    sequence_number: 1,
    ...overrides,
  };
}

describe('WorkflowErrorMessage', () => {
  it('renders retrying errors with retry status and warning styling cues', () => {
    render(
      <WorkflowErrorMessage
        error={createErrorUpdate({
          attempt_number: 2,
          max_attempts: 5,
          is_retrying: true,
        })}
      />
    );

    expect(screen.getByText('Rate limited by the AI provider')).toBeInTheDocument();
    expect(screen.getByText('Retrying (Attempt 2/5)')).toBeInTheDocument();
    expect(screen.getByTestId('rotate-cw')).toBeInTheDocument();
    expect(screen.queryByTestId('alert-triangle')).not.toBeInTheDocument();
  });

  it('renders exhausted errors without retrying label and with destructive styling cues', () => {
    render(
      <WorkflowErrorMessage
        error={createErrorUpdate({
          error_summary: 'Rate limited by the AI provider. Workflow paused — send a message to retry.',
          attempt_number: 5,
          max_attempts: 5,
          is_retrying: false,
        })}
      />
    );

    expect(
      screen.getByText('Rate limited by the AI provider. Workflow paused — send a message to retry.')
    ).toBeInTheDocument();
    expect(screen.getByText('Attempt 5/5')).toBeInTheDocument();
    expect(screen.getByTestId('alert-triangle')).toBeInTheDocument();
    expect(screen.queryByTestId('rotate-cw')).not.toBeInTheDocument();
    expect(screen.queryByText('Retrying (Attempt 5/5)')).not.toBeInTheDocument();
  });

  it('hides Temporal event bookkeeping in the expanded detail view', async () => {
    const user = userEvent.setup();
    render(
      <WorkflowErrorMessage
        error={createErrorUpdate({ error_message: TEMPORAL_WRAPPED_ERROR })}
      />
    );

    await user.click(screen.getByRole('button', { name: /Click for details/ }));

    expect(
      screen.getByText('failed to stream LLM response: upstream connection reset')
    ).toBeInTheDocument();
    expect(screen.queryByText(/scheduledEventID/)).not.toBeInTheDocument();
    expect(screen.queryByText(/identity:/)).not.toBeInTheDocument();
  });

  it('reveals the untouched error string on demand', async () => {
    const user = userEvent.setup();
    render(
      <WorkflowErrorMessage
        error={createErrorUpdate({ error_message: TEMPORAL_WRAPPED_ERROR })}
      />
    );

    await user.click(screen.getByRole('button', { name: /Click for details/ }));
    await user.click(screen.getByRole('button', { name: 'Show raw error' }));

    expect(screen.getByText(TEMPORAL_WRAPPED_ERROR)).toBeInTheDocument();
  });

  it('omits the raw toggle when there is no scaffolding to strip', async () => {
    const user = userEvent.setup();
    render(
      <WorkflowErrorMessage
        error={createErrorUpdate({ error_message: 'plain failure with no wrapper' })}
      />
    );

    await user.click(screen.getByRole('button', { name: /Click for details/ }));

    expect(screen.getByText('plain failure with no wrapper')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Show raw error' })).not.toBeInTheDocument();
  });
});
