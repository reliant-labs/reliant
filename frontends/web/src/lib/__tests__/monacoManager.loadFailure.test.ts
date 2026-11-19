/**
 * Monaco is a lazy chunk of this build (`lib/monacoBundle`, loaded by
 * `lib/monacoManager`). When that chunk fails to load — a flaky network, or
 * a deploy that replaced it under an open tab — the failure must reach the
 * caller as an Error naming what failed, and nowhere else: no unhandled
 * rejection, which fails every e2e page guard.
 *
 * It used to be a CDN script, whose failure @monaco-editor/loader reported as
 * a bare DOM `Event` and also left uncaught (frontends/web/patches fixes the
 * library; e2e/monaco-cdn-unavailable.spec.ts proves the CDN is out of the
 * picture).
 */

import { setImmediate } from 'node:timers/promises';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../monacoBundle', () => {
  throw new Error('Failed to fetch dynamically imported module: /assets/monacoBundle-x.js');
});

import { monacoManager } from '../monacoManager';

describe('the Monaco chunk fails to load', () => {
  const unhandled: unknown[] = [];
  const record = (reason: unknown) => unhandled.push(reason);

  beforeEach(() => {
    unhandled.length = 0;
    process.on('unhandledRejection', record);
  });
  afterEach(() => {
    process.off('unhandledRejection', record);
  });

  it('rejects getMonaco with an Error naming the chunk, and leaves no unhandled rejection', async () => {
    await expect(monacoManager.getMonaco()).rejects.toThrow(/^The Monaco editor chunk did not load: ./);
    // Node's rejection tracking reports an unhandled rejection once the
    // microtask queue has drained, before the next macrotask runs.
    await setImmediate();
    expect(unhandled).toEqual([]);
  });

  it('does not cache the failure: the next call loads again', async () => {
    await expect(monacoManager.getMonaco()).rejects.toThrow();
    expect(monacoManager.getStatus().initialized).toBe(false);
    await expect(monacoManager.getMonaco()).rejects.toThrow(/Monaco editor chunk did not load/);
  });
});
