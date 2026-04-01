import { beforeEach, describe, expect, it, vi } from 'vitest';

import { MessageRole } from '../../gen/reliant/v1/chat_pb';
import { buildWorkflowParamsPayload, chatGrpc } from '../chat-grpc';

const mocks = vi.hoisted(() => ({
  createChat: vi.fn(),
  sendMessage: vi.fn(),
  updateWorkflowParams: vi.fn(),
}));

vi.mock('../grpc-client', () => ({
  grpcClient: {
    chat: () => ({
      createChat: mocks.createChat,
      sendMessage: mocks.sendMessage,
      updateWorkflowParams: mocks.updateWorkflowParams,
    }),
  },
}));

describe('chat-grpc workflow_params payload contracts', () => {
  beforeEach(() => {
    mocks.createChat.mockReset();
    mocks.sendMessage.mockReset();
    mocks.updateWorkflowParams.mockReset();

    mocks.createChat.mockResolvedValue({
      chat: { id: 'chat-1' },
      workflowId: 'workflow-1',
      runId: 'run-1',
      draftId: '',
    });

    mocks.sendMessage.mockResolvedValue({
      chatId: 'chat-1',
      workflowId: 'workflow-1',
      runId: 'run-1',
      status: 'ok',
      messageId: 'message-1',
    });

    mocks.updateWorkflowParams.mockResolvedValue({
      success: true,
      message: 'ok',
    });
  });

  it('buildWorkflowParamsPayload keeps nested workflow_params structure only', () => {
    const payload = buildWorkflowParamsPayload({
      agent: {
        model: { id: 'claude-3-7-sonnet' },
      },
      mode: 'ask',
    });

    expect(payload.mode).toMatchObject({ kind: { case: 'stringValue', value: 'ask' } });
    expect(payload.agent.kind.case).toBe('structValue');
    // model is now an object {id: string}, which becomes a nested struct
    const agentFields = payload.agent.kind.case === 'structValue' ? payload.agent.kind.value.fields : {};
    expect(agentFields.model.kind.case).toBe('structValue');
    const modelFields = agentFields.model.kind.case === 'structValue' ? agentFields.model.kind.value.fields : {};
    expect(modelFields.id).toMatchObject({ kind: { case: 'stringValue', value: 'claude-3-7-sonnet' } });
  });

  it('buildWorkflowParamsPayload rejects dotted keys', () => {
    expect(() =>
      buildWorkflowParamsPayload({
        'agent.model': 'claude-3-7-sonnet',
      }),
    ).toThrow('workflow_params must use nested object keys. Dotted keys are no longer supported.');
  });

  it('create sends nested workflow_params and preserves selected presets mapping', async () => {
    await chatGrpc.create({
      project_id: 'project-1',
      messages: [{ role: MessageRole.USER, content: 'hello' }],
      workflow_params: {
        agent: {
          model: { id: 'claude-3-7-sonnet' },
        },
      },
      selected_presets: {
        '': 'workflow-default',
        Agent: 'agent-preset',
      },
    });

    const request = mocks.createChat.mock.calls[0][0];
    expect(request.workflowParams).toHaveProperty('agent');
    expect(request.workflowParams).not.toHaveProperty('agent.model');
    expect(request.selectedPresets).toEqual({
      '': 'workflow-default',
      Agent: 'agent-preset',
    });
  });

  it('sendMessage rejects dotted workflow_params and does not call the gRPC client', async () => {
    await expect(
      chatGrpc.sendMessage('chat-1', {
        messages: [{ role: MessageRole.USER, content: 'hello' }],
        workflow_params: {
          'agent.model': 'claude-3-7-sonnet',
        },
      }),
    ).rejects.toThrow('workflow_params must use nested object keys. Dotted keys are no longer supported.');

    expect(mocks.sendMessage).not.toHaveBeenCalled();
  });

  it('sendMessage sends nested workflow_params and preserves selected presets mapping', async () => {
    await chatGrpc.sendMessage('chat-1', {
      messages: [{ role: MessageRole.USER, content: 'hello' }],
      workflow_params: {
        agent: {
          model: { id: 'claude-3-7-sonnet' },
        },
      },
      selected_presets: {
        '': 'workflow-default',
        Agent: 'agent-preset',
      },
    });

    const request = mocks.sendMessage.mock.calls[0][0];
    expect(request.workflowParams).toHaveProperty('agent');
    expect(request.workflowParams).not.toHaveProperty('agent.model');
    expect(request.selectedPresets).toEqual({
      '': 'workflow-default',
      Agent: 'agent-preset',
    });
  });
});
