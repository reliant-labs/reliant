/**
 * Pins frontends/web/patches/@monaco-editor__loader@1.7.0.patch.
 *
 * The app never lets @monaco-editor/loader fetch Monaco: `monacoManager`
 * hands it the bundled editor first. These cover the loader on its own, as if
 * that ordering were ever broken:
 *
 *   - its default `paths.vs` names no CDN, so a stray init() asks our own
 *     origin for `/loader.js` and fails there, never at jsDelivr;
 *   - a script that fails to load rejects init() and nothing else. Unpatched,
 *     makeCancelable left a second promise rejected with the script's DOM
 *     Event uncaught, which failed every e2e page guard (reliant#2967).
 */

import { setImmediate } from 'node:timers/promises';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import loader from '@monaco-editor/loader';

describe('@monaco-editor/loader, as patched', () => {
  const unhandled: unknown[] = [];
  const record = (reason: unknown) => unhandled.push(reason);
  let loading: Promise<unknown>;
  let script: HTMLScriptElement | undefined;

  beforeAll(() => {
    process.on('unhandledRejection', record);
    // The loader's state is module-wide: one init(), one injected script.
    loading = loader.init();
    script = Array.from(document.body.querySelectorAll('script')).find((el) => el.src.endsWith('/loader.js'));
  });
  afterAll(() => {
    process.off('unhandledRejection', record);
  });

  it('asks our own origin for its loader script, never a CDN', () => {
    expect(script, 'the loader injects its loader.js script').toBeDefined();
    expect(script!.getAttribute('src')).toBe('/loader.js');
  });

  it('rejects init() when the script fails to load, and leaves no unhandled rejection', async () => {
    expect(script, 'the loader injects its loader.js script').toBeDefined();
    script!.dispatchEvent(new Event('error'));

    await expect(loading).rejects.toBeInstanceOf(Event);
    // Node reports an unhandled rejection from its rejection tracking, which
    // runs once the microtask queue has drained, before the next macrotask:
    // by the time setImmediate fires, any unhandled one has been reported.
    await setImmediate();
    expect(unhandled).toEqual([]);
  });
});
