package cliauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/oauth2"
)

// ClientID is the reliant CLI's OAuth client id at the control plane.
const ClientID = "reliant-cli"

// The scopes reliant's CLI asks for.
const (
	// ScopeAPI calls reliant's API as the user.
	ScopeAPI = "reliant:api"
	// ScopeDaemon registers and connects a daemon as the user.
	ScopeDaemon = "daemon:connect"
)

// ErrNotLoggedIn reports no stored credential for a server.
var ErrNotLoggedIn = errors.New("not logged in")

// Login is one browser login.
type Login struct {
	// Server is the reliant API server the token will be presented to. The
	// authorization server is discovered from it.
	Server string
	// Scopes to request.
	Scopes []string
	// Name labels the minted token (<client>@<name>). A second login with the
	// same name replaces that token server-side.
	Name string
	// NonInteractive refuses to open a browser, returning
	// ErrInteractiveRequired instead.
	NonInteractive bool
	// Out receives the "open this URL" notice.
	Out io.Writer
	// OpenURL launches the browser; nil uses the platform opener.
	OpenURL func(string) error
	// Timeout bounds the wait for the browser; 0 means 5 minutes.
	Timeout time.Duration
	// HTTPClient performs discovery and the token exchange; nil uses bounded defaults.
	HTTPClient oauth2.HTTPDoer
}

// ErrInteractiveRequired is returned when NonInteractive is set: the flow
// needs a browser and a human.
var ErrInteractiveRequired = errors.New("interactive login required but running non-interactively")

// Run performs the login and returns the credential. It does not store it; see
// Store. The two are split because a daemon registration keeps its credential in
// the daemon store, not the CLI's.
func (l Login) Run(ctx context.Context) (credentials.Credential, error) {
	if l.NonInteractive {
		return credentials.Credential{}, ErrInteractiveRequired
	}
	meta, err := oauth2.DiscoverAuthorizationServer(ctx, l.HTTPClient, l.Server)
	if err != nil {
		if errors.Is(err, oauth2.ErrNoAuthorizationServer) {
			return credentials.Credential{}, fmt.Errorf("%s does not offer browser login (%w)\n"+
				"it must publish %s naming its control plane; for a self-hosted server, "+
				"mint a token there and pass it with RELIANT_TOKEN", l.Server, err, oauth2.AuthorizationServerMetadataPath)
		}
		return credentials.Credential{}, err
	}
	open := l.OpenURL
	if open == nil {
		open = OpenBrowser
	}
	extra := url.Values{}
	if l.Name != "" {
		extra.Set("device", l.Name)
	}
	tok, err := oauth2.LoopbackLogin{
		AuthorizeEndpoint: meta.AuthorizationEndpoint,
		TokenEndpoint:     meta.TokenEndpoint,
		ClientID:          ClientID,
		Scopes:            l.Scopes,
		Extra:             extra,
		OpenURL:           open,
		Out:               l.Out,
		Timeout:           l.Timeout,
		HTTPClient:        l.HTTPClient,
	}.Run(ctx)
	if err != nil {
		return credentials.Credential{}, fmt.Errorf("login via %s: %w", meta.Issuer, err)
	}
	now := time.Now().UTC()
	c := credentials.Credential{
		Token:     tok.AccessToken,
		Scopes:    strings.Fields(tok.Scope),
		CreatedAt: now,
		Issuer:    meta.Issuer,
	}
	if len(tok.AccessToken) > 13 {
		c.TokenPrefix = tok.AccessToken[:13]
	}
	if exp := tok.Expiry(now); !exp.IsZero() {
		c.ExpiresAt = &exp
	}
	return c, nil
}

// adapter is the Service over one credentials-file location and one set of
// outbound collaborators.
type adapter struct{ deps Deps }

// New returns the login adapter.
//
// forge:constructor
func New(deps Deps) Service {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &adapter{deps: deps}
}

// DepsFromEnv locates the shared credentials file the way `forge login` does:
// $FORGE_HOME, then $XDG_CONFIG_HOME, then the home directory
// (forge/pkg/credentials.Dirs). It is the only place this package reads the
// ambient environment.
func DepsFromEnv() Deps {
	home, _ := os.UserHomeDir()
	return Deps{Dirs: credentials.Dirs{
		ForgeHome:     os.Getenv("FORGE_HOME"),
		XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"),
		Home:          home,
	}}
}

func (a *adapter) Login(ctx context.Context, l Login) (credentials.Credential, error) {
	if l.HTTPClient == nil {
		l.HTTPClient = a.deps.HTTPClient
	}
	if l.OpenURL == nil {
		l.OpenURL = a.deps.OpenURL
	}
	return l.Run(ctx)
}

func (a *adapter) CredentialsPath() (string, error) { return a.deps.Dirs.Path() }

func (a *adapter) Store(server string, c credentials.Credential) (string, error) {
	path, err := a.CredentialsPath()
	if err != nil {
		return "", err
	}
	return path, credentials.Store(path, server, ClientID, c)
}

func (a *adapter) Lookup(server string) (credentials.Credential, string, error) {
	path, err := a.CredentialsPath()
	if err != nil {
		return credentials.Credential{}, "", err
	}
	c, err := credentials.Lookup(path, server, ClientID)
	if errors.Is(err, credentials.ErrNotFound) {
		return credentials.Credential{}, path, ErrNotLoggedIn
	}
	if err != nil {
		return credentials.Credential{}, path, err
	}
	if c.Expired(a.deps.Now()) {
		return credentials.Credential{}, path, fmt.Errorf("%w: the stored login expired at %s", ErrNotLoggedIn, c.ExpiresAt.Format(time.RFC3339))
	}
	return c, path, nil
}

func (a *adapter) Remove(server string) (bool, string, error) {
	path, err := a.CredentialsPath()
	if err != nil {
		return false, "", err
	}
	existed, err := credentials.Remove(path, server, ClientID)
	return existed, path, err
}

// The CLI's entry points: the adapter over the environment's credentials
// file. Commands call these; tests that must not touch the environment
// construct New(Deps{...}) directly.

// CredentialsPath is where the shared credentials file lives: the same file
// `forge login` writes.
func CredentialsPath() (string, error) { return New(DepsFromEnv()).CredentialsPath() }

// Store saves c as the reliant CLI's credential for server and returns the
// file path.
func Store(server string, c credentials.Credential) (string, error) {
	return New(DepsFromEnv()).Store(server, c)
}

// Lookup returns the stored credential for server, or ErrNotLoggedIn.
// An entry past its issuer-reported expiry counts as not logged in.
func Lookup(server string) (credentials.Credential, string, error) {
	return New(DepsFromEnv()).Lookup(server)
}

// Remove forgets the stored credential for server.
func Remove(server string) (bool, string, error) { return New(DepsFromEnv()).Remove(server) }

// OpenBrowser launches the platform's URL handler.
func OpenBrowser(target string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target).Start()
	case "linux":
		return exec.Command("xdg-open", target).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
	default:
		return fmt.Errorf("cannot open a browser on %s", runtime.GOOS)
	}
}

// AuthorizationServerEnv names the PUBLIC base URL of the control plane that
// issues this deployment's tokens. The API server publishes it as RFC 8414
// metadata so `reliant auth login --server <this API>` can find where to log
// in. It is public: the in-cluster RELIANT_CONTROL_PLANE_URL is a service
// address a browser cannot reach, so it cannot stand in.
const AuthorizationServerEnv = "RELIANT_AUTHORIZATION_SERVER"

// MetadataHandler serves RFC 8414 metadata naming issuer (the control plane)
// and its CLI login endpoints. Mount it at
// oauth2.AuthorizationServerMetadataPath on the API server. It returns nil
// when issuer is empty: a self-hosted server with no control plane offers no
// browser login, and the CLI says so on the 404 rather than guessing.
func MetadataHandler(issuer string) http.Handler {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if issuer == "" {
		return nil
	}
	doc := map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_ = json.NewEncoder(w).Encode(doc)
	})
}
