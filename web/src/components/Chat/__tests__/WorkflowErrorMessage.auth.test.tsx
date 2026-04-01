import { render, screen } from '@testing-library/react';
import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { WorkflowErrorMessage } from '../WorkflowErrorMessage';
import type { ErrorUpdate } from '../../../types/streaming';

function buildError(overrides: Partial<ErrorUpdate> = {}): ErrorUpdate {
  return {
    update_type: 'error',
    id: 'err-1',
    chat_id: 'chat-1',
    activity_type: 'V2_WorkflowError',
    activity_id: 'activity-1',
    error_message: 'generic error',
    timestamp: '2026-03-24T13:19:08.000Z',
    sequence_number: 1,
    ...overrides,
  };
}

describe('WorkflowErrorMessage auth fallback', () => {
  it('renders Claude reconnect guidance for older Anthropic auth errors without error_summary', () => {
    render(
      <WorkflowErrorMessage
        error={buildError({
          error_message:
            'failed to stream LLM response: LLM streaming error: POST "https://api.anthropic.com/v1/messages": 401 Unauthorized {"type":"error","error":{"type":"authentication_error","message":"Invalid authentication credentials"}}',
        })}
      />,
    );

    expect(
      screen.getByText('Claude session expired. Please reconnect Claude. Workflow paused — send a message to retry.'),
    ).toBeInTheDocument();
  });

  it('renders Codex reconnect guidance for older Codex auth errors without error_summary', () => {
    render(
      <WorkflowErrorMessage
        error={buildError({
          error_message: 'codex authentication required: connect Codex from Settings',
        })}
      />,
    );

    expect(
      screen.getByText('Codex session expired. Please reconnect Codex. Workflow paused — send a message to retry.'),
    ).toBeInTheDocument();
  });
});