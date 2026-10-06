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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

const startChat = vi.fn()
const selectChat = vi.fn()
const navigate = vi.fn()

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

vi.mock('../../../store/worktreeStore', () => ({
  useWorktreeStore: (selector: (s: unknown) => unknown) =>
    selector({
      worktrees: [{ id: 'wt-main', is_main: true, name: 'main' }],
      loadWorktrees: vi.fn(),
    }),
}))

vi.mock('../../../store/globalDataStore', () => ({
  useWorkflows: () => ({
    workflows: [
      { name: 'builtin://agent', description: 'Basic agentic chat' },
      { name: 'builtin://forge-one-shot', description: 'Build with Forge' },
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
  daemonList.current = [{ daemonId: 'd-laptop', hostname: 'laptop', status: 1 }]
  startChat.mockReset()
  startChat.mockResolvedValue({ id: 'chat-9' })
  selectChat.mockReset()
  navigate.mockReset()
})

describe('MobileNewChat', () => {
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

  it('offers no attachment, params, or workspace controls', () => {
    render(<MobileNewChat />)
    expect(screen.queryByLabelText(/attach/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/parameters/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/daemon/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/workspace|worktree|branch/i)).not.toBeInTheDocument()
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
})
