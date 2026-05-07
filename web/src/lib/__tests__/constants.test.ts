import { WORKTREE_OPERATION_TIMEOUT_MS } from '../constants';

describe('timeout constants', () => {
  test('worktree operations have an extended timeout', () => {
    expect(WORKTREE_OPERATION_TIMEOUT_MS).toBe(120000);
    expect(WORKTREE_OPERATION_TIMEOUT_MS).toBeGreaterThan(30000);
  });
});
