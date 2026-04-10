import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { SkillToolRenderer } from './SkillToolRenderer';
import type { ToolRenderContext } from './types';

// Mock MarkdownRenderer to avoid pulling in heavy markdown deps.
const markdownRendererMock = vi.hoisted(() =>
  vi.fn(({ content }: { content: string }) => (
    <div data-testid="markdown-renderer">{content}</div>
  ))
);

vi.mock('../MarkdownRenderer', () => ({
  MarkdownRenderer: markdownRendererMock,
}));

function createContext(overrides: Partial<ToolRenderContext> = {}): ToolRenderContext {
  return {
    toolName: 'skill',
    toolCallId: 'tool-1',
    input: {},
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

describe('SkillToolRenderer', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe('load action', () => {
    it('renders skill name for load action', () => {
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'load', name: 'go/error-handling' },
          })}
        />
      );

      expect(screen.getByText('go/error-handling')).toBeInTheDocument();
    });

    it('renders "load" badge for load action', () => {
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'load', name: 'testing' },
          })}
        />
      );

      expect(screen.getByText('load')).toBeInTheDocument();
    });

    it('renders collapsible body that starts collapsed', () => {
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'load', name: 'testing' },
            result: {
              name: 'skill',
              content: '# Testing Skill\nSome body text',
              is_error: false,
            },
          })}
        />
      );

      // Toggle button is present
      expect(screen.getByText('Skill content')).toBeInTheDocument();
      // Body is not rendered initially (collapsed)
      expect(screen.queryByTestId('markdown-renderer')).not.toBeInTheDocument();
    });

    it('renders markdown body when expanded', async () => {
      const user = userEvent.setup();
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'load', name: 'testing' },
            result: {
              name: 'skill',
              content: '# Testing Skill\nSome body text',
              is_error: false,
            },
          })}
        />
      );

      await user.click(screen.getByText('Skill content'));

      const body = screen.getByTestId('markdown-renderer');
      expect(body).toBeInTheDocument();
      expect(body).toHaveTextContent('# Testing Skill Some body text');
      expect(markdownRendererMock).toHaveBeenCalledWith(
        expect.objectContaining({
          content: '# Testing Skill\nSome body text',
          worktreeId: 'wt-1',
        }),
        undefined
      );
    });

    it('shows placeholder when skill name is missing', () => {
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'load' },
          })}
        />
      );

      expect(screen.getByText('Loading skill...')).toBeInTheDocument();
    });

    it('accepts skill_name as alternative input key', () => {
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'load', skill_name: 'python/typing' },
          })}
        />
      );

      expect(screen.getByText('python/typing')).toBeInTheDocument();
    });
  });

  describe('list action', () => {
    it('renders "list" heading and skill list entries', () => {
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'list' },
            result: {
              name: 'skill',
              content: JSON.stringify(['go/error-handling', 'python/typing']),
              is_error: false,
            },
          })}
        />
      );

      expect(screen.getByText('Available Skills (2)')).toBeInTheDocument();
      expect(screen.getByText('go/error-handling')).toBeInTheDocument();
      expect(screen.getByText('python/typing')).toBeInTheDocument();
    });

    it('renders skill list with object entries', () => {
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'list' },
            result: {
              name: 'skill',
              content: JSON.stringify([
                { name: 'skill-one' },
                { title: 'skill-two' },
              ]),
              is_error: false,
            },
          })}
        />
      );

      expect(screen.getByText('skill-one')).toBeInTheDocument();
      expect(screen.getByText('skill-two')).toBeInTheDocument();
    });

    it('falls back to plain text when list result is not JSON', () => {
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'list' },
            result: {
              name: 'skill',
              content: 'plain text list',
              is_error: false,
            },
          })}
        />
      );

      expect(screen.getByText('plain text list')).toBeInTheDocument();
    });

    it('renders empty-state message for empty list', () => {
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'list' },
            result: {
              name: 'skill',
              content: '[]',
              is_error: false,
            },
          })}
        />
      );

      expect(screen.getByText('No skills available')).toBeInTheDocument();
    });
  });

  describe('search action', () => {
    it('renders search query and results', () => {
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'search', query: 'error handling' },
            result: {
              name: 'skill',
              content: 'Found: go/error-handling',
              is_error: false,
            },
          })}
        />
      );

      expect(screen.getByText(/error handling/)).toBeInTheDocument();
      expect(screen.getByText('Found: go/error-handling')).toBeInTheDocument();
    });

    it('renders nothing when search has no result yet', () => {
      const { container } = render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'search', query: 'error handling' },
          })}
        />
      );

      expect(container).toBeEmptyDOMElement();
    });
  });

  describe('edge cases', () => {
    it('handles missing input gracefully', () => {
      const { container } = render(
        <SkillToolRenderer ctx={createContext({ input: {} })} />
      );

      // No action → no specialized branch → no result → null render
      expect(container).toBeEmptyDOMElement();
    });

    it('handles string input gracefully (treated as empty object)', () => {
      const { container } = render(
        // Cast to match the union type — string inputs are rare but supported
        <SkillToolRenderer ctx={createContext({ input: 'raw' as unknown as Record<string, unknown> })} />
      );

      expect(container).toBeEmptyDOMElement();
    });

    it('renders fallback content when action is unknown but result is present', () => {
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'unknown' },
            result: {
              name: 'skill',
              content: 'fallback content',
              is_error: false,
            },
          })}
        />
      );

      expect(screen.getByText('fallback content')).toBeInTheDocument();
    });

    it('still renders load header when result has error flag', () => {
      // Even when the tool call errored, the renderer should still show
      // the skill name + load badge so the user sees what was attempted.
      render(
        <SkillToolRenderer
          ctx={createContext({
            input: { action: 'load', name: 'broken-skill' },
            result: {
              name: 'skill',
              content: 'skill not found',
              is_error: true,
            },
            hasFailed: true,
          })}
        />
      );

      expect(screen.getByText('broken-skill')).toBeInTheDocument();
      expect(screen.getByText('load')).toBeInTheDocument();
    });
  });
});
