// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/require"
)

// TestNoSqlcSliceInPostgresQueries rejects sqlc.slice() in the Postgres query
// files.
//
// sqlc.slice() is a MySQL/SQLite feature. For the postgresql engine sqlc
// emits `IN ($n)` but the generated Go still rewrites a `/*SLICE:x*/?` marker
// that is not in the string, so the query binds len(ids) arguments against a
// single placeholder: one id works, zero or several fail with "expected 1
// arguments, got N". Every query that used it had grown a hand-built IN clause
// in its store to route around the broken generated function. The Postgres
// form is an array parameter — `= ANY(sqlc.arg('ids')::text[])` — which sqlc
// binds with pq.Array and which handles an empty list without special-casing.
func TestNoSqlcSliceInPostgresQueries(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("postgres", "queries", "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "query files not found — did the directory move?")

	sliceCall := regexp.MustCompile(`sqlc\.slice\s*\(`)
	for _, f := range files {
		body, err := os.ReadFile(f)
		require.NoError(t, err)
		for i, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			if sliceCall.MatchString(line) {
				t.Errorf("%s:%d uses sqlc.slice(), which sqlc does not support for postgresql; "+
					"use `= ANY(sqlc.arg('name')::text[])` instead", f, i+1)
			}
		}
	}
}

// TestGeneratedArrayParamQueries_ZeroOneMany runs every generated query that
// takes a list of ids against a real database with zero, one and several ids.
//
// It calls the sqlc-generated functions directly, not the store wrappers,
// because the wrappers are what used to hide the defect: each one hand-built
// its own IN clause, so repository-level tests passed while the generated
// function underneath could not bind more than one id.
func TestGeneratedArrayParamQueries_ZeroOneMany(t *testing.T) {
	repo, rawDB, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	q := pgdb.New(rawDB)

	fx := seedArrayParamFixture(t, repo, ctx)

	t.Run("GetAttachmentsByIDs", func(t *testing.T) {
		for _, tc := range idCases(fx.attachmentIDs) {
			rows, err := q.GetAttachmentsByIDs(ctx, tc.ids)
			require.NoError(t, err, tc.name)
			got := make([]string, len(rows))
			for i, r := range rows {
				got[i] = r.ID
			}
			requireSameIDs(t, tc.ids, got, tc.name)
		}
	})

	t.Run("ListContentBlocksForMessages", func(t *testing.T) {
		for _, tc := range idCases(fx.messageIDs) {
			rows, err := q.ListContentBlocksForMessages(ctx, tc.ids)
			require.NoError(t, err, tc.name)
			got := make([]string, len(rows))
			for i, r := range rows {
				got[i] = r.MessageID
			}
			requireSameIDs(t, tc.ids, got, tc.name)
		}
	})

	t.Run("ListToolCallsByMessageIDs", func(t *testing.T) {
		for _, tc := range idCases(fx.messageIDs) {
			rows, err := q.ListToolCallsByMessageIDs(ctx, tc.ids)
			require.NoError(t, err, tc.name)
			got := make([]string, len(rows))
			for i, r := range rows {
				got[i] = r.MessageID.String
			}
			requireSameIDs(t, tc.ids, got, tc.name)
		}
	})

	t.Run("ListToolCallResultsByMessageIDs", func(t *testing.T) {
		for _, tc := range idCases(fx.messageIDs) {
			rows, err := q.ListToolCallResultsByMessageIDs(ctx, tc.ids)
			require.NoError(t, err, tc.name)
			got := make([]string, len(rows))
			for i, r := range rows {
				got[i] = r.MessageID.String
			}
			requireSameIDs(t, tc.ids, got, tc.name)
		}
	})

	// The two writes run in order on the same rows: claim, then backfill the
	// envelope pointer on exactly what was claimed — the drain's sequence.
	t.Run("MarkAgentMessagesDelivered+SetAgentMessagesDeliveredMessageID", func(t *testing.T) {
		deliveredAt := sql.NullTime{Time: time.Now().UTC().Truncate(time.Second), Valid: true}
		for _, tc := range idCases(fx.agentMessageIDs) {
			claimed, err := q.MarkAgentMessagesDelivered(ctx, pgdb.MarkAgentMessagesDeliveredParams{
				DeliveredAt: deliveredAt,
				Ids:         tc.ids,
			})
			require.NoError(t, err, tc.name)
			requireSameIDs(t, tc.ids, claimed, tc.name)

			require.NoError(t, q.SetAgentMessagesDeliveredMessageID(ctx, pgdb.SetAgentMessagesDeliveredMessageIDParams{
				DeliveredMessageID: sql.NullString{String: fx.envelopeMessageID, Valid: true},
				Ids:                tc.ids,
			}), tc.name)
		}

		// Every row was in exactly one case, so all of them must now be
		// delivered and point at the envelope — including the ones a
		// first-id-only query would have skipped.
		for _, id := range fx.agentMessageIDs {
			var status int32
			var envelope sql.NullString
			require.NoError(t, rawDB.QueryRowContext(ctx,
				`SELECT status, delivered_message_id FROM agent_messages WHERE id = $1`, id,
			).Scan(&status, &envelope))
			require.Equal(t, int32(core.AgentMessageStatusDelivered), status, "message %s status", id)
			require.Equal(t, fx.envelopeMessageID, envelope.String, "message %s envelope", id)
		}
	})
}

type idCase struct {
	name string
	ids  []string
}

// idCases splits ids into the three shapes every array query must handle:
// none, exactly one, and several. The one-id case is the only one the broken
// sqlc.slice() codegen ever handled, so on its own it proves nothing.
func idCases(ids []string) []idCase {
	return []idCase{
		{name: "zero ids", ids: []string{}},
		{name: "one id", ids: ids[:1]},
		{name: "several ids", ids: ids[1:]},
	}
}

func requireSameIDs(t *testing.T, want, got []string, msg string) {
	t.Helper()
	w := append([]string(nil), want...)
	g := append([]string(nil), got...)
	sort.Strings(w)
	sort.Strings(g)
	if len(w) == 0 {
		w = nil
	}
	if len(g) == 0 {
		g = nil
	}
	require.Equal(t, w, g, msg)
}

type arrayParamFixture struct {
	attachmentIDs     []string // 4
	messageIDs        []string // 4, each with one content block, one tool call and one result
	agentMessageIDs   []string // 4, queued
	envelopeMessageID string
}

// seedArrayParamFixture writes four of everything, so each query sees one id
// in the "one" case and three in the "several" case, plus one decoy row per
// table that no case asks for (so a query that ignored its filter would fail).
func seedArrayParamFixture(t *testing.T, repo *Repo, ctx context.Context) arrayParamFixture {
	t.Helper()
	now := time.Now().UTC()
	var fx arrayParamFixture

	chatID, parentThreadID, childThreadID := seedAgentMessageThreads(t, repo, ctx)
	cwID := uuid.New().String()
	_, err := repo.CreateContextWindow(ctx, &ContextWindow{ID: cwID, ThreadID: parentThreadID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		decoy := i == 4

		attachmentID := uuid.New().String()
		require.NoError(t, repo.CreateAttachment(ctx, &Attachment{
			ID: attachmentID, UserID: "test-user", Filename: "f.txt", Size: 1,
			MimeType: "text/plain", FilePath: "test-user/f.txt", AttachmentType: "file_reference",
			CreatedAt: now, UpdatedAt: now,
		}))

		messageID := uuid.New().String()
		require.NoError(t, repo.CreateMessage(ctx, &Message{
			ID: messageID, ChatID: chatID, Ordinal: int64(i + 101), Seq: int64(i + 101), ThreadID: parentThreadID,
			ContextWindowID: cwID, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
			CreatedAt: now, UpdatedAt: now,
		}))
		toolCallID := "toolu_" + uuid.New().String()[:12]
		require.NoError(t, repo.CreateContentBlock(ctx, &MessageContentBlock{
			ID: uuid.New().String(), MessageID: messageID, Position: 0,
			BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			ToolCallID: ptr.Of(toolCallID), ToolName: ptr.Of("shell"), Version: ptr.Of(1),
			CreatedAt: now, UpdatedAt: now,
		}))
		require.NoError(t, UpsertToolCallStatus(ctx, repo, &core.ToolCall{
			ID: toolCallID, ChatID: chatID, ThreadID: &parentThreadID, MessageID: &messageID,
			ToolName: "shell", Status: core.ToolCallStatusCompleted, CompletedAt: &now,
			RequestedAt: now, CreatedAt: now, UpdatedAt: now,
		}))
		require.NoError(t, repo.UpsertToolCallResult(ctx, &ToolCallResult{
			ToolCallID: toolCallID, MessageID: &messageID, Content: "ok",
			CreatedAt: now, UpdatedAt: now,
		}))

		agentMessageID := uuid.New().String()
		enqueueTestAgentMessage(t, repo, ctx, agentMessageID, chatID, parentThreadID, childThreadID, "m", now)

		if decoy {
			continue
		}
		fx.attachmentIDs = append(fx.attachmentIDs, attachmentID)
		fx.messageIDs = append(fx.messageIDs, messageID)
		fx.agentMessageIDs = append(fx.agentMessageIDs, agentMessageID)
	}

	fx.envelopeMessageID = seedDeliveredMessage(t, repo, ctx, chatID, childThreadID)
	return fx
}
