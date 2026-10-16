import { useState } from "react";
import { useChatParamsStore } from "../../../store/chatParamsStore";
/** Machine/workspace routing and configuration forwarding from the shared composer. */

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

const startChat = vi.fn()
const selectChat = vi.fn()
const navigate = vi.fn()

const transferTempToChat = vi.fn();

// Test this screen's routing contract; the real composer is covered separately.
vi.mock('../../Chat/ChatInput', () => ({
  ChatInput: ({ onSend, disabled, mobileOptionsContent }: { onSend: (...args: any[]) => Promise<void>; disabled: boolean; mobileOptionsContent?: (onClose: () => void) => React.ReactNode }) => {
    const [showOptions, setShowOptions] = useState(false);
    const [message, setMessage] = useState('');
    const workflow = useChatParamsStore(state => state.tempNewChatWorkflow) ?? 'builtin://forge-one-shot';
    const setWorkflow = useChatParamsStore.getState().setTempNewChatWorkflow;
    const [configured, setConfigured] = useState(false);
    return <>
      <button onClick={() => setShowOptions(true)}>Chat options</button>
      {showOptions && mobileOptionsContent?.(() => setShowOptions(false))}
      <span>Forge One Shot</span>
      <label>Workflow<select aria-label="Composer workflow" value={workflow} onChange={event => setWorkflow(event.target.value)}><option value="builtin://forge-one-shot">Forge</option><option value="builtin://agent">Agent</option></select></label>
      <button onClick={() => setConfigured(true)}>Configure attachments and parameters</button>
      <textarea aria-label="Message" value={message} onChange={event => setMessage(event.target.value)} />
      <button aria-label="Send" disabled={disabled || (!message.trim() && !configured)} onClick={async () => {
        try {
          await onSend(message, configured ? ['attachment-1'] : undefined, workflow,
            configured ? { model: { id: 'model-1' }, 'agent.temperature': 0.4 } : undefined, undefined,
            configured ? { agent: 'coding', default: null } : undefined);
          setMessage('');
        } catch { /* The real composer retains the draft on failure too. */ }
      }}>Send</button>
    </>;
  },
}));

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, ...props }: { children?: React.ReactNode }) => (
    <a {...props}>{children}</a>
  ),
  useNavigate: () => navigate,
  // No `worktreeId` — exercises the default (main worktree) path. The
  // group-header "new chat in this workspace" override is covered in
  // MobileChatList's own tests.
  useSearch: () => ({}),
}))

vi.mock('../../../store/chatStore', () => ({
  useChatStore: Object.assign(vi.fn(), {
    getState: () => ({ startChat, selectChat }),
  }),
}))

vi.mock('../../../store/projectStore', () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector({ currentProject: { id: 'p1', name: 'reliant' } }),
}))

// WorktreeStatus: ACTIVE = 1, CREATING = 5, FAILED = 6.
const worktreeList = vi.hoisted(() => ({
  current: [] as Array<Record<string, unknown>>,
}))
const MAIN_WORKTREE = { id: 'wt-main', is_main: true, name: 'main', branch: 'main', status: 1 }
vi.mock('../../../store/worktreeStore', () => ({
  useWorktreeStore: (selector: (s: unknown) => unknown) =>
    selector({ worktrees: worktreeList.current, loadWorktrees: vi.fn() }),
}))

// The sheet has its own tests; here it only needs to report a creation.
vi.mock('../MobileCreateWorkspaceSheet', () => ({
  MobileCreateWorkspaceSheet: ({ onCreated }: { onCreated: (w: { id: string }) => void }) => (
    <div role="dialog" aria-label="New workspace">
      <button
        type="button"
        onClick={() => {
          worktreeList.current = [
            ...worktreeList.current,
            { id: 'wt-new', is_main: false, name: 'fresh', branch: 'fresh', status: 5 },
          ]
          onCreated({ id: 'wt-new' })
        }}
      >
        Create stub
      </button>
    </div>
  ),
}))

vi.mock('../../../store/globalDataStore', () => ({
  useWorkflows: () => ({
    workflows: [
      { name: 'builtin://agent', description: 'Basic agentic chat' },
      { name: 'builtin://forge-one-shot', description: 'Build with Forge' },
      // Its Chat trigger is off: a chat cannot start it.
      { name: 'nightly-digest', description: 'Runs on a schedule', automation_only: true },
    ],
    loading: false,
  }),
}))

vi.mock('../../../store/preferencesStore', () => ({
  DEFAULT_WORKFLOW: 'builtin://agent',
  usePreferencesStore: (selector: (s: unknown) => unknown) =>
    selector({
      preferences: { defaultWorkflow: 'builtin://forge-one-shot' },
      isLoading: false,
      loadPreferences: vi.fn(),
      isWorkflowHidden: () => false,
    }),
}))

vi.mock('../../../lib/analytics', () => ({ trackEvent: vi.fn() }))

// DaemonStatus: ACTIVE = 1, SUSPENDED = 5.
const daemonList = vi.hoisted(() => ({
  current: [{ daemonId: 'd-laptop', hostname: 'laptop', status: 1 }] as Array<{
    daemonId: string
    hostname: string
    status: number
  }>,
}))
vi.mock('@/hooks/useOnboardingQueries', () => ({
  useDaemonList: () => ({ data: daemonList.current, isLoading: false }),
}))

const { MobileNewChat } = await import('../MobileNewChat')

beforeEach(() => {
  useChatParamsStore.setState({ tempNewChatWorkflow: null, tempNewChatParams: {}, tempNewChatPresets: {}, transferTempToChat });
  worktreeList.current = [MAIN_WORKTREE]
  daemonList.current = [{ daemonId: 'd-laptop', hostname: 'laptop', status: 1 }]
  startChat.mockReset()
  startChat.mockResolvedValue({ id: 'chat-9' })
  selectChat.mockReset()
  navigate.mockReset()
})

async function renderNewChat() {
  render(<MobileNewChat />);
}

describe('MobileNewChat', () => {
  it('shows the current project as context', async () => {
    await renderNewChat()
    expect(screen.getByText('reliant')).toBeInTheDocument()
  })


  it('creates the chat against the main worktree and navigates to it', async () => {
    await renderNewChat()

    await userEvent.type(screen.getByLabelText('Message'), 'ship it')
    await userEvent.click(screen.getByLabelText('Send'))

    await waitFor(() => expect(startChat).toHaveBeenCalled())
    const [worktreeId, content, attachments, params, workflow] =
      startChat.mock.calls[0]
    expect(worktreeId).toBe('wt-main')
    expect(content).toBe('ship it')
    // Untouched composer configuration stays absent over the wire.
    expect(attachments).toBeUndefined()
    expect(params).toBeUndefined()
    expect(workflow).toBe('builtin://forge-one-shot')

    await waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({
        to: '/m/chats/$chatId',
        params: { chatId: 'chat-9' },
      }),
    )
  })

  it('sends the workflow the user picked over their default', async () => {
    await renderNewChat()

    // Open the workflow picker sheet and choose a different workflow.
    await userEvent.selectOptions(screen.getByLabelText('Composer workflow'), 'builtin://agent')

    await userEvent.type(screen.getByLabelText('Message'), 'hello')
    await userEvent.click(screen.getByLabelText('Send'))

    await waitFor(() => expect(startChat).toHaveBeenCalled())
    expect(startChat.mock.calls[0][4]).toBe('builtin://agent')
  })


  it('will not send an empty message', async () => {
    await renderNewChat()
    expect(screen.getByLabelText('Send')).toBeDisabled()

    await userEvent.type(screen.getByLabelText('Message'), '   ')
    expect(screen.getByLabelText('Send')).toBeDisabled()
    expect(startChat).not.toHaveBeenCalled()
  })

  it('keeps the typed message when creation fails', async () => {
    startChat.mockRejectedValue(new Error('no daemon available'))
    await renderNewChat()

    const input = screen.getByLabelText('Message')
    await userEvent.type(input, 'retry me')
    await userEvent.click(screen.getByLabelText('Send'))

    // Retyping a prompt on a phone is the worst possible recovery.
    expect(await screen.findByText('no daemon available')).toBeInTheDocument()
    expect(input).toHaveValue('retry me')
    expect(navigate).not.toHaveBeenCalled()
  })

  it('forwards attachments, model settings, grouped parameters and presets from the composer', async () => {
    await renderNewChat();
    await userEvent.click(screen.getByText('Configure attachments and parameters'));
    await userEvent.click(screen.getByLabelText('Send'));
    await waitFor(() => expect(startChat).toHaveBeenCalled());
    expect(startChat.mock.calls[0].slice(1, 6)).toEqual([
      '', ['attachment-1'], { model: { id: 'model-1' }, 'agent.temperature': 0.4 },
      'builtin://forge-one-shot', { agent: 'coding' },
    ]);
    expect(transferTempToChat).toHaveBeenCalledWith('chat-9');
  });

  it('starts the chat in the workspace the user picks', async () => {
    worktreeList.current = [
      MAIN_WORKTREE,
      { id: 'wt-feature', is_main: false, name: 'feature-x', branch: 'feature/x', status: 1 },
      // Never got a checkout, so a chat cannot run in it.
      { id: 'wt-failed', is_main: false, name: 'broken', branch: 'broken', status: 6 },
    ]
    await renderNewChat()

    await userEvent.click(screen.getByText('Workspace'))
    expect(screen.queryByRole('button', { name: /^broken/ })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /^feature-x/ }))

    await userEvent.type(screen.getByLabelText('Message'), 'on the branch')
    await userEvent.click(screen.getByLabelText('Send'))
    await waitFor(() => expect(startChat).toHaveBeenCalled())
    expect(startChat.mock.calls[0][0]).toBe('wt-feature')
  })

  it('creates a workspace from the picker and selects it', async () => {
    await renderNewChat()

    await userEvent.click(screen.getByText('Workspace'))
    await userEvent.click(screen.getByRole('button', { name: 'New workspace' }))
    await userEvent.click(screen.getByRole('button', { name: 'Create stub' }))

    expect(screen.queryByRole('dialog', { name: 'New workspace' })).not.toBeInTheDocument()
      // Still CREATING: it is offered, labelled as such, and selected.
    expect(screen.getByText('fresh')).toBeInTheDocument()

    await userEvent.type(screen.getByLabelText('Message'), 'go')
    await userEvent.click(screen.getByLabelText('Send'))
    await waitFor(() => expect(startChat).toHaveBeenCalled())
    expect(startChat.mock.calls[0][0]).toBe('wt-new')
  })

  it('hides the workspace row for a chat with no machine', async () => {
    daemonList.current = [{ daemonId: 'd-laptop', hostname: 'laptop', status: 5 }]
    await renderNewChat()
    expect(screen.getByText('No machine')).toBeInTheDocument()
    expect(screen.queryByText('Workspace')).not.toBeInTheDocument()
  })

  it('runs on the awake machine by default, sending no daemon', async () => {
    await renderNewChat()
    expect(screen.getByText('Runs on')).toBeInTheDocument()
    expect(screen.getByText('laptop')).toBeInTheDocument()

    await userEvent.type(screen.getByLabelText('Message'), 'hi')
    await userEvent.click(screen.getByLabelText('Send'))

    await waitFor(() => expect(startChat).toHaveBeenCalled())
    // No seventh argument: default resolution, exactly as before.
    expect(startChat.mock.calls[0][6]).toBeUndefined()
  })

  it('preselects No machine when none of the machines is awake', async () => {
    daemonList.current = [{ daemonId: 'd-laptop', hostname: 'laptop', status: 5 }]
    await renderNewChat()
    expect(screen.getByText('No machine')).toBeInTheDocument()

    await userEvent.type(screen.getByLabelText('Message'), 'read the news')
    await userEvent.click(screen.getByLabelText('Send'))

    await waitFor(() => expect(startChat).toHaveBeenCalled())
    expect(startChat.mock.calls[0][0]).toBe('wt-main')
    expect(startChat.mock.calls[0][6]).toEqual({ daemonId: undefined, noMachine: true })
  })

  it('wakes an asleep machine the user picks', async () => {
    daemonList.current = [{ daemonId: 'd-laptop', hostname: 'laptop', status: 5 }]
    await renderNewChat()

    await userEvent.click(screen.getByText('Runs on'))
    await userEvent.click(screen.getByRole('button', { name: /^laptop/ }))
    await userEvent.type(screen.getByLabelText('Message'), 'fix the build')
    await userEvent.click(screen.getByLabelText('Send'))

    await waitFor(() => expect(startChat).toHaveBeenCalled())
    // A chosen daemon is woken by the server at send.
    expect(startChat.mock.calls[0][6]).toEqual({ daemonId: 'd-laptop', noMachine: undefined })
  })
})

it('shows Start with choices directly above the compact composer', () => {
  render(<MobileNewChat />);
  expect(screen.getByText('Start with')).toBeInTheDocument();
  expect(screen.getByText('Runs on')).toBeInTheDocument();
  expect(screen.getByText('Workspace')).toBeInTheDocument();
  expect(screen.queryByRole('heading', { name: 'How can I help?' })).not.toBeInTheDocument();
});

it('shares the Start with workflow selection with the composer and clears previous configuration', async () => {
  useChatParamsStore.setState({ tempNewChatParams: { old: true }, tempNewChatPresets: { coder: 'old' } });
  render(<MobileNewChat />);
  await userEvent.click(screen.getByRole('button', { name: /^Workflow/ }));
  expect(screen.queryByRole('button', { name: /Nightly Digest/ })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole('button', { name: /^Agent/ }));
  expect(useChatParamsStore.getState().tempNewChatWorkflow).toBe('builtin://agent');
  expect(useChatParamsStore.getState().tempNewChatParams).toEqual({});
  expect(useChatParamsStore.getState().tempNewChatPresets).toEqual({});
  await userEvent.type(screen.getByLabelText('Message'), 'hello');
  await userEvent.click(screen.getByLabelText('Send'));
  await waitFor(() => expect(startChat).toHaveBeenCalled());
  expect(startChat.mock.calls[0][4]).toBe('builtin://agent');
});
