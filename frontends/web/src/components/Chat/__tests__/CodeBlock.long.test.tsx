import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { CodeBlock, LONG_CODE_BLOCK_LINES } from '../CodeBlock';

const lines = (n: number) => Array.from({ length: n }, (_, i) => `const line${i} = ${i};`).join('\n');

// §6.1 of the chat design: a header with the language and copy, and a long
// block opens clipped behind "Show all N lines".
describe('code block', () => {
  it('names the language in its header', () => {
    render(<CodeBlock language="ts">{lines(3)}</CodeBlock>);
    expect(screen.getByText('ts')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy code' })).toBeInTheDocument();
  });

  it('shows a short block whole', () => {
    render(<CodeBlock language="ts">{lines(LONG_CODE_BLOCK_LINES)}</CodeBlock>);
    expect(screen.queryByRole('button', { name: /show all/i })).toBeNull();
  });

  it('clips a long block and opens it on request', () => {
    const { container } = render(<CodeBlock language="ts">{lines(87)}</CodeBlock>);
    const toggle = screen.getByRole('button', { name: 'Show all 87 lines' });
    expect(container.querySelector('pre')?.parentElement).toHaveClass('max-h-[20rem]', 'overflow-hidden');

    fireEvent.click(toggle);
    expect(screen.getByRole('button', { name: 'Show less' })).toHaveAttribute('aria-expanded', 'true');
    expect(container.querySelector('pre')?.parentElement).not.toHaveClass('max-h-[20rem]');
  });
});
