import { describe, expect, it } from 'vitest';
import { cleanTemporalErrorMessage, hasTemporalScaffolding } from '../temporalErrors';

describe('cleanTemporalErrorMessage', () => {
  it('strips the activity error frame and keeps the causal chain', () => {
    const raw =
      'activity error (type: CallLLM, scheduledEventID: 104, startedEventID: 105, identity: 53948@host): ' +
      'failed to stream LLM response: LLM streaming error: received error while streaming: ' +
      '{"type":"error","error":{"details":null,"type":"overloaded_error","message":"Overloaded"},"request_id":"req_011CZ9Dhrfa8CWNE5TY9tjnt"}';

    const cleaned = cleanTemporalErrorMessage(raw);

    expect(cleaned).toBe(
      'failed to stream LLM response: LLM streaming error: received error while streaming: ' +
        '{"type":"error","error":{"details":null,"type":"overloaded_error","message":"Overloaded"},"request_id":"req_011CZ9Dhrfa8CWNE5TY9tjnt"}'
    );
    expect(cleaned).not.toContain('scheduledEventID');
    expect(cleaned).not.toContain('identity:');
  });

  it('handles an identity containing an @ suffix', () => {
    const raw =
      'activity error (type: CallLLM, scheduledEventID: 35, startedEventID: 36, identity: 82721@MacBook-Pro-5.local@): ' +
      'failed to stream LLM response: LLM streaming error: POST "https://api.anthropic.com/v1/messages": 401 Unauthorized';

    expect(cleanTemporalErrorMessage(raw)).toBe(
      'failed to stream LLM response: LLM streaming error: POST "https://api.anthropic.com/v1/messages": 401 Unauthorized'
    );
  });

  it('strips child workflow and workflow execution frames', () => {
    const raw =
      'workflow execution error (type: ReliantWorkflow, workflowID: wf-123, runID: 9c1f-abc): ' +
      'child workflow execution error (type: SubWorkflow, workflowID: wf-456, runID: aa11, initiatedEventID: 12, startedEventID: 13): ' +
      'tool execution failed';

    expect(cleanTemporalErrorMessage(raw)).toBe('tool execution failed');
  });

  it('removes the application error type and retryable suffix', () => {
    const raw =
      'activity error (type: CallLLM, scheduledEventID: 1, startedEventID: 2, identity: host): ' +
      'model request rejected (type: ProviderError, retryable: true)';

    expect(cleanTemporalErrorMessage(raw)).toBe('model request rejected');
  });

  it('rewords timeout kinds instead of dropping them', () => {
    const raw =
      'activity error (type: CallLLM, scheduledEventID: 1, startedEventID: 2, identity: host): ' +
      'activity timeout (type: StartToClose)';

    expect(cleanTemporalErrorMessage(raw)).toBe('activity timeout (start-to-close timeout)');
  });

  it('collapses repeated re-wrapped segments', () => {
    const raw =
      'activity error (type: CallLLM, scheduledEventID: 1, startedEventID: 2, identity: host): ' +
      'connection refused: connection refused: dial tcp 127.0.0.1:8080';

    expect(cleanTemporalErrorMessage(raw)).toBe('connection refused: dial tcp 127.0.0.1:8080');
  });

  it('leaves a plain error untouched', () => {
    const raw = 'rate limit exceeded for model claude-opus-4-20250514';
    expect(cleanTemporalErrorMessage(raw)).toBe(raw);
  });

  it('preserves the original when scaffolding is the entire message', () => {
    const raw =
      'activity error (type: CallLLM, scheduledEventID: 1, startedEventID: 2, identity: host)';
    expect(cleanTemporalErrorMessage(raw)).toBe(raw);
  });

  it('returns empty input unchanged', () => {
    expect(cleanTemporalErrorMessage('')).toBe('');
  });

  it('reports whether scaffolding was present', () => {
    expect(
      hasTemporalScaffolding(
        'activity error (type: CallLLM, scheduledEventID: 1, startedEventID: 2, identity: host): boom'
      )
    ).toBe(true);
    expect(hasTemporalScaffolding('boom')).toBe(false);
  });
});
