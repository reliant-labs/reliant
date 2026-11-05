/**
 * The new-chat screen — what it sends, and what it refuses to show.
 *
 * The scope assertions matter as much as the happy path: `chatAttachments`
 * and `chatWorkflowParams` are false for this surface, and the way that
 * regresses is someone porting one more control over from the desktop
 * composer "since it was right there". Where the chat runs is the one
 * deliberate exception (`chatDaemonSelection`, research/NO_MACHINE_CHATS.md
 * §2.5): a phone often has no machine awake, and No machine is the default
 * then.
 */

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

const composerPayload = vi.hoisted(() => ({ attachments: undefined as string[] | undefined, params: undefined as Record<string, unknown> | undefined, presets: undefined as Record<string, string> | undefined }));
// This suite tests the route/startChat seam; the real composer's configuration
// and upload behavior have focused ChatInput tests.
vi.mock('../../Chat/ChatInput', async () => {
  const { useState } = await import('react');
  return { ChatInput: ({ onSend, disabled }: { onSend: (text: string, attachments?: string[], workflow?: string, params?: Record<string, unknown>, thread?: undefined, presets?: Record<string, string>) => Promise<void>; disabled: boolean }) => {
    const [text, setText] = useState('');
    return <><textarea aria-label="Message" value={text} onChange={(e) => setText(e.target.value)} /><button aria-label="Send" disabled={disabled || !text.trim()} onClick={() => { void onSend(text, composerPayload.attachments, undefined, composerPayload.params, undefined, composerPayload.presets).catch(() => {}); }}>Send</button></>;
  }};
});
const startChat = vi.fn()
const selectChat = vi.fn()
const navigate = vi.fn()

// Where the project is installed decides which machine a new chat names
// (lib/chatMachine defaultMachineDaemon); these suites are not about that.
const installList = vi.hoisted(() => ({
  current: [] as Array<{ project_id: string; daemon_id: string; install_state: number }>,
}));
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  useProjectDaemonInstalls: () => ({ data: installList.current, isLoading: false }),
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

vi.mock('../../../store/projectStore', () => {
  const state = { currentProject: { id: 'p1', name: 'reliant' } }
  return {
    useProjectStore: Object.assign((selector: (s: unknown) => unknown) => selector(state), {
      getState: () => state,
    }),
  }
})

// WorktreeStatus: ACTIVE = 1, CREATING = 5, FAILED = 6.
const worktreeList = vi.hoisted(() => ({
  current: [] as Array<Record<string, unknown>>,
}))
const MAIN_WORKTREE = { id: 'wt-main', is_main: true, name: 'main', branch: 'main', status: 1 }
const worktreePaging = vi.hoisted(() => ({ hasMore: false, loadMore: () => {} }))
vi.mock('../../../hooks/worktree-queries', () => ({
  useWorktreeList: () => ({
    worktrees: worktreeList.current,
    hasMore: worktreePaging.hasMore,
    isLoadingMore: false,
    loadMore: worktreePaging.loadMore,
  }),
}))

// The surface's real capabilities, with per-test overrides.
const capabilityOverrides = vi.hoisted(() => ({ current: {} as Record<string, boolean> }))
vi.mock('@/lib/surfaceContext', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/surfaceContext')>()
  return {
    ...actual,
    useCapability: (name: Parameters<typeof actual.useCapability>[0]) =>
      capabilityOverrides.current[name] ?? actual.useCapability(name),
  }
})

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

const workflowCatalog = vi.hoisted(() => ({ bareNames: false }));
vi.mock("../../../hooks/workflow-queries", async (importActual) => ({
  ...(await importActual<typeof import("../../../hooks/workflow-queries")>()),
  useWorkflows: () => ({
    workflows: [
      { name: workflowCatalog.bareNames ? 'agent' : 'builtin://agent', description: 'Basic agentic chat' },
      { name: 'builtin://forge-one-shot', description: 'Build with Forge' },
      // Its Chat trigger is off: a chat cannot start it.
      { name: 'nightly-digest', description: 'Runs on a schedule', automation_only: true },
    ],
    loading: false,
  }),
}))

const hidden = vi.hoisted(() => ({ isWorkflowHidden: () => false, isPresetHidden: () => false }))
vi.mock('../../../hooks/settings-queries', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../../hooks/settings-queries')>()),
  useDefaultWorkflow: () => 'builtin://forge-one-shot',
  useHiddenItems: () => hidden,
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
const { ComposeModeProvider } = await import('@/lib/composeMode')

const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString()
// Main plus five branches, most recent first: b1 (1h) … b5 (5h).
const SIX_WORKTREES = [
  MAIN_WORKTREE,
  ...[3, 1, 5, 2, 4].map((h) => ({ id: `wt-b${h}`, is_main: false, name: `b${h}`, branch: `b${h}`, status: 1, last_active: hoursAgo(h) })),
]

beforeEach(async () => {
  workflowCatalog.bareNames = false;
  composerPayload.attachments = undefined;
  composerPayload.params = undefined;
  composerPayload.presets = undefined;
  const { useChatParamsStore } = await import('../../../store/chatParamsStore');
  useChatParamsStore.getState().setTempNewChatWorkflow(null);
  worktreeList.current = [MAIN_WORKTREE]
  worktreePaging.hasMore = false
  worktreePaging.loadMore = vi.fn()
  capabilityOverrides.current = {}
  installList.current = []
  daemonList.current = [{ daemonId: 'd-laptop', hostname: 'laptop', status: 1 }]
  startChat.mockReset()
  startChat.mockResolvedValue({ id: 'chat-9' })
  selectChat.mockReset()
  navigate.mockReset()
})

describe('MobileNewChat', () => {
  it('forwards shared composer uploads and workflow configuration', async () => {
    composerPayload.attachments = ['upload-1'];
    composerPayload.params = { goal: 'ship', agent: { mode: 'auto' } };
    composerPayload.presets = { agent: 'careful' };
    render(<MobileNewChat />);
    await userEvent.type(screen.getByLabelText('Message'), 'go');
    await userEvent.click(screen.getByLabelText('Send'));
    expect(startChat.mock.calls[0].slice(2, 6)).toEqual([composerPayload.attachments, composerPayload.params, 'builtin://forge-one-shot', composerPayload.presets]);
  });
  it('offers the created chat after navigation fails without enabling another creation', async () => {
    navigate.mockRejectedValueOnce(new Error('navigation failed'));
    render(<MobileNewChat />);
    await userEvent.type(screen.getByLabelText('Message'), 'go');
    await userEvent.click(screen.getByLabelText('Send'));
    await screen.findByText('Chat created. Open it to continue.');
    expect(screen.getByLabelText('Send')).toBeDisabled();
    expect(screen.getByText('Open created chat')).toBeInTheDocument();
    expect(startChat).toHaveBeenCalledTimes(1);
  });
  it('resolves a URI selection against bare catalog names without exposing the URI', async () => {
    workflowCatalog.bareNames = true;
    const { useChatParamsStore } = await import('../../../store/chatParamsStore');
    useChatParamsStore.getState().setTempNewChatWorkflow('builtin://agent');
    render(<MobileNewChat />);
    expect(screen.getByRole('button', { name: 'Workflow Agent' })).toBeInTheDocument();
    expect(screen.queryByText('builtin://agent')).not.toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('Message'), 'go');
    await userEvent.click(screen.getByLabelText('Send'));
    expect(startChat.mock.calls[0][4]).toBe('builtin://agent');
  });

  it('shows the current project as context', () => {
    render(<MobileNewChat />)
    expect(screen.getByText('reliant')).toBeInTheDocument()
  })

  it("preselects the user's default workflow, not the hardcoded fallback", () => {
    render(<MobileNewChat />)
    expect(screen.getByText('Forge One Shot')).toBeInTheDocument()
  })

  it('creates the chat against the main worktree and navigates to it', async () => {
    render(<MobileNewChat />)

    await userEvent.type(screen.getByLabelText('Message'), 'ship it')
    await userEvent.click(screen.getByLabelText('Send'))

    await waitFor(() => expect(startChat).toHaveBeenCalled())
    const [worktreeId, content, attachments, params, workflow] =
      startChat.mock.calls[0]
    expect(worktreeId).toBe('wt-main')
    expect(content).toBe('ship it')
    // Attachments and workflow params are out of scope on this surface —
    // they must go over the wire as absent, not as empty containers.
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
    render(<MobileNewChat />)

    // Open the workflow picker sheet and choose a different workflow.
    await userEvent.click(screen.getByText('Workflow'))
    await userEvent.click(screen.getByRole('button', { name: /^Agent/ }))

    await userEvent.type(screen.getByLabelText('Message'), 'hello')
    await userEvent.click(screen.getByLabelText('Send'))

    await waitFor(() => expect(startChat).toHaveBeenCalled())
    expect(startChat.mock.calls[0][4]).toBe('builtin://agent')
  })

  it('does not offer a workflow whose Chat trigger is off', async () => {
    render(<MobileNewChat />)
    await userEvent.click(screen.getByText('Workflow'))
    expect(screen.getByRole('button', { name: /^Agent/ })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Nightly Digest/i })).not.toBeInTheDocument()
  })

  it('will not send an empty message', async () => {
    render(<MobileNewChat />)
    expect(screen.getByLabelText('Send')).toBeDisabled()

    await userEvent.type(screen.getByLabelText('Message'), '   ')
    expect(screen.getByLabelText('Send')).toBeDisabled()
    expect(startChat).not.toHaveBeenCalled()
  })

  it('keeps the typed message when creation fails', async () => {
    startChat.mockRejectedValue(new Error('no daemon available'))
    render(<MobileNewChat />)

    const input = screen.getByLabelText('Message')
    await userEvent.type(input, 'retry me')
    await userEvent.click(screen.getByLabelText('Send'))

    // Retyping a prompt on a phone is the worst possible recovery.
    expect(await screen.findByText('no daemon available')).toBeInTheDocument()
    expect(input).toHaveValue('retry me')
    expect(navigate).not.toHaveBeenCalled()
  })

  it('offers no attachment or params controls', () => {
    render(<MobileNewChat />)
    expect(screen.queryByLabelText(/attach/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/parameters/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/daemon/i)).not.toBeInTheDocument()
  })

  it('starts the chat in the workspace the user picks', async () => {
    worktreeList.current = [
      MAIN_WORKTREE,
      { id: 'wt-feature', is_main: false, name: 'feature-x', branch: 'feature/x', status: 1 },
      // Never got a checkout, so a chat cannot run in it.
      { id: 'wt-failed', is_main: false, name: 'broken', branch: 'broken', status: 6 },
    ]
    render(<MobileNewChat />)

    // One tap on the canvas, no sheet.
    expect(screen.getByRole('radio', { name: /^main/ })).toBeChecked()
    expect(screen.queryByRole('radio', { name: /^broken/ })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('radio', { name: /^feature-x/ }))
    expect(screen.getByRole('radio', { name: /^feature-x/ })).toBeChecked()
    expect(screen.getByRole('radio', { name: /^main/ })).not.toBeChecked()

    await userEvent.type(screen.getByLabelText('Message'), 'on the branch')
    await userEvent.click(screen.getByLabelText('Send'))
    await waitFor(() => expect(startChat).toHaveBeenCalled())
    expect(startChat.mock.calls[0][0]).toBe('wt-feature')
  })

  it('creates a workspace from the canvas and selects it', async () => {
    render(<MobileNewChat />)

    await userEvent.click(screen.getByRole('button', { name: 'New workspace' }))
    await userEvent.click(screen.getByRole('button', { name: 'Create stub' }))

    expect(screen.queryByRole('dialog', { name: 'New workspace' })).not.toBeInTheDocument()
    // Still CREATING: it is offered, labelled as such, and selected.
    expect(screen.getByRole('radio', { name: /^fresh.*Setting up/ })).toBeChecked()

    await userEvent.type(screen.getByLabelText('Message'), 'go')
    await userEvent.click(screen.getByLabelText('Send'))
    await waitFor(() => expect(startChat).toHaveBeenCalled())
    expect(startChat.mock.calls[0][0]).toBe('wt-new')
  })

  it('hides the workspace row for a chat with no machine', () => {
    daemonList.current = [{ daemonId: 'd-laptop', hostname: 'laptop', status: 5 }]
    render(<MobileNewChat />)
    expect(screen.getByText('No machine')).toBeInTheDocument()
    expect(screen.queryByText('Workspace')).not.toBeInTheDocument()
  })

  it('runs on the awake machine by default, sending no daemon', async () => {
    render(<MobileNewChat />)
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
    render(<MobileNewChat />)
    expect(screen.getByText('No machine')).toBeInTheDocument()

    await userEvent.type(screen.getByLabelText('Message'), 'read the news')
    await userEvent.click(screen.getByLabelText('Send'))

    await waitFor(() => expect(startChat).toHaveBeenCalled())
    expect(startChat.mock.calls[0][0]).toBe('wt-main')
    expect(startChat.mock.calls[0][6]).toEqual({ daemonId: undefined, noMachine: true })
  })

  it('wakes an asleep machine the user picks', async () => {
    daemonList.current = [{ daemonId: 'd-laptop', hostname: 'laptop', status: 5 }]
    render(<MobileNewChat />)

    await userEvent.click(screen.getByText('Runs on'))
    await userEvent.click(screen.getByRole('button', { name: /^laptop/ }))
    await userEvent.type(screen.getByLabelText('Message'), 'fix the build')
    await userEvent.click(screen.getByLabelText('Send'))

    await waitFor(() => expect(startChat).toHaveBeenCalled())
    // A chosen daemon is woken by the server at send.
    expect(startChat.mock.calls[0][6]).toEqual({ daemonId: 'd-laptop', noMachine: undefined })
  })

  // ProjectInstallState: INSTALLED = 1.
  it('will not start on a machine the project is not set up on, and keeps the draft', async () => {
    daemonList.current = [
      { daemonId: 'd-laptop', hostname: 'laptop', status: 1 },
      { daemonId: 'd-desk', hostname: 'desk', status: 1 },
    ]
    installList.current = [{ project_id: 'p1', daemon_id: 'd-desk', install_state: 1 }]
    render(<MobileNewChat />)

    await userEvent.click(screen.getByText('Runs on'))
    await userEvent.click(screen.getByRole('button', { name: /^laptop/ }))
    expect(screen.getByTestId('mobile-new-chat-placement')).toHaveTextContent("reliant isn't set up on laptop")

    await userEvent.type(screen.getByLabelText('Message'), 'fix the build')
    expect(screen.getByLabelText('Message')).toHaveValue('fix the build')
    expect(screen.getByLabelText('Send')).toBeDisabled()
    expect(startChat).not.toHaveBeenCalled()
  })

  it("offers only the picked machine's workspaces and runs a branch workspace on its own machine", async () => {
    daemonList.current = [
      { daemonId: 'd-laptop', hostname: 'laptop', status: 1 },
      { daemonId: 'd-desk', hostname: 'desk', status: 1 },
    ]
    worktreeList.current = [
      MAIN_WORKTREE,
      { id: 'wt-desk', is_main: false, name: 'desk-branch', branch: 'desk', status: 1, daemon_id: 'd-desk' },
      { id: 'wt-laptop', is_main: false, name: 'laptop-branch', branch: 'laptop', status: 1, daemon_id: 'd-laptop' },
    ]
    render(<MobileNewChat />)

    await userEvent.click(screen.getByText('Runs on'))
    await userEvent.click(screen.getByRole('button', { name: /^laptop/ }))
    expect(screen.queryByRole('radio', { name: /^desk-branch/ })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('radio', { name: /^laptop-branch/ }))

    await userEvent.type(screen.getByLabelText('Message'), 'on the branch')
    await userEvent.click(screen.getByLabelText('Send'))
    await waitFor(() => expect(startChat).toHaveBeenCalled())
    expect(startChat.mock.calls[0][0]).toBe('wt-laptop')
    // The workspace decides the machine; the server resolves it from the checkout.
    expect(startChat.mock.calls[0][6]).toBeUndefined()
  })
  it('lists main first, then the most recent workspaces, with Show all for the rest', async () => {
    worktreeList.current = SIX_WORKTREES
    render(<MobileNewChat />)

    const radios = screen.getAllByRole('radio')
    expect(radios.map((r) => r.textContent)).toEqual(['maindefault', 'b11h ago', 'b22h ago', 'b33h ago'])
    await userEvent.click(screen.getByRole('button', { name: 'Show all (6)' }))
    // The full list, in a sheet.
    expect(screen.getByRole('button', { name: /^b5/ })).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /^b5/ }))

    // Picked from the sheet: it takes the last slot, checked, so the choice stays visible.
    expect(screen.getByRole('radio', { name: /^b5/ })).toBeChecked()
    expect(screen.queryByRole('radio', { name: /^b3/ })).not.toBeInTheDocument()

    await userEvent.type(screen.getByLabelText('Message'), 'deep in the list')
    await userEvent.click(screen.getByLabelText('Send'))
    await waitFor(() => expect(startChat).toHaveBeenCalled())
    expect(startChat.mock.calls[0][0]).toBe('wt-b5')
  })

  it('pages the Show all sheet when the server has more workspaces', async () => {
    worktreePaging.hasMore = true
    worktreeList.current = [MAIN_WORKTREE, { id: 'wt-b1', is_main: false, name: 'b1', branch: 'b1', status: 1 }]
    render(<MobileNewChat />)

    await userEvent.click(screen.getByRole('button', { name: 'Show all (2+)' }))
    await userEvent.click(screen.getByRole('button', { name: 'Load more' }))
    expect(worktreePaging.loadMore).toHaveBeenCalled()
  })

  it('has no Show all when every workspace is already listed', () => {
    worktreeList.current = [MAIN_WORKTREE, { id: 'wt-b1', is_main: false, name: 'b1', branch: 'b1', status: 1 }]
    render(<MobileNewChat />)
    expect(screen.queryByRole('button', { name: /Show all/ })).not.toBeInTheDocument()
  })

  it('shows main as a single read-only line when it is the only workspace and none can be created', () => {
    capabilityOverrides.current = { worktreeCreate: false }
    render(<MobileNewChat />)
    expect(screen.getByText('Workspace')).toBeInTheDocument()
    expect(screen.getByText('main')).toBeInTheDocument()
    expect(screen.queryByRole('radio')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'New workspace' })).not.toBeInTheDocument()
  })

  it('offers main as a choice when a workspace can be created', () => {
    render(<MobileNewChat />)
    expect(screen.getByRole('radio', { name: /^main/ })).toBeChecked()
    expect(screen.getByRole('button', { name: 'New workspace' })).toBeInTheDocument()
  })

  it('hides Runs on without machine selection on this surface', () => {
    capabilityOverrides.current = { chatDaemonSelection: false }
    render(<MobileNewChat />)
    expect(screen.queryByText('Runs on')).not.toBeInTheDocument()
    expect(screen.getByText('Workflow')).toBeInTheDocument()
  })

  it('renders no suggested prompts and no model control', () => {
    render(<MobileNewChat />)
    expect(screen.queryByText(/start with/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/try asking|suggest/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/^model$/i)).not.toBeInTheDocument()
  })

  it('has no summary line while the keyboard is down', () => {
    render(<MobileNewChat />)
    expect(screen.queryByTestId('mobile-new-chat-summary')).not.toBeInTheDocument()
  })

  it('keeps the choice visible as a summary line while composing, and tapping it brings the options back', async () => {
    worktreeList.current = [MAIN_WORKTREE, { id: 'wt-feature', is_main: false, name: 'feature-x', branch: 'feature/x', status: 1 }]
    render(
      <ComposeModeProvider keyboardOpen>
        <MobileNewChat />
      </ComposeModeProvider>,
    )
    const summary = screen.getByTestId('mobile-new-chat-summary')
    expect(summary).toHaveTextContent('main · laptop · Forge One Shot')

    await userEvent.click(screen.getByRole('radio', { name: /^feature-x/ }))
    expect(summary).toHaveTextContent('feature-x · laptop · Forge One Shot')

    const input = screen.getByLabelText('Message')
    input.focus()
    const canvas = screen.getByRole('radiogroup', { name: 'Workspace' }).closest('.overflow-y-auto') as HTMLElement
    canvas.scrollTop = 120
    // fireEvent rather than userEvent: userEvent's click would move focus itself.
    fireEvent.click(summary)
    expect(input).not.toHaveFocus()
    expect(canvas.scrollTop).toBe(0)
  })
})
