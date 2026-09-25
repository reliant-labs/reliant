package tokenauthority

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
)

// Memory is an in-process Authority for tests. It applies the SAME grant rules
// as both production stores (forge/pkg/accesstoken.Grant.Validate) and the same
// liveness semantics, so a test exercising it exercises real behaviour — only
// persistence is fake.
type Memory struct {
	mu     sync.Mutex
	tokens map[string]*memToken // by hash
	now    func() time.Time
	// unavailable makes Introspect fail as an unreachable authority would.
	unavailable bool
}

// SetMemoryUnavailable makes m's Introspect fail with ErrMemoryUnavailable (a
// transport failure, NOT a credential rejection) until cleared.
//
// A function rather than a method on purpose: Memory implements Authority, and
// a test-only control knob in its method set would make it part of the
// Authority surface every caller sees (forge's contract rule flags exactly
// that). Tests in other packages still reach it by name.
func SetMemoryUnavailable(m *Memory, v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unavailable = v
}

type memToken struct {
	info      TokenInfo
	actingID  string
	hash      string
	revokedAt *time.Time
}

// NewMemory returns an empty in-memory Authority.
func NewMemory() *Memory {
	return &Memory{tokens: map[string]*memToken{}, now: time.Now}
}

func (m *Memory) live(t *memToken) bool {
	return t.revokedAt == nil && (t.info.ExpiresAt == nil || t.info.ExpiresAt.After(m.now()))
}

// Introspect implements Authority.
func (m *Memory) Introspect(_ context.Context, token string) (*fat.Principal, error) {
	if !fat.HasFormat(token) {
		return nil, ErrInactive
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unavailable {
		return nil, ErrMemoryUnavailable
	}
	t, ok := m.tokens[fat.Hash(token)]
	if !ok || !m.live(t) {
		return nil, ErrInactive
	}
	scopes, err := fat.NewSet(t.info.Scopes)
	if err != nil {
		return nil, err
	}
	return &fat.Principal{
		TokenID: t.info.ID, OrgID: t.actingID, ActingUserID: t.actingID, Scopes: scopes,
		Resource: t.info.Resource, Ephemeral: t.info.Ephemeral, ExpiresAt: t.info.ExpiresAt,
	}, nil
}

// MintForUser implements Authority.
func (m *Memory) MintForUser(_ context.Context, req MintRequest) (Minted, error) {
	if strings.TrimSpace(req.UserID) == "" {
		return Minted{}, fmt.Errorf("%w: user id is required", fat.ErrInvalidGrant)
	}
	scopes := fat.SetOf(req.Scopes...)
	g := fat.Grant{OrgID: req.UserID, Name: strings.TrimSpace(req.Name), Scopes: scopes, ActingUserID: req.UserID,
		Resource: req.Resource, Ephemeral: req.Ephemeral, ExpiresAt: req.ExpiresAt}
	if err := g.Validate(m.now()); err != nil {
		return Minted{}, err
	}
	minted, err := fat.Mint()
	if err != nil {
		return Minted{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rotated := false
	if req.Rotate {
		now := m.now()
		for _, t := range m.tokens {
			if t.actingID == req.UserID && t.info.Name == g.Name && t.revokedAt == nil &&
				strings.Join(t.info.Scopes, ",") == strings.Join(scopes.Strings(), ",") {
				t.revokedAt, rotated = &now, true
			}
		}
	}
	id := uuid.NewString()
	m.tokens[minted.Hash] = &memToken{
		info: TokenInfo{ID: id, Name: g.Name, DisplayPrefix: minted.DisplayPrefix, Scopes: scopes.Strings(),
			Resource: req.Resource, Ephemeral: req.Ephemeral, CreatedAt: m.now(), ExpiresAt: req.ExpiresAt},
		actingID: req.UserID, hash: minted.Hash,
	}
	return Minted{TokenID: id, Plaintext: minted.Plaintext, DisplayPrefix: minted.DisplayPrefix,
		ExpiresAt: req.ExpiresAt, Rotated: rotated}, nil
}

// ListForUser implements Authority.
func (m *Memory) ListForUser(_ context.Context, userID string, scope fat.Scope) ([]TokenInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []TokenInfo
	for _, t := range m.tokens {
		if t.actingID != userID || t.revokedAt != nil {
			continue
		}
		if scope != "" && !contains(t.info.Scopes, string(scope)) {
			continue
		}
		out = append(out, t.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// RevokeForUser implements Authority.
func (m *Memory) RevokeForUser(_ context.Context, userID, tokenID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.info.ID == tokenID && t.actingID == userID {
			if t.revokedAt == nil {
				now := m.now()
				t.revokedAt = &now
			}
			return nil
		}
	}
	return ErrNotFound
}

// RevokeResource implements Authority.
func (m *Memory) RevokeResource(_ context.Context, resource fat.Resource) (int64, error) {
	return m.revokeWhere(func(t *memToken) bool {
		return t.info.Resource != nil && *t.info.Resource == resource
	}), nil
}

// RevokeEphemeral implements Authority.
func (m *Memory) RevokeEphemeral(_ context.Context, userID string) (int64, error) {
	return m.revokeWhere(func(t *memToken) bool { return t.actingID == userID && t.info.Ephemeral }), nil
}

func (m *Memory) revokeWhere(match func(*memToken) bool) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	now := m.now()
	for _, t := range m.tokens {
		if t.revokedAt == nil && match(t) {
			t.revokedAt = &now
			n++
		}
	}
	return n
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ErrMemoryUnavailable lets a test simulate an unreachable authority.
var ErrMemoryUnavailable = errors.New("tokenauthority: memory authority unavailable")
