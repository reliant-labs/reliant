import { describe, it, expect } from 'vitest';
import { isSkillTool, isLoadToolTool, isSpawnTool } from './toolFormatters';

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
