// Package cloud resolves the two things forge needs to talk to a hosted
// control plane, and keeps them apart on purpose:
//
//   - the ENDPOINT, which is a per-environment fact declared in that
//     environment's KCL (forge.ControlPlane) and lives in git.
//   - the CREDENTIAL, which is an opaque bearer string that must never
//     live in git, resolved at call time from a flag, an environment
//     variable, the shared credentials file `forge login` writes
//     (forge/pkg/credentials) keyed by THAT endpoint, or a credential helper
//     the host application names (forge/pkg/cloudcred).
//
// This split mirrors internal/secrets: KCL emits the DECLARATION, Go
// resolves the VALUE. It is also why there is no `forge context use`.
// A stateful "current endpoint" would make the answer to "which server
// did that command hit" depend on machine-local state that never appears
// in a diff; deriving it from the env argument makes the answer a
// property of the repository instead.
//
// Nothing here imports control-plane or reliant, and nothing may. The
// endpoint is an arbitrary URL and the credential an opaque string — the
// same relationship forge's External deploy target has with flyctl, which
// needs FLY_API_TOKEN without forge linking Fly's SDK.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: Client (client.go) is an outbound HTTP adapter owned by the CLI command layer; converting it to a contract.go adapter is tracked as CONTRACTS follow-up F2
//forge:exclude-contract: CLI-internal credential/endpoint resolution plus the control-plane HTTP client the forge CLI builds per command
package cloud

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// DefaultTokenEnv is the environment variable forge reads a control-plane
// credential from when an environment's ControlPlane declaration does not
// name a different one.
const DefaultTokenEnv = "FORGE_CONTROL_PLANE_TOKEN"

// CredentialSource says where a resolved credential came from. Commands
// print it so "which credential did that use" never requires guessing —
// the single most common confusion when a CI run and a laptop disagree.
type CredentialSource string

// The places a credential can come from, in the precedence order resolution
// tries them. An explicit flag beats the environment, the environment beats
// the credentials file on disk, and the file beats a host application's
// session. That order is what makes a CI run overridable, a human's own
// `forge login` deliberate, and a signed-in host the ambient default rather
// than a thing that silently wins.
const (
	SourceFlag  CredentialSource = "flag"             // --token
	SourceEnv   CredentialSource = "env"              // the declared token_env
	SourceLogin CredentialSource = "credentials file" // written by `forge login`
	// SourceHelper is a token the credential helper named by
	// $FORGE_CREDENTIAL_HELPER minted from a host application's session
	// (pkg/cloudcred). forge does not know which application; Credential.From
	// carries the helper's own description of it.
	SourceHelper CredentialSource = "credential helper"
)

// Credential is a resolved bearer token plus where it came from.
type Credential struct {
	Kind   string
	Token  string
	Source CredentialSource
	// From is the human-readable origin: the flag name, the env var name,
	// the credentials file path, or the helper's description of the
	// session it used. Safe to print; never the token itself.
	From string
	// ExpiresAt is when the token stops being accepted, when known: a
	// helper's tokens and a stored login carry one; nil means unknown.
	ExpiresAt *time.Time
	// Scopes the issuer reported granting, when known (a stored login records
	// them). Informational — the server decides.
	Scopes []string
}

// Identity names the credential WITHOUT its secret: the issuer's display
// prefix for an `rlat_` access token ("rlat_A3f9Kd2p…" — what the console's
// token list shows, so a human can match it), else only its length.
//
// Every human-facing line that says WHICH credential was used prints this,
// never Token. An agent or a person who cannot tell which credential failed
// opens the credentials file to find out, and the file holds the secret —
// which is how a forge credential came to be pasted into a session log.
func (c Credential) Identity() string {
	return TokenIdentity(c.Token)
}

// TokenIdentity is Credential.Identity for a bare token string.
func TokenIdentity(token string) string {
	if p := displayPrefix(token); strings.HasPrefix(p, "rlat_") {
		return p + "…"
	}
	if token == "" {
		return "(none)"
	}
	return fmt.Sprintf("(a %d-character token; not an rlat_ access token)", len(token))
}

// ErrNoCredential is returned when no credential could be resolved. Use
// errors.Is to detect it; the message is already actionable (it names the
// remedy for this machine — `forge login`, or signing in to the host
// application — and the environment variable), so callers should surface it
// as-is rather than wrapping it in a generic auth failure.
var ErrNoCredential = errors.New("no control-plane credential")

// helperTimeout bounds one run of the credential helper. A helper usually
// answers from its own cache in milliseconds; when it has to mint it makes
// one round trip. Anything slower is broken, and a hung helper must not hang
// a deploy.
const helperTimeout = 60 * time.Second

// helperReuseMargin is how close to its expiry a helper token may be and still
// be reused within this process. Past it the helper is asked again — it, not
// forge, decides whether that means a cache hit or a fresh mint.
const helperReuseMargin = time.Minute

// ResolveCredential returns the bearer credential for ep, in this
// precedence order:
//
//  1. flagToken              — an explicit --token on the command line
//  2. os.Getenv(ep.TokenEnv) — the env var the environment DECLARED
//  3. the credentials file   — what `forge login` stored FOR ep.URL
//  4. the credential helper  — $FORGE_CREDENTIAL_HELPER, a host application
//     that holds the user's session (Reliant sets it), minting a short-lived
//     token for ep on demand
//
// WHY THIS ORDER. It runs most-explicit to most-ambient. A flag is typed
// for one command and can mean nothing else, so it must win — that is
// what makes a one-off override possible at all, and what makes a bug
// report reproducible ("run it with --token X"). The env var comes next
// because CI is where it is used: a pipeline has no browser and no
// persistent home directory, so the variable IS its credential, and it
// must beat any file that happens to exist in a cached runner image. The
// file comes before the helper because a human who ran `forge login` chose
// that credential for that endpoint deliberately, and a host application
// signed in as somebody else must not displace it. The helper is last
// because it is the most ambient of all: it is simply "whoever this machine
// is signed in to the host as" — the right DEFAULT, and the wrong override.
//
// An EXPIRED `forge login` is not a dead end when a helper is configured:
// the user is still signed in to the host, so resolution falls through to it
// rather than demanding a second login. Without a helper it is reported as
// expired, because then `forge login` really is the fix.
//
// The file lookup is keyed by the endpoint. A login to staging can never be
// presented to prod: a prod command finds no entry and says so, instead of
// sending staging's token to prod's server and failing with an
// indistinguishable 401. The helper is asked FOR ep, and refuses an endpoint
// its session does not belong to — the endpoint comes from a project's KCL,
// and a repository is not a trusted party.
func ResolveCredential(flagToken string, ep Endpoint) (Credential, error) {
	if c, ok := claimableCredentialFor(ep); ok {
		return c, nil
	}
	if t := strings.TrimSpace(flagToken); t != "" {
		return Credential{Token: t, Source: SourceFlag, From: "--token"}, nil
	}
	tokenEnv := ep.TokenEnv
	if tokenEnv == "" {
		tokenEnv = DefaultTokenEnv
	}
	if t := strings.TrimSpace(os.Getenv(tokenEnv)); t != "" {
		return Credential{Token: t, Source: SourceEnv, From: tokenEnv}, nil
	}

	helperValue := os.Getenv(cloudcred.HelperEnv)
	hasHelper := strings.TrimSpace(helperValue) != ""

	path, err := CredentialsPath()
	if err != nil {
		return Credential{}, err
	}
	expiredLogin := ""
	stored, lookupErr := lookupCredential(path, ep.URL)
	switch {
	case lookupErr == nil && !stored.Expired(time.Now()):
		return Credential{Kind: stored.Kind, Token: stored.Token, Source: SourceLogin, From: path,
			ExpiresAt: stored.ExpiresAt, Scopes: stored.Scopes}, nil
	case lookupErr == nil:
		expiredLogin = fmt.Sprintf("the forge login for %s stored in %s expired at %s",
			ep.URL, path, stored.ExpiresAt.Format(time.RFC3339))
		if !hasHelper {
			return Credential{}, fmt.Errorf("%w\n%s\nfix: %s", ErrNoCredential, expiredLogin, loginHint(ep))
		}
	case !errors.Is(lookupErr, credentials.ErrNotFound):
		// A file that exists but cannot be read is an ERROR, not "not
		// logged in": the user did log in, and telling them otherwise
		// sends them round a loop that cannot fix it.
		return Credential{}, lookupErr
	}

	if hasHelper {
		argv, err := cloudcred.ParseCommand(helperValue)
		if err != nil {
			return Credential{}, fmt.Errorf("%w for %s: %v", ErrNoCredential, ep.URL, err)
		}
		cred, err := helperCredential(argv, ep)
		if err != nil {
			return Credential{}, helperFailure(ep, tokenEnv, expiredLogin, err)
		}
		return cred, nil
	}

	return Credential{}, fmt.Errorf(
		"%w for %s\nforge needs a bearer credential to reach the hosted control plane.\n"+
			"fix, in the order forge checks them:\n"+
			"    --token <token>        explicit, wins over everything (one-off / debugging)\n"+
			"    export %s=<token>      for CI — a pipeline has no browser\n"+
			"    %s        for a human — opens a browser and stores the credential\n"+
			"no account? `forge env deploy <env> --claimable` publishes a deploy anyone can claim within 72h",
		ErrNoCredential, ep.URL, tokenEnv, loginHint(ep))
}

// helperFailure words a helper that produced no token. A helper's REFUSAL is
// the host's own advice ("sign in to …") and is shown verbatim: under a host,
// sending the user to `forge login` would be the wrong fix — they are meant to
// be signed in once, there. CI's route is named too, because a pipeline that
// inherited a helper variable still has no browser.
func helperFailure(ep Endpoint, tokenEnv, expiredLogin string, err error) error {
	var b strings.Builder
	var refusal *cloudcred.HelperError
	if errors.As(err, &refusal) {
		fmt.Fprintf(&b, "%v for %s\n%s", ErrNoCredential, ep.URL, refusal.Message)
	} else {
		fmt.Fprintf(&b, "%v for %s\nthe credential helper ($%s) did not produce one: %v",
			ErrNoCredential, ep.URL, cloudcred.HelperEnv, err)
	}
	if expiredLogin != "" {
		fmt.Fprintf(&b, "\n(%s)", expiredLogin)
	}
	fmt.Fprintf(&b, "\nfor CI or a one-off, a token bypasses the helper: export %s=<token>, or --token <token>", tokenEnv)
	return &helperResolveError{msg: b.String(), cause: err}
}

// helperResolveError is ErrNoCredential (errors.Is) whose message is the
// helper's, and which still unwraps to the helper's own error (errors.As
// reaches a *cloudcred.HelperError for its code).
type helperResolveError struct {
	msg   string
	cause error
}

func (e *helperResolveError) Error() string   { return e.msg }
func (e *helperResolveError) Unwrap() []error { return []error{ErrNoCredential, e.cause} }

// helperMemo remembers the helper's token per (endpoint, helper command) for
// the life of this process. One forge command resolves its credential in
// several places (the deploy, the registry login, the rollout wait), and the
// helper — a separate process — need not run for each. It is only a
// per-process memo: across processes, caching is the helper's job, because
// the helper is what knows when its session changed.
var helperMemo = struct {
	sync.Mutex
	entries map[string]Credential
}{entries: map[string]Credential{}}

// helperCredential runs the helper for ep, or returns this process's memo of
// its last answer while that is not about to expire.
func helperCredential(argv []string, ep Endpoint) (Credential, error) {
	return helperCredentialFor(argv, ep, LoginScopes())
}

// helperMemoKey is the memo slot for one (endpoint, helper, scopes) request.
// The default request (LoginScopes) keeps the historical key, so
// forgetHelperCredential drops exactly the base credential.
func helperMemoKey(endpointKey string, argv, scopes []string) string {
	key := endpointKey + "\x00" + cloudcred.FormatCommand(argv)
	if strings.Join(scopes, " ") != strings.Join(LoginScopes(), " ") {
		key += "\x00" + strings.Join(scopes, " ")
	}
	return key
}

// helperCredentialFor is helperCredential asking the helper for scopes.
func helperCredentialFor(argv []string, ep Endpoint, scopes []string) (Credential, error) {
	endpointKey, err := credentials.Normalize(ep.URL)
	if err != nil {
		return Credential{}, err
	}
	memoKey := helperMemoKey(endpointKey, argv, scopes)

	helperMemo.Lock()
	defer helperMemo.Unlock()
	if cached, ok := helperMemo.entries[memoKey]; ok {
		tok := cloudcred.Token{ExpiresAt: cached.ExpiresAt}
		if !tok.Expired(time.Now(), helperReuseMargin) {
			return cached, nil
		}
		delete(helperMemo.entries, memoKey)
	}

	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()
	tok, err := cloudcred.Exec(ctx, argv, cloudcred.Request{
		Endpoint: endpointKey,
		Scopes:   scopes,
	})
	if err != nil {
		return Credential{}, err
	}
	from := strings.TrimSpace(tok.Source)
	if from == "" {
		from = "$" + cloudcred.HelperEnv + " (" + argv[0] + ")"
	}
	cred := Credential{Token: tok.Token, Source: SourceHelper, From: from, ExpiresAt: tok.ExpiresAt, Scopes: tok.Scopes}
	helperMemo.entries[memoKey] = cred
	return cred, nil
}

// refreshedCredential asks the credential helper again after the control plane
// refused the one this client holds, and adopts the answer when it is a
// DIFFERENT token. ok is false when there is nothing new to try: the client's
// credential did not come from the helper (a flag, an env var or a login the
// user chose is never swapped for something else), the helper is gone, or it
// handed back the same token — in which case a retry could only repeat the
// refusal.
//
// The helper, not forge, knows whether its session can mint a replacement: it
// answers from its own cache when that is still good and mints when it is not.
func (c *Client) refreshedCredential() (Credential, bool) {
	cur := c.credential()
	if cur.Source != SourceHelper {
		return Credential{}, false
	}
	helperValue := os.Getenv(cloudcred.HelperEnv)
	if strings.TrimSpace(helperValue) == "" {
		return Credential{}, false
	}
	argv, err := cloudcred.ParseCommand(helperValue)
	if err != nil {
		return Credential{}, false
	}
	forgetHelperCredential(argv, c.Endpoint)
	fresh, err := helperCredential(argv, c.Endpoint)
	if err != nil || fresh.Token == "" || fresh.Token == cur.Token {
		return Credential{}, false
	}
	c.credMu.Lock()
	c.Credential = fresh
	c.credMu.Unlock()
	return fresh, true
}

// forgetHelperCredential drops this process's memo of the helper's token for
// ep, so the next helperCredential asks the helper rather than replaying a
// token the control plane has just refused.
func forgetHelperCredential(argv []string, ep Endpoint) {
	endpointKey, err := credentials.Normalize(ep.URL)
	if err != nil {
		return
	}
	helperMemo.Lock()
	defer helperMemo.Unlock()
	delete(helperMemo.entries, endpointKey+"\x00"+cloudcred.FormatCommand(argv))
}

// CredentialOrder is the resolution order in one line, for help text and
// errors that explain where a credential comes from.
const CredentialOrder = "--token, then the env's declared token_env, then what `forge login` stored, " +
	"then the credential helper $" + cloudcred.HelperEnv + " (a host application's session — Reliant sets it)"

// SignInHint is the "authenticate and retry" remedy for THIS process. Under a
// host application that provides a credential helper, the user is meant to be
// signed in once, to the host — so the remedy is the host's sign-in, not a
// second login to forge. A standalone forge says `forge login`.
func SignInHint() string {
	if strings.TrimSpace(os.Getenv(cloudcred.HelperEnv)) != "" {
		return "sign in to the application that provides forge's credentials ($" + cloudcred.HelperEnv + ")"
	}
	return "`forge login`"
}

// loginHint is the `forge login` invocation that covers ep. An endpoint an
// env declared is covered by a bare `forge login`, which logs into every
// control plane the project declares — login never takes an env. Anything
// else is named with --endpoint.
func loginHint(ep Endpoint) string {
	if ep.Env != "" {
		return "forge login"
	}
	return "forge login --endpoint " + ep.URL
}

// CredentialsPath is where the shared credentials file lives, from
// $FORGE_HOME, $XDG_CONFIG_HOME and the home directory. forge/pkg/credentials
// owns both the precedence and the lookup (DirsFromEnv), so forge and every
// other CLI sharing the file resolve it the same way.
func CredentialsPath() (string, error) {
	return credentials.PathFromEnv()
}
