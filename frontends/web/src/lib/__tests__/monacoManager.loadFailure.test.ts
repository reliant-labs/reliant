/**
 * Monaco is fetched from a CDN (`lib/monacoManager`). When its loader script
 * fails to load — the CDN is down, or its response is blocked — the failure
 * must reach the caller as an Error, and nowhere else.
 *
 * @monaco-editor/loader rejects with the script's DOM error Event, and its
 * makeCancelable left a second, uncaught promise rejected with it: every
 * desktop page load raised an unhandled rejection whose value was an `Event`,
 * which failed the org-switcher e2e guard when jsdelivr's response was
 * blocked in CI. frontends/web/patches/@monaco-editor__loader@1.7.0.patch
 * fixes the library; this pins it, and the Error the manager rethrows.
 */

import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { monacoManager } from '../monacoManager';

describe('Monaco loader script fails to load', () => {
  const unhandled: unknown[] = [];
  const record = (reason: unknown) => unhandled.push(reason);

  beforeEach(() => {
    unhandled.length = 0;
    process.on('unhandledRejection', record);
  });
  afterEach(() => {
    process.off('unhandledRejection', record);
  });

  it('rejects getMonaco with an Error naming the script, and leaves no unhandled rejection', async () => {
    const loading = monacoManager.getMonaco('load-failure');

    const script = Array.from(document.body.querySelectorAll('script')).find((el) => el.src.endsWith('/vs/loader.js'));
    expect(script, 'the loader injects its loader.js script').toBeDefined();
    script!.dispatchEvent(new Event('error'));

    await expect(loading).rejects.toThrow(/loader script did not load \(error event\): https:\/\/cdn\.jsdelivr\.net\/.*\/vs\/loader\.js/);
    // Unhandled rejections are reported after the microtask queue drains.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(unhandled).toEqual([]);
  });
});
