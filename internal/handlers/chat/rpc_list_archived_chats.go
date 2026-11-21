package chat

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// Archived list page sizes: the size a page_token with no page_size gets, and
// the most one page returns.
const (
	defaultArchivedChatsPageSize = 50
	maxArchivedChatsPageSize     = 200
)

// ListArchivedChats lists the caller's archived chats with worktree info: the
// named org's (orgs.ListScope), or every org's when it names none. A request
// with a page_size or page_token gets one page and a next_page_token; one with
// neither gets every archived chat, which is what clients released before
// paging read and render.
func (s *Service) ListArchivedChats(
	ctx context.Context,
	req *connect.Request[reliantv1.ListArchivedChatsRequest],
) (*connect.Response[reliantv1.ListArchivedChatsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	var page db.ArchivedChatPage
	if size, token := req.Msg.GetPageSize(), req.Msg.GetPageToken(); size > 0 || token != "" {
		page.Limit = min(int(size), maxArchivedChatsPageSize)
		if page.Limit <= 0 {
			page.Limit = defaultArchivedChatsPageSize
		}
		if token != "" {
			cursor, err := decodeArchivedChatCursor(token)
			if err != nil {
				return nil, svcerr.InvalidArgument("invalid page_token")
			}
			page.After = cursor
		}
	}

	scope, err := s.listScope(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	archivedChats, more, err := s.deps.Repo.ListArchivedChats(ctx, userID, scope, page)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list archived chats"))
	}
	// The cursor is cut from the rows as read, before the visibility filter,
	// so a page whose rows are all denied is short, never the end of the list.
	nextPageToken := ""
	if more && len(archivedChats) > 0 {
		last := archivedChats[len(archivedChats)-1]
		nextPageToken = encodeArchivedChatCursor(db.ArchivedChatCursor{UpdatedAt: last.UpdatedAt, ChatID: last.ID})
	}
	archivedChats = authz.Visible(ctx, userID, archivedChats, func(ctx context.Context, userID string, ac *db.ArchivedChatInfo) bool {
		return authz.CanReadChat(ctx, userID, &ac.Chat)
	})

	protoChats := make([]*reliantv1.ArchivedChat, len(archivedChats))
	for i, ac := range archivedChats {
		acProto := chatToProto(&ac.Chat)
		protoChats[i] = &reliantv1.ArchivedChat{
			Chat: acProto,
		}
		if ac.WorktreeName != nil {
			protoChats[i].WorktreeName = ac.WorktreeName
		}
		if ac.WorktreeDeletedAt != nil {
			deletedAt := ac.WorktreeDeletedAt.Format(time.RFC3339)
			protoChats[i].WorktreeDeletedAt = &deletedAt
		}
	}

	return connect.NewResponse(&reliantv1.ListArchivedChatsResponse{
		Chats:         protoChats,
		Total:         int32(len(protoChats)),
		NextPageToken: nextPageToken,
	}), nil
}

// The archived list's page token is the last row's (updated_at, chat id),
// base64url-encoded. It is opaque to callers; the keyset it encodes is the
// list's sort key, so paging never repeats a chat or skips one already ahead
// of the cursor.
func encodeArchivedChatCursor(c db.ArchivedChatCursor) string {
	raw := strconv.FormatInt(c.UpdatedAt.UnixMicro(), 10) + "|" + c.ChatID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeArchivedChatCursor(token string) (*db.ArchivedChatCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	micros, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return nil, fmt.Errorf("malformed cursor")
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return nil, err
	}
	return &db.ArchivedChatCursor{UpdatedAt: time.UnixMicro(n).UTC(), ChatID: id}, nil
}
