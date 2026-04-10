import { describe, it, expect } from 'vitest';
import { isSkillTool, isLoadToolTool } from './toolFormatters';

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
