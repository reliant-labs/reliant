package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/store"
)

// TestAttachmentOrgColumn proves, against a database, what
// db/migrations/20261127161000_attachments_org_id (and its validate and index
// files) enforce: org_id is nullable, so a legacy row with none is accepted;
// a blank org_id is refused by attachments_org_id_not_blank, validated; the
// (user_id, org_id) index exists; and CreateAttachment writes the org through.
func TestAttachmentOrgColumn(t *testing.T) {
	t.Parallel()

	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()

	insert := func(id string, org any) error {
		_, err := raw.Exec(`INSERT INTO attachments (id, user_id, filename, size, mime_type, file_path, created_at, updated_at, attachment_type, org_id)
			VALUES ($1, 'u1', 'a.png', 1, 'image/png', 'u1/a.png', now(), now(), 'image', $2)`, id, org)
		return err
	}

	require.NoError(t, insert("legacy", nil), "a row with no org (every legacy row) is accepted")
	require.NoError(t, insert("in-org", "org-1"))

	err := insert("blank", "")
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "a blank org_id is refused: %v", err)
	assert.Equal(t, "23514", pgErr.Code, "check_violation")
	assert.Equal(t, store.AttachmentConstraintOrgIDNotBlank, pgErr.ConstraintName)

	var validated bool
	require.NoError(t, raw.QueryRow(`SELECT convalidated FROM pg_constraint WHERE conname = 'attachments_org_id_not_blank'`).Scan(&validated))
	assert.True(t, validated, "the validate migration validated the CHECK")

	var indexDef string
	require.NoError(t, raw.QueryRow(`SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_attachments_user_org'`).Scan(&indexDef))
	assert.Contains(t, indexDef, "(user_id, org_id)")

	now := time.Now().UTC()
	require.NoError(t, repo.CreateAttachment(context.Background(), &Attachment{
		ID: "created", UserID: "u1", Filename: "b.png", Size: 1, MimeType: "image/png", FilePath: "u1/b.png",
		AttachmentType: "image", OrgID: ptr.Of("org-1"), CreatedAt: now, UpdatedAt: now,
	}))
	got, err := repo.GetAttachment(context.Background(), "created")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, ptr.Of("org-1"), got.OrgID, "CreateAttachment records the org")

	legacy, err := repo.GetAttachment(context.Background(), "legacy")
	require.NoError(t, err)
	require.NotNil(t, legacy)
	assert.Nil(t, legacy.OrgID, "a legacy row reads back with no org")
}

// TestGetAttachmentForUserInOrg proves the org-scoped read matches only the
// owner's attachment made in the org it names: never another user's, another
// org's, one made in no org, or a blank argument.
func TestGetAttachmentForUserInOrg(t *testing.T) {
	t.Parallel()

	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	seed := func(id, user string, org *string) {
		t.Helper()
		require.NoError(t, repo.CreateAttachment(ctx, &Attachment{
			ID: id, UserID: user, Filename: id + ".png", Size: 1, MimeType: "image/png", FilePath: user + "/" + id,
			AttachmentType: "image", Content: []byte{1}, OrgID: org, CreatedAt: now, UpdatedAt: now,
		}))
	}
	seed("mine-in-org", "alice", ptr.Of("org-1"))
	seed("mine-other-org", "alice", ptr.Of("org-2"))
	seed("mine-no-org", "alice", nil)
	seed("bobs-in-org", "bob", ptr.Of("org-1"))

	got, err := store.GetAttachmentForUserInOrg(ctx, repo.ORM(), "alice", "org-1", "mine-in-org")
	require.NoError(t, err)
	require.NotNil(t, got, "the owner's attachment made in the org")
	assert.Equal(t, "mine-in-org", got.ID)
	assert.Equal(t, ptr.Of("org-1"), got.OrgID)
	assert.Equal(t, []byte{1}, got.Content)

	for name, c := range map[string]struct{ user, org, id string }{
		"another org's":           {"alice", "org-1", "mine-other-org"},
		"one made in no org":      {"alice", "org-1", "mine-no-org"},
		"another user's":          {"alice", "org-1", "bobs-in-org"},
		"one that does not exist": {"alice", "org-1", "missing"},
		"a blank user":            {"", "org-1", "mine-in-org"},
		"a blank org":             {"alice", "", "mine-no-org"},
		"a blank id":              {"alice", "org-1", ""},
	} {
		got, err := store.GetAttachmentForUserInOrg(ctx, repo.ORM(), c.user, c.org, c.id)
		require.NoError(t, err, name)
		assert.Nil(t, got, "%s is not found", name)
	}
}
