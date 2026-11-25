// Copyright (c) 2025 Reliant Labs
package chataccess_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// members answers IsMember from a (user/org) table; an absent pair is not a member.
type members map[string]bool

func (m members) IsMember(_ context.Context, userID, orgID string) (bool, bool, error) {
	return m[userID+"/"+orgID], true, nil
}

type outage struct{}

func (outage) IsMember(context.Context, string, string) (bool, bool, error) {
	return false, true, errors.New("control-plane unavailable")
}

// ownerStore is the owner-filtered read the real store does: a chat is
// returned only to its owner, else core.ErrChatNotFound.
type ownerStore struct{ rows map[string]*core.ChatRow }

func (s ownerStore) GetChatRowWithUserCheck(_ context.Context, id, userID string) (*core.ChatRow, error) {
	if c, ok := s.rows[id]; ok && c.UserID == userID {
		return c, nil
	}
	return nil, core.ErrChatNotFound
}

func (s ownerStore) GetChatWithUserCheck(ctx context.Context, id, userID string) (*core.Chat, error) {
	row, err := s.GetChatRowWithUserCheck(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	return &core.Chat{ID: row.ID, UserID: row.UserID, OrgID: row.OrgID, ProjectDeleted: row.ProjectDeleted}, nil
}

func org(s string) *string { return &s }

func newService() chataccess.Service {
	return chataccess.New(chataccess.Deps{Store: ownerStore{rows: map[string]*core.ChatRow{
		"mine":    {ID: "mine", UserID: "alice"},
		"in-acme": {ID: "in-acme", UserID: "alice", OrgID: org("acme")},
		"theirs":  {ID: "theirs", UserID: "bob", OrgID: org("acme")},
		"deleted": {ID: "deleted", UserID: "alice", ProjectDeleted: true},
	}}})
}

// reads is both reads of the service, so every case below runs against each.
// A refused read returns no row.
func reads(svc chataccess.Service) map[string]func(t *testing.T, ctx context.Context, id, userID string) error {
	return map[string]func(t *testing.T, ctx context.Context, id, userID string) error{
		"row": func(t *testing.T, ctx context.Context, id, userID string) error {
			got, err := svc.GetChatRowWithUserCheck(ctx, id, userID)
			if err != nil {
				assert.Nil(t, got)
			}
			return err
		},
		"view": func(t *testing.T, ctx context.Context, id, userID string) error {
			got, err := svc.GetChatWithUserCheck(ctx, id, userID)
			if err != nil {
				assert.Nil(t, got)
			}
			return err
		},
	}
}

// Another tenant's chat, a missing one and one in a deleted project are the
// same answer, NotFound, so chat ids cannot be probed across users or orgs.
func TestChatAccess_CrossTenantIsNotFound(t *testing.T) {
	t.Parallel()
	ctx := authz.WithMembership(context.Background(), members{"alice/acme": true, "bob/acme": true})

	for name, read := range reads(newService()) {
		for what, id := range map[string]string{"another user's": "theirs", "missing": "nope", "in a deleted project": "deleted"} {
			t.Run(name+"/"+what, func(t *testing.T) {
				err := read(t, ctx, id, "alice")
				require.Error(t, err)
				assert.ErrorIs(t, err, core.ErrChatNotFound)
			})
		}
		t.Run(name+"/own chats read", func(t *testing.T) {
			require.NoError(t, read(t, ctx, "mine", "alice"))
			require.NoError(t, read(t, ctx, "in-acme", "alice"))
		})
	}
}

// An owner who left the chat's org, an outage of the membership answer, and a
// credential bound to another org are all NotFound, never a read.
func TestChatAccess_OrgRefusals(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]context.Context{
		"the owner left the org":      authz.WithMembership(context.Background(), members{"alice/home": true}),
		"membership outage":           authz.WithMembership(context.Background(), outage{}),
		"membership never wired":      context.Background(),
		"credential of another org":   authz.WithCredentialOrg(authz.WithMembership(context.Background(), members{"alice/acme": true, "alice/globex": true}), "globex"),
		"another user's, same org":    authz.WithMembership(context.Background(), members{"alice/acme": true, "bob/acme": true}),
		"the viewer is not the owner": authz.WithMembership(context.Background(), members{"alice/acme": true, "bob/acme": true}),
	} {
		for readName, read := range reads(newService()) {
			t.Run(name+"/"+readName, func(t *testing.T) {
				id, user := "in-acme", "alice"
				if name == "another user's, same org" || name == "the viewer is not the owner" {
					user = "bob" // bob is a member of acme but does not own the chat
				}
				err := read(t, tc, id, user)
				require.Error(t, err)
				assert.ErrorIs(t, err, core.ErrChatNotFound)
			})
		}
	}
}

// An empty id or caller never reaches the store: an empty owner would
// otherwise be a filter that matches no, or every, row.
func TestChatAccess_EmptyScopeFailsClosed(t *testing.T) {
	t.Parallel()
	svc := chataccess.New(chataccess.Deps{Store: failingStore{t: t}})
	ctx := context.Background()
	for name, read := range reads(svc) {
		t.Run(name, func(t *testing.T) {
			require.Error(t, read(t, ctx, "", "alice"))
			require.Error(t, read(t, ctx, "mine", ""))
		})
	}
}

type failingStore struct{ t *testing.T }

func (s failingStore) GetChatRowWithUserCheck(context.Context, string, string) (*core.ChatRow, error) {
	s.t.Error("the store was read with an empty scope")
	return nil, core.ErrChatNotFound
}

func (s failingStore) GetChatWithUserCheck(context.Context, string, string) (*core.Chat, error) {
	s.t.Error("the store was read with an empty scope")
	return nil, core.ErrChatNotFound
}

// Over a real database: the owner filter of the store and authz's org rule
// hold together through NewFromORM, which is what production wires.
func TestChatAccess_CrossTenantOverTheRealStore(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	svc := chataccess.NewFromORM(repo.ORM())

	now := time.Now().UTC().Truncate(time.Millisecond)
	chat := &db.Chat{ID: uuid.NewString(), Title: "t", ProjectID: "test-project", UserID: "owner", CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, repo.CreateChat(ctx, chat))

	for name, read := range reads(svc) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, read(t, ctx, chat.ID, "owner"))
			for who, user := range map[string]string{"another user": "someone-else"} {
				err := read(t, ctx, chat.ID, user)
				require.Error(t, err, who)
				assert.ErrorIs(t, err, core.ErrChatNotFound)
			}
			err := read(t, ctx, uuid.NewString(), "owner")
			assert.ErrorIs(t, err, core.ErrChatNotFound)
		})
	}
}
