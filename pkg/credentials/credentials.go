// Package credentials is the ONE on-disk store of control-plane access tokens,
// shared by every CLI that talks to a Reliant Labs control plane — `forge
// login` and `reliant auth login` both write it, and every command of both
// reads it.
//
// ── ONE FILE, KEYED BY ENDPOINT, NO "CURRENT" ─────────────────────────
//
// The file maps a NORMALIZED endpoint URL, then a client id, to the credential
// that client holds for that endpoint:
//
//	{
//	  "version": 1,
//	  "credentials": {
//	    "https://admin.reliantapi.com": {
//	      "forge-cli": {"token": "rlat_…", "scopes": ["deploy:write", …], …}
//	    },
//	    "http://127.0.0.1:8090": {
//	      "forge-cli":   {"token": "rlat_…", …},
//	      "reliant-cli": {"token": "rlat_…", …}
//	    }
//	  }
//	}
//
// WHY THE CLIENT LEVEL. The endpoint is the server a token is PRESENTED to.
// Two CLIs can present tokens to the same origin with different scopes: in a
// dev stack one admin-server serves both forge's deploy API and reliant's
// proxied API. Keyed by endpoint alone, each CLI's login would silently
// replace the other's token with one that lacks its scopes. A client id is not
// a "current" selection; each CLI always reads its own.
//
// It answers exactly one question — "what credential does client C hold for
// endpoint X" — and deliberately never "which endpoint should I use". WHICH endpoint is
// always decided by the caller from something explicit and reviewable (an
// env's KCL declaration for forge, --server / RELIANT_SERVER for reliant).
// A stored "current" entry would make which server a command hits depend on
// what a previous command did, which is the stateful-context model both CLIs
// have deleted.
//
// ── WHERE IT LIVES ────────────────────────────────────────────────────
//
// Path resolves as:
//
//	$FORGE_HOME/credentials.json            when FORGE_HOME is set
//	$XDG_CONFIG_HOME/forge/credentials.json when XDG_CONFIG_HOME is set
//	~/.config/forge/credentials.json        otherwise, on every OS
//
// ~/.config rather than os.UserConfigDir: on macOS that is ~/Library/
// Application Support, a path with a space in it that users cannot type and
// that no other CLI tool uses. A file two CLIs share — and that a human is
// told to inspect or delete — belongs where CLI config conventionally lives.
// FORGE_HOME is honoured first so a test or a sandboxed CI job can redirect
// the file without touching a developer's real credentials.
//
// DirsFromEnv (env.go) reads those three inputs from the process — the one
// place this package touches the environment, exempted from internal/pkgguard
// by name. Every CLI that shares the file calls it rather than reading the
// variables itself, which is what makes it one file. Dirs stays a plain value
// for callers (tests, a sandbox) that must place the file explicitly.
//
// The directory is named for forge because forge OWNS the format: this package
// is its only reader and writer, and reliant imports it rather than
// re-implementing it. A second implementation of the same file is how two
// tools end up disagreeing about its shape.
//
// ── WHAT IS STORED ────────────────────────────────────────────────────
//
// The token is opaque: nothing here parses it. Scopes, expiry and client id
// are the issuer's answer at login time, stored for DISPLAY and for the
// "expired, log in again" hint — never for an authorization decision, which
// only the server can make.
//
//forge:exclude-contract: a pkg/ file-format library: one on-disk format over a caller-supplied path, stdlib only, one implementation
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// FileName is the credentials file's base name.
const FileName = "credentials.json"

// formatVersion is the on-disk format version. A file with a higher version
// was written by a newer binary; refusing it beats silently dropping fields
// that binary relies on.
const formatVersion = 1

// Credential is what one endpoint's login produced.
type Credential struct {
	// Kind distinguishes human identities from delegated capabilities.
	Kind string `json:"kind,omitempty"`
	// Token is the bearer credential. Opaque; never logged.
	Token string `json:"token"`
	// TokenPrefix is the issuer's display prefix (e.g. "rlat_A3f9Kd2p") —
	// safe to print, and how a human matches this entry to a token listed
	// in the web UI.
	TokenPrefix string `json:"token_prefix,omitempty"`
	// Scopes the issuer granted, as it reported them.
	Scopes []string `json:"scopes,omitempty"`
	// ExpiresAt is when the issuer said the token expires; nil when it did
	// not say. Informational — the server decides.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// CreatedAt is when this entry was written.
	CreatedAt time.Time `json:"created_at"`
	// Issuer is the authorization server that minted it, when that differs
	// from the endpoint it is presented to (reliant logs in at the control
	// plane and presents the token to its API server). Informational.
	Issuer string `json:"issuer,omitempty"`
	// AccountID and AccountEmail name the account the login signed in as,
	// as the issuer reported them (the token reply's account_id and
	// account_email extension fields; AccountID is the issuer's stable
	// external id for the person). Empty when the issuer did not say — an
	// older issuer, or a token pasted by hand. Informational: they let a
	// client show who it is signed in as, and tell two logins of one
	// person from logins of two people, without a network call. The server
	// decides who the token acts as.
	AccountID    string `json:"account_id,omitempty"`
	AccountEmail string `json:"account_email,omitempty"`
	// Claimable is set only on an entry stored under a claimable-deploy client
	// id (forge-cli-claimable…): the anonymous deploy this token belongs to.
	// It sits beside the token because the claim link is as sensitive as the
	// token is (whoever opens it first owns the deploy), and the file is
	// already 0600. Nil on every ordinary login.
	Claimable *Claimable `json:"claimable,omitempty"`
}

// Claimable is the anonymous deploy a claimable-deploy credential belongs to.
// Everything here is what `forge env status --claimable` reprints, so it is
// stored rather than re-fetched: the agent that holds it has no account to ask
// the control plane with.
type Claimable struct {
	// ClaimURL is the link a human opens to take the deploy over.
	ClaimURL string `json:"claim_url"`
	// OrgID is the anonymous user's personal org.
	OrgID string `json:"org_id,omitempty"`
	// URL is the deploy's live address, as last observed. Empty until the
	// first deploy has gone live.
	URL string `json:"url,omitempty"`
	// ExpiresAt is when the deploy goes offline; PurgeAfter when it is
	// deleted (and when the token stops being accepted).
	ExpiresAt  time.Time `json:"expires_at"`
	PurgeAfter time.Time `json:"purge_after"`
}

// Expired reports whether the issuer-reported expiry has passed.
func (c Credential) Expired(now time.Time) bool {
	return c.ExpiresAt != nil && !c.ExpiresAt.After(now)
}

// File is the whole store.
type File struct {
	Version int `json:"version"`
	// Credentials is endpoint → client id → credential.
	Credentials map[string]map[string]Credential `json:"credentials"`
}

// ErrNotFound reports that the file holds no credential for an endpoint and
// client.
var ErrNotFound = errors.New("credentials: no stored credential for this endpoint")

// Dirs are the inputs to the file's location: the values of $FORGE_HOME and
// $XDG_CONFIG_HOME, and the user's home directory. A caller fills them from
// its environment; see the package doc for the precedence.
type Dirs struct {
	ForgeHome     string
	XDGConfigHome string
	Home          string
}

// Path returns where the credentials file lives for these Dirs.
func (d Dirs) Path() (string, error) {
	if home := strings.TrimSpace(d.ForgeHome); home != "" {
		return filepath.Join(home, FileName), nil
	}
	if xdg := strings.TrimSpace(d.XDGConfigHome); xdg != "" {
		return filepath.Join(xdg, "forge", FileName), nil
	}
	if strings.TrimSpace(d.Home) == "" {
		return "", errors.New("credentials: no home directory to place the credentials file in")
	}
	return filepath.Join(d.Home, ".config", "forge", FileName), nil
}

// Normalize turns an endpoint URL into its store key: lower-cased scheme and
// host, the port only when it is not the scheme's default, and nothing else —
// no path, no query, no trailing slash.
//
// Two spellings of one server must land on one entry, or a user who logged in
// to "https://Admin.example.com/" is told they are not logged in to
// "https://admin.example.com". The path is dropped because a credential is
// issued by an ORIGIN; two paths on one origin are one issuer.
func Normalize(endpoint string) (string, error) {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return "", errors.New("credentials: endpoint is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("credentials: parse endpoint %q: %w", endpoint, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("credentials: endpoint %q must be an http(s) URL", endpoint)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("credentials: endpoint %q has no host", endpoint)
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") { // IPv6 literal
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}

// Load reads the file at path. A missing file is an empty store, not an
// error: "never logged in" is the ordinary first state.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{Version: formatVersion, Credentials: map[string]map[string]Credential{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("credentials: read %s: %w", path, err)
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		// A corrupt file is an ERROR, not "logged out": the user did log
		// in, and telling them otherwise sends them round a loop that
		// rewrites — and so destroys — every other endpoint's entry.
		return nil, fmt.Errorf("credentials: parse %s: %w", path, err)
	}
	if f.Version > formatVersion {
		return nil, fmt.Errorf("credentials: %s has format version %d; this binary understands up to %d — upgrade it",
			path, f.Version, formatVersion)
	}
	f.Version = formatVersion
	if f.Credentials == nil {
		f.Credentials = map[string]map[string]Credential{}
	}
	return &f, nil
}

// Save writes f to path ATOMICALLY with mode 0600 in a 0700 directory.
//
// Atomic (temp file + rename in the same directory) because two CLIs write
// this file and a torn write would lose every endpoint's credential, not just
// the one being updated. The modes matter because every value is a bearer
// credential: a world-readable file is a leak on any shared machine.
func Save(path string, f *File) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("credentials: create %s: %w", dir, err)
	}
	f.Version = formatVersion
	if f.Credentials == nil {
		f.Credentials = map[string]map[string]Credential{}
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("credentials: encode: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*.json")
	if err != nil {
		return fmt.Errorf("credentials: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("credentials: chmod temp file: %w", err)
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("credentials: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("credentials: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("credentials: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("credentials: replace %s: %w", path, err)
	}
	return nil
}

// Get returns the credential client holds for endpoint, or ErrNotFound.
func (f *File) Get(endpoint, client string) (Credential, error) {
	key, err := Normalize(endpoint)
	if err != nil {
		return Credential{}, err
	}
	c, ok := f.Credentials[key][client]
	if !ok || strings.TrimSpace(c.Token) == "" {
		return Credential{}, ErrNotFound
	}
	return c, nil
}

// Put stores c as client's credential for endpoint, replacing any previous
// one for that pair and leaving every other pair untouched.
func (f *File) Put(endpoint, client string, c Credential) error {
	key, err := Normalize(endpoint)
	if err != nil {
		return err
	}
	if strings.TrimSpace(client) == "" {
		return errors.New("credentials: a client id is required")
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("credentials: refusing to store an empty token")
	}
	if f.Credentials == nil {
		f.Credentials = map[string]map[string]Credential{}
	}
	if f.Credentials[key] == nil {
		f.Credentials[key] = map[string]Credential{}
	}
	f.Credentials[key][client] = c
	return nil
}

// Delete removes client's credential for endpoint and reports whether there
// was one. An endpoint left with no clients is dropped.
func (f *File) Delete(endpoint, client string) (bool, error) {
	key, err := Normalize(endpoint)
	if err != nil {
		return false, err
	}
	_, ok := f.Credentials[key][client]
	delete(f.Credentials[key], client)
	if len(f.Credentials[key]) == 0 {
		delete(f.Credentials, key)
	}
	return ok, nil
}

// Endpoints lists the stored endpoints, sorted.
func (f *File) Endpoints() []string {
	out := make([]string, 0, len(f.Credentials))
	for k := range f.Credentials {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Lookup is the one-call read: client's credential for endpoint in the file
// at path, or ErrNotFound.
func Lookup(path, endpoint, client string) (Credential, error) {
	f, err := Load(path)
	if err != nil {
		return Credential{}, err
	}
	return f.Get(endpoint, client)
}

// Update serializes a credential-file read/modify/write transaction across all
// participating processes. The callback must not perform network operations.
func Update(path string, mutate func(*File) error) error {
	unlock, err := lockMutation(path)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := Load(path)
	if err != nil {
		return err
	}
	before, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := mutate(f); err != nil {
		return err
	}
	after, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if string(before) == string(after) {
		return nil
	}
	return Save(path, f)
}

// Store is the one-call write: put c for (endpoint, client) into the file at
// path and save it, preserving every other entry.
func Store(path, endpoint, client string, c Credential) error {
	return Update(path, func(f *File) error { return f.Put(endpoint, client, c) })
}

// Remove is the one-call delete: drop (endpoint, client) from the file at
// path. Reports whether an entry existed. Every other entry is untouched.
func Remove(path, endpoint, client string) (bool, error) {
	return removeMatching(path, endpoint, client, "", false)
}

// RemoveRevoked removes only the exact session the caller revoked. A newer
// login or a different namespace is never erased by an older logout.
func RemoveRevoked(path, endpoint, client, revokedToken string) (bool, error) {
	return removeMatching(path, endpoint, client, revokedToken, true)
}

func lockMutation(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	return func() { _ = lock.Unlock() }, nil
}

func removeMatching(path, endpoint, client, token string, conditional bool) (bool, error) {
	unlock, err := lockMutation(path)
	if err != nil {
		return false, err
	}
	defer unlock()
	f, err := Load(path)
	if err != nil {
		return false, err
	}
	if conditional {
		current, err := f.Get(endpoint, client)
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if current.Token != token {
			return false, errors.New("credential changed during logout; newer login retained")
		}
	}
	existed, err := f.Delete(endpoint, client)
	if err != nil || !existed {
		return existed, err
	}
	return true, Save(path, f)
}
