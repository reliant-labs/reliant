// Copyright (c) 2025 Reliant Labs

package vault

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
)

// Rotating the key-encryption key (KEK) touches vault_keys only. Each tenant's
// data key (DEK) stays the same, so every value sealed under it stays valid;
// only the DEK's wrapping changes. The rotation, in order:
//
//  1. RELIANT_VAULT_KEY becomes "new:<b64>,old:<b64>" on every server, so new
//     DEKs are wrapped under the new KEK and old ones still unwrap.
//  2. Rewrap moves every stored DEK onto the new KEK.
//  3. VerifyWrapped proves no DEK still needs the old KEK.
//  4. RELIANT_VAULT_KEY becomes "new:<b64>", and VerifyWrapped runs again
//     under it.

// KEKReport counts the stored data keys by the KEK each is wrapped under. It
// carries key ids and counts only, never key material.
type KEKReport struct {
	// Primary is the keyring's primary KEK: where every DEK belongs.
	Primary string
	// Total counts the DEKs that are not destroyed.
	Total int
	// ByKEK counts them by the KEK they were wrapped under when read.
	ByKEK map[string]int
	// Rewrapped counts the DEKs Rewrap moved onto Primary (always 0 on a dry run or a verify).
	Rewrapped int
	// Pending counts the DEKs not yet on Primary (a dry run's would-rewrap count).
	Pending int
}

// String renders the report on one line.
func (r KEKReport) String() string {
	ids := make([]string, 0, len(r.ByKEK))
	for id := range r.ByKEK {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%s=%d", id, r.ByKEK[id]))
	}
	return fmt.Sprintf("primary KEK %s: %d data key(s) [%s]; rewrapped %d, pending %d",
		r.Primary, r.Total, strings.Join(parts, " "), r.Rewrapped, r.Pending)
}

// Rewrap re-wraps every stored data key under ring's primary KEK. ring must
// still hold every KEK a stored key is wrapped under; a key that does not open
// under ring fails the whole run, and nothing is written. Keys already on the
// primary are left alone, so a rerun is a no-op. dryRun unwraps every key,
// which proves ring opens them all, and writes nothing.
//
// It runs in one transaction that locks the rows it reads, so it is safe
// beside live servers: they only ever read a key's wrapping, and the DEK they
// cache is the one the new wrapping holds.
func Rewrap(ctx context.Context, db orm.Context, ring *crypto.Keyring, dryRun bool) (KEKReport, error) {
	rep := KEKReport{Primary: ring.PrimaryKeyID(), ByKEK: map[string]int{}}
	err := db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		rows, err := store.ListLiveVaultKeysForUpdate(ctx, db)
		if err != nil {
			return fmt.Errorf("vault: listing data keys: %w", err)
		}
		for _, row := range rows {
			onPrimary, dek, err := openStored(&rep, ring, row)
			if err != nil {
				return err
			}
			if onPrimary || dryRun {
				clear(dek)
				if !onPrimary {
					rep.Pending++
				}
				continue
			}
			wrapped, err := ring.Seal(dek, wrapAAD(row.ID, Tenant{Kind: row.TenantKind, ID: row.TenantID}))
			clear(dek)
			if err != nil {
				return fmt.Errorf("vault: wrapping data key %s: %w", row.ID, err)
			}
			if err := store.RewrapVaultKey(ctx, db, row.ID, rep.Primary, wrapped); err != nil {
				return fmt.Errorf("vault: storing data key %s: %w", row.ID, err)
			}
			rep.Rewrapped++
		}
		return nil
	})
	if err != nil {
		return KEKReport{}, err
	}
	return rep, nil
}

// VerifyWrapped proves every stored data key is wrapped under ring's primary
// KEK and opens under it, so any other KEK can leave the keyring. It writes
// nothing. A key on another KEK, or one that does not open, is an error that
// names its id.
func VerifyWrapped(ctx context.Context, db orm.Context, ring *crypto.Keyring) (KEKReport, error) {
	rep := KEKReport{Primary: ring.PrimaryKeyID(), ByKEK: map[string]int{}}
	var off []string
	err := db.RunTxReadOnly(ctx, func(ctx context.Context) error {
		rows, err := store.ListLiveVaultKeys(ctx, db)
		if err != nil {
			return fmt.Errorf("vault: listing data keys: %w", err)
		}
		for _, row := range rows {
			onPrimary, dek, err := openStored(&rep, ring, row)
			clear(dek)
			if err != nil {
				return err
			}
			if !onPrimary {
				rep.Pending++
				off = append(off, row.ID)
			}
		}
		return nil
	})
	if err != nil {
		return KEKReport{}, err
	}
	if len(off) > 0 {
		return rep, fmt.Errorf("vault: %d data key(s) are not wrapped under the primary KEK %s (first: %s); run `reliant db vault rewrap`",
			len(off), rep.Primary, off[0])
	}
	return rep, nil
}

// openStored unwraps one stored data key under ring and counts it in rep. A
// key is on the primary only when both its envelope and its kek_id say so.
func openStored(rep *KEKReport, ring *crypto.Keyring, row *store.VaultKey) (onPrimary bool, dek []byte, err error) {
	wrappedUnder, err := envelopeKeyID(row.WrappedDek)
	if err != nil {
		return false, nil, fmt.Errorf("vault: data key %s: %w", row.ID, err)
	}
	rep.Total++
	rep.ByKEK[wrappedUnder]++
	dek, err = ring.Open(row.WrappedDek, wrapAAD(row.ID, Tenant{Kind: row.TenantKind, ID: row.TenantID}))
	if err != nil {
		return false, nil, fmt.Errorf("vault: data key %s (wrapped under KEK %s) does not open under this keyring: %w", row.ID, wrappedUnder, err)
	}
	return wrappedUnder == rep.Primary && row.KekID == rep.Primary, dek, nil
}
