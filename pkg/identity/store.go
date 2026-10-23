package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// KeyStore is where credentials the library GENERATES live, and it is also the
// library's memory of them: Zitadel returns a machine key's private half, a
// personal access token and a client secret exactly once, so the store holds
// the ONLY copy. A pass that finds an entry missing issues a new credential
// rather than failing, which is what makes an emptied store recoverable by
// simply running again.
//
// Names are opaque, stable strings such as "machine-key:ops-automation". Dev
// maps them to files (DirKeyStore); the control plane maps them to OpenBao
// paths.
type KeyStore interface {
	// Load returns the stored value, or (nil, nil) when none is stored.
	Load(ctx context.Context, name string) ([]byte, error)
	// Save durably replaces the value. An error means the value was NOT
	// stored; the library then discards what it had just issued and keeps the
	// previous credential.
	Save(ctx context.Context, name string, value []byte) error
}

// Store entry names.
const (
	keyMachinePrefix = "machine-key:"
	keyBrokerPrefix  = "broker-token:"
	keySecretPrefix  = "client-secret:"
	keySMTPDigest    = "smtp-password-digest"
)

// MachineKeyEntry is the KeyStore name of a machine user's key document.
func MachineKeyEntry(username string) string { return keyMachinePrefix + username }

// BrokerTokenEntry is the KeyStore name of a broker account's token record.
func BrokerTokenEntry(username string) string { return keyBrokerPrefix + username }

// ClientSecretEntry is the KeyStore name of a web application's client secret.
func ClientSecretEntry(app string) string { return keySecretPrefix + app }

// MemKeyStore is an in-memory KeyStore for tests and one-shot tools.
type MemKeyStore struct {
	mu sync.Mutex
	m  map[string][]byte
	// SaveErr, when set, makes every Save fail; tests use it to prove a failed
	// delivery leaves the previous credential intact.
	SaveErr error
}

func (s *MemKeyStore) Load(_ context.Context, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[name]
	if !ok {
		return nil, nil
	}
	return bytes.Clone(v), nil
}

func (s *MemKeyStore) Save(_ context.Context, name string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.SaveErr != nil {
		return s.SaveErr
	}
	if s.m == nil {
		s.m = map[string][]byte{}
	}
	s.m[name] = bytes.Clone(value)
	return nil
}

// Delete drops an entry (test helper: simulates an emptied store).
func (s *MemKeyStore) Delete(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, name)
}

// DirKeyStore keeps one 0600 file per entry under Dir. It is the dev store:
// the directory is gitignored and holds nothing a throwaway local instance
// would not.
type DirKeyStore struct{ Dir string }

func (d DirKeyStore) path(name string) string {
	return filepath.Join(d.Dir, url.PathEscape(name))
}

func (d DirKeyStore) Load(_ context.Context, name string) ([]byte, error) {
	raw, err := os.ReadFile(d.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", d.path(name), err)
	}
	return raw, nil
}

func (d DirKeyStore) Save(_ context.Context, name string, value []byte) error {
	return writeFileAtomic(d.path(name), value, 0o600)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Outputs is the write-only publication of what consumers of the instance
// need. Publish is called once at the end of every successful pass, so an
// implementation must be idempotent; it should treat Output's secret fields
// (which json omits) as credentials.
type Outputs interface {
	Publish(ctx context.Context, out Output) error
}

// OutputsFunc adapts a function to Outputs.
type OutputsFunc func(ctx context.Context, out Output) error

func (f OutputsFunc) Publish(ctx context.Context, out Output) error { return f(ctx, out) }

// Output is everything a consumer of the instance is configured from.
type Output struct {
	// Issuer is the `iss` the instance mints; the validator's jwt_issuer.
	Issuer string `json:"issuer"`
	// OrgID is the organization the content lives in.
	OrgID string `json:"org_id"`
	// ProjectID is the project id, which is also the token audience.
	ProjectID string `json:"project_id"`
	// Audience is the value a validator must enforce as `aud` (ProjectID).
	Audience string `json:"audience"`
	// RolesClaim is the namespaced claim the project's roles are asserted
	// under: a map of role key to org id to org domain, not a flat list.
	RolesClaim string `json:"roles_claim"`
	// Scopes are what a client must request for an access token that carries
	// the audience and the roles claim.
	Scopes []string `json:"scopes"`
	// JWKS is a snapshot of the instance's public signing keys, for a validator
	// that must start while the issuer is unreachable.
	JWKS json.RawMessage `json:"jwks,omitempty"`
	// Apps are the registered applications by name.
	Apps map[string]AppOutput `json:"apps,omitempty"`
	// Machine are the machine users by username.
	Machine map[string]MachineOutput `json:"machine,omitempty"`
}

// AppOutput is one application's published values.
type AppOutput struct {
	AppID          string   `json:"app_id"`
	ClientID       string   `json:"client_id"`
	RedirectURIs   []string `json:"redirect_uris,omitempty"`
	PostLogoutURIs []string `json:"post_logout_uris,omitempty"`
	LoginURI       string   `json:"login_uri,omitempty"`
	// ClientSecret is set for a web app whose secret is in the KeyStore.
	ClientSecret string `json:"-"`
	// BrokerUser / BrokerToken / BrokerTokenExpires describe an
	// AppNativeBroker's account and its CURRENT token. The token is the
	// idp_broker_token the product's server runs with.
	BrokerUser         string    `json:"broker_user,omitempty"`
	BrokerToken        string    `json:"-"`
	BrokerTokenID      string    `json:"broker_token_id,omitempty"`
	BrokerTokenExpires time.Time `json:"broker_token_expires,omitempty"`
}

// MachineOutput is one machine user's published values. The key itself is in
// the KeyStore, not here.
type MachineOutput struct {
	UserID       string    `json:"user_id"`
	KeyID        string    `json:"key_id,omitempty"`
	KeyExpires   time.Time `json:"key_expires,omitempty"`
	TokenID      string    `json:"token_id,omitempty"`
	TokenExpires time.Time `json:"token_expires,omitempty"`
	// Token is the machine user's current personal access token, if it has one.
	Token string `json:"-"`
}

// FileOutputs writes the non-secret Output as JSON to Path, atomically, 0644.
// It is the dev publication: `.forge/identity/<name>.json`.
type FileOutputs struct{ Path string }

func (f FileOutputs) Publish(_ context.Context, out Output) error {
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(f.Path, append(raw, '\n'), 0o644)
}
