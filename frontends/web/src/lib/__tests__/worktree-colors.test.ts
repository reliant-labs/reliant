import { getWorktreeColor, resetWorktreeColors } from '../worktree-colors';

describe('worktree-colors', () => {
  beforeEach(() => {
    // Reset color assignments before each test
    resetWorktreeColors();
  });

  test('should assign unique colors to different worktrees', () => {
    const worktreeIds = ['wt1', 'wt2', 'wt3', 'wt4'];

    const colors = worktreeIds.map(id => getWorktreeColor(id, worktreeIds));

    // All colors should be unique
    const uniqueColors = new Set(colors);
    expect(uniqueColors.size).toBe(worktreeIds.length);
  });

  test('should return the same color for the same worktree ID', () => {
    const worktreeIds = ['wt1', 'wt2', 'wt3'];

    const color1 = getWorktreeColor('wt1', worktreeIds);
    const color2 = getWorktreeColor('wt1', worktreeIds);

    expect(color1).toBe(color2);
  });

  test('should handle up to 12 worktrees without duplicates', () => {
    const worktreeIds = Array.from({ length: 12 }, (_, i) => `wt${i + 1}`);

    const colors = worktreeIds.map(id => getWorktreeColor(id, worktreeIds));

    // All colors should be unique when we have 12 or fewer worktrees
    const uniqueColors = new Set(colors);
    expect(uniqueColors.size).toBe(12);
  });

  test('should assign colors consistently across calls with same worktree list', () => {
    const worktreeIds = ['wt1', 'wt2', 'wt3', 'wt4'];

    // First pass
    const firstPassColors = worktreeIds.map(id => getWorktreeColor(id, worktreeIds));

    // Second pass
    const secondPassColors = worktreeIds.map(id => getWorktreeColor(id, worktreeIds));

    // Colors should be the same
    expect(firstPassColors).toEqual(secondPassColors);
  });

  test('should work without providing allWorktreeIds parameter', () => {
    const color1 = getWorktreeColor('wt1');
    const color2 = getWorktreeColor('wt2');

    // Should return valid color strings
    expect(color1).toMatch(/^#[0-9a-f]{6}$/i);
    expect(color2).toMatch(/^#[0-9a-f]{6}$/i);
  });

  test('should preserve existing assignments when adding new worktrees', () => {
    const initialIds = ['wt1', 'wt2', 'wt3'];
    const initialColors = initialIds.map(id => getWorktreeColor(id, initialIds));

    // Add a new worktree
    const expandedIds = [...initialIds, 'wt4'];
    const expandedColors = expandedIds.map(id => getWorktreeColor(id, expandedIds));

    // First three colors should remain the same
    expect(expandedColors.slice(0, 3)).toEqual(initialColors);

    // New worktree should have a different color
    expect(expandedColors[3]).not.toBe(expandedColors[0]);
    expect(expandedColors[3]).not.toBe(expandedColors[1]);
    expect(expandedColors[3]).not.toBe(expandedColors[2]);
  });
});
