import { describe, it, expect } from 'vitest';
import { formatToolParams, isSkillTool, isLoadToolTool, isSpawnSendTool, isSpawnTool } from './toolFormatters';

describe('isSkillTool', () => {
  it('returns true for the "skill" tool name', () => {
    expect(isSkillTool('skill')).toBe(true);
  });

  it('returns false for other tool names', () => {
    expect(isSkillTool('view')).toBe(false);
    expect(isSkillTool('grep')).toBe(false);
    expect(isSkillTool('load_tool')).toBe(false);
    expect(isSkillTool('skills')).toBe(false);
    expect(isSkillTool('')).toBe(false);
  });

  it('is case-insensitive', () => {
    expect(isSkillTool('Skill')).toBe(true);
    expect(isSkillTool('SKILL')).toBe(true);
    expect(isSkillTool('SkIlL')).toBe(true);
  });
});

describe('isLoadToolTool', () => {
  it('returns true for the "load_tool" tool name', () => {
    expect(isLoadToolTool('load_tool')).toBe(true);
  });

  it('returns false for other tool names', () => {
    expect(isLoadToolTool('skill')).toBe(false);
    expect(isLoadToolTool('load')).toBe(false);
    expect(isLoadToolTool('loadTool')).toBe(false);
    expect(isLoadToolTool('tool')).toBe(false);
    expect(isLoadToolTool('')).toBe(false);
  });

  it('is case-insensitive', () => {
    expect(isLoadToolTool('Load_Tool')).toBe(true);
    expect(isLoadToolTool('LOAD_TOOL')).toBe(true);
  });
});

describe('isSpawnTool', () => {
  it('returns true for the "spawn" tool name', () => {
    expect(isSpawnTool('spawn')).toBe(true);
  });

  it('returns false for sibling tools that merely contain "spawn"', () => {
    expect(isSpawnTool('spawn_status')).toBe(false);
    expect(isSpawnTool('spawn_send')).toBe(false);
  });

  it('matches the base name through an MCP prefix', () => {
    expect(isSpawnTool('mcp__reliant__spawn')).toBe(true);
    expect(isSpawnTool('mcp__reliant__spawn_status')).toBe(false);
    expect(isSpawnTool('mcp__reliant__spawn_send')).toBe(false);
  });

  it('returns false for other tool names', () => {
    expect(isSpawnTool('view')).toBe(false);
    expect(isSpawnTool('bash')).toBe(false);
    expect(isSpawnTool('')).toBe(false);
  });

  it('is case-insensitive', () => {
    expect(isSpawnTool('Spawn')).toBe(true);
    expect(isSpawnTool('SPAWN')).toBe(true);
  });
});

describe('spawn_send', () => {
  it('is recognised bare and MCP-prefixed, and only spawn_send', () => {
    expect(isSpawnSendTool('spawn_send')).toBe(true);
    expect(isSpawnSendTool('mcp__reliant__spawn_send')).toBe(true);
    expect(isSpawnSendTool('spawn')).toBe(false);
    expect(isSpawnSendTool('spawn_status')).toBe(false);
  });

  it('formats the row as the subject addressed to the recipient, never the message', () => {
    const formatted = formatToolParams('spawn_send', {
      agent_id: '6b1f3c2e-uuid',
      title: 'Reviewer',
      subject: 'Tests pass; PR ready',
      message: 'A very long report that must not appear on the one-line row.',
    });
    expect(formatted?.summary).toBe('to Reviewer: Tests pass; PR ready');
    expect(formatted?.summary).not.toContain('6b1f3c2e');
    expect(formatted?.fullText).not.toContain('very long report');
  });

  it('falls back to the first line of the message when there is no subject', () => {
    const formatted = formatToolParams('spawn_send', {
      agent_id: 'thread-1',
      message: '**Done.** All green\nsecond line',
    });
    expect(formatted?.summary).toBe('to agent: Done. All green');
  });
});
