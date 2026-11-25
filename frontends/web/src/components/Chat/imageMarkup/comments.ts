/**
 * The comments block: numbered pin comments written into the draft message so
 * the model reads them next to the marked-up image. Pure string helpers.
 */
import type { CommentPin } from './model';

/** '' when no pin has text; blank pins are skipped but keep their numbers. */
export function formatCommentsBlock(filename: string, pins: CommentPin[]): string {
  const lines = [...pins]
    .sort((a, b) => a.n - b.n)
    .map((p) => ({ n: p.n, text: p.text.replace(/\s*\n\s*/g, ' ').trim() }))
    .filter((p) => p.text !== '')
    .map((p) => `${p.n}. ${p.text}`);
  if (lines.length === 0) return '';
  return `Comments on ${filename} (numbers match the pins on the image):\n${lines.join('\n')}`;
}

/**
 * Swap an earlier block for the new one. Null when the earlier block is gone
 * from the draft (the user edited it), so the caller can insert instead.
 */
export function replaceCommentsBlock(draft: string, previousBlock: string, nextBlock: string): string | null {
  if (previousBlock === '') return null;
  const at = draft.indexOf(previousBlock);
  if (at < 0) return null;
  const before = draft.slice(0, at);
  const after = draft.slice(at + previousBlock.length);
  if (nextBlock !== '') return before + nextBlock + after;
  // Removing the block: drop one adjacent blank-line separator so no gap is left behind.
  if (after.startsWith('\n\n')) return before + after.slice(2);
  if (before.endsWith('\n\n')) return before.slice(0, -2) + after;
  return before + after.replace(/^\n/, '');
}

/** Insert `block` at `cursor` (default: end), set off from surrounding text by a blank line. */
export function insertCommentsBlock(draft: string, block: string, cursor?: number): { text: string; cursor: number } {
  if (block === '') return { text: draft, cursor: cursor ?? draft.length };
  const at = Math.max(0, Math.min(cursor ?? draft.length, draft.length));
  const before = draft.slice(0, at);
  const after = draft.slice(at);
  const lead = before === '' ? '' : before.endsWith('\n\n') ? '' : before.endsWith('\n') ? '\n' : '\n\n';
  const trail = after === '' ? '' : after.startsWith('\n\n') ? '' : after.startsWith('\n') ? '\n' : '\n\n';
  const text = before + lead + block + trail + after;
  return { text, cursor: before.length + lead.length + block.length };
}
