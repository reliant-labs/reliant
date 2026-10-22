// Color palette for worktrees
const WORKTREE_COLORS = [
  '#4ade80', // green
  '#3b82f6', // blue
  '#a855f7', // purple
  '#f59e0b', // amber
  '#ec4899', // pink
  '#06b6d4', // cyan
  '#8b5cf6', // violet
  '#10b981', // emerald
  '#f97316', // orange
  '#14b8a6', // teal
  '#6366f1', // indigo
  '#84cc16', // lime
];

// Cache to track assigned colors and ensure uniqueness
const colorAssignments = new Map<string, string>();
const usedColors = new Set<string>();

// Generate a consistent unique color for a worktree based on its ID
export function getWorktreeColor(worktreeId: string, allWorktreeIds?: string[]): string {
  // Return cached color if already assigned
  if (colorAssignments.has(worktreeId)) {
    return colorAssignments.get(worktreeId)!;
  }

  // If we have all worktree IDs, rebuild the cache to ensure uniqueness
  if (allWorktreeIds) {
    // Clear and rebuild cache with current worktrees
    const existingAssignments = new Map(colorAssignments);
    colorAssignments.clear();
    usedColors.clear();

    // Sort worktree IDs for consistent assignment order
    const sortedIds = [...allWorktreeIds].sort();

    for (const id of sortedIds) {
      // Preserve existing assignment if it was already cached
      if (existingAssignments.has(id)) {
        const existingColor = existingAssignments.get(id)!;
        if (!usedColors.has(existingColor)) {
          colorAssignments.set(id, existingColor);
          usedColors.add(existingColor);
          continue;
        }
      }

      // Find first unused color
      const unusedColor = WORKTREE_COLORS.find(color => !usedColors.has(color));
      if (unusedColor) {
        colorAssignments.set(id, unusedColor);
        usedColors.add(unusedColor);
      } else {
        // Fallback: if all colors are used, use hash-based assignment
        const color = hashBasedColor(id);
        colorAssignments.set(id, color);
        usedColors.add(color);
      }
    }
  } else {
    // No full list provided, use hash-based with collision avoidance
    let attempts = 0;
    let color = hashBasedColor(worktreeId);

    // Try to find an unused color by trying different hash seeds
    while (usedColors.has(color) && attempts < WORKTREE_COLORS.length) {
      color = hashBasedColor(worktreeId + attempts);
      attempts++;
    }

    colorAssignments.set(worktreeId, color);
    usedColors.add(color);
  }

  return colorAssignments.get(worktreeId)!;
}

// Helper function for hash-based color selection
function hashBasedColor(input: string): string {
  let hash = 0;
  for (let i = 0; i < input.length; i++) {
    hash = input.charCodeAt(i) + ((hash << 5) - hash);
  }
  const index = Math.abs(hash) % WORKTREE_COLORS.length;
  return WORKTREE_COLORS[index];
}

// Function to reset color assignments (useful for testing or when worktrees are deleted)
export function resetWorktreeColors(): void {
  colorAssignments.clear();
  usedColors.clear();
}
