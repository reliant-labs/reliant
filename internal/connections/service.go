// Copyright (c) 2025 Reliant Labs

package connections

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/vault"
)

// APIKeyKind picks the shape of a pasted credential.
type APIKeyKind int

const (
	APIKeyKindAPIKey APIKeyKind = iota + 1
	APIKeyKindBasic
)

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
	RecordTestResult(ctx context.Context, userID, id, accountLabel, senderID string) error
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
	delegated DelegatedAvailable
}

// NewService wires the service.
func NewService(store serviceStore, v Sealer, providers *Registry, tokens *TokenSource, broker *Broker, doer HTTPDoer) *Service {
	if doer == nil {
		doer = NewHTTPClient()
	}
	return &Service{store: store, vault: v, providers: providers, tokens: tokens, broker: broker, doer: doer}
}

// WithDelegated tells the service which delegated brokers this deployment
// wires, so ListIntegrations reports them available. The api-server holds no
// brokers (credentials resolve on the worker); it is told by configuration.
func (s *Service) WithDelegated(fn DelegatedAvailable) *Service {
	s.delegated = fn
	return s
}

// Integration is one entry of the integration catalog.
type Integration struct {
	ID, DisplayName string
	// Methods are the ways to connect, in manifest order (most preferred
	// first), each with whether this deployment can offer it.
	Methods []MethodStatus
	// Params are the non-secret settings a new connection asks for.
	Params []*reliantv1.ConnectionParam
}

// ListIntegrations returns every integration that declares auth, with each
// method's availability. An unconfigured method is listed, with an operator
// reason, rather than hidden.
func (s *Service) ListIntegrations() []Integration {
	var out []Integration
	for _, p := range s.providers.List() {
		in := Integration{ID: p.ID, DisplayName: p.DisplayName, Params: p.spec.GetConnectionParams()}
		for _, a := range p.spec.GetAuth() {
			in.Methods = append(in.Methods, p.methodStatus(manifest.AuthKind(a), s.delegated))
		}
		out = append(out, in)
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
	// Params are the integration's connection_params (not secret).
	Params map[string]string
}

// CreateAPIKey saves an api_key or basic connection for a catalog integration
// that declares that method. The values are sealed and never returned.
func (s *Service) CreateAPIKey(ctx context.Context, p CreateAPIKeyParams) (*core.Connection, error) {
	prov, ok := s.providers.Get(p.IntegrationID)
	if !ok {
		return nil, newError(CodeInvalidArgument, "unknown integration %q", p.IntegrationID)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return nil, newError(CodeInvalidArgument, "name is required and at most %d characters", maxNameLen)
	}
	params, err := prov.checkParams(p.Params)
	if err != nil {
		return nil, err
	}

	id := uuidConnID()
	conn := &core.Connection{
		ID: id, OwnerKind: core.ConnectionOwnerUser, UserID: p.UserID, IntegrationID: p.IntegrationID,
		Name: name, Status: core.ConnectionStatusActive, Scopes: []string{}, Params: params,
	}
	var plain []plainField
	switch p.Kind {
	case APIKeyKindAPIKey:
		m, ok := prov.Method(MethodAPIKey)
		if !ok {
			return nil, newError(CodeInvalidArgument, "%s does not accept an API key", prov.DisplayName)
		}
		key := p.Fields["api_key"]
		if key == "" || len(key) > maxSecretLen || strings.ContainsAny(key, "\x00\r\n") {
			return nil, newError(CodeInvalidArgument, "fields.api_key is required")
		}
		if err := onlyFields(p.Fields, "api_key", "header"); err != nil {
			return nil, err
		}
		if openAPIKeyPlacement(m.GetApiKey()) {
			header := p.Fields["header"]
			if header == "" {
				header = DefaultAPIKeyHeader
			}
			if !ValidAPIKeyHeader(header) {
				return nil, newError(CodeInvalidArgument, "fields.header must be one of bearer, x-api-key, api-key, x-auth-token")
			}
			conn.AuthHeader = &header
		} else if p.Fields["header"] != "" {
			return nil, newError(CodeInvalidArgument, "%s decides where its API key goes; fields.header is not accepted", prov.DisplayName)
		}
		conn.AuthKind = core.ConnectionAuthAPIKey
		plain = []plainField{{core.SecretFieldAPIKey, key}}
	case APIKeyKindBasic:
		m, ok := prov.Method(MethodBasic)
		if !ok {
			return nil, newError(CodeInvalidArgument, "%s does not accept a username and password", prov.DisplayName)
		}
		user, pass := p.Fields["username"], p.Fields["password"]
		if up := m.GetBasic().GetUsernameParam(); up != "" {
			if user != "" {
				return nil, newError(CodeInvalidArgument, "%s takes the username from %s; fields.username is not accepted", prov.DisplayName, up)
			}
			user = params[up]
		}
		if err := onlyFields(p.Fields, "username", "password"); err != nil {
			return nil, err
		}
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

	// An integration whose events route by account (Twilio) delivers an
	// event only to connections that record its account, so that account
	// must be the one the provider names for this credential — never one the
	// user merely typed. Probe before anything is stored: a credential the
	// provider refuses is a form error, not a connection that silently
	// never receives anything.
	if prov.RoutesByAccount() {
		who, err := s.identifyPasted(ctx, prov, conn, plain)
		if err != nil {
			return nil, err
		}
		conn.ExternalAccountID = &who.ExternalAccountID
		if who.AccountLabel != "" {
			conn.AccountLabel = &who.AccountLabel
		}
		if who.SenderID != "" {
			conn.SenderID = &who.SenderID
		}
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
	if prov, ok := s.providers.Get(conn.IntegrationID); ok && conn.AuthKind == core.ConnectionAuthOAuth2 {
		if tok, err := s.tokens.Token(ctx, conn.UserID, conn.ID); err == nil {
			prov.revoke(ctx, s.doer, conn.Params, tok)
		}
	}
	if err := s.store.DeleteConnection(ctx, userID, id, userEvent(core.ConnectionEventDeleted, userID)); err != nil {
		return mapStoreErr(err)
	}
	s.tokens.Forget(id)
	return nil
}

// TestResult is the outcome of a probe.
type TestResult struct {
	OK           bool
	Probed       bool
	AccountLabel string
	ErrorClass   string
}

// Test runs the integration's identity probe with the live (refreshed if need
// be) credential and records the account label. An integration that declares
// no probe can only prove that the stored secret opens.
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
	prov, ok := s.providers.Get(conn.IntegrationID)
	if !ok || !prov.HasProbe() {
		return &TestResult{OK: true}, nil
	}
	auth, err := authenticatorFor(prov, conn)
	if err != nil {
		return nil, err
	}
	who, perr := prov.identify(ctx, s.doer, conn.Params, func(r *http.Request) error {
		return auth.Apply(r, secret, NewRedactor())
	})
	if perr != nil {
		var pe *probeError
		class := "unreachable"
		if errors.As(perr, &pe) {
			class = pe.class
		}
		return &TestResult{Probed: true, ErrorClass: class}, nil
	}
	if err := s.store.RecordTestResult(ctx, userID, id, who.AccountLabel, who.SenderID); err != nil {
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

// identifyPasted runs the integration's identity probe with a credential that
// has not been stored yet. The plaintext lives only in a vault.Secret for the
// call; a refusal is InvalidArgument (fix the form), anything else
// Unavailable (retry).
func (s *Service) identifyPasted(ctx context.Context, prov *Provider, conn *core.Connection, plain []plainField) (Identity, error) {
	var secret vault.Secret
	switch conn.AuthKind {
	case core.ConnectionAuthBasic:
		secret = vault.NewSecret([]byte(plain[0].value + "\x00" + plain[1].value))
	default:
		secret = vault.NewSecret([]byte(plain[0].value))
	}
	auth, err := authenticatorFor(prov, conn)
	if err != nil {
		return Identity{}, err
	}
	who, err := prov.identify(ctx, s.doer, conn.Params, func(r *http.Request) error {
		return auth.Apply(r, secret, NewRedactor())
	})
	if err == nil {
		return who, nil
	}
	var pe *probeError
	if errors.As(err, &pe) && pe.class == "unauthorized" {
		return Identity{}, newError(CodeInvalidArgument, "%s refused these credentials: check the account and the secret", prov.DisplayName)
	}
	return Identity{}, newError(CodeUnavailable, "could not reach %s to check these credentials; try again", prov.DisplayName)
}

// onlyFields refuses a pasted-credential field the method does not take, so a
// typo is an error rather than a silently dropped value.
func onlyFields(fields map[string]string, allowed ...string) error {
	for k := range fields {
		ok := false
		for _, a := range allowed {
			ok = ok || k == a
		}
		if !ok {
			return newError(CodeInvalidArgument, "fields.%s is not accepted", k)
		}
	}
	return nil
}

// StartOAuth begins a flow bound to p.UserID and returns the provider URL to
// send the user to.
func (s *Service) StartOAuth(ctx context.Context, p StartParams) (string, error) {
	return s.broker.Start(ctx, p)
}

// CompleteOAuth finishes a flow with the code the callback relayed. Only the
// user who started it can.
func (s *Service) CompleteOAuth(ctx context.Context, userID, state, code string) (*Completion, error) {
	return s.broker.Complete(ctx, CompleteParams{State: state, Code: code, UserID: userID})
}
