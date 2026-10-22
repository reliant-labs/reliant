import { renderHook } from '@testing-library/react';

// Mock the chat store hook so useLoadedSkills doesn't pull in the real store.
// We drive the hook by controlling what `useChatMessages` returns per test.
const useChatMessagesMock = vi.hoisted(() => vi.fn());

vi.mock('../store/chatStoreHooks', () => ({
  useChatMessages: useChatMessagesMock,
}));

// The real `../types/chat` re-exports proto enums from the generated `gen/`
// folder, which may not exist in every test environment. Provide a minimal
// shim with just the enum values that useLoadedSkills actually touches.
vi.mock('../types/chat', () => ({
  ContentBlockType: {
    UNSPECIFIED: 0,
    TEXT: 1,
    TOOL_CALL: 2,
    TOOL_RESULT: 3,
    THINKING: 4,
  },
}));

import { useLoadedSkills } from './useLoadedSkills';

// Minimal message/block shapes — only the fields the hook reads.
type Block = {
  type: number;
  toolName?: string;
  input?: string;
};
type Message = { contentBlocks: Block[] };

const TOOL_CALL = 2;
const TEXT = 1;

function skillCallBlock(input: Record<string, unknown>): Block {
  return {
    type: TOOL_CALL,
    toolName: 'skill',
    input: JSON.stringify(input),
  };
}

function msg(...blocks: Block[]): Message {
  return { contentBlocks: blocks };
}

describe('useLoadedSkills', () => {
  beforeEach(() => {
    useChatMessagesMock.mockReset();
  });

  it('returns empty array when there are no messages', () => {
    useChatMessagesMock.mockReturnValue([]);

    const { result } = renderHook(() => useLoadedSkills('chat-1'));
    expect(result.current).toEqual([]);
  });

  it('returns empty array when messages have no skill calls', () => {
    useChatMessagesMock.mockReturnValue([
      msg({ type: TEXT }),
      msg({ type: TOOL_CALL, toolName: 'view', input: '{}' }),
    ]);

    const { result } = renderHook(() => useLoadedSkills('chat-1'));
    expect(result.current).toEqual([]);
  });

  it('extracts loaded skill names from messages', () => {
    useChatMessagesMock.mockReturnValue([
      msg(skillCallBlock({ action: 'load', name: 'go' })),
      msg(skillCallBlock({ action: 'load', name: 'python/typing' })),
    ]);

    const { result } = renderHook(() => useLoadedSkills('chat-1'));
    expect(result.current).toEqual(['go', 'python/typing']);
  });

  it('deduplicates skill names across messages', () => {
    useChatMessagesMock.mockReturnValue([
      msg(skillCallBlock({ action: 'load', name: 'go' })),
      msg(skillCallBlock({ action: 'load', name: 'go' })),
      msg(skillCallBlock({ action: 'load', name: 'python' })),
    ]);

    const { result } = renderHook(() => useLoadedSkills('chat-1'));
    expect(result.current).toEqual(['go', 'python']);
  });

  it('ignores skill list and search calls (only load counts)', () => {
    useChatMessagesMock.mockReturnValue([
      msg(skillCallBlock({ action: 'list' })),
      msg(skillCallBlock({ action: 'search', query: 'error' })),
      msg(skillCallBlock({ action: 'load', name: 'go' })),
    ]);

    const { result } = renderHook(() => useLoadedSkills('chat-1'));
    expect(result.current).toEqual(['go']);
  });

  it('handles nested skill paths', () => {
    useChatMessagesMock.mockReturnValue([
      msg(skillCallBlock({ action: 'load', name: 'go/error-handling' })),
    ]);

    const { result } = renderHook(() => useLoadedSkills('chat-1'));
    expect(result.current).toEqual(['go/error-handling']);
  });

  it('accepts skill_name as an alternative name key', () => {
    useChatMessagesMock.mockReturnValue([
      msg(skillCallBlock({ action: 'load', skill_name: 'python' })),
    ]);

    const { result } = renderHook(() => useLoadedSkills('chat-1'));
    expect(result.current).toEqual(['python']);
  });

  it('handles malformed JSON input without crashing', () => {
    useChatMessagesMock.mockReturnValue([
      msg({
        type: TOOL_CALL,
        toolName: 'skill',
        input: '{not valid json',
      }),
      msg(skillCallBlock({ action: 'load', name: 'go' })),
    ]);

    const { result } = renderHook(() => useLoadedSkills('chat-1'));
    expect(result.current).toEqual(['go']);
  });

  it('handles skill calls with load action but no name', () => {
    useChatMessagesMock.mockReturnValue([
      msg(skillCallBlock({ action: 'load' })),
      msg(skillCallBlock({ action: 'load', name: 'go' })),
    ]);

    const { result } = renderHook(() => useLoadedSkills('chat-1'));
    expect(result.current).toEqual(['go']);
  });

  it('is case-insensitive for the skill tool name', () => {
    useChatMessagesMock.mockReturnValue([
      msg({
        type: TOOL_CALL,
        toolName: 'Skill',
        input: JSON.stringify({ action: 'load', name: 'go' }),
      }),
    ]);

    const { result } = renderHook(() => useLoadedSkills('chat-1'));
    expect(result.current).toEqual(['go']);
  });

  it('updates when messages change between renders', () => {
    useChatMessagesMock.mockReturnValue([
      msg(skillCallBlock({ action: 'load', name: 'go' })),
    ]);

    const { result, rerender } = renderHook(() => useLoadedSkills('chat-1'));
    expect(result.current).toEqual(['go']);

    useChatMessagesMock.mockReturnValue([
      msg(skillCallBlock({ action: 'load', name: 'go' })),
      msg(skillCallBlock({ action: 'load', name: 'python' })),
    ]);

    rerender();
    expect(result.current).toEqual(['go', 'python']);
  });
});
