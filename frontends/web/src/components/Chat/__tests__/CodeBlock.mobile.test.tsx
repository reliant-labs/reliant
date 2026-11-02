import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { CodeBlock } from '../CodeBlock';
import { SurfaceProvider } from '../../../lib/surfaceContext';

describe('mobile code block', () => {
  it('keeps code locally scrollable and exposes a touch-sized copy control', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    const { container } = render(<SurfaceProvider surface="mobile"><CodeBlock language="ts">{'const longLine = 1;'}</CodeBlock></SurfaceProvider>);
    expect(container.querySelector('pre')).toHaveClass('overflow-x-auto', 'max-w-full');
    expect(container.querySelector('code')).toHaveClass('whitespace-pre');
    const copy = screen.getByRole('button', { name: 'Copy code' });
    expect(copy).toHaveClass('min-h-[44px]', 'min-w-[44px]');
    fireEvent.click(copy);
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('const longLine = 1;'));
  });
});
