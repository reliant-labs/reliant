import type { ChatSortOption } from "../store/chatListPreferencesStore";

/**
 * The only chat fields ordering depends on. Kept structural so the desktop
 * sidebar (`ChatWithActivity`) and the navigation store (domain `Chat`) can
 * both feed the same comparator without converting.
 */
export interface ChatOrderFields {
  id: string;
  title?: string;
  createdAt: string;
  lastMessageAt?: string;
}

/**
 * Whether a chat is blocked on the user. Supplied by the caller because each
 * surface reads it from a different place (the sidebar precomputes an
 * `activityState`, the navigation store queries the activity store by id).
 */
export type NeedsAttention<T> = (chat: T) => boolean;

const DEFAULT_TITLE = "New chat";

function activityTime(chat: ChatOrderFields): number {
  return new Date(chat.lastMessageAt || chat.createdAt).getTime();
}

function createdTime(chat: ChatOrderFields): number {
  return new Date(chat.createdAt).getTime();
}

/**
 * Ordering must be a total order, not just a partial one. The chat list
 * arrives from the server ordered by `updated_at DESC`, so its array order
 * changes every time any chat is touched; a comparator that returns 0 for two
 * chats lets that churn through to the screen, because a stable sort preserves
 * input order for ties. Breaking every tie on the immutable id makes the
 * rendered order a function of the chats alone.
 */
function byId(a: ChatOrderFields, b: ChatOrderFields): number {
  return a.id.localeCompare(b.id);
}

/**
 * Compare two chats under the user's selected sort order.
 *
 * Note what is deliberately absent: there is no unconditional "float chats
 * awaiting approval to the top" clause. That reordering is what
 * `needs_attention_first` selects, and applying it to every mode is what made
 * the list churn — a chat that asked for approval jumped to the top and fell
 * back when it was answered, so `newest_first` did not hold still even though
 * no chat's `created_at` had changed.
 */
export function compareChats<T extends ChatOrderFields>(
  a: T,
  b: T,
  sortOrder: ChatSortOption,
  needsAttention: NeedsAttention<T>
): number {
  switch (sortOrder) {
    case "needs_attention_first": {
      const aNeedsAttention = needsAttention(a);
      const bNeedsAttention = needsAttention(b);
      if (aNeedsAttention !== bNeedsAttention) return aNeedsAttention ? -1 : 1;
      return activityTime(b) - activityTime(a) || byId(a, b);
    }
    case "recent_activity":
      return activityTime(b) - activityTime(a) || byId(a, b);
    case "newest_first":
      return createdTime(b) - createdTime(a) || byId(a, b);
    case "oldest_first":
      return createdTime(a) - createdTime(b) || byId(a, b);
    case "alphabetical_asc":
      return (
        (a.title || DEFAULT_TITLE).localeCompare(b.title || DEFAULT_TITLE) ||
        byId(a, b)
      );
    case "alphabetical_desc":
      return (
        (b.title || DEFAULT_TITLE).localeCompare(a.title || DEFAULT_TITLE) ||
        byId(a, b)
      );
    default:
      return byId(a, b);
  }
}

export function sortChats<T extends ChatOrderFields>(
  chats: T[],
  sortOrder: ChatSortOption,
  needsAttention: NeedsAttention<T>
): T[] {
  return [...chats].sort((a, b) => compareChats(a, b, sortOrder, needsAttention));
}

/**
 * Order two non-empty workspace groups whose chats are already sorted, by
 * comparing the chat each one leads with.
 *
 * This needs no per-mode branching and inherits the mode's stability: under
 * `newest_first` a group is ranked by its newest chat's `created_at`, which
 * only moves when a chat is created, so groups stop trading places every time
 * a message lands. Under `needs_attention_first` the leading chat is the one
 * needing attention, so those groups still rise on their own.
 */
export function compareChatGroups<T extends ChatOrderFields>(
  aChats: T[],
  bChats: T[],
  sortOrder: ChatSortOption,
  needsAttention: NeedsAttention<T>
): number {
  return compareChats(aChats[0], bChats[0], sortOrder, needsAttention);
}
