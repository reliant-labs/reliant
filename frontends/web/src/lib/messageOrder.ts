/**
 * Canonical client-side ordering for chat messages: the ONE ordering used
 * everywhere a message list is sorted. It lives in @reliantlabs/sdk with the
 * reducers that depend on it; this module keeps the app's import path.
 */
export {
  compareMessagesWithinThread,
  sortMessagesForDisplay,
  type OrderableMessage,
} from "@reliantlabs/sdk/client";
