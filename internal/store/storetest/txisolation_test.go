// Copyright (c) 2025 Reliant Labs
package storetest_test

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"testing"
	"time"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/crypto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/vault"
)

// TestTransactionOpenersBeginAtTheirLevel pins the isolation level each
// transaction opener puts on the wire, as observed by the server-facing
// driver rather than read off an options struct. The levels are the pre-P5b
// ones (the hand SQL and Repository transactions the ORM port replaced),
// except where a row says otherwise; internal/db/SCHEMA.md rule 7 is why a
// port must keep them. A port that silently moves a lock-based transaction
// onto the ORM's SERIALIZABLE default fails here, by name.
func TestTransactionOpenersBeginAtTheirLevel(t *testing.T) {
	env := storetest.New(t)
	newChat(t, env, "chat-iso")
	now := time.Now().UTC()
	if err := store.InsertWorktree(context.Background(), env.ORM, &core.Worktree{
		ID: "wt-iso", Name: "iso", Path: "/wt/iso", Branch: "feat/iso", BaseBranch: "main",
		ProjectID: storetest.SeedProjectID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("seed worktree: %v", err)
	}
	p := env.Probe(t)
	noop := func(context.Context) error { return nil }
	// Warm the probe vault's DEK for the seed user, so the account rows below
	// begin only their own transaction: a tenant's first seal adds the
	// vault's detached ones (pinned by their own subtest at the end).
	if _, _, err := p.Repo.UpsertProviderAccount(context.Background(), core.AccountUpsert{
		UserID: storetest.SeedUserID, Provider: "openai", AuthKind: core.AuthKindAPIKey,
		IdentityKey: "iso-warm", Secrets: core.AccountSecrets{Secret: "sk-warm"},
	}); err != nil {
		t.Fatalf("warm the vault: %v", err)
	}

	const (
		serializable  = "serializable"
		readCommitted = "read committed"
	)
	cases := []struct {
		name string
		want string
		run  func(ctx context.Context) error
	}{
		// The Repository's own transactions: SERIALIZABLE unless asked.
		{"Repo.RunTx", serializable, func(ctx context.Context) error { return p.Repo.RunTx(ctx, noop) }},
		{"Repo.RunTxReadOnly", serializable, func(ctx context.Context) error { return p.Repo.RunTxReadOnly(ctx, noop) }},
		{"Repo.RunTxNoRetry", serializable, func(ctx context.Context) error { return p.Repo.RunTxNoRetry(ctx, noop) }},
		{"Repo.RunTxWithOptions(read committed)", readCommitted, func(ctx context.Context) error {
			return p.Repo.RunTxWithOptions(ctx, db.TxOptions{Isolation: db.IsolationReadCommitted}, noop)
		}},
		// The ORM's default, which every lock-based port must override.
		{"orm.Client.RunTx (forge default)", serializable, func(ctx context.Context) error { return p.ORM.RunTx(ctx, noop) }},

		// Serializable Repository writers.
		{"Repo.CreateChatUpdate", serializable, func(ctx context.Context) error {
			return p.Repo.CreateChatUpdate(ctx, "chat-iso", reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE, "e", `{}`)
		}},
		{"Repo.MutateChatSelection", serializable, func(ctx context.Context) error {
			_, err := p.Repo.MutateChatSelection(ctx, "chat-iso", func(cur *chatselection.Selection) (*chatselection.Selection, error) { return cur, nil })
			return err
		}},
		// An upsert plus a stale-row delete; the 40001 retry is harmless
		// (no effect outside the database).
		{"Repo.ReplaceIntegrationAccess", serializable, func(ctx context.Context) error {
			return p.Repo.ReplaceIntegrationAccess(ctx, storetest.SeedUserID, "github", "", now, nil)
		}},

		// READ COMMITTED: correctness from row/advisory locks or a single
		// allocating statement, never from snapshot isolation.
		{"Repo.CreateUserUpdate", readCommitted, func(ctx context.Context) error {
			return p.Repo.CreateUserUpdate(ctx, &db.UserUpdate{
				UserID: storetest.SeedUserID, UpdateType: db.UserUpdateChatActivityChanged,
				EntityType: db.EntityTypeChat, EntityID: "chat-iso", Data: []byte(`{}`),
			})
		}},
		{"Repo.RemoveDaemon", readCommitted, func(ctx context.Context) error {
			_, _, err := p.Repo.RemoveDaemon(ctx, "daemon-none")
			return err
		}},
		{"Repo.CreateWorkflowDraft", readCommitted, func(ctx context.Context) error {
			return p.Repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
				ID: "wd-iso", UserID: storetest.SeedUserID, Name: "Iso", Slug: "iso",
				Definition: "name: iso\n", Status: core.WorkflowDraftStatusDraft, CreatedAt: now, UpdatedAt: now,
			})
		}},
		{"Repo.MergeWorktreeCleanupMetadata", readCommitted, func(ctx context.Context) error {
			return p.Repo.MergeWorktreeCleanupMetadata(ctx, "wt-iso", func(*core.CleanupMetadata, bool) bool { return false })
		}},
		{"Repo.LockChatRunControl", readCommitted, func(ctx context.Context) error {
			release, err := p.Repo.LockChatRunControl(ctx, "chat-iso")
			if err == nil {
				release()
			}
			return err
		}},
		{"Repo.LockProviderKey", readCommitted, func(ctx context.Context) error {
			release, err := p.Repo.LockProviderKey(ctx, storetest.SeedUserID, "anthropic")
			if err == nil {
				release()
			}
			return err
		}},
		// The accounts store (AccountStore.inTx): an advisory lock per (user,
		// provider), then read-then-write; never retried, since it seals
		// with fresh randomness under the locks it took.
		{"Repo.UpsertProviderAccount (AccountStore.inTx)", readCommitted, func(ctx context.Context) error {
			_, _, err := p.Repo.UpsertProviderAccount(ctx, core.AccountUpsert{
				UserID: storetest.SeedUserID, Provider: "openai", AuthKind: core.AuthKindAPIKey,
				IdentityKey: "iso-key", Secrets: core.AccountSecrets{Secret: "sk-iso"},
			})
			return err
		}},
		{"Repo.ReorderProviderAccounts (AccountStore.inTx)", readCommitted, func(ctx context.Context) error {
			return p.Repo.ReorderProviderAccounts(ctx, storetest.SeedUserID, "gemini", nil)
		}},
		// The self-hosted token store's rotation (LocalStore.MintForUser with
		// Rotate): READ COMMITTED as database/sql's BeginTx(nil) was, never
		// retried (MaxAttempts 1) — the advisory lock serializes rotations,
		// and a retry would mint a second token.
		{"tokenauthority.LocalStore.MintForUser (rotate)", readCommitted, func(ctx context.Context) error {
			_, err := tokenauthority.NewLocalStore(p.ORM).MintForUser(ctx, tokenauthority.MintRequest{
				UserID: storetest.SeedUserID, Name: "iso", Scopes: []fat.Scope{fat.ScopeReliantAPI}, Rotate: true,
			})
			return err
		}},
		// The connection store (connectionTx), which also never retries: the
		// secrets lock refreshes a token over the network.
		{"store.SoftDeleteConnection (connectionTx)", readCommitted, func(ctx context.Context) error {
			err := store.SoftDeleteConnection(ctx, p.ORM, storetest.SeedUserID, "conn-none", core.ConnectionEvent{Kind: "deleted", Actor: "test", At: now})
			if errors.Is(err, core.ErrConnectionNotFound) {
				return nil // the transaction was begun; there was just nothing to delete
			}
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p.Reset()
			if err := c.run(context.Background()); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if got := p.Isolations(); !slices.Equal(got, []string{c.want}) {
				t.Fatalf("%s began transactions at %v, want exactly one at %s", c.name, got, c.want)
			}
		})
	}

	// A transaction joined is not begun: an inner opener inherits the outer
	// transaction's level, whatever it asks for (RunTxWithOptions).
	t.Run("a read committed opener inside Repo.RunTx joins it", func(t *testing.T) {
		p.Reset()
		err := p.Repo.RunTx(context.Background(), func(ctx context.Context) error {
			return store.MergeWorktreeCleanupMetadata(ctx, p.ORM, "wt-iso", func(*core.CleanupMetadata, bool) bool { return false })
		})
		if err != nil {
			t.Fatalf("Repo.RunTx: %v", err)
		}
		if got := p.Isolations(); !slices.Equal(got, []string{serializable}) {
			t.Fatalf("began %v, want only the outer [serializable]", got)
		}
	})

	// The vault never joins either: a tenant's first DEK, sealed inside a
	// caller's transaction, is read, written and re-read on detached READ
	// COMMITTED transactions of its own, so the caller's rollback cannot take
	// back a key the vault already sealed under. Outside a transaction its
	// statements run on the pool and begin nothing.
	t.Run("the vault's DEK statements inside Repo.RunTx detach at read committed", func(t *testing.T) {
		ring, err := crypto.ParseKeyring("v1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)))
		if err != nil {
			t.Fatal(err)
		}
		v := vault.New(vault.Deps{DB: p.ORM, Keys: vault.NewEnvKeyWrapper(ring)})
		p.Reset()
		var sealed []byte
		err = p.Repo.RunTx(context.Background(), func(ctx context.Context) error {
			var err error
			sealed, err = v.Seal(ctx, vault.UserTenant("iso-tenant"), []byte("x"), []byte("a"))
			return err
		})
		if err != nil {
			t.Fatalf("Seal inside Repo.RunTx: %v", err)
		}
		if got := p.Isolations(); !slices.Equal(got, []string{serializable, readCommitted, readCommitted, readCommitted}) {
			t.Fatalf("began %v, want the outer [serializable] then three detached [read committed] (read, create, re-read)", got)
		}
		p.Reset()
		fresh := vault.New(vault.Deps{DB: p.ORM, Keys: vault.NewEnvKeyWrapper(ring)})
		if _, err := fresh.Open(context.Background(), vault.UserTenant("iso-tenant"), sealed, []byte("a")); err != nil {
			t.Fatalf("Open outside a transaction: %v", err)
		}
		if got := p.Isolations(); len(got) != 0 {
			t.Fatalf("outside a transaction the vault began %v, want none", got)
		}
	})

	// The write ledger never joins: inside a transaction each of its
	// statements runs detached (orm.Detached), on the pool as its own
	// autocommit transaction. It begins none, and the caller's rollback
	// cannot take back an entry it wrote.
	t.Run("the write ledger inside Repo.RunTx runs outside it", func(t *testing.T) {
		p.Reset()
		rollback := errors.New("roll the caller back")
		err := p.Repo.RunTx(context.Background(), func(ctx context.Context) error {
			if err := p.Repo.IntegrationWrites().CreateIntegrationWrite(ctx, &core.IntegrationWrite{
				ID: "iw-iso", UserID: "iso-user", ChatID: "iso-chat", Integration: "github", Action: "issue.comment",
				Actor: core.IntegrationActorOwner, Scope: "github:acme/app", Target: "github:acme/app",
			}); err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("Repo.RunTx: %v, want the caller's own error", err)
		}
		if got := p.Isolations(); !slices.Equal(got, []string{serializable}) {
			t.Fatalf("began %v, want only the outer [serializable]: the ledger begins none", got)
		}
		var n int
		if err := env.SQL.QueryRow(`SELECT count(*) FROM integration_writes WHERE id = 'iw-iso'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("the ledger entry written inside the rolled-back transaction: %d rows, want 1", n)
		}
	})
}
