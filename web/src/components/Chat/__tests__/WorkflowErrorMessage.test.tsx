import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { WorkflowErrorMessage } from '../WorkflowErrorMessage';
import type { ErrorUpdate } from '../../../types/streaming';

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
});
