// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for ProviderAccount. The generated provider_account_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: providerAccountRepo (a *crud.Repo[ProviderAccount]) and the
//     package-level GetProviderAccountByID / ListProviderAccount / CreateProviderAccount / UpdateProviderAccount / DeleteProviderAccount
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     ProviderAccount struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindProviderAccountBy(ctx context.Context, db orm.Context, /* args */) ([]*ProviderAccount, error) {
//		var rows []*ProviderAccount
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("user_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveProviderAccounts(ctx context.Context, db orm.Context) ([]*ProviderAccount, error) {
//		return ListProviderAccount(ctx, db, orm.WhereEq("user_id", true))
//	}
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Provider accounts (P5b lane L6). The statements of internal/db's accountStore, which
// keeps everything that is not SQL: sealing and opening the secret columns (it holds the vault,
// and binds each ciphertext to its account id and column), the advisory-lock-then-read-then-write
// transactions, the identity merge, and the compare-and-swap loop. Nothing here ever sees a
// plaintext secret: the sealed columns cross this boundary as ciphertext.

// SealedProviderAccount is one provider_accounts row: its core metadata plus the sealed columns,
// still ciphertext.
type SealedProviderAccount struct {
	core.ProviderAccount
	Secret, Refresh, IDToken []byte
}

// ProviderAccountCredential is one credential write: the account's identity, its metadata, and
// its secrets already sealed. On an insert an empty string (or nil ciphertext, or a zero
// ExpiresAt) is NULL. On a rewrite an empty metadata string, a nil Refresh or IDToken and an
// empty LegacySource keep what the row holds — a re-consent that omits a refresh token must not
// erase the one held — while Secret and ExpiresAt are always written.
type ProviderAccountCredential struct {
	AuthKind                                                   core.AuthKind
	IdentityKey                                                string
	Email, ExternalAccountID, OrganizationID, OrganizationName string
	Plan, PlanRaw, KeyHint, Scope                              string
	Secret, Refresh, IDToken                                   []byte
	ExpiresAt                                                  time.Time
	LegacySource                                               string
	UpdatedAt                                                  time.Time
}

// providerAccountSealedColumns stay on disk for a metadata read; legacy_source is read only by
// the legacy copy (ProviderAccountLegacySource).
var providerAccountSealedColumns = []string{
	ProviderAccountFieldSecretSealed, ProviderAccountFieldRefreshSealed,
	ProviderAccountFieldIDTokenSealed, ProviderAccountFieldLegacySource,
}

// providerAccountMetaColumns is what a write returns: every column core.ProviderAccount carries.
const providerAccountMetaColumns = `id, user_id, provider, auth_kind, identity_key, label, email, external_account_id,
	organization_id, organization_name, plan, plan_raw, key_hint, priority, paused_at, needs_reauth_at,
	needs_reauth_reason, expires_at, scope, created_at, updated_at`

func providerAccountToCore(r *ProviderAccount) core.ProviderAccount {
	a := core.ProviderAccount{
		ID: r.ID, UserID: r.UserID, Provider: r.Provider, AuthKind: r.AuthKind, IdentityKey: r.IdentityKey,
		Label: r.Label, Email: acctText(r.Email), ExternalAccountID: acctText(r.ExternalAccountID),
		OrganizationID: acctText(r.OrganizationID), OrganizationName: acctText(r.OrganizationName),
		Plan: acctText(r.Plan), PlanRaw: acctText(r.PlanRaw), KeyHint: acctText(r.KeyHint),
		Priority: int(r.Priority), PausedAt: r.PausedAt, NeedsReauthAt: r.NeedsReauthAt,
		NeedsReauthReason: acctText(r.NeedsReauthReason), Scope: acctText(r.Scope),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.ExpiresAt != nil {
		a.ExpiresAt = *r.ExpiresAt
	}
	return a
}

func sealedProviderAccountToCore(r *ProviderAccount) SealedProviderAccount {
	return SealedProviderAccount{
		ProviderAccount: providerAccountToCore(r),
		Secret:          r.SecretSealed, Refresh: r.RefreshSealed, IDToken: r.IDTokenSealed,
	}
}

// acctText reads a nullable text column; NULL is "".
func acctText(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// acctNullText writes a nullable text column; "" is NULL.
func acctNullText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// acctNullTime writes a nullable timestamp column; the zero time is NULL.
func acctNullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

// acctUTC normalizes an optional timestamp argument.
func acctUTC(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func userProviderAccountsOrder(userID string) []orm.QueryOption {
	return []orm.QueryOption{
		orm.WhereEq(ProviderAccountFieldUserID, userID),
		orm.WithOrderBy(ProviderAccountFieldProvider, orm.Asc),
		orm.WithOrderBy(ProviderAccountFieldPriority, orm.Asc),
	}
}

// ListUserProviderAccounts returns the user's accounts, metadata only, by (provider, priority).
// The builder rather than ListProviderAccount, so the ciphertext stays on disk.
func ListUserProviderAccounts(ctx context.Context, db orm.Context, userID string) ([]core.ProviderAccount, error) {
	var rows []*ProviderAccount
	if err := db.Bun().NewSelect().Model(&rows).ExcludeColumn(providerAccountSealedColumns...).
		Where("user_id = ?", userID).Order("provider", "priority").Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]core.ProviderAccount, 0, len(rows))
	for _, r := range rows {
		out = append(out, providerAccountToCore(r))
	}
	return out, nil
}

// ListUserProviderAccountsSealed returns the user's accounts with their sealed columns, by
// (provider, priority).
func ListUserProviderAccountsSealed(ctx context.Context, db orm.Context, userID string) ([]SealedProviderAccount, error) {
	rows, err := ListProviderAccount(ctx, db, userProviderAccountsOrder(userID)...)
	if err != nil {
		return nil, err
	}
	out := make([]SealedProviderAccount, 0, len(rows))
	for _, r := range rows {
		out = append(out, sealedProviderAccountToCore(r))
	}
	return out, nil
}

// GetUserProviderAccountSealed returns one of the user's accounts with its sealed columns, or
// core.ErrProviderAccountNotFound when the user has no account with that id.
func GetUserProviderAccountSealed(ctx context.Context, db orm.Context, userID, accountID string) (*SealedProviderAccount, error) {
	rows, err := ListProviderAccount(ctx, db,
		orm.WhereEq(ProviderAccountFieldID, accountID), orm.WhereEq(ProviderAccountFieldUserID, userID), orm.WithLimit(1))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, core.ErrProviderAccountNotFound
	}
	a := sealedProviderAccountToCore(rows[0])
	return &a, nil
}

// LockUserProviderAccounts serializes writers of one user's accounts for one provider until the
// transaction ctx carries ends. The key is shared with every release that writes these rows, so
// its text must never change.
func LockUserProviderAccounts(ctx context.Context, db orm.Context, userID, provider string) error {
	name := fmt.Sprintf("provider-accounts:%d:%s:%s", len(userID), userID, provider)
	if _, err := db.Bun().NewRaw(`SELECT pg_advisory_xact_lock(hashtextextended(?0, 0))`, name).Exec(ctx); err != nil {
		return fmt.Errorf("lock provider accounts: %w", err)
	}
	return nil
}

// lockProviderAccount reads one account's metadata FOR UPDATE; nil when no row matches.
func lockProviderAccount(ctx context.Context, db orm.Context, where func(*bun.SelectQuery) *bun.SelectQuery) (*core.ProviderAccount, error) {
	var row ProviderAccount
	q := db.Bun().NewSelect().Model(&row).ExcludeColumn(providerAccountSealedColumns...)
	err := where(q).For("UPDATE").Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a := providerAccountToCore(&row)
	return &a, nil
}

// LockProviderAccountByIdentity locks the user's account of provider with that identity; nil
// when there is none.
func LockProviderAccountByIdentity(ctx context.Context, db orm.Context, userID, provider, identity string) (*core.ProviderAccount, error) {
	return lockProviderAccount(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("user_id = ?", userID).Where("provider = ?", provider).Where("identity_key = ?", identity)
	})
}

// LockUserProviderAccount locks one of the user's accounts, or returns
// core.ErrProviderAccountNotFound.
func LockUserProviderAccount(ctx context.Context, db orm.Context, userID, accountID string) (*core.ProviderAccount, error) {
	a, err := lockProviderAccount(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("id = ?", accountID).Where("user_id = ?", userID)
	})
	if err == nil && a == nil {
		return nil, core.ErrProviderAccountNotFound
	}
	return a, err
}

// LockLegacyMirror locks the first (by priority) of the user's accounts of provider that the
// legacy copy made from source; nil when there is none.
func LockLegacyMirror(ctx context.Context, db orm.Context, userID, provider, source string) (*core.ProviderAccount, error) {
	return lockProviderAccount(ctx, db, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("user_id = ?", userID).Where("provider = ?", provider).Where("legacy_source = ?", source).
			Order("priority").Limit(1)
	})
}

// ProviderAccountLegacySource returns the legacy table the account was copied from, or nil for
// an account connected natively.
func ProviderAccountLegacySource(ctx context.Context, db orm.Context, accountID string) (*string, error) {
	row, err := GetProviderAccountByID(ctx, db, accountID)
	if err != nil {
		return nil, err
	}
	return row.LegacySource, nil
}

// NextProviderAccountPriority is the priority a new account of provider takes: after the last.
func NextProviderAccountPriority(ctx context.Context, db orm.Context, userID, provider string) (int, error) {
	var n int
	err := db.Bun().NewSelect().Model((*ProviderAccount)(nil)).ColumnExpr("COALESCE(MAX(priority), -1) + 1").
		Where("user_id = ?", userID).Where("provider = ?", provider).Scan(ctx, &n)
	return n, err
}

// InsertProviderAccount writes a new account at priority, created at createdAt, and returns it
// as stored.
func InsertProviderAccount(ctx context.Context, db orm.Context, id, userID, provider string, priority int, c ProviderAccountCredential, createdAt time.Time) (core.ProviderAccount, error) {
	row := &ProviderAccount{
		ID: id, UserID: userID, Provider: provider, AuthKind: c.AuthKind, IdentityKey: c.IdentityKey,
		Email: acctNullText(c.Email), ExternalAccountID: acctNullText(c.ExternalAccountID),
		OrganizationID: acctNullText(c.OrganizationID), OrganizationName: acctNullText(c.OrganizationName),
		Plan: acctNullText(c.Plan), PlanRaw: acctNullText(c.PlanRaw), KeyHint: acctNullText(c.KeyHint),
		Priority: int32(priority), SecretSealed: c.Secret, RefreshSealed: c.Refresh, IDTokenSealed: c.IDToken,
		ExpiresAt: acctNullTime(c.ExpiresAt), Scope: acctNullText(c.Scope), LegacySource: acctNullText(c.LegacySource),
		CreatedAt: createdAt.UTC(), UpdatedAt: c.UpdatedAt.UTC(),
	}
	// RETURNING, so the account carries the stored (microsecond) timestamps, as a read would.
	if err := db.Bun().NewInsert().Model(row).Returning(providerAccountMetaColumns).Scan(ctx); err != nil {
		return core.ProviderAccount{}, err
	}
	return providerAccountToCore(row), nil
}

// RewriteProviderAccountCredential rewrites one of the user's accounts in place with a fresh
// credential (see ProviderAccountCredential for what an empty field keeps) and clears its
// needs-reauth mark. Label, priority and pause are the user's and are not touched.
func RewriteProviderAccountCredential(ctx context.Context, db orm.Context, userID, accountID string, c ProviderAccountCredential) (core.ProviderAccount, error) {
	var row ProviderAccount
	err := db.Bun().NewRaw(`UPDATE provider_accounts SET
			identity_key = ?2,
			auth_kind = ?3,
			email = COALESCE(?4, email),
			external_account_id = COALESCE(?5, external_account_id),
			organization_id = COALESCE(?6, organization_id),
			organization_name = COALESCE(?7, organization_name),
			plan = COALESCE(?8, plan),
			plan_raw = COALESCE(?9, plan_raw),
			key_hint = COALESCE(?10, key_hint),
			secret_sealed = ?11,
			refresh_sealed = COALESCE(?12, refresh_sealed),
			id_token_sealed = COALESCE(?13, id_token_sealed),
			expires_at = ?14,
			scope = COALESCE(?15, scope),
			needs_reauth_at = NULL,
			needs_reauth_reason = NULL,
			legacy_source = COALESCE(?16, legacy_source),
			updated_at = ?17
		WHERE id = ?0 AND user_id = ?1
		RETURNING `+providerAccountMetaColumns,
		accountID, userID, c.IdentityKey, string(c.AuthKind), acctNullText(c.Email), acctNullText(c.ExternalAccountID),
		acctNullText(c.OrganizationID), acctNullText(c.OrganizationName), acctNullText(c.Plan), acctNullText(c.PlanRaw),
		acctNullText(c.KeyHint), c.Secret, c.Refresh, c.IDToken, acctNullTime(c.ExpiresAt), acctNullText(c.Scope),
		acctNullText(c.LegacySource), c.UpdatedAt.UTC()).Scan(ctx, &row)
	if err != nil {
		return core.ProviderAccount{}, err
	}
	return providerAccountToCore(&row), nil
}

// MoveThreadAssignments hands the threads assigned to account from over to account to, except
// a thread that already has to for the same provider (that row stays on from, and cascades
// away with it).
func MoveThreadAssignments(ctx context.Context, db orm.Context, from, to string) error {
	_, err := db.Bun().NewRaw(`UPDATE thread_provider_accounts t SET account_id = ?0
		WHERE t.account_id = ?1 AND NOT EXISTS (
			SELECT 1 FROM thread_provider_accounts o
			WHERE o.thread_id = t.thread_id AND o.provider = t.provider AND o.account_id = ?0)`, to, from).Exec(ctx)
	return err
}

// RemoveProviderAccount deletes the account by id. A missing one is not an error.
func RemoveProviderAccount(ctx context.Context, db orm.Context, accountID string) error {
	if err := DeleteProviderAccount(ctx, db, accountID); err != nil && !errors.Is(err, orm.ErrNoRows) {
		return err
	}
	return nil
}

// SetProviderAccountStanding sets the account's priority, label and pause, and nothing else
// (not updated_at: the merge that calls it rewrites the credential next, which stamps it).
func SetProviderAccountStanding(ctx context.Context, db orm.Context, accountID string, priority int, label string, pausedAt *time.Time) error {
	_, err := db.Bun().NewUpdate().Model((*ProviderAccount)(nil)).
		Set("priority = ?", orm.Arg(priority)).Set("label = ?", label).Set("paused_at = ?", acctUTC(pausedAt)).
		Where("id = ?", accountID).Exec(ctx)
	return err
}

// DensifyProviderAccounts renumbers the user's priorities for provider to 0..n-1, keeping their
// order. The unique constraint on priority is deferred, so the intermediate states are allowed.
func DensifyProviderAccounts(ctx context.Context, db orm.Context, userID, provider string) error {
	_, err := db.Bun().NewRaw(`UPDATE provider_accounts p SET priority = r.rn
		FROM (SELECT id, (row_number() OVER (ORDER BY priority, created_at, id) - 1)::int AS rn
		      FROM provider_accounts WHERE user_id = ?0 AND provider = ?1) r
		WHERE p.id = r.id AND p.priority <> r.rn`, userID, provider).Exec(ctx)
	return err
}

// ProviderAccountWitness reads the account's sealed secret and refresh columns: the witness a
// compare-and-swap conditions its write on. found=false when the user has no such account.
func ProviderAccountWitness(ctx context.Context, db orm.Context, userID, accountID string) (secret, refresh []byte, found bool, err error) {
	var row ProviderAccount
	err = db.Bun().NewSelect().Model(&row).Column(ProviderAccountFieldSecretSealed, ProviderAccountFieldRefreshSealed).
		Where("id = ?", accountID).Where("user_id = ?", userID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	return row.SecretSealed, row.RefreshSealed, true, nil
}

// SwapProviderAccountTokens writes next's tokens and the metadata a refresh may learn (email,
// organization name, plan), only while the row's sealed secret and refresh columns still equal
// the witness. Every write re-seals the secret, so the witness changes whenever the row does:
// of two writers holding one witness exactly one matches. It reports whether it wrote.
func SwapProviderAccountTokens(ctx context.Context, db orm.Context, userID, accountID string, next ProviderAccountCredential, witnessSecret, witnessRefresh []byte) (bool, error) {
	res, err := db.Bun().NewRaw(`UPDATE provider_accounts SET
			secret_sealed = ?2,
			refresh_sealed = COALESCE(?3, refresh_sealed),
			id_token_sealed = COALESCE(?4, id_token_sealed),
			expires_at = ?5,
			email = COALESCE(?6, email),
			organization_name = COALESCE(?7, organization_name),
			plan = COALESCE(?8, plan),
			plan_raw = COALESCE(?9, plan_raw),
			updated_at = ?10
		WHERE id = ?0 AND user_id = ?1 AND secret_sealed = ?11 AND refresh_sealed IS NOT DISTINCT FROM ?12`,
		accountID, userID, next.Secret, next.Refresh, next.IDToken, acctNullTime(next.ExpiresAt),
		acctNullText(next.Email), acctNullText(next.OrganizationName), acctNullText(next.Plan), acctNullText(next.PlanRaw),
		next.UpdatedAt.UTC(), witnessSecret, witnessRefresh).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// PatchUserProviderAccount writes the user-owned state of one of the user's accounts — label,
// pause, needs-reauth mark — stamps updated_at with now, and returns the account. The caller
// resolves the values (it read the row under LockUserProviderAccount).
func PatchUserProviderAccount(ctx context.Context, db orm.Context, userID, accountID, label string, pausedAt, reauthAt *time.Time, reauthReason string, now time.Time) (core.ProviderAccount, error) {
	var row ProviderAccount
	err := db.Bun().NewUpdate().Model(&row).
		Set("label = ?", label).Set("paused_at = ?", acctUTC(pausedAt)).Set("needs_reauth_at = ?", acctUTC(reauthAt)).
		Set("needs_reauth_reason = ?", acctNullText(reauthReason)).Set("updated_at = ?", now.UTC()).
		Where("id = ?", accountID).Where("user_id = ?", userID).
		Returning(providerAccountMetaColumns).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ProviderAccount{}, core.ErrProviderAccountNotFound
	}
	if err != nil {
		return core.ProviderAccount{}, err
	}
	return providerAccountToCore(&row), nil
}

// LockProviderAccountIDs locks the user's accounts of provider and returns their ids.
func LockProviderAccountIDs(ctx context.Context, db orm.Context, userID, provider string) ([]string, error) {
	var ids []string
	err := db.Bun().NewSelect().Model((*ProviderAccount)(nil)).Column(ProviderAccountFieldID).
		Where("user_id = ?", userID).Where("provider = ?", provider).For("UPDATE").Scan(ctx, &ids)
	return ids, err
}

// SetProviderAccountPriority moves the account to priority and stamps updated_at with now.
func SetProviderAccountPriority(ctx context.Context, db orm.Context, accountID string, priority int, now time.Time) error {
	_, err := db.Bun().NewUpdate().Model((*ProviderAccount)(nil)).
		Set("priority = ?", orm.Arg(priority)).Set("updated_at = ?", now.UTC()).
		Where("id = ?", accountID).Exec(ctx)
	return err
}

// UserProviderAccountProvider returns the provider of one of the user's accounts, or
// core.ErrProviderAccountNotFound.
func UserProviderAccountProvider(ctx context.Context, db orm.Context, userID, accountID string) (string, error) {
	var provider string
	err := db.Bun().NewSelect().Model((*ProviderAccount)(nil)).Column(ProviderAccountFieldProvider).
		Where("id = ?", accountID).Where("user_id = ?", userID).Scan(ctx, &provider)
	if errors.Is(err, sql.ErrNoRows) {
		return "", core.ErrProviderAccountNotFound
	}
	return provider, err
}

// DeleteUserProviderAccount deletes one of the user's accounts and reports whether there was
// one. Its limit state and thread assignments cascade.
func DeleteUserProviderAccount(ctx context.Context, db orm.Context, userID, accountID string) (bool, error) {
	res, err := db.Bun().NewDelete().Model((*ProviderAccount)(nil)).
		Where("id = ?", accountID).Where("user_id = ?", userID).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ListUserIDsWithProviderAccount returns every user holding an account for provider, by id.
func ListUserIDsWithProviderAccount(ctx context.Context, db orm.Context, provider string) ([]string, error) {
	var ids []string
	err := db.Bun().NewSelect().Model((*ProviderAccount)(nil)).Distinct().Column(ProviderAccountFieldUserID).
		Where("provider = ?", provider).Order(ProviderAccountFieldUserID).Scan(ctx, &ids)
	return ids, err
}

// LatestServingProviderAccount returns the account that served the thread's newest message that
// names one, or "" when none does.
func LatestServingProviderAccount(ctx context.Context, db orm.Context, threadID string) (string, error) {
	var id string
	err := db.Bun().NewSelect().Model((*Message)(nil)).Column(MessageFieldProviderAccountID).
		Where("thread_id = ?", threadID).Where("provider_account_id IS NOT NULL").
		OrderExpr("seq DESC").Limit(1).Scan(ctx, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ProviderAccountExtra carries fields on ProviderAccount that are NOT columns — computed
// values, transient state, anything derived rather than stored. ProviderAccount is
// embedded on ProviderAccountExtra (see provider_account_orm.go), so a field added here is a field on
// every ProviderAccount value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ProviderAccountExtra struct {
}
