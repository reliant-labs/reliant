package chat

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"github.com/reliant-labs/reliant/internal/threads"
	"golang.org/x/sync/errgroup"
)

// ListMessages lists messages for a chat with content blocks
// For branched chats, this resolves inherited messages via the context window chain.
func (s *Service) ListMessages(
	ctx context.Context,
	req *connect.Request[reliantv1.ListMessagesRequest],
) (*connect.Response[reliantv1.ListMessagesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Verify user owns the chat
	chat, err := s.deps.Repo.GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if chat.UserID != userID {
		return nil, svcerr.NotFound("chat")
	}

	// Parse pagination params
	recentLimit := 0
	if req.Msg.Recent != nil && *req.Msg.Recent > 0 {
		recentLimit = int(*req.Msg.Recent)
	}

	var beforeSeq int64
	if req.Msg.BeforeSeq != nil && *req.Msg.BeforeSeq > 0 {
		beforeSeq = *req.Msg.BeforeSeq
	}

	// Determine the main thread ID for this chat
	mainThreadID := req.Msg.ChatId
	if id := chat.MainThreadID(); id != "" {
		mainThreadID = id
	}

	threadsSvc := threads.NewService(s.deps.Repo)

	var messages []*db.Message
	var totalCount int
	var oldestSeq int64
	var hasMore bool

	// The thread whose point of view these messages are read from; governs
	// tool-call status resolution (see messageproto.Options.ViewingThreadID).
	viewingThreadID := mainThreadID

	switch {
	case req.Msg.ThreadId != nil && *req.Msg.ThreadId != "":
		// Single-thread read: return exactly this thread's visual thread. No
		// merge with siblings and no main-thread window — the caller asked for
		// one thread, so the answer is that thread or an error, never a
		// best-effort slice of something else.
		threadID := *req.Msg.ThreadId
		viewingThreadID = threadID

		thread, err := s.deps.Repo.GetThread(ctx, threadID)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get thread"))
		}
		// Ownership is transitive through the chat, which is already
		// authorized above. A thread from another chat is NotFound rather
		// than a silent fall back to the chat-wide list: answering a
		// different question than the one asked is how the spawn preview
		// came to render the wrong thing in the first place.
		if thread.ChatID != req.Msg.ChatId {
			return nil, svcerr.NotFound("thread")
		}

		messages, hasMore, err = s.listThreadMessages(ctx, threadsSvc, threadID, beforeSeq, recentLimit)
		if err != nil {
			return nil, messagesReadError(ctx, err)
		}
		// The display count, not the LLM one: listThreadMessages reads the
		// transcript (which crosses compaction boundaries), so a total that
		// stopped at the newest summary would describe a different list than
		// the one being returned.
		totalCount, err = threadsSvc.CountDisplayMessages(ctx, threadID)
		if err != nil {
			return nil, messagesReadError(ctx, err)
		}
		if len(messages) > 0 {
			oldestSeq = messages[0].Seq
		}

	case recentLimit > 0:
		messages, totalCount, oldestSeq, hasMore, err = s.listMessagesBounded(
			ctx, threadsSvc, req.Msg.ChatId, mainThreadID, beforeSeq, recentLimit)
		if err != nil {
			return nil, messagesReadError(ctx, err)
		}
	default:
		// No page size given: the caller wants the whole chat (e.g. the cold
		// initial load before any pagination has happened). Resolve every
		// thread's full history and hand it to paginateBySeq unbounded.
		messages, totalCount, err = s.listMessagesUnbounded(ctx, threadsSvc, req.Msg.ChatId, mainThreadID)
		if err != nil {
			return nil, messagesReadError(ctx, err)
		}

		var page messagePage
		messages, page = paginateBySeq(messages, beforeSeq, recentLimit, mainThreadID)
		oldestSeq, hasMore = page.OldestSeq, page.HasMore
	}

	// Batch-fetch content blocks for the paginated page of messages in one
	// query, then group by message id. This replaces three separate
	// ListContentBlocks (single-message) round trips per message with a
	// single ListContentBlocksForMessages call.
	messageIDs := make([]string, len(messages))
	for i, msg := range messages {
		messageIDs[i] = msg.ID
	}
	allBlocks, err := s.deps.Repo.ListContentBlocksForMessages(ctx, messageIDs)
	if err != nil {
		return nil, messagesReadError(ctx, err)
	}
	blocksByMessageID := make(map[string][]*db.MessageContentBlock, len(messages))
	for _, block := range allBlocks {
		blocksByMessageID[block.MessageID] = append(blocksByMessageID[block.MessageID], block)
	}

	// Collect all attachment IDs from image and file_reference blocks
	attachmentIDSet := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM {
			continue
		}
		for _, block := range blocksByMessageID[msg.ID] {
			if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) && block.Content != nil {
				attachmentIDSet[*block.Content] = true
			}
		}
	}

	// Fetch all attachments in bulk
	attachmentIDs := make([]string, 0, len(attachmentIDSet))
	for id := range attachmentIDSet {
		attachmentIDs = append(attachmentIDs, id)
	}
	attachmentMap := make(map[string]*db.Attachment)
	if len(attachmentIDs) > 0 {
		attachmentsData, err := s.deps.Repo.GetAttachmentsByIDs(ctx, attachmentIDs)
		if err == nil {
			for _, att := range attachmentsData {
				attachmentMap[att.ID] = att
			}
		}
	}

	protoMessages := messageproto.AssembleForDisplay(
		ctx, s.deps.Repo, messages, blocksByMessageID, attachmentMap, viewingThreadID, 0)

	return connect.NewResponse(&reliantv1.ListMessagesResponse{
		Messages:  protoMessages,
		Total:     int32(totalCount),
		Count:     int32(len(protoMessages)),
		HasMore:   hasMore,
		OldestSeq: oldestSeq,
	}), nil
}

// listMessagesUnbounded resolves EVERY message this chat has -- the main
// thread's full CW-chain-resolved history plus every child thread's full
// history -- for the legacy "recent not specified" path. This is the
// pre-existing (unbounded) ListMessages behavior, extracted unchanged so
// paginateBySeq keeps operating over a truly complete slice for that one
// caller shape.
func (s *Service) listMessagesUnbounded(
	ctx context.Context, threadsSvc *threads.Service, chatID, mainThreadID string,
) ([]*db.Message, int, error) {
	mainThreadMessages, err := threadsSvc.LoadDisplayMessages(ctx, mainThreadID)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to resolve main thread messages: %w", err)
	}
	for _, msg := range mainThreadMessages {
		if msg.ThreadID != mainThreadID {
			msg.ThreadID = mainThreadID
		}
	}

	allThreads, err := s.deps.Repo.ListThreadsByConversation(ctx, chatID)
	if err != nil {
		logging.WarnContext(ctx, "Failed to list threads for conversation", "error", err, "chatID", chatID)
		allThreads = []*db.Thread{}
	}

	var childThreadMessages []*db.Message
	for _, thread := range allThreads {
		if thread.ID == mainThreadID {
			continue
		}
		childMsgs, err := threadsSvc.LoadDisplayMessages(ctx, thread.ID)
		if err != nil {
			logging.WarnContext(ctx, "Failed to load child thread messages", "error", err, "threadID", thread.ID)
			continue
		}
		childThreadMessages = append(childThreadMessages, childMsgs...)
	}

	messageMap := make(map[string]*db.Message, len(mainThreadMessages)+len(childThreadMessages))
	for _, msg := range mainThreadMessages {
		messageMap[msg.ID] = msg
	}
	for _, msg := range childThreadMessages {
		if _, exists := messageMap[msg.ID]; !exists {
			messageMap[msg.ID] = msg
		}
	}
	messages := make([]*db.Message, 0, len(messageMap))
	for _, msg := range messageMap {
		messages = append(messages, msg)
	}

	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Seq < messages[j].Seq
	})

	return messages, len(messages), nil
}

// listThreadMessages reads exactly one thread's visual thread — the read
// behind ListMessagesRequest.thread_id.
//
// It is the whole implementation of a single-thread read, and it is this
// short on purpose: one thread's history is a thing the threads service
// already knows how to resolve (fork points, compaction chains and visual-
// thread normalization included). The chat-wide paths above are long because
// they merge N threads and then have to decide what to keep; there is nothing
// to merge here, so there is nothing to decide.
//
// recentLimit <= 0 means "the whole thread", which is what a spawn preview
// wants: the child is bounded by its own length, not by a page size borrowed
// from the main transcript.
func (s *Service) listThreadMessages(
	ctx context.Context, threadsSvc *threads.Service, threadID string, beforeSeq int64, recentLimit int,
) ([]*db.Message, bool, error) {
	if recentLimit > 0 {
		return threadsSvc.LoadRecentMessagesBefore(ctx, threadID, beforeSeq, recentLimit)
	}

	messages, err := threadsSvc.LoadDisplayMessages(ctx, threadID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load thread messages: %w", err)
	}
	if beforeSeq > 0 {
		filtered := messages[:0]
		for _, msg := range messages {
			if msg.Seq < beforeSeq {
				filtered = append(filtered, msg)
			}
		}
		messages = filtered
	}
	// The whole thread was read, so nothing precedes it.
	return messages, false, nil
}

// listMessagesBounded is the SQL-bounded counterpart to
// listMessagesUnbounded, for the normal (recentLimit > 0) path. It reads
// only the window a page actually needs instead of the chat's entire
// history:
//
//   - Main thread: LoadRecentMessagesBefore walks the main thread's
//     context-window chain newest-first and stops once the page is full, so a
//     compacted or branched chat costs the windows the page spans rather than
//     its whole transcript.
//   - Siblings: ONE query for the messages of every non-main, non-spawn
//     thread inside the seq span the main-thread window covers. This is the
//     same set, from the same query, the chat snapshot builds its siblings
//     from, so a scroll-back page and the snapshot agree on what a transcript
//     holds. Spawn threads are left out for the snapshot's reason: a spawn
//     renders as one tool-call card that reads its own thread
//     (ListMessages with thread_id) when expanded.
//   - Total: CountDisplayMessages(main) covers the main thread's own
//     CW-chain-resolved count -- the DISPLAY resolution, matching the
//     transcript this page is a window into; the LLM count stops at a
//     compaction summary and would undercount a compacted chat by its whole
//     summarized history. The chat's messages on every other thread are one
//     COUNT (siblings don't inherit from elsewhere, so their own chat_id-scoped
//     rows are their whole count).
//   - HasMore: the main thread's own hasMoreOlder. seq is a chat-global order
//     (the chat's seq counter, which a branch's fork raises past its
//     inherited history), and the oldest page (no older main message) takes
//     every sibling below it too, so no sibling message is left without a
//     page. A chat whose main thread has nothing in range pages its siblings
//     alone, recentLimit at a time, and HasMore comes from that read.
//
// The two counts depend on nothing the page read produces, so they run
// beside it.
func (s *Service) listMessagesBounded(
	ctx context.Context, threadsSvc *threads.Service, chatID, mainThreadID string, beforeSeq int64, recentLimit int,
) ([]*db.Message, int, int64, bool, error) {
	var mainCount, siblingCount int
	counts, countsCtx := errgroup.WithContext(ctx)
	counts.Go(func() (err error) {
		if mainCount, err = threadsSvc.CountDisplayMessages(countsCtx, mainThreadID); err != nil {
			return fmt.Errorf("failed to count main thread messages: %w", err)
		}
		return nil
	})
	counts.Go(func() (err error) {
		if siblingCount, err = s.deps.Repo.CountMessagesInChatOutsideThread(countsCtx, chatID, mainThreadID); err != nil {
			return fmt.Errorf("failed to count sibling thread messages: %w", err)
		}
		return nil
	})

	mainThreadMessages, mainHasMoreOlder, err := threadsSvc.LoadRecentMessagesBefore(ctx, mainThreadID, beforeSeq, recentLimit)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("failed to load recent main thread messages: %w", err)
	}
	for _, msg := range mainThreadMessages {
		if msg.ThreadID != mainThreadID {
			msg.ThreadID = mainThreadID
		}
	}

	// The seq span this page's main-thread window actually covers. Siblings
	// are bounded to this range rather than their whole history -- same
	// effect as windowByMainThread's "every sibling message inside that seq
	// range". On the newest page the range is open above, like the chat
	// snapshot's own window (buildChatSnapshot): a sibling thread can
	// out-write and out-live the main thread, finishing after the main
	// thread's newest message in this page.
	// The cursor itself is still the right upper bound on a page OTHER than
	// the newest -- everything at or after beforeSeq belongs to a page
	// already served.
	var fromSeq int64
	var toSeq *int64
	if beforeSeq > 0 {
		toSeq = &beforeSeq
	}
	hasMore := mainHasMoreOlder
	siblingLimit := 0
	switch {
	case len(mainThreadMessages) == 0:
		// No main-thread window to bound the range by: page the siblings
		// themselves, newest first, probing one past the page for hasMore.
		siblingLimit = recentLimit + 1
	case mainHasMoreOlder:
		fromSeq = mainThreadMessages[0].Seq
	default:
		// The oldest page: a sibling older than the main thread's first
		// message has no later page to land on, so it lands here (fromSeq 0).
	}

	siblingMessages, err := s.deps.Repo.ListTranscriptSiblingMessagesInRange(ctx, chatID, mainThreadID, fromSeq, toSeq, siblingLimit)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("failed to load sibling thread messages: %w", err)
	}
	if siblingLimit > 0 && len(siblingMessages) > recentLimit {
		siblingMessages = siblingMessages[len(siblingMessages)-recentLimit:]
		hasMore = true
	}

	messageMap := make(map[string]*db.Message, len(mainThreadMessages)+len(siblingMessages))
	for _, msg := range mainThreadMessages {
		messageMap[msg.ID] = msg
	}
	for _, msg := range siblingMessages {
		if _, exists := messageMap[msg.ID]; !exists {
			messageMap[msg.ID] = msg
		}
	}

	messages := make([]*db.Message, 0, len(messageMap))
	for _, msg := range messageMap {
		messages = append(messages, msg)
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Seq < messages[j].Seq
	})

	// The window above is already the correctly-sized page (the same shape
	// windowByMainThread would produce), but re-apply it for safety: a
	// sibling thread with messages exactly AT the boundary could otherwise
	// push the reported page past recentLimit main-thread messages. Not on
	// the oldest page, whose siblings below the main thread's first message
	// have no other page to land on.
	if mainHasMoreOlder {
		messages = windowByMainThread(messages, mainThreadID, recentLimit)
	}

	if err := counts.Wait(); err != nil {
		return nil, 0, 0, false, err
	}

	var oldestSeq int64
	if len(messages) > 0 {
		oldestSeq = messages[0].Seq
	}

	return messages, mainCount + siblingCount, oldestSeq, hasMore, nil
}

// messagePage carries the cursor facts a paginated message read has to report
// back to the client.
type messagePage struct {
	// OldestSeq is the lowest seq in the returned page, i.e. the cursor the
	// client passes as before_seq to ask for the page before this one.
	OldestSeq int64
	// HasMore reports whether any message older than this page exists.
	HasMore bool
}

// windowByMainThread returns the tail of messages containing the newest `limit`
// main-thread messages, plus every sibling-thread message inside that range.
//
// Counting the window across every thread makes a page of a spawn-heavy chat
// almost entirely spawn messages, which render collapsed inside their tool
// call rather than in the transcript — so a full page loads and the visible
// conversation barely moves. Below one cursor in a real chat there were 5,675
// messages chat-wide but only 781 on the main thread, so a 200-message page
// advanced the transcript by ~27 rows. Counting the MAIN thread and carrying
// siblings along keeps both the transcript's N rows and the spawn messages
// the tool-call previews render from.
//
// messages must be sorted by seq ascending. mainThreadID == "" falls back to
// a plain newest-N cut. If limit <= 0 or there are already at most `limit`
// messages, messages is returned unchanged.
func windowByMainThread(messages []*db.Message, mainThreadID string, limit int) []*db.Message {
	if limit <= 0 || len(messages) <= limit {
		return messages
	}
	if mainThreadID == "" {
		return messages[len(messages)-limit:]
	}
	// Walk back until we have `limit` main-thread messages, then cut there —
	// sibling messages above that point come along for free.
	mainSeen, cutoff := 0, -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].ThreadID == mainThreadID {
			mainSeen++
			if mainSeen == limit {
				cutoff = i
				break
			}
		}
	}
	if cutoff > 0 {
		return messages[cutoff:]
	}
	// cutoff < 0 means the remaining history holds fewer than `limit`
	// main-thread messages; return all of it.
	return messages
}

// paginateBySeq narrows a seq-ascending message slice to one page and reports
// the cursor facts for it.
//
// hasMore is derived by comparing the page's oldest seq against the oldest seq
// in the whole chat, which is the only formulation that answers the question the
// client is actually asking ("is there anything before what I'm holding?").
//
// The previous implementation was `len(messages) < totalCount || beforeSeq > 0`
// with totalCount taken AFTER the before_seq filter. Both halves were wrong.
// totalCount measured the already-filtered set, so it shrank in step with the
// slice it was compared against; and `beforeSeq > 0` forced hasMore true for
// every paginated request, so scrollback could never terminate. Together they
// made the client unable to distinguish "more history exists" from "you have
// reached the beginning" — the user-visible symptom being a chat that refuses
// to load older messages no matter how far you scroll.
//
// The page size is counted in MAIN-THREAD messages via windowByMainThread; see
// its doc comment for why.
//
// messages must already be sorted by seq ascending.
func paginateBySeq(messages []*db.Message, beforeSeq int64, recentLimit int, mainThreadID string) ([]*db.Message, messagePage) {
	if len(messages) == 0 {
		return messages, messagePage{}
	}

	// The oldest seq the chat has, captured before either filter runs.
	chatOldestSeq := messages[0].Seq

	if beforeSeq > 0 {
		filtered := make([]*db.Message, 0, len(messages))
		for _, msg := range messages {
			if msg.Seq < beforeSeq {
				filtered = append(filtered, msg)
			}
		}
		messages = filtered
	}

	// Messages are oldest-first, so the most recent N are at the end.
	messages = windowByMainThread(messages, mainThreadID, recentLimit)

	if len(messages) == 0 {
		// Paginated past the beginning: nothing to return and nothing older.
		return messages, messagePage{}
	}

	oldest := messages[0].Seq
	return messages, messagePage{
		OldestSeq: oldest,
		HasMore:   oldest > chatOldestSeq,
	}
}

// messagesReadError is a failed message read as the client reads it. A read
// the client abandoned (tab closed, page resumed, navigation) fails with the
// request's own cancellation or deadline even when the driver's error does not
// say so; that is the caller's doing, not a server fault, and reporting it as
// internal made every phone-browser suspend look like an outage. So the
// request's end, when it has ended, is part of the error.
func messagesReadError(ctx context.Context, err error) error {
	return svcerr.WrapOr(errors.Join(ctx.Err(), err), svcerr.Internal("failed to load messages"))
}
