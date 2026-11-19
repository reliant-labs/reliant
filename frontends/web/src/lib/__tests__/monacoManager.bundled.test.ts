/**
 * `monacoManager` gives @monaco-editor/loader the bundled Monaco before the
 * loader's first `init()`. The loader reads its config only on that first
 * call; an init that ran first would inject the loader's default script
 * instead of using the editor this build ships.
 */

import { describe, expect, it, vi } from 'vitest';

const calls = vi.hoisted(() => [] as string[]);
const fakeMonaco = vi.hoisted(() => ({ editor: {}, languages: {} }));

vi.mock('../monacoBundle', () => ({ monaco: fakeMonaco }));
vi.mock('@monaco-editor/react', () => ({
  loader: {
    config: vi.fn((config: { monaco?: unknown }) => calls.push(config.monaco === fakeMonaco ? 'config(bundled monaco)' : 'config(other)')),
    init: vi.fn(async () => {
      calls.push('init');
      return fakeMonaco;
    }),
  },
}));
vi.mock('../monacoTheme', () => ({ configureMonacoTheme: vi.fn(() => calls.push('theme')) }));
vi.mock('../monaco-cel-language', () => ({ registerCELLanguage: vi.fn(() => calls.push('cel')) }));

import { monacoManager } from '../monacoManager';

describe('monacoManager with the bundled Monaco', () => {
  it('configures the loader with it before init, then themes it, once', async () => {
    const [first, second] = await Promise.all([monacoManager.getMonaco(), monacoManager.getMonaco()]);

    expect(first).toBe(fakeMonaco);
    expect(second).toBe(fakeMonaco);
    expect(await monacoManager.getMonaco()).toBe(fakeMonaco);
    expect(calls).toEqual(['config(bundled monaco)', 'init', 'theme', 'cel']);
    expect(monacoManager.current()).toBe(fakeMonaco);
  });
});
