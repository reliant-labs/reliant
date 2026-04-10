import { render, screen } from '@testing-library/react';
import { LoadToolRenderer } from './LoadToolRenderer';
import type { ToolRenderContext } from './types';

function createContext(overrides: Partial<ToolRenderContext> = {}): ToolRenderContext {
  return {
    toolName: 'load_tool',
    toolCallId: 'tool-1',
    input: { name: 'grep' },
    result: undefined,
    worktreeId: 'wt-1',
    isExpanded: true,
    isCompleted: true,
    isExecuting: false,
    isPreparing: false,
    hasFailed: false,
    ...overrides,
  };
}

describe('LoadToolRenderer', () => {
  it('renders tool name', () => {
    render(<LoadToolRenderer ctx={createContext({ input: { name: 'grep' } })} />);
    expect(screen.getByText('grep')).toBeInTheDocument();
  });

  it('accepts tool_name as alternative input key', () => {
    render(
      <LoadToolRenderer ctx={createContext({ input: { tool_name: 'shell' } })} />
    );
    expect(screen.getByText('shell')).toBeInTheDocument();
  });

  it('shows placeholder when no tool name', () => {
    render(<LoadToolRenderer ctx={createContext({ input: {} })} />);
    expect(screen.getByText('Loading tool...')).toBeInTheDocument();
  });

  it('renders "loaded" badge on successful load', () => {
    render(
      <LoadToolRenderer
        ctx={createContext({
          input: { name: 'grep' },
          result: { name: 'load_tool', content: 'ok', is_error: false },
        })}
      />
    );

    expect(screen.getByText('loaded')).toBeInTheDocument();
    expect(screen.queryByText('denied')).not.toBeInTheDocument();
    expect(screen.queryByText('failed')).not.toBeInTheDocument();
  });

  it('renders "denied" badge when result indicates permission denial', () => {
    render(
      <LoadToolRenderer
        ctx={createContext({
          input: { name: 'shell' },
          result: {
            name: 'load_tool',
            content: 'permission denied by policy',
            is_error: true,
          },
          hasFailed: true,
        })}
      />
    );

    expect(screen.getByText('denied')).toBeInTheDocument();
    // Reason is also surfaced
    expect(screen.getByText('permission denied by policy')).toBeInTheDocument();
  });

  it('renders "failed" badge for non-denial errors', () => {
    render(
      <LoadToolRenderer
        ctx={createContext({
          input: { name: 'shell' },
          result: {
            name: 'load_tool',
            content: 'unknown tool',
            is_error: true,
          },
          hasFailed: true,
        })}
      />
    );

    expect(screen.getByText('failed')).toBeInTheDocument();
    expect(screen.queryByText('denied')).not.toBeInTheDocument();
  });

  it('does not show a status badge when result is absent', () => {
    render(<LoadToolRenderer ctx={createContext({ input: { name: 'grep' } })} />);

    expect(screen.queryByText('loaded')).not.toBeInTheDocument();
    expect(screen.queryByText('denied')).not.toBeInTheDocument();
    expect(screen.queryByText('failed')).not.toBeInTheDocument();
  });

  it('handles missing input gracefully', () => {
    render(
      <LoadToolRenderer
        ctx={createContext({
          input: 'garbage' as unknown as Record<string, unknown>,
          result: { name: 'load_tool', content: 'ok', is_error: false },
        })}
      />
    );

    expect(screen.getByText('Loading tool...')).toBeInTheDocument();
    expect(screen.getByText('loaded')).toBeInTheDocument();
  });

  it('uses a compact inline layout (no large card)', () => {
    const { container } = render(
      <LoadToolRenderer
        ctx={createContext({
          input: { name: 'grep' },
          result: { name: 'load_tool', content: 'ok', is_error: false },
        })}
      />
    );

    // The wrapper is a single compact container with the tool-content-load-tool
    // class; failure/success body is at most one extra line. Ensure no nested
    // scrollable/body region is rendered.
    const wrapper = container.querySelector('.tool-content-load-tool');
    expect(wrapper).not.toBeNull();
    // Only the header row exists when successful — no error body.
    expect(wrapper?.querySelectorAll('.text-destructive\\/80').length).toBe(0);
  });
});
