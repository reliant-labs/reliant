import { render, fireEvent } from '@testing-library/react';
import { LightweightDiffViewer } from './LightweightDiffViewer';

// Backgrounds from getDarkColors().
const ADDED_BG = 'rgb(26, 77, 46)';
const CONTEXT_BG = 'rgb(30, 30, 30)';

function rowsIn(container: HTMLElement): HTMLElement[] {
  const pre = container.querySelector('pre');
  if (!pre) throw new Error('no <pre>');
  // Rendered rows hold a .token-line; the virtualization spacers do not.
  return Array.from(pre.children).filter((el) =>
    el.querySelector('.token-line')
  ) as HTMLElement[];
}

beforeEach(() => {
  document.documentElement.classList.add('dark');
});

describe('LightweightDiffViewer', () => {
  it('paints the row background across the full scroll width, not just the viewport', () => {
    // jsdom does no layout, so this asserts the CSS contract that produces the
    // behaviour rather than measured pixels: a flex row defaults to the
    // container's visible width, which truncates the add/remove background at
    // the horizontal fold on any line wide enough to scroll. Verified in
    // Chrome: without min-width:max-content a 1050px-wide line left a 450px
    // uncolored gap; with it the row measured the full 1050px.
    const original = 'const a = 1;';
    const modified = `const a = 1;\nconst wide = "${'x'.repeat(400)}";`;

    const { container } = render(
      <LightweightDiffViewer
        original={original}
        modified={modified}
        filename="wide.ts"
        showLineNumbers={false}
      />
    );

    const added = rowsIn(container).filter(
      (row) => row.style.backgroundColor === ADDED_BG
    );

    expect(added.length).toBeGreaterThan(0);
    for (const row of added) {
      expect(row.style.minWidth).toBe('max-content');
    }
  });

  it('keeps add/remove backgrounds on rows revealed by vertical scrolling', () => {
    // Guards the virtualized path: a hunk far below the initial window must
    // still carry its background when scrolled into view.
    const originalLines = Array.from({ length: 400 }, (_, i) => `const line${i} = ${i};`);
    const modifiedLines = [...originalLines];
    modifiedLines.splice(380, 0, 'const insertedDeep = "deep";');

    const { container } = render(
      <LightweightDiffViewer
        original={originalLines.join('\n')}
        modified={modifiedLines.join('\n')}
        filename="big.ts"
        showLineNumbers={false}
        maxHeight={250}
      />
    );

    const pre = container.querySelector('pre')!;
    fireEvent.scroll(pre, { target: { scrollTop: 100000 } });

    const inserted = rowsIn(container).find((row) =>
      row.textContent?.includes('insertedDeep')
    );

    expect(inserted).toBeTruthy();
    expect(inserted!.style.backgroundColor).toBe(ADDED_BG);
  });

  it('leaves trailing unchanged lines on the context background', () => {
    // The complement of the bug: unchanged lines below a hunk are *supposed* to
    // be uncolored, so a fix must not paint them green.
    const original = 'const a = 1;\nconst b = 2;\nconst c = 3;';
    const modified = 'const a = 1;\nconst b = 22;\nconst c = 3;';

    const { container } = render(
      <LightweightDiffViewer
        original={original}
        modified={modified}
        filename="small.ts"
        showLineNumbers={false}
      />
    );

    const rows = rowsIn(container);
    const last = rows[rows.length - 1];

    expect(last.textContent).toContain('const c = 3;');
    expect(last.style.backgroundColor).toBe(CONTEXT_BG);
  });
});
