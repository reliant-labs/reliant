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

import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import loader from '@monaco-editor/loader';

describe('@monaco-editor/loader, as patched', () => {
  const unhandled: unknown[] = [];
  const record = (reason: unknown) => unhandled.push(reason);

  beforeEach(() => {
    unhandled.length = 0;
    process.on('unhandledRejection', record);
  });
  afterEach(() => {
    process.off('unhandledRejection', record);
  });

  it('asks no CDN for Monaco, and a script that fails to load rejects init() alone', async () => {
    const loading = loader.init();

    const script = Array.from(document.body.querySelectorAll('script')).find((el) => el.src.endsWith('/loader.js'));
    expect(script, 'the loader injects its loader.js script').toBeDefined();
    expect(script!.getAttribute('src')).toBe('/loader.js');
    script!.dispatchEvent(new Event('error'));

    await expect(loading).rejects.toBeInstanceOf(Event);
    // Unhandled rejections are reported after the microtask queue drains.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(unhandled).toEqual([]);
  });
});
