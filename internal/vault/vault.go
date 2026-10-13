// Copyright (c) 2025 Reliant Labs

package vault

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/reliant-labs/forge/pkg/crypto"
)

const (
	// TenantUser is the only tenant kind v1 uses; "org" is reserved.
	TenantUser = "user"

	stateGlobalPrimary = "primary"
	dekLen             = 32
	envelopeMagicLen   = 4
)

var (
	// ErrKeyNotFound: the ciphertext names a vault key id with no row.
	ErrKeyNotFound = errors.New("vault: ciphertext names an unknown vault key")
	// ErrKeyDestroyed: the tenant's key was crypto-shredded.
	ErrKeyDestroyed = errors.New("vault: tenant key has been destroyed")
	// ErrTenantMismatch: the ciphertext was sealed for a different tenant.
	ErrTenantMismatch = errors.New("vault: ciphertext belongs to a different tenant")
)

// Tenant is the unit that owns a DEK.
type Tenant struct{ Kind, ID string }

// UserTenant is the v1 tenant for a user id.
func UserTenant(userID string) Tenant { return Tenant{Kind: TenantUser, ID: userID} }

type cachedKey struct {
	tenant Tenant
	ring   *crypto.Keyring
}

// sealer is the Service: it seals and opens values per tenant, caching the
// unwrapped DEKs it has used.
type sealer struct {
	db      DBTX
	wrapper KeyWrapper

	mu      sync.RWMutex
	primary map[Tenant]string
	keys    map[string]cachedKey
}

// New builds the vault over deps.DB and deps.Keys.
func New(deps Deps) Service {
	return &sealer{
		db:      deps.DB,
		wrapper: deps.Keys,
		primary: map[Tenant]string{},
		keys:    map[string]cachedKey{},
	}
}

// Seal implements Service.
func (v *sealer) Seal(ctx context.Context, tenant Tenant, plaintext, aad []byte) ([]byte, error) {
	_, ring, err := v.primaryRing(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return ring.Seal(plaintext, boundAAD(tenant, aad))
}

// Open implements Service.
func (v *sealer) Open(ctx context.Context, tenant Tenant, ciphertext, aad []byte) ([]byte, error) {
	keyID, err := envelopeKeyID(ciphertext)
	if err != nil {
		return nil, err
	}
	ring, err := v.ringByID(ctx, tenant, keyID)
	if err != nil {
		return nil, err
	}
	return ring.Open(ciphertext, boundAAD(tenant, aad))
}

// OpenSecret implements Service.
func (v *sealer) OpenSecret(ctx context.Context, tenant Tenant, ciphertext, aad []byte) (Secret, error) {
	pt, err := v.Open(ctx, tenant, ciphertext, aad)
	if err != nil {
		return Secret{}, err
	}
	defer clear(pt)
	return NewSecret(pt), nil
}

func boundAAD(t Tenant, aad []byte) []byte {
	out := make([]byte, 0, len(t.Kind)+len(t.ID)+len(aad)+2)
	out = append(out, t.Kind...)
	out = append(out, 0)
	out = append(out, t.ID...)
	out = append(out, 0)
	return append(out, aad...)
}

func wrapAAD(keyID string, t Tenant) []byte {
	return boundAAD(t, []byte("vault_keys\x00"+keyID))
}

// envelopeKeyID reads the key id the forge envelope carries after its 4-byte
// magic and 1-byte length.
func envelopeKeyID(blob []byte) (string, error) {
	if len(blob) < envelopeMagicLen+2 {
		return "", fmt.Errorf("%w: %d bytes", crypto.ErrUnversionedCiphertext, len(blob))
	}
	idLen := int(blob[envelopeMagicLen])
	end := envelopeMagicLen + 1 + idLen
	if idLen == 0 || end > len(blob) {
		return "", fmt.Errorf("%w: bad key id length", crypto.ErrKeyringInvalid)
	}
	return string(blob[envelopeMagicLen+1 : end]), nil
}

func (v *sealer) primaryRing(ctx context.Context, tenant Tenant) (string, *crypto.Keyring, error) {
	v.mu.RLock()
	id, ok := v.primary[tenant]
	v.mu.RUnlock()
	if ok {
		ring, err := v.ringByID(ctx, tenant, id)
		return id, ring, err
	}

	id, wrapped, found, err := v.selectPrimary(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	if !found {
		if err := v.createPrimary(ctx, tenant); err != nil {
			return "", nil, err
		}
		if id, wrapped, found, err = v.selectPrimary(ctx, tenant); err != nil {
			return "", nil, err
		}
		if !found {
			return "", nil, fmt.Errorf("vault: no primary key for %s/%s after create", tenant.Kind, tenant.ID)
		}
	}
	ring, err := v.install(tenant, id, wrapped)
	if err != nil {
		return "", nil, err
	}
	v.mu.Lock()
	v.primary[tenant] = id
	v.mu.Unlock()
	return id, ring, nil
}

func (v *sealer) selectPrimary(ctx context.Context, t Tenant) (id string, wrapped []byte, found bool, err error) {
	err = v.db.QueryRowContext(ctx,
		`SELECT id, wrapped_dek FROM vault_keys WHERE tenant_kind = $1 AND tenant_id = $2 AND state = $3`,
		t.Kind, t.ID, stateGlobalPrimary).Scan(&id, &wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, fmt.Errorf("vault: loading primary key: %w", err)
	}
	return id, wrapped, true, nil
}

// createPrimary inserts the tenant's first DEK. ON CONFLICT DO NOTHING makes
// concurrent first uses converge on one row: the loser re-reads the winner's.
func (v *sealer) createPrimary(ctx context.Context, t Tenant) error {
	dek := make([]byte, dekLen)
	if _, err := rand.Read(dek); err != nil {
		return fmt.Errorf("vault: generating dek: %w", err)
	}
	defer clear(dek)
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return fmt.Errorf("vault: generating key id: %w", err)
	}
	id := "vk_" + hex.EncodeToString(idBytes)
	wrapped, kekID, err := v.wrapper.Wrap(dek, wrapAAD(id, t))
	if err != nil {
		return err
	}
	_, err = v.db.ExecContext(ctx,
		`INSERT INTO vault_keys (id, tenant_kind, tenant_id, version, kek_id, wrapped_dek, state)
		 VALUES ($1, $2, $3, 1, $4, $5, $6) ON CONFLICT DO NOTHING`,
		id, t.Kind, t.ID, kekID, wrapped, stateGlobalPrimary)
	if err != nil {
		return fmt.Errorf("vault: storing key: %w", err)
	}
	return nil
}

func (v *sealer) ringByID(ctx context.Context, t Tenant, id string) (*crypto.Keyring, error) {
	v.mu.RLock()
	c, ok := v.keys[id]
	v.mu.RUnlock()
	if ok {
		if c.tenant != t {
			return nil, ErrTenantMismatch
		}
		return c.ring, nil
	}

	var (
		kind, tenantID, state string
		wrapped               []byte
	)
	err := v.db.QueryRowContext(ctx,
		`SELECT tenant_kind, tenant_id, state, wrapped_dek FROM vault_keys WHERE id = $1`, id).
		Scan(&kind, &tenantID, &state, &wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("vault: loading key: %w", err)
	}
	if (Tenant{Kind: kind, ID: tenantID}) != t {
		return nil, ErrTenantMismatch
	}
	if state == "destroyed" {
		return nil, ErrKeyDestroyed
	}
	return v.install(t, id, wrapped)
}

// install unwraps a DEK and caches the resulting single-key ring. The ring id
// is the vault key id, so every ciphertext it seals names its own key.
func (v *sealer) install(t Tenant, id string, wrapped []byte) (*crypto.Keyring, error) {
	dek, err := v.wrapper.Unwrap(wrapped, wrapAAD(id, t))
	if err != nil {
		return nil, err
	}
	defer clear(dek)
	ring, err := crypto.ParseKeyring(id + ":" + base64.StdEncoding.EncodeToString(dek))
	if err != nil {
		return nil, fmt.Errorf("vault: building dek ring: %w", err)
	}
	v.mu.Lock()
	v.keys[id] = cachedKey{tenant: t, ring: ring}
	v.mu.Unlock()
	return ring, nil
}

// KeyIDOf returns the id of the vault key (vault_keys.id) that sealed blob, so
// a row can record which key it needs without a second lookup.
func KeyIDOf(blob []byte) (string, error) { return envelopeKeyID(blob) }
