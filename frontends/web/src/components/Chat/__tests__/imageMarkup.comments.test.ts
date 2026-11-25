import { describe, expect, it } from 'vitest';
import { formatCommentsBlock, insertCommentsBlock, replaceCommentsBlock } from '../imageMarkup/comments';
import type { CommentPin } from '../imageMarkup/model';

const pin = (n: number, text: string): CommentPin => ({ kind: 'pin', id: `p${n}`, n, x: 0, y: 0, text });

describe('formatCommentsBlock', () => {
  it('is empty when there are no pins or only blank ones', () => {
    expect(formatCommentsBlock('a.png', [])).toBe('');
    expect(formatCommentsBlock('a.png', [pin(1, ''), pin(2, '  \n ')])).toBe('');
  });

  it('formats a header and numbered lines', () => {
    expect(formatCommentsBlock('shot.png', [pin(1, 'Too big'), pin(2, 'Wrong colour')])).toBe(
      'Comments on shot.png (numbers match the pins on the image):\n1. Too big\n2. Wrong colour',
    );
  });

  it('sorts by number, skips blank pins but keeps numbers, trims and collapses newlines', () => {
    expect(formatCommentsBlock('a.png', [pin(3, ' third '), pin(2, ''), pin(1, 'first\nline two\n\n  three')])).toBe(
      'Comments on a.png (numbers match the pins on the image):\n1. first line two three\n3. third',
    );
  });
});

describe('replaceCommentsBlock', () => {
  const prev = 'Comments on a.png (numbers match the pins on the image):\n1. old';
  const next = 'Comments on a.png (numbers match the pins on the image):\n1. new';

  it('swaps the block in place, keeping surrounding text', () => {
    expect(replaceCommentsBlock(`Look at this\n\n${prev}\n\nthanks`, prev, next)).toBe(`Look at this\n\n${next}\n\nthanks`);
  });

  it('returns null when the previous block is empty or no longer verbatim in the draft', () => {
    expect(replaceCommentsBlock('hello', '', next)).toBeNull();
    expect(replaceCommentsBlock(`hi\n\n${prev} edited`.replace('1. old', '1. changed'), prev, next)).toBeNull();
    expect(replaceCommentsBlock('nothing here', prev, next)).toBeNull();
  });

  it('drops one trailing blank-line separator when removing', () => {
    expect(replaceCommentsBlock(`${prev}\n\nthanks`, prev, '')).toBe('thanks');
    expect(replaceCommentsBlock(`intro\n\n${prev}\n\nthanks`, prev, '')).toBe('intro\n\nthanks');
  });

  it('drops the leading separator when the block was last', () => {
    expect(replaceCommentsBlock(`intro\n\n${prev}`, prev, '')).toBe('intro');
  });

  it('removing a block that is the whole draft leaves an empty draft', () => {
    expect(replaceCommentsBlock(prev, prev, '')).toBe('');
  });
});

describe('insertCommentsBlock', () => {
  const block = 'Comments on a.png (numbers match the pins on the image):\n1. x';

  it('inserts into an empty draft without padding', () => {
    expect(insertCommentsBlock('', block)).toEqual({ text: block, cursor: block.length });
  });

  it('appends after existing text with a blank line', () => {
    const r = insertCommentsBlock('hello', block);
    expect(r.text).toBe(`hello\n\n${block}`);
    expect(r.cursor).toBe(r.text.length);
  });

  it('only tops up an existing newline', () => {
    expect(insertCommentsBlock('hello\n', block).text).toBe(`hello\n\n${block}`);
    expect(insertCommentsBlock('hello\n\n', block).text).toBe(`hello\n\n${block}`);
  });

  it('inserts at the cursor, separating from text on both sides', () => {
    const r = insertCommentsBlock('beforeafter', block, 6);
    expect(r.text).toBe(`before\n\n${block}\n\nafter`);
    expect(r.cursor).toBe(`before\n\n${block}`.length);
  });

  it('inserts at the start', () => {
    expect(insertCommentsBlock('tail', block, 0)).toEqual({ text: `${block}\n\ntail`, cursor: block.length });
  });

  it('clamps an out-of-range cursor', () => {
    expect(insertCommentsBlock('hi', block, 99).text).toBe(`hi\n\n${block}`);
    expect(insertCommentsBlock('hi', block, -4).text).toBe(`${block}\n\nhi`);
  });

  it('leaves the draft alone for an empty block', () => {
    expect(insertCommentsBlock('hi', '', 1)).toEqual({ text: 'hi', cursor: 1 });
  });
});
