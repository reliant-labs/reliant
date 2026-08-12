// ChatSearch - Search chat history and within current chat (Cmd+Shift+C)
import { MessageRole, ContentBlockType } from "../../gen/reliant/v1/chat_pb";
import { useState, useRef, useEffect, forwardRef, useImperativeHandle, useCallback } from "react";
import { createPortal } from "react-dom";
import { Search, MessageSquare, Loader2, Clock, ArrowRight } from "lucide-react";
import { cn } from "../../lib/utils";
import { useChatStore } from "../../store/chatStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatList } from "../../hooks/chat-queries";
import { useChatNavigationStore } from "../../store/chatNavigationStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { api, type Message } from "../../api/client";
import { focusChatInput } from "../../hooks/useFocusManager";
import { requestScrollToMessage } from "../../lib/scrollToMessage";

type SearchMode = "history" | "current";

interface ChatSearchResult {
  chatId: string;
  title: string;
  updatedAt: string;
  messageCount: number;
  snippet?: string;
  matchedMessageId?: string;
}

interface MessageSearchResult {
  messageId: string;
  role: MessageRole;
  content: string;
  createdAt: string;
  matchStart?: number;
  matchEnd?: number;
}

export interface ChatSearchRef {
  open: (mode?: SearchMode) => void;
  close: () => void;
  isOpen: () => boolean;
}

interface ChatSearchProps {
  isOpen?: boolean;
  onClose?: () => void;
  /**
   * Which tab to show when opened via the `isOpen` prop.
   *
   * "history" makes this a chat switcher (find a chat by name); "current"
   * searches inside the open conversation. The imperative `open()` handle takes
   * the same argument.
   */
  initialMode?: SearchMode;
}

export const ChatSearch = forwardRef<ChatSearchRef, ChatSearchProps>(({ isOpen: externalIsOpen, onClose, initialMode }, ref) => {
  const [internalIsOpen, setInternalIsOpen] = useState(false);
  
  // Use external control if provided, otherwise use internal state
  const isOpen = externalIsOpen ?? internalIsOpen;
  const setIsOpen = useCallback((value: boolean) => {
    setInternalIsOpen(value);
    if (!value && onClose) {
      onClose();
    }
  }, [onClose]);
  const [mode, setMode] = useState<SearchMode>("history");
  const [query, setQuery] = useState("");
  const [chatResults, setChatResults] = useState<ChatSearchResult[]>([]);
  const [messageResults, setMessageResults] = useState<MessageSearchResult[]>([]);
  const [highlightedIndex, setHighlightedIndex] = useState(0);
  const [isLoading, setIsLoading] = useState(false);

  const inputRef = useRef<HTMLInputElement>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const searchTimeoutRef = useRef<NodeJS.Timeout | null>(null);

  const currentProject = useProjectStore((state) => state.currentProject);
  const { data: chats = [] } = useChatList(currentProject?.id);
  const activeChatId = useChatStore((state) => state.activeChatId);
  const selectChat = useChatStore((state) => state.selectChat);
  const navigateToChat = useChatNavigationStore((state) => state.navigateToChat);
  const switchWorktreeContext = useWorktreeStore((state) => state.switchWorktreeContext);
  const worktrees = useWorktreeStore((state) => state.worktrees);

  // Get current chat messages
  const currentChat = chats.find((c) => c.id === activeChatId);

  // Close helper
  const closeAndFocus = useCallback(() => {
    setIsOpen(false);
    setQuery("");
    setChatResults([]);
    setMessageResults([]);
    focusChatInput();
  }, [setIsOpen]);

  // Load recent chats for initial display (defined before hooks that use it)
  const loadRecentChats = useCallback(() => {
    const recent = [...chats]
      .sort((a, b) => new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime())
      .slice(0, 10)
      .map((chat) => ({
        chatId: chat.id,
        title: chat.title,
        updatedAt: chat.updatedAt,
        messageCount: 0,
      }));
    setChatResults(recent);
  }, [chats]);

  // Expose methods via ref
  useImperativeHandle(ref, () => ({
    open: (initialMode: SearchMode = "history") => {
      setIsOpen(true);
      setMode(initialMode);
      setQuery("");
      setHighlightedIndex(0);
      
      // Load initial results
      if (initialMode === "history") {
        loadRecentChats();
      }
      
      setTimeout(() => inputRef.current?.focus(), 50);
    },
    close: closeAndFocus,
    isOpen: () => isOpen,
  }), [isOpen, loadRecentChats, closeAndFocus, setIsOpen]);
  
  // Sync with external isOpen state
  useEffect(() => {
    if (externalIsOpen) {
      const mode = initialMode ?? "current";
      setQuery("");
      setHighlightedIndex(0);
      setChatResults([]);
      setMessageResults([]);
      setMode(mode);
      // Seed the list only for the chat-switcher view; "current" searches the
      // open conversation and has nothing to show until there is a query.
      if (mode === "history") loadRecentChats();
      setTimeout(() => inputRef.current?.focus(), 50);
    }
  }, [externalIsOpen, initialMode, loadRecentChats]);

  // Search chat history
  const searchChatHistory = useCallback(async (searchQuery: string) => {
    if (!searchQuery.trim()) {
      loadRecentChats();
      return;
    }

    setIsLoading(true);
    try {
      if (currentProject) {
        // Use backend FTS search
        const results = await api.chatsV2.search(currentProject.id, searchQuery);
        setChatResults(
          results.slice(0, 15).map((chat) => ({
            chatId: chat.id,
            title: chat.title,
            updatedAt: chat.updatedAt,
            messageCount: 0,
          }))
        );
      } else {
        // Fallback to client-side search
        const filtered = chats
          .filter((chat) => 
            chat.title.toLowerCase().includes(searchQuery.toLowerCase())
          )
          .slice(0, 15)
          .map((chat) => ({
            chatId: chat.id,
            title: chat.title,
            updatedAt: chat.updatedAt,
            messageCount: 0,
          }));
        setChatResults(filtered);
      }
    } catch (error) {
      console.error("Chat search failed:", error);
      // Fallback to client-side
      const filtered = chats
        .filter((chat) => 
          chat.title.toLowerCase().includes(searchQuery.toLowerCase())
        )
        .slice(0, 15)
        .map((chat) => ({
          chatId: chat.id,
          title: chat.title,
          updatedAt: chat.updatedAt,
          messageCount: 0,
        }));
      setChatResults(filtered);
    } finally {
      setIsLoading(false);
    }
  }, [currentProject, chats, loadRecentChats]);

  // Search within current chat
  const searchCurrentChat = useCallback(async (searchQuery: string) => {
    if (!activeChatId || !searchQuery.trim()) {
      setMessageResults([]);
      return;
    }

    setIsLoading(true);
    try {
      // Fetch messages for current chat
      const result = await api.chatsV2.listMessages(activeChatId);
      const messages = result.messages;
      const query = searchQuery.toLowerCase();
      
      const results: MessageSearchResult[] = [];
      
      for (const msg of messages) {
        // Search in text content
        const content = getMessageTextContent(msg);
        const lowerContent = content.toLowerCase();
        const matchIndex = lowerContent.indexOf(query);
        
        if (matchIndex !== -1) {
          results.push({
            messageId: msg.id,
            role: msg.role,
            content: content,
            createdAt: msg.createdAt || '',
            matchStart: matchIndex,
            matchEnd: matchIndex + query.length,
          });
        }
      }
      
      setMessageResults(results.slice(0, 20));
    } catch (error) {
      console.error("Message search failed:", error);
      setMessageResults([]);
    } finally {
      setIsLoading(false);
    }
  }, [activeChatId]);

  // Get text content from message
  const getMessageTextContent = (message: Message): string => {
    // Extract text from proto contentBlocks
    const blocks = message.contentBlocks || [];
    return blocks
      .filter((b) => b.type === ContentBlockType.TEXT)
      .map((b) => b.content || "")
      .join("");
  };

  // Debounced search
  useEffect(() => {
    if (searchTimeoutRef.current) {
      clearTimeout(searchTimeoutRef.current);
    }

    searchTimeoutRef.current = setTimeout(() => {
      if (mode === "history") {
        searchChatHistory(query);
      } else {
        searchCurrentChat(query);
      }
    }, 300);

    return () => {
      if (searchTimeoutRef.current) {
        clearTimeout(searchTimeoutRef.current);
      }
    };
  }, [query, mode, searchChatHistory, searchCurrentChat]);

  // Reset results when mode changes
  useEffect(() => {
    setHighlightedIndex(0);
    if (mode === "history") {
      setMessageResults([]);
      if (!query) loadRecentChats();
    } else {
      setChatResults([]);
    }
  }, [mode, loadRecentChats, query]);

  // Close on click outside
  useEffect(() => {
    if (!isOpen) return;

    const handleClickOutside = (e: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) {
        closeAndFocus();
      }
    };

    setTimeout(() => {
      document.addEventListener("mousedown", handleClickOutside);
    }, 100);

    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
    };
  }, [isOpen, closeAndFocus]);

  // Get total results count for navigation
  const totalResults = mode === "history" ? chatResults.length : messageResults.length;

  // Handle keyboard navigation
  const handleKeyDown = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setHighlightedIndex((prev) => 
          prev < totalResults - 1 ? prev + 1 : 0
        );
        break;
      case "ArrowUp":
        e.preventDefault();
        setHighlightedIndex((prev) => 
          prev > 0 ? prev - 1 : totalResults - 1
        );
        break;
      case "Enter":
        e.preventDefault();
        if (mode === "history" && chatResults[highlightedIndex]) {
          openChat(chatResults[highlightedIndex]);
        } else if (mode === "current" && messageResults[highlightedIndex]) {
          scrollToMessage(messageResults[highlightedIndex]);
        }
        break;
      case "Tab":
        e.preventDefault();
        setMode(mode === "history" ? "current" : "history");
        break;
      case "Escape":
        e.preventDefault();
        closeAndFocus();
        break;
    }
  };

  // Open a chat from history
  const openChat = async (result: ChatSearchResult) => {
    const chat = chats.find((c) => c.id === result.chatId);
    if (chat) {
      if (currentProject?.id) {
        if (chat.worktreeId) {
          const targetWorktree = worktrees.find((worktree) => worktree.id === chat.worktreeId) ?? null;
          if (targetWorktree) {
            await switchWorktreeContext(currentProject.id, targetWorktree);
          }
        } else {
          await switchWorktreeContext(currentProject.id, null);
        }
      }
      navigateToChat(chat.id);
      selectChat(chat);

      // When the hit was a specific message, land on it rather than dumping the
      // user at the bottom of the conversation. The timeline for the new chat
      // has not mounted yet, so the event has to wait for it — the listener
      // ignores ids it cannot find, which is the same no-op as before if the
      // message falls outside the loaded page.
      if (result.matchedMessageId) {
        requestScrollToMessage(result.matchedMessageId);
      }
    }
    closeAndFocus();
  };

  // Scroll to message in current chat
  const scrollToMessage = (result: MessageSearchResult) => {
    window.dispatchEvent(new CustomEvent("scroll-to-message", {
      detail: { messageId: result.messageId }
    }));
    closeAndFocus();
  };

  // Highlight match in content
  const highlightMatch = (content: string, start?: number, end?: number) => {
    if (start === undefined || end === undefined) {
      return <span className="text-muted-foreground">{truncateContent(content)}</span>;
    }
    
    // Get surrounding context
    const contextStart = Math.max(0, start - 30);
    const contextEnd = Math.min(content.length, end + 50);
    
    const before = content.slice(contextStart, start);
    const match = content.slice(start, end);
    const after = content.slice(end, contextEnd);
    
    return (
      <>
        {contextStart > 0 && "..."}
        <span className="text-muted-foreground">{before}</span>
        <span className="bg-yellow-500/30 text-foreground font-medium">{match}</span>
        <span className="text-muted-foreground">{after}</span>
        {contextEnd < content.length && "..."}
      </>
    );
  };

  // Truncate content for display
  const truncateContent = (content: string, maxLength = 100) => {
    if (content.length <= maxLength) return content;
    return content.slice(0, maxLength) + "...";
  };

  // Format relative time
  const formatRelativeTime = (dateStr: string) => {
    const date = new Date(dateStr);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffMins = Math.floor(diffMs / 60000);
    const diffHours = Math.floor(diffMs / 3600000);
    const diffDays = Math.floor(diffMs / 86400000);
    
    if (diffMins < 1) return "just now";
    if (diffMins < 60) return `${diffMins}m ago`;
    if (diffHours < 24) return `${diffHours}h ago`;
    if (diffDays < 7) return `${diffDays}d ago`;
    return date.toLocaleDateString();
  };

  // Auto-scroll highlighted item into view
  useEffect(() => {
    if (highlightedIndex >= 0 && dropdownRef.current) {
      const element = dropdownRef.current.querySelector(`[data-index="${highlightedIndex}"]`);
      element?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
  }, [highlightedIndex]);

  if (!isOpen) return null;

  return createPortal(
    <div 
      className="fixed inset-0 z-50 flex items-start justify-center pt-[10vh]"
      data-modal-open="true"
      onClick={(e) => {
        if (e.target === e.currentTarget) closeAndFocus();
      }}
    >
      {/* Backdrop */}
      <div className="absolute inset-0 bg-black/50" />
      
      {/* Modal */}
      <div 
        ref={dropdownRef}
        className="relative w-full max-w-2xl bg-background border border-border rounded-lg shadow-2xl overflow-hidden"
      >
        {/* Mode tabs */}
        <div className="flex border-b border-border">
          <button
            onClick={() => setMode("history")}
            className={cn(
              "flex-1 px-4 py-2 text-sm font-medium transition-colors",
              mode === "history" 
                ? "bg-muted text-foreground border-b-2 border-primary" 
                : "text-muted-foreground hover:text-foreground"
            )}
          >
            Chat History
          </button>
          <button
            onClick={() => setMode("current")}
            disabled={!activeChatId}
            className={cn(
              "flex-1 px-4 py-2 text-sm font-medium transition-colors",
              mode === "current" 
                ? "bg-muted text-foreground border-b-2 border-primary" 
                : "text-muted-foreground hover:text-foreground",
              !activeChatId && "opacity-50 cursor-not-allowed"
            )}
          >
            Current Chat {currentChat && `(${currentChat.title})`}
          </button>
        </div>

        {/* Search input */}
        <div className="flex items-center gap-2 px-4 py-3 border-b border-border">
          <Search className="w-4 h-4 text-muted-foreground flex-shrink-0" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={mode === "history" ? "Search chat history..." : "Search in current chat..."}
            className="flex-1 bg-transparent text-sm font-mono outline-none placeholder:text-muted-foreground"
            autoFocus
          />
          {isLoading && <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />}
        </div>

        {/* Results */}
        <div className="max-h-[60vh] overflow-y-auto">
          {mode === "history" ? (
            // Chat history results
            chatResults.length === 0 ? (
              <div className="px-4 py-8 text-center">
                <p className="text-sm text-muted-foreground font-mono">
                  {query ? "No chats found" : "No recent chats"}
                </p>
              </div>
            ) : (
              <div className="py-1">
                {chatResults.map((result, index) => (
                  <button
                    key={result.chatId}
                    data-index={index}
                    onClick={() => openChat(result)}
                    onMouseEnter={() => setHighlightedIndex(index)}
                    className={cn(
                      "w-full flex items-center gap-3 px-4 py-3 text-left transition-colors",
                      highlightedIndex === index 
                        ? "bg-primary/10 border-l-2 border-primary" 
                        : "hover:bg-muted/50 border-l-2 border-transparent"
                    )}
                  >
                    <MessageSquare className={cn(
                      "w-4 h-4 flex-shrink-0",
                      highlightedIndex === index ? "text-primary" : "text-muted-foreground"
                    )} />
                    <div className="flex-1 min-w-0">
                      <div className="text-sm font-medium truncate">{result.title}</div>
                      <div className="flex items-center gap-2 text-xs text-muted-foreground">
                        <span>{result.messageCount} messages</span>
                        <span>•</span>
                        <span className="flex items-center gap-1">
                          <Clock className="w-3 h-3" />
                          {formatRelativeTime(result.updatedAt)}
                        </span>
                      </div>
                    </div>
                    <ArrowRight className="w-4 h-4 text-muted-foreground" />
                  </button>
                ))}
              </div>
            )
          ) : (
            // Current chat message results
            messageResults.length === 0 ? (
              <div className="px-4 py-8 text-center">
                <p className="text-sm text-muted-foreground font-mono">
                  {query ? "No messages found" : "Type to search messages..."}
                </p>
              </div>
            ) : (
              <div className="py-1">
                {messageResults.map((result, index) => (
                  <button
                    key={result.messageId}
                    data-index={index}
                    onClick={() => scrollToMessage(result)}
                    onMouseEnter={() => setHighlightedIndex(index)}
                    className={cn(
                      "w-full flex items-start gap-3 px-4 py-3 text-left transition-colors",
                      highlightedIndex === index 
                        ? "bg-primary/10 border-l-2 border-primary" 
                        : "hover:bg-muted/50 border-l-2 border-transparent"
                    )}
                  >
                    <div className={cn(
                      "w-6 h-6 rounded-full flex items-center justify-center text-xs font-medium flex-shrink-0 mt-0.5",
                      result.role === MessageRole.USER 
                        ? "bg-primary/20 text-primary" 
                        : "bg-muted text-muted-foreground"
                    )}>
                      {result.role === MessageRole.USER ? "U" : "A"}
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="text-xs text-muted-foreground mb-1">
                        {result.role === MessageRole.USER ? "You" : "Assistant"} • {formatRelativeTime(result.createdAt)}
                      </div>
                      <div className="text-sm font-mono truncate">
                        {highlightMatch(result.content, result.matchStart, result.matchEnd)}
                      </div>
                    </div>
                  </button>
                ))}
              </div>
            )
          )}
        </div>

        {/* Footer */}
        <div className="px-4 py-2 border-t border-border bg-muted/30 flex items-center gap-3 text-xs text-muted-foreground">
          <span><kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">Tab</kbd> Switch mode</span>
          <span><kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">↑↓</kbd> Navigate</span>
          <span><kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">Enter</kbd> Select</span>
          <span><kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">Esc</kbd> Close</span>
        </div>
      </div>
    </div>,
    document.body
  );
});

ChatSearch.displayName = "ChatSearch";