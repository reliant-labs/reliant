// TEMPORARY (README.md): the chat store as the tour's navigation sees it in
// the demo, which has no chats. useTourNavigation clears the open chat only
// when a Reliant tour ends.

export const useChatStore = {
  getState: () => ({ clearCurrentChat: (_next?: unknown) => {} }),
};
