import { describe, it, expect, beforeEach } from 'vitest'
import { useAttachmentStore } from '../../../store/attachmentStore'

describe('Attachment Tab Isolation', () => {
  beforeEach(() => {
    // Reset the attachment store before each test
    useAttachmentStore.getState().reset()
  })

  it('should not share attachments between tabs with different tabIds', () => {
    const store = useAttachmentStore.getState()

    // Create mock attachment
    const mockAttachment1 = {
      id: 'att-1',
      filename: 'file1.txt',
      size: 100,
      mime_type: 'text/plain',
      url: '/attachments/att-1',
    }

    const mockAttachment2 = {
      id: 'att-2',
      filename: 'file2.txt',
      size: 200,
      mime_type: 'text/plain',
      url: '/attachments/att-2',
    }

    // Simulate tab 1 - use tabId as sessionId
    const tab1SessionId = 'tab-1'
    
    // Manually add attachment to tab 1
    const newAttachments1 = new Map(store.attachments)
    newAttachments1.set(tab1SessionId, [mockAttachment1])
    useAttachmentStore.setState({ attachments: newAttachments1 })

    // Verify tab 1 has the attachment
    expect(store.getAttachments(tab1SessionId)).toHaveLength(1)
    expect(store.getAttachments(tab1SessionId)[0].id).toBe('att-1')

    // Simulate tab 2 - use different tabId as sessionId
    const tab2SessionId = 'tab-2'
    
    // Tab 2 should have NO attachments
    expect(store.getAttachments(tab2SessionId)).toHaveLength(0)

    // Add attachment to tab 2
    const currentStore = useAttachmentStore.getState()
    const newAttachments2 = new Map(currentStore.attachments)
    newAttachments2.set(tab2SessionId, [mockAttachment2])
    useAttachmentStore.setState({ attachments: newAttachments2 })

    // Get fresh store state
    const finalStore = useAttachmentStore.getState()
    
    // Verify each tab has its own attachments
    expect(finalStore.getAttachments(tab1SessionId)).toHaveLength(1)
    expect(finalStore.getAttachments(tab1SessionId)[0].id).toBe('att-1')
    
    expect(finalStore.getAttachments(tab2SessionId)).toHaveLength(1)
    expect(finalStore.getAttachments(tab2SessionId)[0].id).toBe('att-2')
  })

  it('should NOT share attachments when using "temp" sessionId', () => {
    const store = useAttachmentStore.getState()

    const mockAttachment = {
      id: 'att-temp',
      filename: 'temp.txt',
      size: 100,
      mime_type: 'text/plain',
      url: '/attachments/att-temp',
    }

    // Add attachment using "temp" sessionId (old behavior - this is the bug)
    const newAttachments = new Map(store.attachments)
    newAttachments.set('temp', [mockAttachment])
    useAttachmentStore.setState({ attachments: newAttachments })

    // Both "tab-1" and "tab-2" should NOT see temp attachments
    expect(store.getAttachments('tab-1')).toHaveLength(0)
    expect(store.getAttachments('tab-2')).toHaveLength(0)
    
    // Only "temp" should have attachments
    expect(store.getAttachments('temp')).toHaveLength(1)
  })

  it('should clear attachments for a specific tab without affecting others', () => {
    const store = useAttachmentStore.getState()

    const mockAttachment1 = {
      id: 'att-1',
      filename: 'file1.txt',
      size: 100,
      mime_type: 'text/plain',
      url: '/attachments/att-1',
    }

    const mockAttachment2 = {
      id: 'att-2',
      filename: 'file2.txt',
      size: 200,
      mime_type: 'text/plain',
      url: '/attachments/att-2',
    }

    // Add attachments to two different tabs
    const newAttachments = new Map(store.attachments)
    newAttachments.set('tab-1', [mockAttachment1])
    newAttachments.set('tab-2', [mockAttachment2])
    useAttachmentStore.setState({ attachments: newAttachments })

    // Clear tab-1 attachments
    store.clearAttachments('tab-1')

    // Tab 1 should be empty
    expect(store.getAttachments('tab-1')).toHaveLength(0)
    
    // Tab 2 should still have its attachment
    expect(store.getAttachments('tab-2')).toHaveLength(1)
    expect(store.getAttachments('tab-2')[0].id).toBe('att-2')
  })

  it('should remove a specific attachment from a tab without affecting other tabs', () => {
    const store = useAttachmentStore.getState()

    const mockAttachment1 = {
      id: 'att-1',
      filename: 'file1.txt',
      size: 100,
      mime_type: 'text/plain',
      url: '/attachments/att-1',
    }

    const mockAttachment2 = {
      id: 'att-2',
      filename: 'file2.txt',
      size: 200,
      mime_type: 'text/plain',
      url: '/attachments/att-2',
    }

    // Add attachments to two different tabs
    const newAttachments = new Map(store.attachments)
    newAttachments.set('tab-1', [mockAttachment1])
    newAttachments.set('tab-2', [mockAttachment2])
    useAttachmentStore.setState({ attachments: newAttachments })

    // Remove attachment from tab-1
    store.removeAttachment('tab-1', 'att-1')

    // Tab 1 should be empty
    expect(store.getAttachments('tab-1')).toHaveLength(0)
    
    // Tab 2 should still have its attachment
    expect(store.getAttachments('tab-2')).toHaveLength(1)
  })

  it('should handle new tabs with undefined chatId by using unique tabIds', () => {
    const store = useAttachmentStore.getState()

    // Simulate the scenario where ChatInput uses: chatId || tabId || "temp"
    const getSessionId = (chatId: string | undefined, tabId: string | undefined) => {
      return chatId || tabId || 'temp'
    }

    const mockAttachment1 = {
      id: 'att-1',
      filename: 'file1.txt',
      size: 100,
      mime_type: 'text/plain',
      url: '/attachments/att-1',
    }

    const mockAttachment2 = {
      id: 'att-2',
      filename: 'file2.txt',
      size: 200,
      mime_type: 'text/plain',
      url: '/attachments/att-2',
    }

    // Tab 1: no chatId, has tabId
    const sessionId1 = getSessionId(undefined, 'tab-1')
    expect(sessionId1).toBe('tab-1')
    
    const newAttachments1 = new Map(store.attachments)
    newAttachments1.set(sessionId1, [mockAttachment1])
    useAttachmentStore.setState({ attachments: newAttachments1 })

    // Tab 2: no chatId, has different tabId
    const sessionId2 = getSessionId(undefined, 'tab-2')
    expect(sessionId2).toBe('tab-2')
    
    // Tab 2 should NOT see tab 1's attachments
    expect(store.getAttachments(sessionId2)).toHaveLength(0)

    // Add attachment to tab 2
    const currentStore2 = useAttachmentStore.getState()
    const newAttachments2 = new Map(currentStore2.attachments)
    newAttachments2.set(sessionId2, [mockAttachment2])
    useAttachmentStore.setState({ attachments: newAttachments2 })

    // Get fresh store state
    const finalStore2 = useAttachmentStore.getState()
    
    // Verify isolation
    expect(finalStore2.getAttachments(sessionId1)).toHaveLength(1)
    expect(finalStore2.getAttachments(sessionId1)[0].id).toBe('att-1')
    
    expect(finalStore2.getAttachments(sessionId2)).toHaveLength(1)
    expect(finalStore2.getAttachments(sessionId2)[0].id).toBe('att-2')
  })

  it('should transition attachments from tabId to chatId when chat is created', () => {
    const store = useAttachmentStore.getState()

    const mockAttachment = {
      id: 'att-1',
      filename: 'file1.txt',
      size: 100,
      mime_type: 'text/plain',
      url: '/attachments/att-1',
    }

    // Start with tabId as sessionId (new tab, no chat yet)
    const tabId = 'tab-1'
    const newAttachments1 = new Map(store.attachments)
    newAttachments1.set(tabId, [mockAttachment])
    useAttachmentStore.setState({ attachments: newAttachments1 })

    expect(store.getAttachments(tabId)).toHaveLength(1)

    // Simulate chat creation - transfer attachments to chatId
    const chatId = 'chat-abc'
    const attachmentsFromTab = store.getAttachments(tabId)
    
    // Add to chatId
    const newAttachments2 = new Map(store.attachments)
    newAttachments2.set(chatId, attachmentsFromTab)
    // Clear from tabId
    newAttachments2.delete(tabId)
    useAttachmentStore.setState({ attachments: newAttachments2 })

    // Verify transfer
    expect(store.getAttachments(tabId)).toHaveLength(0)
    expect(store.getAttachments(chatId)).toHaveLength(1)
    expect(store.getAttachments(chatId)[0].id).toBe('att-1')
  })
})
