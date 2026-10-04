// Copyright (c) 2025 Reliant Labs

package connections

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// APIKeyKind picks the shape of a pasted credential.
type APIKeyKind int

const (
	APIKeyKindAPIKey APIKeyKind = iota + 1
	APIKeyKindBasic
)

var integrationIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

const maxNameLen = 100
const maxSecretLen = 8 << 10

// serviceStore is what Service needs from persistence.
type serviceStore interface {
	tokenStore
	brokerStore
	ListConnections(ctx context.Context, userID, integrationID string) ([]*core.Connection, error)
	RenameConnection(ctx context.Context, userID, id, name string, ev core.ConnectionEvent) error
	SetDefaultConnection(ctx context.Context, userID, id string, ev core.ConnectionEvent) error
	DeleteConnection(ctx context.Context, userID, id string, ev core.ConnectionEvent) error
	RecordTestResult(ctx context.Context, userID, id, accountLabel string) error
	ListConnectionEvents(ctx context.Context, userID, id string, limit int, beforeID int64) ([]core.ConnectionEvent, error)
}

// Service is the business logic behind ConnectionService. Every method takes
// the caller's user id explicitly and every store call carries it, so a
// connection that is not the caller's is indistinguishable from a missing one.
type Service struct {
	store     serviceStore
	vault     Sealer
	providers *Registry
	tokens    *TokenSource
	broker    *Broker
	doer      HTTPDoer
}

// NewService wires the service.
func NewService(store serviceStore, v Sealer, providers *Registry, tokens *TokenSource, broker *Broker, doer HTTPDoer) *Service {
	if doer == nil {
		doer = NewHTTPClient()
	}
	return &Service{store: store, vault: v, providers: providers, tokens: tokens, broker: broker, doer: doer}
}

// Integration is one entry of the integration catalog.
type Integration struct {
	ID, DisplayName, AuthKind string
	Available                 bool
	UnavailableReason         string
}

// ListIntegrations returns the OAuth providers, available or not.
func (s *Service) ListIntegrations() []Integration {
	var out []Integration
	for _, p := range s.providers.List() {
		out = append(out, Integration{ID: p.ID, DisplayName: p.DisplayName, AuthKind: p.AuthKind,
			Available: p.Available(), UnavailableReason: p.UnavailableReason()})
	}
	return out
}

func (s *Service) List(ctx context.Context, userID, integrationID string) ([]*core.Connection, error) {
	cs, err := s.store.ListConnections(ctx, userID, integrationID)
	return cs, mapStoreErr(err)
}

func (s *Service) Get(ctx context.Context, userID, id string) (*core.Connection, error) {
	c, err := s.store.GetConnection(ctx, userID, id)
	return c, mapStoreErr(err)
}

// CreateAPIKeyParams is a pasted credential.
type CreateAPIKeyParams struct {
	UserID        string
	IntegrationID string
	Name          string
	Kind          APIKeyKind
	Fields        map[string]string
}

// CreateAPIKey saves an api_key or basic connection. The values are sealed and
// never returned.
func (s *Service) CreateAPIKey(ctx context.Context, p CreateAPIKeyParams) (*core.Connection, error) {
	if !integrationIDPattern.MatchString(p.IntegrationID) {
		return nil, newError(CodeInvalidArgument, "integration_id must be lowercase letters, digits, '-' or '_'")
	}
	if _, oauth := s.providers.Get(p.IntegrationID); oauth {
		return nil, newError(CodeInvalidArgument, "%q connects through its own sign-in flow, not a pasted key", p.IntegrationID)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return nil, newError(CodeInvalidArgument, "name is required and at most %d characters", maxNameLen)
	}

	id := uuidConnID()
	conn := &core.Connection{
		ID: id, OwnerKind: core.ConnectionOwnerUser, UserID: p.UserID, IntegrationID: p.IntegrationID,
		Name: name, Status: core.ConnectionStatusActive, Scopes: []string{},
	}
	var plain []plainField
	switch p.Kind {
	case APIKeyKindAPIKey:
		key := p.Fields["api_key"]
		if key == "" || len(key) > maxSecretLen {
			return nil, newError(CodeInvalidArgument, "fields.api_key is required")
		}
		header := p.Fields["header"]
		if header == "" {
			header = DefaultAPIKeyHeader
		}
		if !ValidAPIKeyHeader(header) {
			return nil, newError(CodeInvalidArgument, "fields.header must be one of bearer, x-api-key, api-key, x-auth-token")
		}
		conn.AuthKind = core.ConnectionAuthAPIKey
		conn.AuthHeader = &header
		plain = []plainField{{core.SecretFieldAPIKey, key}}
	case APIKeyKindBasic:
		user, pass := p.Fields["username"], p.Fields["password"]
		if user == "" || pass == "" || len(user) > maxSecretLen || len(pass) > maxSecretLen {
			return nil, newError(CodeInvalidArgument, "fields.username and fields.password are required")
		}
		if strings.ContainsRune(user, 0) || strings.ContainsRune(pass, 0) || strings.Contains(user, ":") {
			return nil, newError(CodeInvalidArgument, "username may not contain ':' and neither value may contain NUL")
		}
		conn.AuthKind = core.ConnectionAuthBasic
		plain = []plainField{{core.SecretFieldAPIKey, user}, {core.SecretFieldPassword, pass}}
	default:
		return nil, newError(CodeInvalidArgument, "kind must be api_key or basic")
	}

	secrets := make([]core.ConnectionSecret, 0, len(plain))
	for _, f := range plain {
		ct, err := s.vault.Seal(ctx, vault.UserTenant(p.UserID), []byte(f.value), SecretAAD(id, f.field))
		if err != nil {
			return nil, &Error{Code: CodeInternal, Message: "sealing credential", Err: err}
		}
		keyID, err := vault.KeyIDOf(ct)
		if err != nil {
			return nil, &Error{Code: CodeInternal, Message: "reading key id", Err: err}
		}
		secrets = append(secrets, core.ConnectionSecret{ConnectionID: id, Field: f.field, VaultKeyID: keyID, Ciphertext: ct})
	}
	if err := s.store.CreateConnection(ctx, conn, secrets, userEvent(core.ConnectionEventCreated, p.UserID)); err != nil {
		return nil, mapStoreErr(err)
	}
	return s.Get(ctx, p.UserID, id)
}

func (s *Service) Rename(ctx context.Context, userID, id, name string) (*core.Connection, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return nil, newError(CodeInvalidArgument, "name is required and at most %d characters", maxNameLen)
	}
	if err := s.store.RenameConnection(ctx, userID, id, name, userEvent(core.ConnectionEventRenamed, userID)); err != nil {
		return nil, mapStoreErr(err)
	}
	return s.Get(ctx, userID, id)
}

func (s *Service) SetDefault(ctx context.Context, userID, id string) (*core.Connection, error) {
	if err := s.store.SetDefaultConnection(ctx, userID, id, userEvent(core.ConnectionEventDefaultChanged, userID)); err != nil {
		return nil, mapStoreErr(err)
	}
	return s.Get(ctx, userID, id)
}

// Delete revokes at the provider where an API exists (best effort: a failure
// never blocks the delete, because a user must always be able to remove a
// credential), then soft-deletes the row and destroys its ciphertext.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	conn, err := s.store.GetConnection(ctx, userID, id)
	if err != nil {
		return mapStoreErr(err)
	}
	if prov, ok := s.providers.Get(conn.IntegrationID); ok && prov.Available() && conn.AuthKind == core.ConnectionAuthGitHubAppUser {
		s.revokeGitHub(ctx, prov, conn)
	}
	if err := s.store.DeleteConnection(ctx, userID, id, userEvent(core.ConnectionEventDeleted, userID)); err != nil {
		return mapStoreErr(err)
	}
	s.tokens.Forget(id)
	return nil
}

func (s *Service) revokeGitHub(ctx context.Context, prov *Provider, conn *core.Connection) {
	tok, err := s.tokens.Token(ctx, conn.UserID, conn.ID)
	if err != nil {
		return
	}
	var clientSecret string
	_ = prov.ClientSecret.Use(func(b []byte) error { clientSecret = string(b); return nil })
	_ = tok.Use(func(b []byte) error {
		body := strings.NewReader(`{"access_token":"` + string(b) + `"}`)
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, "https://api.github.com/applications/"+prov.ClientID+"/token", body)
		if err != nil {
			return nil
		}
		req.SetBasicAuth(prov.ClientID, clientSecret)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")
		if resp, err := s.doer.Do(req); err == nil {
			_ = resp.Body.Close()
		}
		return nil
	})
}

// TestResult is the outcome of a probe.
type TestResult struct {
	OK           bool
	Probed       bool
	AccountLabel string
	ErrorClass   string
}

// Test runs the per-auth-kind probe. OAuth connections call the provider's
// identity endpoint with a live (refreshed if need be) token and record the
// account label; pasted credentials have no provider endpoint in v1, so the
// probe is that the stored secret opens.
func (s *Service) Test(ctx context.Context, userID, id string) (*TestResult, error) {
	conn, err := s.store.GetConnection(ctx, userID, id)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	secret, err := s.tokens.Token(ctx, userID, id)
	if err != nil {
		switch CodeOf(err) {
		case CodeNeedsReauth:
			return &TestResult{ErrorClass: "needs_reauth"}, nil
		case CodeUnavailable:
			return &TestResult{ErrorClass: "unreachable"}, nil
		}
		return nil, err
	}
	prov, isOAuth := s.providers.Get(conn.IntegrationID)
	if !isOAuth || (conn.AuthKind != core.ConnectionAuthOAuth2 && conn.AuthKind != core.ConnectionAuthGitHubAppUser) {
		return &TestResult{OK: true}, nil
	}
	var who Identity
	var perr error
	_ = secret.Use(func(b []byte) error {
		who, perr = prov.identify(ctx, s.doer, string(b))
		return nil
	})
	if perr != nil {
		var pe *probeError
		class := "unreachable"
		if errors.As(perr, &pe) {
			class = pe.class
		}
		return &TestResult{Probed: true, ErrorClass: class}, nil
	}
	if err := s.store.RecordTestResult(ctx, userID, id, who.AccountLabel); err != nil {
		return nil, mapStoreErr(err)
	}
	return &TestResult{OK: true, Probed: true, AccountLabel: who.AccountLabel}, nil
}

func (s *Service) Events(ctx context.Context, userID, id string, limit int, beforeID int64) ([]core.ConnectionEvent, error) {
	if _, err := s.store.GetConnection(ctx, userID, id); err != nil {
		return nil, mapStoreErr(err)
	}
	evs, err := s.store.ListConnectionEvents(ctx, userID, id, limit, beforeID)
	return evs, mapStoreErr(err)
}

// StartOAuth begins an RPC-bound flow.
func (s *Service) StartOAuth(ctx context.Context, p StartParams) (string, error) {
	if p.Binder == "" {
		p.Binder = RPCBinder(p.UserID)
	}
	return s.broker.Start(ctx, p)
}

// CompleteOAuth finishes an RPC-bound flow.
func (s *Service) CompleteOAuth(ctx context.Context, userID, state, code string) (*Completion, error) {
	c, err := s.broker.Complete(ctx, CompleteParams{State: state, Code: code, Binder: RPCBinder(userID)})
	if err != nil {
		return nil, err
	}
	if c.Connection.UserID != userID {
		return nil, newError(CodeFailedPrecondition, "this authorization was started by a different user")
	}
	return c, nil
}
